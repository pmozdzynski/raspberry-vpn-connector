package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	runDir       = "/run/vpn-connector"
	pidFile      = runDir + "/openconnect.pid"
	stateFile    = runDir + "/state.json"
	logFile      = runDir + "/openconnect.log"
	connectTimeout = 3 * time.Minute
)

const (
	VPNPhaseDisconnected = "disconnected"
	VPNPhaseConnecting   = "connecting"
	VPNPhaseNeedInput    = "need_input"
	VPNPhaseConnected    = "connected"
	VPNPhaseError        = "error"
)

type VPNState struct {
	Connected   bool   `json:"connected"`
	Phase       string `json:"phase"`
	ProfileID   string `json:"profile_id,omitempty"`
	ProfileName string `json:"profile_name,omitempty"`
	TunIface    string `json:"tun_iface,omitempty"`
	ServerURL   string `json:"server_url,omitempty"`
	Since       string `json:"since,omitempty"`
	LastError   string `json:"last_error,omitempty"`
	InputPrompt string `json:"input_prompt,omitempty"`
	InputKind   string `json:"input_kind,omitempty"`
}

type vpnConn struct {
	cmd          *exec.Cmd
	stdin        io.WriteCloser
	profile      VPNProfile
	stopCh       chan struct{}
	passwordSent bool
	inputSent    bool
	parentDeadAt time.Time
}

var (
	vpnMu            sync.Mutex
	activeVPNSession *vpnConn
	autoReconnectMu  sync.Mutex
	autoReconnecting bool
)

func GetVPNState() VPNState {
	data, err := os.ReadFile(stateFile)
	if err != nil {
		if isOpenConnectRunning() {
			return VPNState{Connected: true, Phase: VPNPhaseConnected, TunIface: detectTunInterface()}
		}
		return VPNState{Phase: VPNPhaseDisconnected}
	}
	var st VPNState
	if json.Unmarshal(data, &st) != nil {
		return VPNState{Phase: VPNPhaseDisconnected}
	}

	running := isOpenConnectRunning()
	if st.Phase == VPNPhaseConnecting || st.Phase == VPNPhaseNeedInput {
		if tun := detectTunInterface(); tunnelReady(tun) {
			st.Phase = VPNPhaseConnected
			st.Connected = true
			st.TunIface = tun
			go promoteActiveSession()
			if running {
				return st
			}
		}
		if running {
			return st
		}
		if st.Phase != VPNPhaseConnected {
			st.Phase = VPNPhaseDisconnected
			st.Connected = false
		}
		return st
	}

	st.Connected = running
	tun := st.TunIface
	if tun == "" {
		tun = detectTunInterface()
	}

	if st.Phase == VPNPhaseConnected && !running {
		st.Connected = false
		st.Phase = VPNPhaseDisconnected
		if st.LastError == "" {
			st.LastError = disconnectReasonFromLog(fmt.Errorf("tunnel down"))
		}
		saveVPNState(st)
		StopManagementWatchdog()
		go func() { _ = ApplyDirectNAT() }()
		return st
	}
	if st.Connected {
		st.Phase = VPNPhaseConnected
		if st.TunIface == "" {
			st.TunIface = tun
		}
	} else if st.Phase == "" {
		st.Phase = VPNPhaseDisconnected
	}
	return st
}

func activeVPNServerURL() string {
	return GetVPNState().ServerURL
}

func scheduleManagementMaintenance(serverURL string) {
	StartManagementWatchdog(serverURL)
}

func saveVPNState(st VPNState) {
	_ = os.MkdirAll(runDir, 0755)
	data, _ := json.MarshalIndent(st, "", "  ")
	_ = os.WriteFile(stateFile, data, 0644)
}

func isOpenConnectRunning() bool {
	if pid := readTrackedOpenConnectPID(); pid > 0 && processRunning(pid) {
		return true
	}
	return findOpenConnectPID() > 0
}

func readTrackedOpenConnectPID() int {
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}

func processRunning(pid int) bool {
	if isProcessZombie(pid) {
		return false
	}
	return exec.Command("kill", "-0", strconv.Itoa(pid)).Run() == nil
}

func isProcessZombie(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return true
	}
	closeIdx := strings.LastIndex(string(data), ")")
	if closeIdx < 0 || closeIdx+2 >= len(data) {
		return false
	}
	return data[closeIdx+2] == 'Z'
}

func findOpenConnectPID() int {
	out, err := exec.Command("pgrep", "-n", "-x", "openconnect").Output()
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || pid <= 0 || isProcessZombie(pid) {
		return 0
	}
	return pid
}

func writeOpenConnectPID(pid int) {
	if pid <= 0 {
		return
	}
	_ = os.WriteFile(pidFile, []byte(fmt.Sprintf("%d\n", pid)), 0644)
}

func detectTunInterface() string {
	if interfaceUp(vpnTunInterface) {
		return vpnTunInterface
	}
	out, err := exec.Command("ip", "-o", "link", "show", "type", "tun").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			return strings.TrimSuffix(fields[1], ":")
		}
	}
	return ""
}

func interfaceUp(iface string) bool {
	out, err := exec.Command("ip", "link", "show", iface).Output()
	if err != nil {
		return false
	}
	s := strings.ToLower(string(out))
	return strings.Contains(s, "state up") || strings.Contains(s, "state unknown")
}

func tunnelReady(iface string) bool {
	return iface != "" && interfaceHasIPv4(iface)
}

func StartConnect(profileID, password string) error {
	vpnMu.Lock()
	defer vpnMu.Unlock()

	profile, ok := GetProfile(profileID)
	if !ok {
		return fmt.Errorf("profile not found")
	}

	pass := password
	if pass == "" {
		pass = profile.Password
	}
	if pass == "" {
		return fmt.Errorf("password required")
	}

	if err := disconnectLocked(); err != nil {
		log.Printf("disconnect before connect: %v", err)
	}

	ensurePolicyRoutingTable()

	_ = os.MkdirAll(runDir, 0755)
	_ = os.WriteFile(logFile, []byte{}, 0644)

	stdinReader, stdinWriter := io.Pipe()
	args := buildOpenConnectArgs(profile)

	cmd := exec.Command("openconnect", args...)
	cmd.Stdin = stdinReader
	logOut, _ := os.OpenFile(logFile, os.O_APPEND|os.O_WRONLY, 0644)
	if logOut != nil {
		cmd.Stdout = logOut
		cmd.Stderr = logOut
	} else {
		cmd.Stdout = io.Discard
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		_ = stdinWriter.Close()
		_ = stdinReader.Close()
		return fmt.Errorf("failed to start openconnect: %w", err)
	}

	writeOpenConnectPID(cmd.Process.Pid)

	sess := &vpnConn{
		cmd:     cmd,
		stdin:   stdinWriter,
		profile: profile,
		stopCh:  make(chan struct{}),
	}
	activeVPNSession = sess

	saveVPNState(VPNState{
		Phase:       VPNPhaseConnecting,
		ProfileID:   profile.ID,
		ProfileName: profile.Name,
		ServerURL:   profile.ServerURL,
		InputKind:   "token",
	})

	go watchOpenConnectProcess(sess)

	go func() {
		if _, err := fmt.Fprintf(stdinWriter, "%s\n", pass); err != nil {
			log.Printf("write password to openconnect: %v", err)
		}
		vpnMu.Lock()
		if activeVPNSession == sess {
			sess.passwordSent = true
		}
		vpnMu.Unlock()
	}()

	go monitorVPNSession(sess)
	return nil
}

func monitorVPNSession(sess *vpnConn) {
	deadline := time.Now().Add(connectTimeout)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-sess.stopCh:
			return
		case <-ticker.C:
			vpnMu.Lock()
			active := activeVPNSession == sess
			vpnMu.Unlock()
			if !active {
				return
			}

			tun := detectTunInterface()
			if tunnelReady(tun) {
				ignoreTunInNetworkManager(tun)
				finishConnected(sess, tun)
				return
			}

			if !processAlive(sess.cmd) {
				if openConnectChildAlive() {
					if sess.parentDeadAt.IsZero() {
						sess.parentDeadAt = time.Now()
					}
					continue
				}
				if !sess.parentDeadAt.IsZero() && time.Since(sess.parentDeadAt) < 8*time.Second {
					continue
				}
				failSession(sess, "openconnect exited before tunnel was established")
				return
			}
			sess.parentDeadAt = time.Time{}

			if time.Now().After(deadline) {
				failSession(sess, "connection timed out waiting for VPN or token")
				return
			}

			if !sess.passwordSent {
				continue
			}

			if prompt, ok := detectTokenPrompt(readLogTail(80)); ok && !sess.inputSent {
				vpnMu.Lock()
				if activeVPNSession == sess {
					saveVPNState(VPNState{
						Phase:       VPNPhaseNeedInput,
						ProfileID:   sess.profile.ID,
						ProfileName: sess.profile.Name,
						ServerURL:   sess.profile.ServerURL,
						InputPrompt: prompt,
						InputKind:   "token",
					})
				}
				vpnMu.Unlock()
			}
		}
	}
}

func promoteActiveSession() {
	vpnMu.Lock()
	sess := activeVPNSession
	vpnMu.Unlock()
	if sess == nil {
		return
	}
	tun := detectTunInterface()
	if tun == "" {
		tun = vpnTunInterface
	}
	if tunnelReady(tun) {
		ignoreTunInNetworkManager(tun)
		finishConnected(sess, tun)
	}
}

func openConnectChildAlive() bool {
	if findOpenConnectPID() > 0 {
		return true
	}
	return tunnelReady(detectTunInterface())
}

func finishConnected(sess *vpnConn, tun string) {
	vpnMu.Lock()
	if activeVPNSession != sess || !interfaceHasIPv4(tun) {
		vpnMu.Unlock()
		return
	}
	profile := sess.profile
	vpnMu.Unlock()

	ignoreTunInNetworkManager(tun)
	_ = ApplyVPNNAT(tun)
	scheduleManagementMaintenance(profile.ServerURL)

	vpnMu.Lock()
	defer vpnMu.Unlock()
	if activeVPNSession != sess {
		return
	}
	st := VPNState{
		Connected:   true,
		Phase:       VPNPhaseConnected,
		ProfileID:   profile.ID,
		ProfileName: profile.Name,
		TunIface:    tun,
		ServerURL:   profile.ServerURL,
		Since:       time.Now().UTC().Format(time.RFC3339),
	}
	saveVPNState(st)

	cfg := GetRouterConfig()
	cfg.LastProfileID = profile.ID
	_ = SaveRouterConfig(cfg)

	if pid := findOpenConnectPID(); pid > 0 {
		writeOpenConnectPID(pid)
	}
}

func ignoreTunInNetworkManager(iface string) {
	if !usesNetworkManager() || iface == "" {
		return
	}
	_ = exec.Command("nmcli", "device", "set", iface, "managed", "no").Run()
}

func watchOpenConnectProcess(sess *vpnConn) {
	var waitErr error
	if sess != nil && sess.cmd != nil {
		waitErr = sess.cmd.Wait()
	}

	vpnMu.Lock()
	if activeVPNSession != sess {
		vpnMu.Unlock()
		return
	}
	tun := detectTunInterface()
	childPID := 0
	if tunnelReady(tun) {
		childPID = findOpenConnectPID()
	}
	vpnMu.Unlock()

	if childPID > 0 {
		writeOpenConnectPID(childPID)
		ignoreTunInNetworkManager(tun)
		finishConnected(sess, tun)
		log.Printf("openconnect parent exited (%v); tracking pid %d", waitErr, childPID)
		go watchOpenConnectPID(sess, childPID)
		return
	}

	vpnMu.Lock()
	defer vpnMu.Unlock()
	if activeVPNSession != sess {
		return
	}
	handleOpenConnectStoppedLocked(sess, waitErr)
}

func handleOpenConnectStoppedLocked(sess *vpnConn, reason error) {
	if activeVPNSession != sess {
		return
	}
	log.Printf("openconnect stopped: %v", reason)
	if tail := strings.TrimSpace(readLogTail(25)); tail != "" {
		log.Printf("openconnect log tail:\n%s", tail)
	}
	activeVPNSession = nil
	_ = os.Remove(pidFile)

	st := readVPNStateFile()
	wasConnected := st.Phase == VPNPhaseConnected
	profile := sess.profile
	switch st.Phase {
	case VPNPhaseConnected:
		saveVPNState(VPNState{
			Phase:       VPNPhaseDisconnected,
			ProfileID:   profile.ID,
			ProfileName: profile.Name,
			LastError:   disconnectReasonFromLog(reason),
		})
	case VPNPhaseConnecting, VPNPhaseNeedInput:
		msg := "openconnect exited before tunnel was established"
		if reason != nil {
			msg = reason.Error()
		}
		saveVPNState(VPNState{
			Phase:     VPNPhaseError,
			ProfileID: sess.profile.ID,
			LastError: msg,
		})
	default:
		saveVPNState(VPNState{Phase: VPNPhaseDisconnected})
	}

	StopManagementWatchdog()
	go func() {
		_ = ApplyDirectNAT()
		if wasConnected && profile.SavePassword && profile.Password != "" {
			scheduleAutoReconnect(profile.ID)
		}
	}()
}

func readVPNStateFile() VPNState {
	data, err := os.ReadFile(stateFile)
	if err != nil {
		return VPNState{Phase: VPNPhaseDisconnected}
	}
	var st VPNState
	if json.Unmarshal(data, &st) != nil {
		return VPNState{Phase: VPNPhaseDisconnected}
	}
	return st
}

func handleOpenConnectStopped(sess *vpnConn, reason error) {
	vpnMu.Lock()
	defer vpnMu.Unlock()
	handleOpenConnectStoppedLocked(sess, reason)
}

func watchOpenConnectPID(sess *vpnConn, pid int) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		vpnMu.Lock()
		active := activeVPNSession == sess
		vpnMu.Unlock()
		if !active {
			return
		}
		if processRunning(pid) {
			continue
		}
		break
	}

	vpnMu.Lock()
	defer vpnMu.Unlock()
	if activeVPNSession != sess {
		return
	}
	handleOpenConnectStoppedLocked(sess, fmt.Errorf("openconnect pid %d exited", pid))
}

const vpnTunInterface = "vpn0"

func openConnectScriptPath() string {
	candidates := []string{
		"/opt/vpn-connector/scripts/vpnc-policy.sh",
		filepath.Join("scripts", "vpnc-policy.sh"),
	}
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			if abs, err := filepath.Abs(p); err == nil {
				return abs
			}
			return p
		}
	}
	return "/bin/true"
}

func useNoDTLS(profile VPNProfile) bool {
	if profile.NoDTLS {
		return true
	}
	// FortiGate sends GFtype heartbeats over DTLS that break openconnect on Linux/ARM.
	return profile.Protocol == "fortinet"
}

func buildOpenConnectArgs(profile VPNProfile) []string {
	args := []string{
		"--protocol=" + profile.Protocol,
		"-u", profile.Username,
		"--servercert", profile.ServerCertPin,
		"--passwd-on-stdin",
		"-i", vpnTunInterface,
		"--script=" + openConnectScriptPath(),
		"--queue-len=8",
		"--reconnect-timeout=600",
		"--force-dpd=30",
	}
	if useNoDTLS(profile) {
		args = append(args, "--no-dtls")
	}
	args = append(args, profile.ServerURL)
	return args
}

func disconnectReasonFromLog(reason error) string {
	tail := strings.ToLower(readLogTail(30))
	switch {
	case strings.Contains(tail, "cookie is no longer valid"), strings.Contains(tail, "cookie was rejected"):
		return "VPN session expired on server; reconnect (FortiToken may be required)"
	case strings.Contains(tail, "unexpected pre-ppp packet"), strings.Contains(tail, "read error on dtls"), strings.Contains(tail, "failed to connect dtls"):
		return "VPN DTLS failed (Fortinet heartbeat issue); reconnecting with SSL-only should fix this"
	case strings.Contains(tail, "detected dead peer"):
		return "VPN lost contact with server (dead peer); auto-reconnect will retry if password is saved"
	default:
		if reason != nil && reason.Error() != "" {
			return "VPN disconnected: " + reason.Error()
		}
		return "VPN disconnected"
	}
}

func scheduleAutoReconnect(profileID string) {
	autoReconnectMu.Lock()
	if autoReconnecting {
		autoReconnectMu.Unlock()
		return
	}
	autoReconnecting = true
	autoReconnectMu.Unlock()

	go func() {
		defer func() {
			autoReconnectMu.Lock()
			autoReconnecting = false
			autoReconnectMu.Unlock()
		}()

		time.Sleep(8 * time.Second)
		profile, ok := GetProfile(profileID)
		if !ok || profile.Password == "" {
			return
		}
		st := GetVPNState()
		if st.Phase == VPNPhaseConnected || isOpenConnectRunning() {
			return
		}
		log.Printf("Auto-reconnecting VPN profile %s", profile.Name)
		if err := StartConnect(profile.ID, profile.Password); err != nil {
			log.Printf("Auto-reconnect failed: %v", err)
		}
	}()
}

func failSession(sess *vpnConn, msg string) {
	vpnMu.Lock()
	if activeVPNSession != sess {
		vpnMu.Unlock()
		return
	}
	log.Printf("VPN session failed: %s", msg)
	activeVPNSession = nil
	_ = killSessionProcess(sess)
	saveVPNState(VPNState{
		Phase:     VPNPhaseError,
		ProfileID: sess.profile.ID,
		LastError: msg,
	})
	vpnMu.Unlock()
	StopManagementWatchdog()
	_ = ApplyDirectNAT()
}

func SubmitVPNInput(input string) error {
	vpnMu.Lock()
	defer vpnMu.Unlock()

	if activeVPNSession == nil {
		return fmt.Errorf("no active VPN connection attempt")
	}
	st := readVPNStateFile()
	if st.Phase != VPNPhaseNeedInput && st.Phase != VPNPhaseConnecting {
		return fmt.Errorf("VPN is not waiting for input")
	}
	input = strings.TrimSpace(input)
	if input == "" {
		return fmt.Errorf("input required")
	}

	if _, err := fmt.Fprintf(activeVPNSession.stdin, "%s\n", input); err != nil {
		return fmt.Errorf("failed to send input to openconnect: %w", err)
	}
	activeVPNSession.inputSent = true

	saveVPNState(VPNState{
		Phase:       VPNPhaseConnecting,
		ProfileID:   activeVPNSession.profile.ID,
		ProfileName: activeVPNSession.profile.Name,
		ServerURL:   activeVPNSession.profile.ServerURL,
		InputKind:   "token",
	})
	return nil
}

func ConnectProfile(profileID, password string) error {
	if err := StartConnect(profileID, password); err != nil {
		return err
	}

	deadline := time.Now().Add(connectTimeout)
	for time.Now().Before(deadline) {
		st := GetVPNState()
		switch st.Phase {
		case VPNPhaseConnected:
			return nil
		case VPNPhaseNeedInput:
			return fmt.Errorf("token required: submit via /api/vpn/input")
		case VPNPhaseError:
			if st.LastError != "" {
				return fmt.Errorf("%s", st.LastError)
			}
			return fmt.Errorf("connection failed")
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("connection timed out")
}

func DisconnectVPN() error {
	vpnMu.Lock()
	defer vpnMu.Unlock()
	return disconnectLocked()
}

func disconnectLocked() error {
	if activeVPNSession != nil {
		sess := activeVPNSession
		activeVPNSession = nil
		close(sess.stopCh)
		_ = killSessionProcess(sess)
		if sess.stdin != nil {
			_ = sess.stdin.Close()
		}
	}

	if pid := readTrackedOpenConnectPID(); pid > 0 && processRunning(pid) {
		_ = exec.Command("kill", strconv.Itoa(pid)).Run()
		time.Sleep(500 * time.Millisecond)
		_ = exec.Command("kill", "-9", strconv.Itoa(pid)).Run()
	}
	if pid := findOpenConnectPID(); pid > 0 {
		_ = exec.Command("kill", "-9", strconv.Itoa(pid)).Run()
	}
	_ = os.Remove(pidFile)
	saveVPNState(VPNState{Phase: VPNPhaseDisconnected})
	StopManagementWatchdog()
	go func() { _ = ApplyDirectNAT() }()
	return nil
}

func killSessionProcess(sess *vpnConn) error {
	if openConnectChildAlive() {
		pid := findOpenConnectPID()
		if pid > 0 {
			_ = exec.Command("kill", strconv.Itoa(pid)).Run()
			time.Sleep(500 * time.Millisecond)
			if processRunning(pid) {
				_ = exec.Command("kill", "-9", strconv.Itoa(pid)).Run()
			}
		}
		return nil
	}
	if sess == nil || sess.cmd == nil || sess.cmd.Process == nil {
		return nil
	}
	pgid := sess.cmd.Process.Pid
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	time.Sleep(500 * time.Millisecond)
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	return nil
}

func processAlive(cmd *exec.Cmd) bool {
	if cmd == nil || cmd.Process == nil {
		return false
	}
	return processRunning(cmd.Process.Pid)
}

func interfaceHasIPv4(iface string) bool {
	out, err := exec.Command("ip", "-o", "-4", "addr", "show", "dev", iface).Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

func detectTokenPrompt(logContent string) (string, bool) {
	if strings.TrimSpace(logContent) == "" {
		return "", false
	}
	lines := strings.Split(logContent, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		keywords := []string{
			"one-time password", "one time password", "fortitoken", "token code",
			"enter token", "enter otp", "authentication code", "secondary password",
			"two-factor", "2fa", "passcode", "please enter your response",
			"please enter", "enter code",
		}
		for _, kw := range keywords {
			if strings.Contains(lower, kw) {
				return line, true
			}
		}
		if strings.Contains(lower, "token") && strings.Contains(lower, "enter") {
			return line, true
		}
	}
	return "", false
}

func readLogTail(maxLines int) string {
	data, err := os.ReadFile(logFile)
	if err != nil {
		return ""
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return strings.Join(lines, "\n")
}

func ReconnectLastProfile() error {
	cfg := GetRouterConfig()
	if cfg.LastProfileID == "" {
		return fmt.Errorf("no profile connected before")
	}
	profile, ok := GetProfile(cfg.LastProfileID)
	if !ok {
		return fmt.Errorf("last profile not found")
	}
	return StartConnect(profile.ID, profile.Password)
}

func EnsureOpenConnectInstalled() error {
	if _, err := exec.LookPath("openconnect"); err != nil {
		return fmt.Errorf("openconnect not installed; run: apt-get install -y openconnect")
	}
	return nil
}

func OpenConnectLogTail() string {
	return readLogTail(40)
}

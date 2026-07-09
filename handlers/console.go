package handlers

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"
)

var tokenPopupRunning atomic.Bool

const (
	ansiReset  = "\033[0m"
	ansiBlack  = "\033[40m"
	ansiWhite  = "\033[37m"
	ansiYellow = "\033[33m"
	ansiGreen  = "\033[32m"
	ansiCyan   = "\033[36m"
	ansiBold   = "\033[1m"
	ansiClear  = "\033[2J\033[H"
)

// StartTokenPopupWatcher pops a full-screen console prompt on the device display
// (HDMI / tty) when FortiToken input is required during an active VPN connect.
func StartTokenPopupWatcher() {
	go func() {
		wasWaiting := false
		for {
			time.Sleep(800 * time.Millisecond)
			waiting := consoleTokenWaitActive()
			if waiting && !wasWaiting {
				go launchTokenPopup()
			}
			wasWaiting = waiting
		}
	}()
}

func consoleTokenWaitActive() bool {
	if !isOpenConnectRunning() {
		return false
	}
	st := GetVPNState()
	if st.Phase == VPNPhaseNeedInput {
		return true
	}
	return VPNWaitingForToken()
}

func launchTokenPopup() {
	if !tokenPopupRunning.CompareAndSwap(false, true) {
		return
	}
	defer tokenPopupRunning.Store(false)

	if _, err := exec.LookPath("openvt"); err != nil {
		log.Printf("Console token popup unavailable (install kbd package for openvt): %v", err)
		return
	}

	exe, err := os.Executable()
	if err != nil {
		log.Printf("Console token popup: %v", err)
		return
	}

	cmd := exec.Command("openvt", "-s", "-w", "-f", "--", exe, "token-prompt")
	if out, err := cmd.CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			log.Printf("Console token popup: %v (%s)", err, msg)
		}
	}
}

// RunTokenPromptPopup shows a one-shot full-screen token prompt (used by openvt).
func RunTokenPromptPopup() int {
	return runConsoleScreen(true)
}

// RunConsoleMenu runs a continuous status menu on the local console TTY.
func RunConsoleMenu() int {
	return runConsoleScreen(false)
}

func runConsoleScreen(popupOnly bool) int {
	tty, err := openConsoleTTY()
	if err != nil {
		fmt.Fprintf(os.Stderr, "console: %v\n", err)
		return 1
	}
	defer tty.Close()

	reader := bufio.NewReader(tty)

	if popupOnly {
		deadline := time.Now().Add(45 * time.Second)
		for time.Now().Before(deadline) {
			if consoleTokenWaitActive() {
				break
			}
			time.Sleep(300 * time.Millisecond)
		}
		if !consoleTokenWaitActive() {
			return 0
		}
	}

	for {
		st := GetVPNState()
		waiting := consoleTokenWaitActive()

		if popupOnly && !waiting {
			drawConsoleDone(tty, st)
			time.Sleep(1500 * time.Millisecond)
			return 0
		}

		drawConsoleScreen(tty, st, waiting)

		if waiting {
			token, cancelled := readConsoleToken(reader, tty, popupOnly)
			if cancelled {
				if popupOnly {
					return 0
				}
				time.Sleep(500 * time.Millisecond)
				continue
			}
			if err := SubmitVPNInput(token); err != nil {
				drawConsoleMessage(tty, "Error: "+err.Error(), 2*time.Second)
				continue
			}
			drawConsoleMessage(tty, "Token sent. Finishing VPN connection...", 2*time.Second)
			if popupOnly {
				return 0
			}
			continue
		}

		time.Sleep(2 * time.Second)
	}
}

func openConsoleTTY() (*os.File, error) {
	for _, path := range []string{"/dev/tty", "/dev/console", "/dev/tty1"} {
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err == nil {
			return f, nil
		}
	}
	return nil, fmt.Errorf("no console TTY available (try running on tty1 or via openvt)")
}

func drawConsoleScreen(w io.Writer, st VPNState, waitingForToken bool) {
	fmt.Fprint(w, ansiClear, ansiBlack, ansiWhite)

	lines := []string{
		"",
		"  ╔══════════════════════════════════════════════╗",
		"  ║           VPN Connector — Console            ║",
		"  ╠══════════════════════════════════════════════╣",
	}

	if st.ProfileName != "" {
		lines = append(lines, fmt.Sprintf("  ║  Profile: %-34s ║", truncateConsole(st.ProfileName, 34)))
	} else if st.ProfileID != "" {
		lines = append(lines, fmt.Sprintf("  ║  Profile: %-34s ║", truncateConsole(st.ProfileID, 34)))
	}

	stateLabel := consolePhaseLabel(st.Phase, waitingForToken)
	lines = append(lines, fmt.Sprintf("  ║  State:   %-34s ║", stateLabel))

	if st.TunIface != "" && st.Phase == VPNPhaseConnected {
		lines = append(lines, fmt.Sprintf("  ║  Tunnel:  %-34s ║", st.TunIface))
	}

	lines = append(lines, "  ║                                              ║")

	if waitingForToken {
		prompt := strings.TrimSpace(st.InputPrompt)
		if prompt == "" {
			prompt = "Enter token code or leave empty for FortiToken Mobile push."
		}
		for _, part := range wrapConsoleText(prompt, 44) {
			lines = append(lines, fmt.Sprintf("  ║  %-44s ║", part))
		}
		lines = append(lines, "  ║                                              ║")
		lines = append(lines, "  ║  Type your token on the line below.          ║")
		lines = append(lines, "  ║                                              ║")
		lines = append(lines, "  ║  Enter=Submit   q=Dismiss (web UI works too) ║")
	} else {
		cfg := GetRouterConfig()
		hint := FormatManagementHint(cfg)
		for _, part := range wrapConsoleText(hint, 44) {
			lines = append(lines, fmt.Sprintf("  ║  %-44s ║", part))
		}
		lines = append(lines, "  ║                                              ║")
		lines = append(lines, "  ║  Connect from the web dashboard.             ║")
		lines = append(lines, "  ║  This screen refreshes automatically.        ║")
	}

	lines = append(lines,
		"  ╚══════════════════════════════════════════════╝",
		"",
		ansiReset,
	)

	for _, line := range lines {
		fmt.Fprintln(w, line)
	}

	if waitingForToken {
		fmt.Fprint(w, ansiBlack, ansiWhite, ansiYellow, "  Token: ", ansiWhite)
	}
}

func drawConsoleDone(w io.Writer, st VPNState) {
	fmt.Fprint(w, ansiClear, ansiBlack, ansiWhite)
	msg := "VPN connected."
	if st.Phase != VPNPhaseConnected {
		msg = "Token prompt closed."
	}
	fmt.Fprintf(w, "\n  %s%s%s\n\n", ansiGreen, msg, ansiReset)
}

func drawConsoleMessage(w io.Writer, msg string, wait time.Duration) {
	fmt.Fprint(w, ansiClear, ansiBlack, ansiWhite)
	fmt.Fprintf(w, "\n  %s%s%s\n\n", ansiCyan, msg, ansiReset)
	time.Sleep(wait)
}

func readConsoleToken(r *bufio.Reader, tty *os.File, popupOnly bool) (string, bool) {
	if !popupOnly {
		fmt.Fprint(tty, ansiYellow, "  Token: ", ansiWhite)
	}
	line, err := r.ReadString('\n')
	if err != nil {
		return "", true
	}
	line = strings.TrimSpace(line)
	if line == "q" || line == "Q" {
		return "", true
	}
	return line, false
}

func consolePhaseLabel(phase string, waiting bool) string {
	if waiting {
		return "Token required"
	}
	switch phase {
	case VPNPhaseConnected:
		return "Connected"
	case VPNPhaseConnecting:
		return "Connecting..."
	case VPNPhaseNeedInput:
		return "Token required"
	case VPNPhaseError:
		return "Error"
	default:
		return "Disconnected"
	}
}

func truncateConsole(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}

func wrapConsoleText(text string, width int) []string {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return []string{""}
	}
	var out []string
	for len(text) > width {
		cut := width
		if idx := strings.LastIndex(text[:width], " "); idx > 0 {
			cut = idx
		}
		out = append(out, text[:cut])
		text = strings.TrimSpace(text[cut:])
	}
	if text != "" {
		out = append(out, text)
	}
	return out
}

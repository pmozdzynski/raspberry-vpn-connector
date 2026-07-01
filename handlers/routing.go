package handlers

import (
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

const vpnPolicyTableID = 200
const vpnPolicyTableName = "vpn-connector"

var (
	mgmtWatchdogMu sync.Mutex
	mgmtWatchdogOn bool
)

func ensurePolicyRoutingTable() {
	const rtTables = "/etc/iproute2/rt_tables"
	data, err := os.ReadFile(rtTables)
	if err != nil {
		return
	}
	if strings.Contains(string(data), vpnPolicyTableName) {
		return
	}
	line := fmt.Sprintf("%d %s\n", vpnPolicyTableID, vpnPolicyTableName)
	_ = os.WriteFile(rtTables, append(data, []byte(line)...), 0644)
}

func lanSubnetCIDR(cfg RouterConfig) string {
	if cfg.LANAddress == "" || cfg.LANPrefix == 0 {
		return ""
	}
	ip := networkAddr(cfg.LANAddress, cfg.LANPrefix)
	if ip == nil {
		return ""
	}
	return fmt.Sprintf("%s/%d", ip.String(), cfg.LANPrefix)
}

func wanSubnetCIDR(iface string) string {
	wanIP, wanPrefix := getInterfaceIPv4CIDR(iface)
	if wanIP == "" || wanPrefix == 0 {
		return ""
	}
	ip := networkAddr(wanIP, wanPrefix)
	if ip == nil {
		return ""
	}
	return fmt.Sprintf("%s/%d", ip.String(), wanPrefix)
}

func ensureIPRule(args ...string) {
	delArgs := append([]string{"rule", "del"}, args...)
	_ = exec.Command("ip", delArgs...).Run()
	addArgs := append([]string{"rule", "add"}, args...)
	_ = exec.Command("ip", addArgs...).Run()
}

func flushVPNPolicyRouting(cfg RouterConfig) {
	table := strconv.Itoa(vpnPolicyTableID)
	_ = exec.Command("ip", "route", "flush", "table", table).Run()
	if subnet := lanSubnetCIDR(cfg); subnet != "" {
		_ = exec.Command("ip", "rule", "del", "from", subnet, "table", table).Run()
	}
}

func wanGatewayFromRoutes(iface string) string {
	if iface == "" {
		return ""
	}
	out, err := exec.Command("ip", "route", "show", "dev", iface).Output()
	if err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 || fields[0] != "default" {
				continue
			}
			for i := 0; i < len(fields)-1; i++ {
				if fields[i] == "via" {
					return fields[i+1]
				}
			}
		}
	}
	out, err = exec.Command("ip", "route", "show", "default").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "default" {
			continue
		}
		dev := routeField(fields, "dev")
		if dev != iface {
			continue
		}
		if gw := routeField(fields, "via"); gw != "" {
			return gw
		}
	}
	return ""
}

func routeField(fields []string, key string) string {
	for i := 0; i < len(fields)-1; i++ {
		if fields[i] == key {
			return fields[i+1]
		}
	}
	return ""
}

func guessWANGateway(iface string) string {
	wanIP, _ := getInterfaceIPv4CIDR(iface)
	if wanIP == "" {
		return ""
	}
	parts := strings.Split(wanIP, ".")
	if len(parts) != 4 {
		return ""
	}
	return fmt.Sprintf("%s.%s.%s.1", parts[0], parts[1], parts[2])
}

func resolvedWANGateway(cfg RouterConfig) string {
	if gw := wanGatewayFromRoutes(cfg.WANInterface); gw != "" {
		if gw != cfg.WANGateway {
			updated := cfg
			updated.WANGateway = gw
			_ = SaveRouterConfig(updated)
		}
		return gw
	}
	if cfg.WANGateway != "" {
		return cfg.WANGateway
	}
	return guessWANGateway(cfg.WANInterface)
}

func removeTunnelDefaultsFromMain() {
	for i := 0; i < 8; i++ {
		out, _ := exec.Command("ip", "route", "show", "table", "main", "default").Output()
		removed := false
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || !strings.HasPrefix(line, "default") {
				continue
			}
			dev := routeField(strings.Fields(line), "dev")
			if dev == "" || (!strings.HasPrefix(dev, "vpn") && !strings.HasPrefix(dev, "tun") && !strings.HasPrefix(dev, "tap")) {
				continue
			}
			_ = exec.Command("ip", "route", "del", line).Run()
			removed = true
		}
		if !removed {
			return
		}
	}
}

// Pin Pi management traffic on the home LAN (eth0), never via vpn0.
func ensureWANDefaultOnMain(cfg RouterConfig) {
	if cfg.WANInterface == "" {
		return
	}
	gw := resolvedWANGateway(cfg)
	if gw == "" {
		log.Printf("WAN gateway unknown; cannot restore default route on %s", cfg.WANInterface)
		return
	}
	removeTunnelDefaultsFromMain()
	wanIP, _ := getInterfaceIPv4CIDR(cfg.WANInterface)
	args := []string{"route", "replace", "default", "via", gw, "dev", cfg.WANInterface, "metric", "100"}
	if wanIP != "" {
		args = append(args, "src", wanIP)
	}
	_ = exec.Command("ip", args...).Run()
}

func protectManagementRules(cfg RouterConfig) {
	if cfg.WANInterface != "" {
		wanIP, _ := getInterfaceIPv4CIDR(cfg.WANInterface)
		if wanIP != "" {
			host := wanIP + "/32"
			ensureIPRule("to", host, "lookup", "main", "priority", "43")
			ensureIPRule("from", host, "lookup", "main", "priority", "49")
		}
		if subnet := wanSubnetCIDR(cfg.WANInterface); subnet != "" {
			ensureIPRule("from", subnet, "lookup", "main", "priority", "50")
			ensureIPRule("to", subnet, "lookup", "main", "priority", "51")
		}
	}
	if cfg.LANAddress != "" {
		host := cfg.LANAddress + "/32"
		ensureIPRule("to", host, "lookup", "main", "priority", "44")
		ensureIPRule("from", host, "lookup", "main", "priority", "44")
	}
	if subnet := lanSubnetCIDR(cfg); subnet != "" {
		ensureIPRule("to", subnet, "lookup", "main", "priority", "45")
	}
}

func ensureConnectedSubnetRoutes(cfg RouterConfig) {
	if cfg.WANInterface != "" {
		if wanIP, prefix := getInterfaceIPv4CIDR(cfg.WANInterface); wanIP != "" {
			if netAddr := networkAddr(wanIP, prefix); netAddr != nil {
				subnet := fmt.Sprintf("%s/%d", netAddr.String(), prefix)
				_ = exec.Command("ip", "route", "replace", subnet, "dev", cfg.WANInterface,
					"proto", "kernel", "scope", "link", "src", wanIP).Run()
			}
		}
	}
	if cfg.LANInterface != "" {
		if subnet := lanSubnetCIDR(cfg); subnet != "" && cfg.LANAddress != "" {
			_ = exec.Command("ip", "route", "replace", subnet, "dev", cfg.LANInterface,
				"proto", "kernel", "scope", "link", "src", cfg.LANAddress).Run()
		}
	}
}

func ensureVPNHostRouteViaWAN(cfg RouterConfig, serverURL string) {
	if cfg.WANInterface == "" || strings.TrimSpace(serverURL) == "" {
		return
	}
	host := vpnServerRouteHost(serverURL)
	if host == nil {
		return
	}
	args := []string{"route", "replace", host.String() + "/32", "dev", cfg.WANInterface}
	if gw := resolvedWANGateway(cfg); gw != "" {
		args = append(args, "via", gw)
	}
	_ = exec.Command("ip", args...).Run()
}

func MaintainManagementAccess(cfg RouterConfig, serverURL string) {
	protectManagementRules(cfg)
	ensureConnectedSubnetRoutes(cfg)
	ensureWANDefaultOnMain(cfg)
	ensureWANInputAccess(cfg)
	ensureLANInputAccess(cfg)
	if serverURL != "" {
		ensureVPNHostRouteViaWAN(cfg, serverURL)
	}
	loosenReversePathFiltering(cfg.WANInterface, cfg.LANInterface)
}

func StartManagementWatchdog(serverURL string) {
	mgmtWatchdogMu.Lock()
	if mgmtWatchdogOn {
		mgmtWatchdogMu.Unlock()
		return
	}
	mgmtWatchdogOn = true
	mgmtWatchdogMu.Unlock()

	go func() {
		defer func() {
			mgmtWatchdogMu.Lock()
			mgmtWatchdogOn = false
			mgmtWatchdogMu.Unlock()
		}()

		cfg := GetRouterConfig()
		delays := []time.Duration{0, 2 * time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second}
		for _, delay := range delays {
			time.Sleep(delay)
			if !GetVPNState().Connected {
				return
			}
			MaintainManagementAccess(cfg, serverURL)
		}

		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			if !GetVPNState().Connected {
				return
			}
			MaintainManagementAccess(GetRouterConfig(), serverURL)
		}
	}()
}

func StopManagementWatchdog() {
	mgmtWatchdogMu.Lock()
	mgmtWatchdogOn = false
	mgmtWatchdogMu.Unlock()
}

func ApplyVPNPolicyRouting(cfg RouterConfig, tunIface string, serverURL string) error {
	ensurePolicyRoutingTable()
	flushVPNPolicyRouting(cfg)

	table := strconv.Itoa(vpnPolicyTableID)
	lan := cfg.LANInterface
	wan := cfg.WANInterface
	lanSubnet := lanSubnetCIDR(cfg)
	if lanSubnet == "" || lan == "" || tunIface == "" {
		return fmt.Errorf("VPN policy routing: missing LAN or tunnel interface")
	}

	MaintainManagementAccess(cfg, serverURL)

	if wanSubnet := wanSubnetCIDR(wan); wanSubnet != "" {
		_ = exec.Command("ip", "route", "replace", wanSubnet, "dev", wan, "table", table).Run()
	}
	_ = exec.Command("ip", "route", "replace", lanSubnet, "dev", lan, "table", table).Run()
	if out, err := exec.Command("ip", "route", "replace", "default", "dev", tunIface, "table", table).CombinedOutput(); err != nil {
		return fmt.Errorf("ip route default dev %s table %s: %v: %s", tunIface, table, err, strings.TrimSpace(string(out)))
	}

	ensureIPRule("from", lanSubnet, "lookup", table, "priority", "100")

	log.Printf("VPN policy routing: LAN %s -> %s; WAN management pinned on %s", lanSubnet, tunIface, wan)
	return nil
}

func ApplyDirectPolicyRouting(cfg RouterConfig) {
	flushVPNPolicyRouting(cfg)
	MaintainManagementAccess(cfg, "")
}

func loosenReversePathFiltering(ifaces ...string) {
	_ = exec.Command("sysctl", "-w", "net.ipv4.conf.all.rp_filter=2").Run()
	_ = exec.Command("sysctl", "-w", "net.ipv4.conf.default.rp_filter=2").Run()
	for _, iface := range ifaces {
		if iface == "" {
			continue
		}
		_ = exec.Command("sysctl", "-w", "net.ipv4.conf."+iface+".rp_filter=2").Run()
	}
}

func ensureWANInputAccess(cfg RouterConfig) {
	if cfg.WANInterface == "" {
		return
	}
	spec := []string{"-i", cfg.WANInterface, "-p", "tcp", "--dport", "5000", "-j", "ACCEPT"}
	if exec.Command("iptables", append([]string{"-C", "INPUT"}, spec...)...).Run() != nil {
		exec.Command("iptables", append([]string{"-I", "INPUT", "1"}, spec...)...).Run()
	}
}

func ensureLANInputAccess(cfg RouterConfig) {
	if cfg.LANInterface == "" {
		return
	}
	spec := []string{"-i", cfg.LANInterface, "-p", "tcp", "--dport", "5000", "-j", "ACCEPT"}
	if exec.Command("iptables", append([]string{"-C", "INPUT"}, spec...)...).Run() != nil {
		exec.Command("iptables", append([]string{"-I", "INPUT", "1"}, spec...)...).Run()
	}
}

func parseVPNHost(serverURL string) string {
	u := strings.TrimSpace(serverURL)
	u = strings.TrimPrefix(u, "https://")
	u = strings.TrimPrefix(u, "http://")
	if host, _, ok := strings.Cut(u, "/"); ok {
		u = host
	}
	if host, _, ok := strings.Cut(u, ":"); ok {
		return host
	}
	return u
}

func vpnServerRouteHost(serverURL string) net.IP {
	host := parseVPNHost(serverURL)
	if ip := net.ParseIP(host); ip != nil {
		return ip
	}
	out, err := exec.Command("getent", "hosts", host).Output()
	if err != nil {
		return nil
	}
	fields := strings.Fields(string(out))
	if len(fields) < 1 {
		return nil
	}
	return net.ParseIP(fields[0])
}

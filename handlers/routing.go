package handlers

import (
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

const vpnPolicyTableID = 200
const vpnPolicyTableName = "vpn-connector"

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

func flushVPNPolicyRouting(cfg RouterConfig) {
	table := strconv.Itoa(vpnPolicyTableID)
	_ = exec.Command("ip", "route", "flush", "table", table).Run()
	if subnet := lanSubnetCIDR(cfg); subnet != "" {
		_ = exec.Command("ip", "rule", "del", "from", subnet, "table", table).Run()
	}
}

func wanGatewayForInterface(iface string) string {
	out, err := exec.Command("ip", "route", "show", "dev", iface).Output()
	if err != nil {
		return ""
	}
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
	out, err = exec.Command("ip", "route", "show", "default").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "default" {
			continue
		}
		dev := ""
		for i := 0; i < len(fields)-1; i++ {
			if fields[i] == "dev" {
				dev = fields[i+1]
				break
			}
		}
		if dev != iface {
			continue
		}
		for i := 0; i < len(fields)-1; i++ {
			if fields[i] == "via" {
				return fields[i+1]
			}
		}
	}
	return ""
}

// Keep the Pi's own traffic (dashboard on eth0) on the home network, not via VPN.
func ensureWANDefaultOnMain(cfg RouterConfig) {
	if cfg.WANInterface == "" {
		return
	}
	gw := wanGatewayForInterface(cfg.WANInterface)
	if gw == "" {
		return
	}
	out, _ := exec.Command("ip", "route", "show", "table", "main").Output()
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "default" {
			continue
		}
		dev := ""
		for i := 0; i < len(fields)-1; i++ {
			if fields[i] == "dev" {
				dev = fields[i+1]
				break
			}
		}
		if dev == cfg.WANInterface {
			return
		}
		if strings.HasPrefix(dev, "vpn") || strings.HasPrefix(dev, "tun") {
			_ = exec.Command("ip", "route", "del", "default", "table", "main").Run()
			break
		}
	}
	_ = exec.Command("ip", "route", "replace", "default", "via", gw, "dev", cfg.WANInterface, "table", "main").Run()
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
	if gw := wanGatewayForInterface(cfg.WANInterface); gw != "" {
		args = append(args, "via", gw)
	}
	_ = exec.Command("ip", args...).Run()
}

// Re-add the directly-connected subnet routes on WAN/LAN. With DHCP "noprefixroute"
// on eth0, the kernel does not add these automatically, so if anything flushes them
// the Pi loses reply routing to its own home LAN (dashboard on WAN IP).
func ensureConnectedSubnetRoutes(cfg RouterConfig) {
	if cfg.WANInterface != "" {
		if wanIP, prefix := getInterfaceIPv4CIDR(cfg.WANInterface); wanIP != "" {
			if net := networkAddr(wanIP, prefix); net != nil {
				subnet := fmt.Sprintf("%s/%d", net.String(), prefix)
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

func flushLegacyManagementRules(cfg RouterConfig) {
	if cfg.WANInterface != "" {
		wanIP, _ := getInterfaceIPv4CIDR(cfg.WANInterface)
		if wanIP != "" {
			host := wanIP + "/32"
			_ = exec.Command("ip", "rule", "del", "to", host, "lookup", "main", "priority", "43").Run()
			_ = exec.Command("ip", "rule", "del", "from", host, "lookup", "main", "priority", "49").Run()
		}
		if subnet := wanSubnetCIDR(cfg.WANInterface); subnet != "" {
			_ = exec.Command("ip", "rule", "del", "from", subnet, "lookup", "main", "priority", "50").Run()
			_ = exec.Command("ip", "rule", "del", "to", subnet, "lookup", "main", "priority", "51").Run()
		}
	}
	if cfg.LANAddress != "" {
		host := cfg.LANAddress + "/32"
		_ = exec.Command("ip", "rule", "del", "to", host, "lookup", "main", "priority", "44").Run()
		_ = exec.Command("ip", "rule", "del", "from", host, "lookup", "main", "priority", "44").Run()
	}
	if subnet := lanSubnetCIDR(cfg); subnet != "" {
		_ = exec.Command("ip", "rule", "del", "to", subnet, "lookup", "main", "priority", "45").Run()
	}
	if cfg.LANInterface != "" {
		table := strconv.Itoa(vpnPolicyTableID)
		_ = exec.Command("ip", "rule", "del", "iif", cfg.LANInterface, "lookup", table).Run()
	}
}

func MaintainManagementAccess(cfg RouterConfig, serverURL string) {
	flushLegacyManagementRules(cfg)
	ensureConnectedSubnetRoutes(cfg)
	ensureWANDefaultOnMain(cfg)
	ensureWANInputAccess(cfg)
	ensureLANInputAccess(cfg)
	if serverURL != "" {
		ensureVPNHostRouteViaWAN(cfg, serverURL)
	}
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

	_ = exec.Command("ip", "rule", "del", "from", lanSubnet, "table", table).Run()
	_ = exec.Command("ip", "rule", "add", "from", lanSubnet, "lookup", table, "priority", "100").Run()

	loosenReversePathFiltering(lan, tunIface, wan)
	log.Printf("VPN policy routing: LAN %s -> %s", lanSubnet, tunIface)
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

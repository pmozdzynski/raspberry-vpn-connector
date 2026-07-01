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
	if cfg.LANInterface != "" {
		_ = exec.Command("ip", "rule", "del", "iif", cfg.LANInterface, "table", table).Run()
	}
	if cfg.WANInterface != "" {
		if subnet := wanSubnetCIDR(cfg.WANInterface); subnet != "" {
			_ = exec.Command("ip", "rule", "del", "from", subnet, "lookup", "main", "priority", "50").Run()
			_ = exec.Command("ip", "rule", "del", "to", subnet, "lookup", "main", "priority", "51").Run()
		}
		wanIP, _ := getInterfaceIPv4CIDR(cfg.WANInterface)
		if wanIP != "" {
			_ = exec.Command("ip", "rule", "del", "from", wanIP+"/32", "lookup", "main", "priority", "49").Run()
		}
	}
}

func protectWANManagement(cfg RouterConfig) {
	if cfg.WANInterface == "" {
		return
	}
	if subnet := wanSubnetCIDR(cfg.WANInterface); subnet != "" {
		_ = exec.Command("ip", "rule", "add", "from", subnet, "lookup", "main", "priority", "50").Run()
		_ = exec.Command("ip", "rule", "add", "to", subnet, "lookup", "main", "priority", "51").Run()
	}
	wanIP, _ := getInterfaceIPv4CIDR(cfg.WANInterface)
	if wanIP != "" {
		_ = exec.Command("ip", "rule", "add", "from", wanIP+"/32", "lookup", "main", "priority", "49").Run()
	}
}

func ApplyVPNPolicyRouting(cfg RouterConfig, tunIface string) error {
	ensurePolicyRoutingTable()
	flushVPNPolicyRouting(cfg)

	table := strconv.Itoa(vpnPolicyTableID)
	lan := cfg.LANInterface
	wan := cfg.WANInterface
	lanSubnet := lanSubnetCIDR(cfg)
	if lanSubnet == "" || lan == "" || tunIface == "" {
		return fmt.Errorf("VPN policy routing: missing LAN or tunnel interface")
	}

	protectWANManagement(cfg)

	if wanSubnet := wanSubnetCIDR(wan); wanSubnet != "" {
		_ = exec.Command("ip", "route", "add", wanSubnet, "dev", wan, "table", table).Run()
	}
	_ = exec.Command("ip", "route", "add", lanSubnet, "dev", lan, "table", table).Run()
	if out, err := exec.Command("ip", "route", "replace", "default", "dev", tunIface, "table", table).CombinedOutput(); err != nil {
		return fmt.Errorf("ip route default dev %s table %s: %v: %s", tunIface, table, err, strings.TrimSpace(string(out)))
	}

	_ = exec.Command("ip", "rule", "add", "from", lanSubnet, "lookup", table, "priority", "100").Run()
	_ = exec.Command("ip", "rule", "add", "iif", lan, "lookup", table, "priority", "101").Run()

	loosenReversePathFiltering(lan, tunIface, wan)
	log.Printf("VPN policy routing: LAN %s uses default via %s (full tunnel for clients)", lanSubnet, tunIface)
	return nil
}

func ApplyDirectPolicyRouting(cfg RouterConfig) {
	flushVPNPolicyRouting(cfg)
	protectWANManagement(cfg)
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

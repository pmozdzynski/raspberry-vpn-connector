package handlers

import (
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"
)

const vpnDNSStateFile = "/run/vpn-connector/vpn-dns.conf"

type VPNDNSInfo struct {
	Servers []string
	Domains []string
}

func readVPNDNSState() VPNDNSInfo {
	for attempt := 0; attempt < 6; attempt++ {
		info := parseVPNDNSStateFile()
		if len(info.Servers) > 0 {
			return info
		}
		if attempt < 5 {
			time.Sleep(500 * time.Millisecond)
		}
	}
	return parseVPNDNSFromLog(readLogTail(200))
}

func appendDNSServers(info *VPNDNSInfo, raw string, seen map[string]bool) {
	for _, token := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ' ' || r == ',' || r == ';'
	}) {
		token = strings.Trim(token, "\"'")
		ip := net.ParseIP(token)
		if ip == nil || ip.To4() == nil {
			continue
		}
		addr := ip.String()
		if seen[addr] {
			continue
		}
		seen[addr] = true
		info.Servers = append(info.Servers, addr)
	}
}

func appendDomains(info *VPNDNSInfo, raw string, seen map[string]bool) {
	for _, token := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ' ' || r == ',' || r == ';'
	}) {
		d := strings.Trim(strings.TrimSuffix(strings.Trim(token, "\"'"), "."), " ")
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		info.Domains = append(info.Domains, d)
	}
}

func parseVPNDNSStateFile() VPNDNSInfo {
	data, err := os.ReadFile(vpnDNSStateFile)
	if err != nil {
		return VPNDNSInfo{}
	}
	var info VPNDNSInfo
	seenDNS := map[string]bool{}
	seenDomain := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok || val == "" {
			continue
		}
		switch key {
		case "dns":
			appendDNSServers(&info, val, seenDNS)
		case "domain":
			appendDomains(&info, val, seenDomain)
		}
	}
	return info
}

func parseVPNDNSFromLog(logContent string) VPNDNSInfo {
	if strings.TrimSpace(logContent) == "" {
		return VPNDNSInfo{}
	}
	var info VPNDNSInfo
	seenDNS := map[string]bool{}
	seenDomain := map[string]bool{}
	for _, line := range strings.Split(logContent, "\n") {
		line = strings.TrimSpace(line)
		lower := strings.ToLower(line)
		if strings.Contains(lower, "dns") {
			for _, token := range strings.Fields(line) {
				ip := net.ParseIP(strings.Trim(token, ",;\"'"))
				if ip != nil && ip.To4() != nil && !seenDNS[ip.String()] {
					seenDNS[ip.String()] = true
					info.Servers = append(info.Servers, ip.String())
				}
			}
		}
		if strings.Contains(lower, "domain") && strings.Contains(line, "=") {
			_, val, ok := strings.Cut(line, "=")
			if ok {
				appendDomains(&info, val, seenDomain)
			}
		}
	}
	return info
}

func renderDnsmasqUpstream(cfg RouterConfig, vpn *VPNDNSInfo) string {
	var lines []string
	vpnServerSet := map[string]bool{}
	if vpn != nil && len(vpn.Servers) > 0 {
		for _, server := range vpn.Servers {
			vpnServerSet[server] = true
		}
		for _, domain := range vpn.Domains {
			for _, server := range vpn.Servers {
				lines = append(lines, "server=/"+domain+"/"+server)
			}
			// Corporate DNS often returns RFC1918; dnsmasq blocks those by default.
			lines = append(lines, "rebind-domain-ok=/"+domain+"/")
		}
	}

	wanDNS := getWANDNSServers(cfg.WANInterface)
	addedWAN := false
	if len(wanDNS) > 0 {
		for _, server := range wanDNS {
			if vpnServerSet[server] {
				continue
			}
			lines = append(lines, "server="+server)
			addedWAN = true
		}
	}
	if !addedWAN {
		lines = append(lines, "server=1.1.1.1", "server=9.9.9.9")
	}
	return strings.Join(lines, "\n") + "\n"
}

func renderDnsmasqDHCPOptions(vpn *VPNDNSInfo) string {
	if vpn == nil || len(vpn.Domains) == 0 {
		return ""
	}
	var lines []string
	lines = append(lines, "dhcp-option=option:domain-name,"+vpn.Domains[0])
	if len(vpn.Domains) > 1 {
		lines = append(lines, "dhcp-option=option:domain-search,"+strings.Join(vpn.Domains, ","))
	} else {
		lines = append(lines, "dhcp-option=option:domain-search,"+vpn.Domains[0])
	}
	return strings.Join(lines, "\n") + "\n"
}

func writeDnsmasqConfig(cfg RouterConfig, vpn *VPNDNSInfo) error {
	if err := ensureDnsmasqInstalled(); err != nil {
		return err
	}
	if err := os.MkdirAll("/etc/dnsmasq.d", 0755); err != nil {
		return err
	}

	netmask := prefixToNetmask(cfg.LANPrefix)
	upstream := renderDnsmasqUpstream(cfg, vpn)
	dhcpDNS := renderDnsmasqDHCPOptions(vpn)

	conf := fmt.Sprintf(`# Managed by vpn-connector
interface=%s
bind-interfaces
listen-address=%s
except-interface=%s
dhcp-range=%s,%s,%s,%dh
dhcp-option=option:router,%s
dhcp-option=option:dns-server,%s
%sno-resolv
%s
`, cfg.LANInterface, cfg.LANAddress, cfg.WANInterface,
		cfg.DHCPRangeStart, cfg.DHCPRangeEnd, netmask, cfg.DHCPLeaseHours,
		cfg.LANAddress, cfg.LANAddress, dhcpDNS, upstream)

	path := "/etc/dnsmasq.d/vpn-connector.conf"
	if err := os.WriteFile(path, []byte(conf), 0644); err != nil {
		return err
	}

	if out, err := exec.Command("dnsmasq", "--test").CombinedOutput(); err != nil {
		return fmt.Errorf("dnsmasq config test failed: %v: %s", err, strings.TrimSpace(string(out)))
	}

	exec.Command("systemctl", "enable", "dnsmasq").Run()
	if out, err := exec.Command("systemctl", "restart", "dnsmasq").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl restart dnsmasq: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func ApplyVPNDNS() error {
	info := readVPNDNSState()
	cfg := GetRouterConfig()
	if len(info.Servers) == 0 {
		log.Printf("VPN DNS: no tunnel DNS servers in %s; using WAN upstream only", vpnDNSStateFile)
	} else {
		log.Printf("VPN DNS: upstream %v domains %v", info.Servers, info.Domains)
	}
	if err := writeDnsmasqConfig(cfg, &info); err != nil {
		log.Printf("VPN DNS: %v", err)
		return err
	}
	return nil
}

func ApplyDirectDNS() error {
	cfg := GetRouterConfig()
	if err := writeDnsmasqConfig(cfg, nil); err != nil {
		log.Printf("direct DNS: %v", err)
		return err
	}
	return nil
}

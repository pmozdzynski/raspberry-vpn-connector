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
	var info VPNDNSInfo
	for attempt := 0; attempt < 6; attempt++ {
		info = mergeVPNDNS(info, parseVPNDNSStateFile())
		if len(info.Servers) > 0 && len(info.Domains) > 0 {
			return info
		}
		if attempt < 5 {
			time.Sleep(500 * time.Millisecond)
		}
	}
	info = mergeVPNDNS(info, parseResolvConfVPNDNS())
	if len(info.Servers) == 0 {
		info = mergeVPNDNS(info, parseVPNDNSFromLog(readLogTail(200)))
	}
	return info
}

func mergeVPNDNS(base, extra VPNDNSInfo) VPNDNSInfo {
	seenDNS := map[string]bool{}
	seenDomain := map[string]bool{}
	out := VPNDNSInfo{}
	for _, s := range append(base.Servers, extra.Servers...) {
		if s == "" || seenDNS[s] {
			continue
		}
		seenDNS[s] = true
		out.Servers = append(out.Servers, s)
	}
	for _, d := range append(base.Domains, extra.Domains...) {
		if d == "" || seenDomain[d] {
			continue
		}
		seenDomain[d] = true
		out.Domains = append(out.Domains, d)
	}
	return out
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
		if d == "" || isIgnoredSearchDomain(d) || seen[d] {
			continue
		}
		seen[d] = true
		info.Domains = append(info.Domains, d)
	}
}

func isIgnoredSearchDomain(domain string) bool {
	switch strings.ToLower(domain) {
	case "lan", "local", "home", "localdomain":
		return true
	default:
		return false
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

func parseResolvConfVPNDNS() VPNDNSInfo {
	data, err := os.ReadFile("/etc/resolv.conf")
	if err != nil {
		return VPNDNSInfo{}
	}
	var info VPNDNSInfo
	seenDNS := map[string]bool{}
	seenDomain := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "nameserver "):
			appendDNSServers(&info, strings.TrimSpace(strings.TrimPrefix(line, "nameserver ")), seenDNS)
		case strings.HasPrefix(line, "search "):
			appendDomains(&info, strings.TrimSpace(strings.TrimPrefix(line, "search ")), seenDomain)
		case strings.HasPrefix(line, "domain "):
			appendDomains(&info, strings.TrimSpace(strings.TrimPrefix(line, "domain ")), seenDomain)
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

func vpnDNSServerLine(domain, server, tunIface string) string {
	line := "server=/" + domain + "/" + server
	if tunIface != "" {
		line += "@" + tunIface
	}
	return line
}

func getPublicDNSServers(cfg RouterConfig) []string {
	if servers := getInterfaceDNSServers(cfg.WANInterface); len(servers) > 0 {
		return servers
	}
	if gw := resolvedWANGateway(cfg); gw != "" {
		return []string{gw}
	}
	return []string{"1.1.1.1", "9.9.9.9"}
}

func getInterfaceDNSServers(iface string) []string {
	if iface == "" {
		return nil
	}
	output, err := execCommandOutput("resolvectl", "dns", iface)
	if err != nil || output == "" {
		return nil
	}
	var servers []string
	for _, part := range strings.Fields(strings.TrimPrefix(output, iface+":")) {
		if net.ParseIP(part) != nil {
			servers = append(servers, part)
		}
	}
	return servers
}

func renderDnsmasqUpstream(cfg RouterConfig, vpn *VPNDNSInfo, tunIface string) string {
	var lines []string
	if vpn != nil && len(vpn.Servers) > 0 && len(vpn.Domains) > 0 {
		for _, domain := range vpn.Domains {
			for _, server := range vpn.Servers {
				lines = append(lines, vpnDNSServerLine(domain, server, tunIface))
			}
			lines = append(lines, "rebind-domain-ok=/"+domain+"/")
		}
	}

	for _, server := range getPublicDNSServers(cfg) {
		lines = append(lines, "server="+server)
	}
	if len(lines) == 0 {
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

func writeDnsmasqConfig(cfg RouterConfig, vpn *VPNDNSInfo, tunIface string) error {
	if err := ensureDnsmasqInstalled(); err != nil {
		return err
	}
	if err := os.MkdirAll("/etc/dnsmasq.d", 0755); err != nil {
		return err
	}

	netmask := prefixToNetmask(cfg.LANPrefix)
	upstream := renderDnsmasqUpstream(cfg, vpn, tunIface)
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
	tun := GetVPNState().TunIface
	if tun == "" {
		tun = "vpn0"
	}
	if len(info.Servers) == 0 {
		log.Printf("VPN DNS: no tunnel DNS servers found; public DNS only")
	} else if len(info.Domains) == 0 {
		log.Printf("VPN DNS: servers %v but no domains; public DNS only", info.Servers)
	} else {
		log.Printf("VPN DNS: zones %v via %v@%s", info.Domains, info.Servers, tun)
	}
	if err := writeDnsmasqConfig(cfg, &info, tun); err != nil {
		log.Printf("VPN DNS: %v", err)
		return err
	}
	return nil
}

func ApplyDirectDNS() error {
	cfg := GetRouterConfig()
	if err := writeDnsmasqConfig(cfg, nil, ""); err != nil {
		log.Printf("direct DNS: %v", err)
		return err
	}
	return nil
}

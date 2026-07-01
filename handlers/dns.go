package handlers

import (
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"strings"
)

const vpnDNSStateFile = "/run/vpn-connector/vpn-dns.conf"

type VPNDNSInfo struct {
	Servers []string
	Domains []string
}

func readVPNDNSState() VPNDNSInfo {
	info := parseVPNDNSStateFile()
	if len(info.Servers) > 0 {
		return info
	}
	return parseVPNDNSFromLog(readLogTail(200))
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
			if net.ParseIP(val) != nil && !seenDNS[val] {
				seenDNS[val] = true
				info.Servers = append(info.Servers, val)
			}
		case "domain":
			d := strings.TrimSuffix(strings.TrimSpace(val), ".")
			if d != "" && !seenDomain[d] {
				seenDomain[d] = true
				info.Domains = append(info.Domains, d)
			}
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
				d := strings.Trim(strings.TrimSpace(val), ",;\"'")
				d = strings.TrimSuffix(d, ".")
				if d != "" && !strings.Contains(d, " ") && !seenDomain[d] {
					seenDomain[d] = true
					info.Domains = append(info.Domains, d)
				}
			}
		}
	}
	return info
}

func renderDnsmasqUpstream(cfg RouterConfig, vpn *VPNDNSInfo) string {
	var lines []string
	if vpn != nil && len(vpn.Servers) > 0 {
		for _, domain := range vpn.Domains {
			for _, server := range vpn.Servers {
				lines = append(lines, "server=/"+domain+"/"+server)
			}
		}
		for _, server := range vpn.Servers {
			lines = append(lines, "server="+server)
		}
	}

	wanDNS := getWANDNSServers(cfg.WANInterface)
	if len(wanDNS) > 0 {
		for _, server := range wanDNS {
			lines = append(lines, "server="+server)
		}
	} else if len(lines) == 0 {
		lines = append(lines, "server=1.1.1.1", "server=9.9.9.9")
	} else {
		lines = append(lines, "server=1.1.1.1")
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

	conf := fmt.Sprintf(`# Managed by vpn-connector
interface=%s
bind-interfaces
listen-address=%s
except-interface=%s
dhcp-range=%s,%s,%s,%dh
dhcp-option=option:router,%s
dhcp-option=option:dns-server,%s
no-resolv
%s
`, cfg.LANInterface, cfg.LANAddress, cfg.WANInterface,
		cfg.DHCPRangeStart, cfg.DHCPRangeEnd, netmask, cfg.DHCPLeaseHours,
		cfg.LANAddress, cfg.LANAddress, upstream)

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

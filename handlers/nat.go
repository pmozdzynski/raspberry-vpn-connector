package handlers

import (
	"log"
	"os"
	"os/exec"
)

const (
	natChain   = "VPN-CONNECTOR-NAT"
	fwdChain   = "VPN-CONNECTOR-FWD"
	inputChain = "VPN-CONNECTOR-IN"
)

func ensureIPTablesChains() {
	exec.Command("iptables", "-N", fwdChain).Run()
	exec.Command("iptables", "-t", "nat", "-N", natChain).Run()
	if exec.Command("iptables", "-C", "FORWARD", "-j", fwdChain).Run() != nil {
		exec.Command("iptables", "-A", "FORWARD", "-j", fwdChain).Run()
	}
	if exec.Command("iptables", "-t", "nat", "-C", "POSTROUTING", "-j", natChain).Run() != nil {
		exec.Command("iptables", "-t", "nat", "-A", "POSTROUTING", "-j", natChain).Run()
	}
}

func flushRouterRules() {
	ensureIPTablesChains()
	exec.Command("iptables", "-F", fwdChain).Run()
	exec.Command("iptables", "-t", "nat", "-F", natChain).Run()
}

func appendForward(args ...string) {
	ensureIPTablesChains()
	cmdArgs := append([]string{"-A", fwdChain}, args...)
	exec.Command("iptables", cmdArgs...).Run()
}

func appendNAT(outIface string) {
	appendNATFromSource("", outIface)
}

func appendNATFromSource(srcCIDR, outIface string) {
	if outIface == "" {
		return
	}
	ensureIPTablesChains()
	args := []string{"-t", "nat", "-A", natChain}
	if srcCIDR != "" {
		args = append(args, "-s", srcCIDR)
	}
	args = append(args, "-o", outIface, "-j", "MASQUERADE")
	exec.Command("iptables", args...).Run()
}

func ensureForwardAccept() {
	if exec.Command("iptables", "-C", "FORWARD", "-j", fwdChain).Run() != nil {
		exec.Command("iptables", "-I", "FORWARD", "1", "-j", fwdChain).Run()
	}
}

func ensureMSSClamp(outIface string) {
	if outIface == "" {
		return
	}
	spec := []string{"-t", "mangle", "-A", "FORWARD", "-o", outIface, "-p", "tcp",
		"--tcp-flags", "SYN,RST", "SYN", "-j", "TCPMSS", "--clamp-mss-to-pmtu"}
	check := append([]string{"-C"}, spec...)
	if exec.Command("iptables", check...).Run() != nil {
		exec.Command("iptables", spec...).Run()
	}
}

func ApplyDirectNAT() error {
	cfg := GetRouterConfig()
	flushRouterRules()
	EnsureIPForwarding()
	ApplyDirectPolicyRouting(cfg)

	wan := cfg.WANInterface
	if wan == "" {
		var err error
		wan, err = detectDefaultRouteInterface()
		if err != nil {
			wan = "wlan0"
		}
	}

	appendNATFromSource(lanSubnetCIDR(cfg), wan)
	lan := cfg.LANInterface
	if lan != "" {
		appendForward("-i", lan, "-o", wan, "-j", "ACCEPT")
		appendForward("-i", wan, "-o", lan, "-m", "state", "--state", "RELATED,ESTABLISHED", "-j", "ACCEPT")
	}
	_ = ApplyDirectDNS()
	log.Printf("Direct NAT via %s", wan)
	return nil
}

func ApplyVPNNAT(tunIface string) error {
	cfg := GetRouterConfig()
	flushRouterRules()
	EnsureIPForwarding()

	if err := ApplyVPNPolicyRouting(cfg, tunIface, activeVPNServerURL()); err != nil {
		log.Printf("VPN policy routing: %v", err)
	}

	wan := cfg.WANInterface
	if wan == "" {
		var err error
		wan, err = detectDefaultRouteInterface()
		if err != nil {
			wan = "eth0"
		}
	}

	lanCIDR := lanSubnetCIDR(cfg)
	if lanCIDR != "" {
		appendNATFromSource(lanCIDR, tunIface)
		appendNATFromSource(lanCIDR, wan)
	} else {
		appendNAT(tunIface)
		appendNAT(wan)
	}
	lan := cfg.LANInterface
	if lan != "" {
		appendForward("-i", lan, "-o", tunIface, "-j", "ACCEPT")
		appendForward("-i", tunIface, "-o", lan, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT")
		appendForward("-i", lan, "-o", wan, "-j", "ACCEPT")
		appendForward("-i", wan, "-o", lan, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT")
		ensureMSSClamp(tunIface)
		ensureMSSClamp(wan)
	}
	ensureForwardAccept()
	_ = ApplyVPNDNS()
	log.Printf("VPN NAT via %s + internet via %s (split-tunnel)", tunIface, wan)
	return nil
}

func EnsureIPForwarding() error {
	settings := map[string]string{
		"net.ipv4.ip_forward":              "1",
		"net.ipv4.conf.all.forwarding":     "1",
		"net.ipv4.conf.default.forwarding": "1",
	}
	for k, v := range settings {
		exec.Command("sysctl", "-w", k+"="+v).Run()
	}
	if err := persistIPForwarding(); err != nil {
		log.Printf("persist IP forwarding: %v", err)
	}
	ensureManagementFirewall(GetRouterConfig())
	return nil
}

func ensureManagementFirewall(cfg RouterConfig) {
	exec.Command("iptables", "-N", inputChain).Run()
	exec.Command("iptables", "-F", inputChain).Run()

	rules := [][]string{
		{"-i", "lo", "-j", "ACCEPT"},
		{"-p", "tcp", "--dport", "5000", "-j", "ACCEPT"},
		{"-p", "tcp", "--sport", "5000", "-m", "conntrack", "--ctstate", "ESTABLISHED,RELATED", "-j", "ACCEPT"},
	}
	for _, iface := range []string{cfg.WANInterface, cfg.LANInterface} {
		if iface == "" {
			continue
		}
		rules = append(rules,
			[]string{"-i", iface, "-m", "addrtype", "--dst-type", "LOCAL", "-j", "ACCEPT"},
			[]string{"-i", iface, "-p", "tcp", "--dport", "5000", "-j", "ACCEPT"},
			[]string{"-i", iface, "-p", "udp", "--dport", "53", "-j", "ACCEPT"},
			[]string{"-i", iface, "-p", "tcp", "--dport", "53", "-j", "ACCEPT"},
		)
	}
	for _, spec := range rules {
		exec.Command("iptables", append([]string{"-A", inputChain}, spec...)...).Run()
	}

	// NetworkManager may insert INPUT rules when tun0 appears; keep our jump first.
	_ = exec.Command("iptables", "-D", "INPUT", "-j", inputChain).Run()
	exec.Command("iptables", "-I", "INPUT", "1", "-j", inputChain).Run()
}

func ensureManagementPortOpen() {
	ensureManagementFirewall(GetRouterConfig())
}

func persistIPForwarding() error {
	const path = "/etc/sysctl.d/99-vpn-connector.conf"
	content := `# Managed by vpn-connector
net.ipv4.ip_forward=1
net.ipv4.conf.all.forwarding=1
net.ipv4.conf.default.forwarding=1
`
	if existing, err := os.ReadFile(path); err == nil && string(existing) == content {
		return nil
	}
	return os.WriteFile(path, []byte(content), 0644)
}

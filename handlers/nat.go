package handlers

import (
	"log"
	"os"
	"os/exec"
)

const (
	natChain = "VPN-CONNECTOR-NAT"
	fwdChain = "VPN-CONNECTOR-FWD"
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
	ensureIPTablesChains()
	exec.Command("iptables", "-t", "nat", "-A", natChain, "-o", outIface, "-j", "MASQUERADE").Run()
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

	appendNAT(wan)
	lan := cfg.LANInterface
	if lan != "" {
		appendForward("-i", lan, "-o", wan, "-j", "ACCEPT")
		appendForward("-i", wan, "-o", lan, "-m", "state", "--state", "RELATED,ESTABLISHED", "-j", "ACCEPT")
	}
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

	appendNAT(tunIface)
	lan := cfg.LANInterface
	if lan != "" {
		appendForward("-i", lan, "-o", tunIface, "-j", "ACCEPT")
		appendForward("-i", tunIface, "-o", lan, "-m", "state", "--state", "RELATED,ESTABLISHED", "-j", "ACCEPT")
	}
	log.Printf("VPN NAT via %s", tunIface)
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
	ensureManagementPortOpen()
	return nil
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

func ensureManagementPortOpen() {
	if exec.Command("iptables", "-C", "INPUT", "-p", "tcp", "--dport", "5000", "-j", "ACCEPT").Run() != nil {
		exec.Command("iptables", "-I", "INPUT", "1", "-p", "tcp", "--dport", "5000", "-j", "ACCEPT").Run()
	}
}

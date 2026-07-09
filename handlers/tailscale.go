package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"strings"
)

const (
	tailscaleInterface  = "tailscale0"
	tailscaleSourceCIDR = "100.64.0.0/10"
)

type TailscaleStatus struct {
	Installed       bool     `json:"installed"`
	Running         bool     `json:"running"`
	ExitNodeEnabled bool     `json:"exit_node_enabled"`
	Advertised      bool     `json:"advertised"`
	IPv4            string   `json:"ipv4,omitempty"`
	Hostname        string   `json:"hostname,omitempty"`
	VPNConnected    bool     `json:"vpn_connected"`
	CorpDNSDomains  []string `json:"corp_dns_domains,omitempty"`
}

type tailscaleStatusJSON struct {
	Self struct {
		Online       bool     `json:"Online"`
		TailscaleIPs []string `json:"TailscaleIPs"`
		DNSName      string   `json:"DNSName"`
	} `json:"Self"`
	ExitNodeStatus struct {
		ExitNodeOption bool `json:"ExitNodeOption"`
	} `json:"ExitNodeStatus"`
}

func TailscaleInstalled() bool {
	return commandExists("tailscale")
}

func TailscaleRunning() bool {
	if !TailscaleInstalled() {
		return false
	}
	return exec.Command("tailscale", "status", "--json").Run() == nil
}

func TailscaleExitNodeConfigured() bool {
	return GetRouterConfig().TailscaleExitNodeEnabled
}

func GetTailscaleStatus() TailscaleStatus {
	st := TailscaleStatus{
		Installed:       TailscaleInstalled(),
		ExitNodeEnabled: TailscaleExitNodeConfigured(),
		VPNConnected:    GetVPNState().Connected,
	}
	if !st.Installed {
		return st
	}

	parsed, err := parseTailscaleStatusJSON()
	if err != nil {
		return st
	}

	st.Running = parsed.Self.Online || len(parsed.Self.TailscaleIPs) > 0
	st.Advertised = parsed.ExitNodeStatus.ExitNodeOption
	st.IPv4 = tailscaleIPv4FromStatus(parsed)
	st.Hostname = strings.TrimSuffix(parsed.Self.DNSName, ".")

	if st.VPNConnected {
		info := readVPNDNSState()
		st.CorpDNSDomains = info.Domains
	}
	return st
}

func parseTailscaleStatusJSON() (tailscaleStatusJSON, error) {
	var parsed tailscaleStatusJSON
	out, err := exec.Command("tailscale", "status", "--json").Output()
	if err != nil {
		return parsed, err
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return parsed, err
	}
	return parsed, nil
}

func tailscaleIPv4FromStatus(st tailscaleStatusJSON) string {
	for _, ip := range st.Self.TailscaleIPs {
		if strings.Contains(ip, ".") {
			return ip
		}
	}
	return tailscaleIPv4FromInterface()
}

func tailscaleIPv4FromInterface() string {
	ip, _ := getInterfaceIPv4CIDR(tailscaleInterface)
	return ip
}

func SetTailscaleExitNode(enabled bool) error {
	cfg := GetRouterConfig()
	cfg.TailscaleExitNodeEnabled = enabled
	if err := SaveRouterConfig(cfg); err != nil {
		return err
	}
	return ApplyTailscaleExitNode()
}

func ApplyTailscaleExitNode() error {
	if !TailscaleInstalled() {
		if TailscaleExitNodeConfigured() {
			return fmt.Errorf("tailscale is not installed; install it and log in, then retry")
		}
		return nil
	}
	if !TailscaleRunning() {
		if TailscaleExitNodeConfigured() {
			return fmt.Errorf("tailscale is installed but not running; run: tailscale up")
		}
		return nil
	}

	// Routers must not use Tailscale MagicDNS for their own resolution (breaks OpenConnect hostname lookup).
	if out, err := exec.Command("tailscale", "set", "--accept-dns=false").CombinedOutput(); err != nil {
		log.Printf("Tailscale accept-dns=false: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if err := EnsureSystemResolver(GetRouterConfig()); err != nil {
		log.Printf("System DNS restore: %v", err)
	}

	enabled := TailscaleExitNodeConfigured()
	args := []string{"set", "--advertise-exit-node=" + boolString(enabled)}
	if out, err := exec.Command("tailscale", args...).CombinedOutput(); err != nil {
		if enabled {
			fallback := exec.Command("tailscale", "up", "--advertise-exit-node")
			if out2, err2 := fallback.CombinedOutput(); err2 != nil {
				return fmt.Errorf("tailscale advertise exit node: %v: %s; fallback: %v: %s",
					err, strings.TrimSpace(string(out)), err2, strings.TrimSpace(string(out2)))
			}
		} else {
			return fmt.Errorf("tailscale disable exit node: %v: %s", err, strings.TrimSpace(string(out)))
		}
	}

	if enabled {
		log.Printf("Tailscale exit node advertised (filtered: corp via VPN when connected, else WAN)")
	} else {
		log.Printf("Tailscale exit node advertisement disabled")
	}

	return reapplyRoutingForTailscale()
}

func reapplyRoutingForTailscale() error {
	st := GetVPNState()
	if st.Connected && st.TunIface != "" && interfaceUp(st.TunIface) {
		return ApplyVPNNAT(st.TunIface)
	}
	return ApplyDirectNAT()
}

func appendTailscaleNAT(wan, tunIface string) {
	if !TailscaleExitNodeConfigured() {
		return
	}
	if wan != "" {
		appendNATFromSource(tailscaleSourceCIDR, wan)
		appendForward("-i", tailscaleInterface, "-o", wan, "-j", "ACCEPT")
		appendForward("-i", wan, "-o", tailscaleInterface, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT")
		ensureMSSClamp(wan)
	}
	if tunIface != "" && interfaceUp(tunIface) {
		appendNATFromSource(tailscaleSourceCIDR, tunIface)
		appendForward("-i", tailscaleInterface, "-o", tunIface, "-j", "ACCEPT")
		appendForward("-i", tunIface, "-o", tailscaleInterface, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT")
		ensureMSSClamp(tunIface)
	}
}

func tailscaleListenAddresses(cfg RouterConfig) []string {
	if !TailscaleExitNodeConfigured() {
		return nil
	}
	ip := tailscaleIPv4FromInterface()
	if ip == "" {
		parsed, err := parseTailscaleStatusJSON()
		if err == nil {
			ip = tailscaleIPv4FromStatus(parsed)
		}
	}
	if ip == "" {
		return nil
	}
	return []string{ip}
}

func tailscaleDnsmasqInterfaces(cfg RouterConfig) []string {
	if !TailscaleExitNodeConfigured() || !interfaceUp(tailscaleInterface) {
		return nil
	}
	return []string{tailscaleInterface}
}

func EnsureTailscaleRouterDNS() {
	if !TailscaleInstalled() || !TailscaleRunning() {
		return
	}
	_ = exec.Command("tailscale", "set", "--accept-dns=false").Run()
	if err := EnsureSystemResolver(GetRouterConfig()); err != nil {
		log.Printf("Tailscale router DNS: %v", err)
	}
}

func boolString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

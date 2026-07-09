package handlers

import (
	"fmt"
	"log"
	"os/exec"
	"strings"
)

type WiFiAPStatus struct {
	Enabled       bool   `json:"enabled"`
	SSID          string `json:"ssid,omitempty"`
	Interface     string `json:"interface,omitempty"`
	HostapdActive bool   `json:"hostapd_active"`
	InterfaceUp   bool   `json:"interface_up"`
	Beaconing     bool   `json:"beaconing"`
	LastError     string `json:"last_error,omitempty"`
}

func GetWiFiAPStatus() WiFiAPStatus {
	cfg := GetRouterConfig()
	if cfg.LANType != "wireless" {
		if cfg.LANInterface != "" && ResolveInterfaceKind(cfg.LANInterface) == "wireless" {
			cfg.LANType = "wireless"
		} else {
			return WiFiAPStatus{Enabled: false}
		}
	}

	st := WiFiAPStatus{
		Enabled:   true,
		SSID:      cfg.APSSID,
		Interface: cfg.LANInterface,
	}
	st.HostapdActive = exec.Command("systemctl", "is-active", "--quiet", "hostapd").Run() == nil
	st.InterfaceUp = exec.Command("ip", "link", "show", cfg.LANInterface).Run() == nil &&
		!strings.Contains(strings.ToLower(execCommandOutputMust("ip", "-o", "link", "show", cfg.LANInterface)), "state down")

	if out, err := exec.Command("iw", "dev", cfg.LANInterface, "info").CombinedOutput(); err == nil {
		st.Beaconing = strings.Contains(string(out), "type AP")
	} else {
		st.LastError = strings.TrimSpace(string(out))
	}
	return st
}

func execCommandOutputMust(name string, args ...string) string {
	out, _ := exec.Command(name, args...).Output()
	return string(out)
}

func prepareWiFiAPInterface(iface, country string) error {
	if iface == "" {
		return fmt.Errorf("LAN WiFi interface is required")
	}

	_ = exec.Command("rfkill", "unblock", "wifi").Run()
	if country != "" {
		_ = exec.Command("iw", "reg", "set", country).Run()
	}

	if usesNetworkManager() {
		_ = exec.Command("nmcli", "device", "set", iface, "managed", "no").Run()
		_ = exec.Command("nmcli", "device", "disconnect", iface).Run()
	}

	_ = exec.Command("systemctl", "stop", "wpa_supplicant@"+iface+".service").Run()
	_ = exec.Command("systemctl", "disable", "wpa_supplicant@"+iface+".service").Run()
	_ = exec.Command("pkill", "-f", "wpa_supplicant.*"+iface).Run()

	exec.Command("ip", "link", "set", iface, "down").Run()
	if out, err := exec.Command("iw", "dev", iface, "set", "type", "__ap").CombinedOutput(); err != nil {
		log.Printf("Bootstrap: iw set type __ap on %s: %v: %s", iface, err, strings.TrimSpace(string(out)))
	}
	exec.Command("ip", "link", "set", iface, "up").Run()
	return nil
}

func EnsureWiFiAccessPoint() error {
	cfg := GetRouterConfig()
	if !cfg.Configured || cfg.LANInterface == "" {
		return nil
	}
	lanType := cfg.LANType
	if lanType == "" {
		lanType = ResolveInterfaceKind(cfg.LANInterface)
	}
	if lanType != "wireless" {
		return nil
	}

	if cfg.LANAddress != "" && cfg.LANPrefix > 0 {
		cidr := fmt.Sprintf("%s/%d", cfg.LANAddress, cfg.LANPrefix)
		if err := ensureLANAddress(cfg.LANInterface, cidr); err != nil {
			log.Printf("WiFi AP: LAN address: %v", err)
		}
	}

	if err := prepareWiFiAPInterface(cfg.LANInterface, cfg.WifiCountry); err != nil {
		return err
	}
	if err := prepareHostapd(cfg); err != nil {
		return err
	}
	if exec.Command("systemctl", "is-active", "--quiet", "hostapd").Run() != nil {
		return activateHostapd()
	}
	return nil
}

func EnsureRouterServices() error {
	cfg := GetRouterConfig()
	st := GetVPNState()
	if st.Connected {
		MaintainManagementAccess(cfg, st.ServerURL)
		EnsureVPNDNSIfNeeded()
	} else {
		MaintainManagementAccess(cfg, "")
	}

	if err := EnsureWiFiAccessPoint(); err != nil {
		log.Printf("Ensure WiFi AP: %v", err)
	}
	if exec.Command("systemctl", "is-enabled", "--quiet", "dnsmasq").Run() == nil {
		_ = exec.Command("systemctl", "restart", "dnsmasq").Run()
	}
	EnsureTailscaleRouterDNS()
	if err := ApplyTailscaleExitNode(); err != nil {
		log.Printf("Tailscale exit node: %v", err)
	}
	return nil
}

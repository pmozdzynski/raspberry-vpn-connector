package handlers

import (
	"fmt"
	"log"
	"os/exec"
	"strings"
)

type PackageSnapshot struct {
	AptAvailable bool `json:"apt_available"`
	Dnsmasq      bool `json:"dnsmasq"`
	Hostapd      bool `json:"hostapd"`
	OpenConnect  bool `json:"openconnect"`
	Iptables     bool `json:"iptables"`
	Tailscale    bool `json:"tailscale"`
}

func GetPackageSnapshot() PackageSnapshot {
	return PackageSnapshot{
		AptAvailable: commandExists("apt-get"),
		Dnsmasq:      isDnsmasqInstalled(),
		Hostapd:      commandExists("hostapd"),
		OpenConnect:  commandExists("openconnect"),
		Iptables:     commandExists("iptables"),
		Tailscale:    commandExists("tailscale"),
	}
}

func isDnsmasqInstalled() bool {
	out, err := exec.Command("dpkg-query", "-W", "-f=${Status}", "dnsmasq").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "install ok installed")
}

func installSystemPackages() error {
	required := []string{
		"dnsmasq",
		"openconnect",
		"vpnc-scripts",
		"iptables",
		"iproute2",
	}
	if !commandExists("apt-get") {
		return fmt.Errorf("apt-get not available; install packages manually: %s", strings.Join(required, ", "))
	}

	log.Println("Bootstrap: apt-get update")
	if out, err := exec.Command("apt-get", "update").CombinedOutput(); err != nil {
		return fmt.Errorf("apt-get update: %v: %s", err, strings.TrimSpace(string(out)))
	}

	args := append([]string{"install", "-y"}, required...)
	log.Println("Bootstrap: apt-get install", strings.Join(required, " "))
	if out, err := exec.Command("apt-get", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("apt-get install: %v: %s", err, strings.TrimSpace(string(out)))
	}
	exec.Command("systemctl", "daemon-reload").Run()
	return nil
}

func ensureDnsmasqInstalled() error {
	if isDnsmasqInstalled() {
		return nil
	}
	return installSystemPackages()
}

func ensureHostapdInstalled() error {
	if commandExists("hostapd") {
		return nil
	}
	return installSystemPackages()
}

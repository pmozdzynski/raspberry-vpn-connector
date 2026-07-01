package handlers

import (
	"os/exec"
	"strings"
)

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func execCommandOutput(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output()
	return strings.TrimSpace(string(out)), err
}

func IsIPForwardingEnabled() bool {
	out, err := execCommandOutput("sysctl", "-n", "net.ipv4.ip_forward")
	return err == nil && strings.TrimSpace(out) == "1"
}

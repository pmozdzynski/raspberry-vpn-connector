package handlers

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

type NetworkInterface struct {
	Name           string   `json:"name"`
	MAC            string   `json:"mac"`
	State          string   `json:"state"`
	IPv4           []string `json:"ipv4"`
	IsDefaultRoute bool     `json:"is_default_route"`
	Kind           string   `json:"kind"`
}

type RoutingSummary struct {
	DefaultInterface string `json:"default_interface"`
	DefaultGateway   string `json:"default_gateway"`
	IPForwarding     bool   `json:"ip_forwarding"`
}

type VPNSnapshot struct {
	Installed bool `json:"installed"`
	Running   bool `json:"running"`
	State     VPNState `json:"state"`
}

type NetworkSnapshot struct {
	Interfaces    []NetworkInterface `json:"interfaces"`
	Routing       RoutingSummary     `json:"routing"`
	VPN           VPNSnapshot        `json:"vpn"`
	Packages      PackageSnapshot    `json:"packages"`
	SuggestedLAN  SuggestedLANConfig `json:"suggested_lan"`
	ManagementIPs []string           `json:"management_ips"`
	Hostname      string             `json:"hostname"`
	Configured    bool               `json:"configured"`
	Config        RouterConfig       `json:"config"`
}

func GetNetworkSnapshot() NetworkSnapshot {
	defaultIface, defaultGW := getDefaultRoute()
	return NetworkSnapshot{
		Interfaces: listNetworkInterfaces(defaultIface),
		Routing: RoutingSummary{
			DefaultInterface: defaultIface,
			DefaultGateway:   defaultGW,
			IPForwarding:     IsIPForwardingEnabled(),
		},
		VPN:           getVPNSnapshot(),
		Packages:      GetPackageSnapshot(),
		SuggestedLAN:  SuggestLANSubnet(defaultIface),
		ManagementIPs: getManagementAccessIPs(),
		Hostname:      getSystemHostname(),
		Configured:    IsConfigured(),
		Config:        GetRouterConfig(),
	}
}

func getVPNSnapshot() VPNSnapshot {
	snap := VPNSnapshot{State: GetVPNState()}
	snap.Installed = commandExists("openconnect")
	snap.Running = snap.State.Connected
	return snap
}

func getSystemHostname() string {
	name, err := execCommandOutput("hostname")
	if err != nil || name == "" {
		return "vpn-connector"
	}
	return name
}

func listNetworkInterfaces(defaultIface string) []NetworkInterface {
	output, err := exec.Command("ip", "-j", "link", "show").Output()
	if err == nil {
		return parseJSONLinks(output, defaultIface)
	}
	return parseTextLinks(defaultIface)
}

type ipLinkJSON struct {
	IfName    string `json:"ifname"`
	OperState string `json:"operstate"`
	Address   string `json:"address"`
	LinkType  string `json:"link_type"`
}

func parseJSONLinks(output []byte, defaultIface string) []NetworkInterface {
	var links []ipLinkJSON
	if err := json.Unmarshal(output, &links); err != nil {
		return parseTextLinks(defaultIface)
	}
	ipv4Map := getIPv4ByInterface()
	var result []NetworkInterface
	for _, link := range links {
		if shouldSkipInterface(link.IfName) {
			continue
		}
		result = append(result, NetworkInterface{
			Name:           link.IfName,
			MAC:            link.Address,
			State:          strings.ToLower(link.OperState),
			IPv4:           ipv4Map[link.IfName],
			IsDefaultRoute: link.IfName == defaultIface,
			Kind:           classifyInterface(link.IfName, link.LinkType),
		})
	}
	return result
}

func parseTextLinks(defaultIface string) []NetworkInterface {
	output, err := exec.Command("sh", "-c", "ip -o link show | awk -F': ' '{print $2}'").Output()
	if err != nil {
		return nil
	}
	ipv4Map := getIPv4ByInterface()
	var result []NetworkInterface
	for _, name := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		name = strings.TrimSpace(name)
		if shouldSkipInterface(name) {
			continue
		}
		result = append(result, NetworkInterface{
			Name:           name,
			State:          "unknown",
			IPv4:           ipv4Map[name],
			IsDefaultRoute: name == defaultIface,
			Kind:           classifyInterface(name, ""),
		})
	}
	return result
}

func getIPv4ByInterface() map[string][]string {
	result := make(map[string][]string)
	output, err := exec.Command("ip", "-o", "-4", "addr", "show").Output()
	if err != nil {
		return result
	}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		name := strings.TrimSuffix(fields[1], ":")
		addr := strings.Split(fields[3], "/")[0]
		result[name] = append(result[name], addr)
	}
	return result
}

func getDefaultRoute() (iface, gateway string) {
	output, err := exec.Command("ip", "route", "show", "default").Output()
	if err != nil {
		return "", ""
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) == 0 {
		return "", ""
	}
	fields := strings.Fields(lines[0])
	for i, field := range fields {
		switch field {
		case "dev":
			if i+1 < len(fields) {
				iface = fields[i+1]
			}
		case "via":
			if i+1 < len(fields) {
				gateway = fields[i+1]
			}
		}
	}
	return iface, gateway
}

func detectDefaultRouteInterface() (string, error) {
	iface, _ := getDefaultRoute()
	if iface == "" {
		return "", fmt.Errorf("no default route interface detected")
	}
	return iface, nil
}

func shouldSkipInterface(name string) bool {
	if name == "" || name == "lo" {
		return true
	}
	if strings.HasPrefix(name, "docker") || strings.HasPrefix(name, "veth") || strings.HasPrefix(name, "br-") {
		return true
	}
	if strings.HasPrefix(name, "tun") || strings.HasPrefix(name, "tap") {
		return true
	}
	return false
}

func classifyInterface(name, linkType string) string {
	lower := strings.ToLower(name + " " + linkType)
	switch {
	case strings.Contains(lower, "wlan") || strings.Contains(lower, "wifi"):
		return "wireless"
	case strings.Contains(lower, "eth") || linkType == "ether":
		return "ethernet"
	default:
		return "other"
	}
}

func ResolveInterfaceKind(name string) string {
	for _, iface := range listNetworkInterfaces("") {
		if iface.Name == name {
			return iface.Kind
		}
	}
	return classifyInterface(name, "")
}

func IsWirelessInterface(name string) bool {
	return ResolveInterfaceKind(name) == "wireless"
}

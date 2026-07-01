package handlers

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
)

func ApplyBootstrap(cfg RouterConfig) error {
	return ApplyBootstrapWithProgress(cfg, nil)
}

func ApplyBootstrapWithProgress(cfg RouterConfig, progress setupProgressReporter) error {
	if cfg.LANInterface == "" {
		return fmt.Errorf("LAN interface is required")
	}
	if cfg.LANAddress == "" {
		return fmt.Errorf("LAN address is required")
	}
	if cfg.LANPrefix == 0 {
		cfg.LANPrefix = 24
	}
	if cfg.DHCPRangeStart == "" || cfg.DHCPRangeEnd == "" {
		return fmt.Errorf("DHCP range is required")
	}
	if cfg.AdminUsername == "" {
		cfg.AdminUsername = "admin"
	}
	if cfg.AdminPassword == "" {
		return fmt.Errorf("admin password is required")
	}
	if cfg.DHCPLeaseHours == 0 {
		cfg.DHCPLeaseHours = 12
	}

	if cfg.WANInterface == "" {
		iface, err := detectDefaultRouteInterface()
		if err != nil {
			return fmt.Errorf("WAN interface not selected and no default route detected")
		}
		cfg.WANInterface = iface
	}
	if cfg.WANInterface == cfg.LANInterface {
		return fmt.Errorf("WAN and LAN must be different interfaces")
	}
	if err := validateLANDoesNotOverlapWAN(cfg.WANInterface, cfg.LANAddress, cfg.LANPrefix); err != nil {
		return err
	}

	cfg.WANType = ResolveInterfaceKind(cfg.WANInterface)
	cfg.LANType = ResolveInterfaceKind(cfg.LANInterface)

	if cfg.LANType == "wireless" {
		if cfg.APSSID == "" {
			cfg.APSSID = "vpn-connector"
		}
		if cfg.APPassword == "" {
			cfg.APPassword = "vpn-connector-wifi"
		}
		if cfg.WifiCountry == "" {
			cfg.WifiCountry = "PL"
		}
	}

	steps := []struct {
		name string
		fn   func() error
	}{
		{"install system packages", installSystemPackages},
		{"enable IP forwarding", func() error { return EnsureIPForwarding() }},
		{"verify WAN stays on DHCP", func() error { return ensureWANDHCP(cfg) }},
		{"configure LAN interface", func() error { return configureLANInterface(cfg) }},
	}

	if cfg.LANType == "wireless" {
		steps = append(steps, struct {
			name string
			fn   func() error
		}{"prepare WiFi access point", func() error { return prepareHostapd(cfg) }})
	} else {
		steps = append(steps, struct {
			name string
			fn   func() error
		}{"disable WiFi access point", disableHostapd})
	}

	steps = append(steps, struct {
		name string
		fn   func() error
	}{"configure dnsmasq", func() error { return configureDnsmasq(cfg) }})

	for _, step := range steps {
		log.Printf("Bootstrap: %s", step.name)
		progress.running(step.name, "started")
		if err := step.fn(); err != nil {
			progress.fail(step.name, err.Error())
			return fmt.Errorf("%s: %w", step.name, err)
		}
		progress.ok(step.name, "completed")
		if step.name == "configure LAN interface" {
			progress.warn("management access", FormatManagementHint(cfg))
		}
	}

	progress.running("save configuration", "writing /etc/vpn-connector/config.json")
	cfg.Configured = true
	if err := SaveRouterConfig(cfg); err != nil {
		progress.fail("save configuration", err.Error())
		return fmt.Errorf("save config: %w", err)
	}
	progress.ok("save configuration", "saved")

	progress.running("initial routing", "direct NAT via WAN")
	if err := ApplyDirectNAT(); err != nil {
		progress.fail("initial routing", err.Error())
		return err
	}
	progress.ok("initial routing", "direct NAT active")

	if cfg.LANType == "wireless" {
		progress.running("start WiFi access point", "activating hostapd (WAN management should stay up)")
		if err := ensureWANReachable(cfg); err != nil {
			progress.warn("start WiFi access point", err.Error())
		}
		if err := activateHostapd(); err != nil {
			progress.fail("start WiFi access point", err.Error())
			return fmt.Errorf("start WiFi access point: %w", err)
		}
		progress.ok("start WiFi access point", "completed")
		progress.warn("management access", FormatManagementHint(cfg)+". If WiFi WAN dropped, use Ethernet WAN or connect to the LAN AP and open the LAN gateway URL.")
	}

	log.Printf("Bootstrap completed: WAN %s (%s), LAN %s (%s)", cfg.WANInterface, cfg.WANType, cfg.LANInterface, cfg.LANType)
	return nil
}

func ensureWANDHCP(cfg RouterConfig) error {
	wanIP, wanPrefix := getInterfaceIPv4CIDR(cfg.WANInterface)
	kind := cfg.WANType
	if kind == "" {
		kind = ResolveInterfaceKind(cfg.WANInterface)
	}
	if wanIP != "" {
		log.Printf("Bootstrap: WAN %s (%s) uses %s/%d (unchanged)", cfg.WANInterface, kind, wanIP, wanPrefix)
	} else if kind == "wireless" {
		log.Printf("Bootstrap: WAN %s (WiFi) has no IPv4 yet; connect to your WiFi network before routing traffic", cfg.WANInterface)
	} else {
		log.Printf("Bootstrap: WAN %s (Ethernet) has no IPv4 yet; plug in cable and wait for DHCP", cfg.WANInterface)
	}
	return nil
}

func configureLANInterface(cfg RouterConfig) error {
	exec.Command("ip", "link", "set", cfg.LANInterface, "up").Run()
	cidr := fmt.Sprintf("%s/%d", cfg.LANAddress, cfg.LANPrefix)
	lanType := cfg.LANType
	if lanType == "" {
		lanType = ResolveInterfaceKind(cfg.LANInterface)
	}

	if lanType == "wireless" {
		return configureWirelessLANInterface(cfg.LANInterface, cidr)
	}

	if usesNetworkManager() {
		return configureNMEthernetLAN(cfg.LANInterface, cidr)
	}

	if dhcpcdUsable() {
		block := fmt.Sprintf("\ninterface %s\nstatic ip_address=%s\n", cfg.LANInterface, cidr)
		if err := appendUniqueBlock("/etc/dhcpcd.conf", "interface "+cfg.LANInterface, block, restartDhcpcd); err != nil {
			log.Printf("Bootstrap: dhcpcd LAN config failed, falling back to ip: %v", err)
			return applyStaticIPAddress(cfg.LANInterface, cidr)
		}
		return ensureLANAddress(cfg.LANInterface, cidr)
	}

	return applyStaticIPAddress(cfg.LANInterface, cidr)
}

func configureWirelessLANInterface(iface, cidr string) error {
	if usesNetworkManager() {
		if out, err := exec.Command("nmcli", "device", "set", iface, "managed", "no").CombinedOutput(); err != nil {
			log.Printf("Bootstrap: nmcli device set managed no: %v: %s", err, strings.TrimSpace(string(out)))
		}
		return applyStaticIPAddress(iface, cidr)
	}

	if dhcpcdUsable() {
		block := fmt.Sprintf("\ninterface %s\nstatic ip_address=%s\nnohook wpa_supplicant\n", iface, cidr)
		if err := appendUniqueBlock("/etc/dhcpcd.conf", "interface "+iface, block, restartDhcpcd); err != nil {
			log.Printf("Bootstrap: dhcpcd wireless LAN config failed, falling back to ip: %v", err)
			return applyStaticIPAddress(iface, cidr)
		}
		return ensureLANAddress(iface, cidr)
	}

	return applyStaticIPAddress(iface, cidr)
}

func configureNMEthernetLAN(iface, cidr string) error {
	connName := "vpn-connector-lan"
	_ = exec.Command("nmcli", "device", "disconnect", iface).Run()
	_ = exec.Command("nmcli", "con", "delete", connName).Run()

	cmd := exec.Command("nmcli", "con", "add", "type", "ethernet",
		"ifname", iface,
		"con-name", connName,
		"ipv4.method", "manual",
		"ipv4.addresses", cidr,
		"ipv6.method", "ignore",
		"connection.autoconnect", "yes",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("Bootstrap: nmcli con add failed, falling back to ip: %v: %s", err, strings.TrimSpace(string(out)))
		return applyStaticIPAddress(iface, cidr)
	}
	if out, err := exec.Command("nmcli", "con", "up", connName).CombinedOutput(); err != nil {
		nmOut := strings.TrimSpace(string(out))
		log.Printf("Bootstrap: nmcli con up failed, falling back to ip: %v: %s", err, nmOut)
		if ipErr := applyStaticIPAddress(iface, cidr); ipErr != nil {
			return fmt.Errorf("nmcli con up: %v: %s; ip fallback: %w", err, nmOut, ipErr)
		}
	}
	return ensureLANAddress(iface, cidr)
}

func dhcpcdUsable() bool {
	if !commandExists("dhcpcd") {
		return false
	}
	if exec.Command("systemctl", "is-active", "--quiet", "dhcpcd").Run() == nil {
		return true
	}
	return exec.Command("systemctl", "is-enabled", "--quiet", "dhcpcd").Run() == nil
}

func restartDhcpcd() error {
	out, err := exec.Command("systemctl", "restart", "dhcpcd").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func ensureLANAddress(iface, cidr string) error {
	wantIP := strings.Split(cidr, "/")[0]
	out, err := exec.Command("ip", "-4", "-o", "addr", "show", "dev", iface).Output()
	if err == nil && strings.Contains(string(out), wantIP) {
		return nil
	}
	return applyStaticIPAddress(iface, cidr)
}

func applyStaticIPAddress(iface, cidr string) error {
	exec.Command("ip", "addr", "flush", "dev", iface).Run()
	exec.Command("ip", "link", "set", iface, "up").Run()
	if out, err := exec.Command("ip", "addr", "add", cidr, "dev", iface).CombinedOutput(); err != nil {
		if !strings.Contains(string(out), "File exists") {
			return fmt.Errorf("ip addr add %s dev %s: %v: %s", cidr, iface, err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

func prepareHostapd(cfg RouterConfig) error {
	if err := ensureHostapdInstalled(); err != nil {
		return err
	}

	conf := fmt.Sprintf(`# Managed by vpn-connector
interface=%s
driver=nl80211
ssid=%s
hw_mode=g
channel=6
country_code=%s
ieee80211d=1
ieee80211n=1
wmm_enabled=1
auth_algs=1
wpa=2
wpa_passphrase=%s
wpa_key_mgmt=WPA-PSK
rsn_pairwise=CCMP
ctrl_interface=/var/run/hostapd
ctrl_interface_group=0
`, cfg.LANInterface, cfg.APSSID, cfg.WifiCountry, cfg.APPassword)

	if err := os.WriteFile("/etc/hostapd/hostapd.conf", []byte(conf), 0644); err != nil {
		return err
	}

	defaultPath := "/etc/default/hostapd"
	defaultContent := "DAEMON_CONF=\"/etc/hostapd/hostapd.conf\"\n"
	if data, err := os.ReadFile(defaultPath); err != nil || !strings.Contains(string(data), "DAEMON_CONF") {
		_ = os.WriteFile(defaultPath, []byte(defaultContent), 0644)
	}

	exec.Command("systemctl", "unmask", "hostapd").Run()
	exec.Command("systemctl", "enable", "hostapd").Run()
	return nil
}

func activateHostapd() error {
	cfg := GetRouterConfig()
	if err := prepareWiFiAPInterface(cfg.LANInterface, cfg.WifiCountry); err != nil {
		log.Printf("Bootstrap: prepare WiFi AP interface: %v", err)
	}
	if out, err := exec.Command("systemctl", "restart", "hostapd").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl restart hostapd: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if st := GetWiFiAPStatus(); !st.Beaconing {
		log.Printf("Bootstrap: hostapd started but %s is not in AP mode yet; check iw/hostapd logs", cfg.LANInterface)
	}
	return nil
}

func ensureWANReachable(cfg RouterConfig) error {
	if cfg.WANInterface == "" {
		return nil
	}
	exec.Command("ip", "link", "set", cfg.WANInterface, "up").Run()
	wanIP, _ := getInterfaceIPv4CIDR(cfg.WANInterface)
	if wanIP != "" {
		log.Printf("Bootstrap: WAN %s reachable at %s before starting AP", cfg.WANInterface, wanIP)
		return nil
	}
	kind := cfg.WANType
	if kind == "" {
		kind = ResolveInterfaceKind(cfg.WANInterface)
	}
	if kind == "wireless" {
		return fmt.Errorf("WAN %s has no IPv4; prefer Ethernet for setup access while the LAN AP starts", cfg.WANInterface)
	}
	return fmt.Errorf("WAN %s has no IPv4 yet", cfg.WANInterface)
}

func configureHostapd(cfg RouterConfig) error {
	if err := prepareHostapd(cfg); err != nil {
		return err
	}
	return activateHostapd()
}

func disableHostapd() error {
	exec.Command("systemctl", "stop", "hostapd").Run()
	exec.Command("systemctl", "disable", "hostapd").Run()
	return nil
}

func configureDnsmasq(cfg RouterConfig) error {
	if err := ensureDnsmasqInstalled(); err != nil {
		return err
	}
	if err := os.MkdirAll("/etc/dnsmasq.d", 0755); err != nil {
		return err
	}

	netmask := prefixToNetmask(cfg.LANPrefix)
	wanDNS := getWANDNSServers(cfg.WANInterface)
	upstream := "server=1.1.1.1\nserver=9.9.9.9\n"
	if len(wanDNS) > 0 {
		var lines []string
		for _, s := range wanDNS {
			lines = append(lines, "server="+s)
		}
		upstream = strings.Join(lines, "\n") + "\n"
	}

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

func getWANDNSServers(iface string) []string {
	output, err := execCommandOutput("resolvectl", "dns", iface)
	if err == nil && output != "" {
		var servers []string
		for _, part := range strings.Fields(strings.TrimPrefix(output, iface+":")) {
			if strings.Contains(part, ".") {
				servers = append(servers, part)
			}
		}
		if len(servers) > 0 {
			return servers
		}
	}

	resolv, err := os.ReadFile("/etc/resolv.conf")
	if err != nil {
		return nil
	}
	var servers []string
	for _, line := range strings.Split(string(resolv), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "nameserver ") {
			servers = append(servers, strings.TrimSpace(strings.TrimPrefix(line, "nameserver ")))
		}
	}
	return servers
}

func usesNetworkManager() bool {
	return exec.Command("systemctl", "is-active", "--quiet", "NetworkManager").Run() == nil
}

func prefixToNetmask(prefix int) string {
	if prefix <= 0 || prefix > 32 {
		return "255.255.255.0"
	}
	mask := uint32(0xFFFFFFFF << (32 - prefix))
	return fmt.Sprintf("%d.%d.%d.%d",
		(mask>>24)&0xFF, (mask>>16)&0xFF, (mask>>8)&0xFF, mask&0xFF)
}

func appendUniqueBlock(path, marker, block string, restart func() error) error {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	content := string(data)
	if strings.Contains(content, marker) {
		return restart()
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteString(block); err != nil {
		return err
	}
	return restart()
}

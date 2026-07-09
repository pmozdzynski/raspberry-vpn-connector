package handlers

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTailscaleIPv4FromStatus(t *testing.T) {
	st := tailscaleStatusJSON{}
	st.Self.TailscaleIPs = []string{"fd7a:115c:a1e0::1", "100.64.0.2"}
	if got := tailscaleIPv4FromStatus(st); got != "100.64.0.2" {
		t.Fatalf("expected 100.64.0.2, got %q", got)
	}
}

func TestParseTailscaleStatusJSON(t *testing.T) {
	raw := `{
		"Self": {
			"Online": true,
			"TailscaleIPs": ["100.64.0.5"],
			"DNSName": "pi.example.ts.net."
		},
		"ExitNodeStatus": {
			"ExitNodeOption": true
		}
	}`
	var parsed tailscaleStatusJSON
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatal(err)
	}
	if !parsed.Self.Online {
		t.Fatal("expected online")
	}
	if !parsed.ExitNodeStatus.ExitNodeOption {
		t.Fatal("expected exit node advertised")
	}
	if got := tailscaleIPv4FromStatus(parsed); got != "100.64.0.5" {
		t.Fatalf("expected 100.64.0.5, got %q", got)
	}
}

func TestRenderDnsmasqUpstreamWithoutVPN(t *testing.T) {
	cfg := RouterConfig{WANInterface: "eth0"}
	upstream := renderDnsmasqUpstream(cfg, nil, "")
	if strings.Contains(upstream, "@vpn0") {
		t.Fatal("expected no vpn0 binding when VPN DNS unavailable")
	}
	if !strings.Contains(upstream, "server=") {
		t.Fatal("expected public upstream servers")
	}
}

func TestRenderDnsmasqUpstreamWithVPN(t *testing.T) {
	cfg := RouterConfig{WANInterface: "eth0"}
	vpn := &VPNDNSInfo{
		Servers: []string{"10.0.0.53"},
		Domains: []string{"corp.example"},
	}
	upstream := renderDnsmasqUpstream(cfg, vpn, "vpn0")
	if !strings.Contains(upstream, "server=/corp.example/10.0.0.53@vpn0") {
		t.Fatalf("expected corp zone via vpn0, got:\n%s", upstream)
	}
}
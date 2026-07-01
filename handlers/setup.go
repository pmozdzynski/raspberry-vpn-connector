package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type setupApplyRequest struct {
	WANInterface   string `json:"wan_interface"`
	LANInterface   string `json:"lan_interface"`
	LANAddress     string `json:"lan_address"`
	LANPrefix      int    `json:"lan_prefix"`
	DHCPRangeStart string `json:"dhcp_range_start"`
	DHCPRangeEnd   string `json:"dhcp_range_end"`
	DHCPLeaseHours int    `json:"dhcp_lease_hours"`
	APSSID         string `json:"ap_ssid"`
	APPassword     string `json:"ap_password"`
	WifiCountry    string `json:"wifi_country"`
	AdminUsername  string `json:"admin_username"`
	AdminPassword  string `json:"admin_password"`
}

func SetupStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	snapshot := GetNetworkSnapshot()
	if wan := strings.TrimSpace(r.URL.Query().Get("wan")); wan != "" {
		snapshot.SuggestedLAN = SuggestLANSubnet(wan)
	}
	json.NewEncoder(w).Encode(snapshot)
}

func SetupApplyHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if IsConfigured() {
		http.Error(w, "Router is already configured", http.StatusConflict)
		return
	}

	cfg, err := parseSetupRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	stream := strings.Contains(r.Header.Get("Accept"), "text/event-stream") ||
		r.URL.Query().Get("stream") == "1"

	if stream {
		setupApplyStream(w, cfg)
		return
	}

	if err := ApplyBootstrapWithProgress(cfg, nil); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	SetRuntimeCredentials(cfg.AdminUsername, cfg.AdminPassword)
	writeSetupOK(w, cfg)
}

func parseSetupRequest(r *http.Request) (RouterConfig, error) {
	var req setupApplyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return RouterConfig{}, fmt.Errorf("invalid JSON body")
	}

	cfg := RouterConfig{
		WANInterface:   strings.TrimSpace(req.WANInterface),
		LANInterface:   strings.TrimSpace(req.LANInterface),
		LANAddress:     strings.TrimSpace(req.LANAddress),
		LANPrefix:      req.LANPrefix,
		DHCPRangeStart: strings.TrimSpace(req.DHCPRangeStart),
		DHCPRangeEnd:   strings.TrimSpace(req.DHCPRangeEnd),
		DHCPLeaseHours: req.DHCPLeaseHours,
		APSSID:         strings.TrimSpace(req.APSSID),
		APPassword:     req.APPassword,
		WifiCountry:    strings.TrimSpace(req.WifiCountry),
		AdminUsername:  strings.TrimSpace(req.AdminUsername),
		AdminPassword:  req.AdminPassword,
	}

	if cfg.LANPrefix == 0 {
		cfg.LANPrefix = 24
	}
	if cfg.AdminUsername == "" {
		cfg.AdminUsername = "admin"
	}
	if cfg.WANInterface == "" {
		iface, err := detectDefaultRouteInterface()
		if err == nil {
			cfg.WANInterface = iface
		}
	}

	suggested := SuggestLANSubnet(cfg.WANInterface)
	if cfg.LANAddress == "" {
		cfg.LANAddress = suggested.Address
	}
	if cfg.DHCPRangeStart == "" {
		cfg.DHCPRangeStart = suggested.DHCPStart
	}
	if cfg.DHCPRangeEnd == "" {
		cfg.DHCPRangeEnd = suggested.DHCPEnd
	}

	return cfg, nil
}

func setupApplyStream(w http.ResponseWriter, cfg RouterConfig) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	send := func(status, step, detail string, extra map[string]interface{}) {
		payload := map[string]interface{}{
			"status": status,
			"step":   step,
			"detail": detail,
		}
		for k, v := range extra {
			payload[k] = v
		}
		body, _ := json.Marshal(payload)
		fmt.Fprintf(w, "data: %s\n\n", body)
		flusher.Flush()
	}

	progress := setupProgressReporter(func(status, step, detail string) {
		extra := map[string]interface{}{}
		if status == "warn" && step == "management access" {
			extra["access_urls"] = FormatManagementURLs(cfg)
		}
		if status == "ok" && step == "enable IP forwarding" {
			extra["ip_forwarding"] = IsIPForwardingEnabled()
		}
		send(status, step, detail, extra)
	})

	if err := ApplyBootstrapWithProgress(cfg, progress); err != nil {
		send("error", "", err.Error(), nil)
		return
	}

	SetRuntimeCredentials(cfg.AdminUsername, cfg.AdminPassword)
	forwarding := "off"
	if IsIPForwardingEnabled() {
		forwarding = "on"
	}
	doneDetail := fmt.Sprintf("Router configured. IP forwarding %s. LAN AP %s on %s. %s",
		forwarding, cfg.APSSID, cfg.LANInterface, FormatManagementHint(cfg))
	send("done", "", doneDetail, map[string]interface{}{
		"access_urls":   FormatManagementURLs(cfg),
		"ip_forwarding": IsIPForwardingEnabled(),
	})
}

func writeSetupOK(w http.ResponseWriter, cfg RouterConfig) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":       true,
		"message":  fmt.Sprintf("Router configured. LAN %s on %s", cfg.LANAddress, cfg.LANInterface),
		"login_at": "/login",
	})
}

func SetupPageHandler(w http.ResponseWriter, r *http.Request) {
	if IsConfigured() {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	http.ServeFile(w, r, "./templates/setup.html")
}

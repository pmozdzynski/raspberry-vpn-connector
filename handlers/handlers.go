package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
)

func StatusHandler(w http.ResponseWriter, r *http.Request) {
	profiles := LoadProfiles()
	public := make([]VPNProfile, 0, len(profiles))
	for _, p := range profiles {
		public = append(public, PublicProfile(p))
	}

	response := map[string]interface{}{
		"configured": IsConfigured(),
		"network":    GetNetworkSnapshot(),
		"vpn":        GetVPNState(),
		"tailscale":  GetTailscaleStatus(),
		"profiles":   public,
		"log_tail":   OpenConnectLogTail(),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func ProfilesHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		profiles := LoadProfiles()
		public := make([]VPNProfile, 0, len(profiles))
		for _, p := range profiles {
			public = append(public, PublicProfile(p))
		}
		writeJSON(w, public)
	case http.MethodPost:
		var p VPNProfile
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		p.Name = strings.TrimSpace(p.Name)
		p.Username = strings.TrimSpace(p.Username)
		p.ServerURL = strings.TrimSpace(p.ServerURL)
		p.ServerCertPin = strings.TrimSpace(p.ServerCertPin)
		p.Protocol = NormalizeVPNProtocol(p.Protocol)
		if p.Protocol == "" {
			http.Error(w, "unsupported protocol (use anyconnect, nc, gp, pulse, f5, fortinet, or array)", http.StatusBadRequest)
			return
		}
		if p.Name == "" || p.Username == "" || p.ServerURL == "" || p.ServerCertPin == "" {
			http.Error(w, "name, username, server_url, and servercert_pin are required", http.StatusBadRequest)
			return
		}
		if !strings.HasPrefix(p.ServerCertPin, "pin-sha256:") {
			p.ServerCertPin = "pin-sha256:" + strings.TrimPrefix(p.ServerCertPin, "pin-sha256:")
		}
		saved, err := UpsertProfile(p)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, PublicProfile(saved))
	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "missing id", http.StatusBadRequest)
			return
		}
		if err := DeleteProfile(id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

type connectRequest struct {
	ProfileID string `json:"profile_id"`
	Password  string `json:"password"`
}

func VPNConnectHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req connectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if req.ProfileID == "" {
		http.Error(w, "profile_id required", http.StatusBadRequest)
		return
	}
	if err := StartConnect(req.ProfileID, req.Password); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, GetVPNState())
}

type vpnInputRequest struct {
	Input string `json:"input"`
	Token string `json:"token"`
}

func VPNInputHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req vpnInputRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	input := strings.TrimSpace(req.Input)
	if input == "" {
		input = strings.TrimSpace(req.Token)
	}
	if err := SubmitVPNInput(input); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, GetVPNState())
}

func VPNDisconnectHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := DisconnectVPN(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, GetVPNState())
}

func VPNReconnectHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := ReconnectLastProfile(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, GetVPNState())
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

type tailscaleExitNodeRequest struct {
	Enabled bool `json:"enabled"`
}

func TailscaleExitNodeHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, GetTailscaleStatus())
	case http.MethodPost:
		var req tailscaleExitNodeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if err := SetTailscaleExitNode(req.Enabled); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, GetTailscaleStatus())
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

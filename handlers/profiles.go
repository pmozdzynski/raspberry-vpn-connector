package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

const profilesFile = configDir + "/profiles.json"

// VPNProfile is a saved OpenConnect endpoint.
type VPNProfile struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Protocol      string    `json:"protocol"`
	Username      string    `json:"username"`
	ServerURL     string    `json:"server_url"`
	ServerCertPin string    `json:"servercert_pin"`
	SavePassword  bool      `json:"save_password"`
	NoDTLS        bool      `json:"no_dtls,omitempty"`
	Password      string    `json:"password,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type profilesStore struct {
	Profiles []VPNProfile `json:"profiles"`
}

var profilesMu sync.RWMutex

var supportedVPNProtocols = []string{
	"anyconnect",
	"nc",
	"gp",
	"pulse",
	"f5",
	"fortinet",
	"array",
}

func SupportedVPNProtocols() []string {
	out := make([]string, len(supportedVPNProtocols))
	copy(out, supportedVPNProtocols)
	return out
}

func NormalizeVPNProtocol(protocol string) string {
	p := strings.ToLower(strings.TrimSpace(protocol))
	if p == "" {
		return "fortinet"
	}
	for _, allowed := range supportedVPNProtocols {
		if p == allowed {
			return p
		}
	}
	return ""
}

func newProfileID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func LoadProfiles() []VPNProfile {
	profilesMu.RLock()
	defer profilesMu.RUnlock()

	data, err := os.ReadFile(profilesFile)
	if err != nil {
		return nil
	}
	var store profilesStore
	if json.Unmarshal(data, &store) != nil {
		return nil
	}
	return store.Profiles
}

func saveProfiles(profiles []VPNProfile) error {
	profilesMu.Lock()
	defer profilesMu.Unlock()

	if err := os.MkdirAll(configDir, 0750); err != nil {
		return err
	}
	data, err := json.MarshalIndent(profilesStore{Profiles: profiles}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(profilesFile, data, 0600)
}

func GetProfile(id string) (VPNProfile, bool) {
	for _, p := range LoadProfiles() {
		if p.ID == id {
			return p, true
		}
	}
	return VPNProfile{}, false
}

func UpsertProfile(p VPNProfile) (VPNProfile, error) {
	profiles := LoadProfiles()
	now := time.Now().UTC()
	if p.ID == "" {
		p.ID = newProfileID()
		p.CreatedAt = now
	}
	p.UpdatedAt = now
	p.Protocol = NormalizeVPNProtocol(p.Protocol)
	if p.Protocol == "" {
		return VPNProfile{}, fmt.Errorf("unsupported protocol (use anyconnect, nc, gp, pulse, f5, fortinet, or array)")
	}

	found := false
	for i, existing := range profiles {
		if existing.ID == p.ID {
			if !p.SavePassword || p.Password == "" {
				p.Password = existing.Password
			}
			profiles[i] = p
			found = true
			break
		}
	}
	if !found {
		profiles = append(profiles, p)
	}
	if err := saveProfiles(profiles); err != nil {
		return VPNProfile{}, err
	}
	return p, nil
}

func DeleteProfile(id string) error {
	profiles := LoadProfiles()
	var kept []VPNProfile
	for _, p := range profiles {
		if p.ID != id {
			kept = append(kept, p)
		}
	}
	return saveProfiles(kept)
}

// PublicProfile hides stored password in API responses.
func PublicProfile(p VPNProfile) VPNProfile {
	out := p
	if out.Password != "" {
		out.Password = "********"
	}
	return out
}

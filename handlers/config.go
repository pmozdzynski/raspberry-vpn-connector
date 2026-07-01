package handlers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

const (
	configDir  = "/etc/vpn-connector"
	configFile = configDir + "/config.json"
)

type RouterConfig struct {
	Configured     bool   `json:"configured"`
	WANInterface   string `json:"wan_interface"`
	WANType        string `json:"wan_type,omitempty"` // wireless or ethernet
	LANInterface   string `json:"lan_interface"`
	LANType        string `json:"lan_type,omitempty"` // wireless or ethernet
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
	LastProfileID  string `json:"last_profile_id,omitempty"`
}

var (
	configMu     sync.RWMutex
	routerConfig RouterConfig
)

func init() {
	routerConfig = LoadRouterConfig()
	ReloadAuthCredentials()
}

func LoadRouterConfig() RouterConfig {
	data, err := os.ReadFile(configFile)
	if err != nil {
		return RouterConfig{LANPrefix: 24}
	}
	var cfg RouterConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return RouterConfig{LANPrefix: 24}
	}
	if cfg.LANPrefix == 0 {
		cfg.LANPrefix = 24
	}
	return cfg
}

func SaveRouterConfig(cfg RouterConfig) error {
	configMu.Lock()
	defer configMu.Unlock()

	if err := os.MkdirAll(configDir, 0750); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(configFile, data, 0600); err != nil {
		return err
	}
	routerConfig = cfg
	ReloadAuthCredentials()
	return nil
}

func GetRouterConfig() RouterConfig {
	configMu.RLock()
	defer configMu.RUnlock()
	return routerConfig
}

func IsConfigured() bool {
	cfg := GetRouterConfig()
	return cfg.Configured && cfg.WANInterface != "" && cfg.LANInterface != ""
}

func ScriptsDir() string {
	for _, dir := range []string{
		"/opt/vpn-connector/scripts",
		filepath.Join(".", "scripts"),
	} {
		if _, err := os.Stat(dir); err == nil {
			return dir
		}
	}
	return "/opt/vpn-connector/scripts"
}

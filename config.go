package main

import (
	"encoding/json"
	"log"
	"os"
)

const configFile = "config.json"

// AccountConfig holds configuration for a specific account mapping
type AccountConfig struct {
	SureID      string `json:"sure_id"`
	Name        string `json:"name"`
	BalanceOnly bool   `json:"balance_only,omitzero"`
}

// Config holds the application configuration
type Config struct {
	SureAPIKey  string                   `json:"sure_api_key"`
	SureBaseURL string                   `json:"sure_base_url"` // e.g., http://localhost:3000/api/v1
	AccessURL   string                   `json:"access_url"`    // The permanent SimpleFIN URL
	SetupToken  string                   `json:"setup_token"`   // Used only once if AccessURL is empty
	AccountMap  map[string]AccountConfig `json:"account_map"`   // Maps SimpleFIN ID -> AccountConfig
}

// LoadConfig reads the configuration from disk
func LoadConfig() Config {
	file, err := os.ReadFile(configFile)
	if err != nil {
		log.Fatalf("Please create a %s file", configFile)
	}

	// Try loading with the new structure
	var cfg Config
	if err := json.Unmarshal(file, &cfg); err == nil && len(cfg.AccountMap) > 0 {
		// Check if it's actually the new format by looking at one entry
		isNewFormat := false
		for _, v := range cfg.AccountMap {
			if v.SureID != "" {
				isNewFormat = true
			}
			break
		}
		if isNewFormat {
			return cfg
		}
	}

	// If it fails or it's the old format, try loading as map[string]string
	var oldCfg struct {
		SureAPIKey  string            `json:"sure_api_key"`
		SureBaseURL string            `json:"sure_base_url"`
		AccessURL   string            `json:"access_url"`
		SetupToken  string            `json:"setup_token"`
		AccountMap  map[string]string `json:"account_map"`
	}

	if err := json.Unmarshal(file, &oldCfg); err != nil {
		// Fallback for very old format or during migration
		var veryOldCfg struct {
			MaybeAPIKey  string            `json:"maybe_api_key"`
			MaybeBaseURL string            `json:"maybe_base_url"`
			AccessURL    string            `json:"access_url"`
			SetupToken   string            `json:"setup_token"`
			AccountMap   map[string]string `json:"account_map"`
		}
		if err := json.Unmarshal(file, &veryOldCfg); err == nil {
			oldCfg.SureAPIKey = veryOldCfg.MaybeAPIKey
			oldCfg.SureBaseURL = veryOldCfg.MaybeBaseURL
			oldCfg.AccessURL = veryOldCfg.AccessURL
			oldCfg.SetupToken = veryOldCfg.SetupToken
			oldCfg.AccountMap = veryOldCfg.AccountMap
		} else {
			log.Fatalf("Failed to parse %s: %v", configFile, err)
		}
	}

	// Migrate to new format
	cfg = Config{
		SureAPIKey:  oldCfg.SureAPIKey,
		SureBaseURL: oldCfg.SureBaseURL,
		AccessURL:   oldCfg.AccessURL,
		SetupToken:  oldCfg.SetupToken,
		AccountMap:  make(map[string]AccountConfig),
	}

	for k, v := range oldCfg.AccountMap {
		cfg.AccountMap[k] = AccountConfig{
			SureID: v,
			Name:   "Unknown Account", // Will be updated by --sync-metadata
		}
	}

	return cfg
}

// SaveConfig writes the configuration to disk
func SaveConfig(cfg Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configFile, data, 0644)
}

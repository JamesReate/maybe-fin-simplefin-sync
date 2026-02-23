package main

import (
	"encoding/json"
	"log"
	"os"
)

const configFile = "config.json"

// Config holds the application configuration
type Config struct {
	MaybeAPIKey  string            `json:"maybe_api_key"`
	MaybeBaseURL string            `json:"maybe_base_url"` // e.g., http://localhost:3000/api/v1
	AccessURL    string            `json:"access_url"`     // The permanent SimpleFIN URL
	SetupToken   string            `json:"setup_token"`    // Used only once if AccessURL is empty
	AccountMap   map[string]string `json:"account_map"`    // Maps SimpleFIN ID -> Maybe ID
}

// LoadConfig reads the configuration from disk
func LoadConfig() Config {
	file, err := os.ReadFile(configFile)
	if err != nil {
		log.Fatalf("Please create a %s file", configFile)
	}
	var cfg Config
	if err := json.Unmarshal(file, &cfg); err != nil {
		log.Fatalf("Failed to parse %s: %v", configFile, err)
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

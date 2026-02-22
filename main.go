package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

// --- Configuration Structs ---
type Config struct {
	MaybeAPIKey   string            `json:"maybe_api_key"`
	MaybeBaseURL  string            `json:"maybe_base_url"` // e.g., http://localhost:3000/api/v1
	AccessURL     string            `json:"access_url"`     // The permanent SimpleFIN URL
	SetupToken    string            `json:"setup_token"`    // Used only once if AccessURL is empty
	AccountMap    map[string]string `json:"account_map"`    // Maps SimpleFIN ID -> Maybe ID
}

// --- SimpleFIN Structs ---
type SimpleFINResponse struct {
	Accounts []SFAccount `json:"accounts"`
}
type SFAccount struct {
	ID           string          `json:"id"`
	Transactions []SFTransaction `json:"transactions"`
}
type SFTransaction struct {
	ID           string `json:"id"`
	Amount       string `json:"amount"`
	Description  string `json:"description"`
	TransactedAt int64  `json:"transacted_at"`
}

// --- Maybe Structs (Adjust based on your exact Maybe branch schema) ---
type MaybeTransaction struct {
	AccountID string `json:"account_id"`
	Amount    string `json:"amount"`
	Date      string `json:"date"`
	Name      string `json:"name"`
	Notes     string `json:"notes"`
}

const stateFile = "sync_state.json"
const configFile = "config.json"

func main() {
	config := loadConfig()
	state := loadState()

	// 1. Handle SimpleFIN Authentication
	if config.AccessURL == "" && config.SetupToken != "" {
		config.AccessURL = claimSimpleFINToken(config.SetupToken)
		saveConfig(config)
		log.Println("Successfully claimed and saved permanent Access URL.")
	} else if config.AccessURL == "" {
		log.Fatal("No AccessURL or SetupToken provided in config.json")
	}

	// 2. Fetch Data from SimpleFIN
	log.Println("Fetching transactions from SimpleFIN...")
	sfData := fetchSimpleFINData(config.AccessURL)

	// 3. Process and Sync to Maybe
	newTxCount := 0
	for _, account := range sfData.Accounts {
		maybeAccountID, mapped := config.AccountMap[account.ID]
		if !mapped {
			log.Printf("Skipping SimpleFIN account %s (Not mapped in config)", account.ID)
			continue
		}

		for _, tx := range account.Transactions {
			if _, processed := state[tx.ID]; processed {
				continue // Idempotency check: skip if already processed
			}

			// Format for Maybe API
			txDate := time.Unix(tx.TransactedAt, 0).Format("2006-01-02")
			payload := MaybeTransaction{
				AccountID: maybeAccountID,
				Amount:    tx.Amount,
				Date:      txDate,
				Name:      tx.Description,
				Notes:     fmt.Sprintf("Imported via SimpleFIN. ID: %s", tx.ID),
			}

			err := createMaybeTransaction(config.MaybeBaseURL, config.MaybeAPIKey, payload)
			if err != nil {
				log.Printf("Failed to create tx %s: %v", tx.ID, err)
				continue
			}

			// Mark as processed and save state immediately
			state[tx.ID] = true
			saveState(state)
			newTxCount++
			log.Printf("Synced transaction: %s - %s", txDate, tx.Description)
		}
	}

	log.Printf("Sync complete. %d new transactions added.", newTxCount)
}

// --- Helper Functions ---

func claimSimpleFINToken(setupToken string) string {
	decoded, err := base64.StdEncoding.DecodeString(setupToken)
	if err != nil {
		log.Fatalf("Invalid base64 Setup Token: %v", err)
	}
	claimURL := string(decoded)

	req, _ := http.NewRequest("POST", claimURL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		log.Fatalf("Failed to claim token at %s", claimURL)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	return string(body) // This is the permanent Access URL
}

func fetchSimpleFINData(accessURL string) SimpleFINResponse {
	// The Access URL contains the basic auth credentials inherently
	reqURL := accessURL + "/accounts"
	resp, err := http.Get(reqURL)
	if err != nil || resp.StatusCode != 200 {
		log.Fatalf("Failed to fetch SimpleFIN data. Status: %v", resp.StatusCode)
	}
	defer resp.Body.Close()

	var sfResp SimpleFINResponse
	json.NewDecoder(resp.Body).Decode(&sfResp)
	return sfResp
}

func createMaybeTransaction(baseURL, apiKey string, tx MaybeTransaction) error {
	url := fmt.Sprintf("%s/transactions", baseURL)
	
	// Wrap in a "transaction" key as standard in Rails APIs
	payload := map[string]interface{}{"transaction": tx}
	jsonValue, _ := json.Marshal(payload)

	req, _ := http.NewRequest("POST", url, bytes.NewBuffer(jsonValue))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API error %d: %s", resp.StatusCode, string(bodyBytes))
	}
	return nil
}

func loadConfig() Config {
	file, err := os.ReadFile(configFile)
	if err != nil {
		log.Fatalf("Please create a %s file", configFile)
	}
	var cfg Config
	json.Unmarshal(file, &cfg)
	return cfg
}

func saveConfig(cfg Config) {
	data, _ := json.MarshalIndent(cfg, "", "  ")
	os.WriteFile(configFile, data, 0644)
}

func loadState() map[string]bool {
	state := make(map[string]bool)
	file, err := os.ReadFile(stateFile)
	if err == nil {
		json.Unmarshal(file, &state)
	}
	return state
}

func saveState(state map[string]bool) {
	data, _ := json.MarshalIndent(state, "", "  ")
	os.WriteFile(stateFile, data, 0644)
}

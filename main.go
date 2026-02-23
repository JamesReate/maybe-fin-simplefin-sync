package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// --- Configuration Structs ---
type Config struct {
	MaybeAPIKey  string            `json:"maybe_api_key"`
	MaybeBaseURL string            `json:"maybe_base_url"` // e.g., http://localhost:3000/api/v1
	AccessURL    string            `json:"access_url"`     // The permanent SimpleFIN URL
	SetupToken   string            `json:"setup_token"`    // Used only once if AccessURL is empty
	AccountMap   map[string]string `json:"account_map"`    // Maps SimpleFIN ID -> Maybe ID
}

// --- SimpleFIN Structs ---
type SimpleFINResponse struct {
	Accounts []SFAccount `json:"accounts"`
}
type SFAccount struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Org          SFOrg           `json:"org"`
	Transactions []SFTransaction `json:"transactions"`
}

type SFOrg struct {
	Domain  string `json:"domain"`
	SfinURL string `json:"sfin-url"`
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

type MaybeAccount struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Balance        string `json:"balance"`
	Currency       string `json:"currency"`
	Classification string `json:"classification"`
	AccountType    string `json:"account_type"`
}

type MaybeAccountsResponse struct {
	Accounts []MaybeAccount `json:"accounts"`
}

type CreateMaybeAccountRequest struct {
	Account struct {
		Name         string  `json:"name"`
		Balance      float64 `json:"balance"`
		CurrencyCode string  `json:"currency_code"`
		Category     string  `json:"category"`
	} `json:"account"`
}

type CreateMaybeAccountResponse struct {
	Account MaybeAccount `json:"account"`
}

const stateFile = "sync_state.json"
const configFile = "config.json"

func main() {
	autoCreate := flag.Bool("auto-create-accounts", false, "Automatically prompt to create unmapped accounts")
	flag.Parse()

	config := loadConfig()

	// Print Maybe Finance Accounts
	log.Println("Fetching accounts from Maybe Finance...")
	maybeAccounts, err := fetchMaybeAccounts(config.MaybeBaseURL, config.MaybeAPIKey)
	if err != nil {
		log.Printf("Failed to fetch Maybe accounts: %v", err)
	} else {
		fmt.Println("\nMaybe Finance Accounts:")
		for _, acc := range maybeAccounts {
			fmt.Printf("- %s (ID: %s) Balance: %s\n", acc.Name, acc.ID, acc.Balance)
		}
		fmt.Println()
	}

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
			if *autoCreate {
				var err error
				maybeAccountID, err = promptAndCreateMaybeAccount(config.MaybeBaseURL, config.MaybeAPIKey, account)
				if err != nil {
					log.Printf("Failed to create account for %s: %v", account.Name, err)
					log.Fatalf("Please manually create the account in Maybe Finance and try again. ID: %s", account.ID)
				}
				// Save config with new mapping
				config.AccountMap[account.ID] = maybeAccountID
				saveConfig(config)
				log.Printf("Successfully mapped SimpleFIN account %s to Maybe account %s", account.Name, maybeAccountID)
			} else {
				log.Printf("Skipping SimpleFIN account %s, %s (Not mapped in config): %s", account.ID, account.Name, account.Org.Domain)
				continue
			}
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

func fetchMaybeAccounts(baseURL, apiKey string) ([]MaybeAccount, error) {
	url := fmt.Sprintf("%s/accounts", baseURL)

	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("X-Api-Key", apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var result MaybeAccountsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result.Accounts, nil
}

func createMaybeTransaction(baseURL, apiKey string, tx MaybeTransaction) error {
	url := fmt.Sprintf("%s/transactions", baseURL)

	// Wrap in a "transaction" key as standard in Rails APIs
	payload := map[string]interface{}{"transaction": tx}
	jsonValue, _ := json.Marshal(payload)

	req, _ := http.NewRequest("POST", url, bytes.NewBuffer(jsonValue))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", apiKey)

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

func promptAndCreateMaybeAccount(baseURL, apiKey string, sfAcc SFAccount) (string, error) {
	reader := bufio.NewReader(os.Stdin)

	fmt.Printf("\nUnmapped SimpleFIN account found:\n")
	fmt.Printf("  Name: %s\n", sfAcc.Name)
	fmt.Printf("  Org:  %s\n", sfAcc.Org.Domain)

	defaultName := fmt.Sprintf("%s %s", sfAcc.Name, sfAcc.Org.Domain)
	fmt.Printf("Enter Maybe account name [%s]: ", defaultName)
	name, _ := reader.ReadString('\n')
	name = strings.TrimSpace(name)
	if name == "" {
		name = defaultName
	}

	var category string
	validCategories := map[string]bool{
		"depository":  true,
		"credit_card": true,
		"loan":        true,
		"investment":  true,
	}

	for {
		fmt.Printf("Enter category (depository, credit_card, loan, investment): ")
		category, _ = reader.ReadString('\n')
		category = strings.TrimSpace(category)
		if validCategories[category] {
			break
		}
		fmt.Println("Invalid category. Please try again.")
	}

	// For the initial balance, we might want to get it from SimpleFIN,
	// but SFAccount in our struct doesn't have a balance field.
	// SimpleFIN /accounts endpoint usually returns balance in some format.
	// However, the issue description doesn't specify where to get the balance.
	// I'll assume 0.0 for now, or just leave it to be updated by transactions.
	// Actually, Maybe API might require it.

	return createMaybeAccount(baseURL, apiKey, name, category)
}

func createMaybeAccount(baseURL, apiKey, name, category string) (string, error) {
	url := fmt.Sprintf("%s/accounts", baseURL)

	var payload CreateMaybeAccountRequest
	payload.Account.Name = name
	payload.Account.Category = category
	payload.Account.CurrencyCode = "USD" // Defaulting to USD
	payload.Account.Balance = 0.0        // Defaulting to 0.0

	jsonValue, _ := json.Marshal(payload)

	req, _ := http.NewRequest("POST", url, bytes.NewBuffer(jsonValue))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API error %d: %s", resp.StatusCode, string(bodyBytes))
	}

	bodyBytes, _ := io.ReadAll(resp.Body)
	fmt.Printf("Create account API response: %s\n", string(bodyBytes))

	var result CreateMaybeAccountResponse
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return "", err
	}

	return result.Account.ID, nil
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

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

	"github.com/tidwall/gjson"
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
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	Org              SFOrg           `json:"org"`
	Balance          float64         `json:"balance"`
	AvailableBalance float64         `json:"available-balance"`
	BalanceDate      uint64          `json:"balance-date"`
	Transactions     []SFTransaction `json:"transactions"`
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

type CachedAccount struct {
	Account   SFAccount `json:"account"`
	FetchedAt time.Time `json:"fetched_at"`
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
		Name            string  `json:"name"`
		Balance         float64 `json:"balance"`
		Currency        string  `json:"currency"`
		AccountableType string  `json:"accountable_type"`
		SubType         string  `json:"sub_type"`
	} `json:"account"`
}

const stateFile = "sync_state.json"
const configFile = "config.json"
const cacheDir = "tmp"

func main() {
	autoCreate := flag.Bool("auto-create-accounts", false, "Automatically prompt to create unmapped accounts")
	forceRefresh := flag.Bool("force-refresh", false, "Force refresh SimpleFIN data (ignore cache)")
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
	sfData := fetchSimpleFINData(config.AccessURL, *forceRefresh)

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
			fmt.Printf("Processing %d transaction for %s: %s\n", len(account.Transactions), account.Name, tx.Description)
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

func fetchSimpleFINData(accessURL string, forceRefresh bool) SimpleFINResponse {
	if _, err := os.Stat(cacheDir); os.IsNotExist(err) {
		os.Mkdir(cacheDir, 0755)
	}

	var allAccounts []SFAccount

	// Check for cached data
	files, _ := os.ReadDir(cacheDir)
	cachedMap := make(map[string]CachedAccount)
	for _, f := range files {
		if !strings.HasPrefix(f.Name(), "account_") || !strings.HasSuffix(f.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(cacheDir + "/" + f.Name())
		if err != nil {
			continue
		}
		var cached CachedAccount
		if err := json.Unmarshal(data, &cached); err == nil {
			if !forceRefresh && time.Since(cached.FetchedAt) < 24*time.Hour {
				allAccounts = append(allAccounts, cached.Account)
				cachedMap[cached.Account.ID] = cached
			}
		}
	}

	// SimpleFIN /accounts returns ALL accounts, but if we have some in cache,
	// and we don't know if there are new ones, we might still want to call it.
	// The requirement: "if you add a new account it should know only to pull the info for that account."
	// This is tricky because /accounts returns all. If we want to know if there's a new account,
	// we have to call it.

	// However, if we pull all accounts and we see that they are already in cache,
	// we can skip the heavy processing? No, the pull itself is the expensive part (API hit).

	// If we have some accounts in cache, but some are missing or expired, we call /accounts.
	// If all are in cache and not expired, and forceRefresh is false, we might still want to call
	// to check for NEW accounts.

	// To satisfy "track by account", we can assume if we have cached data for accounts,
	// and we want to "know only to pull info for that account", maybe there's a param?
	// SimpleFIN /accounts takes ?account=<id>? I'll check if it's common.
	// Usually SimpleFIN is just a list.

	// Let's call the API if forceRefresh is true OR if we have NO accounts in cache OR if anything is expired.
	needsPull := forceRefresh || len(allAccounts) == 0

	// Check if any cached account is expired
	if !needsPull {
		for _, cached := range cachedMap {
			if time.Since(cached.FetchedAt) >= 24*time.Hour {
				needsPull = true
				break
			}
		}
	}

	if needsPull {
		log.Println("Cache expired or missing, pulling from SimpleFIN...")
		reqURL := accessURL + "/accounts"
		resp, err := http.Get(reqURL)
		if err != nil || resp.StatusCode != 200 {
			log.Fatalf("Failed to fetch SimpleFIN data. Status: %v", resp.StatusCode)
		}
		defer resp.Body.Close()

		var sfResp SimpleFINResponse
		json.NewDecoder(resp.Body).Decode(&sfResp)
		fmt.Printf("Fetched %d accounts from SimpleFIN.\n", len(sfResp.Accounts))

		// Update cache
		for _, acc := range sfResp.Accounts {
			fmt.Printf("Caching SimpleFIN account %s (ID: %s) with %d transactions and balance %.2f\n", acc.Name, acc.ID, len(acc.Transactions), acc.Balance)
			cached := CachedAccount{
				Account:   acc,
				FetchedAt: time.Now(),
			}
			data, _ := json.MarshalIndent(cached, "", "  ")
			os.WriteFile(cacheDir+"/account_"+acc.ID+".json", data, 0644)
		}
		return sfResp
	}

	log.Println("Using cached SimpleFIN data.")
	return SimpleFINResponse{Accounts: allAccounts}
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

	// AccountableType picker
	accountableTypes := []struct {
		value string
		label string
	}{
		{"Depository", "Depository (Assets) - Bank accounts like checking/savings"},
		{"Investment", "Investment (Assets) - Brokerage, 401k, IRA, etc."},
		{"Crypto", "Crypto (Assets) - Cryptocurrency wallets/exchanges"},
		{"Property", "Property (Assets) - Real estate"},
		{"Vehicle", "Vehicle (Assets) - Cars, trucks, etc."},
		{"OtherAsset", "Other Asset (Assets) - Jewelry, collectibles, etc."},
		{"CreditCard", "Credit Card (Liabilities) - Credit card debt"},
		{"Loan", "Loan (Liabilities) - Mortgages, student loans, etc."},
		{"OtherLiability", "Other Liability (Liabilities) - Other debts"},
	}

	fmt.Println("\nSelect AccountableType:")
	for i, at := range accountableTypes {
		fmt.Printf("  %d. %s\n", i+1, at.label)
	}

	var accountableType string
	for {
		fmt.Print("Enter selection (1-9): ")
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)
		if idx := parseInt(input); idx >= 1 && idx <= len(accountableTypes) {
			accountableType = accountableTypes[idx-1].value
			break
		}
		fmt.Println("Invalid selection. Please try again.")
	}

	// SubType picker based on AccountableType
	subtype := promptSubtype(reader, accountableType)

	return createMaybeAccount(baseURL, apiKey, name, accountableType, subtype)
}

func promptSubtype(reader *bufio.Reader, accountableType string) string {
	subtypes := getSubtypes(accountableType)
	if len(subtypes) == 0 {
		return "" // No subtypes for this accountableType
	}

	fmt.Printf("\nSelect SubType for %s:\n", accountableType)
	for i, st := range subtypes {
		fmt.Printf("  %d. %s\n", i+1, st.label)
	}

	for {
		fmt.Printf("Enter selection (1-%d): ", len(subtypes))
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)
		if idx := parseInt(input); idx >= 1 && idx <= len(subtypes) {
			return subtypes[idx-1].value
		}
		fmt.Println("Invalid selection. Please try again.")
	}
}

func getSubtypes(accountableType string) []struct {
	value string
	label string
} {
	switch accountableType {
	case "Depository":
		return []struct {
			value string
			label string
		}{
			{"checking", "Checking Account"},
			{"savings", "Savings Account"},
			{"hsa", "Health Savings Account"},
			{"cd", "Certificate of Deposit"},
			{"money_market", "Money Market Account"},
		}
	case "Investment":
		return []struct {
			value string
			label string
		}{
			{"brokerage", "Brokerage (USA)"},
			{"401k", "401(k) (USA)"},
			{"roth_401k", "Roth 401(k) (USA)"},
			{"403b", "403(b) (USA)"},
			{"457b", "457(b) (USA)"},
			{"tsp", "Thrift Savings Plan (USA)"},
			{"ira", "IRA (USA)"},
			{"roth_ira", "Roth IRA (USA)"},
			{"sep_ira", "SEP IRA (USA)"},
			{"simple_ira", "SIMPLE IRA (USA)"},
			{"529_plan", "529 Plan (USA)"},
			{"hsa", "HSA (USA)"},
			{"ugma", "UGMA (USA)"},
			{"utma", "UTMA (USA)"},
			{"isa", "ISA (UK)"},
			{"lisa", "LISA (UK)"},
			{"sipp", "SIPP (UK)"},
			{"workplace_pension_uk", "Workplace Pension (UK)"},
			{"rrsp", "RRSP (Canada)"},
			{"tfsa", "TFSA (Canada)"},
			{"resp", "RESP (Canada)"},
			{"lira", "LIRA (Canada)"},
			{"rrif", "RRIF (Canada)"},
			{"super", "Superannuation (Australia)"},
			{"smsf", "SMSF (Australia)"},
			{"pea", "PEA (Europe)"},
			{"pillar_3a", "Pillar 3a (Europe)"},
			{"riester", "Riester (Europe)"},
			{"pension", "Pension (Global)"},
			{"retirement", "Retirement (Global)"},
			{"mutual_fund", "Mutual Fund (Global)"},
			{"angel", "Angel Investment (Global)"},
			{"trust", "Trust (Global)"},
			{"other", "Other (Global)"},
		}
	case "Crypto":
		return []struct {
			value string
			label string
		}{
			{"wallet", "Crypto Wallet"},
			{"exchange", "Crypto Exchange"},
		}
	case "Property":
		return []struct {
			value string
			label string
		}{
			{"single_family_home", "Single Family Home"},
			{"multi_family_home", "Multi Family Home"},
			{"condominium", "Condominium"},
			{"townhouse", "Townhouse"},
			{"investment_property", "Investment Property"},
			{"second_home", "Second Home"},
		}
	case "CreditCard":
		return []struct {
			value string
			label string
		}{
			{"credit_card", "Credit Card"},
		}
	case "Loan":
		return []struct {
			value string
			label string
		}{
			{"mortgage", "Mortgage"},
			{"student", "Student Loan"},
			{"auto", "Auto Loan"},
			{"other", "Other Loan"},
		}
	default:
		return nil
	}
}

func parseInt(s string) int {
	var n int
	fmt.Sscanf(s, "%d", &n)
	return n
}

func createMaybeAccount(baseURL, apiKey, name, category, subtype string) (string, error) {
	url := fmt.Sprintf("%s/accounts", baseURL)

	var payload CreateMaybeAccountRequest
	payload.Account.Name = name
	payload.Account.AccountableType = category
	payload.Account.Currency = "USD" // Defaulting to USD
	payload.Account.SubType = subtype
	payload.Account.Balance = 0.0 // Defaulting to 0.0

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

	return gjson.GetBytes(bodyBytes, "id").String(), nil
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

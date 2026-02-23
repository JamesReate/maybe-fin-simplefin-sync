package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

const (
	cacheDir = "tmp"
	// ANSI color codes
	colorRed   = "\033[31m"
	colorReset = "\033[0m"
)

// SimpleFINResponse represents the response from SimpleFIN API
type SimpleFINResponse struct {
	Errors   []string    `json:"errors,omitempty"`
	Accounts []SFAccount `json:"accounts"`
}

// SFAccount represents a SimpleFIN account
type SFAccount struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	Org              SFOrg           `json:"org"`
	Balance          string          `json:"balance"`
	AvailableBalance string          `json:"available-balance"`
	BalanceDate      uint64          `json:"balance-date"`
	Transactions     []SFTransaction `json:"transactions"`
}

// SFOrg represents the financial institution
type SFOrg struct {
	Domain  string `json:"domain"`
	SfinURL string `json:"sfin-url"`
}

// SFTransaction represents a SimpleFIN transaction
type SFTransaction struct {
	ID           string `json:"id"`
	Amount       string `json:"amount"`
	Description  string `json:"description"`
	TransactedAt int64  `json:"transacted_at"`
}

// CachedAccount holds cached account data with timestamp
type CachedAccount struct {
	Account   SFAccount `json:"account"`
	FetchedAt time.Time `json:"fetched_at"`
}

// ClaimSimpleFINToken exchanges a setup token for a permanent access URL
func ClaimSimpleFINToken(setupToken string) string {
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

// FetchSimpleFINData fetches accounts and transactions from SimpleFIN
func FetchSimpleFINData(accessURL string, forceRefresh bool) SimpleFINResponse {
	if _, err := os.Stat(cacheDir); os.IsNotExist(err) {
		os.Mkdir(cacheDir, 0755)
	}

	// Load account sync state to track last sync dates
	accountSyncState := LoadAccountSyncState()

	// Step 1: Fetch accounts with balances only (no transactions)
	log.Println("Fetching account balances from SimpleFIN...")
	balancesURL := accessURL + "/accounts?balances-only=1"
	resp, err := http.Get(balancesURL)
	if err != nil || resp.StatusCode != 200 {
		log.Fatalf("Failed to fetch SimpleFIN balances. Status: %v", resp.StatusCode)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	var sfResp SimpleFINResponse
	if err := json.Unmarshal(bodyBytes, &sfResp); err != nil {
		log.Fatalf("Failed to decode SimpleFIN response: %v\nResponse body: %s", err, string(bodyBytes))
	}

	// Print any errors from SimpleFIN
	printSimpleFINErrors(sfResp.Errors)

	log.Printf("Found %d accounts\n", len(sfResp.Accounts))

	// Step 2: Fetch transactions for each account individually
	totalTransactions := 0
	for i := range sfResp.Accounts {
		account := &sfResp.Accounts[i]

		// Determine date range for transaction fetch
		startDate, endDate := getTransactionDateRange(account.ID, accountSyncState)

		log.Printf("Fetching transactions for account %s (%s)...", account.Name, account.ID)

		// Build URL with account ID and date parameters
		txURL := fmt.Sprintf("%s/accounts?account=%s", accessURL, account.ID)
		if startDate != "" {
			txURL += fmt.Sprintf("&start-date=%s", startDate)
		}
		if endDate != "" {
			txURL += fmt.Sprintf("&end-date=%s", endDate)
		}

		txResp, err := http.Get(txURL)
		if err != nil || txResp.StatusCode != 200 {
			log.Printf("Warning: Failed to fetch transactions for account %s: %v", account.ID, err)
			continue
		}

		txBodyBytes, _ := io.ReadAll(txResp.Body)
		txResp.Body.Close()

		var accountResp SimpleFINResponse
		if err := json.Unmarshal(txBodyBytes, &accountResp); err != nil {
			log.Printf("Warning: Failed to decode transactions for account %s: %v\nResponse body: %s", account.ID, err, string(txBodyBytes))
			continue
		}

		// Print any errors from SimpleFIN for this account
		printSimpleFINErrors(accountResp.Errors)

		// Extract transactions from the response
		if len(accountResp.Accounts) > 0 {
			account.Transactions = accountResp.Accounts[0].Transactions
			totalTransactions += len(account.Transactions)
			log.Printf("  → Pulled %d transactions", len(account.Transactions))
		} else {
			log.Printf("  → No transactions found")
		}

		// Update sync state for this account
		accountSyncState[account.ID] = AccountSyncState{
			LastSyncDate: time.Now().Format("2006-01-02"),
		}

		// Cache the account data
		cached := CachedAccount{
			Account:   *account,
			FetchedAt: time.Now(),
		}
		data, _ := json.MarshalIndent(cached, "", "  ")
		os.WriteFile(cacheDir+"/account_"+account.ID+".json", data, 0644)
	}

	// Save updated sync state
	SaveAccountSyncState(accountSyncState)

	log.Printf("Total transactions pulled: %d\n", totalTransactions)
	return sfResp
}

// getTransactionDateRange determines the start and end dates for fetching transactions
func getTransactionDateRange(accountID string, syncState map[string]AccountSyncState) (string, string) {
	endDate := time.Now().Format("2006-01-02")

	// Check if we have a last sync date for this account
	if state, exists := syncState[accountID]; exists && state.LastSyncDate != "" {
		// Use last sync date as start date to get only new transactions
		return state.LastSyncDate, endDate
	}

	// No previous sync - go back one year
	startDate := time.Now().AddDate(-1, 0, 0).Format("2006-01-02")
	return startDate, endDate
}

// printSimpleFINErrors prints SimpleFIN errors in red color
func printSimpleFINErrors(errors []string) {
	if len(errors) > 0 {
		for _, errMsg := range errors {
			fmt.Printf("%s⚠️  SimpleFIN Error: %s%s\n", colorRed, errMsg, colorReset)
		}
	}
}

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
	// 90 days in seconds
	maxRangeSeconds = 90 * 24 * 60 * 60
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
func FetchSimpleFINData(accessURL string, forceRefresh bool, config Config) SimpleFINResponse {
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

	// Step 2: Fetch transactions for each account individually in 90-day increments
	totalTransactions := 0
	for i := range sfResp.Accounts {
		account := &sfResp.Accounts[i]

		// Check if we should skip transactions for this account
		if accConfig, mapped := config.AccountMap[account.ID]; mapped && accConfig.BalanceOnly {
			log.Printf("Skipping transaction fetch for account %s (balance_only is set)", account.Name)
			continue
		}

		// Determine date range for transaction fetch
		totalStartDate, totalEndDate := getTransactionDateRange(account.ID, accountSyncState)

		log.Printf("Fetching transactions for account %s (%s) from %d to %d...", account.Name, account.ID, totalStartDate, totalEndDate)

		// SimpleFIN API limit: Difference between start and end date must not exceed 90 days.
		// Page through the total date range in increments of maxRangeSeconds (90 days).
		currentStartDate := totalStartDate
		for currentStartDate < totalEndDate {
			currentEndDate := currentStartDate + maxRangeSeconds
			if currentEndDate > totalEndDate {
				currentEndDate = totalEndDate
			}

			log.Printf("  → Fetching page: %d to %d", currentStartDate, currentEndDate)

			// Build URL with account ID and date parameters
			txURL := fmt.Sprintf("%s/accounts?account=%s", accessURL, account.ID)
			if currentStartDate != 0 {
				txURL += fmt.Sprintf("&start-date=%d", currentStartDate)
			}
			if currentEndDate != 0 {
				txURL += fmt.Sprintf("&end-date=%d", currentEndDate)
			}

			txResp, err := http.Get(txURL)
			if err != nil || txResp.StatusCode != 200 {
				if txResp != nil {
					txBodyBytes, _ := io.ReadAll(txResp.Body)
					txResp.Body.Close()
					log.Printf("Warning: Failed to fetch transactions for account %s. Status: %v Body: %s Error: %v", account.ID, txResp.StatusCode, string(txBodyBytes), err)
				} else {
					log.Printf("Warning: Failed to fetch transactions for account %s: %v", account.ID, err)
				}
				log.Fatalf("failed to get trxs\n")
			}

			txBodyBytes, _ := io.ReadAll(txResp.Body)
			txResp.Body.Close()

			var accountResp SimpleFINResponse
			if err := json.Unmarshal(txBodyBytes, &accountResp); err != nil {
				log.Printf("Warning: Failed to decode transactions for account %s: %v\nResponse body: %s", account.ID, err, string(txBodyBytes))
				break // Break the paging loop for this account on decode error
			}

			// Print any errors from SimpleFIN for this account
			printSimpleFINErrors(accountResp.Errors)

			// Extract transactions from the response and append to the account's transaction list
			if len(accountResp.Accounts) > 0 {
				account.Transactions = append(account.Transactions, accountResp.Accounts[0].Transactions...)
				log.Printf("    → Pulled %d transactions in this page", len(accountResp.Accounts[0].Transactions))
			}

			// Move to the next page, starting exactly where we left off to avoid missing any transactions
			currentStartDate = currentEndDate
		}

		if len(account.Transactions) > 0 {
			totalTransactions += len(account.Transactions)
			log.Printf("  → Total pulled for %s: %d transactions", account.Name, len(account.Transactions))
		} else {
			log.Printf("  → No transactions found for %s", account.Name)
		}

		// Update sync state for this account to the end of the total range we just processed
		accountSyncState[account.ID] = AccountSyncState{
			LastSyncDate: totalEndDate,
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
func getTransactionDateRange(accountID string, syncState map[string]AccountSyncState) (int64, int64) {
	endDate := time.Now().Unix()

	// Check if we have a last sync date for this account
	if state, exists := syncState[accountID]; exists && state.LastSyncDate != 0 {
		// Use last sync date as start date to get only new transactions
		return state.LastSyncDate, endDate
	}

	// No previous sync - go back one year
	startDate := time.Now().AddDate(-1, 0, 0).Unix()
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

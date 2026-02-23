package main

import (
	"flag"
	"fmt"
	"log"
	"time"
)

func main() {
	autoCreate := flag.Bool("auto-create-accounts", false, "Automatically prompt to create unmapped accounts")
	forceRefresh := flag.Bool("force-refresh", false, "Force refresh SimpleFIN data (ignore cache)")
	flag.Parse()

	config := LoadConfig()

	// Print Maybe Finance Accounts
	log.Println("Fetching accounts from Maybe Finance...")
	maybeAccounts, err := FetchMaybeAccounts(config.MaybeBaseURL, config.MaybeAPIKey)
	if err != nil {
		log.Printf("Failed to fetch Maybe accounts: %v", err)
	} else {
		fmt.Println("\nMaybe Finance Accounts:")
		for _, acc := range maybeAccounts {
			fmt.Printf("- %s (ID: %s) Balance: %s\n", acc.Name, acc.ID, acc.Balance)
		}
		fmt.Println()
	}

	state := LoadState()

	// 1. Handle SimpleFIN Authentication
	if config.AccessURL == "" && config.SetupToken != "" {
		config.AccessURL = ClaimSimpleFINToken(config.SetupToken)
		if err := SaveConfig(config); err != nil {
			log.Fatalf("Failed to save config: %v", err)
		}
		log.Println("Successfully claimed and saved permanent Access URL.")
	} else if config.AccessURL == "" {
		log.Fatal("No AccessURL or SetupToken provided in config.json")
	}

	// 2. Fetch Data from SimpleFIN
	log.Println("Fetching transactions from SimpleFIN...")
	sfData := FetchSimpleFINData(config.AccessURL, *forceRefresh)

	// 3. Process and Sync to Maybe
	newTxCount := 0
	for _, account := range sfData.Accounts {
		maybeAccountID, mapped := config.AccountMap[account.ID]
		if !mapped {
			if *autoCreate {
				var err error
				maybeAccountID, err = PromptAndCreateMaybeAccount(config.MaybeBaseURL, config.MaybeAPIKey, account)
				if err != nil {
					log.Printf("Failed to create account for %s: %v", account.Name, err)
					log.Fatalf("Please manually create the account in Maybe Finance and try again. ID: %s", account.ID)
				}
				// Save config with new mapping
				config.AccountMap[account.ID] = maybeAccountID
				if err := SaveConfig(config); err != nil {
					log.Fatalf("Failed to save config: %v", err)
				}
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

			err := CreateMaybeTransaction(config.MaybeBaseURL, config.MaybeAPIKey, payload)
			if err != nil {
				log.Printf("Failed to create tx %s: %v", tx.ID, err)
				continue
			}

			// Mark as processed and save state immediately
			state[tx.ID] = true
			if err := SaveState(state); err != nil {
				log.Printf("Warning: Failed to save state: %v", err)
			}
			newTxCount++
			log.Printf("Synced transaction: %s - %s", txDate, tx.Description)
		}
	}

	log.Printf("Sync complete. %d new transactions added.", newTxCount)
}

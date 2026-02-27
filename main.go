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
	syncMetadata := flag.Bool("sync-metadata", false, "Sync account names from Sure and update config")
	flag.Parse()

	config := LoadConfig()

	if *syncMetadata {
		syncAccountMetadata(&config)
		if err := SaveConfig(config); err != nil {
			log.Fatalf("Failed to save config: %v", err)
		}
		log.Println("Metadata sync complete.")
		return
	}

	// Print Sure Accounts
	log.Println("Fetching accounts from Sure...")
	sureAccounts, err := FetchSureAccounts(config.SureBaseURL, config.SureAPIKey)
	if err != nil {
		log.Printf("Failed to fetch Sure accounts: %v", err)
	} else {
		fmt.Println("\nSure Accounts:")
		for _, acc := range sureAccounts {
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
	sfData := FetchSimpleFINData(config.AccessURL, *forceRefresh, config)

	// 3. Process and Sync to Sure
	newTxCount := 0
	for _, account := range sfData.Accounts {
		accConfig, mapped := config.AccountMap[account.ID]
		if !mapped {
			if *autoCreate {
				var err error
				sureAccountID, err := PromptAndCreateSureAccount(config.SureBaseURL, config.SureAPIKey, account)
				if err != nil {
					log.Printf("Failed to create account for %s: %v", account.Name, err)
					log.Fatalf("Please manually create the account in Sure and try again. ID: %s", account.ID)
				}
				// Save config with new mapping
				accConfig = AccountConfig{
					SureID: sureAccountID,
					Name:   account.Name,
				}
				config.AccountMap[account.ID] = accConfig
				if err := SaveConfig(config); err != nil {
					log.Fatalf("Failed to save config: %v", err)
				}
				log.Printf("Successfully mapped SimpleFIN account %s to Sure account %s", account.Name, sureAccountID)
			} else {
				log.Printf("Skipping SimpleFIN account %s, %s (Not mapped in config): %s", account.ID, account.Name, account.Org.Domain)
				continue
			}
		}

		if accConfig.BalanceOnly {
			log.Printf("Skipping transactions for %s (balance_only is set)", accConfig.Name)
			continue
		}

		sureAccountID := accConfig.SureID

		for _, tx := range account.Transactions {
			if _, processed := state[tx.ID]; processed {
				continue // Idempotency check: skip if already processed
			}

			// Format for Sure API
			txDate := time.Unix(tx.TransactedAt, 0).Format("2006-01-02")
			txName := tx.Description
			if txName == "" {
				txName = txDate
			}
			payload := SureTransaction{
				AccountID: sureAccountID,
				Amount:    tx.Amount,
				Date:      txDate,
				Name:      txName,
				Notes:     fmt.Sprintf("Imported via SimpleFIN. ID: %s", tx.ID),
			}

			err := CreateSureTransaction(config.SureBaseURL, config.SureAPIKey, payload)
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
			log.Printf("Synced transaction: %s - %s", txDate, txName)
		}
	}

	log.Printf("Sync complete. %d new transactions added.", newTxCount)
}

func syncAccountMetadata(config *Config) {
	log.Println("Syncing account metadata from Sure...")
	sureAccounts, err := FetchSureAccounts(config.SureBaseURL, config.SureAPIKey)
	if err != nil {
		log.Fatalf("Failed to fetch Sure accounts: %v", err)
	}

	sureAccountsMap := make(map[string]SureAccount)
	for _, acc := range sureAccounts {
		sureAccountsMap[acc.ID] = acc
	}

	updated := false
	for sfID, accConfig := range config.AccountMap {
		if sureAcc, ok := sureAccountsMap[accConfig.SureID]; ok {
			if accConfig.Name != sureAcc.Name {
				log.Printf("Updating name for account %s: %s -> %s", sfID, accConfig.Name, sureAcc.Name)
				accConfig.Name = sureAcc.Name
				config.AccountMap[sfID] = accConfig
				updated = true
			}
		} else {
			log.Printf("Warning: Mapped Sure account %s not found in Sure", accConfig.SureID)
		}
	}

	if updated {
		log.Println("Metadata updated.")
	} else {
		log.Println("No metadata changes detected.")
	}
}

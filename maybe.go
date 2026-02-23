package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/tidwall/gjson"
)

// MaybeTransaction represents a transaction in Maybe Finance
type MaybeTransaction struct {
	AccountID string `json:"account_id"`
	Amount    string `json:"amount"`
	Date      string `json:"date"`
	Name      string `json:"name"`
	Notes     string `json:"notes"`
}

// MaybeAccount represents an account in Maybe Finance
type MaybeAccount struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Balance        string `json:"balance"`
	Currency       string `json:"currency"`
	Classification string `json:"classification"`
	AccountType    string `json:"account_type"`
}

// MaybeAccountsResponse represents the response from Maybe accounts endpoint
type MaybeAccountsResponse struct {
	Accounts []MaybeAccount `json:"accounts"`
}

// CreateMaybeAccountRequest represents the request to create a Maybe account
type CreateMaybeAccountRequest struct {
	Account struct {
		Name            string  `json:"name"`
		Balance         float64 `json:"balance"`
		Currency        string  `json:"currency"`
		AccountableType string  `json:"accountable_type"`
		SubType         string  `json:"sub_type"`
	} `json:"account"`
}

// FetchMaybeAccounts retrieves all accounts from Maybe Finance
func FetchMaybeAccounts(baseURL, apiKey string) ([]MaybeAccount, error) {
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

// CreateMaybeTransaction creates a new transaction in Maybe Finance
func CreateMaybeTransaction(baseURL, apiKey string, tx MaybeTransaction) error {
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

// PromptAndCreateMaybeAccount prompts the user to create a new Maybe account
func PromptAndCreateMaybeAccount(baseURL, apiKey string, sfAcc SFAccount) (string, error) {
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

// promptSubtype prompts the user to select a subtype based on the accountable type
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

// getSubtypes returns the available subtypes for a given accountable type
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

// createMaybeAccount creates a new account in Maybe Finance
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
	log.Printf("Create account API response: %s\n", string(bodyBytes))

	return gjson.GetBytes(bodyBytes, "id").String(), nil
}

// parseInt safely parses a string to an integer
func parseInt(s string) int {
	var n int
	fmt.Sscanf(s, "%d", &n)
	return n
}

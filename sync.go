package main

import (
	"encoding/json"
	"os"
)

const (
	stateFile            = "sync_state.json"
	accountSyncStateFile = "account_sync_state.json"
)

// AccountSyncState tracks the last sync date for an account
type AccountSyncState struct {
	LastSyncDate string `json:"last_sync_date"` // YYYY-MM-DD format
}

// LoadState loads the transaction sync state from disk
// Maps transaction ID -> processed status
func LoadState() map[string]bool {
	state := make(map[string]bool)
	file, err := os.ReadFile(stateFile)
	if err == nil {
		json.Unmarshal(file, &state)
	}
	return state
}

// SaveState saves the transaction sync state to disk
func SaveState(state map[string]bool) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(stateFile, data, 0644)
}

// LoadAccountSyncState loads the account sync state from disk
// Maps account ID -> sync state
func LoadAccountSyncState() map[string]AccountSyncState {
	state := make(map[string]AccountSyncState)
	file, err := os.ReadFile(accountSyncStateFile)
	if err == nil {
		json.Unmarshal(file, &state)
	}
	return state
}

// SaveAccountSyncState saves the account sync state to disk
func SaveAccountSyncState(state map[string]AccountSyncState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(accountSyncStateFile, data, 0644)
}

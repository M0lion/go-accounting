package eb

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// SessionFile is what eb-auth writes and eb-fetch reads.
type SessionFile struct {
	SessionID  string                  `json:"session_id"`
	Bank       string                  `json:"bank"`
	Country    string                  `json:"country"`
	ValidUntil string                  `json:"valid_until"`
	CreatedAt  string                  `json:"created_at"`
	Accounts   map[string]SavedAccount `json:"accounts"`           // keyed by 11-digit account number
	Unmapped   []SavedAccount          `json:"unmapped,omitempty"` // accounts without a Norwegian number
	// The full POST /sessions response. Some of it is only ever returned once.
	Raw json.RawMessage `json:"raw"`
}

type SavedAccount struct {
	UID                string `json:"uid"`
	IBAN               string `json:"iban,omitempty"`
	Name               string `json:"name,omitempty"`
	Details            string `json:"details,omitempty"`
	Product            string `json:"product,omitempty"`
	Currency           string `json:"currency,omitempty"`
	IdentificationHash string `json:"identification_hash"`
}

func ReadSessionFile(path string) (*SessionFile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s SessionFile
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if s.SessionID == "" {
		return nil, fmt.Errorf("%s has no session_id", path)
	}
	return &s, nil
}

// WriteSessionFile writes atomically with 0600 permissions: the session ID
// together with your JWT gives read access to your accounts.
func WriteSessionFile(path string, s *SessionFile) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".session-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// eb-fetch fetches booked transactions for one account from Enable Banking
// and writes them to stdout as JSON Lines, one transaction per line:
//
//	{"account":"12345678903","fetched_at":"2026-09-28T10:00:00Z","tx":{...raw EB transaction...}}
//
// Usage with a session file from eb-auth (recommended):
//
//	EB_JWT="$(eb-jwt)" eb-fetch -session-file sessions/dnb.json -account 1234.56.78903 -from 2026-09-01
//
// The account uid is looked up in the session file, so no extra bank calls
// are made. Without a session file, pass -session and eb-fetch finds the uid
// through /accounts/{uid}/details (one bank call per account in the session),
// or pass -uid directly.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"time"

	"ebtools/internal/eb"
)

type config struct {
	baseURL     string
	jwt         string
	sessionFile string
	session     string
	account     string // normalized 11-digit Norwegian account number
	uid         string
	from        string
	to          string
}

func main() {
	var c config
	var rawAccount string
	flag.StringVar(&c.jwt, "jwt", os.Getenv("EB_JWT"), "JWT from eb-jwt (env EB_JWT)")
	flag.StringVar(&c.sessionFile, "session-file", os.Getenv("EB_SESSION_FILE"), "session file from eb-auth (env EB_SESSION_FILE)")
	flag.StringVar(&c.session, "session", os.Getenv("EB_SESSION"), "session ID, if not using -session-file (env EB_SESSION)")
	flag.StringVar(&rawAccount, "account", "", "Norwegian account number, e.g. 1234.56.78903")
	flag.StringVar(&c.uid, "uid", "", "session account uid (optional; overrides the lookup)")
	flag.StringVar(&c.from, "from", "", "first booking date to fetch, YYYY-MM-DD (required)")
	flag.StringVar(&c.to, "to", "", "last booking date to fetch, YYYY-MM-DD (default: today)")
	flag.StringVar(&c.baseURL, "base-url", eb.DefaultBaseURL, "API base URL")
	flag.Parse()

	if err := run(&c, rawAccount); err != nil {
		fmt.Fprintln(os.Stderr, "eb-fetch:", err)
		os.Exit(1)
	}
}

func run(c *config, rawAccount string) error {
	if err := validate(c, rawAccount); err != nil {
		return err
	}
	api := eb.NewClient(c.baseURL, c.jwt)

	if c.sessionFile != "" {
		sf, err := eb.ReadSessionFile(c.sessionFile)
		if err != nil {
			return err
		}
		c.session = sf.SessionID
		if c.uid == "" {
			a, ok := sf.Accounts[c.account]
			if !ok {
				return fmt.Errorf("account %s is not in %s; was it included when you authorized with eb-auth?", c.account, c.sessionFile)
			}
			if a.UID == "" {
				return fmt.Errorf("account %s has no uid in this session (the bank doesn't allow fetching it)", c.account)
			}
			c.uid = a.UID
		}
	}

	if err := checkSession(api, c.session); err != nil {
		return err
	}
	if c.uid == "" {
		uid, err := findAccountUID(api, c.session, c.account)
		if err != nil {
			return err
		}
		c.uid = uid
	}

	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()

	n, err := fetchTransactions(api, c, out)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "eb-fetch: %d booked transactions for %s (%s to %s)\n", n, c.account, c.from, c.to)
	return nil
}

func validate(c *config, rawAccount string) error {
	if c.jwt == "" {
		return errors.New("missing -jwt (or EB_JWT)")
	}
	if c.sessionFile == "" && c.session == "" {
		return errors.New("missing -session-file or -session")
	}
	acct, err := eb.NormalizeAccountNumber(rawAccount)
	if err != nil {
		return fmt.Errorf("-account: %w", err)
	}
	c.account = acct

	if c.from == "" {
		return errors.New("missing -from")
	}
	if c.to == "" {
		c.to = time.Now().Format("2006-01-02")
	}
	from, err := time.Parse("2006-01-02", c.from)
	if err != nil {
		return fmt.Errorf("-from: %w", err)
	}
	to, err := time.Parse("2006-01-02", c.to)
	if err != nil {
		return fmt.Errorf("-to: %w", err)
	}
	if to.Before(from) {
		return errors.New("-to is before -from")
	}
	return nil
}

// --- Session ----------------------------------------------------------------

type session struct {
	Status string `json:"status"`
	Access struct {
		ValidUntil string `json:"valid_until"`
	} `json:"access"`
	AccountsData []struct {
		UID string `json:"uid"`
	} `json:"accounts_data"`
}

func getSession(api *eb.Client, id string) (*session, error) {
	var s session
	if err := api.Get("/sessions/"+url.PathEscape(id), nil, &s); err != nil {
		return nil, fmt.Errorf("getting session: %w", err)
	}
	return &s, nil
}

func checkSession(api *eb.Client, id string) error {
	s, err := getSession(api, id)
	if err != nil {
		return err
	}
	if s.Status != "AUTHORIZED" {
		return fmt.Errorf("session status is %s, not AUTHORIZED: run eb-auth again", s.Status)
	}
	if t, err := time.Parse(time.RFC3339, s.Access.ValidUntil); err == nil {
		if left := time.Until(t); left < 14*24*time.Hour {
			fmt.Fprintf(os.Stderr, "eb-fetch: warning: consent expires %s (in %d days); run eb-auth again soon\n",
				t.Format("2006-01-02"), int(left.Hours()/24))
		}
	}
	return nil
}

// findAccountUID is the fallback when there's no session file. It calls
// /accounts/{uid}/details for each account in the session, which contacts
// the bank and counts toward its request limits.
func findAccountUID(api *eb.Client, sessionID, account string) (string, error) {
	s, err := getSession(api, sessionID)
	if err != nil {
		return "", err
	}
	for _, a := range s.AccountsData {
		if a.UID == "" {
			continue
		}
		var d eb.AccountResource
		if err := api.Get("/accounts/"+url.PathEscape(a.UID)+"/details", nil, &d); err != nil {
			return "", fmt.Errorf("getting details for account %s: %w", a.UID, err)
		}
		if d.NorwegianAccountNumber() == account {
			return a.UID, nil
		}
	}
	return "", fmt.Errorf("account %s is not accessible in this session (%d accounts checked)", account, len(s.AccountsData))
}

// --- Transactions -----------------------------------------------------------

type transactionsPage struct {
	Transactions    []json.RawMessage `json:"transactions"`
	ContinuationKey string            `json:"continuation_key"`
}

type outputLine struct {
	Account   string          `json:"account"`
	FetchedAt string          `json:"fetched_at"`
	Tx        json.RawMessage `json:"tx"`
}

const maxPages = 1000

func fetchTransactions(api *eb.Client, c *config, out io.Writer) (int, error) {
	fetchedAt := time.Now().UTC().Format(time.RFC3339)
	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)

	q := url.Values{}
	q.Set("date_from", c.from)
	q.Set("date_to", c.to)
	q.Set("transaction_status", "BOOK")

	count := 0
	seenKeys := map[string]bool{}
	for page := 0; page < maxPages; page++ {
		var p transactionsPage
		if err := api.Get("/accounts/"+url.PathEscape(c.uid)+"/transactions", q, &p); err != nil {
			return count, fmt.Errorf("fetching transactions: %w", err)
		}
		for _, raw := range p.Transactions {
			// Filter client-side too: not every bank honours transaction_status.
			var t struct {
				Status string `json:"status"`
			}
			if err := json.Unmarshal(raw, &t); err != nil {
				return count, fmt.Errorf("decoding transaction: %w", err)
			}
			if t.Status != "BOOK" {
				continue
			}
			line := outputLine{Account: c.account, FetchedAt: fetchedAt, Tx: compact(raw)}
			if err := enc.Encode(line); err != nil {
				return count, err
			}
			count++
		}
		if p.ContinuationKey == "" {
			return count, nil
		}
		if seenKeys[p.ContinuationKey] {
			return count, errors.New("API repeated a continuation_key; stopping to avoid a loop")
		}
		seenKeys[p.ContinuationKey] = true
		q.Set("continuation_key", p.ContinuationKey)
	}
	return count, fmt.Errorf("stopped after %d pages", maxPages)
}

// compact removes whitespace so each transaction stays on one line.
func compact(raw json.RawMessage) json.RawMessage {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return raw
	}
	return buf.Bytes()
}

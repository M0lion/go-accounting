// eb-auth runs the one-time Enable Banking authorization for a bank and
// saves the resulting session, including the mapping from Norwegian account
// numbers to session account uids, to a session file for eb-fetch.
//
// List banks:
//
//	EB_JWT="$(eb-jwt)" eb-auth -list
//
// Authorize:
//
//	EB_JWT="$(eb-jwt)" eb-auth -bank DNB -redirect-url https://example.org/eb -out sessions/dnb.json
//
// eb-auth prints a link to open in your browser. After logging in with the
// bank, you are redirected to -redirect-url with a code in the query string:
//
//   - If -redirect-url is http://localhost:PORT/... or http://127.0.0.1:PORT/...,
//     eb-auth listens there and picks up the code automatically.
//   - Otherwise, copy the full URL from the browser's address bar after the
//     redirect and paste it into the terminal. The page itself doesn't need
//     to load; only the URL matters.
//
// The redirect URL must be registered on your application in the
// Enable Banking control panel.
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"ebtools/internal/eb"
)

type options struct {
	jwt         string
	baseURL     string
	country     string
	bank        string
	psuType     string
	redirectURL string
	days        int
	out         string
	list        bool
	paste       bool
	timeout     time.Duration
}

func main() {
	var o options
	flag.StringVar(&o.jwt, "jwt", os.Getenv("EB_JWT"), "JWT from eb-jwt (env EB_JWT)")
	flag.StringVar(&o.baseURL, "base-url", eb.DefaultBaseURL, "API base URL")
	flag.StringVar(&o.country, "country", "NO", "bank country code")
	flag.StringVar(&o.bank, "bank", "", "bank name exactly as shown by -list")
	flag.StringVar(&o.psuType, "psu-type", "personal", "personal or business")
	flag.StringVar(&o.redirectURL, "redirect-url", os.Getenv("EB_REDIRECT_URL"), "redirect URL registered for your app (env EB_REDIRECT_URL)")
	flag.IntVar(&o.days, "days", 0, "consent validity in days (default: the bank's maximum)")
	flag.StringVar(&o.out, "out", "", "session file to write, e.g. sessions/dnb.json")
	flag.BoolVar(&o.list, "list", false, "list available banks for -country and exit")
	flag.BoolVar(&o.paste, "paste", false, "always paste the redirect URL, even for localhost redirects")
	flag.DurationVar(&o.timeout, "timeout", 15*time.Minute, "how long to wait for the login to complete")
	flag.Parse()

	if err := run(&o); err != nil {
		fmt.Fprintln(os.Stderr, "eb-auth:", err)
		os.Exit(1)
	}
}

type aspsp struct {
	Name                   string   `json:"name"`
	Country                string   `json:"country"`
	PSUTypes               []string `json:"psu_types"`
	MaximumConsentValidity int64    `json:"maximum_consent_validity"`
	Beta                   bool     `json:"beta"`
}

func run(o *options) error {
	if o.jwt == "" {
		return errors.New("missing -jwt (or EB_JWT)")
	}
	api := eb.NewClient(o.baseURL, o.jwt)

	banks, err := listBanks(api, o.country, o.psuType)
	if err != nil {
		return err
	}
	if o.list {
		printBanks(banks)
		return nil
	}

	if o.bank == "" {
		return errors.New("missing -bank (see -list)")
	}
	if o.redirectURL == "" {
		return errors.New("missing -redirect-url (or EB_REDIRECT_URL)")
	}
	if o.out == "" {
		return errors.New("missing -out")
	}
	redirect, err := url.Parse(o.redirectURL)
	if err != nil || redirect.Scheme == "" || redirect.Host == "" {
		return fmt.Errorf("invalid -redirect-url %q", o.redirectURL)
	}

	bank, err := findBank(banks, o.bank)
	if err != nil {
		return err
	}
	validUntil, err := consentEnd(bank, o.days)
	if err != nil {
		return err
	}

	// Set up the local listener before starting, so the redirect can't
	// arrive before we're ready for it.
	var listener *callbackListener
	if !o.paste && isLocal(redirect) {
		listener, err = listen(redirect)
		if err != nil {
			return fmt.Errorf("listening on %s: %w (use -paste to paste the URL instead)", redirect.Host, err)
		}
		defer listener.close()
	}

	state := randomState()
	var started struct {
		URL string `json:"url"`
	}
	req := map[string]any{
		"access":       map[string]any{"valid_until": validUntil.Format(time.RFC3339)},
		"aspsp":        map[string]string{"name": bank.Name, "country": bank.Country},
		"state":        state,
		"redirect_url": o.redirectURL,
		"psu_type":     o.psuType,
	}
	if err := api.Post("/auth", req, &started); err != nil {
		return fmt.Errorf("starting authorization: %w", err)
	}

	fmt.Fprintf(os.Stderr, "\nOpen this link in your browser and log in with %s:\n\n  %s\n\n", bank.Name, started.URL)

	ctx, cancel := context.WithTimeout(context.Background(), o.timeout)
	defer cancel()
	var callback url.Values
	if listener != nil {
		fmt.Fprintf(os.Stderr, "Waiting for the redirect to %s ...\n", o.redirectURL)
		callback, err = listener.wait(ctx)
	} else {
		fmt.Fprintln(os.Stderr, "After logging in, copy the full URL from the address bar and paste it here:")
		callback, err = readPastedURL(ctx)
	}
	if err != nil {
		return err
	}

	code, err := checkCallback(callback, state)
	if err != nil {
		return err
	}

	var raw json.RawMessage
	if err := api.Post("/sessions", map[string]string{"code": code}, &raw); err != nil {
		return fmt.Errorf("creating session: %w", err)
	}
	sf, err := buildSessionFile(raw, bank)
	if err != nil {
		return err
	}
	if err := eb.WriteSessionFile(o.out, sf); err != nil {
		return fmt.Errorf("writing %s: %w", o.out, err)
	}
	printSummary(sf, o.out)
	return nil
}

// --- Banks ------------------------------------------------------------------

func listBanks(api *eb.Client, country, psuType string) ([]aspsp, error) {
	var resp struct {
		ASPSPs []aspsp `json:"aspsps"`
	}
	q := url.Values{"country": {country}, "psu_type": {psuType}, "service": {"AIS"}}
	if err := api.Get("/aspsps", q, &resp); err != nil {
		return nil, fmt.Errorf("listing banks: %w", err)
	}
	sort.Slice(resp.ASPSPs, func(i, j int) bool { return resp.ASPSPs[i].Name < resp.ASPSPs[j].Name })
	return resp.ASPSPs, nil
}

func printBanks(banks []aspsp) {
	for _, b := range banks {
		note := ""
		if b.Beta {
			note = "  (beta)"
		}
		fmt.Printf("%-40s max consent %3d days%s\n", b.Name, b.MaximumConsentValidity/86400, note)
	}
}

func findBank(banks []aspsp, name string) (aspsp, error) {
	var partial []string
	for _, b := range banks {
		if b.Name == name {
			return b, nil
		}
		if strings.EqualFold(b.Name, name) {
			return b, nil
		}
		if strings.Contains(strings.ToLower(b.Name), strings.ToLower(name)) {
			partial = append(partial, b.Name)
		}
	}
	if len(partial) > 0 {
		return aspsp{}, fmt.Errorf("no bank named %q; did you mean: %s", name, strings.Join(partial, ", "))
	}
	return aspsp{}, fmt.Errorf("no bank named %q (see -list)", name)
}

func consentEnd(b aspsp, days int) (time.Time, error) {
	// Stay a little under the maximum so clock differences don't push the
	// request over the bank's limit.
	limit := time.Duration(b.MaximumConsentValidity) * time.Second
	safe := limit - 10*time.Minute
	if safe <= 0 {
		return time.Time{}, fmt.Errorf("%s reports no usable consent validity", b.Name)
	}
	d := safe
	if days > 0 {
		d = time.Duration(days) * 24 * time.Hour
		if d > limit {
			return time.Time{}, fmt.Errorf("-days %d exceeds %s's maximum of %d days", days, b.Name, int(limit.Hours()/24))
		}
		if d > safe {
			d = safe
		}
	}
	return time.Now().UTC().Add(d).Truncate(time.Second), nil
}

// --- Receiving the redirect ---------------------------------------------------

func isLocal(u *url.URL) bool {
	h := u.Hostname()
	return u.Scheme == "http" && (h == "localhost" || h == "127.0.0.1" || h == "::1") && u.Port() != ""
}

type callbackListener struct {
	srv  *http.Server
	got  chan url.Values
	errc chan error
}

func listen(redirect *url.URL) (*callbackListener, error) {
	ln, err := net.Listen("tcp", redirect.Host)
	if err != nil {
		return nil, err
	}
	l := &callbackListener{got: make(chan url.Values, 1), errc: make(chan error, 1)}
	path := redirect.Path
	if path == "" {
		path = "/"
	}
	mux := http.NewServeMux()
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("code") == "" && q.Get("error") == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "eb-auth received the authorization. You can close this tab.")
		select {
		case l.got <- q:
		default: // already have one; ignore repeats (e.g. page reloads)
		}
	})
	l.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := l.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			l.errc <- err
		}
	}()
	return l, nil
}

func (l *callbackListener) wait(ctx context.Context) (url.Values, error) {
	select {
	case q := <-l.got:
		return q, nil
	case err := <-l.errc:
		return nil, err
	case <-ctx.Done():
		return nil, errors.New("timed out waiting for the redirect")
	}
}

func (l *callbackListener) close() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	l.srv.Shutdown(ctx)
}

func readPastedURL(ctx context.Context) (url.Values, error) {
	type result struct {
		q   url.Values
		err error
	}
	ch := make(chan result, 1)
	go func() {
		sc := bufio.NewScanner(os.Stdin)
		sc.Buffer(make([]byte, 64*1024), 64*1024)
		for {
			fmt.Fprint(os.Stderr, "> ")
			if !sc.Scan() {
				err := sc.Err()
				if err == nil {
					err = errors.New("no URL pasted (stdin closed)")
				}
				ch <- result{err: err}
				return
			}
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			u, err := url.Parse(line)
			if err != nil || (u.Query().Get("code") == "" && u.Query().Get("error") == "") {
				fmt.Fprintln(os.Stderr, "That doesn't look like the redirect URL (no code= in it). Try again:")
				continue
			}
			ch <- result{q: u.Query()}
			return
		}
	}()
	select {
	case r := <-ch:
		return r.q, r.err
	case <-ctx.Done():
		return nil, errors.New("timed out waiting for the pasted URL")
	}
}

func checkCallback(q url.Values, state string) (string, error) {
	if e := q.Get("error"); e != "" {
		return "", fmt.Errorf("authorization failed: %s: %s", e, q.Get("error_description"))
	}
	if q.Get("state") != state {
		return "", errors.New("state in the redirect doesn't match this run; start over (was the URL from an older attempt?)")
	}
	code := q.Get("code")
	if code == "" {
		return "", errors.New("redirect has no code")
	}
	return code, nil
}

func randomState() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// --- Session file -----------------------------------------------------------

func buildSessionFile(raw json.RawMessage, bank aspsp) (*eb.SessionFile, error) {
	var resp struct {
		SessionID string               `json:"session_id"`
		Accounts  []eb.AccountResource `json:"accounts"`
		Access    struct {
			ValidUntil string `json:"valid_until"`
		} `json:"access"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("decoding session response: %w", err)
	}
	if resp.SessionID == "" {
		return nil, errors.New("session response has no session_id")
	}

	sf := &eb.SessionFile{
		SessionID:  resp.SessionID,
		Bank:       bank.Name,
		Country:    bank.Country,
		ValidUntil: resp.Access.ValidUntil,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		Accounts:   map[string]eb.SavedAccount{},
		Raw:        raw,
	}
	for _, a := range resp.Accounts {
		saved := eb.SavedAccount{
			UID:                a.UID,
			IBAN:               a.AccountID.IBAN,
			Name:               a.Name,
			Details:            a.Details,
			Product:            a.Product,
			Currency:           a.Currency,
			IdentificationHash: a.IdentificationHash,
		}
		if n := a.NorwegianAccountNumber(); n != "" {
			sf.Accounts[n] = saved
		} else {
			sf.Unmapped = append(sf.Unmapped, saved)
		}
	}
	return sf, nil
}

func printSummary(sf *eb.SessionFile, path string) {
	fmt.Fprintf(os.Stderr, "\nSaved session for %s to %s (valid until %s).\n", sf.Bank, path, sf.ValidUntil)
	nums := make([]string, 0, len(sf.Accounts))
	for n := range sf.Accounts {
		nums = append(nums, n)
	}
	sort.Strings(nums)
	for _, n := range nums {
		a := sf.Accounts[n]
		label := strings.TrimSpace(a.Details + " " + a.Product)
		note := ""
		if a.UID == "" {
			note = "  (no uid: transactions not available)"
		}
		fmt.Fprintf(os.Stderr, "  %s.%s.%s  %s%s\n", n[:4], n[4:6], n[6:], label, note)
	}
	for _, a := range sf.Unmapped {
		fmt.Fprintf(os.Stderr, "  (no Norwegian account number) %s %s\n", a.IBAN, a.Details)
	}
	if len(sf.Accounts) == 0 && len(sf.Unmapped) == 0 {
		fmt.Fprintln(os.Stderr, "  warning: the bank returned no accounts for this consent")
	}
}

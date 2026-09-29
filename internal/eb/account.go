package eb

import (
	"errors"
	"fmt"
	"strings"
)

// NormalizeAccountNumber accepts "1234.56.78903", "1234 56 78903" or
// "12345678903" and verifies the MOD11 check digit used by Norwegian banks.
func NormalizeAccountNumber(s string) (string, error) {
	if s == "" {
		return "", errors.New("missing account number")
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.' || r == ' ':
		default:
			return "", fmt.Errorf("account number %q contains invalid character %q", s, r)
		}
	}
	n := b.String()
	if len(n) != 11 {
		return "", fmt.Errorf("account number %q must have 11 digits", s)
	}
	if !validMOD11(n) {
		return "", fmt.Errorf("account number %q fails the MOD11 check (typo?)", s)
	}
	return n, nil
}

func validMOD11(n string) bool {
	weights := []int{5, 4, 3, 2, 7, 6, 5, 4, 3, 2}
	sum := 0
	for i, w := range weights {
		sum += int(n[i]-'0') * w
	}
	check := 11 - sum%11
	if check == 11 {
		check = 0
	}
	return check != 10 && check == int(n[10]-'0')
}

// Identification is the account identifier shape used in API responses.
type Identification struct {
	Identification string `json:"identification"`
	SchemeName     string `json:"scheme_name"`
}

// AccountResource holds the fields we use from the API's account objects.
type AccountResource struct {
	AccountID struct {
		IBAN string `json:"iban"`
	} `json:"account_id"`
	AllAccountIDs      []Identification `json:"all_account_ids"`
	UID                string           `json:"uid"`
	IdentificationHash string           `json:"identification_hash"`
	Name               string           `json:"name"`
	Details            string           `json:"details"`
	Product            string           `json:"product"`
	Currency           string           `json:"currency"`
	CashAccountType    string           `json:"cash_account_type"`
}

// NorwegianAccountNumber extracts the 11-digit account number from an
// account resource, via its IBAN (NO + 2 check digits + number) or its
// other identifiers. It returns "" if none is found.
func (a AccountResource) NorwegianAccountNumber() string {
	candidates := []string{a.AccountID.IBAN}
	for _, id := range a.AllAccountIDs {
		candidates = append(candidates, id.Identification)
	}
	for _, c := range candidates {
		v := strings.ToUpper(strings.NewReplacer(" ", "", ".", "").Replace(c))
		if len(v) == 15 && strings.HasPrefix(v, "NO") {
			v = v[4:]
		}
		if len(v) == 11 && isDigits(v) && validMOD11(v) {
			return v
		}
	}
	return ""
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

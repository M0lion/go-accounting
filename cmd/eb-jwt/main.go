// eb-jwt prints a signed JWT for the Enable Banking API to stdout.
//
// Usage:
//
//	eb-jwt -app-id <application id> -key private.key [-ttl 1h]
//
// The app ID and key path can also come from EB_APP_ID and EB_KEY.
// Uses only the Go standard library.
package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"
)

// Enable Banking rejects tokens with a TTL above 24 hours.
const maxTTL = 24 * time.Hour

func main() {
	appID := flag.String("app-id", os.Getenv("EB_APP_ID"), "Enable Banking application ID (env EB_APP_ID)")
	keyPath := flag.String("key", os.Getenv("EB_KEY"), "path to PEM-encoded RSA private key (env EB_KEY)")
	ttl := flag.Duration("ttl", time.Hour, "token lifetime, max 24h")
	flag.Parse()

	if err := run(*appID, *keyPath, *ttl); err != nil {
		fmt.Fprintln(os.Stderr, "eb-jwt:", err)
		os.Exit(1)
	}
}

func run(appID, keyPath string, ttl time.Duration) error {
	if appID == "" {
		return errors.New("missing -app-id (or EB_APP_ID)")
	}
	if keyPath == "" {
		return errors.New("missing -key (or EB_KEY)")
	}
	if ttl <= 0 || ttl > maxTTL {
		return fmt.Errorf("-ttl must be between 0 and %s", maxTTL)
	}

	key, err := loadKey(keyPath)
	if err != nil {
		return err
	}

	now := time.Now().Unix()
	header := map[string]string{"typ": "JWT", "alg": "RS256", "kid": appID}
	claims := map[string]any{
		"iss": "enablebanking.com",
		"aud": "api.enablebanking.com",
		"iat": now,
		"exp": now + int64(ttl.Seconds()),
	}

	token, err := sign(header, claims, key)
	if err != nil {
		return err
	}
	fmt.Println(token)
	return nil
}

func loadKey(path string) (*rsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading key: %w", err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("key file contains no PEM block")
	}

	// openssl genrsa gives PKCS#8 ("BEGIN PRIVATE KEY") on OpenSSL 3,
	// PKCS#1 ("BEGIN RSA PRIVATE KEY") on older versions. Accept both.
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing key: %w", err)
	}
	k, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("key is not an RSA key (Enable Banking requires RS256)")
	}
	return k, nil
}

func sign(header map[string]string, claims map[string]any, key *rsa.PrivateKey) (string, error) {
	enc := base64.RawURLEncoding
	h, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	c, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signingInput := enc.EncodeToString(h) + "." + enc.EncodeToString(c)

	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("signing: %w", err)
	}
	return signingInput + "." + enc.EncodeToString(sig), nil
}

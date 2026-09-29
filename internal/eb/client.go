// Package eb contains the pieces shared by the Enable Banking tools:
// a small API client, Norwegian account number handling and the session file.
package eb

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const DefaultBaseURL = "https://api.enablebanking.com"

type Client struct {
	Base string
	JWT  string
	HTTP *http.Client
}

func NewClient(base, jwt string) *Client {
	return &Client{
		Base: strings.TrimRight(base, "/"),
		JWT:  jwt,
		HTTP: &http.Client{Timeout: 90 * time.Second},
	}
}

type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("API returned %d: %s", e.Status, e.Body)
}

// Get performs a GET and decodes the JSON response into v. It retries on
// rate limiting (429), server errors and network failures.
func (c *Client) Get(path string, query url.Values, v any) error {
	u := c.Base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(1<<attempt) * time.Second) // 2s, 4s, 8s
		}
		status, body, err := c.do(http.MethodGet, u, nil)
		if err != nil {
			lastErr = err
			continue
		}
		if status == http.StatusTooManyRequests || status >= 500 {
			lastErr = &APIError{status, body}
			continue
		}
		return decode(path, status, body, v)
	}
	return fmt.Errorf("GET %s failed after retries: %w", path, lastErr)
}

// Post sends a JSON body and decodes the JSON response into v.
// It does not retry: some calls (like POST /sessions) use one-time codes.
func (c *Client) Post(path string, payload, v any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	status, body, err := c.do(http.MethodPost, c.Base+path, b)
	if err != nil {
		return fmt.Errorf("POST %s: %w", path, err)
	}
	return decode(path, status, body, v)
}

func (c *Client) do(method, u string, payload []byte) (int, string, error) {
	var r io.Reader
	if payload != nil {
		r = bytes.NewReader(payload)
	}
	req, err := http.NewRequest(method, u, r)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.JWT)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, "", err
	}
	return resp.StatusCode, string(body), nil
}

func decode(path string, status int, body string, v any) error {
	if status != http.StatusOK {
		e := &APIError{status, strings.TrimSpace(body)}
		if status == http.StatusUnauthorized {
			return fmt.Errorf("JWT rejected (expired, or wrong app ID/key?): %w", e)
		}
		return e
	}
	if err := json.Unmarshal([]byte(body), v); err != nil {
		return fmt.Errorf("decoding response from %s: %w", path, err)
	}
	return nil
}

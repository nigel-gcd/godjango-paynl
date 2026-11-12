// Package client is a small, dependency-free client for the Pay.nl (PAY.) APIs.
//
// It covers the parts of the API needed for basic payment workflows:
//
//   - Creating orders (transactions) through the Order API (connect.pay.nl/v1/orders)
//   - Retrieving an order's status
//   - Capturing, voiding, aborting, approving and declining orders
//   - Refunding transactions through the REST API (rest.pay.nl/v2/transactions/{id}/refund)
//
// The client intentionally does not depend on go-django, so it can be used
// (and tested) on its own. The go-django integration lives in the parent package.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// DefaultConnectURL is the base URL for the Order API (TGU).
	DefaultConnectURL = "https://connect.pay.nl"

	// DefaultRestURL is the base URL for the REST API, used for refunds.
	DefaultRestURL = "https://rest.pay.nl"

	// DefaultUserAgent is sent with every request unless overridden.
	DefaultUserAgent = "go-django-paynl/0.1"

	// DefaultTimeout is used when no custom *http.Client is provided.
	DefaultTimeout = 30 * time.Second
)

// Config holds the credentials and endpoints used by the client.
//
// Pay.nl authenticates using HTTP Basic auth. The username is either an
// API token code (AT-xxxx-xxxx) or a sales location / service ID (SL-xxxx-xxxx),
// the password is respectively the API token or the service secret.
type Config struct {
	// Username is the token code (AT-xxxx-xxxx) or service ID (SL-xxxx-xxxx).
	Username string

	// Password is the API token or the service secret.
	Password string

	// ServiceID is the sales location (SL-xxxx-xxxx) orders are created for.
	// If empty, the ServiceID set on the request is used.
	ServiceID string

	// TestMode marks created orders as test orders.
	TestMode bool

	// ConnectURL overrides DefaultConnectURL.
	ConnectURL string

	// RestURL overrides DefaultRestURL.
	RestURL string

	// UserAgent overrides DefaultUserAgent.
	UserAgent string

	// HTTPClient overrides the default http client.
	HTTPClient *http.Client
}

// Client talks to the Pay.nl APIs. It is safe for concurrent use.
type Client struct {
	cfg  Config
	http *http.Client
}

// New creates a new client from the given config.
func New(cfg Config) (*Client, error) {
	if cfg.Username == "" || cfg.Password == "" {
		return nil, errors.New("paynl: username and password are required")
	}

	if cfg.ConnectURL == "" {
		cfg.ConnectURL = DefaultConnectURL
	}
	if cfg.RestURL == "" {
		cfg.RestURL = DefaultRestURL
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = DefaultUserAgent
	}

	cfg.ConnectURL = strings.TrimRight(cfg.ConnectURL, "/")
	cfg.RestURL = strings.TrimRight(cfg.RestURL, "/")

	var httpClient = cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: DefaultTimeout}
	}

	return &Client{cfg: cfg, http: httpClient}, nil
}

// Config returns a copy of the client's configuration.
func (c *Client) Config() Config {
	return c.cfg
}

// do executes a request against baseURL+path, JSON encoding body (if not nil)
// and JSON decoding the response into out (if not nil).
func (c *Client) do(ctx context.Context, method, baseURL, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		var buf bytes.Buffer
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return fmt.Errorf("paynl: encode request: %w", err)
		}
		reader = &buf
	}

	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("paynl: build request: %w", err)
	}

	req.SetBasicAuth(c.cfg.Username, c.cfg.Password)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.cfg.UserAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("paynl: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("paynl: read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return newAPIError(resp.StatusCode, data)
	}

	if out == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}

	if raw, ok := out.(*json.RawMessage); ok {
		*raw = append((*raw)[:0], data...)
		return nil
	}

	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("paynl: decode response: %w", err)
	}

	if r, ok := out.(rawSetter); ok {
		r.setRaw(data)
	}

	return nil
}

// rawSetter is implemented by response types which keep the raw response body.
type rawSetter interface {
	setRaw([]byte)
}

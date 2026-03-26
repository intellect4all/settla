// Package mockhttp provides provider factories that delegate to an external
// mock provider HTTP service. This enables dynamic control of provider behavior
// (latency, error rates, outages) during live demos via the admin API.
//
// Set SETTLA_PROVIDER_MODE=mock-http and SETTLA_PROVIDER_MOCK_ONRAMP_GBP_MOCKPROVIDER_URL
// (or per-provider equivalent) to activate.
package mockhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const defaultBaseURL = "http://mockprovider:9095"

// Client is a shared HTTP client for communicating with the mock provider service.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient creates a Client pointing at the given base URL.
func NewClient(baseURL string) *Client {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// doPost sends a POST request and decodes the JSON response into result.
func (c *Client) doPost(ctx context.Context, path string, body any, result any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("settla-mockhttp: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("settla-mockhttp: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("settla-mockhttp: %s: %w", path, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("settla-mockhttp: read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return fmt.Errorf("settla-mockhttp: %s returned %d: %s", path, resp.StatusCode, string(respBody))
	}

	if result != nil {
		if err := json.Unmarshal(respBody, result); err != nil {
			return fmt.Errorf("settla-mockhttp: decode response: %w", err)
		}
	}
	return nil
}

// doGet sends a GET request and decodes the JSON response into result.
func (c *Client) doGet(ctx context.Context, path string, result any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("settla-mockhttp: create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("settla-mockhttp: %s: %w", path, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("settla-mockhttp: read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return fmt.Errorf("settla-mockhttp: %s returned %d: %s", path, resp.StatusCode, string(respBody))
	}

	if result != nil {
		if err := json.Unmarshal(respBody, result); err != nil {
			return fmt.Errorf("settla-mockhttp: decode response: %w", err)
		}
	}
	return nil
}

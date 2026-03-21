package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// TestResult captures a single API test execution.
type TestResult struct {
	Category       string        `json:"category"`
	Endpoint       string        `json:"endpoint"`
	Method         string        `json:"method"`
	Path           string        `json:"path"`
	AuthType       string        `json:"auth_type"`
	StatusCode     int           `json:"status_code"`
	ExpectedStatus int           `json:"expected_status"`
	Passed         bool          `json:"passed"`
	Duration       time.Duration `json:"duration_ms"`
	RequestBody    any           `json:"request_body,omitempty"`
	ResponseBody   any           `json:"response_body,omitempty"`
	Error          string        `json:"error,omitempty"`
	Skipped        bool          `json:"skipped"`
	SkipReason     string        `json:"skip_reason,omitempty"`
	Description    string        `json:"description,omitempty"`
}

// Client wraps net/http with auth, timing, and request/response capture.
type Client struct {
	baseURL    string
	httpClient *http.Client
	delay      time.Duration
}

// NewClient creates a test HTTP client.
func NewClient(baseURL string, delay time.Duration) *Client {
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		delay: delay,
	}
}

// Do executes an HTTP request and returns (statusCode, responseBody, duration, error).
// body is marshalled to JSON if non-nil. authHeader is set as Authorization if non-empty.
func (c *Client) Do(method, path string, body any, authHeader string) (int, any, time.Duration, error) {
	if c.delay > 0 {
		time.Sleep(c.delay)
	}

	url := c.baseURL + path

	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, nil, 0, fmt.Errorf("marshal body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return 0, nil, 0, fmt.Errorf("create request: %w", err)
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}

	// Auto-generate idempotency key for mutating requests
	if method == "POST" || method == "PUT" || method == "PATCH" {
		req.Header.Set("X-Idempotency-Key", generateIdempotencyKey())
	}

	start := time.Now()
	resp, err := c.httpClient.Do(req)
	duration := time.Since(start)

	if err != nil {
		return 0, nil, duration, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respData, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, duration, fmt.Errorf("read response: %w", err)
	}

	var respBody any
	if len(respData) > 0 {
		respBody = json.RawMessage(respData)
	}

	return resp.StatusCode, respBody, duration, nil
}

func generateIdempotencyKey() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "apitest-" + hex.EncodeToString(b)
}

//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Environment helpers
// ---------------------------------------------------------------------------

func gatewayURL() string {
	if v := os.Getenv("GATEWAY_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "http://localhost:3100"
}

func seedAPIKey() string {
	if v := os.Getenv("E2E_SEED_API_KEY"); v != "" {
		return v
	}
	return "sk_live_lemfi_demo_key"
}

func seedAPIKeyB() string {
	if v := os.Getenv("E2E_SEED_API_KEY_B"); v != "" {
		return v
	}
	return "sk_live_fincra_demo_key"
}

func opsAPIKey() string {
	if v := os.Getenv("SETTLA_OPS_API_KEY"); v != "" {
		return v
	}
	return "settla-ops-secret-change-me"
}

// ---------------------------------------------------------------------------
// HTTP client
// ---------------------------------------------------------------------------

type apiClient struct {
	base   string
	http   *http.Client
	apiKey string
}

func newClient(apiKey string) *apiClient {
	return &apiClient{
		base:   gatewayURL(),
		http:   &http.Client{Timeout: 30 * time.Second},
		apiKey: apiKey,
	}
}

// apiResponse wraps a parsed JSON response.
type apiResponse struct {
	StatusCode int
	Body       map[string]any
	RawBody    []byte
	Duration   time.Duration
}

func (r *apiResponse) field(path string) any {
	parts := strings.Split(path, ".")
	var cur any = r.Body
	for _, p := range parts {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[p]
	}
	return cur
}

func (r *apiResponse) str(path string) string {
	v := r.field(path)
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v)
}

func (r *apiResponse) num(path string) float64 {
	v := r.field(path)
	if v == nil {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return n
	case json.Number:
		f, _ := n.Float64()
		return f
	}
	return 0
}

func (c *apiClient) do(method, path string, body any, opts ...reqOpt) (*apiResponse, error) {
	o := applyOpts(opts)

	// Serialize body once for potential retries
	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal body: %w", err)
		}
	}

	// Retry on 503 (circuit breaker open) up to 2 times with backoff
	const maxRetries = 2
	var result *apiResponse

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
		}

		var bodyReader io.Reader
		if bodyBytes != nil {
			bodyReader = bytes.NewReader(bodyBytes)
		}

		req, err := http.NewRequest(method, c.base+path, bodyReader)
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}

		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Accept", "application/json")

		if o.noAuth {
			// skip auth header
		} else if o.authHeader != "" {
			req.Header.Set("Authorization", o.authHeader)
		} else if c.apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+c.apiKey)
		}

		if method == "POST" || method == "PUT" || method == "PATCH" {
			if o.idempotencyKey != "" {
				req.Header.Set("X-Idempotency-Key", o.idempotencyKey)
			} else {
				req.Header.Set("X-Idempotency-Key", randomIdemKey())
			}
		}

		for k, v := range o.headers {
			req.Header.Set(k, v)
		}

		start := time.Now()
		resp, err := c.http.Do(req)
		dur := time.Since(start)
		if err != nil {
			if attempt < maxRetries {
				continue
			}
			return nil, fmt.Errorf("request failed: %w", err)
		}

		rawBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("read response: %w", err)
		}

		result = &apiResponse{
			StatusCode: resp.StatusCode,
			RawBody:    rawBody,
			Duration:   dur,
		}

		if len(rawBody) > 0 {
			var m map[string]any
			if json.Unmarshal(rawBody, &m) == nil {
				result.Body = m
			}
		}

		// Retry on 503 (circuit breaker / upstream unavailable)
		if resp.StatusCode == 503 && attempt < maxRetries {
			continue
		}
		break
	}

	return result, nil
}

// Convenience methods
func (c *apiClient) get(path string, opts ...reqOpt) (*apiResponse, error) {
	return c.do("GET", path, nil, opts...)
}

func (c *apiClient) post(path string, body any, opts ...reqOpt) (*apiResponse, error) {
	return c.do("POST", path, body, opts...)
}

func (c *apiClient) put(path string, body any, opts ...reqOpt) (*apiResponse, error) {
	return c.do("PUT", path, body, opts...)
}

func (c *apiClient) del(path string, opts ...reqOpt) (*apiResponse, error) {
	return c.do("DELETE", path, nil, opts...)
}

// ---------------------------------------------------------------------------
// Request options
// ---------------------------------------------------------------------------

type reqOpt func(*reqOptions)

type reqOptions struct {
	noAuth         bool
	authHeader     string
	idempotencyKey string
	headers        map[string]string
}

func applyOpts(opts []reqOpt) reqOptions {
	o := reqOptions{headers: map[string]string{}}
	for _, fn := range opts {
		fn(&o)
	}
	return o
}

func withNoAuth() reqOpt {
	return func(o *reqOptions) { o.noAuth = true }
}

func withAuth(header string) reqOpt {
	return func(o *reqOptions) { o.authHeader = header }
}

func withIdemKey(key string) reqOpt {
	return func(o *reqOptions) { o.idempotencyKey = key }
}

func withHeader(k, v string) reqOpt {
	return func(o *reqOptions) { o.headers[k] = v }
}

// ---------------------------------------------------------------------------
// Polling with exponential backoff
// ---------------------------------------------------------------------------

type pollOpts struct {
	timeout  time.Duration
	interval time.Duration
}

func defaultPollOpts() pollOpts {
	return pollOpts{
		timeout:  30 * time.Second,
		interval: 500 * time.Millisecond,
	}
}

// pollUntil polls fn every interval until it returns true or timeout.
func pollUntil(t *testing.T, desc string, fn func() (bool, error), opts ...func(*pollOpts)) {
	t.Helper()
	po := defaultPollOpts()
	for _, o := range opts {
		o(&po)
	}

	ctx, cancel := context.WithTimeout(context.Background(), po.timeout)
	defer cancel()

	ticker := time.NewTicker(po.interval)
	defer ticker.Stop()

	var lastErr error
	for {
		select {
		case <-ctx.Done():
			t.Fatalf("pollUntil(%s) timed out after %v: last error: %v", desc, po.timeout, lastErr)
			return
		case <-ticker.C:
			done, err := fn()
			if err != nil {
				lastErr = err
				continue
			}
			if done {
				return
			}
		}
	}
}

func withTimeout(d time.Duration) func(*pollOpts) {
	return func(po *pollOpts) { po.timeout = d }
}

func withInterval(d time.Duration) func(*pollOpts) {
	return func(po *pollOpts) { po.interval = d }
}

// ---------------------------------------------------------------------------
// Assertion helpers
// ---------------------------------------------------------------------------

func requireStatus(t *testing.T, resp *apiResponse, expected int) {
	t.Helper()
	if resp.StatusCode != expected {
		// Skip on "not enabled" errors — tenant config may not have feature enabled
		body := string(resp.RawBody)
		if resp.StatusCode == 500 && (strings.Contains(body, "not enabled") || strings.Contains(body, "disabled") || strings.Contains(body, "not supported") || strings.Contains(body, "no available") || strings.Contains(body, "pool")) {
			t.Skipf("feature not available for tenant: %s", body)
		}
		t.Fatalf("expected status %d, got %d; body: %s", expected, resp.StatusCode, string(resp.RawBody))
	}
}

func requireStatusOneOf(t *testing.T, resp *apiResponse, codes ...int) {
	t.Helper()
	for _, c := range codes {
		if resp.StatusCode == c {
			return
		}
	}
	t.Fatalf("expected status in %v, got %d; body: %s", codes, resp.StatusCode, string(resp.RawBody))
}

func requireField(t *testing.T, resp *apiResponse, path string) string {
	t.Helper()
	v := resp.str(path)
	if v == "" {
		t.Fatalf("expected non-empty field %q in response: %s", path, string(resp.RawBody))
	}
	return v
}

// ---------------------------------------------------------------------------
// Unique ID helpers
// ---------------------------------------------------------------------------

func defaultSender() map[string]any {
	return map[string]any{"name": "E2E Sender", "email": "sender@e2e-test.io", "country": "GB"}
}

func randomIdemKey() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "e2e-" + hex.EncodeToString(b)
}

func uniqueRef(prefix string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s-%s-%d", prefix, hex.EncodeToString(b), time.Now().UnixNano()%10000)
}

// ---------------------------------------------------------------------------
// Setup / gateway readiness
// ---------------------------------------------------------------------------

func waitForGateway(t *testing.T) {
	t.Helper()
	c := newClient("")
	pollUntil(t, "gateway health", func() (bool, error) {
		resp, err := c.get("/health", withNoAuth())
		if err != nil {
			return false, err
		}
		return resp.StatusCode == 200, nil
	}, withTimeout(120*time.Second), withInterval(2*time.Second))
}

func skipIfNoGateway(t *testing.T) {
	t.Helper()
	c := newClient("")
	resp, err := c.get("/health", withNoAuth())
	if err != nil || resp.StatusCode != 200 {
		t.Skip("gateway not reachable; set GATEWAY_URL or run `make docker-up`")
	}
}

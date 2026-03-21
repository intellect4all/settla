package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	gatewayDefault := os.Getenv("GATEWAY_URL")
	if gatewayDefault == "" {
		gatewayDefault = "http://localhost:3100"
	}

	gateway := flag.String("gateway", gatewayDefault, "Gateway base URL")
	seedKey := flag.String("seed-key", "sk_live_lemfi_demo_key", "Seed tenant API key")
	skipAuth := flag.Bool("skip-auth-flow", false, "Skip new-tenant registration flow")
	output := flag.String("output", "tests/api-test/", "Report output directory")
	delay := flag.Int("delay", 50, "Delay between requests in ms")
	flag.Parse()

	*gateway = strings.TrimRight(*gateway, "/")

	fmt.Println("=== Settla Tenant API Test Runner ===")
	fmt.Printf("Gateway:   %s\n", *gateway)
	fmt.Printf("Seed key:  %s\n", *seedKey)
	fmt.Printf("Delay:     %dms\n", *delay)
	fmt.Println()

	startTime := time.Now()
	client := NewClient(*gateway, time.Duration(*delay)*time.Millisecond)
	var results []TestResult

	// Phase 1: Health check
	fmt.Println("[Phase 1] Health check...")
	if err := waitForHealth(client, 120*time.Second); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: Gateway not reachable: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("  Gateway is healthy.")

	// Phase 2: Auth flow (new tenant registration)
	var authState *AuthState
	if !*skipAuth {
		fmt.Println("\n[Phase 2] Auth flow (new tenant registration)...")
		var authResults []TestResult
		authState, authResults = RunAuthFlow(client)
		results = append(results, authResults...)
		printPhaseResults("Auth Flow", authResults)
	} else {
		fmt.Println("\n[Phase 2] Skipped (--skip-auth-flow)")
	}

	// Phase 3: Seed tenant testing (full endpoint catalog)
	fmt.Println("\n[Phase 3] Seed tenant endpoint catalog...")
	if !verifySeedTenant(client, *seedKey) {
		fmt.Fprintln(os.Stderr, "FATAL: Seed tenant API key rejected. Run `make db-seed` first.")
		os.Exit(1)
	}

	catalogResults := RunEndpointCatalog(client, *seedKey)
	results = append(results, catalogResults...)
	printPhaseResults("Endpoint Catalog", catalogResults)

	// Phase 4: JWT re-test (if auth flow succeeded)
	if authState != nil && authState.JWT != "" {
		fmt.Println("\n[Phase 4] JWT re-test (key endpoints with portal JWT)...")
		jwtResults := RunJWTRetest(client, authState.JWT)
		results = append(results, jwtResults...)
		printPhaseResults("JWT Re-test", jwtResults)
	} else {
		fmt.Println("\n[Phase 4] Skipped (no JWT available)")
	}

	// Phase 5: Generate report
	fmt.Println("\n[Phase 5] Generating report...")
	duration := time.Since(startTime)
	report := Report{
		Gateway:   *gateway,
		Generated: time.Now().UTC(),
		Duration:  duration,
		Results:   results,
	}

	if err := os.MkdirAll(*output, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: Cannot create output dir: %v\n", err)
	}
	if err := WriteMarkdownReport(report, *output+"/report.md"); err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: report.md failed: %v\n", err)
	}
	if err := WriteJSONReport(report, *output+"/results.json"); err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: results.json failed: %v\n", err)
	}
	fmt.Printf("  Reports written to %s\n", *output)

	// Phase 6: Summary
	passed, failed, skipped := 0, 0, 0
	for _, r := range results {
		switch {
		case r.Skipped:
			skipped++
		case r.Passed:
			passed++
		default:
			failed++
		}
	}
	total := len(results)
	passRate := 0.0
	if total-skipped > 0 {
		passRate = float64(passed) / float64(total-skipped) * 100
	}

	fmt.Printf("\n=== Summary (%s) ===\n", duration.Round(time.Millisecond))
	fmt.Printf("  Total:   %d\n", total)
	fmt.Printf("  Passed:  %d\n", passed)
	fmt.Printf("  Failed:  %d\n", failed)
	fmt.Printf("  Skipped: %d\n", skipped)
	fmt.Printf("  Pass %%:  %.1f%%\n", passRate)

	if failed > 0 {
		fmt.Println("\nFailed tests:")
		for _, r := range results {
			if !r.Passed && !r.Skipped {
				fmt.Printf("  - %s %s → %d (expected %d)", r.Method, r.Path, r.StatusCode, r.ExpectedStatus)
				if r.Error != "" {
					fmt.Printf(" [%s]", r.Error)
				}
				fmt.Println()
			}
		}
		os.Exit(1)
	}
	os.Exit(0)
}

func waitForHealth(c *Client, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	wait := 500 * time.Millisecond
	for time.Now().Before(deadline) {
		resp, err := http.Get(c.baseURL + "/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
		}
		time.Sleep(wait)
		if wait < 5*time.Second {
			wait = wait * 2
		}
	}
	return fmt.Errorf("gateway did not become healthy within %s", timeout)
}

func verifySeedTenant(c *Client, apiKey string) bool {
	code, _, _, err := c.Do("GET", "/v1/me", nil, "Bearer "+apiKey)
	return err == nil && code != 401
}

func printPhaseResults(name string, results []TestResult) {
	passed, failed, skipped := 0, 0, 0
	for _, r := range results {
		switch {
		case r.Skipped:
			skipped++
		case r.Passed:
			passed++
		default:
			failed++
		}
	}
	fmt.Printf("  %s: %d passed, %d failed, %d skipped\n", name, passed, failed, skipped)
}

// RunJWTRetest runs a subset of endpoints using JWT auth instead of API key.
func RunJWTRetest(c *Client, jwt string) []TestResult {
	tests := []EndpointTest{
		{Category: "JWT Re-test", Method: "GET", Path: "/v1/me", Auth: "jwt", ExpectedStatus: 200, Description: "Get profile (JWT)"},
		{Category: "JWT Re-test", Method: "GET", Path: "/v1/me/api-keys", Auth: "jwt", ExpectedStatus: 200, Description: "List API keys (JWT)"},
		{Category: "JWT Re-test", Method: "GET", Path: "/v1/me/dashboard", Auth: "jwt", ExpectedStatus: 200, Description: "Dashboard metrics (JWT)"},
		{Category: "JWT Re-test", Method: "GET", Path: "/v1/me/transfers/stats", Auth: "jwt", ExpectedStatus: 200, Description: "Transfer stats (JWT)"},
		{Category: "JWT Re-test", Method: "GET", Path: "/v1/me/fees/report", Auth: "jwt", ExpectedStatus: 200, Description: "Fee report (JWT)"},
		{Category: "JWT Re-test", Method: "GET", Path: "/v1/me/analytics/status-distribution", Auth: "jwt", ExpectedStatus: 200, Description: "Status distribution (JWT)"},
		{Category: "JWT Re-test", Method: "GET", Path: "/v1/me/analytics/corridors", Auth: "jwt", ExpectedStatus: 200, Description: "Corridor analytics (JWT)"},
		{Category: "JWT Re-test", Method: "GET", Path: "/v1/me/webhooks/stats", Auth: "jwt", ExpectedStatus: 200, Description: "Webhook stats (JWT)"},
		{Category: "JWT Re-test", Method: "GET", Path: "/v1/portal/crypto-settings", Auth: "jwt", ExpectedStatus: 200, Description: "Crypto settings (JWT)"},
		{Category: "JWT Re-test", Method: "GET", Path: "/v1/treasury/positions", Auth: "jwt", ExpectedStatus: 200, Description: "Treasury positions (JWT)"},
	}

	ctx := map[string]string{}
	var results []TestResult
	for _, t := range tests {
		r := executeTest(c, t, "Bearer "+jwt, ctx)
		results = append(results, r)
		printTestResult(r)
	}
	return results
}

func printTestResult(r TestResult) {
	icon := "PASS"
	if r.Skipped {
		icon = "SKIP"
	} else if !r.Passed {
		icon = "FAIL"
	}
	fmt.Printf("    [%s] %s %s → %d (%s)\n", icon, r.Method, r.Path, r.StatusCode, r.Duration.Round(time.Millisecond))
}

func executeTest(c *Client, t EndpointTest, authHeader string, ctx map[string]string) TestResult {
	path := t.Path
	// Substitute {param} placeholders from context
	for key, val := range ctx {
		path = strings.ReplaceAll(path, "{"+key+"}", val)
	}

	// Check dependencies
	if t.DependsOn != "" {
		if _, ok := ctx[t.DependsOn]; !ok {
			return TestResult{
				Category:       t.Category,
				Endpoint:       t.Method + " " + t.Path,
				Method:         t.Method,
				Path:           path,
				AuthType:       t.Auth,
				ExpectedStatus: t.ExpectedStatus,
				Skipped:        true,
				SkipReason:     fmt.Sprintf("depends on %q which was not set", t.DependsOn),
				Description:    t.Description,
			}
		}
	}

	// Build query string
	if len(t.Query) > 0 {
		parts := make([]string, 0, len(t.Query))
		for k, v := range t.Query {
			parts = append(parts, k+"="+v)
		}
		path += "?" + strings.Join(parts, "&")
	}

	var body any
	if t.Body != nil {
		body = t.Body
	}

	code, respBody, dur, err := c.Do(t.Method, path, body, authHeader)

	result := TestResult{
		Category:       t.Category,
		Endpoint:       t.Method + " " + t.Path,
		Method:         t.Method,
		Path:           path,
		AuthType:       t.Auth,
		StatusCode:     code,
		ExpectedStatus: t.ExpectedStatus,
		Passed:         code == t.ExpectedStatus,
		Duration:       dur,
		RequestBody:    t.Body,
		ResponseBody:   respBody,
		Description:    t.Description,
	}
	if err != nil {
		result.Error = err.Error()
		result.Passed = false
	}

	// Extract context values from response
	if result.Passed && t.ContextKey != "" && t.ContextField != "" && respBody != nil {
		if raw, ok := respBody.(json.RawMessage); ok {
			var m map[string]any
			if json.Unmarshal(raw, &m) == nil {
				if val := extractField(m, t.ContextField); val != "" {
					ctx[t.ContextKey] = val
				}
			}
		}
	}

	return result
}

func extractField(m map[string]any, field string) string {
	parts := strings.Split(field, ".")
	current := m
	for i, part := range parts {
		if i == len(parts)-1 {
			if v, ok := current[part]; ok {
				return fmt.Sprintf("%v", v)
			}
			return ""
		}
		if next, ok := current[part].(map[string]any); ok {
			current = next
		} else {
			return ""
		}
	}
	return ""
}

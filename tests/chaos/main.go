package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/google/uuid"
)

// ChaosConfig configures the chaos test framework.
type ChaosConfig struct {
	ComposeFile     string // Path to docker-compose.yml
	EnvFile         string // Path to .env
	GatewayURL      string
	ServerURL       string
	LoadTPS         int           // TPS during chaos (default: 500)
	LoadDuration    time.Duration // How long to run load before injection
	RecoveryWait    time.Duration // How long to wait after injection for recovery
	MaxScenarioTime time.Duration // Max time per scenario
}

// DefaultChaosConfig returns sensible defaults.
func DefaultChaosConfig() ChaosConfig {
	return ChaosConfig{
		ComposeFile:     "deploy/docker-compose.yml",
		EnvFile:         ".env",
		GatewayURL:      "http://localhost:3100",
		ServerURL:       "http://localhost:8080",
		LoadTPS:         500,
		LoadDuration:    60 * time.Second,
		RecoveryWait:    30 * time.Second,
		MaxScenarioTime: 5 * time.Minute,
	}
}

// ChaosRunner orchestrates chaos test scenarios.
type ChaosRunner struct {
	config     ChaosConfig
	logger     *slog.Logger
	httpClient *http.Client
	results    []ScenarioResult
}

// ScenarioResult tracks the outcome of a single chaos scenario.
type ScenarioResult struct {
	Name               string
	Duration           time.Duration
	RequestsAffected   int64
	TotalRequests      int64
	AffectedPercent    float64
	RecoveryTime       time.Duration
	DataConsistent     bool
	LedgerBalanced     bool
	TreasuryConsistent bool
	StuckTransfers     int64
	Passed             bool
	FailReason         string
}

// NewChaosRunner creates a new chaos test runner.
func NewChaosRunner(config ChaosConfig, logger *slog.Logger) *ChaosRunner {
	return &ChaosRunner{
		config:     config,
		logger:     logger,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// Run executes all chaos scenarios sequentially.
func (c *ChaosRunner) Run(ctx context.Context) error {
	scenarios := []struct {
		name string
		fn   func(context.Context) ScenarioResult
	}{
		{"TigerBeetle Restart", c.scenarioTigerBeetleRestart},
		{"Postgres Pause", c.scenarioPostgresPause},
		{"NATS Restart", c.scenarioNatsRestart},
		{"Redis Failure", c.scenarioRedisFailure},
		{"Server Crash", c.scenarioServerCrash},
		{"PgBouncer Saturation", c.scenarioPgBouncerSaturation},
		{"Outbox Relay Interruption", c.scenarioOutboxRelayInterruption},
		{"Worker Node Restart", c.scenarioWorkerNodeRestart},
		{"Memory Pressure", c.scenarioMemoryPressure},
		{"Slow Consumer (NATS Backpressure)", c.scenarioSlowConsumer},
		{"Concurrent Settlement Trigger", c.scenarioConcurrentSettlement},
		{"Transfer DB Failover", c.scenarioTransferDBFailover},
		{"Cascading Failure (NATS + Redis)", c.scenarioCascadingFailure},
		{"Rapid Restart Cycle", c.scenarioRapidRestartCycle},
		{"Gateway-Server Network Partition", c.scenarioGatewayServerPartition},
		{"Hot Tenant Flood", c.scenarioHotTenantFlood},
	}

	fmt.Println()
	fmt.Println("╔══════════════════════════════════════════════════════════════╗")
	fmt.Println("║              S E T T L A   C H A O S   T E S T S           ║")
	fmt.Println("║        Proving recovery from infrastructure failures        ║")
	fmt.Println("╚══════════════════════════════════════════════════════════════╝")
	fmt.Println()

	for i, scenario := range scenarios {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		fmt.Printf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		fmt.Printf("  Scenario %d/%d: %s\n", i+1, len(scenarios), scenario.name)
		fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n\n")

		// Ensure clean state before each scenario
		c.logger.Info("ensuring clean state before scenario", "scenario", scenario.name)
		if err := c.ensureHealthy(ctx); err != nil {
			c.logger.Error("infrastructure not healthy, attempting recovery", "error", err)
			c.dockerCompose("up", "-d")
			time.Sleep(30 * time.Second)
			if err := c.ensureHealthy(ctx); err != nil {
				result := ScenarioResult{
					Name:       scenario.name,
					Passed:     false,
					FailReason: fmt.Sprintf("infrastructure not healthy: %v", err),
				}
				c.results = append(c.results, result)
				continue
			}
		}

		// Run scenario with timeout
		scenarioCtx, cancel := context.WithTimeout(ctx, c.config.MaxScenarioTime)
		result := scenario.fn(scenarioCtx)
		cancel()

		c.results = append(c.results, result)

		if result.Passed {
			fmt.Printf("\n  PASS  %s  (duration: %v, recovery: %v)\n\n",
				scenario.name, result.Duration, result.RecoveryTime)
		} else {
			fmt.Printf("\n  FAIL  %s  (%s)\n\n",
				scenario.name, result.FailReason)
		}
	}

	// Print summary
	c.printSummary()

	// Check for any failures
	for _, r := range c.results {
		if !r.Passed {
			return fmt.Errorf("chaos test failed: %s", r.FailReason)
		}
	}

	return nil
}

// printSummary prints the final chaos test report.
func (c *ChaosRunner) printSummary() {
	fmt.Println()
	fmt.Println("══════════════════════════════════════════════════════════════")
	fmt.Println("  CHAOS TEST SUMMARY")
	fmt.Println("══════════════════════════════════════════════════════════════")
	fmt.Println()

	passed := 0
	failed := 0
	for _, r := range c.results {
		status := "PASS"
		if !r.Passed {
			status = "FAIL"
			failed++
		} else {
			passed++
		}
		fmt.Printf("  %s  %-30s  duration=%-8v  recovery=%-8v  affected=%.1f%%\n",
			status, r.Name, r.Duration.Round(time.Second), r.RecoveryTime.Round(time.Second), r.AffectedPercent)
		if !r.Passed {
			fmt.Printf("       reason: %s\n", r.FailReason)
		}
		fmt.Printf("       ledger_balanced=%v  treasury_consistent=%v\n", r.LedgerBalanced, r.TreasuryConsistent)
	}

	fmt.Printf("\n  %d passed, %d failed out of %d scenarios\n\n", passed, failed, len(c.results))

	if failed == 0 {
		fmt.Println("  ALL SCENARIOS PASSED — system recovers gracefully from failures")
	} else {
		fmt.Println("  SOME SCENARIOS FAILED — review failure reasons above")
	}
	fmt.Println()
}


// dockerCompose runs a docker compose command.
func (c *ChaosRunner) dockerCompose(args ...string) error {
	cmdArgs := []string{"compose", "-f", c.config.ComposeFile, "--env-file", c.config.EnvFile}
	cmdArgs = append(cmdArgs, args...)

	cmd := exec.Command("docker", cmdArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	c.logger.Info("docker compose", "args", strings.Join(args, " "))
	return cmd.Run()
}

// dockerComposeQuiet runs a docker compose command without output.
func (c *ChaosRunner) dockerComposeQuiet(args ...string) error {
	cmdArgs := []string{"compose", "-f", c.config.ComposeFile, "--env-file", c.config.EnvFile}
	cmdArgs = append(cmdArgs, args...)

	cmd := exec.Command("docker", cmdArgs...)
	return cmd.Run()
}

// dockerExec runs a command inside a container.
func (c *ChaosRunner) dockerExec(service string, args ...string) (string, error) {
	cmdArgs := []string{"compose", "-f", c.config.ComposeFile, "--env-file", c.config.EnvFile,
		"exec", "-T", service}
	cmdArgs = append(cmdArgs, args...)

	cmd := exec.Command("docker", cmdArgs...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// ensureHealthy checks that all services are healthy.
func (c *ChaosRunner) ensureHealthy(ctx context.Context) error {
	// Check gateway
	if err := c.waitForHealthy(ctx, c.config.GatewayURL+"/health", 30*time.Second); err != nil {
		return fmt.Errorf("gateway not healthy: %w", err)
	}
	// Check server
	if err := c.waitForHealthy(ctx, c.config.ServerURL+"/health", 30*time.Second); err != nil {
		return fmt.Errorf("server not healthy: %w", err)
	}
	return nil
}

// waitForHealthy polls a health endpoint until it returns 200 or times out.
func (c *ChaosRunner) waitForHealthy(ctx context.Context, url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		resp, err := c.httpClient.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("timeout waiting for %s", url)
}


// ChaosLoadGenerator generates background load during chaos scenarios.
type ChaosLoadGenerator struct {
	config     ChaosConfig
	logger     *slog.Logger
	httpClient *http.Client

	mu         sync.Mutex
	created    atomic.Int64
	completed  atomic.Int64
	failed     atomic.Int64
	errors     atomic.Int64
	preErrors  int64 // errors before injection
	preCreated int64 // created before injection
}

// NewChaosLoadGenerator creates a background load generator.
func NewChaosLoadGenerator(config ChaosConfig, logger *slog.Logger) *ChaosLoadGenerator {
	return &ChaosLoadGenerator{
		config:     config,
		logger:     logger,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// Start begins generating load. Returns a stop function.
func (g *ChaosLoadGenerator) Start(ctx context.Context) context.CancelFunc {
	ctx, cancel := context.WithCancel(ctx)

	// Simple rate-limited worker pool
	workers := g.config.LoadTPS / 10
	if workers < 1 {
		workers = 1
	}

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ticker := time.NewTicker(time.Duration(float64(time.Second) / float64(g.config.LoadTPS) * float64(workers)))
			defer ticker.Stop()

			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					g.executeTransfer(ctx)
				}
			}
		}()
	}

	return func() {
		cancel()
		wg.Wait()
	}
}

// MarkInjectionPoint records counters at the point of failure injection.
func (g *ChaosLoadGenerator) MarkInjectionPoint() {
	g.preErrors = g.errors.Load()
	g.preCreated = g.created.Load()
}

// AffectedRequests returns the number of requests that failed after injection.
func (g *ChaosLoadGenerator) AffectedRequests() int64 {
	return g.errors.Load() - g.preErrors
}

// TotalRequestsAfterInjection returns total requests after injection.
func (g *ChaosLoadGenerator) TotalRequestsAfterInjection() int64 {
	return g.created.Load() - g.preCreated + g.AffectedRequests()
}

func (g *ChaosLoadGenerator) executeTransfer(ctx context.Context) {
	// Create a simple transfer
	body := map[string]interface{}{
		"idempotency_key": uuid.Must(uuid.NewV7()).String(),
		"source_currency": "GBP",
		"source_amount":   fmt.Sprintf("%d", 100+rand.Intn(900)),
		"dest_currency":   "NGN",
		"sender": map[string]string{
			"name":    "Chaos Test Sender",
			"email":   "chaos@test.com",
			"country": "GB",
		},
		"recipient": map[string]string{
			"name":    "Chaos Test Recipient",
			"country": "NG",
		},
	}

	jsonBody, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", g.config.GatewayURL+"/v1/transfers", bytes.NewReader(jsonBody))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk_live_lemfi_demo_key")

	resp, err := g.httpClient.Do(req)
	if err != nil {
		g.errors.Add(1)
		return
	}
	defer resp.Body.Close()
	io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		g.errors.Add(1)
		g.failed.Add(1)
		return
	}

	g.created.Add(1)
}


func main() {
	var (
		composeFile = flag.String("compose", "deploy/docker-compose.yml", "Docker compose file")
		envFile     = flag.String("env", ".env", "Environment file")
		gatewayURL  = flag.String("gateway", "http://localhost:3100", "Gateway URL")
		serverURL   = flag.String("server", "http://localhost:8080", "Server URL")
		tps         = flag.Int("tps", 500, "Background TPS during chaos")
	)
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	config := DefaultChaosConfig()
	config.ComposeFile = *composeFile
	config.EnvFile = *envFile
	config.GatewayURL = *gatewayURL
	config.ServerURL = *serverURL
	config.LoadTPS = *tps

	runner := NewChaosRunner(config, logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		logger.Info("received shutdown signal")
		cancel()
	}()

	if err := runner.Run(ctx); err != nil {
		logger.Error("chaos tests failed", "error", err)
		os.Exit(1)
	}

	logger.Info("chaos tests completed successfully")
}

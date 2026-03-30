# Chapter 4.5: Provider Adapters

**Reading time:** ~35 minutes
**Prerequisites:** Chapter 4.4 (Smart Routing)
**Code references:** `domain/provider.go`, `rail/provider/registry.go`, `rail/provider/factory/`, `rail/provider/all/all.go`, `rail/provider/mock/`, `rail/provider/settla/`, `rail/router/router.go`

---

## Learning Objectives

By the end of this chapter, you will be able to:

1. Describe the `OnRampProvider` and `OffRampProvider` interfaces
2. Explain the `ProviderRegistry` and how providers are registered
3. Explain the factory system: self-registration, bootstrap, and per-provider config
4. Describe the webhook normalizer contract and why it is mandatory
5. Walk through the mock provider implementations
6. Describe how `CoreRouterAdapter` applies per-tenant fee schedules
7. Add a new provider to the system end-to-end using the factory pattern

---

## The Provider Interfaces

Settla defines two core provider interfaces in `domain/provider.go`. These
are the contracts that every fiat on-ramp and off-ramp must satisfy:

### OnRampProvider (fiat to stablecoin)

```go
type OnRampProvider interface {
    // ID returns the unique provider identifier.
    ID() string
    // SupportedPairs returns the currency pairs this provider can handle.
    SupportedPairs() []CurrencyPair
    // GetQuote returns a quote for the given request.
    GetQuote(ctx context.Context, req QuoteRequest) (*ProviderQuote, error)
    // Execute initiates an on-ramp transaction.
    Execute(ctx context.Context, req OnRampRequest) (*ProviderTx, error)
    // GetStatus returns the current status of a transaction.
    GetStatus(ctx context.Context, txID string) (*ProviderTx, error)
}
```

### OffRampProvider (stablecoin to fiat)

```go
type OffRampProvider interface {
    // ID returns the unique provider identifier.
    ID() string
    // SupportedPairs returns the currency pairs this provider can handle.
    SupportedPairs() []CurrencyPair
    // GetQuote returns a quote for the given request.
    GetQuote(ctx context.Context, req QuoteRequest) (*ProviderQuote, error)
    // Execute initiates an off-ramp transaction.
    Execute(ctx context.Context, req OffRampRequest) (*ProviderTx, error)
    // GetStatus returns the current status of a transaction.
    GetStatus(ctx context.Context, txID string) (*ProviderTx, error)
}
```

### BlockchainClient (on-chain settlement)

```go
type BlockchainClient interface {
    Chain() string
    GetBalance(ctx context.Context, address string, token string) (decimal.Decimal, error)
    EstimateGas(ctx context.Context, req TxRequest) (decimal.Decimal, error)
    SendTransaction(ctx context.Context, req TxRequest) (*ChainTx, error)
    GetTransaction(ctx context.Context, hash string) (*ChainTx, error)
    SubscribeTransactions(ctx context.Context, address string, ch chan<- ChainTx) error
}
```

### Interface Symmetry

Notice the deliberate symmetry between `OnRampProvider` and `OffRampProvider`.
Both have:

```
ID()              -- identification
SupportedPairs()  -- capability declaration
GetQuote()        -- pricing (read-only, no side effects)
Execute()         -- action (creates a transaction)
GetStatus()       -- polling (idempotent status check)
```

The only difference is the `Execute` method signature:

```
OnRampProvider.Execute(OnRampRequest)  -- no recipient (buying stablecoin)
OffRampProvider.Execute(OffRampRequest) -- has Recipient (sending fiat)
```

This symmetry makes the router's candidate-building loop clean -- it calls
`GetQuote` on both with the same pattern.

---

## Supporting Types

### QuoteRequest and ProviderQuote

```go
type QuoteRequest struct {
    SourceCurrency Currency
    SourceAmount   decimal.Decimal
    DestCurrency   Currency
    DestCountry    string
}

type ProviderQuote struct {
    ProviderID       string
    Rate             decimal.Decimal   // FX rate multiplier
    Fee              decimal.Decimal   // Fixed fee
    EstimatedSeconds int               // Processing time estimate
}
```

### OnRampRequest and OffRampRequest

```go
type OnRampRequest struct {
    Amount       decimal.Decimal
    FromCurrency Currency
    ToCurrency   Currency
    Reference    string
    QuotedRate   decimal.Decimal  // Reject if live rate diverges > 2%
}

type OffRampRequest struct {
    Amount       decimal.Decimal
    FromCurrency Currency
    ToCurrency   Currency
    Recipient    Recipient
    Reference    string
    QuotedRate   decimal.Decimal
}
```

The `QuotedRate` field enables slippage protection. When a provider receives
an `Execute` call, it compares `QuotedRate` with the current live rate. If
the rate has moved more than the configured tolerance (default 2%), the
provider rejects the execution. This prevents executing transfers at
significantly worse rates than quoted.

### ProviderTx

```go
type ProviderTx struct {
    ID         string
    ExternalID string              // Provider's internal ID
    Status     string              // "PENDING", "COMPLETED", "FAILED"
    Amount     decimal.Decimal
    Currency   Currency
    TxHash     string              // Blockchain tx hash (if applicable)
    Metadata   map[string]string   // Provider-specific data
}
```

---

## The ProviderRegistry

The registry manages all available providers in memory:

```go
type Registry struct {
    mu          sync.RWMutex
    onRamps     map[string]domain.OnRampProvider
    offRamps    map[string]domain.OffRampProvider
    blockchains map[string]domain.BlockchainClient
}

var _ domain.ProviderRegistry = (*Registry)(nil)
```

It implements the `domain.ProviderRegistry` interface:

```go
type ProviderRegistry interface {
    ListOnRampIDs(ctx context.Context) []string
    ListOffRampIDs(ctx context.Context) []string
    GetOnRamp(id string) (OnRampProvider, error)
    GetOffRamp(id string) (OffRampProvider, error)
    GetBlockchain(chain string) (BlockchainClient, error)
    ListBlockchainChains() []string
}
```

### Registration

Providers are registered at startup:

```go
func NewRegistry() *Registry {
    return &Registry{
        onRamps:     make(map[string]domain.OnRampProvider),
        offRamps:    make(map[string]domain.OffRampProvider),
        blockchains: make(map[string]domain.BlockchainClient),
    }
}

func (r *Registry) RegisterOnRamp(p domain.OnRampProvider) {
    r.mu.Lock()
    defer r.mu.Unlock()
    r.onRamps[p.ID()] = p
}

func (r *Registry) RegisterOffRamp(p domain.OffRampProvider) {
    r.mu.Lock()
    defer r.mu.Unlock()
    r.offRamps[p.ID()] = p
}

func (r *Registry) RegisterBlockchainClient(c domain.BlockchainClient) {
    r.mu.Lock()
    defer r.mu.Unlock()
    r.blockchains[c.Chain()] = c
}
```

### Provider Modes

The registry supports four modes for different environments:

```go
type ProviderMode string

const (
    ProviderModeMock     ProviderMode = "mock"      // Unit tests, CI
    ProviderModeMockHTTP ProviderMode = "mock-http"  // Demos
    ProviderModeTestnet  ProviderMode = "testnet"    // Dev, staging
    ProviderModeLive     ProviderMode = "live"       // Production (future)
)
```

The `NewRegistryFromMode` factory selects providers based on the environment.
This is the legacy path -- the modern approach uses the factory system
described in the next section:

```go
func NewRegistryFromMode(mode ProviderMode, deps *SettlaProviderDeps,
    logger *slog.Logger) *Registry {

    reg := NewRegistry()
    switch mode {
    case ProviderModeTestnet:
        if deps == nil { return reg }
        if deps.OnRamp != nil {
            reg.RegisterOnRamp(deps.OnRamp)
        }
        if deps.OffRamp != nil {
            reg.RegisterOffRamp(deps.OffRamp)
        }
        for _, c := range deps.Chains {
            reg.RegisterBlockchainClient(c)
        }
    case ProviderModeLive:
        logger.Warn("settla-rail: live provider mode not yet implemented")
    default:
        // Mock mode -- caller registers providers after construction
    }
    return reg
}
```

The mode is resolved from the environment:

```go
func ModeFromEnv() ProviderMode {
    mode := os.Getenv("SETTLA_PROVIDER_MODE")
    switch ProviderMode(mode) {
    case ProviderModeMock:
        return ProviderModeMock
    case ProviderModeMockHTTP:
        return ProviderModeMockHTTP
    case ProviderModeTestnet:
        return ProviderModeTestnet
    case ProviderModeLive:
        return ProviderModeLive
    default:
        if os.Getenv("SETTLA_ENV") == "test" {
            return ProviderModeMock
        }
        return ProviderModeTestnet
    }
}
```

---

## The Provider Factory System

The registry described above works, but it has a scaling problem. Every
time you add a new provider you must manually construct it in
`cmd/settla-server/main.go`, wire its dependencies, and register it in the
correct order. With two providers this is manageable. With ten, it becomes
a maintenance burden and a source of wiring bugs.

The factory system in `rail/provider/factory/` solves this with Go's
standard `init()` + blank import pattern -- the same approach used by
`database/sql`, `image/png`, and the Go standard library. Each provider
package registers its own factory functions at import time. At startup,
`Bootstrap()` discovers all registered factories, loads per-provider config
from environment variables, and constructs everything automatically.

### Factory Types

The factory package defines typed constructor functions for each provider
category:

```go
// OnRampFactory creates an OnRampProvider from shared dependencies and config.
type OnRampFactory func(deps Deps, cfg ProviderConfig) (domain.OnRampProvider, error)

// OffRampFactory creates an OffRampProvider from shared dependencies and config.
type OffRampFactory func(deps Deps, cfg ProviderConfig) (domain.OffRampProvider, error)

// BlockchainFactory creates a BlockchainClient from shared dependencies and config.
type BlockchainFactory func(deps Deps, cfg ProviderConfig) (domain.BlockchainClient, error)

// NormalizerFactory creates a WebhookNormalizer for a provider.
// Every on-ramp and off-ramp provider MUST register a normalizer -- without it,
// Settla cannot process the provider's inbound webhooks.
type NormalizerFactory func(deps Deps, cfg ProviderConfig) (domain.WebhookNormalizer, error)

// ListenerFactory creates an optional ProviderListener for providers that
// communicate via non-HTTP channels (WebSocket, gRPC streaming, polling).
type ListenerFactory func(deps Deps, cfg ProviderConfig) (domain.ProviderListener, error)
```

Each factory receives shared `Deps` (logger, blockchain registry, wallet
manager) and per-provider `ProviderConfig` loaded from environment
variables.

### Shared Dependencies

The `Deps` struct provides everything a factory might need without
importing concrete packages:

```go
// ChainRegistry is a minimal interface for blockchain client lookup.
// Satisfied by *blockchain.Registry without importing it -- breaks the
// factory -> blockchain -> {tron,ethereum,solana} -> factory cycle.
type ChainRegistry interface {
    GetClient(chain domain.CryptoChain) (domain.BlockchainClient, error)
}

// Deps holds shared dependencies that provider factories may use.
type Deps struct {
    Logger        *slog.Logger
    BlockchainReg ChainRegistry // nil in mock mode; *blockchain.Registry in testnet/live
    WalletManager any           // nil when read-only / not configured
}
```

Notice the `ChainRegistry` interface. This breaks what would otherwise be
a circular dependency: if the factory imported `blockchain.Registry`
directly, and blockchain packages used the factory to register themselves,
you would have a cycle. The interface decouples them.

### Per-Provider Configuration

Every provider's operational config is loaded from environment variables
using a consistent naming convention:

```go
type ProviderConfig struct {
    Enabled bool                  // SETTLA_PROVIDER_{ID}_ENABLED
    CBFailures int               // circuit breaker failure threshold (default: 15)
    CBResetMs  int               // circuit breaker reset interval (default: 10000)
    CBHalfOpen int               // max concurrent half-open requests (default: 2)
    RateLimitPerSec int          // requests per second (default: 100)
    RateLimitBurst  int          // burst capacity (default: 200)
    Extra map[string]string      // provider-specific key-value pairs
}
```

The naming convention is `SETTLA_PROVIDER_{UPPER_ID}_{KEY}`. For example,
a provider called `flutterwave` would read:

```bash
SETTLA_PROVIDER_FLUTTERWAVE_ENABLED=true
SETTLA_PROVIDER_FLUTTERWAVE_CB_FAILURES=10
SETTLA_PROVIDER_FLUTTERWAVE_CB_RESET_MS=5000
SETTLA_PROVIDER_FLUTTERWAVE_RATE_LIMIT=50
SETTLA_PROVIDER_FLUTTERWAVE_API_KEY=sk_live_xxx
```

Known keys (`ENABLED`, `CB_FAILURES`, `CB_RESET_MS`, `CB_HALF_OPEN`,
`RATE_LIMIT`, `RATE_BURST`) are parsed into typed fields. Any other
key matching the prefix is stored in `Extra` -- this is where provider-
specific secrets like API keys end up. The `LoadProviderConfig` function
handles all of this:

```go
func LoadProviderConfig(providerID string) ProviderConfig {
    cfg := DefaultProviderConfig()
    prefix := "SETTLA_PROVIDER_" + envKey(providerID) + "_"

    for _, env := range os.Environ() {
        if !strings.HasPrefix(env, prefix) {
            continue
        }
        kv := strings.SplitN(env, "=", 2)
        key := strings.TrimPrefix(kv[0], prefix)
        val := kv[1]

        switch key {
        case "ENABLED":
            cfg.Enabled = val == "true" || val == "1" || val == "yes"
        case "CB_FAILURES":
            if n, err := strconv.Atoi(val); err == nil && n > 0 {
                cfg.CBFailures = n
            }
        // ... other known keys ...
        default:
            cfg.Extra[key] = val  // provider-specific extras
        }
    }
    return cfg
}
```

### Self-Registration with init()

Each provider package has a `register.go` file that calls factory
registration functions from `init()`. This means merely importing the
package causes its providers to become available -- no manual wiring
required.

Here is the mock provider's `register.go`:

```go
// rail/provider/mock/register.go
package mock

func init() {
    mock := []factory.ProviderMode{factory.ModeMock}

    factory.RegisterOnRampFactory("mock-onramp-gbp", mock, newMockOnRampGBP)
    factory.RegisterOnRampFactory("mock-onramp-ngn", mock, newMockOnRampNGN)
    factory.RegisterOffRampFactory("mock-offramp-ngn", mock, newMockOffRampNGN)
    factory.RegisterOffRampFactory("mock-offramp-gbp", mock, newMockOffRampGBP)
    factory.RegisterBlockchainFactory("mock-tron", mock, newMockTron)

    // All mock providers share the same webhook normalizer.
    factory.RegisterNormalizerFactory("mock-onramp-gbp", mock, newMockNormalizer)
    factory.RegisterNormalizerFactory("mock-onramp-ngn", mock, newMockNormalizer)
    factory.RegisterNormalizerFactory("mock-offramp-ngn", mock, newMockNormalizer)
    factory.RegisterNormalizerFactory("mock-offramp-gbp", mock, newMockNormalizer)
}
```

And the Settla testnet provider's `register.go`:

```go
// rail/provider/settla/register.go
package settla

func init() {
    testnet := []factory.ProviderMode{factory.ModeTestnet}

    factory.RegisterOnRampFactory("settla-onramp", testnet, newSettlaOnRamp)
    factory.RegisterOffRampFactory("settla-offramp", testnet, newSettlaOffRamp)
    factory.RegisterNormalizerFactory("settla-onramp", testnet, newSettlaNormalizer)
    factory.RegisterNormalizerFactory("settla-offramp", testnet, newSettlaNormalizer)
}
```

Notice the pattern:

1. Each `init()` declares which modes its factories apply to (`ModeMock`,
   `ModeTestnet`, etc.)
2. Each on-ramp and off-ramp factory has a matching normalizer factory --
   this is enforced at bootstrap time
3. Blockchain factories do not need normalizers (blockchains do not send
   webhooks)
4. The factory functions (`newMockOnRampGBP`, `newSettlaOnRamp`) are private
   to the package -- only the `init()` call exposes them to the factory
   system

### The All-Providers Aggregation

How does the binary know to import these packages? Through a single
aggregation file that uses blank imports:

```go
// rail/provider/all/all.go
package all

import (
    // On-ramp and off-ramp providers.
    _ "github.com/intellect4all/settla/rail/provider/mock"
    _ "github.com/intellect4all/settla/rail/provider/mockhttp"
    _ "github.com/intellect4all/settla/rail/provider/settla"
)
```

This is the ONLY file that needs editing when adding a new provider. The
blank import (`_`) triggers the package's `init()` function, which
registers its factories into the global factory registry. The main binary
imports `all`:

```go
import _ "github.com/intellect4all/settla/rail/provider/all"
```

And every registered provider is automatically available at bootstrap time.

### Bootstrap

The `Bootstrap` function ties everything together. It takes a mode and
shared deps, iterates over all registered factories matching that mode,
loads per-provider config from environment variables, constructs each
provider, and validates that every on-ramp and off-ramp has a matching
normalizer:

```go
func Bootstrap(mode ProviderMode, deps Deps) (*BootstrapResult, error) {
    logger := deps.Logger.With("module", "factory.bootstrap")

    result := &BootstrapResult{
        OnRamps:     make(map[string]domain.OnRampProvider),
        OffRamps:    make(map[string]domain.OffRampProvider),
        Normalizers: make(map[string]domain.WebhookNormalizer),
        Listeners:   make(map[string]domain.ProviderListener),
        Configs:     make(map[string]ProviderConfig),
    }

    // Build normalizers first -- we need them to validate provider registrations.
    normFactories := NormalizerFactories(mode)
    for name, f := range normFactories {
        cfg := LoadProviderConfig(name)
        if !cfg.Enabled {
            continue
        }
        n, err := f(deps, cfg)
        if err != nil {
            return nil, fmt.Errorf("settla-factory: normalizer %q: %w", name, err)
        }
        result.Normalizers[name] = n
    }

    // On-ramps
    for name, f := range OnRampFactories(mode) {
        cfg := LoadProviderConfig(name)
        if !cfg.Enabled {
            logger.Info("settla-factory: provider disabled, skipping",
                "provider", name, "type", "onramp")
            continue
        }
        // Enforce: every on-ramp provider must have a normalizer.
        if _, ok := result.Normalizers[name]; !ok {
            return nil, fmt.Errorf("settla-factory: on-ramp %q has no registered "+
                "WebhookNormalizer", name)
        }
        p, err := f(deps, cfg)
        if err != nil {
            return nil, fmt.Errorf("settla-factory: on-ramp %q: %w", name, err)
        }
        result.OnRamps[p.ID()] = p
        result.Configs[p.ID()] = cfg
    }

    // Off-ramps (same normalizer enforcement)
    // Blockchains (no normalizer required)
    // Listeners (optional)

    logger.Info("settla-factory: bootstrap complete",
        "mode", string(mode),
        "on_ramps", len(result.OnRamps),
        "off_ramps", len(result.OffRamps),
        "blockchains", len(result.Blockchains),
        "normalizers", len(result.Normalizers),
    )
    return result, nil
}
```

The `BootstrapResult` collects everything the caller needs to populate a
`provider.Registry`:

```go
type BootstrapResult struct {
    OnRamps     map[string]domain.OnRampProvider
    OffRamps    map[string]domain.OffRampProvider
    Blockchains []domain.BlockchainClient
    Normalizers map[string]domain.WebhookNormalizer
    Listeners   map[string]domain.ProviderListener
    Configs     map[string]ProviderConfig
}
```

The bootstrap order matters:

1. **Normalizers first** -- so we can validate that every on-ramp and
   off-ramp has one
2. **On-ramps** -- each checked against the normalizer map
3. **Off-ramps** -- same normalizer check
4. **Blockchains** -- no normalizer required (blockchains don't send
   webhooks)
5. **Listeners** -- optional, for non-HTTP provider communication

If any enabled provider lacks a normalizer, `Bootstrap` returns a hard
error. This is deliberate. Without a normalizer, the `InboundWebhookWorker`
cannot process that provider's async callbacks, which means transfers
through that provider would hang forever in `PENDING_PROVIDER` state.
Failing loudly at startup is far better than discovering this in
production.

### Webhook Normalizers

Every on-ramp and off-ramp provider must have a matching webhook
normalizer. This is a recent addition to the provider contract, driven
by the `InboundWebhookWorker`'s need for standardized payloads.

The problem: each payment provider sends webhooks in a different format.
Flutterwave sends nested JSON with `data.status`. Paystack sends
`event` and `data.gateway_response`. Each has different status strings,
different field names, different error formats. The webhook receiver
(`api/webhook/`) cannot know all these formats -- and it should not have
to.

The solution: `domain.WebhookNormalizer`:

```go
type WebhookNormalizer interface {
    NormalizeWebhook(providerSlug string, rawBody []byte) (*ProviderWebhookPayload, error)
}
```

Each provider implements this interface to translate its raw webhook
body into a standard `ProviderWebhookPayload`:

```go
type ProviderWebhookPayload struct {
    TransferID  uuid.UUID
    TenantID    uuid.UUID
    ProviderID  string
    ProviderRef string
    Status      string   // "completed" or "failed"
    TxHash      string
    Error       string
    ErrorCode   string
    TxType      string   // "onramp" or "offramp"
}
```

Here is the mock normalizer as an example:

```go
// rail/provider/mock/normalizer.go
type Normalizer struct{}

type mockWebhookBody struct {
    TransferID string `json:"transfer_id"`
    TenantID   string `json:"tenant_id"`
    Reference  string `json:"reference"`
    Status     string `json:"status"`
    TxHash     string `json:"tx_hash,omitempty"`
    TxType     string `json:"tx_type,omitempty"`
    Error      string `json:"error,omitempty"`
    ErrorCode  string `json:"error_code,omitempty"`
}

func (n *Normalizer) NormalizeWebhook(providerSlug string,
    rawBody []byte) (*domain.ProviderWebhookPayload, error) {

    var body mockWebhookBody
    if err := json.Unmarshal(rawBody, &body); err != nil {
        return nil, fmt.Errorf("mock normalizer: invalid JSON: %w", err)
    }

    if body.TransferID == "" || body.TenantID == "" || body.Status == "" {
        return nil, fmt.Errorf("mock normalizer: missing required fields")
    }

    transferID, err := uuid.Parse(body.TransferID)
    if err != nil {
        return nil, fmt.Errorf("mock normalizer: invalid transfer_id: %w", err)
    }
    tenantID, err := uuid.Parse(body.TenantID)
    if err != nil {
        return nil, fmt.Errorf("mock normalizer: invalid tenant_id: %w", err)
    }

    status := normalizeStatus(body.Status)
    if status == "" {
        return nil, nil // non-terminal status, skip
    }

    return &domain.ProviderWebhookPayload{
        TransferID:  transferID,
        TenantID:    tenantID,
        ProviderID:  providerSlug,
        ProviderRef: body.Reference,
        Status:      status,
        TxHash:      body.TxHash,
        Error:       body.Error,
        ErrorCode:   body.ErrorCode,
        TxType:      body.TxType,
    }, nil
}

func normalizeStatus(s string) string {
    switch strings.ToLower(s) {
    case "completed", "success", "successful", "confirmed":
        return "completed"
    case "failed", "failure", "error", "rejected", "declined":
        return "failed"
    default:
        return ""
    }
}
```

Key patterns in normalizers:

- **Status mapping.** Different providers use different words for the same
  thing. Flutterwave says "successful", Paystack says "success", a bank
  might say "confirmed". The normalizer maps all of these to two terminal
  states: `"completed"` and `"failed"`.
- **Non-terminal skip.** If the status is neither terminal (e.g.,
  "processing", "pending"), the normalizer returns `nil, nil`. The webhook
  receiver skips the event rather than erroring.
- **Validation.** The normalizer validates required fields and UUID formats.
  Malformed webhooks are rejected with clear error messages rather than
  propagated with corrupt data.

The webhook processing flow with normalizers:

```
Provider HTTP callback
    |
    v
api/webhook/ (Fastify)
    |  receives raw body, identifies provider from URL path
    |
    v
registry.GetNormalizer(providerSlug)
    |  returns the provider's WebhookNormalizer
    |
    v
normalizer.NormalizeWebhook(slug, rawBody)
    |  provider-specific JSON -> standard ProviderWebhookPayload
    |
    v
Publish to SETTLA_PROVIDER_WEBHOOKS stream
    |
    v
InboundWebhookWorker
    |  processes standardized payload, calls Engine.Handle*Result
```

---

## Mock Provider Implementations

The mock providers in `rail/provider/mock/` are used for testing and
development. They demonstrate the minimum viable implementation of each
interface.

### Mock OnRampProvider

```go
type OnRampProvider struct {
    id    string
    pairs []domain.CurrencyPair
    rate  decimal.Decimal  // FX rate multiplier
    fee   decimal.Decimal  // Fixed fee per transaction
    delay time.Duration    // Simulated processing delay
}

func NewOnRampProvider(id string, pairs []domain.CurrencyPair,
    rate, fee decimal.Decimal, delay time.Duration) *OnRampProvider {
    return &OnRampProvider{id: id, pairs: pairs, rate: rate, fee: fee, delay: delay}
}

func (p *OnRampProvider) ID() string { return p.id }
func (p *OnRampProvider) SupportedPairs() []domain.CurrencyPair { return p.pairs }

func (p *OnRampProvider) GetQuote(_ context.Context,
    req domain.QuoteRequest) (*domain.ProviderQuote, error) {
    if !p.supportsPair(req.SourceCurrency, req.DestCurrency) {
        return nil, fmt.Errorf("settla-rail: mock on-ramp %s: "+
            "unsupported pair %s->%s", p.id, req.SourceCurrency, req.DestCurrency)
    }
    return &domain.ProviderQuote{
        ProviderID:       p.id,
        Rate:             p.rate,
        Fee:              p.fee,
        EstimatedSeconds: int(p.delay.Seconds()) + 60,
    }, nil
}

func (p *OnRampProvider) Execute(ctx context.Context,
    req domain.OnRampRequest) (*domain.ProviderTx, error) {
    if !p.supportsPair(req.FromCurrency, req.ToCurrency) {
        return nil, fmt.Errorf("unsupported pair")
    }
    if p.delay > 0 {
        select {
        case <-time.After(p.delay):
        case <-ctx.Done():
            return nil, ctx.Err()
        }
    }
    return &domain.ProviderTx{
        ID:         uuid.New().String(),
        ExternalID: fmt.Sprintf("mock-onramp-%s", uuid.New().String()[:8]),
        Status:     "COMPLETED",
        Amount:     req.Amount.Mul(p.rate).Sub(p.fee),
        Currency:   req.ToCurrency,
        Metadata:   map[string]string{"provider": p.id, "reference": req.Reference},
    }, nil
}
```

Key patterns:
- `supportsPair` validates the currency pair before quoting or executing
- `GetQuote` returns an error for unsupported pairs (the router's `continue`
  catches this)
- `Execute` respects context cancellation during the simulated delay
- The output amount is `amount * rate - fee`

### Mock OffRampProvider

Identical structure to OnRampProvider, but `Execute` takes `OffRampRequest`
(with `Recipient`):

```go
func (p *OffRampProvider) Execute(ctx context.Context,
    req domain.OffRampRequest) (*domain.ProviderTx, error) {
    // Same pattern: validate pair, simulate delay, return transaction
    return &domain.ProviderTx{
        ID:         uuid.New().String(),
        ExternalID: fmt.Sprintf("mock-offramp-%s", uuid.New().String()[:8]),
        Status:     "COMPLETED",
        Amount:     req.Amount.Mul(p.rate).Sub(p.fee),
        Currency:   req.ToCurrency,
        Metadata:   map[string]string{"provider": p.id, "reference": req.Reference},
    }, nil
}
```

### Mock BlockchainClient

```go
type BlockchainClient struct {
    chain    string
    gasFee   decimal.Decimal
    mu       sync.Mutex
    balances map[string]decimal.Decimal  // address:token -> balance
    txCount  int
}

func (c *BlockchainClient) Chain() string { return c.chain }

func (c *BlockchainClient) EstimateGas(_ context.Context,
    _ domain.TxRequest) (decimal.Decimal, error) {
    return c.gasFee, nil  // Fixed gas fee for testing
}

func (c *BlockchainClient) SendTransaction(_ context.Context,
    req domain.TxRequest) (*domain.ChainTx, error) {
    c.mu.Lock()
    c.txCount++
    txNum := c.txCount
    c.mu.Unlock()
    return &domain.ChainTx{
        Hash:          fmt.Sprintf("mock-tx-%s-%d", c.chain, txNum),
        Status:        "CONFIRMED",
        Confirmations: 20,
        BlockNumber:   uint64(1000 + txNum),
        Fee:           c.gasFee,
    }, nil
}
```

The mock blockchain client uses a mutex-protected counter for transaction
IDs and returns immediately with "CONFIRMED" status.

---

## Per-Tenant Fee Wrapping: CoreRouterAdapter

The router calculates raw provider fees. The `CoreRouterAdapter` wraps the
router to add per-tenant fee schedules:

```go
type CoreRouterAdapter struct {
    router  *Router
    tenants TenantStore
    logger  *slog.Logger
}

func (a *CoreRouterAdapter) GetQuote(ctx context.Context,
    tenantID uuid.UUID, req domain.QuoteRequest) (*domain.Quote, error) {

    // 1. Load tenant for fee schedule
    tenant, err := a.tenants.GetTenant(ctx, tenantID)
    if err != nil {
        return nil, fmt.Errorf("settla-rail: loading tenant: %w", err)
    }

    // 2. Route (calls router.Route internally)
    result, err := a.router.Route(ctx, domain.RouteRequest{
        TenantID:       tenantID,
        SourceCurrency: req.SourceCurrency,
        TargetCurrency: req.DestCurrency,
        Amount:         req.SourceAmount,
    })
    if err != nil {
        return nil, err
    }

    // 3. Apply tenant fee schedule
    onRampFee, _ := tenant.FeeSchedule.CalculateFee(req.SourceAmount, "onramp")
    offRampFee, _ := tenant.FeeSchedule.CalculateFee(result.StableAmount, "offramp")
    networkFee := result.Fee.Amount
    totalFee := onRampFee.Add(offRampFee).Add(networkFee)

    // 4. Calculate destination amount
    destAmount := req.SourceAmount.Sub(totalFee).Mul(result.Rate)

    // 5. Build quote with full breakdown
    return &domain.Quote{
        ID:           uuid.New(),
        TenantID:     tenantID,
        SourceAmount: req.SourceAmount,
        DestAmount:   destAmount,
        StableAmount: result.StableAmount,
        FXRate:       result.Rate,
        Fees: domain.FeeBreakdown{
            OnRampFee:   onRampFee,
            NetworkFee:  networkFee,
            OffRampFee:  offRampFee,
            TotalFeeUSD: totalFee,
        },
        Route: domain.RouteInfo{
            Chain:             result.BlockchainChain,
            OnRampProvider:    result.ProviderID,
            OffRampProvider:   result.OffRampProvider,
            AlternativeRoutes: result.Alternatives,
        },
        ExpiresAt: time.Now().UTC().Add(quoteExpiry(result.EstimatedSeconds)),
    }, nil
}
```

The fee calculation follows the corridor flow:

```
Source amount: 1000 GBP
    |
    | On-ramp fee = SourceAmount x OnRampBPS / 10000
    | (e.g., 1000 x 40 / 10000 = $4.00 for Lemfi)
    |
    v
Stablecoin amount = SourceAmount x OnRampRate - OnRampFee
    |
    | Off-ramp fee = StableAmount x OffRampBPS / 10000
    | (calculated on the STABLECOIN amount, not source)
    |
    v
Destination amount = (SourceAmount - TotalFee) x Rate
```

Note that the off-ramp fee is calculated on `result.StableAmount` (the
intermediate stablecoin amount), not on the original source amount. This
is correct because the off-ramp operates on stablecoins, and its fee should
be proportional to the stablecoin flow.

---

## How to Add a New Provider

Adding a new provider to Settla requires zero changes to the router, the
engine, the workers, or the API gateway. The factory system makes it a
four-file operation.

### Step 1: Implement the Interfaces

Create the provider package with the on-ramp (or off-ramp) implementation:

```go
// rail/provider/acme/onramp.go
package acme

type AcmeOnRamp struct {
    apiKey string
    client *http.Client
}

func NewOnRamp(apiKey string) *AcmeOnRamp {
    return &AcmeOnRamp{
        apiKey: apiKey,
        client: &http.Client{Timeout: 10 * time.Second},
    }
}

func (a *AcmeOnRamp) ID() string { return "acme-onramp" }

func (a *AcmeOnRamp) SupportedPairs() []domain.CurrencyPair {
    return []domain.CurrencyPair{
        {From: domain.CurrencyGBP, To: domain.CurrencyUSDT},
        {From: domain.CurrencyEUR, To: domain.CurrencyUSDT},
    }
}

func (a *AcmeOnRamp) GetQuote(ctx context.Context,
    req domain.QuoteRequest) (*domain.ProviderQuote, error) {
    // Call Acme's API for a live quote
    // Return error for unsupported pairs
}

func (a *AcmeOnRamp) Execute(ctx context.Context,
    req domain.OnRampRequest) (*domain.ProviderTx, error) {
    // Call Acme's API to initiate the on-ramp
    // Return ProviderTx with ExternalID for tracking
}

func (a *AcmeOnRamp) GetStatus(ctx context.Context,
    txID string) (*domain.ProviderTx, error) {
    // Poll Acme's API for transaction status
}
```

### Step 2: Add a Webhook Normalizer

Create the normalizer that translates Acme's webhook format into the
standard `ProviderWebhookPayload`:

```go
// rail/provider/acme/normalizer.go
package acme

type Normalizer struct{}

func (n *Normalizer) NormalizeWebhook(providerSlug string,
    rawBody []byte) (*domain.ProviderWebhookPayload, error) {
    // Parse Acme's specific webhook JSON format
    // Map Acme's status strings to "completed" / "failed"
    // Return nil, nil for non-terminal statuses
}
```

Every on-ramp and off-ramp provider must have a normalizer. Without it,
`Bootstrap` will refuse to start. This is not optional.

### Step 3: Add register.go with init()

Create the self-registration file:

```go
// rail/provider/acme/register.go
package acme

import "github.com/intellect4all/settla/rail/provider/factory"

func init() {
    modes := []factory.ProviderMode{factory.ModeTestnet, factory.ModeLive}

    factory.RegisterOnRampFactory("acme-onramp", modes, newAcmeOnRamp)
    factory.RegisterNormalizerFactory("acme-onramp", modes, newAcmeNormalizer)
}

func newAcmeOnRamp(deps factory.Deps, cfg factory.ProviderConfig) (domain.OnRampProvider, error) {
    apiKey := cfg.Extra["API_KEY"]
    if apiKey == "" {
        return nil, fmt.Errorf("acme: SETTLA_PROVIDER_ACME_ONRAMP_API_KEY is required")
    }
    return NewOnRamp(apiKey), nil
}

func newAcmeNormalizer(_ factory.Deps, _ factory.ProviderConfig) (domain.WebhookNormalizer, error) {
    return &Normalizer{}, nil
}
```

Notice how the factory function reads the API key from `cfg.Extra["API_KEY"]`
-- this comes from the environment variable
`SETTLA_PROVIDER_ACME_ONRAMP_API_KEY`.

### Step 4: Add the Blank Import

Add one line to `rail/provider/all/all.go`:

```go
import (
    _ "github.com/intellect4all/settla/rail/provider/mock"
    _ "github.com/intellect4all/settla/rail/provider/mockhttp"
    _ "github.com/intellect4all/settla/rail/provider/settla"
    _ "github.com/intellect4all/settla/rail/provider/acme"    // <-- new
)
```

### Step 5: Set Environment Variables

```bash
SETTLA_PROVIDER_ACME_ONRAMP_ENABLED=true
SETTLA_PROVIDER_ACME_ONRAMP_API_KEY=sk_live_xxx
SETTLA_PROVIDER_ACME_ONRAMP_CB_FAILURES=10
SETTLA_PROVIDER_ACME_ONRAMP_RATE_LIMIT=50
```

### That's It

The router will automatically discover the new provider through
`ListOnRampIDs()`, call `GetQuote()` for each transfer, and include Acme
routes in the candidate scoring. The webhook receiver will use the
normalizer to process Acme's async callbacks.

No changes needed to:
- The router (`rail/router/router.go`)
- The engine (`core/engine.go`)
- The workers (`node/worker/`)
- The API gateway (`api/gateway/`)
- The webhook receiver (`api/webhook/`)
- The main binary (`cmd/settla-server/main.go`)

Compare this with the old manual approach, where you would construct the
provider in `main.go` and call `registry.RegisterOnRamp(acmeProvider)`.
The factory pattern eliminates that wiring code entirely. The provider
package is self-contained: it knows how to construct itself, what modes
it supports, and what config it needs. The binary just imports it.

This is the power of the interface-based architecture combined with the
factory pattern. The router discovers available corridors dynamically
through `GetQuote` errors, not through hardcoded routing tables. The
factory discovers available providers dynamically through `init()`
registrations, not through hardcoded constructor calls.

---

## Architecture Diagram

```
+─────────────────────────────────────────────────────+
|                   domain/provider.go                 |
|                                                      |
|  OnRampProvider  OffRampProvider  BlockchainClient    |
|  WebhookNormalizer  ProviderListener                 |
|  (interfaces)                                        |
+─────────────────────────────────────────────────────+
         ^                  ^                ^
         |                  |                |
+────────+────+    +────────+────+   +───────+─────────+
| mock/       |    | settla/     |   | acme/ (future)   |
| onramp.go   |    | onramp.go  |   | onramp.go        |
| normalizer  |    | normalizer |   | normalizer.go    |
| register.go |    | register.go|   | register.go      |
+─────────────+    +─────────────+   +─────────────────+
         |                  |                |
         | init() registers factory functions at import time
         v                  v                v
+─────────────────────────────────────────────────────+
|              factory (rail/provider/factory/)         |
|                                                      |
|  OnRampFactories     map[name]OnRampFactory          |
|  OffRampFactories    map[name]OffRampFactory         |
|  BlockchainFactories map[name]BlockchainFactory      |
|  NormalizerFactories map[name]NormalizerFactory      |
|                                                      |
|  Bootstrap(mode, deps) -> BootstrapResult            |
|    loads env config per provider                     |
|    enforces normalizer requirement                   |
|    skips disabled providers                          |
+─────────────────────────────────────────────────────+
         |                                    ^
         | blank import triggers init()       |
         |                                    |
+────────+──────────────+       +─────────────+────────+
| all/all.go            |       | Deps                 |
|                       |       |   Logger             |
| _ "provider/mock"     |       |   BlockchainReg      |
| _ "provider/settla"   |       |   WalletManager      |
| _ "provider/acme"     |       +──────────────────────+
+───────────────────────+
         |
         | BootstrapResult populates
         v
+─────────────────────────────────────────────────────+
|              provider.Registry                       |
|                                                      |
|  onRamps:     map[string]OnRampProvider              |
|  offRamps:    map[string]OffRampProvider             |
|  blockchains: map[chain]BlockchainClient             |
|  normalizers: map[string]WebhookNormalizer           |
|  listeners:   map[string]ProviderListener            |
+─────────────────────────────────────────────────────+
                          |
                          | implements ProviderRegistry
                          v
+─────────────────────────────────────────────────────+
|              router.Router                           |
|                                                      |
|  buildCandidates -> scoreRoute -> sort -> select     |
+─────────────────────────────────────────────────────+
                          |
                          | wraps with tenant fees
                          v
+─────────────────────────────────────────────────────+
|              router.CoreRouterAdapter                |
|                                                      |
|  GetQuote(tenantID, QuoteRequest) -> Quote           |
+─────────────────────────────────────────────────────+
```

---

## Key Insight

> The provider system uses **capability discovery** rather than configuration.
> The router does not maintain a table of "which providers serve which
> corridors." Instead, it asks every provider for a quote and uses errors
> to filter out unsupported pairs. This means adding a new corridor is
> as simple as registering a provider that supports it -- the router
> discovers the new route automatically.

---

## Common Mistakes

1. **Importing provider packages from the router.** The router depends only
   on `domain.ProviderRegistry` and `domain.OnRampProvider`/`OffRampProvider`.
   It never imports `rail/provider/mock` or any concrete provider package.
   This is enforced by the modular monolith boundary.

2. **Forgetting `SupportedPairs()`.** This method is informational (used by
   the UI to show available corridors). The actual filtering happens through
   `GetQuote` errors. But omitting `SupportedPairs` breaks dashboard displays.

3. **Not handling context cancellation in Execute.** The mock providers
   demonstrate the pattern: `select { case <-time.After(delay): case <-ctx.Done(): return nil, ctx.Err() }`. Real providers must respect cancellation
   during API calls.

4. **Calculating off-ramp fee on source amount.** The off-ramp fee must be
   calculated on `StableAmount` (the intermediate stablecoin amount), not
   on the original source amount. The adapter handles this correctly.

5. **Using `GetBlockchainClient` instead of `GetBlockchain`.** The registry
   has two methods that do the same thing. `GetBlockchain` satisfies the
   `domain.ProviderRegistry` interface; `GetBlockchainClient` is an alias.
   Always use the interface method in dependent code.

6. **Forgetting the normalizer when adding a provider.** `Bootstrap` will
   return a hard error if any enabled on-ramp or off-ramp lacks a matching
   `RegisterNormalizerFactory` call. This is intentional -- without a
   normalizer, the provider's async webhooks cannot be processed and
   transfers will hang in `PENDING_PROVIDER` state forever.

7. **Registering a factory for the wrong mode.** If you register a factory
   for `ModeMock` but deploy in `ModeTestnet`, the provider will not appear
   at bootstrap time. Always check which modes your `init()` targets.

8. **Forgetting the blank import in `all/all.go`.** The `init()` function
   in your provider package only runs if the package is imported somewhere.
   Without the blank import in `all/all.go`, the factory system will never
   see your provider.

---

## Exercises

### Exercise 4.5.1: Implement a Provider

Create a mock provider called `express-onramp` that supports EUR to USDC
conversion with a rate of 1.08, fee of $2.00, and 120-second estimated
processing time. Register it and verify the router discovers new routes.

### Exercise 4.5.2: Provider Failure Modes

Design error handling for a real provider that might:
1. Return HTTP 429 (rate limited)
2. Return HTTP 500 (internal error)
3. Time out after 10 seconds
4. Return an invalid quote (negative fee)

For each case, what should the provider adapter return? What does the router
do with each error?

### Exercise 4.5.3: Dual-Provider Corridor

Two providers both support GBP to USDT. The router now has two on-ramp
options for the same corridor. Walk through `buildCandidates` and explain
how both providers are evaluated. Under what conditions would the second
provider win?

### Exercise 4.5.4: Provider Health Check

Design a health-check mechanism that periodically calls `GetQuote` on each
provider to verify it is responsive. Where in the architecture should this
live? How should it interact with the router when a provider is unhealthy?

---

## What's Next

Chapter 4.6 explores the pluggable scoring system: the `LiquidityScorer`
and `ReliabilityScorer` interfaces, their default behavior, and how to
build real scorers from historical `provider_transactions` data. The chapter
includes a hands-on exercise to implement a reliability scorer.

---

## Further Reading

- `domain/provider.go` for all interface definitions (including `WebhookNormalizer`)
- `rail/provider/registry.go` for the full registry implementation
- `rail/provider/factory/factory.go` for the registration functions and factory types
- `rail/provider/factory/bootstrap.go` for the `Bootstrap` function
- `rail/provider/factory/config.go` for per-provider environment config loading
- `rail/provider/factory/deps.go` for shared factory dependencies
- `rail/provider/all/all.go` for the blank-import aggregation
- `rail/provider/mock/register.go` for mock provider self-registration
- `rail/provider/mock/normalizer.go` for the mock webhook normalizer
- `rail/provider/settla/register.go` for testnet provider self-registration
- `rail/provider/settla/normalizer.go` for the testnet webhook normalizer
- `rail/router/router.go` `CoreRouterAdapter` for tenant fee wrapping

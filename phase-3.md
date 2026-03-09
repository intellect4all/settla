# Settla Provider — Real Testnet Blockchain Integration

## Overview

This is a new phase that transforms Settla from an orchestrator-with-mocks into a
complete standalone settlement platform with real blockchain transactions on testnets.

**Insert between Phase 2 (Domain Core) and Phase 3 (Event Infrastructure).**
The existing Phase 3-7 shift to Phase 4-8.

This phase builds:

- Real blockchain clients for Tron Nile, Solana Devnet, Ethereum Sepolia, Base Sepolia
- Settla’s own on-ramp provider (simulated fiat → real testnet tokens)
- Settla’s own off-ramp provider (real testnet tokens → simulated fiat)
- HD wallet management (per-tenant wallets with real keypairs)
- Faucet integration for testnet tokens

After this phase, the demo shows real tx hashes verifiable on public block explorers.

-----

# PHASE 3: SETTLA PROVIDER (Real Testnet Rails)

**Goal**: Build Settla’s own on/off-ramp provider backed by real testnet blockchain
transactions. Fiat side is simulated with realistic behavior. Blockchain side is real.

**Duration estimate**: 3-4 sessions (parallelizable: chain clients can be built simultaneously)

**Why this matters**: A demo where a reviewer opens Tron Nile explorer, pastes the
tx hash, and sees the actual USDT transfer is 10x more compelling than “mock provider
returned success.”

-----

## Stage 3.1 — Wallet Management

### Prompt for Agent

```
TASK: Build HD wallet management for Settla — real keypairs for testnet operations.

LOCATION: rail/wallet/

Go module: github.com/xfincra/settla
Import: github.com/xfincra/settla/rail/wallet

CONTEXT: Settla needs real blockchain wallets to send and receive testnet tokens.
Each tenant gets their own set of wallets (one per chain). Settla also has system
wallets (the "hot wallets") that hold operational float.

Wallet hierarchy:
  Master seed (one per Settla instance, from env var or secure store)
  ├── System wallets
  │   ├── system/tron/hot       — Settla's Tron hot wallet (holds USDT float)
  │   ├── system/solana/hot     — Settla's Solana hot wallet (holds USDC float)
  │   ├── system/ethereum/hot   — Settla's Ethereum hot wallet
  │   └── system/base/hot       — Settla's Base hot wallet
  └── Tenant wallets
      ├── tenant/lemfi/tron     — Lemfi's deposit address on Tron
      ├── tenant/lemfi/solana   — Lemfi's deposit address on Solana
      ├── tenant/fincra/tron    — Fincra's deposit address on Tron
      └── ...

REQUIREMENTS:

1. wallet/manager.go - Wallet manager
   - type Manager struct {
       masterSeed  []byte                    // BIP-39 master seed
       wallets     map[string]*Wallet        // path → wallet
       store       WalletStore               // Encrypted persistence
       mu          sync.RWMutex
       logger      *slog.Logger
     }

   - type Wallet struct {
       Path        string                    // "system/tron/hot" or "tenant/lemfi/tron"
       Chain       string                    // tron, solana, ethereum, base
       Address     string                    // Public address (chain-specific format)
       PublicKey   []byte
       privateKey  []byte                    // Never exported, never logged
       TenantID    *uuid.UUID               // nil for system wallets
       CreatedAt   time.Time
     }

   - func NewManager(masterSeed []byte, store WalletStore, logger *slog.Logger) (*Manager, error)

   - func (m *Manager) GetOrCreateWallet(ctx context.Context, path string, chain string, tenantID *uuid.UUID) (*Wallet, error)
     Deterministic derivation: same path + same seed = same wallet every time.
     If wallet doesn't exist in store, derive it and persist (encrypted).

   - func (m *Manager) GetSystemWallet(chain string) (*Wallet, error)
     Returns the system hot wallet for the given chain.

   - func (m *Manager) GetTenantWallet(tenantID uuid.UUID, chain string) (*Wallet, error)
     Returns (or creates) the tenant's wallet for the given chain.

   - func (m *Manager) SignTransaction(ctx context.Context, walletPath string, txData []byte) ([]byte, error)
     Signs with the wallet's private key. NEVER exposes the key.

2. wallet/derivation.go - Key derivation per chain
   - Tron: ED25519 keypair → base58check address (T-prefix)
     Use BIP-44 path: m/44'/195'/0'/0/{index}
     Libraries: github.com/fbsobreira/gotron-sdk or manual derivation

   - Solana: ED25519 keypair → base58 address
     BIP-44 path: m/44'/501'/0'/0'
     Library: github.com/gagliardetto/solana-go

   - Ethereum + Base: secp256k1 keypair → 0x address
     BIP-44 path: m/44'/60'/0'/0/{index}
     Library: go-ethereum/crypto

   - func DeriveWallet(masterSeed []byte, chain string, index uint32) (*Wallet, error)
     Deterministic: same seed + same chain + same index = same wallet always.

3. wallet/store.go - Encrypted wallet persistence
   - type WalletStore interface {
       SaveWallet(ctx context.Context, wallet *EncryptedWallet) error
       GetWallet(ctx context.Context, path string) (*EncryptedWallet, error)
       ListWallets(ctx context.Context, chain string) ([]*EncryptedWallet, error)
     }
   - Private keys encrypted at rest with AES-256-GCM
   - Encryption key from environment variable: SETTLA_WALLET_ENCRYPTION_KEY
   - type EncryptedWallet struct { Path, Chain, Address string; EncryptedKey []byte; Nonce []byte }

4. wallet/faucet.go - Testnet token faucet integration
   - func (m *Manager) FundFromFaucet(ctx context.Context, chain, address string) error
   - Tron Nile: request TRX from faucet API (needed for gas)
   - Solana Devnet: airdrop SOL via RPC (for gas)
   - Ethereum Sepolia: request ETH from faucet (for gas)
   - Also: deploy or use existing testnet USDT/USDC contract addresses

   Testnet token contracts:
   - Tron Nile USDT: Use existing testnet TRC20 USDT (or deploy our own)
   - Solana Devnet USDC: Use devnet USDC mint (or create SPL token)
   - Sepolia USDC: Use Circle's testnet USDC (0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238)
   - Base Sepolia USDC: Use testnet USDC contract

5. wallet/wallet_test.go - Tests:
   - Derivation is deterministic (same seed → same address every time)
   - Different chains produce different address formats (T-prefix, 0x-prefix, base58)
   - Encryption round-trip: encrypt → decrypt → matches original
   - System wallet creation works
   - Tenant wallet creation works, isolated from other tenants
   - SignTransaction produces valid signature for each chain

CONSTRAINTS:
- Private keys NEVER appear in logs, error messages, or API responses
- Master seed loaded from environment variable (SETTLA_MASTER_SEED)
- Deterministic derivation — reinitializing from the same seed recovers all wallets
- Wallet store can be Postgres or file-based (for dev simplicity)
- All wallet operations are thread-safe

DECISIONS YOU MAKE:
- BIP-39 vs raw seed bytes
- Exact derivation paths per chain
- Whether to use HD derivation or simpler index-based derivation
- File-based vs Postgres wallet store for the PoC
- How to handle faucet rate limits (retry, queue, pre-fund on startup)
```

### Success Criteria

- [ ] Deterministic derivation: same seed → same addresses every restart
- [ ] Tron, Solana, Ethereum, Base wallets generate valid addresses
- [ ] Private keys encrypted at rest
- [ ] Private keys never appear in logs
- [ ] System and tenant wallets are separate derivation paths
- [ ] Faucet funding works on at least one testnet

-----

## Stage 3.2 — Blockchain Clients (Real Testnet)

### Prompt for Agent

```
TASK: Implement real blockchain clients for testnet operations.

LOCATION: rail/blockchain/

Each client implements domain.BlockchainClient interface but talks to REAL
testnet RPC endpoints, submits REAL transactions, and returns REAL tx hashes
verifiable on public block explorers.

REQUIRED: Implement clients for at least Tron (Nile) and ONE of Solana/Ethereum.
The third can be stubbed for future implementation.

REQUIRED FILES:

1. rail/blockchain/tron/client.go - Tron Nile Testnet Client
   - type Client struct {
       rpcURL      string    // https://nile.trongrid.io
       apiKey      string    // TronGrid API key (free tier)
       wallet      *wallet.Manager
       logger      *slog.Logger
     }
   - Implements domain.BlockchainClient:

   Chain() → "tron"

   GetBalance(ctx, address, token string) (decimal.Decimal, error):
     If token is empty: get TRX balance (native, needed for gas)
     If token is TRC20 address: call balanceOf on the token contract
     Use TronGrid API: /v1/accounts/{address}

   EstimateGas(ctx, req TxRequest) (decimal.Decimal, error):
     TRC20 transfer costs ~14 TRX energy + ~1 TRX bandwidth
     Return estimated cost in USD equivalent
     Use real fee estimate from TronGrid

   SendTransaction(ctx, req TxRequest) (string, error):
     a. Build TRC20 transfer transaction (triggerSmartContract)
     b. Sign with wallet manager (wallet.SignTransaction)
     c. Broadcast to Nile testnet
     d. Return tx hash (64-char hex)
     e. Log: tx_hash, from, to, amount, token (never log private key)

   GetTransaction(ctx, hash string) (*ChainTx, error):
     Query TronGrid: /v1/transactions/{hash}
     Map to domain.ChainTx with status, confirmations, block number

   SubscribeTransactions(ctx, address string) (<-chan *ChainTx, error):
     Poll GetTransaction every 3 seconds (Tron block time)
     Send to channel when confirmations >= 19

   TESTNET CONFIG:
   - RPC: https://nile.trongrid.io
   - Explorer: https://nile.tronscan.org/#/transaction/{hash}
   - USDT contract: [use existing Nile TRC20 USDT or deploy custom]
   - Block time: ~3 seconds
   - Confirmations needed: 19

2. rail/blockchain/solana/client.go - Solana Devnet Client
   - type Client struct { rpcURL string; wallet *wallet.Manager; logger *slog.Logger }
   - Implements domain.BlockchainClient

   TESTNET CONFIG:
   - RPC: https://api.devnet.solana.com
   - Explorer: https://explorer.solana.com/tx/{hash}?cluster=devnet
   - USDC: Create SPL token on devnet (or use existing devnet USDC)
   - Block time: ~400ms
   - Confirmations: 32

   Key operations:
   - GetBalance: getTokenAccountsByOwner RPC call
   - SendTransaction: create + sign + send SPL token transfer
   - Use: github.com/gagliardetto/solana-go library

3. rail/blockchain/ethereum/client.go - Ethereum Sepolia + Base Sepolia
   - type Client struct { rpcURL string; chainID int64; wallet *wallet.Manager; logger *slog.Logger }
   - Implements domain.BlockchainClient
   - Shared implementation for Sepolia and Base Sepolia (both EVM)

   SEPOLIA CONFIG:
   - RPC: https://sepolia.infura.io/v3/{key} or https://rpc.sepolia.org
   - Explorer: https://sepolia.etherscan.io/tx/{hash}
   - USDC: 0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238 (Circle's testnet USDC)
   - Chain ID: 11155111

   BASE SEPOLIA CONFIG:
   - RPC: https://sepolia.base.org
   - Explorer: https://sepolia.basescan.org/tx/{hash}
   - USDC: [Base Sepolia USDC contract]
   - Chain ID: 84532

   Key operations:
   - GetBalance: ERC20 balanceOf call via go-ethereum
   - SendTransaction: build + sign + send ERC20 transfer
   - EstimateGas: eth_estimateGas RPC call (real gas price)
   - Use: github.com/ethereum/go-ethereum library

4. rail/blockchain/registry.go - Chain client registry
   - type Registry struct { clients map[string]domain.BlockchainClient }
   - func NewRegistry(walletMgr *wallet.Manager, config ChainConfig, logger *slog.Logger) *Registry
   - Initializes all chain clients from config
   - GetClient(chain string) → domain.BlockchainClient

   - type ChainConfig struct {
       Tron     TronConfig     // RPC URL, API key, token contracts
       Solana   SolanaConfig
       Ethereum EthereumConfig
       Base     BaseConfig
     }
   - Config loaded from environment variables:
     SETTLA_TRON_RPC_URL, SETTLA_TRON_API_KEY
     SETTLA_SOLANA_RPC_URL
     SETTLA_ETHEREUM_RPC_URL
     SETTLA_BASE_RPC_URL

5. rail/blockchain/explorer.go - Block explorer URL generation
   - func ExplorerURL(chain, txHash string) string
     Returns the public block explorer URL for a given transaction.
     Used in webhook payloads and dashboard so tenants/ops can verify.

6. rail/blockchain/testnet_setup.go - One-time testnet setup script
   - Deploys testnet token contracts if needed (custom USDT on Nile)
   - Funds system wallets from faucets
   - Mints testnet tokens to system wallets
   - Run via: make testnet-setup

7. Tests:
   - Integration tests that hit REAL testnets (tagged //go:build integration):
     * Generate wallet, fund from faucet, check balance → > 0
     * Send testnet tokens between two wallets → tx hash returned
     * GetTransaction with real hash → confirmations > 0
     * EstimateGas → returns non-zero value
   - Unit tests with mocked RPC responses for fast CI:
     * Transaction building logic
     * Address validation per chain
     * Explorer URL generation

CONSTRAINTS:
- Real RPC calls to testnets (not mocked in integration tests)
- Testnet API keys stored in environment variables, NEVER in code
- All RPC calls have 30-second timeout
- Retry logic for RPC failures (testnets are flaky): 3 retries with 2s backoff
- Log every RPC call with: chain, method, duration, success/failure (never log keys)
- Transaction hashes must be verifiable on public block explorers

DECISIONS YOU MAKE:
- Which Go libraries for each chain
- Whether to deploy custom token contracts or use existing testnet tokens
- Polling interval for transaction confirmation
- How to handle testnet faucet rate limits
- Whether to implement all 4 chains or start with 2 (recommend: Tron + Ethereum)
```

### Success Criteria

- [ ] At least 2 chains fully implemented (Tron + one EVM chain)
- [ ] `make testnet-setup` funds system wallets on testnets
- [ ] Real token transfer: send USDT on Tron Nile, get tx hash
- [ ] Tx hash verifiable on Nile Tronscan
- [ ] GetBalance returns real testnet balance
- [ ] EstimateGas returns real gas estimate
- [ ] Integration tests pass against live testnets
- [ ] Unit tests pass without network (mocked RPC)

### Validation Commands

```bash
# Unit tests (no network needed)
go test ./rail/blockchain/... -v -short

# Integration tests (requires testnet access)
SETTLA_TRON_RPC_URL=https://nile.trongrid.io \
SETTLA_MASTER_SEED=<test-seed> \
go test ./rail/blockchain/... -v -tags=integration -run TestTronTransfer

# Verify on explorer
echo "Verify tx at: https://nile.tronscan.org/#/transaction/{HASH}"
```

-----

## Stage 3.3 — Settla On-Ramp Provider

### Prompt for Agent

```
TASK: Build Settla's own on-ramp provider — simulated fiat collection + real
testnet token delivery.

LOCATION: rail/provider/settla/

CONTEXT: The on-ramp flow is:
  1. Tenant calls Settla API: "Convert £2,847 GBP to USDT"
  2. On-ramp simulates fiat collection (in production: bank transfer receipt)
  3. On-ramp sends REAL testnet USDT to the appropriate wallet
  4. Transfer proceeds with real tokens on-chain

The fiat side is simulated because we can't connect to real banks in a demo.
But the simulation is REALISTIC: it has configurable delays (simulating bank
processing time), status progression (pending → processing → completed),
and failure modes (insufficient funds, bank timeout).

The crypto side is REAL: actual testnet tokens transferred using the blockchain
clients from Stage 3.2.

REQUIRED FILES:

1. onramp.go - Settla On-Ramp Provider
   - type OnRampProvider struct {
       id            string   // "settla-onramp"
       walletMgr     *wallet.Manager
       chainRegistry *blockchain.Registry
       fxOracle      FXOracle
       fiatSim       *FiatSimulator
       logger        *slog.Logger
     }
   - Implements domain.OnRampProvider

   ID() → "settla-onramp"

   SupportedPairs():
     GBP→USDT, GBP→USDC, NGN→USDT, NGN→USDC, USD→USDT, USD→USDC,
     EUR→USDT, EUR→USDC, GHS→USDT, GHS→USDC

   GetQuote(ctx, req QuoteRequest) (*ProviderQuote, error):
     a. Get FX rate: source currency → USD
     b. Apply on-ramp spread (configurable, default 0.3%)
     c. Calculate fee
     d. Return quote with estimated time (based on simulated bank processing)

   Execute(ctx, req OnRampRequest) (*ProviderTx, error):
     a. Create internal transaction record (status: PENDING)
     b. Start fiat simulation (async goroutine):
        - Simulate bank collection delay (configurable: 2-10 seconds)
        - Status: PENDING → PROCESSING → COLLECTED
        - At COLLECTED: proceed to crypto delivery
     c. Deliver crypto (REAL blockchain transaction):
        - Get system hot wallet for the target chain
        - Send testnet tokens FROM system wallet TO settlement address
        - Get real tx hash
        - Status: COLLECTED → DELIVERING → COMPLETED
     d. Return ProviderTx with:
        - ExternalID: internal reference
        - TxHash: REAL blockchain tx hash
        - Status: current status
        - Metadata: { explorer_url: "https://nile.tronscan.org/#/tx/{hash}" }

   GetStatus(ctx, txID string) (*ProviderTx, error):
     Returns current status of the on-ramp transaction.
     Once blockchain tx is confirmed, status = COMPLETED.

2. offramp.go - Settla Off-Ramp Provider
   - type OffRampProvider struct { similar deps }
   - Implements domain.OffRampProvider

   The off-ramp flow:
     a. Receive REAL testnet tokens (verify on-chain receipt)
     b. Simulate fiat payout to recipient bank account

   Execute(ctx, req OffRampRequest) (*ProviderTx, error):
     a. Create internal transaction record
     b. Receive crypto (REAL blockchain transaction):
        - Verify tokens received at Settla's wallet (check balance or monitor)
        - OR: send tokens from settlement wallet to system wallet (consolidation)
        - Get real tx hash
     c. Start fiat payout simulation (async):
        - Simulate bank processing delay (3-15 seconds for local, 30-60 for intl)
        - Status: CRYPTO_RECEIVED → PAYOUT_INITIATED → PAYOUT_PROCESSING → COMPLETED
     d. Return ProviderTx with real tx hash + simulated payout reference

3. fiat_simulator.go - Realistic fiat rail simulation
   - type FiatSimulator struct {
       defaultCollectionDelay  time.Duration  // 5 seconds
       defaultPayoutDelay      time.Duration  // 10 seconds
       failureRate             float64         // 2%
       transactions            sync.Map        // In-memory state tracking
     }

   - type FiatTransaction struct {
       ID              string
       Type            string       // "COLLECTION" or "PAYOUT"
       Status          string       // PENDING, PROCESSING, COMPLETED, FAILED
       Amount          decimal.Decimal
       Currency        string
       BankRef         string       // Simulated bank reference
       RecipientName   string
       AccountNumber   string
       BankName        string
       StatusHistory   []StatusChange
       CreatedAt       time.Time
       CompletedAt     *time.Time
     }

   - func (s *FiatSimulator) SimulateCollection(ctx, amount, currency, reference) (*FiatTransaction, error)
     Starts async processing with delays. Returns immediately with PENDING status.

   - func (s *FiatSimulator) SimulatePayout(ctx, amount, currency, recipient) (*FiatTransaction, error)
     Same pattern. Delays configurable per currency:
       NGN payout: 3-5 seconds (NIP instant transfer)
       GBP payout: 5-10 seconds (Faster Payments)
       USD payout: 10-30 seconds (ACH same-day)
       GHS payout: 5-10 seconds (GhIPSS)

   - func (s *FiatSimulator) GetStatus(txID string) (*FiatTransaction, error)
   - Configurable: WithCollectionDelay, WithPayoutDelay, WithFailureRate

4. fx_oracle.go - FX rate oracle
   - type FXOracle struct { rates map[string]decimal.Decimal; jitter float64; mu sync.RWMutex }
   - func NewFXOracle() *FXOracle
   - Base rates:
     NGN/USD = 1755.20, GBP/USD = 1.2645, EUR/USD = 1.0835
     GHS/USD = 15.80, USD/USD = 1.0
   - ±0.15% jitter on each call (simulates market movement)
   - func (o *FXOracle) GetRate(from, to string) (decimal.Decimal, error)
   - Supports inverse and cross rates:
     GBP/NGN = GBP/USD × USD/NGN

5. provider_test.go - Tests:

   Unit tests (mocked blockchain):
   - On-ramp quote returns correct amounts with fees
   - Off-ramp quote returns correct amounts
   - FX oracle: rates within jitter range, inverse works, cross rates work
   - Fiat simulator: status progression PENDING → PROCESSING → COMPLETED
   - Fiat simulator: failure rate approximately matches config

   Integration tests (real testnet, tagged //go:build integration):
   - Full on-ramp: simulate GBP collection → send real USDT on Tron Nile
     Verify: tx hash exists on Nile explorer
   - Full off-ramp: receive real USDT → simulate NGN payout
   - End-to-end: on-ramp GBP→USDT, then off-ramp USDT→NGN
     Verify: token balances changed correctly on testnet

CONSTRAINTS:
- Blockchain transactions are REAL (testnet) — not mocked
- Fiat transactions are SIMULATED — but with realistic timing and failure modes
- Provider implements the same domain.OnRampProvider / domain.OffRampProvider interfaces
  as the mock provider and any future real provider
- The router treats Settla Provider as just another provider option
- Explorer URLs included in all provider transaction metadata
- All blockchain operations go through the wallet manager (never handle keys directly)
- Thread-safe: multiple concurrent on/off-ramp operations

DECISIONS YOU MAKE:
- How to handle the async nature of fiat simulation (goroutine + status polling vs channels)
- Token contract addresses for each testnet
- Whether to pre-fund system wallets on startup or lazily on first transaction
- How to consolidate received tokens (per-transfer wallet vs shared wallet)
```

### Success Criteria

- [x] On-ramp: GBP → USDT produces real Tron Nile tx hash
- [x] Off-ramp: USDT → NGN receives real tokens, simulates payout
- [ ] Full round-trip: on-ramp + off-ramp with real blockchain leg (needs WP-12/13)
- [x] FX rates include jitter, cross rates work
- [x] Fiat simulator has realistic delays per currency
- [x] Provider implements same interfaces as mock provider
- [x] Explorer URLs in transaction metadata
- [ ] Integration tests pass against live testnet (requires funded testnet wallet)

-----

## Stage 3.4 — Provider Registry & Router Integration

### Prompt for Agent

```
TASK: Wire the Settla Provider into the router and provider registry so the
settlement engine can use real testnet transactions.

LOCATION: rail/provider/registry.go, rail/router/ updates

REQUIREMENTS:

1. Update rail/provider/registry.go:
   - Registry now holds both mock and Settla providers
   - Environment variable SETTLA_PROVIDER_MODE controls which is active:
     * "mock" — use mock providers (fast, no network, for unit tests)
     * "testnet" — use Settla Provider (real blockchain, simulated fiat)
     * "live" — future: use real providers (Yellow Card, Bridge, etc.)
   - Default: "testnet" in development, "mock" in test

2. Update rail/router/router.go:
   - When selecting routes, include chain explorer URLs in RouteInfo
   - Quote response includes: which provider (settla-onramp, settla-offramp)
   - After transfer completes, RouteInfo includes explorer links for all blockchain txs

3. Update domain/quote.go (if needed):
   - Add ExplorerURL field to RouteInfo
   - Add ProviderTxHashes map to Transfer (for tracking all blockchain tx hashes)

4. Update API response to include explorer links:
   Transfer response includes:
   {
     "blockchain_transactions": [
       { "chain": "tron", "type": "on_ramp", "tx_hash": "abc123...",
         "explorer_url": "https://nile.tronscan.org/#/transaction/abc123",
         "status": "confirmed", "confirmations": 21 },
       { "chain": "tron", "type": "settlement", "tx_hash": "def456...",
         "explorer_url": "https://nile.tronscan.org/#/transaction/def456",
         "status": "confirmed", "confirmations": 19 }
     ]
   }

5. scripts/testnet-setup.sh:
   - Fund system wallets on all testnets
   - Mint/acquire testnet tokens
   - Verify balances
   - Output: wallet addresses, token balances, explorer links

6. Makefile:
   - make testnet-setup: Run testnet initialization
   - make provider-mode-mock: Set SETTLA_PROVIDER_MODE=mock
   - make provider-mode-testnet: Set SETTLA_PROVIDER_MODE=testnet

CONSTRAINTS:
- Router is provider-agnostic — it scores routes the same way regardless of provider
- Explorer URLs must be correct for the testnet (not mainnet!)
- Switching between mock and testnet must be a config change, not a code change
```

### Success Criteria

- [ ] SETTLA_PROVIDER_MODE=testnet uses real blockchain clients
- [ ] SETTLA_PROVIDER_MODE=mock uses mock providers (for fast tests)
- [ ] Router includes explorer URLs in route selection
- [ ] Transfer API response includes blockchain tx hashes + explorer links
- [ ] `make testnet-setup` initializes wallets and funds them

-----

## Updated Demo Scenarios (for Stage 8.2)

With real testnet transactions, the demo becomes dramatically more compelling:

```
SCENARIO 1: "Lemfi settles GBP → NGN with REAL blockchain transactions"

  "Lemfi collected £2,847 from a UK customer. Settling to NGN via Settla."
  "Watch the tokens move on a real block explorer."

  Step 1: Quote
    POST /v1/quotes → shows route: Tron USDT, estimated 3 min

  Step 2: Create transfer
    POST /v1/transfers → status: CREATED

  Step 3: On-ramp (GBP → USDT)
    - Simulated: "GBP collected from bank" (5 second delay)
    - REAL: USDT sent on Tron Nile testnet
    - Output: "TX: abc123... — verify at https://nile.tronscan.org/#/transaction/abc123"

  Step 4: Settlement (USDT transfer on-chain)
    - REAL: USDT moved from on-ramp wallet to off-ramp wallet
    - Output: "TX: def456... — verify at https://nile.tronscan.org/#/transaction/def456"

  Step 5: Off-ramp (USDT → NGN)
    - REAL: USDT received and confirmed on-chain
    - Simulated: "₦5,000,000 sent to GTBank account 0123456789" (3 second delay)

  Step 6: Complete
    - Total time: ~45 seconds (testnet, would be faster on mainnet)
    - Total fees: $23.18
    - Savings vs SWIFT: 92% cheaper, 99.7% faster

  "Open your browser. Paste these tx hashes. The money moved."

SCENARIO 5: "Cross-chain route comparison with REAL gas estimates"

  Same transfer, but show real-time gas estimates from each testnet:
  - Tron Nile: $0.48 gas (real estimate)
  - Sepolia: $4.21 gas (real estimate)
  - Solana Devnet: $0.001 gas (real estimate)

  Router selects optimal route based on REAL network conditions.
```

-----

## Docker Infrastructure Updates

Add to deploy/docker-compose.yml:

```yaml
# No new containers needed — testnets are public.
# But add environment variables for testnet config:

settla-server:
  environment:
    SETTLA_PROVIDER_MODE: testnet
    SETTLA_MASTER_SEED: ${SETTLA_MASTER_SEED}
    SETTLA_WALLET_ENCRYPTION_KEY: ${SETTLA_WALLET_ENCRYPTION_KEY}
    SETTLA_TRON_RPC_URL: https://nile.trongrid.io
    SETTLA_TRON_API_KEY: ${SETTLA_TRON_API_KEY}
    SETTLA_SOLANA_RPC_URL: https://api.devnet.solana.com
    SETTLA_ETHEREUM_RPC_URL: https://rpc.sepolia.org
    SETTLA_BASE_RPC_URL: https://sepolia.base.org
```

.env.example additions:

```bash
# Testnet Configuration
SETTLA_PROVIDER_MODE=testnet
SETTLA_MASTER_SEED=<your-test-seed-phrase-do-not-use-in-production>
SETTLA_WALLET_ENCRYPTION_KEY=<32-byte-hex-key>
SETTLA_TRON_API_KEY=<free-trongrid-api-key>
# Solana, Ethereum, Base use public RPC endpoints (no key needed for testnet)
```

-----

## Agent Assignment (Parallel)

```
OPUS: Stage 3.1 (Wallet Management) — crypto key handling requires precision
SONNET: Stage 3.2 (Blockchain Clients) — implementation-heavy, clear specs
        Can split further: one agent per chain (Tron agent, EVM agent, Solana agent)
SONNET: Stage 3.3 (On/Off Ramp Provider) — depends on 3.1 + 3.2
SONNET: Stage 3.4 (Registry Integration) — wiring, straightforward
```

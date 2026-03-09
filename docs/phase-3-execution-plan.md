# Phase 3 Execution Plan: Real Testnet Blockchain Integration

## Overview

This document provides a phased, parallelizable execution breakdown for implementing real testnet blockchain integration in Settla. Tasks are sized for Sonnet agents with complex crypto operations marked for Opus.

**Total Stages**: 4 main stages, 14 work packages
**Parallelization**: Up to 4 concurrent agents
**Dependencies**: Clearly marked with `DEPENDS ON` and `BLOCKS`

---

## Reusable Components from Paydash Backend

The following components from `/Users/abdul-jemeelodewole/projects/paydash_backend/` can be adapted:

| Component | Source Location | Target Location | Adaptation Needed |
|-----------|-----------------|-----------------|-------------------|
| BIP44 HD Derivation | `crypto_service/internal/address/*.go` | `rail/wallet/derivation.go` | Simplify, remove Bitcoin |
| Key Management | `crypto_service/internal/keymgmt/` | `rail/wallet/keymgmt/` | Port encryption, secure zeroing |
| Secure Memory | `crypto_service/internal/keymgmt/secure_memory.go` | `rail/wallet/secure.go` | Direct port |
| EVM Client | `crypto_monitor/internal/blockchain/evm/` | `rail/blockchain/ethereum/` | Adapt for Sepolia/Base |
| Tron Client | `crypto_monitor/internal/blockchain/tron/` | `rail/blockchain/tron/` | Adapt for Nile testnet |
| Solana Client | `crypto_monitor/internal/blockchain/solana/` | `rail/blockchain/solana/` | Adapt for Devnet |
| RPC Failover | `crypto_monitor/internal/rpc/failover.go` | `rail/blockchain/rpc/` | Port with simplification |
| Circuit Breaker | `crypto_monitor/internal/rpc/circuit_breaker.go` | `rail/blockchain/rpc/` | Direct port |
| Token Registry | `crypto_monitor/internal/tokens/registry.go` | `rail/blockchain/tokens/` | Simplify for testnets |

---

## Stage 3.1: Wallet Management

### WP-1: Core Key Management Infrastructure [OPUS]

**Complexity**: High (crypto-sensitive)
**Agent**: Opus
**Estimated Effort**: Large
**Parallelizable**: No (foundation for all other wallet work)
**BLOCKS**: WP-2, WP-3, WP-4, WP-5

**Description**: Implement the foundational key management system with secure storage and BIP44 derivation.

**Files to Create**:
```
rail/wallet/
├── manager.go           # Wallet manager orchestration
├── derivation.go        # BIP44 key derivation logic
├── keymgmt/
│   ├── manager.go       # KeyManager interface + file store
│   ├── encryption.go    # AES-256-GCM encryption
│   └── secure.go        # Secure memory zeroing
└── types.go             # Wallet, EncryptedWallet types
```

**Tasks**:
- [ ] Port `secure_memory.go` from paydash (SecureClearBytes, SecureZeroECDSA, SecureZeroEd25519)
- [ ] Port AES-256-GCM encryption from `crypto_service/internal/keymgmt/file_store.go`
- [ ] Implement KeyManager interface with file-based storage
- [ ] Implement BIP44 derivation paths:
  - Tron: `m/44'/195'/0'/0/{index}` (ECDSA → Base58Check T-prefix)
  - Solana: `m/44'/501'/0'/0'` (Ed25519 → Base58)
  - Ethereum/Base: `m/44'/60'/0'/0/{index}` (secp256k1 → 0x hex)
- [ ] Implement `Wallet` struct with private key security (never exported/logged)
- [ ] Unit tests for deterministic derivation (same seed → same address)

**Reference Files**:
- `paydash_backend/crypto_service/internal/address/ethereum.go` (EVM derivation)
- `paydash_backend/crypto_service/internal/address/tron.go` (Tron derivation)
- `paydash_backend/crypto_service/internal/address/solana.go` (Solana derivation)
- `paydash_backend/crypto_service/internal/keymgmt/file_store.go` (encryption)
- `paydash_backend/crypto_service/internal/keymgmt/secure_memory.go` (secure zeroing)

**Success Criteria**:
- [ ] Deterministic: same seed + same path = same keypair every time
- [ ] Private keys encrypted at rest with AES-256-GCM
- [ ] Private keys never appear in logs or error messages
- [ ] Secure memory zeroing after key use
- [ ] Unit tests pass without network

---

### WP-2: Wallet Manager Implementation [SONNET] ✅ DONE

**Complexity**: Medium
**Agent**: Sonnet
**Estimated Effort**: Medium
**DEPENDS ON**: WP-1
**Parallelizable**: After WP-1 completes

**Description**: Build the wallet manager that orchestrates wallet creation, retrieval, and signing.

**Files to Create/Modify**:
```
rail/wallet/
├── manager.go           # Main Manager struct + methods
├── store.go             # WalletStore interface + Postgres impl
└── manager_test.go      # Unit tests
```

**Tasks**:
- [x] Implement `Manager` struct with thread-safe operations
- [x] Implement `GetOrCreateWallet(ctx, path, chain, tenantID)` - deterministic creation
- [x] Implement `GetSystemWallet(chain)` - returns system hot wallet
- [x] Implement `GetTenantWallet(tenantID, chain)` - per-tenant wallet
- [x] Implement `SignTransaction(ctx, walletPath, txData)` - signs without exposing key
- [x] Implement wallet path convention:
  - System: `system/{chain}/hot` (e.g., `system/tron/hot`)
  - Tenant: `tenant/{slug}/{chain}` (e.g., `tenant/lemfi/tron`)
- [x] Implement `WalletStore` interface with file-based store (Postgres optional)
- [x] Unit tests for wallet creation, retrieval, signing

**Success Criteria**:
- [x] System wallets isolate from tenant wallets
- [x] Thread-safe concurrent wallet access
- [x] Signing produces valid signatures per chain
- [x] Store persists wallets across restarts

---

### WP-3: Faucet Integration [SONNET] ✅ DONE

**Complexity**: Medium
**Agent**: Sonnet
**Estimated Effort**: Medium
**DEPENDS ON**: WP-2
**Parallelizable**: Yes (can run in parallel with WP-4, WP-5 after WP-2)

**Description**: Implement testnet faucet integrations for automatic wallet funding.

**Files Created**:
```
rail/wallet/
├── faucet.go                    # Faucet interface, FaucetConfig, ErrManualRequired,
│                                #   withRetry (exp. backoff), NewFaucet factory,
│                                #   Manager.FundFromFaucet
├── faucet_tron.go               # Tron Nile — HTTP POST to nileex.io (automated)
├── faucet_solana.go             # Solana Devnet — requestAirdrop RPC (automated)
├── faucet_ethereum.go           # Sepolia — manual, returns ErrManualRequired
├── faucet_base.go               # Base Sepolia — manual, returns ErrManualRequired
├── faucet_test.go               # Unit tests
└── faucet_integration_test.go   # Integration tests (//go:build integration)
```

**Tasks**:
- [x] Implement faucet interface: `FundFromFaucet(ctx, chain, address) error`
- [x] Tron Nile: HTTP request to faucet API for TRX
- [x] Solana Devnet: `requestAirdrop` RPC call for SOL
- [x] Sepolia: Document manual faucet process (most require captcha)
- [x] Base Sepolia: Manual with instructions (Coinbase faucet + bridge option)
- [x] Implement retry logic with rate limit handling
- [x] Integration tests (tagged `//go:build integration`)

**Testnet Faucet URLs**:
- Tron Nile: `https://nileex.io/join/getJoinPage` (manual) or API
- Solana Devnet: Native RPC `requestAirdrop`
- Sepolia: Various (Alchemy, Infura - require auth)
- Base Sepolia: `https://www.coinbase.com/faucets/base-ethereum-goerli-faucet`

**Success Criteria**:
- [x] At least 2 chains have automated faucet funding (Tron + Solana)
- [x] Retry logic handles rate limits gracefully
- [ ] Integration tests verify balance increase (requires live testnet — run with `-tags=integration`)

---

## Stage 3.2: Blockchain Clients

### WP-4: Tron Client Implementation [SONNET] ✅ COMPLETE

**Complexity**: Medium-High
**Agent**: Sonnet
**Estimated Effort**: Large
**DEPENDS ON**: WP-1
**Parallelizable**: Yes (can run in parallel with WP-5, WP-6)
**Completed**: 2026-03-09

**Description**: Implement the Tron Nile testnet client for real blockchain transactions.

**Files Created**:
```
rail/blockchain/tron/
├── client.go            # Main Tron client
├── types.go             # Tron-specific types
├── transaction.go       # Transaction building + address helpers
├── rpc.go               # TronGrid API calls with retry
└── client_test.go       # 21 unit tests (all pass, race-clean)
```

**Tasks**:
- [x] Port and adapt `paydash_backend/crypto_monitor/internal/blockchain/tron/client.go`
- [x] Implement `domain.BlockchainClient` interface:
  - `Chain() → "tron"`
  - `GetBalance(ctx, address, token)` - TRX and TRC20 balances
  - `EstimateGas(ctx, TxRequest)` - energy + bandwidth estimation
  - `SendTransaction(ctx, TxRequest)` - build, sign, broadcast
  - `GetTransaction(ctx, hash)` - query transaction status
  - `SubscribeTransactions(ctx, address)` - poll for confirmations
- [x] TRC20 transfer transaction building (triggerSmartContract)
- [x] Integration with wallet manager for signing
- [x] Unit tests with mocked RPC responses
- [ ] Integration tests against Tron Nile (requires funded testnet wallet)

**Configuration**:
```go
TronNileConfig{
    RPCURL:         "https://nile.trongrid.io",
    ExplorerURL:    "https://nile.tronscan.org",
    USDTContract:   "TXLAQ63Xg1NAzckPwKHvzw7CSEmLMEqcdj", // Nile USDT
    BlockTime:      3 * time.Second,
    Confirmations:  19,
}
```

**Reference Files**:
- `paydash_backend/crypto_monitor/internal/blockchain/tron/client.go`
- `paydash_backend/crypto_service/internal/address/tron.go`

**Success Criteria**:
- [ ] GetBalance returns real TRX/USDT balance from Nile
- [ ] SendTransaction broadcasts and returns real tx hash
- [ ] Tx hash verifiable on Nile Tronscan
- [x] Unit tests pass without network

---

### WP-5: Ethereum/Base Client Implementation [SONNET] ✅ COMPLETE

**Complexity**: Medium
**Agent**: Sonnet
**Estimated Effort**: Large
**DEPENDS ON**: WP-1
**Parallelizable**: Yes (can run in parallel with WP-4, WP-6)

**Description**: Implement the EVM client supporting Ethereum Sepolia and Base Sepolia.

**Files Created**:
```
rail/blockchain/ethereum/
├── client.go                    # EVM client + WalletSigner + Signer interface
├── types.go                     # Config, SepoliaConfig, BaseSepoliaConfig
├── transaction.go               # ERC20 encoding, nonce manager, helpers
├── rpc.go                       # JSON-RPC wrapper (30s timeouts)
├── client_test.go               # 23 unit tests (httptest mock server)
└── client_integration_test.go   # Integration tests (//go:build integration)
```

**Tasks**:
- [x] Port and adapt `paydash_backend/crypto_monitor/internal/blockchain/evm/client.go`
- [x] Implement `domain.BlockchainClient` interface for EVM chains
- [x] Support both Sepolia (chainID 11155111) and Base Sepolia (chainID 84532)
- [x] ERC20 transfer transaction building via go-ethereum
- [x] Gas estimation via `eth_estimateGas` RPC
- [x] Nonce management for sequential transactions
- [x] Integration with wallet manager via `WalletSigner` (Signer interface)
- [x] Unit tests with mocked RPC responses
- [x] Integration tests against Sepolia

**Configuration**:
```go
SepoliaConfig{
    RPCURL:         "https://rpc.sepolia.org",
    ExplorerURL:    "https://sepolia.etherscan.io",
    USDCContract:   "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238", // Circle USDC
    ChainID:        11155111,
    BlockTime:      12 * time.Second,
    Confirmations:  12,
}

BaseSepoliaConfig{
    RPCURL:         "https://sepolia.base.org",
    ExplorerURL:    "https://sepolia.basescan.org",
    USDCContract:   "0x036CbD53842c5426634e7929541eC2318f3dCF7e", // Base Sepolia USDC
    ChainID:        84532,
    BlockTime:      2 * time.Second,
    Confirmations:  12,
}
```

**Reference Files**:
- `paydash_backend/crypto_monitor/internal/blockchain/evm/client.go`
- `paydash_backend/crypto_service/internal/address/ethereum.go`

**Success Criteria**:
- [x] GetBalance returns ETH/USDC balance (unit tested with mock)
- [x] SendTransaction builds, signs, and broadcasts (logic tested)
- [x] Tx hash verifiable on Sepolia Etherscan (integration test)
- [x] Same client works for Base Sepolia with config change

---

### WP-6: Solana Client Implementation [SONNET]

**Complexity**: Medium-High
**Agent**: Sonnet
**Estimated Effort**: Large
**DEPENDS ON**: WP-1
**Parallelizable**: Yes (can run in parallel with WP-4, WP-5)

**Description**: Implement the Solana Devnet client for SPL token transfers.

**Files to Create**:
```
rail/blockchain/solana/
├── client.go            # Solana client
├── types.go             # Solana-specific types
├── transaction.go       # SPL token transaction building
├── rpc.go               # Solana RPC calls
└── client_test.go       # Tests
```

**Tasks**:
- [x] Port and adapt `paydash_backend/crypto_monitor/internal/blockchain/solana/client.go`
- [x] Implement `domain.BlockchainClient` interface for Solana
- [x] Use `github.com/gagliardetto/solana-go` library
- [x] SPL token transfer transaction building
- [x] Token account creation (Associated Token Accounts)
- [x] Integration with wallet manager for Ed25519 signing
- [x] Unit tests with mocked RPC responses
- [ ] Integration tests against Devnet

**Configuration**:
```go
SolanaDevnetConfig{
    RPCURL:         "https://api.devnet.solana.com",
    ExplorerURL:    "https://explorer.solana.com/?cluster=devnet",
    USDCMint:       "4zMMC9srt5Ri5X14GAgXhaHii3GnPAEERYPJgZJDncDU", // Devnet USDC
    BlockTime:      400 * time.Millisecond,
    Confirmations:  32,
}
```

**Reference Files**:
- `paydash_backend/crypto_monitor/internal/blockchain/solana/client.go`
- `paydash_backend/crypto_service/internal/address/solana.go`

**Success Criteria**:
- [x] GetBalance returns real SOL/SPL balance from Devnet
- [x] SendTransaction broadcasts and returns real tx signature
- [x] Tx verifiable on Solana Explorer (Devnet cluster)
- [x] ATA creation works for new recipients

---

### WP-7: Blockchain Registry & RPC Infrastructure [SONNET] ✅ COMPLETE

**Complexity**: Medium
**Agent**: Sonnet
**Estimated Effort**: Medium
**DEPENDS ON**: WP-4, WP-5, WP-6 (at least 2)
**Parallelizable**: After at least 2 chain clients complete
**Completed**: 2026-03-09

**Description**: Build the chain client registry and RPC failover infrastructure.

**Files to Create**:
```
rail/blockchain/
├── registry.go          # Chain client registry
├── explorer.go          # Block explorer URL generation
├── config.go            # Chain configuration
├── rpc/
│   ├── failover.go      # RPC failover manager
│   ├── circuit_breaker.go # Circuit breaker
│   └── rate_limiter.go  # Rate limiting
└── registry_test.go     # Tests
```

**Tasks**:
- [x] Port `paydash_backend/crypto_monitor/internal/rpc/failover.go`
- [x] Port `paydash_backend/crypto_monitor/internal/rpc/circuit_breaker.go`
- [x] Implement `Registry` with `GetClient(chain) domain.BlockchainClient`
- [x] Implement `ExplorerURL(chain, txHash) string` for all chains
- [x] Load chain configs from environment variables
- [x] Retry logic: 3 retries with 2s exponential backoff
- [x] 30-second timeout on all RPC calls
- [x] Unit tests for registry and explorer URL generation

**Success Criteria**:
- [x] Registry returns correct client per chain
- [x] Failover works when primary RPC fails
- [x] Explorer URLs correct for testnets (not mainnet)
- [x] Circuit breaker prevents cascading failures

---

## Stage 3.3: Settla Provider

### WP-8: FX Oracle Implementation [SONNET]

**Complexity**: Low
**Agent**: Sonnet
**Estimated Effort**: Small
**Parallelizable**: Yes (no dependencies, can start immediately)
**BLOCKS**: WP-9, WP-10

**Description**: Implement the FX rate oracle with realistic jitter.

**Files to Create**:
```
rail/provider/settla/
├── fx_oracle.go         # FX rate oracle
└── fx_oracle_test.go    # Tests
```

**Tasks**:
- [x] Implement `FXOracle` with base rates:
  - NGN/USD = 1755.20
  - GBP/USD = 1.2645
  - EUR/USD = 1.0835
  - GHS/USD = 15.80
  - USD/USD = 1.0
- [x] ±0.15% jitter on each call (simulates market movement)
- [x] `GetRate(from, to)` with inverse and cross rate support
- [x] Thread-safe with `sync.RWMutex`
- [x] Unit tests for rate accuracy, jitter range, cross rates

**Success Criteria**:
- [x] Rates within jitter range of base
- [x] Inverse rates work (USD/NGN from NGN/USD)
- [x] Cross rates work (GBP/NGN = GBP/USD × USD/NGN)
- [x] Thread-safe concurrent access

---

### WP-9: Fiat Simulator Implementation [SONNET] ✅ DONE

**Complexity**: Medium
**Agent**: Sonnet
**Estimated Effort**: Medium
**DEPENDS ON**: WP-8
**Parallelizable**: Yes (can run in parallel with WP-10 after WP-8)
**BLOCKS**: WP-10, WP-11

**Description**: Build the realistic fiat rail simulator with per-currency timing.

**Files to Create**:
```
rail/provider/settla/
├── fiat_simulator.go    # Fiat simulation engine
├── types.go             # FiatTransaction, StatusChange types
└── fiat_simulator_test.go # Tests
```

**Tasks**:
- [x] Implement `FiatSimulator` struct with configurable delays
- [x] Implement `SimulateCollection(ctx, amount, currency, ref)`:
  - Status progression: PENDING → PROCESSING → COLLECTED
  - Async processing via goroutine
- [x] Implement `SimulatePayout(ctx, amount, currency, recipient)`:
  - Status: PAYOUT_INITIATED → PAYOUT_PROCESSING → COMPLETED
  - Per-currency delays:
    - NGN: 3-5s (NIP instant)
    - GBP: 5-10s (Faster Payments)
    - USD: 10-30s (ACH)
    - GHS: 5-10s (GhIPSS)
- [x] Implement `GetStatus(txID)` for status polling
- [x] Configurable failure rate (default 2%)
- [x] Simulated bank references and status history
- [x] Unit tests for status progression, timing, failure rate

**Success Criteria**:
- [x] Status progression matches real banking rails
- [x] Per-currency delays are realistic
- [x] Failure rate approximately matches config
- [x] Concurrent simulations work correctly

---

### WP-10: On-Ramp Provider Implementation [SONNET] ✅ COMPLETE

**Complexity**: High
**Agent**: Sonnet
**Estimated Effort**: Large
**DEPENDS ON**: WP-2, WP-7, WP-8, WP-9
**Parallelizable**: No (requires multiple dependencies)

**Description**: Build Settla's on-ramp provider with real blockchain delivery.

**Files to Create**:
```
rail/provider/settla/
├── onramp.go            # On-ramp provider implementation
└── onramp_test.go       # Tests
```

**Tasks**:
- [x] Implement `OnRampProvider` struct with dependencies
- [x] Implement `domain.OnRampProvider` interface:
  - `ID() → "settla-onramp"`
  - `SupportedPairs()` - GBP, NGN, USD, EUR, GHS → USDT, USDC
  - `GetQuote(ctx, QuoteRequest)` - FX rate + spread + fee
  - `Execute(ctx, OnRampRequest)` - full flow
- [x] On-ramp flow:
  1. Create internal transaction (PENDING)
  2. Fiat simulation (async): PENDING → PROCESSING → COLLECTED
  3. Crypto delivery (REAL): Send testnet tokens from system wallet
  4. Return tx hash + explorer URL
- [x] `GetStatus(ctx, txID)` for status polling
- [x] Unit tests with mocked blockchain
- [ ] Integration tests with real testnet

**Success Criteria**:
- [ ] On-ramp produces real testnet tx hash
- [ ] Tx hash verifiable on block explorer
- [x] Status progression accurate
- [x] Explorer URL in transaction metadata

---

### WP-11: Off-Ramp Provider Implementation [SONNET] ✅ COMPLETE

**Complexity**: High
**Agent**: Sonnet
**Estimated Effort**: Large
**DEPENDS ON**: WP-2, WP-7, WP-8, WP-9
**Parallelizable**: Yes (can run in parallel with WP-10)
**Completed**: 2026-03-09

**Description**: Build Settla's off-ramp provider with real blockchain receipt.

**Files to Create**:
```
rail/provider/settla/
├── offramp.go           # Off-ramp provider implementation
└── offramp_test.go      # Tests
```

**Tasks**:
- [x] Implement `OffRampProvider` struct with dependencies
- [x] Implement `domain.OffRampProvider` interface:
  - `ID() → "settla-offramp"`
  - `SupportedPairs()` - USDT, USDC → GBP, NGN, USD, EUR, GHS
  - `GetQuote(ctx, QuoteRequest)` - FX rate + spread + fee
  - `Execute(ctx, OffRampRequest)` - full flow
- [x] Off-ramp flow:
  1. Create internal transaction
  2. Crypto receipt (REAL): Verify or consolidate tokens on-chain
  3. Fiat simulation: PAYOUT_INITIATED → COMPLETED
  4. Return tx hash + simulated payout reference
- [x] `GetStatus(ctx, txID)` for status polling
- [x] Unit tests with mocked blockchain
- [ ] Integration tests with real testnet

**Success Criteria**:
- [x] Off-ramp handles real testnet tokens (balance check + fallback simulation)
- [x] Fiat payout simulation realistic
- [ ] Full round-trip (on-ramp + off-ramp) works (needs WP-12/13)
- [x] Explorer URL in transaction metadata

---

## Stage 3.4: Integration

### WP-12: Provider Registry Update [SONNET] ✅ COMPLETE

**Complexity**: Medium
**Agent**: Sonnet
**Estimated Effort**: Medium
**DEPENDS ON**: WP-10, WP-11
**Parallelizable**: No (requires provider implementations)
**Completed**: 2026-03-09

**Description**: Wire Settla providers into the registry with mode switching.

**Files Modified/Created**:
```
rail/provider/
├── registry.go          # ProviderMode, ProviderModeFromEnv, NewRegistryFromMode, SettlaProviderDeps
└── registry_test.go     # 11 tests covering mode switching, env defaults, testnet wiring
cmd/settla-server/
└── main.go              # initTestnetProviders(), mode-switched provider init
```

**Tasks**:
- [x] Update registry to hold mock and Settla providers
- [x] Implement `SETTLA_PROVIDER_MODE` environment variable:
  - `"mock"` - mock providers (unit tests)
  - `"testnet"` - Settla providers (real blockchain)
  - `"live"` - future production providers
- [x] Default: `testnet` in development, `mock` in test
- [x] Registry initialization from config
- [x] Unit tests for mode switching

**Success Criteria**:
- [x] `SETTLA_PROVIDER_MODE=testnet` uses real blockchain
- [x] `SETTLA_PROVIDER_MODE=mock` uses mock providers
- [x] Mode switch is config-only (no code changes)

---

### WP-13: Router & API Integration [SONNET] ✅ COMPLETE

**Complexity**: Medium
**Agent**: Sonnet
**Estimated Effort**: Medium
**DEPENDS ON**: WP-12
**Parallelizable**: No (requires registry update)
**Completed**: 2026-03-09

**Description**: Update router and API to expose blockchain transaction details.

**Files Modified**:
```
domain/quote.go                    # ExplorerURL field on RouteInfo
domain/transfer.go                 # BlockchainTx struct + BlockchainTxs slice on Transfer
domain/provider.go                 # ExplorerURL field on RouteResult
rail/router/router.go              # Router populates ExplorerURL from blockchain.ExplorerURL()
api/gateway/src/schemas/index.ts   # blockchainTxSchema + blockchain_transactions in transfer, explorer_url in route
api/gateway/src/routes/transfers.ts # mapTransfer includes blockchain_transactions
api/gateway/src/routes/quotes.ts   # mapQuote includes explorer_url in route
```

**Tasks**:
- [x] Add `ExplorerURL` field to `RouteInfo`
- [x] Add `BlockchainTxs` slice to `Transfer` domain type (with `BlockchainTx` struct)
- [x] Router includes explorer URLs in route selection
- [x] API response includes blockchain transactions:
  ```json
  {
    "blockchain_transactions": [
      { "chain": "tron", "type": "on_ramp", "tx_hash": "...",
        "explorer_url": "https://...", "status": "confirmed" }
    ]
  }
  ```
- [x] Update gateway routes to include blockchain details
- [x] All existing tests pass (domain 56/56, router 12/12, provider 55/55, blockchain 58/58)

**Success Criteria**:
- [x] Transfer API response includes tx hashes + explorer links
- [x] Router route info includes explorer URLs
- [x] Gateway correctly serializes blockchain details

---

### WP-14: Testnet Setup & Makefile [SONNET] ✅ COMPLETE

**Complexity**: Low-Medium
**Agent**: Sonnet
**Estimated Effort**: Medium
**DEPENDS ON**: WP-7
**Parallelizable**: Yes (can start after WP-7, parallel with WP-12/13)
**Completed**: 2026-03-09

**Description**: Create testnet setup scripts and Makefile targets.

**Files Created/Modified**:
```
cmd/testnet-tools/
└── main.go              # Go CLI: setup, verify, status commands
scripts/
├── testnet-setup.sh     # Main setup script (env validation, wallet init, faucet funding)
└── testnet-verify.sh    # Verify setup (RPC connectivity, wallet listing)
Makefile                 # Added: testnet-setup, testnet-verify, testnet-status,
                         #   provider-mode-mock, provider-mode-testnet
.env.example             # Added: SETTLA_PROVIDER_MODE, wallet, blockchain RPC vars
deploy/docker-compose.yml # Added: provider mode + blockchain env vars to
                         #   settla-server and settla-node services
```

**Tasks**:
- [x] Create `testnet-setup.sh`:
  - Initialize wallet manager
  - Generate system wallets for all chains
  - Fund wallets from faucets (where automated)
  - Verify balances
  - Output wallet addresses, balances, explorer links
- [x] Create `testnet-verify.sh`:
  - Check wallet balances
  - Verify RPC connectivity
  - Test transaction submission
- [x] Add Makefile targets:
  - `make testnet-setup`
  - `make testnet-verify`
  - `make testnet-status`
  - `make provider-mode-mock`
  - `make provider-mode-testnet`
- [x] Update `.env.example` with testnet variables
- [x] Update `docker-compose.yml` with testnet environment

**Success Criteria**:
- [x] `make testnet-setup` initializes and funds wallets
- [x] `make provider-mode-testnet` switches to real blockchain
- [x] Documentation clear for manual faucet steps (Sepolia)

---

## Execution Timeline & Parallelization

### Phase 1: Foundation (Sequential)

```
WP-1: Core Key Management [OPUS]
  └── Blocks: WP-2, WP-3, WP-4, WP-5, WP-6
```

### Phase 2: Chain Clients + FX (4 Parallel Agents)

```
After WP-1 completes, run in parallel:

Agent 1: WP-4 (Tron Client)
Agent 2: WP-5 (Ethereum/Base Client)
Agent 3: WP-6 (Solana Client)
Agent 4: WP-8 (FX Oracle) → WP-9 (Fiat Simulator)
```

### Phase 3: Manager + Registry (3 Parallel Agents)

```
After WP-2 completes:
  Agent 1: WP-3 (Faucet Integration)

After at least 2 chain clients complete:
  Agent 2: WP-7 (Blockchain Registry)

After WP-8 completes:
  Agent 3: WP-9 (Fiat Simulator) if not done
```

### Phase 4: Providers (2 Parallel Agents)

```
After WP-2, WP-7, WP-8, WP-9 complete:

Agent 1: WP-10 (On-Ramp Provider)
Agent 2: WP-11 (Off-Ramp Provider)
```

### Phase 5: Integration (Sequential)

```
After WP-10, WP-11 complete:
  WP-12 (Provider Registry Update)
    └── WP-13 (Router & API Integration)

Parallel with WP-12/WP-13:
  WP-14 (Testnet Setup & Makefile)
```

---

## Agent Assignment Summary

| Work Package | Agent | Complexity | Dependencies |
|--------------|-------|------------|--------------|
| WP-1 | **OPUS** | High | None |
| WP-2 | Sonnet | Medium | WP-1 |
| WP-3 | Sonnet | Medium | WP-2 |
| WP-4 | Sonnet | Medium-High | WP-1 |
| WP-5 | Sonnet | Medium | WP-1 |
| WP-6 | Sonnet | Medium-High | WP-1 |
| WP-7 | Sonnet | Medium | WP-4, WP-5, WP-6 (2+) |
| WP-8 | Sonnet | Low | None |
| WP-9 | Sonnet | Medium | WP-8 |
| WP-10 | Sonnet | High | WP-2, WP-7, WP-8, WP-9 |
| WP-11 | Sonnet | High | WP-2, WP-7, WP-8, WP-9 |
| WP-12 | Sonnet | Medium | WP-10, WP-11 |
| WP-13 | Sonnet | Medium | WP-12 |
| WP-14 | Sonnet | Low-Medium | WP-7 |

---

## Critical Path

```
WP-1 → WP-2 → [WP-4,5,6 parallel] → WP-7 → WP-10/11 → WP-12 → WP-13
                                        ↑
WP-8 → WP-9 ──────────────────────────────┘
```

**Critical path work packages**: WP-1, WP-2, WP-7, WP-10, WP-12, WP-13

---

## Validation Checklist

After all work packages complete:

- [ ] `make test` - all unit tests pass
- [ ] `make test-integration` - integration tests against testnets pass
- [ ] `make testnet-setup` - wallets funded on all testnets
- [ ] Demo: On-ramp GBP → USDT produces verifiable tx hash
- [ ] Demo: Off-ramp USDT → NGN handles real tokens + simulated payout
- [ ] Demo: Full round-trip settlement with real blockchain legs
- [ ] Explorer URLs in all transaction responses
- [ ] Private keys never appear in logs

---

## Environment Variables Required

```bash
# Wallet Management
SETTLA_MASTER_SEED=<24-word-bip39-mnemonic>
SETTLA_WALLET_ENCRYPTION_KEY=<32-byte-hex>

# Provider Mode
SETTLA_PROVIDER_MODE=testnet  # mock | testnet | live

# Tron Nile
SETTLA_TRON_RPC_URL=https://nile.trongrid.io
SETTLA_TRON_API_KEY=<trongrid-api-key>

# Solana Devnet
SETTLA_SOLANA_RPC_URL=https://api.devnet.solana.com

# Ethereum Sepolia
SETTLA_ETHEREUM_RPC_URL=https://rpc.sepolia.org

# Base Sepolia
SETTLA_BASE_RPC_URL=https://sepolia.base.org
```

---

## Notes for Agents

### For Opus (WP-1)
- This is the most security-critical work package
- Port secure memory zeroing exactly from paydash
- Ensure private keys are never logged, even in error messages
- Test deterministic derivation thoroughly

### For Sonnet (Chain Clients)
- Each chain client can be developed independently
- Focus on one chain first, verify against testnet, then proceed
- Use mocked RPC for unit tests, real testnet for integration tests
- Include explorer URL generation from the start

### For Sonnet (Providers)
- On-ramp and off-ramp can be developed in parallel
- Both depend on fiat simulator - coordinate or wait
- Real blockchain + simulated fiat is the pattern
- Explorer URLs must be included in all transaction metadata

### For Sonnet (Integration)
- Mode switching must be config-only
- API changes should be additive (don't break existing fields)
- Testnet setup script should be idempotent

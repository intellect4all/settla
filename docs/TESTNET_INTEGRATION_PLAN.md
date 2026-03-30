# Testnet Stablecoin Integration Plan

**Date:** 2026-03-30
**Author:** Investigation by Claude (Senior Blockchain Integration Engineer)
**Status:** Investigation Complete — Ready for Implementation

---

## Executive Summary

**The blockchain integration is 85-90% complete.** This is NOT a "fully mocked" codebase — Settla has production-grade blockchain clients for all four chains (Tron, Ethereum/Base, Solana) with real RPC calls, real transaction building, real signing infrastructure, and real chain monitoring. The testnet defaults are already configured (Nile, Sepolia, Base Sepolia, Devnet).

**What's missing is wiring, not code.** The blockchain registry creates read-only clients (`nil` signers) because the wallet manager isn't connected to the blockchain clients at startup. The fix is primarily configuration and ~150 lines of wiring code in `cmd/settla-server/main.go`.

### Estimated Total Effort: 2-3 days (not weeks)

| Area | Status | Effort |
|------|--------|--------|
| Blockchain clients (RPC, tx building, signing) | **Complete** | 0 |
| Wallet management (HD, encryption, derivation) | **Complete** | 0 |
| Chain monitor (polling, confirmations, deposits) | **Complete** | 0 |
| Faucet system (Tron/Solana automated) | **Complete** | 0 |
| Explorer URL generation | **Complete** | 0 |
| Signing wiring (connect wallet → clients) | **Missing** | 4-6 hours |
| Alchemy RPC configuration | **Missing** | 1-2 hours |
| Wallet funding (faucets + test tokens) | **Missing** | 2-3 hours |
| End-to-end verification | **Missing** | 4-6 hours |
| Explorer URL wiring to router | **Missing** | 1 hour |

---

## 1. Investigation Findings

### 1.1 Current Architecture

#### Wallet Management — PRODUCTION GRADE
**Package:** `rail/wallet/` (6 files, ~1,300 lines)

| File | Lines | Purpose |
|------|-------|---------|
| `rail/wallet/types.go` | 219 | `Wallet` struct, `EncryptedWallet`, `WalletStore` interface, chain constants |
| `rail/wallet/manager.go` | 528 | Manager lifecycle, derivation, signing, persistence, caching |
| `rail/wallet/derivation.go` | 269 | BIP-44 derivation for all 4 chains, address generation |
| `rail/wallet/store.go` | 223 | File-based encrypted wallet storage (JSON, 0700 permissions) |
| `rail/wallet/keymgmt/manager.go` | 384 | KeyManager interface, FileStore, EnvStore for master seed |
| `rail/wallet/keymgmt/encryption.go` | 67 | AES-256-GCM encrypt/decrypt |
| `rail/wallet/keymgmt/secure.go` | 76 | Secure memory zeroing (unsafe pointer, runtime.KeepAlive) |
| `rail/wallet/faucet.go` | 122 | Testnet faucet funding (automated for Tron/Solana) |

**Key details:**
- **BIP-44 HD wallets** with standard derivation paths:
  - Ethereum/Base: `m/44'/60'/0'/0/{index}`
  - Tron: `m/44'/195'/0'/0/{index}`
  - Solana: `m/44'/501'/0'/0/{index}`
- **AES-256-GCM encryption** at rest for all private keys
- **Secure zeroing** of intermediate BIP-32 keys via `defer` blocks with `unsafe` pointer arithmetic
- **Private keys never exported** — unexported `privateKey` field, typed accessors (`ECDSAPrivateKey()`, `Ed25519PrivateKey()`)
- **Wallet paths:** `system/hot/{chain}`, `system/offramp/{chain}/{txRef}`, `tenant/{slug}/{chain}`
- **Signing methods:**
  - `GetPrivateKeyForSigning(walletPath) (*ecdsa.PrivateKey, error)` — EVM/Tron
  - `GetEd25519KeyForSigning(walletPath) (ed25519.PrivateKey, error)` — Solana
  - `SignTransaction(ctx, walletPath, txHash) ([]byte, error)` — generic

**Config via environment:**
- `SETTLA_WALLET_ENCRYPTION_KEY` — 64-char hex (32 bytes AES-256)
- `SETTLA_MASTER_SEED` — 128-char hex (64 bytes BIP-39 seed)
- `SETTLA_WALLET_STORAGE_PATH` — directory for encrypted wallet files (default: `.settla/wallets`)

#### Chain Abstraction — COMPLETE

**Interface** (`domain/provider.go:175-189`):
```go
type BlockchainClient interface {
    Chain() CryptoChain
    GetBalance(ctx context.Context, address string, token string) (decimal.Decimal, error)
    EstimateGas(ctx context.Context, req TxRequest) (decimal.Decimal, error)
    SendTransaction(ctx context.Context, req TxRequest) (*ChainTx, error)
    GetTransaction(ctx context.Context, hash string) (*ChainTx, error)
    SubscribeTransactions(ctx context.Context, address string, ch chan<- ChainTx) error
}
```

**Implementations exist for all 4 chains:**

| Chain | Package | Client | Signer Interface | Testnet Config |
|-------|---------|--------|------------------|----------------|
| Ethereum | `rail/blockchain/ethereum/` | `Client` | `WalletSigner` (ECDSA) | `SepoliaConfig` (chain 11155111) |
| Base | `rail/blockchain/ethereum/` | `Client` (reused) | `WalletSigner` (ECDSA) | `BaseSepoliaConfig` (chain 84532) |
| Tron | `rail/blockchain/tron/` | `Client` | Direct `*wallet.Manager` | `NileConfig` |
| Solana | `rail/blockchain/solana/` | `Client` | `WalletSigner` (Ed25519) | `DevnetConfig` |

#### Transaction Building — REAL, NOT MOCKED

**Ethereum/Base** (`rail/blockchain/ethereum/client.go:223-290`):
- Real `go-ethereum` `ethclient` RPC calls
- ERC-20 `transfer(address,uint256)` ABI encoding (selector `0xa9059cbb`)
- Gas price estimation via `eth_gasPrice`
- Nonce management with per-address `sync.Map` and local increment
- Transaction signing via `types.SignTx()` with `LatestSignerForChainID`
- Broadcasting via `eth_sendRawTransaction`

**Tron** (`rail/blockchain/tron/client.go:176-255`):
- HTTP API calls to TronGrid (`/wallet/triggersmartcontract`, `/wallet/broadcasttransaction`)
- TRC-20 ABI encoding (same selector, Tron hex address format)
- Energy/bandwidth estimation: `energyPriceSUN = 280`, `trc20TransferEnergy = 30,000`
- Fee limit: `defaultFeeLimit = 100,000,000 SUN` (100 TRX)
- SHA-256 transaction hash → ECDSA signature → broadcast

**Solana** (`rail/blockchain/solana/transaction.go:30-128`):
- Uses `gagliardetto/solana-go` library
- SPL `TransferChecked` instruction building
- Automatic ATA (Associated Token Account) creation for recipients
- Rent-exempt balance handling (`ataRentExemptLamports = 2,039,280`)
- Ed25519 signing via `tx.Sign()`
- Latest blockhash fetching for transaction lifetime

#### RPC Configuration — TESTNET DEFAULTS ALREADY SET

**File:** `rail/blockchain/config.go`

```go
// BlockchainConfig with testnet defaults
type BlockchainConfig struct {
    TronRPCURL     string  // default: "https://nile.trongrid.io"
    TronAPIKey     string
    EthereumRPCURL string  // default: "https://rpc.sepolia.org"
    BaseRPCURL     string  // default: "https://sepolia.base.org"
    SolanaRPCURL   string  // default: "https://api.devnet.solana.com"
}
```

Environment variables: `SETTLA_TRON_RPC_URL`, `SETTLA_ETHEREUM_RPC_URL`, `SETTLA_BASE_RPC_URL`, `SETTLA_SOLANA_RPC_URL`

#### Chain Monitor — FULLY FUNCTIONAL FOR REAL CHAINS

**Package:** `node/chainmonitor/` (11 files, ~1,500 lines)

| Component | File | Capability |
|-----------|------|------------|
| EVM Poller | `evm_poller.go` | Real `eth_getLogs` scanning for ERC-20 Transfer events, reorg detection, checkpoint tracking |
| Tron Poller | `tron_poller.go` | Real TRC-20 transfer API scanning per watched address, timestamp filtering |
| Token Registry | `token_registry.go` | Lock-free atomic snapshot for token contract lookups |
| Address Set | `address_set.go` | Lock-free atomic snapshot for watched deposit addresses |
| Checkpoint Manager | `checkpoint.go` | Block number/hash persistence for crash recovery |
| RPC Failover | `rpc/failover.go` | Multi-provider failover with circuit breakers and rate limiters |

**Already configured for testnets in `cmd/settla-node/main.go:798-883`:**
- Creates pollers per chain from env vars (`SETTLA_ETH_RPC_URL`, `SETTLA_BASE_RPC_URL`, `SETTLA_TRON_RPC_URL`)
- Supports primary + backup RPC endpoints per chain
- Confirmation thresholds: Ethereum/Base = 12, Tron = 19

#### Token Contract Addresses — HARDCODED TESTNET ADDRESSES

| Chain | Token | Contract Address | Source File |
|-------|-------|-----------------|-------------|
| Ethereum Sepolia | USDC | `0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238` | `ethereum/types.go:68` |
| Base Sepolia | USDC | `0x036CbD53842c5426634e7929541eC2318f3dCF7e` | `ethereum/types.go:84` |
| Tron Nile | USDT | `TXLAQ63Xg1NAzckPwKHvzw7CSEmLMEqcdj` | `tron/types.go:37` |
| Solana Devnet | USDC | `4zMMC9srt5Ri5X14GAgXhaHii3GnPAEERYPJgZJDncDU` | `solana/types.go:89` |

#### Explorer URLs — HARDCODED TESTNET URLS

**File:** `rail/blockchain/explorer.go:19-32`

| Chain | Explorer URL Pattern |
|-------|---------------------|
| Tron | `https://nile.tronscan.org/#/transaction/{txHash}` |
| Ethereum | `https://sepolia.etherscan.io/tx/{txHash}` |
| Base | `https://sepolia.basescan.org/tx/{txHash}` |
| Solana | `https://explorer.solana.com/tx/{sig}?cluster=devnet` |

**Gap:** Explorer URL provider is not wired to the router (`router.go:77-79` has `WithExplorerUrl()` option but it's not called in `main.go`).

#### Mock vs Real Boundary — CLEAN MODE SWITCH

**File:** `rail/provider/registry.go:14-47`

```go
const (
    ProviderModeMock     ProviderMode = "mock"      // In-memory, instant, synthetic hashes
    ProviderModeMockHTTP ProviderMode = "mock-http"  // HTTP mock service
    ProviderModeTestnet  ProviderMode = "testnet"    // Real testnet blockchains
    ProviderModeLive     ProviderMode = "live"       // Production (reserved)
)
```

**Decision point** (`cmd/settla-server/main.go:565-575`):
```go
providerMode := provider.ModeFromEnv()  // reads SETTLA_PROVIDER_MODE

if providerMode == provider.ProviderModeTestnet || providerMode == provider.ProviderModeLive {
    chainCfg := blockchain.LoadConfigFromEnv()
    chainReg, err = blockchain.NewRegistryFromConfig(chainCfg, logger)  // ← creates REAL clients
}
```

**The critical gap** (`rail/blockchain/registry.go:47-86`):
```go
// ALL clients created with nil signers (READ-ONLY):
r.Register(tron.NewClient(tronCfg, nil, logger))           // line 54
r.Register(ethereum.NewClient(ethCfg, nil, logger))         // line 61
r.Register(ethereum.NewClient(baseCfg, nil, logger))        // line 72
r.Register(solana.New(solCfg, nil))                         // line 83
```

**This is the #1 thing to fix.** The blockchain clients are fully capable of signing and broadcasting — they just aren't given signers at construction time.

### 1.2 Dependencies

All required blockchain libraries are already in `go.mod`:

| Dependency | Version | Purpose | Testnet Support |
|-----------|---------|---------|-----------------|
| `github.com/ethereum/go-ethereum` | v1.17.1 | EVM chains (Ethereum, Base) — ethclient, types, crypto, ABI | Full testnet support |
| `github.com/gagliardetto/solana-go` | v1.14.0 | Solana — RPC client, transaction building, SPL token | Full devnet support |
| `github.com/tyler-smith/go-bip32` | v1.0.0 | BIP-32 HD key derivation | N/A (local crypto) |
| `github.com/tyler-smith/go-bip39` | v1.1.0 | BIP-39 mnemonic seed generation | N/A (local crypto) |
| `github.com/shopspring/decimal` | v1.4.0 | Decimal arithmetic for monetary amounts | N/A |
| `github.com/btcsuite/btcd/btcutil` | v1.1.6 | Base58 encoding (Tron addresses) | N/A |

**No Tron-specific SDK** — Tron integration uses direct HTTP API calls to TronGrid. This is intentional and avoids the poorly-maintained `gotron-sdk`.

**No new dependencies needed.**

### 1.3 Configuration Audit

**Blockchain-related environment variables (from `.env.example` and `cmd/settla-node/main.go`):**

| Variable | Purpose | Default |
|----------|---------|---------|
| `SETTLA_PROVIDER_MODE` | mock/testnet/live mode selection | `testnet` (non-test builds) |
| `SETTLA_TRON_RPC_URL` | Tron RPC endpoint | `https://nile.trongrid.io` |
| `SETTLA_TRON_API_KEY` | TronGrid API key (rate limits) | empty |
| `SETTLA_ETHEREUM_RPC_URL` | Ethereum RPC endpoint | `https://rpc.sepolia.org` |
| `SETTLA_BASE_RPC_URL` | Base RPC endpoint | `https://sepolia.base.org` |
| `SETTLA_SOLANA_RPC_URL` | Solana RPC endpoint | `https://api.devnet.solana.com` |
| `SETTLA_ETH_RPC_URL` | Chain monitor Ethereum RPC | (unset = poller disabled) |
| `SETTLA_ETH_RPC_API_KEY` | Chain monitor Ethereum API key | empty |
| `SETTLA_ETH_RPC_BACKUP_URL` | Chain monitor Ethereum failover | empty |
| `SETTLA_BASE_RPC_URL` | Chain monitor Base RPC | (unset = poller disabled) |
| `SETTLA_BASE_RPC_API_KEY` | Chain monitor Base API key | empty |
| `SETTLA_BASE_RPC_BACKUP_URL` | Chain monitor Base failover | empty |
| `SETTLA_WALLET_ENCRYPTION_KEY` | 64-char hex for AES-256 wallet encryption | required |
| `SETTLA_MASTER_SEED` | 128-char hex BIP-39 master seed | required |
| `SETTLA_WALLET_STORAGE_PATH` | Encrypted wallet file directory | `.settla/wallets` |

### 1.4 Mock vs Real Boundary

The boundary is a **clean, mode-based switch** at the provider factory level:

```
SETTLA_PROVIDER_MODE=mock     → mock.BlockchainClient (synthetic hashes, instant confirm)
SETTLA_PROVIDER_MODE=testnet  → real blockchain clients (Nile, Sepolia, Devnet)
```

Mock providers are in `rail/provider/mock/` and register themselves for mode `"mock"` via `init()`.
Real providers are in `rail/provider/settla/` and register themselves for mode `"testnet"` via `init()`.

**No mock code is mixed with real code** — they are separate packages loaded by the factory pattern.

### 1.5 What Already Works vs What Needs Building

#### Already Works (No Changes Needed)
- Real EVM RPC client with `go-ethereum` (`rail/blockchain/ethereum/rpc.go`)
- Real Tron HTTP API client (`rail/blockchain/tron/rpc.go`)
- Real Solana RPC client with `solana-go` (`rail/blockchain/solana/rpc.go`)
- ERC-20 transfer encoding and broadcasting (`ethereum/client.go:223-290`)
- TRC-20 transfer encoding and broadcasting (`tron/client.go:176-255`)
- SPL token transfer with ATA creation (`solana/transaction.go:30-128`)
- Transaction signing for all chains (ECDSA for EVM/Tron, Ed25519 for Solana)
- HD wallet derivation (BIP-44) for all 4 chains
- Private key encryption/decryption (AES-256-GCM)
- Chain monitor polling (EVM + Tron)
- Reorg detection and confirmation tracking
- Testnet token contract addresses (USDC Sepolia, USDT Nile, USDC Devnet)
- Explorer URL generation for all testnets
- Faucet system (automated for Tron/Solana)
- RPC failover with circuit breakers

#### Needs Building/Wiring
1. **Wallet manager → blockchain client wiring** (~100 lines in `main.go`)
2. **Wallet address registration** with blockchain clients (`RegisterWallet()` calls)
3. **Explorer URL → router wiring** (~5 lines in `main.go`)
4. **Alchemy RPC URLs** in `.env` configuration
5. **Wallet funding** (faucets for gas, test token minting/obtaining)
6. **Solana chain monitor** (currently only EVM and Tron pollers exist in `cmd/settla-node/main.go`)

---

## 2. Network-Specific Plans

### 2.1 Base Sepolia (EVM — Alchemy Supported)

**Network details:**
- Chain ID: 84532
- RPC: `https://base-sepolia.g.alchemy.com/v2/{ALCHEMY_API_KEY}`
- Block explorer: https://sepolia.basescan.org
- Native gas token: ETH (Base Sepolia ETH)
- USDC contract: `0x036CbD53842c5426634e7929541eC2318f3dCF7e` (already hardcoded in `ethereum/types.go:84`)
- Gas faucet: https://www.alchemy.com/faucets/base-sepolia or https://www.coinbase.com/faucets/base-ethereum-goerli-faucet

**Current state — 95% complete:**
- `ethereum.Client` with `BaseSepoliaConfig` already exists (`ethereum/types.go:78-91`)
- ERC-20 transfer building, signing, broadcasting all implemented
- Chain monitor EVM poller works with Base Sepolia
- Explorer URL: `https://sepolia.basescan.org/tx/{hash}` already configured

**What needs to happen:**

1. **RPC configuration** — Set `SETTLA_BASE_RPC_URL=https://base-sepolia.g.alchemy.com/v2/{KEY}` in `.env`
2. **Create signing-capable client** — In `registry.go`, pass a `WalletSigner` instead of `nil`:
   ```go
   // rail/blockchain/registry.go:67-76 — CHANGE FROM:
   r.Register(ethereum.NewClient(baseCfg, nil, logger))
   // TO:
   baseSigner := ethereum.NewWalletSigner(walletMgr, big.NewInt(84532))
   baseClient := ethereum.NewClient(baseCfg, baseSigner, logger)
   r.Register(baseClient)
   ```
3. **Register system wallet** — After wallet manager creates/loads the Base hot wallet:
   ```go
   baseWallet, _ := walletMgr.GetSystemWallet(wallet.ChainBase)
   baseSigner.RegisterWallet(baseWallet.Address, baseWallet.Path)
   ```
4. **Fund wallet with Base Sepolia ETH** — Use Alchemy faucet (0.5 ETH per day)
5. **Obtain test USDC** — The Circle USDC on Base Sepolia (`0x036CbD53842c5426634e7929541eC2318f3dCF7e`) is the official faucet-mintable USDC. Use the Circle faucet at https://faucet.circle.com/ to mint USDC to the hot wallet address.
6. **Confirmation time** — Base Sepolia: ~2 seconds per block, 12 confirmations = ~24 seconds. Fast enough for demo.

**Code changes needed:**

| File | Change | Lines |
|------|--------|-------|
| `rail/blockchain/registry.go` | Accept `*wallet.Manager`, create signing-capable Base client | 67-76 |
| `cmd/settla-server/main.go` | Create wallet manager, pass to registry, register wallets | ~565-610 |
| `.env.example` | Add `SETTLA_BASE_RPC_URL` Alchemy endpoint | append |

### 2.2 Tron Nile Testnet

**Network details:**
- Network: Nile testnet
- RPC: `https://nile.trongrid.io` (free, already configured as default)
- Block explorer: https://nile.tronscan.org
- Native gas token: TRX (test TRX)
- USDT TRC-20 contract: `TXLAQ63Xg1NAzckPwKHvzw7CSEmLMEqcdj` (already hardcoded in `tron/types.go:37`)
- Faucet: https://nileex.io/join/getJoinPage (1000 TRX per request)

**Alchemy Tron support:** Alchemy does **NOT** support Tron. TronGrid (free tier) is the primary provider. No change needed — the existing `https://nile.trongrid.io` endpoint works without authentication for basic usage. For higher rate limits, register for a free TronGrid API key.

**Current state — 95% complete:**
- `tron.Client` with `NileConfig` already exists (`tron/types.go:33-40`)
- TRC-20 transfer building, signing, broadcasting all implemented
- Chain monitor Tron poller works with Nile
- Explorer URL: `https://nile.tronscan.org/#/transaction/{hash}` already configured

**What needs to happen:**

1. **RPC configuration** — Default `https://nile.trongrid.io` already works. Optionally set `SETTLA_TRON_API_KEY` for higher rate limits.
2. **Create signing-capable client** — In `registry.go`, pass `walletMgr` instead of `nil`:
   ```go
   // rail/blockchain/registry.go:47-54 — CHANGE FROM:
   r.Register(tron.NewClient(tronCfg, nil, logger))
   // TO:
   r.Register(tron.NewClient(tronCfg, walletMgr, logger))
   ```
3. **Fund wallet with test TRX** — Use Nile faucet (automated, `rail/wallet/faucet.go` already supports this):
   ```go
   faucet, _ := wallet.NewFaucet(wallet.ChainTron, wallet.FaucetConfig{})
   faucet.Fund(ctx, tronWallet.Address)  // Requests 1000 TRX from Nile
   ```
4. **Obtain test USDT** — The Nile USDT contract `TXLAQ63Xg1NAzckPwKHvzw7CSEmLMEqcdj` is a test token. Test USDT can be obtained by:
   - Using the Nile testnet USDT faucet (if available on nileex.io)
   - Swapping test TRX for test USDT on Nile DEX
   - Deploying a custom TRC-20 with `mint()` function as fallback
5. **Energy/bandwidth** — TRC-20 transfers require ~30,000 energy units. With 1000 test TRX, this covers ~100+ transfers. The code already handles energy costs (`tron/client.go:24-27`).
6. **Confirmation time** — Tron: 3 seconds per block, 19 confirmations = ~57 seconds. Acceptable for demo (mention "confirming on-chain" during the wait).

**Code changes needed:**

| File | Change | Lines |
|------|--------|-------|
| `rail/blockchain/registry.go` | Pass `walletMgr` to `tron.NewClient()` | 47-54 |
| `cmd/settla-server/main.go` | Create wallet manager, pass to registry builder | ~565-610 |

### 2.3 Solana Devnet

**Network details:**
- Network: Devnet
- RPC: `https://solana-devnet.g.alchemy.com/v2/{ALCHEMY_API_KEY}` (Alchemy supports Solana Devnet)
- Block explorer: https://explorer.solana.com/?cluster=devnet
- Native gas token: SOL (devnet SOL)
- USDC mint: `4zMMC9srt5Ri5X14GAgXhaHii3GnPAEERYPJgZJDncDU` (Circle's official devnet USDC, already hardcoded in `solana/types.go:89`)
- Faucet: `solana airdrop 2` CLI or https://faucet.solana.com (2 SOL per airdrop, automated via `rail/wallet/faucet.go`)

**Alchemy Solana support:** Yes, Alchemy supports Solana Devnet. Use `https://solana-devnet.g.alchemy.com/v2/{KEY}`.

**Current state — 90% complete:**
- `solana.Client` with `DevnetConfig` already exists (`solana/types.go:83-93`)
- SPL token transfer with ATA creation all implemented
- Explorer URL: `https://explorer.solana.com/tx/{sig}?cluster=devnet` already configured
- **Gap:** No Solana poller in chain monitor (only EVM and Tron pollers exist)

**What needs to happen:**

1. **RPC configuration** — Set `SETTLA_SOLANA_RPC_URL=https://solana-devnet.g.alchemy.com/v2/{KEY}` in `.env`
2. **Create signing-capable client** — In `registry.go`, pass signer instead of `nil`:
   ```go
   // rail/blockchain/registry.go:78-83 — CHANGE FROM:
   r.Register(solana.New(solCfg, nil))
   // TO:
   r.Register(solana.New(solCfg, walletMgr))
   ```
3. **Register system wallet:**
   ```go
   solWallet, _ := walletMgr.GetSystemWallet(wallet.ChainSolana)
   solClient.RegisterWallet(solWallet.Address, solWallet.Path)
   ```
4. **Fund wallet with devnet SOL** — Automated via faucet system:
   ```go
   faucet, _ := wallet.NewFaucet(wallet.ChainSolana, wallet.FaucetConfig{})
   faucet.Fund(ctx, solWallet.Address)  // Airdrops 2 SOL
   ```
5. **Obtain test USDC** — Circle's devnet USDC (`4zMMC9srt5Ri5X14GAgXhaHii3GnPAEERYPJgZJDncDU`) can be obtained via:
   - Circle's USDC faucet at https://faucet.circle.com/ (select Solana Devnet)
   - spl-token CLI: `spl-token transfer` from a pre-funded account
6. **Rent handling** — Already implemented. `ataRentExemptLamports = 2,039,280` (~0.002 SOL) is automatically paid when creating recipient ATAs (`solana/transaction.go:75-81`).
7. **Commitment levels** — Already configured: `CommitmentConfirmed` in `DevnetConfig` (`solana/types.go:92`). Confirmed = ~400ms (single slot). 32 slots for full finality = ~12.8 seconds.
8. **Chain monitor gap** — No Solana poller exists in `cmd/settla-node/main.go`. For deposit detection on Solana, a new poller would need to be created. However, for **outbound transfers** (the primary demo use case), the chain monitor is not needed — only `SendTransaction()` + `GetTransaction()` are required.

**Code changes needed:**

| File | Change | Lines |
|------|--------|-------|
| `rail/blockchain/registry.go` | Pass `walletMgr` to `solana.New()` | 78-83 |
| `cmd/settla-server/main.go` | Register Solana wallet after creation | ~565-610 |
| `.env.example` | Add Alchemy Solana Devnet RPC URL | append |

### 2.4 Cross-Chain Configuration Design

The existing configuration system (`rail/blockchain/config.go`) already supports per-chain RPC URLs via environment variables. The recommended `.env` additions:

```bash
# ── Provider Mode ──
SETTLA_PROVIDER_MODE=testnet

# ── Wallet Management ──
SETTLA_WALLET_ENCRYPTION_KEY=<64-char-hex>    # Generate: openssl rand -hex 32
SETTLA_MASTER_SEED=<128-char-hex>             # Generate: openssl rand -hex 64
SETTLA_WALLET_STORAGE_PATH=.settla/wallets

# ── Base Sepolia (EVM) ──
SETTLA_BASE_RPC_URL=https://base-sepolia.g.alchemy.com/v2/<ALCHEMY_API_KEY>
# Chain ID 84532 (hardcoded in ethereum/types.go:80)
# USDC: 0x036CbD53842c5426634e7929541eC2318f3dCF7e (hardcoded in ethereum/types.go:84)
# Explorer: https://sepolia.basescan.org (hardcoded in explorer.go)

# ── Tron Nile ──
SETTLA_TRON_RPC_URL=https://nile.trongrid.io
SETTLA_TRON_API_KEY=<optional-trongrid-api-key>
# USDT: TXLAQ63Xg1NAzckPwKHvzw7CSEmLMEqcdj (hardcoded in tron/types.go:37)
# Explorer: https://nile.tronscan.org (hardcoded in explorer.go)

# ── Solana Devnet ──
SETTLA_SOLANA_RPC_URL=https://solana-devnet.g.alchemy.com/v2/<ALCHEMY_API_KEY>
# USDC mint: 4zMMC9srt5Ri5X14GAgXhaHii3GnPAEERYPJgZJDncDU (hardcoded in solana/types.go:89)
# Explorer: https://explorer.solana.com/?cluster=devnet (hardcoded in explorer.go)

# ── Chain Monitor (settla-node) ──
SETTLA_ETH_RPC_URL=https://eth-sepolia.g.alchemy.com/v2/<ALCHEMY_API_KEY>
SETTLA_ETH_RPC_BACKUP_URL=https://rpc.sepolia.org
SETTLA_BASE_RPC_URL=https://base-sepolia.g.alchemy.com/v2/<ALCHEMY_API_KEY>
SETTLA_BASE_RPC_BACKUP_URL=https://sepolia.base.org
```

**Per-chain confirmation thresholds** are already configurable via `ChainConfig` struct (`node/chainmonitor/config.go`). Testnet defaults: Ethereum/Base = 12, Tron = 19, Solana = 32 slots.

**Per-chain hot wallet addresses** are automatically derived from the master seed — different addresses for each chain due to BIP-44 coin type differentiation. The same master seed MUST be used consistently (never rotated) to maintain address determinism.

---

## 3. Cross-Chain Configuration Design

The existing architecture already supports the cross-chain configuration pattern. Token contracts and explorer URLs are hardcoded for testnet in the chain-specific config structs. For mainnet migration, these would need to be made configurable:

**Current (testnet-only, hardcoded):**
```go
// ethereum/types.go
BaseSepoliaConfig = Config{
    ChainID:       84532,
    RPCURL:        "https://sepolia.base.org",
    USDCContract:  "0x036CbD53842c5426634e7929541eC2318f3dCF7e",
    Confirmations: 12,
}
```

**Future (configurable per network):** Would require environment variable overrides for contract addresses, which is a mainnet concern — not needed for the testnet demo.

---

## 4. Implementation Roadmap

### 4.1 Dependency Changes

**None required.** All blockchain libraries are already in `go.mod`:
- `go-ethereum v1.17.1` — EVM chains
- `solana-go v1.14.0` — Solana
- `go-bip32 v1.0.0` + `go-bip39 v1.1.0` — HD wallets

### 4.2 Code Change Inventory

#### Change 1: Registry accepts wallet manager for signing

**File:** `rail/blockchain/registry.go`
**Action:** Modify `NewRegistryFromConfig` to accept `*wallet.Manager` and create signing-capable clients

```
Current (line 43):
  func NewRegistryFromConfig(cfg BlockchainConfig, logger *slog.Logger) (*Registry, error)

Change to:
  func NewRegistryFromConfig(cfg BlockchainConfig, walletMgr *wallet.Manager, logger *slog.Logger) (*Registry, error)

Lines 47-54 (Tron): Change nil → walletMgr
  tron.NewClient(tronCfg, nil, logger)  →  tron.NewClient(tronCfg, walletMgr, logger)

Lines 56-65 (Ethereum): Create WalletSigner, pass to NewClient
  ethereum.NewClient(ethCfg, nil, logger)
  →
  ethSigner := ethereum.NewWalletSigner(walletMgr, big.NewInt(int64(ethCfg.ChainID)))
  ethereum.NewClient(ethCfg, ethSigner, logger)

Lines 67-76 (Base): Same pattern as Ethereum
  ethereum.NewClient(baseCfg, nil, logger)
  →
  baseSigner := ethereum.NewWalletSigner(walletMgr, big.NewInt(int64(baseCfg.ChainID)))
  ethereum.NewClient(baseCfg, baseSigner, logger)

Lines 78-83 (Solana): Pass walletMgr as signer
  solana.New(solCfg, nil)  →  solana.New(solCfg, walletMgr)
```

**New method — register system wallets after registry creation:**
```go
func (r *Registry) RegisterSystemWallets(walletMgr *wallet.Manager) error {
    chains := []wallet.Chain{wallet.ChainTron, wallet.ChainEthereum, wallet.ChainBase, wallet.ChainSolana}
    for _, chain := range chains {
        w, err := walletMgr.GetSystemWallet(chain)
        if err != nil {
            return fmt.Errorf("creating %s system wallet: %w", chain, err)
        }
        client, err := r.GetClient(domain.CryptoChain(chain.String()))
        if err != nil {
            continue // chain not configured
        }
        // Register wallet address with the client's signer
        if registrar, ok := client.(interface{ RegisterWallet(string, string) }); ok {
            registrar.RegisterWallet(w.Address, w.Path)
        }
        r.logger.Info("registered system wallet", "chain", chain, "address", w.Address)
    }
    return nil
}
```

#### Change 2: Main server creates wallet manager and wires to registry

**File:** `cmd/settla-server/main.go`
**Action:** Add wallet manager creation before blockchain registry initialization (~line 560)

```go
// NEW: Create wallet manager for signing
var walletMgr *wallet.Manager
if providerMode == provider.ProviderModeTestnet || providerMode == provider.ProviderModeLive {
    walletMgr, err = wallet.NewManager(wallet.ManagerConfig{
        MasterSeed:    decodeSeedFromEnv("SETTLA_MASTER_SEED"),
        EncryptionKey: os.Getenv("SETTLA_WALLET_ENCRYPTION_KEY"),
        StoragePath:   getEnvOr("SETTLA_WALLET_STORAGE_PATH", ".settla/wallets"),
        KeyID:         "settla-master",
    })
    if err != nil {
        logger.Error("failed to create wallet manager", "error", err)
        os.Exit(1)
    }
    defer walletMgr.Close()
}

// EXISTING (modified): Create blockchain registry with signing capability
if providerMode == provider.ProviderModeTestnet || providerMode == provider.ProviderModeLive {
    chainCfg := blockchain.LoadConfigFromEnv()
    chainReg, err = blockchain.NewRegistryFromConfig(chainCfg, walletMgr, logger)  // ← pass walletMgr
    if err != nil { ... }

    // NEW: Register system wallets for all chains
    if err := chainReg.RegisterSystemWallets(walletMgr); err != nil {
        logger.Warn("failed to register some system wallets", "error", err)
    }
}
```

#### Change 3: Wire explorer URL to router

**File:** `cmd/settla-server/main.go`
**Action:** Add `WithExplorerUrl()` option when creating router (~line 620+)

```go
routerOpts := []router.Option{
    // ... existing options ...
}
if chainReg != nil {
    routerOpts = append(routerOpts, router.WithExplorerUrl(blockchain.ExplorerURL))
}
r := router.New(providerReg, routerOpts...)
```

#### Change 4: Update .env.example with Alchemy endpoints

**File:** `.env.example`
**Action:** Add Alchemy RPC URLs and wallet config

```bash
# Blockchain RPC (Alchemy)
SETTLA_BASE_RPC_URL=https://base-sepolia.g.alchemy.com/v2/YOUR_KEY
SETTLA_SOLANA_RPC_URL=https://solana-devnet.g.alchemy.com/v2/YOUR_KEY
SETTLA_ETH_RPC_URL=https://eth-sepolia.g.alchemy.com/v2/YOUR_KEY

# Wallet Management
SETTLA_WALLET_ENCRYPTION_KEY=   # openssl rand -hex 32
SETTLA_MASTER_SEED=             # openssl rand -hex 64
SETTLA_WALLET_STORAGE_PATH=.settla/wallets
```

#### Change 5: Registry test update

**File:** `rail/blockchain/registry_test.go`
**Action:** Update `NewRegistryFromConfig` calls to pass `nil` wallet manager (tests use mocks)

### 4.3 Wallet Setup Procedures

#### Generate Master Seed and Encryption Key

```bash
# Step 1: Generate encryption key (32 bytes = 64 hex chars)
export SETTLA_WALLET_ENCRYPTION_KEY=$(openssl rand -hex 32)
echo "Encryption Key: $SETTLA_WALLET_ENCRYPTION_KEY"

# Step 2: Generate master seed (64 bytes = 128 hex chars)
export SETTLA_MASTER_SEED=$(openssl rand -hex 64)
echo "Master Seed: $SETTLA_MASTER_SEED"

# Step 3: Create wallet storage directory
mkdir -p .settla/wallets

# Step 4: Add to .env file
echo "SETTLA_WALLET_ENCRYPTION_KEY=$SETTLA_WALLET_ENCRYPTION_KEY" >> .env
echo "SETTLA_MASTER_SEED=$SETTLA_MASTER_SEED" >> .env
```

**CRITICAL:** Back up `.env` securely. Losing the master seed = losing all derived wallet addresses and any tokens held.

#### Fund Base Sepolia Wallet

```bash
# Step 1: Start the server to generate wallet addresses
SETTLA_PROVIDER_MODE=testnet go run ./cmd/settla-server/...
# Server logs will show: "registered system wallet chain=base address=0x..."

# Step 2: Copy the Base address from logs

# Step 3: Get Base Sepolia ETH (gas)
# Option A: Alchemy faucet — https://www.alchemy.com/faucets/base-sepolia
# Option B: Coinbase faucet — sign in with Coinbase account
# Request 0.1-0.5 ETH to the address

# Step 4: Get USDC test tokens
# Circle faucet — https://faucet.circle.com/
# Select "Base Sepolia" → paste address → mint 100 USDC

# Step 5: Verify on explorer
# Visit: https://sepolia.basescan.org/address/<YOUR_ADDRESS>
# Check ETH balance and USDC token balance
```

**Estimated faucet limits:** Alchemy: 0.5 ETH/day. Circle USDC: 100 USDC per mint (unlimited frequency). Refill every ~2 weeks depending on demo frequency.

#### Fund Tron Nile Wallet

```bash
# Step 1: Get Tron address from server logs
# "registered system wallet chain=tron address=T..."

# Step 2: Get test TRX (gas/energy)
# Automated: the faucet system in rail/wallet/faucet.go does this automatically
# Manual: Visit https://nileex.io/join/getJoinPage → paste address → request 1000 TRX

# Step 3: Get test USDT
# Option A: Nile USDT may be available via testnet DEX
# Option B: If unavailable, deploy a simple TRC-20 with mint() — see fallback below
# Option C: Check https://nile.tronscan.org/#/token20/TXLAQ63Xg1NAzckPwKHvzw7CSEmLMEqcdj for faucet

# Step 4: Verify on explorer
# Visit: https://nile.tronscan.org/#/address/<YOUR_ADDRESS>
```

**Faucet limits:** Nile faucet: 1000 TRX per request, can be called programmatically. Covers ~100+ TRC-20 transfers (~10 TRX energy cost each).

#### Fund Solana Devnet Wallet

```bash
# Step 1: Get Solana address from server logs
# "registered system wallet chain=solana address=<base58>"

# Step 2: Get devnet SOL (gas)
# Automated: the faucet system does this automatically
# Manual: solana airdrop 2 <ADDRESS> --url devnet
# Or: https://faucet.solana.com

# Step 3: Get test USDC
# Circle faucet — https://faucet.circle.com/
# Select "Solana Devnet" → paste address → mint USDC

# Step 4: Verify on explorer
# Visit: https://explorer.solana.com/address/<ADDRESS>?cluster=devnet
```

**Faucet limits:** Solana airdrop: 2 SOL per request, rate limited to ~1 request per minute. Circle USDC: unlimited minting on devnet. Devnet SOL does NOT expire or reset.

### 4.4 Verification Test Plan

#### Per-Chain Verification Flow

**Base Sepolia:**
```
1. Start server with SETTLA_PROVIDER_MODE=testnet
   → Verify log: "registered system wallet chain=base address=0x..."
2. Check wallet balance via API or direct RPC
   → Verify: ETH balance > 0, USDC balance > 0
3. Create transfer via POST /v1/transfers (destination on Base)
   → Verify: Transfer created, status = PENDING
4. Monitor transfer progression
   → PENDING → FIAT_COLLECTED → CRYPTO_SENT → COMPLETED
5. Extract tx hash from transfer response
   → Verify: Real 0x-prefixed 66-char hash (not "mock-tx-...")
6. Open https://sepolia.basescan.org/tx/{hash}
   → Verify: Real transaction visible, correct USDC amount, correct addresses
7. Check ledger entries
   → Verify: Balanced postings created
8. Confirm explorer URL in API response is clickable
   → Verify: Links to real basescan page showing the transaction
```

**Tron Nile:**
```
1. Start server → Verify log: "registered system wallet chain=tron address=T..."
2. Verify TRX and USDT balances
3. Create transfer (Tron corridor)
4. Monitor: PENDING → FIAT_COLLECTED → CRYPTO_SENT → COMPLETED
5. Extract tx hash → Verify: 64-char hex hash
6. Open https://nile.tronscan.org/#/transaction/{hash}
   → Verify: Real TRC-20 transfer, correct USDT amount
7. Confirm ~57 second confirmation time (19 blocks × 3s)
```

**Solana Devnet:**
```
1. Start server → Verify log: "registered system wallet chain=solana address=..."
2. Verify SOL and USDC balances
3. Create transfer (Solana corridor)
4. Monitor: PENDING → FIAT_COLLECTED → CRYPTO_SENT → COMPLETED
5. Extract signature → Verify: Base58 signature string
6. Open https://explorer.solana.com/tx/{sig}?cluster=devnet
   → Verify: Real SPL transfer, correct USDC amount
7. Confirm ~13 second confirmation time (32 slots × 400ms)
```

---

## 5. Effort Estimates & Timeline

| Task | Effort | Dependencies |
|------|--------|-------------|
| **Phase A: Wiring (Day 1)** | | |
| Modify `registry.go` to accept wallet manager | 2 hours | None |
| Create wallet manager in `main.go`, wire to registry | 2 hours | Phase A.1 |
| Wire explorer URL to router | 30 min | None |
| Update `.env.example` | 30 min | None |
| Update registry tests | 1 hour | Phase A.1 |
| **Phase B: Wallet Setup (Day 1-2)** | | |
| Generate master seed and encryption key | 15 min | Phase A complete |
| Start server, extract wallet addresses | 15 min | Phase A complete |
| Fund Base Sepolia wallet (ETH + USDC) | 30 min | Addresses available |
| Fund Tron Nile wallet (TRX + USDT) | 30 min | Addresses available |
| Fund Solana Devnet wallet (SOL + USDC) | 30 min | Addresses available |
| **Phase C: Verification (Day 2)** | | |
| End-to-end test: Base Sepolia transfer | 2 hours | Phases A+B complete |
| End-to-end test: Tron Nile transfer | 2 hours | Phases A+B complete |
| End-to-end test: Solana Devnet transfer | 2 hours | Phases A+B complete |
| Fix any integration issues found | 2-4 hours | Testing |
| **Total** | **~16-20 hours (2-3 days)** | |

### Priority Order

1. **Base Sepolia first** — Alchemy supported, fastest confirmations (24s), most reliable faucets
2. **Tron Nile second** — Already working RPC defaults, automated faucet, but slower confirmations (57s)
3. **Solana Devnet third** — Alchemy supported, fastest confirmations (13s), but no chain monitor poller for deposits

---

## 6. Risk Assessment & Mitigations

### Faucet Reliability

| Chain | Faucet | Reliability | Mitigation |
|-------|--------|-------------|------------|
| Base Sepolia | Alchemy faucet | High (Alchemy is reliable) | Pre-fund wallet days before demo; keep 0.5+ ETH buffer |
| Base Sepolia | Circle USDC faucet | High (Circle maintains it) | Mint 1000+ USDC in advance |
| Tron Nile | nileex.io faucet | Medium (sometimes slow) | Automated faucet in code retries 3x; pre-fund 5000+ TRX |
| Tron Nile | USDT test token | Medium (availability varies) | Fallback: deploy own TRC-20 with mint() |
| Solana Devnet | solana airdrop | High (official, rate-limited) | Pre-fund 10+ SOL; automated faucet in code |
| Solana Devnet | Circle USDC faucet | High | Mint 1000+ USDC in advance |

**Pre-demo checklist:** Fund all wallets 48 hours before any demo. Verify balances the morning of.

### RPC Reliability

| Chain | Primary RPC | Reliability | Fallback |
|-------|-------------|-------------|----------|
| Base Sepolia | Alchemy | Very high (99.9%+ SLA) | `https://sepolia.base.org` (public) |
| Ethereum Sepolia | Alchemy | Very high | `https://rpc.sepolia.org` (public) |
| Tron Nile | TronGrid | High (occasionally slow) | No built-in fallback; TronGrid is the only maintained Nile endpoint |
| Solana Devnet | Alchemy | Very high | `https://api.devnet.solana.com` (public, rate-limited) |

**Chain monitor already supports failover** via `BackupRPCURL` config for EVM chains.

### Token Availability

| Chain | Token | Freely Mintable | Notes |
|-------|-------|-----------------|-------|
| Base Sepolia | USDC | Yes (Circle faucet) | Unlimited minting via faucet.circle.com |
| Tron Nile | USDT | Uncertain | The hardcoded contract may or may not have a public mint. Fallback: deploy own TRC-20 |
| Solana Devnet | USDC | Yes (Circle faucet) | Unlimited minting via faucet.circle.com |

### Confirmation Times

| Chain | Blocks | Time per Block | Total Confirmation Time | Demo Suitability |
|-------|--------|---------------|------------------------|-----------------|
| Base Sepolia | 12 | ~2s | ~24 seconds | Excellent |
| Tron Nile | 19 | ~3s | ~57 seconds | Acceptable (narrate during wait) |
| Solana Devnet | 32 slots | ~400ms | ~13 seconds | Excellent |

**Demo recommendation:** Lead with Base Sepolia or Solana for fastest feedback. Use Tron to show multi-chain capability.

### Alchemy Rate Limits

| Plan | Compute Units/sec | Sufficient for Demo? |
|------|-------------------|---------------------|
| Free | 330 CU/s | Yes — demo traffic is <<100 CU/s |
| Growth | 660 CU/s | Yes |

Testnet requests consume the same CU as mainnet. A single `eth_sendRawTransaction` = 250 CU. At demo traffic levels (<1 TPS), the free tier is more than sufficient.

### Breaking Changes / Testnet Resets

| Chain | Resets? | Risk |
|-------|---------|------|
| Base Sepolia | No scheduled resets | Low — balances persist indefinitely |
| Tron Nile | No scheduled resets | Low — balances persist |
| Solana Devnet | Occasional resets (~monthly) | Medium — re-fund after reset; Circle USDC survives resets (it's a program, not an account) |

**Mitigation:** Keep faucet scripts/automation ready for quick re-funding.

---

## 7. Demo Script Updates

### Before (Mocked — `SETTLA_PROVIDER_MODE=mock`)

```
Presenter: "Let me create a transfer from GBP to NGN"
[POST /v1/transfers → instant COMPLETED]
Presenter: "The transfer completed. Here's the transaction hash: mock-tx-tron-42"
Audience: "...that's not a real transaction"
Presenter: "It's simulated for the demo"
Audience: [loses confidence]
```

### After (Real Testnet — `SETTLA_PROVIDER_MODE=testnet`)

```
Presenter: "Let me create a transfer from GBP to NGN"
[POST /v1/transfers → PENDING]

Presenter: "The fiat collection is being processed..."
[Status updates: PENDING → FIAT_COLLECTED]

Presenter: "Fiat received. Now Settla is sending the stablecoin settlement on-chain."
[Status: CRYPTO_SENT — real blockchain transaction broadcasted]

Presenter: "Here's the live transaction on the blockchain. You can verify it right now."
[Opens https://sepolia.basescan.org/tx/0x7a8b...3f2e]
[Audience sees: real ERC-20 transfer, real block number, real gas fee, real addresses]

Presenter: "Waiting for confirmations... 4 of 12..."
[Status: COMPLETED after ~24 seconds]

Presenter: "Settlement complete. The USDC has arrived at the destination.
            This is running on Base Sepolia testnet — the same code,
            the same infrastructure, just pointed at testnet RPCs.
            Flipping to mainnet is a configuration change."

Audience: [sees working product, not a mockup]
```

### Key Demo Talking Points

1. **"Click this link"** — Every transfer response includes an explorer URL. Hand the link to the audience.
2. **"Same code, different config"** — Emphasize that `SETTLA_PROVIDER_MODE=testnet` vs `live` is the only difference.
3. **"Multi-chain"** — Show transfers on Base (fast), Tron (largest USDT market), and Solana (fastest finality).
4. **"Real gas fees"** — Point out the actual gas cost on the explorer. "This transfer cost $0.002 in gas on Base."
5. **"Confirmation tracking"** — Show the status progressing through confirmations in real time.

---

## Appendix A: File Reference

### Files That Need Modification

| File | Lines Changed | Purpose |
|------|--------------|---------|
| `rail/blockchain/registry.go` | ~40 lines modified | Accept wallet manager, create signing clients |
| `cmd/settla-server/main.go` | ~30 lines added | Wallet manager creation and wiring |
| `rail/blockchain/registry_test.go` | ~10 lines modified | Update test calls |
| `.env.example` | ~15 lines added | Alchemy URLs, wallet config |

### Files That Already Work (No Changes)

| File | Lines | What It Does |
|------|-------|-------------|
| `rail/blockchain/ethereum/client.go` | 490 | Full EVM client: send, sign, broadcast, poll |
| `rail/blockchain/ethereum/transaction.go` | 150 | ERC-20 encoding, nonce management |
| `rail/blockchain/ethereum/rpc.go` | 110 | go-ethereum ethclient wrapper |
| `rail/blockchain/ethereum/types.go` | 100 | SepoliaConfig, BaseSepoliaConfig with USDC addresses |
| `rail/blockchain/tron/client.go` | 310 | Full Tron client: TRC-20 transfers, signing, broadcast |
| `rail/blockchain/tron/transaction.go` | 200 | TRC-20 encoding, address conversion, tx hash |
| `rail/blockchain/tron/rpc.go` | 230 | TronGrid HTTP client with retry |
| `rail/blockchain/tron/types.go` | 65 | NileConfig with USDT contract |
| `rail/blockchain/solana/client.go` | 350 | Full Solana client: SPL transfers, ATA creation |
| `rail/blockchain/solana/transaction.go` | 200 | SPL TransferChecked instruction building |
| `rail/blockchain/solana/rpc.go` | 140 | solana-go RPC wrapper |
| `rail/blockchain/solana/types.go` | 100 | DevnetConfig with USDC mint |
| `rail/blockchain/explorer.go` | 35 | Testnet explorer URL generation |
| `rail/blockchain/config.go` | 55 | Env var loading with testnet defaults |
| `rail/wallet/manager.go` | 528 | HD wallet lifecycle, signing, encryption |
| `rail/wallet/derivation.go` | 269 | BIP-44 derivation for all chains |
| `rail/wallet/types.go` | 219 | Wallet types, chain constants |
| `rail/wallet/store.go` | 223 | Encrypted file persistence |
| `rail/wallet/faucet.go` | 122 | Automated testnet faucet funding |
| `rail/wallet/keymgmt/` | 527 | Key management, AES-256-GCM, secure zeroing |
| `node/chainmonitor/evm_poller.go` | 310 | ERC-20 Transfer event scanning |
| `node/chainmonitor/tron_poller.go` | 300 | TRC-20 transfer scanning |
| `node/chainmonitor/rpc/eth_client.go` | 442 | Multi-provider EVM RPC with failover |
| `node/chainmonitor/rpc/tron_client.go` | 232 | Multi-provider Tron RPC |
| `rail/provider/settla/onramp.go` | ~500 | Testnet on-ramp with real blockchain |
| `rail/provider/settla/offramp.go` | ~600 | Testnet off-ramp with real blockchain |

### Go Dependencies (All Present in go.mod)

| Package | Version | Purpose |
|---------|---------|---------|
| `github.com/ethereum/go-ethereum` | v1.17.1 | EVM client, types, crypto, ABI |
| `github.com/gagliardetto/solana-go` | v1.14.0 | Solana RPC, transaction, SPL token |
| `github.com/tyler-smith/go-bip32` | v1.0.0 | HD key derivation |
| `github.com/tyler-smith/go-bip39` | v1.1.0 | Mnemonic seeds |
| `github.com/shopspring/decimal` | v1.4.0 | Decimal arithmetic |
| `github.com/btcsuite/btcd/btcutil` | v1.1.6 | Base58 encoding |

---

## Appendix B: Test Token Fallback — Deploy Custom TRC-20

If the Nile USDT contract (`TXLAQ63Xg1NAzckPwKHvzw7CSEmLMEqcdj`) doesn't allow minting, deploy a custom TRC-20:

```solidity
// SPDX-License-Identifier: MIT
pragma solidity ^0.8.0;

contract TestUSDT {
    string public name = "Test USDT";
    string public symbol = "USDT";
    uint8 public decimals = 6;
    mapping(address => uint256) public balanceOf;
    mapping(address => mapping(address => uint256)) public allowance;
    uint256 public totalSupply;

    event Transfer(address indexed from, address indexed to, uint256 value);

    function mint(address to, uint256 amount) external {
        balanceOf[to] += amount;
        totalSupply += amount;
        emit Transfer(address(0), to, amount);
    }

    function transfer(address to, uint256 amount) external returns (bool) {
        require(balanceOf[msg.sender] >= amount, "insufficient balance");
        balanceOf[msg.sender] -= amount;
        balanceOf[to] += amount;
        emit Transfer(msg.sender, to, amount);
        return true;
    }

    // ... approve, transferFrom (standard ERC-20/TRC-20)
}
```

Deploy via TronIDE (https://www.tronide.io/) on Nile testnet, then update `TXLAQ63Xg1NAzckPwKHvzw7CSEmLMEqcdj` in `tron/types.go:37` with the new contract address.

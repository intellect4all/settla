# Chapter 9.4: Blockchain Operations -- Clients, Wallets, and Chain Monitoring

**Reading time: 30 minutes**

## Learning Objectives

By the end of this chapter, you will be able to:

1. Explain Settla's multi-chain architecture and how the blockchain registry provides chain discovery
2. Read the `domain.BlockchainClient` interface and understand how each chain implementation differs
3. Trace the HD wallet derivation path from master seed to per-chain deposit addresses
4. Describe the token registry's lock-free copy-on-write pattern and why it matters for chain monitors
5. Use block explorer integration for transaction verification and debugging
6. Walk through the chain monitor poll cycle from checkpoint load to deposit event emission
7. Identify the security boundaries that protect private key material in memory

---

## Multi-Chain Architecture

Settla processes stablecoin transfers across four blockchain networks. Each
chain has different consensus mechanisms, block times, address formats, and
token standards -- but the settlement engine does not care about any of that.
The blockchain layer abstracts all chain-specific details behind a single
interface.

```
                           domain.BlockchainClient
                                    |
                 +------------------+------------------+
                 |                  |                  |
          +------+------+   +------+------+   +-------+-----+
          |  Ethereum   |   |    Tron     |   |   Solana    |
          |  (+ Base)   |   |             |   |             |
          +-------------+   +-------------+   +-------------+
          | go-ethereum  |   | TronGrid   |   | solana-go   |
          | ERC-20       |   | TRC-20     |   | SPL Token   |
          | secp256k1    |   | secp256k1  |   | Ed25519     |
          | 12s blocks   |   | 3s blocks  |   | 400ms slots |
          +-------------+   +-------------+   +-------------+
                 |                  |                  |
                 v                  v                  v
          +------+------+   +------+------+   +-------+-----+
          | Sepolia      |   | Nile        |   | Devnet      |
          | Base Sepolia |   | Testnet     |   |             |
          +-------------+   +-------------+   +-------------+
```

The key insight: Ethereum and Base share the same client implementation
(`ethereum.Client`) with different configurations. Both are EVM-compatible,
so a single `ethereum.NewClient` constructor handles both chains by varying
the chain ID, RPC URL, and contract addresses. This is why the registry
registers four chains but only has three client packages.

---

## The BlockchainClient Interface

Every blockchain client implements this interface from `domain/provider.go`:

```go
// domain.BlockchainClient -- the contract every chain must satisfy.
type BlockchainClient interface {
    // Chain returns the blockchain identifier (e.g., ChainTron, ChainEthereum).
    Chain() CryptoChain
    // GetBalance returns the token balance for an address.
    GetBalance(ctx context.Context, address string, token string) (decimal.Decimal, error)
    // EstimateGas returns the estimated gas fee for a transaction.
    EstimateGas(ctx context.Context, req TxRequest) (decimal.Decimal, error)
    // SendTransaction submits a transaction to the blockchain.
    SendTransaction(ctx context.Context, req TxRequest) (*ChainTx, error)
    // GetTransaction retrieves a transaction by hash.
    GetTransaction(ctx context.Context, hash string) (*ChainTx, error)
    // SubscribeTransactions subscribes to transaction events for an address.
    SubscribeTransactions(ctx context.Context, address string, ch chan<- ChainTx) error
}
```

Six methods. Every chain implements all six. The `TxRequest` and `ChainTx`
types are also defined in `domain/` so that no upstream module ever imports
a chain-specific package.

> **Key Insight:** The interface is deliberately minimal. Operations like
> "create associated token account" (Solana) or "estimate energy cost" (Tron)
> are internal to each client. Callers see only `SendTransaction` and the
> client handles chain-specific mechanics internally.

---

## The Blockchain Registry

The registry is the single source of truth for blockchain client instances.
It lives in `rail/blockchain/registry.go` and uses a concurrent-safe map
indexed by `domain.CryptoChain`:

```go
// Registry holds blockchain clients indexed by chain name.
type Registry struct {
    mu      sync.RWMutex
    clients map[domain.CryptoChain]domain.BlockchainClient
    logger  *slog.Logger
}
```

### Factory Construction

`NewRegistryFromConfig` bootstraps all four chains from environment
configuration. The pattern is the same for each chain: load testnet
defaults, override from config, construct client, register:

```go
func NewRegistryFromConfig(cfg BlockchainConfig, walletMgr *wallet.Manager,
    logger *slog.Logger) (*Registry, error) {

    r := NewRegistry(logger)

    // -- Tron (Nile testnet) --
    tronCfg := tron.NileConfig
    if cfg.TronRPCURL != "" {
        tronCfg.RPCURL = cfg.TronRPCURL
    }
    r.Register(tron.NewClient(tronCfg, walletMgr, logger))

    // -- Ethereum (Sepolia) -- needs a Signer for EVM tx signing
    ethCfg := ethereum.SepoliaConfig
    var ethSigner ethereum.Signer
    if walletMgr != nil {
        ethSigner = ethereum.NewWalletSigner(walletMgr, big.NewInt(ethCfg.ChainID))
    }
    ethClient, err := ethereum.NewClient(ethCfg, ethSigner, logger)
    r.Register(ethClient)

    // -- Base (Sepolia) -- same ethereum.NewClient, different config
    // -- Solana (Devnet) -- solana.New(solCfg, walletMgr)
    // ... same pattern for each chain ...
    return r, nil
}
```

Two design decisions to note:

1. **walletMgr can be nil.** If nil, clients are read-only -- they can query
   balances and transaction status but cannot sign or broadcast. The
   `settla-server` process uses read-only clients; only `settla-node` (the
   worker process) needs signing capability.

2. **Network connectivity is NOT verified at construction.** The constructor
   never blocks on a network round-trip. This keeps startup fast and avoids
   a hard dependency on external RPC availability during boot.

### Client Lookup

```go
func (r *Registry) GetClient(chain domain.CryptoChain) (domain.BlockchainClient, error) {
    r.mu.RLock()
    defer r.mu.RUnlock()
    c, ok := r.clients[chain]
    if !ok {
        return nil, fmt.Errorf("settla-blockchain: no client registered for chain %q", chain)
    }
    return c, nil
}
```

The `RLock` allows concurrent reads -- multiple workers can look up clients
simultaneously without contention. `Register` takes the full write lock but
is only called during initialisation.

### System Wallet Registration

After clients are constructed, `RegisterSystemWallets` derives the system
hot wallet for each chain and maps the wallet address to its signing path.
It uses a local interface assertion (`walletRegistrar`) to call
`RegisterWallet(address, walletPath)` on each client:

```go
// Local interface -- not in domain.BlockchainClient because not all
// callers need signing capability.
type walletRegistrar interface {
    RegisterWallet(address, walletPath string)
}
if reg, ok := client.(walletRegistrar); ok {
    reg.RegisterWallet(w.Address, w.Path)
}
```

This keeps the domain interface minimal while still enabling signing for
clients that support it.

---

## Blockchain Client Implementations

### Ethereum / Base (EVM)

The Ethereum client in `rail/blockchain/ethereum/` handles both Ethereum
Sepolia and Base Sepolia. The key difference between the two is configuration:

```go
// Sepolia testnet defaults
var SepoliaConfig = Config{
    ChainName:     "ethereum",
    ChainID:       11155111,
    RPCURL:        "https://rpc.sepolia.org",
    Contracts:     map[string]string{"USDC": "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238"},
    BlockTime:     12 * time.Second,
    Confirmations: 12,
    GasLimit:      100_000,
}

// Base Sepolia testnet defaults
var BaseSepoliaConfig = Config{
    ChainName:     "base",
    ChainID:       84532,
    RPCURL:        "https://sepolia.base.org",
    Contracts:     map[string]string{"USDC": "0x036CbD53842c5426634e7929541eC2318f3dCF7e"},
    BlockTime:     2 * time.Second,
    Confirmations: 12,
    GasLimit:      100_000,
}
```

Notice that Base produces blocks every 2 seconds (vs Ethereum's 12 seconds)
but both require 12 confirmations. The poll interval is adjusted accordingly
(5 seconds for Base, 15 seconds for Ethereum).

**Signing** is handled by the `WalletSigner`, which maps blockchain addresses
to wallet manager paths via a `map[string]string` (lowercase 0x address to
wallet path). When `SignTx` is called, it looks up the path, fetches the
private key from the wallet manager on demand, signs with
`types.LatestSignerForChainID`, and returns the signed transaction. The
private key is never cached in the signer -- it lives only in the wallet
manager.

### Tron

The Tron client (`rail/blockchain/tron/`) uses the TronGrid HTTP API. Three
key differences from EVM:

1. **Energy model vs gas.** Tron estimates cost in SUN (like wei) using an
   energy-based model (`energyPriceSUN * trc20TransferEnergy`), converted to
   USD via a configurable TRX/USD exchange rate.

2. **Direct wallet manager reference.** Unlike Ethereum's `Signer` interface,
   the Tron client holds `*wallet.Manager` directly because Tron's transaction
   building flow differs from EVM's `types.SignTx`.

3. **Base58Check addresses.** Same secp256k1 curve as Ethereum, but addresses
   use `0x41` prefix + Base58Check encoding (starting with `T`).

### Solana

The Solana client (`rail/blockchain/solana/`) uses Ed25519 keys and the
`gagliardetto/solana-go` library. Key considerations:

- **SPL token transfers** require Associated Token Accounts (ATAs). The client
  creates them automatically if the recipient does not have one.
- **Commitment levels**: `processed` (optimistic), `confirmed` (supermajority),
  `finalized` (rooted). Settla defaults to `confirmed`.
- **Slot-based timing**: ~400ms per slot, 32 confirmations = ~13 seconds.

### Chain Comparison Table

```
+-------------+-----------+----------+-------------+-----------+
| Property    | Ethereum  | Base     | Tron        | Solana    |
+-------------+-----------+----------+-------------+-----------+
| Token Std   | ERC-20    | ERC-20   | TRC-20      | SPL Token |
| Key Curve   | secp256k1 | secp256k1| secp256k1   | Ed25519   |
| Address Fmt | 0x hex    | 0x hex   | T.. Base58  | Base58    |
| Block Time  | 12s       | 2s       | 3s          | 400ms     |
| Confirms    | 12        | 12       | 19          | 32 slots  |
| Fee Model   | Gas/Gwei  | Gas/Gwei | Energy/SUN  | Lamports  |
| Native Coin | ETH       | ETH      | TRX         | SOL       |
| USDC Decimal| 6         | 6        | 6           | 6         |
+-------------+-----------+----------+-------------+-----------+
```

---

## HD Wallet Derivation

Settla uses BIP-44 hierarchical deterministic (HD) wallets to derive
blockchain addresses from a single master seed. This is the critical
property: given the same seed, you can regenerate every address ever
created, for every chain, for every tenant.

### The Derivation Path

```
Master Seed (64 bytes, from BIP-39 mnemonic)
    |
    v
BIP-32 Extended Key
    |
    +-- m/44'/195'/0'/0/{index}   --> Tron addresses
    |
    +-- m/44'/60'/0'/0/{index}    --> Ethereum addresses
    |
    +-- m/44'/60'/0'/0/{index}    --> Base addresses (same coin type!)
    |
    +-- m/44'/501'/0'/0/{index}   --> Solana addresses
```

The coin type values follow the BIP-44 standard. Note that Ethereum and
Base share coin type 60 -- both are EVM chains. The wallet path (not the
derivation path) distinguishes them.

The coin type mapping from `rail/wallet/types.go`:

```go
func CoinType(c Chain) uint32 {
    switch c {
    case ChainTron:
        return 195
    case ChainSolana:
        return 501
    case ChainEthereum, ChainBase:
        return 60 // Both EVM chains use Ethereum's coin type
    default:
        return 0
    }
}
```

### The DeriveWallet Function

The core derivation in `rail/wallet/derivation.go` calls
`km.DerivePath(keyID, coinType, 0, 0, index)` to get a BIP-32 key, then
dispatches to chain-specific constructors. The intermediate key is zeroed
immediately via `defer SecureZeroBIP32Key(derivedKey)`:

```go
func DeriveWallet(km keymgmt.KeyManager, keyID string, chain Chain,
    index uint32) (*Wallet, error) {

    coinType := CoinType(chain)
    derivedKey, err := km.DerivePath(keyID, coinType, 0, 0, index)
    if err != nil { return nil, err }
    defer SecureZeroBIP32Key(derivedKey) // zero intermediate key material

    switch chain {
    case ChainEthereum, ChainBase:
        return deriveEthereumWallet(derivedKey, chain, index)
    case ChainTron:
        return deriveTronWallet(derivedKey, index)
    case ChainSolana:
        return deriveSolanaWallet(derivedKey, index)
    }
}
```

### Chain-Specific Address Generation

Each chain turns the same BIP-32 derived key into a different address format:

**Ethereum / Base** -- secp256k1 ECDSA, Keccak-256 hash, 0x-prefixed hex:

```go
func deriveEthereumWallet(key *bip32.Key, chain Chain, index uint32) (*Wallet, error) {
    privateKey, err := crypto.ToECDSA(key.Key)
    // ...
    address := crypto.PubkeyToAddress(*publicKeyECDSA).Hex()
    // Result: "0x71C7656EC7ab88b098defB751B7401B5f6d8976F"
}
```

**Tron** -- same secp256k1 key, but Base58Check encoding with `0x41` prefix:

```go
func publicKeyToTronAddress(pub *ecdsa.PublicKey) string {
    addrBytes := crypto.PubkeyToAddress(*pub).Bytes()

    payload := make([]byte, 21)
    payload[0] = 0x41  // Tron's version byte
    copy(payload[1:], addrBytes)

    return base58CheckEncode(payload)
    // Result: "TJRabPrwbZy45sbavfcjinPJC18kjpRTv8"
}
```

**Solana** -- Ed25519 key from first 32 bytes of derived key, Base58 address:

```go
func deriveSolanaWallet(key *bip32.Key, index uint32) (*Wallet, error) {
    seed := make([]byte, 32)
    copy(seed, key.Key[:32])

    privateKey := ed25519.NewKeyFromSeed(seed)
    SecureClearBytes(seed) // Clear the seed copy immediately

    publicKeyBytes := privateKey.Public().(ed25519.PublicKey)
    solanaPubKey := solana.PublicKeyFromBytes(publicKeyBytes)
    address := solanaPubKey.String()
    // Result: "7xKXtg2CW87d97TXJSDpbD5jBkheTqA83TZRuJosgAsU"
}
```

> **Key Insight:** Tron and Ethereum use the same elliptic curve (secp256k1)
> and the same private key bytes. The only difference is address encoding.
> Solana uses an entirely different curve (Ed25519) and derives the key
> differently. This is why `DeriveWallet` dispatches based on chain.

---

## The Wallet Manager

The `wallet.Manager` in `rail/wallet/manager.go` orchestrates wallet
creation, caching, and signing. It is the only component that holds
private key material. Internally it maintains a `keymgmt.KeyManager`
(master seed, encrypted on disk), a wallet cache (`map[path]*Wallet`),
and a per-chain derivation index counter.

### Wallet Paths

Wallets are addressed by path strings: `"system/hot/tron"` for a system
hot wallet, `"tenant/lemfi/tron"` for a tenant wallet,
`"system/offramp/ethereum/tx-ref-123"` for a per-transaction offramp wallet.

### Key Security Model

The manager enforces four security boundaries:

1. **Master seed encrypted at rest** via `keymgmt.KeyManager` with AES-256.
   The encryption key comes from environment variables (production: KMS).

2. **Private keys never exported.** The `Wallet.privateKey` field is
   unexported. The only access paths are `GetPrivateKeyForSigning` (ECDSA)
   and `GetEd25519KeyForSigning` (Solana), both documented as requiring
   the caller to zero the key after use.

3. **Secure memory zeroing** on every code path: `SecureZeroBIP32Key`,
   `SecureZeroECDSA`, `SecureZeroEd25519`, `SecureClearBytes`.

4. **Seed cleared from config** immediately after storing:

```go
if len(cfg.MasterSeed) > 0 {
    if !km.HasSeed(cfg.KeyID) {
        km.StoreMasterSeed(cfg.KeyID, cfg.MasterSeed)
    }
    SecureClearBytes(cfg.MasterSeed) // zero the config copy
}
```

On shutdown, `Close` zeros every cached wallet's private key, closes the
wallet store, and closes the key manager -- ensuring no key material survives
in process memory.

---

## Token Registry

The chain monitor needs to know which token contracts to watch on each
chain. The `TokenRegistry` in `node/chainmonitor/token_registry.go` provides
this mapping using a lock-free atomic copy-on-write pattern.

### Why Lock-Free?

The chain monitor poll loop runs every few seconds per chain. If the token
registry used a mutex, every poll cycle would contend with the periodic
database reload. The atomic pointer swap eliminates this contention entirely:

```go
type TokenRegistry struct {
    data atomic.Pointer[tokenMap]
}

type tokenMap struct {
    byContract map[string]domain.Token // key: "chain:contractAddress" (lowercased)
    byChain    map[string][]domain.Token
}
```

### Reload (Write Path)

`Reload` builds an entirely new `tokenMap`, filtering out inactive tokens,
and atomically swaps it in via `r.data.Store(m)`. Old readers continue
using the previous snapshot until they finish -- no coordination needed.
Tokens can be disabled in the database without a code deployment.

### Lookup (Read Path)

Lookups are zero-allocation atomic pointer loads:

```go
func (r *TokenRegistry) LookupByContract(chain, contractAddress string) (domain.Token, bool) {
    m := r.data.Load()
    t, ok := m.byContract[tokenKey(chain, contractAddress)]
    return t, ok
}
```

`ContractAddresses(chain)` returns all watched contract addresses for a
chain, used by the EVM poller to build the `eth_getLogs` topic filter.

### The Token Type

From `domain/deposit.go`:

```go
type Token struct {
    ID              uuid.UUID
    Chain           CryptoChain  // e.g. ChainTron, ChainEthereum, ChainBase
    Symbol          string       // e.g. "USDT", "USDC"
    ContractAddress string
    Decimals        int32
    IsActive        bool
    CreatedAt       time.Time
    UpdatedAt       time.Time
}
```

The `Decimals` field is critical for converting between on-chain integer
amounts and human-readable decimals. USDT/USDC use 6 decimals on all Settla
chains, but other tokens vary (DAI uses 18 on Ethereum). Getting this wrong
means moving 10^12x too much or too little money.

---

## Block Explorer Integration

`ExplorerURL` in `rail/blockchain/explorer.go` maps (chain, txHash) to a
human-readable URL: Nile Tronscan for Tron, Sepolia Etherscan for Ethereum,
Base Sepolia Basescan for Base, and Solana Explorer (devnet) for Solana.
These URLs appear in webhook payloads, the tenant portal, ops dashboard, and
logs. In production, they point to mainnet explorers.

---

## Chain Monitor Architecture

The chain monitor runs inside the `settla-node` process and continuously
watches for on-chain transfers to Settla-managed deposit addresses. When a
matching transfer is detected, it writes a deposit transaction and outbox
entry atomically -- triggering the deposit session workflow.

### Poll Cycle Overview

Both the EVM and Tron pollers follow the same high-level pattern:

```
Poll()
  |
  +--> 1. Load checkpoint (last processed block number + hash)
  |
  +--> 2. Get current block height from RPC
  |
  +--> 3. Calculate safe block (current - confirmations)
  |
  +--> 4. Calculate start block (checkpoint + 1, minus reorg depth)
  |
  +--> 5. Snapshot watched addresses (lock-free)
  |
  +--> 6. Scan transfers in [startBlock, safeBlock] range
  |         |
  |         +--> For each transfer to a watched address:
  |         |      - Check if already recorded (idempotency)
  |         |      - Look up active deposit session for address
  |         |      - Write deposit tx + outbox entry atomically
  |         |
  +--> 7. Save checkpoint at safe block
  |
  +--> 8. Detect chain reorganisation (compare parent hashes)
```

### EVM Poller

The EVM poller in `node/chainmonitor/evm_poller.go` uses `eth_getLogs` to
batch-query for ERC-20 Transfer events across all watched addresses in a
single RPC call:

```go
func (p *EVMPoller) Poll(ctx context.Context) error {
    // 1. Load checkpoint
    lastBlock, lastHash, err := p.checkpoint.Load(ctx, p.chain)

    // 2. Get current block height
    currentBlock, err := p.client.GetLatestBlockNumber(ctx)

    // 3. Calculate safe block (current - confirmations)
    safeBlock := currentBlock - int64(p.cfg.Confirmations)

    // 4. Start from checkpoint + 1, minus reorgDepth for safety
    startBlock := lastBlock + 1
    if lastBlock > 0 && p.cfg.ReorgDepth > 0 {
        reorgStart := lastBlock - int64(p.cfg.ReorgDepth)
        if reorgStart > 0 && reorgStart < startBlock {
            startBlock = reorgStart
        }
    }

    // 5. Lock-free address snapshot
    addrSnap := p.addresses.Snapshot()

    // 6. Scan using eth_getLogs
    processed, err := p.scanTransfers(ctx, addrSnap, startBlock, safeBlock)

    // 7. Save checkpoint + 8. Reorg detection
    // ...
}
```

The `scanTransfers` method constructs an `eth_getLogs` filter with:
- **Topic[0]**: The ERC-20 Transfer event signature
- **Topic[2]**: The list of watched recipient addresses (OR filter)
- **Contract addresses**: From the token registry

One RPC call covers all watched addresses for all watched tokens. At scale
with thousands of deposit addresses, this is far more efficient than
per-address polling.

### Tron Poller

The Tron poller in `node/chainmonitor/tron_poller.go` follows the same
pattern but uses TronGrid's TRC-20 transfer API instead of log queries:

```go
type TronPoller struct {
    chain        string
    cfg          ChainConfig
    client       *rpc.TronClient
    addresses    *AddressSet
    tokens       *TokenRegistry
    checkpoint   *CheckpointManager
    outboxWriter OutboxWriter
    logger       *slog.Logger
}
```

Key difference: Tron's API returns transfers per-address, so the Tron poller
iterates over watched addresses rather than making a single batch call.
Block timing is approximated (~3 seconds per block) to convert between
block numbers and timestamps.

### The OutboxWriter Interface

Both pollers write detected deposits through `OutboxWriter`:
`WriteDetectedTx` atomically inserts a deposit transaction + outbox entries,
`GetDepositTxByHash` checks idempotency (has this tx hash been recorded?),
and `GetSessionByAddress` looks up the active deposit session for an address.

### Checkpoint Persistence

The `CheckpointManager` stores the last processed block number and hash per
chain. The block hash enables reorg detection: if the parent hash of the new
block does not match the stored hash, a chain reorganisation has occurred and
the poller re-scans from a deeper starting point.

### Backfill After Downtime

If the chain monitor is offline for an extended period, the poll cycle
naturally handles backfill. The start block is `lastCheckpoint + 1` and the
safe block is `currentBlock - confirmations`. The EVM poller's `eth_getLogs`
accepts a block range, so large gaps are split into batch-sized chunks. The
Tron poller uses timestamp-based filtering for the same purpose.

### Address Set (Lock-Free Reads)

The `AddressSet` uses the same atomic snapshot pattern as the token registry.
When a new deposit session creates a deposit address, it is added to a
mutable map and a new immutable snapshot is published via `atomic.Pointer`.
Pollers call `Snapshot()` at the start of each cycle -- no locks, no
contention with address registration.

---

## Address Validation

The wallet package validates addresses per-chain before any transfer:
Tron (34 chars, `T` prefix, Base58Check with `0x41` version byte),
Ethereum/Base (42 chars, `0x` prefix, valid hex), Solana (valid Base58
Ed25519 public key). Validation happens at multiple layers: API gateway,
engine state machine, and blockchain client pre-send.

---

## Common Mistakes

### Mistake 1: Using the Wrong Decimal Count

```go
// WRONG: hardcoding 18 decimals
amount := rawAmount.Div(decimal.NewFromInt(1e18))

// RIGHT: using the token's actual decimal count
token, _ := registry.LookupByContract(chain, contractAddr)
divisor := decimal.NewFromInt(10).Pow(decimal.NewFromInt32(token.Decimals))
amount := rawAmount.Div(divisor)
```

Always use the token registry's `Decimals` field, never hardcode.

### Mistake 2: Not Handling Chain Reorgs

```go
// WRONG: trusting any transaction as final after 1 confirmation
if blockConfirmations >= 1 {
    markDepositConfirmed(tx)
}

// RIGHT: using chain-specific confirmation thresholds
// Ethereum: 12, Tron: 19, Solana: 32 slots
if blockConfirmations >= chainConfig.Confirmations {
    markDepositConfirmed(tx)
}
```

The reorg depth configuration in `ChainConfig` ensures the poller re-scans
recent blocks even after checkpointing them. Without this, a reorg could
cause a deposit to appear confirmed when the underlying transaction was
actually orphaned.

### Mistake 3: Caching Private Keys in Multiple Places

```go
// WRONG: key exists in two places
key, _ := manager.GetPrivateKeyForSigning(path)
cachedKeys[address] = key

// RIGHT: fetch on demand, zero after use
key, _ := manager.GetPrivateKeyForSigning(path)
defer SecureZeroECDSA(key)
```

The wallet manager is the single owner of key material. Copies that are
never zeroed survive in memory until GC (and potentially on swap).

---

## Exercises

1. **Trace a deposit detection.** Starting from `EVMPoller.Poll()`, list
   every function call and data structure involved in detecting a USDC
   transfer on Ethereum Sepolia. How many RPC calls does a single poll
   cycle make at minimum? What determines the maximum block range per
   scan?

2. **Derive an address by hand.** Given a BIP-44 path of `m/44'/195'/0'/0/7`,
   what chain is this wallet for? What coin type is `195`? If you change
   the path to `m/44'/60'/0'/0/7`, which two chains could this wallet
   serve? How does the wallet manager distinguish between them?

3. **Token registry race condition.** Suppose the token registry reload
   runs concurrently with an EVM poll cycle that calls
   `ContractAddresses("ethereum")`. Can the poll see a partially-built
   token map? Explain why or why not, referencing the atomic pointer swap
   pattern.

4. **Design a new chain client.** Settla needs to add support for the
   Polygon PoS chain (EVM-compatible, chain ID 137, MATIC as native token,
   ~2s block time). Which existing client implementation would you reuse?
   What configuration values would you need? Write the `PolygonConfig`
   variable and the two lines needed to register it in
   `NewRegistryFromConfig`.

5. **Security audit.** Review the `Manager.Close()` method. Is there a
   window where a concurrent `GetPrivateKeyForSigning` call could return
   a zeroed key? What would happen if it did? How would you fix this
   if it were a concern in production?

---

## Module 9 Summary

Over these four chapters, we traced the complete deposit and payment flow:

- **9.1 Crypto Deposits** -- session state machine, on-chain detection,
  confirmation tracking, auto-convert vs hold strategies
- **9.2 Bank Deposits** -- virtual accounts, partner bank webhooks,
  amount + reference matching
- **9.3 Payment Links** -- shareable URLs, public redemption, underlying
  deposit sessions
- **9.4 Blockchain Operations** -- registry pattern, HD wallet derivation,
  lock-free token registry, checkpoint-based chain monitoring with reorg
  detection

Together they form a layered stack:

```
+--------------------------------------------------+
|              Tenant API (Sessions)                |
+--------------------------------------------------+
|  Crypto Deposit  |  Bank Deposit  | Payment Links |
+--------------------------------------------------+
|        Chain Monitor (EVM + Tron Pollers)         |
+--------------------------------------------------+
|  Token Registry  |  Address Set  |  Checkpoints   |
+--------------------------------------------------+
|        Blockchain Clients (Registry)              |
+--------------------------------------------------+
|     HD Wallet Manager (BIP-44 Derivation)         |
+--------------------------------------------------+
|       Master Seed (KMS-encrypted)                 |
+--------------------------------------------------+
```

Each layer has a clean interface boundary. The deposit engine does not know
which blockchain a payment arrived on. The chain monitor does not know
whether the deposit will be auto-converted or held. The wallet manager does
not know which tenant owns the address it derived.

---

## What's Next

Module 10 shifts focus to security and compliance -- the cross-cutting
concerns that every layer of Settla must satisfy. We will cover API key
management and HMAC authentication, tenant isolation enforcement, webhook
signature verification, encryption at rest and in transit, audit logging,
and the regulatory requirements that shape every design decision in a
financial system.

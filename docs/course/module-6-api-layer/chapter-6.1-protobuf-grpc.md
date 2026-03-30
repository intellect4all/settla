# Chapter 6.1: Protocol Buffers and gRPC Service Definitions

**Reading time: 25 minutes**

## Learning Objectives

By the end of this chapter you will be able to:

1. Explain why Settla uses Protocol Buffers and gRPC between the TypeScript gateway and the Go backend
2. Read and write proto3 service and message definitions for financial systems
3. Trace the `buf generate` pipeline from `.proto` files to generated Go and TypeScript code
4. Apply proto best practices that prevent money-losing bugs in production

---

## Why gRPC Between Gateway and Server?

Settla's API layer is split into two processes: a TypeScript gateway (Fastify) that faces the internet, and a Go backend (settla-server) that hosts the domain logic. They communicate over gRPC.

```
                       Internet
                          |
                     +---------+
                     |   Tyk   |  API Gateway (auth, rate limits, TLS)
                     +---------+
                          |
                     +---------+
                     | Fastify |  BFF gateway (tenant resolution, REST)
                     | :3000   |  TypeScript
                     +---------+
                          | gRPC (~50 persistent connections)
                     +---------+
                     | settla  |  Domain server (engine, ledger, treasury)
                     | :9090   |  Go
                     +---------+
```

Three reasons drive this choice:

**1. Type safety across the language boundary.** Proto definitions are the contract. If a field is renamed or removed, both sides fail at compile time, not at 3 AM in production.

**2. Binary efficiency at 5,000 TPS.** JSON serialization at this throughput is measurably slower. Protobuf encodes a `Transfer` message in roughly 300 bytes versus 1,200+ bytes as JSON. Over a persistent HTTP/2 connection with header compression, that difference compounds.

**3. Codegen eliminates boilerplate.** One `.proto` file generates Go structs, TypeScript interfaces, and serialization code. No hand-maintained DTOs drifting out of sync.

---

## The Proto File Structure

Settla's proto definitions live in `proto/settla/v1/`. The `v1` path segment is a proto best practice: it allows introducing a `v2` package without breaking existing clients.

```
proto/
  settla/
    v1/
      types.proto          # Shared value objects (Money, FeeBreakdown, Sender, Recipient)
      settlement.proto     # Core services: Settlement, Treasury, Ledger, Auth
      tenant_portal.proto  # Portal self-service (profile, API keys, webhooks, metrics)
      portal_auth.proto    # Portal authentication (register, login, KYB)
      deposit.proto        # Crypto deposit session lifecycle
      bank_deposit.proto   # Bank deposit sessions and virtual accounts
      payment_link.proto   # Payment link CRUD and redemption
      analytics.proto      # Transfer, fee, provider, and reconciliation analytics
```

Eight proto files define eleven gRPC services across the bounded contexts. The split is deliberate: each file corresponds to a domain boundary, and each service maps to a specific module in the Go codebase.

### Shared Types: types.proto

The `types.proto` file defines value objects reused across all services:

```protobuf
syntax = "proto3";

package settla.v1;

option go_package = "github.com/intellect4all/settla/gen/settla/v1;settlav1";

// Money represents a currency-qualified decimal amount.
// Amount is a string to preserve decimal precision (never use float for money).
message Money {
  string amount = 1;   // Decimal string, e.g. "1000.50"
  string currency = 2; // ISO 4217 or stablecoin symbol, e.g. "NGN", "USDT"
}

// FeeBreakdown itemizes fees applied to a transfer or quote.
// All fee amounts are decimal strings.
message FeeBreakdown {
  string on_ramp_fee = 1;    // Fee charged by on-ramp provider
  string network_fee = 2;    // Blockchain gas / network fee
  string off_ramp_fee = 3;   // Fee charged by off-ramp provider
  string total_fee_usd = 4;  // Total fees in USD equivalent
}

// RouteInfo describes the settlement route selected for a quote or transfer.
message RouteInfo {
  string chain = 1;               // Blockchain network, e.g. "tron", "ethereum"
  string stable_coin = 2;         // Stablecoin used, e.g. "USDT", "USDC"
  int32 estimated_time_min = 3;   // Estimated settlement time in minutes
  string on_ramp_provider = 4;    // On-ramp provider ID
  string off_ramp_provider = 5;   // Off-ramp provider ID
}

// Sender identifies the party initiating a transfer.
message Sender {
  string id = 1;       // UUID
  string name = 2;
  string email = 3;
  string country = 4;  // ISO 3166-1 alpha-2
}

// Recipient identifies the party receiving funds.
message Recipient {
  string name = 1;
  string account_number = 2;
  string sort_code = 3;
  string bank_name = 4;
  string country = 5;  // ISO 3166-1 alpha-2
  string iban = 6;
}
```

> **Key Insight: Strings for Money, Always**
>
> Every monetary field in Settla's proto definitions is a `string`, not `double` or `float`. This is the single most important rule in financial system design. IEEE 754 floating point cannot represent `0.10` exactly -- the result is `0.1000000000000000055511151231257827021181583404541015625`. Over millions of transactions, these rounding errors accumulate into real money discrepancies. Settla uses `shopspring/decimal` in Go and `decimal.js` in TypeScript, with proto strings as the transport format.

---

## Service Definitions: The Full Service Catalogue

The proto definitions demonstrate Settla's bounded context approach to API design. Each gRPC service maps to a domain boundary. Across eight proto files, Settla defines eleven services:

```protobuf
// settlement.proto -- 4 services sharing common types

// SettlementService -- transfer and quote lifecycle (9 RPCs)
service SettlementService {
  rpc CreateQuote(CreateQuoteRequest) returns (CreateQuoteResponse);
  rpc GetQuote(GetQuoteRequest) returns (GetQuoteResponse);
  rpc CreateTransfer(CreateTransferRequest) returns (CreateTransferResponse);
  rpc GetTransfer(GetTransferRequest) returns (GetTransferResponse);
  rpc GetTransferByExternalRef(GetTransferByExternalRefRequest)
      returns (GetTransferByExternalRefResponse);
  rpc ListTransfers(ListTransfersRequest) returns (ListTransfersResponse);
  rpc CancelTransfer(CancelTransferRequest) returns (CancelTransferResponse);
  rpc ListTransferEvents(ListTransferEventsRequest)
      returns (ListTransferEventsResponse);
  rpc GetRoutingOptions(GetRoutingOptionsRequest)
      returns (GetRoutingOptionsResponse);
}

// TreasuryService -- liquidity position management (8 RPCs)
service TreasuryService {
  rpc GetPositions(GetPositionsRequest) returns (GetPositionsResponse);
  rpc GetPosition(GetPositionRequest) returns (GetPositionResponse);
  rpc GetLiquidityReport(GetLiquidityReportRequest)
      returns (GetLiquidityReportResponse);
  rpc RequestTopUp(RequestTopUpRequest) returns (RequestTopUpResponse);
  rpc RequestWithdrawal(RequestWithdrawalRequest)
      returns (RequestWithdrawalResponse);
  rpc GetPositionTransaction(GetPositionTransactionRequest)
      returns (GetPositionTransactionResponse);
  rpc ListPositionTransactions(ListPositionTransactionsRequest)
      returns (ListPositionTransactionsResponse);
  rpc GetPositionEventHistory(GetPositionEventHistoryRequest)
      returns (GetPositionEventHistoryResponse);
}

// LedgerService -- account balances and transaction history (3 RPCs)
service LedgerService {
  rpc GetAccounts(GetAccountsRequest) returns (GetAccountsResponse);
  rpc GetAccountBalance(GetAccountBalanceRequest)
      returns (GetAccountBalanceResponse);
  rpc GetTransactions(GetTransactionsRequest) returns (GetTransactionsResponse);
}

// AuthService -- API key validation for the gateway (1 RPC)
service AuthService {
  rpc ValidateAPIKey(ValidateAPIKeyRequest) returns (ValidateAPIKeyResponse);
}
```

```protobuf
// deposit.proto -- crypto deposit session lifecycle (7 RPCs)
service DepositService {
  rpc CreateDepositSession(...) returns (...);
  rpc GetDepositSession(...) returns (...);
  rpc ListDepositSessions(...) returns (...);
  rpc CancelDepositSession(...) returns (...);
  rpc GetDepositSessionByTxHash(...) returns (...);
  rpc GetDepositSessionPublicStatus(...) returns (...);  // public, no auth
  rpc GetCryptoBalances(...) returns (...);
  rpc ConvertCryptoToFiat(...) returns (...);
}

// bank_deposit.proto -- bank deposit sessions and virtual accounts (6 RPCs)
service BankDepositService {
  rpc CreateBankDepositSession(...) returns (...);
  rpc GetBankDepositSession(...) returns (...);
  rpc ListBankDepositSessions(...) returns (...);
  rpc CancelBankDepositSession(...) returns (...);
  rpc ListVirtualAccounts(...) returns (...);
  rpc GetBankingPartner(...) returns (...);
}

// payment_link.proto -- payment link CRUD and redemption (6 RPCs)
service PaymentLinkService {
  rpc CreatePaymentLink(...) returns (...);
  rpc GetPaymentLink(...) returns (...);
  rpc ListPaymentLinks(...) returns (...);
  rpc ResolvePaymentLink(...) returns (...);    // public, no auth
  rpc RedeemPaymentLink(...) returns (...);     // public, no auth
  rpc DisablePaymentLink(...) returns (...);
}

// analytics.proto -- tenant-scoped analytics and exports (7 RPCs)
service AnalyticsService {
  rpc GetTransferAnalytics(...) returns (...);
  rpc GetFeeAnalytics(...) returns (...);
  rpc GetProviderAnalytics(...) returns (...);
  rpc GetReconciliationAnalytics(...) returns (...);
  rpc GetDepositAnalytics(...) returns (...);
  rpc CreateExportJob(...) returns (...);
  rpc GetExportJob(...) returns (...);
}

// tenant_portal.proto -- tenant self-service (8+ RPCs)
service TenantPortalService {
  rpc GetMyTenant(...) returns (...);
  rpc UpdateWebhookConfig(...) returns (...);
  rpc ListAPIKeys(...) returns (...);
  rpc CreateAPIKey(...) returns (...);
  rpc RevokeAPIKey(...) returns (...);
  rpc RotateAPIKey(...) returns (...);
  rpc GetDashboardMetrics(...) returns (...);
  rpc GetTransferStats(...) returns (...);
  rpc GetFeeReport(...) returns (...);
}

// portal_auth.proto -- portal registration and authentication (6 RPCs)
service PortalAuthService {
  rpc Register(...) returns (...);
  rpc Login(...) returns (...);
  rpc VerifyEmail(...) returns (...);
  rpc RefreshToken(...) returns (...);
  rpc SubmitKYB(...) returns (...);
  rpc ApproveKYB(...) returns (...);
}
```

The service catalogue maps the full product surface area:

```
  settlement.proto   ─── SettlementService    (core transfer engine)
                     ─── TreasuryService      (positions + self-service top-up/withdrawal)
                     ─── LedgerService        (account queries)
                     ─── AuthService          (API key validation)

  deposit.proto      ─── DepositService       (crypto deposit sessions)
  bank_deposit.proto ─── BankDepositService   (fiat deposits via virtual accounts)
  payment_link.proto ─── PaymentLinkService   (shareable payment links)
  analytics.proto    ─── AnalyticsService     (analytics and data exports)
  tenant_portal.proto─── TenantPortalService  (self-service: keys, webhooks, metrics)
  portal_auth.proto  ─── PortalAuthService    (registration, login, KYB)
```

Notice that each file owns its own messages and does not import sibling service files (except `payment_link.proto` which imports `deposit.proto` for the `DepositSession` type used in redemption responses). This keeps the dependency graph flat.

### The TreasuryService Expansion

The original `TreasuryService` had 3 RPCs (read-only position queries). It now has 8 RPCs, adding tenant self-service position management:

```protobuf
// New position transaction messages
message PositionTransaction {
  string id = 1;
  string tenant_id = 2;
  string type = 3;       // TOP_UP, WITHDRAWAL, DEPOSIT_CREDIT, INTERNAL_REBALANCE
  string currency = 4;
  string location = 5;
  string amount = 6;     // Decimal string
  string status = 7;     // PENDING, PROCESSING, COMPLETED, FAILED
  string method = 8;     // bank_transfer, crypto, internal
  string destination = 9;
  string reference = 10;
  string failure_reason = 11;
  google.protobuf.Timestamp created_at = 12;
  google.protobuf.Timestamp updated_at = 13;
}

// New position event audit trail
message PositionEventEntry {
  string id = 1;
  string position_id = 2;
  string tenant_id = 3;
  string event_type = 4;     // CREDIT, DEBIT, RESERVE, RELEASE, COMMIT, CONSUME
  string amount = 5;
  string balance_after = 6;
  string locked_after = 7;
  string reference_id = 8;
  string reference_type = 9; // deposit_session, bank_deposit, position_transaction, transfer
  google.protobuf.Timestamp recorded_at = 10;
}
```

The `PositionEventEntry` provides a complete audit trail of every balance mutation, including which entity (transfer, deposit, etc.) caused the change. This is critical for treasury reconciliation.

---

## Message Design Patterns

### Tenant Isolation in Every Request

Every request message includes a `tenant_id` field:

```protobuf
message CreateTransferRequest {
  string tenant_id = 1;          // UUID -- required, tenant scope
  string idempotency_key = 2;    // Required for exactly-once semantics
  string external_ref = 3;       // Caller's reference
  string source_currency = 4;    // ISO 4217
  string source_amount = 5;      // Decimal string, must be positive
  string dest_currency = 6;      // ISO 4217
  Sender sender = 7;
  Recipient recipient = 8;       // Required: name and country at minimum
  string quote_id = 9;           // UUID, optional -- use existing quote
}
```

This is not optional decoration. The gateway extracts the tenant ID from the authenticated API key and injects it into the gRPC request. The tenant ID in the request body is never trusted. This is Critical Invariant #7: tenant isolation.

```
   Client sends:  POST /v1/transfers  (body has NO tenant_id)
   Gateway adds:  tenantId from auth  (extracted from Bearer token)
   gRPC receives: CreateTransferRequest { tenant_id: "from-auth" }
```

### The Transfer State Machine as an Enum

```protobuf
enum TransferStatus {
  TRANSFER_STATUS_UNSPECIFIED = 0;
  TRANSFER_STATUS_CREATED = 1;
  TRANSFER_STATUS_FUNDED = 2;
  TRANSFER_STATUS_ON_RAMPING = 3;
  TRANSFER_STATUS_SETTLING = 4;
  TRANSFER_STATUS_OFF_RAMPING = 5;
  TRANSFER_STATUS_COMPLETING = 6;
  TRANSFER_STATUS_COMPLETED = 7;
  TRANSFER_STATUS_FAILED = 8;
  TRANSFER_STATUS_REFUNDING = 9;
  TRANSFER_STATUS_REFUNDED = 10;
}
```

The `_UNSPECIFIED = 0` value is a proto3 best practice. Proto3 uses 0 as the default for unset fields, so having `UNSPECIFIED` as 0 lets you distinguish between "the caller explicitly set CREATED" and "the caller didn't set any status." The prefix `TRANSFER_STATUS_` avoids enum value collisions since proto3 enums share a namespace within their package.

### Cursor-Based Pagination

```protobuf
message ListTransfersRequest {
  string tenant_id = 1;       // UUID -- required, tenant scope
  int32 page_size = 2;        // Max results per page (default 50, max 1000)
  string page_token = 3;      // Opaque token for cursor-based pagination
}

message ListTransfersResponse {
  repeated Transfer transfers = 1;
  string next_page_token = 2; // Empty if no more results
  int32 total_count = 3;      // Total matching transfers
}
```

The `page_token` is an opaque string. The server currently uses a numeric offset encoded as a string, but the proto contract allows changing the implementation (to a cursor, a timestamp, etc.) without breaking clients. This is why it is `string` and not `int32`.

### Timestamp Handling

```protobuf
message Transfer {
  // ... other fields ...
  google.protobuf.Timestamp created_at = 19;
  google.protobuf.Timestamp updated_at = 20;
  google.protobuf.Timestamp funded_at = 21;    // Set when funded
  google.protobuf.Timestamp completed_at = 22; // Set when completed
  google.protobuf.Timestamp failed_at = 23;    // Set when failed
}
```

Settla uses `google.protobuf.Timestamp` rather than `int64` Unix epoch or `string` ISO 8601. The well-known type provides nanosecond precision, is timezone-agnostic (always UTC), and has built-in conversion functions in every language. In Go: `timestamppb.New(time.Time)`. In TypeScript: automatic conversion to JavaScript Date objects.

---

## The Auth Service: A Special Case

```protobuf
service AuthService {
  rpc ValidateAPIKey(ValidateAPIKeyRequest) returns (ValidateAPIKeyResponse);
}

message ValidateAPIKeyRequest {
  string key_hash = 1; // HMAC-SHA256 hex digest of the raw API key
}

message ValidateAPIKeyResponse {
  bool valid = 1;
  string tenant_id = 2;           // UUID
  string slug = 3;
  string status = 4;              // ACTIVE, SUSPENDED, etc.
  string fee_schedule_json = 5;   // JSON: {"onramp_bps":40,"offramp_bps":25,...}
  string daily_limit_usd = 6;    // Decimal string
  string per_transfer_limit = 7;  // Decimal string
}
```

Two things to note:

1. **The gateway sends an HMAC hash, never the raw key.** The raw API key (`sk_live_...`) is HMAC-SHA256 hashed with a server-side secret (`SETTLA_API_KEY_HMAC_SECRET`) on the TypeScript side before crossing any network boundary. The Go backend stores and compares HMAC hashes. Even if both the gRPC connection and the database were compromised, the attacker cannot verify candidate keys without the HMAC secret. This is a significant improvement over plain SHA-256: without the secret, an attacker who obtains the hash database cannot brute-force keys even if they know the key format (`sk_live_...`).

2. **`fee_schedule_json` is a JSON string inside a proto field.** This is a pragmatic compromise. Fee schedules are per-tenant configuration that changes rarely and has variable structure. Defining a deeply nested proto message for every possible fee permutation would couple the proto schema to business logic that changes quarterly. A JSON string keeps the proto stable while the fee structure evolves.

### The PortalAuthService: Registration and KYB

The `PortalAuthService` (in `portal_auth.proto`) handles the tenant onboarding lifecycle, separate from the API key validation flow:

```protobuf
service PortalAuthService {
  rpc Register(RegisterRequest) returns (RegisterResponse);
  rpc Login(LoginRequest) returns (LoginResponse);
  rpc VerifyEmail(VerifyEmailRequest) returns (VerifyEmailResponse);
  rpc RefreshToken(RefreshTokenRequest) returns (RefreshTokenResponse);
  rpc SubmitKYB(SubmitKYBRequest) returns (SubmitKYBResponse);
  rpc ApproveKYB(ApproveKYBRequest) returns (ApproveKYBResponse);
}
```

This service powers the tenant portal UI. `AuthService.ValidateAPIKey` is for programmatic API access (the hot path at 5,000 TPS). `PortalAuthService` is for human-initiated login and onboarding flows (much lower volume). Separating them into distinct services prevents the portal's JWT-based auth from coupling to the API key validation path.

---

## The Code Generation Pipeline

Settla uses [Buf](https://buf.build) for proto compilation instead of raw `protoc`. The Makefile target is straightforward:

```makefile
## proto: Generate protobuf stubs (Go + TypeScript via buf)
proto:
	rm -rf gen/settla api/gateway/src/gen
	buf lint
	buf generate
```

The pipeline:

```
  proto/settla/v1/*.proto
          |
     buf lint          (style checks: field numbering, naming conventions)
          |
     buf generate      (reads buf.gen.yaml for plugin configuration)
          |
    +-----+-----+
    |             |
  gen/settla/v1/   api/gateway/src/gen/
  (Go stubs)       (TypeScript stubs)
```

Generated Go code lands in `gen/settla/v1/`. For each proto file, two Go files are generated:

- `settlement.pb.go` -- Message structs with marshal/unmarshal methods
- `settlement_grpc.pb.go` -- Service interfaces and client/server stubs
- `deposit.pb.go`, `deposit_grpc.pb.go` -- Deposit service
- `bank_deposit.pb.go`, `bank_deposit_grpc.pb.go` -- Bank deposit service
- `payment_link.pb.go`, `payment_link_grpc.pb.go` -- Payment link service
- `analytics.pb.go`, `analytics_grpc.pb.go` -- Analytics service
- `tenant_portal.pb.go`, `tenant_portal_grpc.pb.go` -- Tenant portal service
- `portal_auth.pb.go`, `portal_auth_grpc.pb.go` -- Portal auth service
- `types.pb.go` -- Shared message types (no service, so no `_grpc.pb.go`)

Generated TypeScript code lands in `api/gateway/src/gen/`. However, the gateway primarily uses `@grpc/proto-loader` for dynamic loading at runtime rather than statically generated TS code. The proto files are loaded once at startup:

```typescript
const packageDef = protoLoader.loadSync(
  [
    path.join(PROTO_DIR, "settla/v1/settlement.proto"),
    path.join(PROTO_DIR, "settla/v1/types.proto"),
    path.join(PROTO_DIR, "settla/v1/tenant_portal.proto"),
    path.join(PROTO_DIR, "settla/v1/portal_auth.proto"),
    path.join(PROTO_DIR, "settla/v1/deposit.proto"),
    path.join(PROTO_DIR, "settla/v1/bank_deposit.proto"),
    path.join(PROTO_DIR, "settla/v1/payment_link.proto"),
    path.join(PROTO_DIR, "settla/v1/analytics.proto"),
  ],
  {
    keepCase: false,  // convert to camelCase for JS
    longs: String,    // CRITICAL: never use JS number for money
    enums: String,
    defaults: true,
    oneofs: true,
    includeDirs: [PROTO_DIR],
  },
);
```

All eight proto files are loaded together so the gateway can construct service clients for all eleven services from a single proto definition load.

The `rm -rf` before generation is intentional. It ensures stale generated code from deleted proto messages does not linger. Generated code should never be hand-edited.

---

## Proto Best Practices for Financial Systems

### 1. Never Reuse Field Numbers

```protobuf
message Transfer {
  string id = 1;              // Field 1 is "id" forever
  string tenant_id = 2;       // Field 2 is "tenant_id" forever
  // If you remove "external_ref", mark field 3 as reserved:
  // reserved 3;
  // reserved "external_ref";
}
```

Proto encodes fields by number, not by name. If you delete field 3 and later add a new field with number 3, old clients will decode old data into the new field. In a financial system, this could mean an account number gets interpreted as a transaction amount.

### 2. Use Wrapper Messages for RPCs

Every RPC uses a dedicated request and response message:

```protobuf
rpc GetTransfer(GetTransferRequest) returns (GetTransferResponse);
```

Never use the domain object directly:

```protobuf
// BAD: cannot add metadata or pagination later
rpc GetTransfer(TransferId) returns (Transfer);
```

The wrapper pattern lets you add fields (pagination, request metadata, feature flags) without changing the RPC signature.

### 3. Make Amounts Strings, Not Doubles

This bears repeating because it is the most common financial system bug:

```protobuf
// CORRECT
string source_amount = 5;  // "1000.50"

// WRONG -- will lose precision
double source_amount = 5;  // 1000.4999999999999 (IEEE 754)
```

### 4. Prefix Enum Values

```protobuf
enum TransferStatus {
  TRANSFER_STATUS_UNSPECIFIED = 0;  // Prefix prevents collision
  TRANSFER_STATUS_CREATED = 1;
}
```

Proto3 enums are package-scoped. Without the prefix, `CREATED` from `TransferStatus` would collide with `CREATED` from any other enum in the same package.

### 5. Always Include an Unspecified Zero Value

```protobuf
enum AccountType {
  ACCOUNT_TYPE_UNSPECIFIED = 0;  // Default / not-set
  ACCOUNT_TYPE_ASSET = 1;
  ACCOUNT_TYPE_LIABILITY = 2;
  ACCOUNT_TYPE_REVENUE = 3;
  ACCOUNT_TYPE_EXPENSE = 4;
}
```

This lets the server distinguish between "client sent ASSET" and "client didn't set the field" (which defaults to 0 in proto3).

---

## The Routing Options Message: Composite Design

The `GetRoutingOptions` RPC demonstrates how proto messages compose to represent complex domain concepts:

```protobuf
message ScoreBreakdown {
  string cost = 1;        // 0-1 decimal
  string speed = 2;       // 0-1 decimal
  string liquidity = 3;   // 0-1 decimal
  string reliability = 4; // 0-1 decimal
}

message RoutingOption {
  string provider = 1;
  string off_ramp_provider = 2;
  string chain = 3;
  string stablecoin = 4;
  string score = 5;                       // composite score, decimal string
  string estimated_fee_usd = 6;           // decimal string
  int32 estimated_settlement_seconds = 7;
  ScoreBreakdown score_breakdown = 8;
}

message GetRoutingOptionsResponse {
  repeated RoutingOption routes = 1;
  google.protobuf.Timestamp quoted_at = 2;
  int32 valid_for_seconds = 3;
}
```

Even the score breakdown components are strings. At the precision Settla needs (four decimal places for routing weights), this prevents any floating-point accumulation errors in the routing algorithm.

---

## Common Mistakes

1. **Using `float`/`double` for monetary amounts.** In a system processing 50 million transactions per day, even sub-cent rounding errors accumulate to thousands of dollars of discrepancy per month.

2. **Putting the raw API key in the proto message.** The `ValidateAPIKeyRequest` takes a `key_hash`, never the raw key. This is defense in depth: even if gRPC traffic is intercepted, the attacker cannot use the hash to authenticate.

3. **Reusing field numbers after deletion.** This causes silent data corruption when old and new clients communicate. Always use `reserved` for removed fields.

4. **Skipping `buf lint`.** The linter catches naming convention violations, missing field documentation, and other issues that compound into maintenance debt.

5. **Coupling proto schema to business logic.** The `fee_schedule_json` field is a JSON string specifically because fee structures change faster than proto schemas should. Not everything needs to be a proto message.

---

## Exercises

1. **Add a new RPC.** Design a `RetryTransfer` RPC for the `SettlementService`. What fields does the request message need? How do you handle idempotency?

2. **Spot the bug.** A developer adds `double settlement_amount = 26;` to the `Transfer` message. Explain why this is wrong and what production impact it would have after 1 million transactions.

3. **Proto evolution.** The business wants to add a `compliance_check` field to `CreateTransferRequest`. The field is a nested message with KYC level and sanctions screening result. Design the message addition without breaking existing clients.

4. **Trace the flow.** Starting from `proto/settla/v1/settlement.proto`, trace exactly what files `buf generate` creates and where they land. Then trace how `api/gateway/src/grpc/client.ts` loads those definitions.

---

## What's Next

In Chapter 6.2, we will examine the Go gRPC server implementation: how `server.go` translates between proto messages and domain types, validates requests, maps errors to gRPC status codes, and propagates tenant context through the call chain.

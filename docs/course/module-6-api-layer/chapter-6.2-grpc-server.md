# Chapter 6.2: The gRPC Server Implementation

**Reading time: 30 minutes**

## Learning Objectives

By the end of this chapter you will be able to:

1. Explain how the Go gRPC server uses dependency injection to remain testable
2. Trace a request from proto message through validation to domain call and back
3. Map domain errors to appropriate gRPC status codes
4. Describe how tenant context propagates from the gateway through gRPC metadata

---

## Server Architecture: Dependency Injection via Interfaces

The gRPC server in `api/grpc/server.go` is the bridge between the proto wire format and Settla's domain logic. Its design follows one principle: the server has zero business logic. It validates inputs, calls domain services, and maps outputs.

```
  gRPC Request (proto bytes)
        |
   +----v----+
   |  Server  |  Validates fields, parses UUIDs/decimals
   +----+----+
        |
   +----v------------------------------------------+
   | Engine            (core.Engine)               |
   | DepositEngine     (deposit.Engine)            |
   | BankDepositEngine (bankdeposit.Engine)        |
   | Treasury          (domain.TreasuryManager)    |
   | Ledger            (domain.Ledger)             |
   | PositionEngine    (position transactions)     |
   | PaymentLinkService(paymentlink.Service)       |
   | AnalyticsStore    (analytics queries)         |
   | PortalAuthStore   (registration, login)       |
   | PortalStore       (tenant self-service)       |
   +----+------------------------------------------+
        |
   +----v----+
   |  Server  |  Maps domain types to proto, domain errors to gRPC status
   +----+----+
        |
   gRPC Response (proto bytes)
```

The `Server` struct embeds ten unimplemented service servers (for forward compatibility) and holds domain service interfaces:

```go
type Server struct {
    pb.UnimplementedSettlementServiceServer
    pb.UnimplementedTreasuryServiceServer
    pb.UnimplementedLedgerServiceServer
    pb.UnimplementedAuthServiceServer
    pb.UnimplementedTenantPortalServiceServer
    pb.UnimplementedPortalAuthServiceServer
    pb.UnimplementedDepositServiceServer
    pb.UnimplementedBankDepositServiceServer
    pb.UnimplementedPaymentLinkServiceServer
    pb.UnimplementedAnalyticsServiceServer

    engine              *core.Engine
    depositEngine       *depositcore.Engine
    bankDepositEngine   *bankdepositcore.Engine
    treasury            domain.TreasuryManager
    ledger              domain.Ledger
    authStore           APIKeyValidator
    portalStore         TenantPortalStore
    portalAuthStore     PortalAuthStore
    webhookStore        WebhookManagementStore
    analyticsStore      AnalyticsStore
    extAnalyticsStore   ExtendedAnalyticsStore
    exportStore         ExportStore
    accountStore        AccountStore
    bankingPartnerStore BankingPartnerStore
    positionEngine      PositionEngine
    positionEventStore  PositionEventStore
    paymentLinkService  *paymentlinkcore.Service
    paymentLinkBaseURL  string
    auditLogger         domain.AuditLogger
    jwtSecret           []byte
    apiKeyHMACSecret    []byte
    logger              *slog.Logger
}
```

Ten `Unimplemented*` embeds, one per service protocol. The `Unimplemented*` embeds are a gRPC Go convention. They implement every RPC method with a "not implemented" response. This means if a new RPC is added to the proto definition, the server compiles and returns `codes.Unimplemented` for the new RPC until you write the handler. Without this, adding an RPC to the proto would break compilation of every server that implements the interface.

The Server struct has grown from 5 fields to 20+ as the platform expanded from transfers-only to deposits, bank deposits, payment links, analytics, portal auth, and tenant self-service. Each field corresponds to a domain boundary, and each is injected via the options pattern.

### The Service Implementation Files

Each service protocol is implemented in its own file:

```
api/grpc/
  server.go                  # Server struct, NewServer, SettlementService RPCs
  position_service.go        # TreasuryService position transaction RPCs
  deposit_service.go         # DepositService RPCs
  bank_deposit_service.go    # BankDepositService RPCs
  payment_link_service.go    # PaymentLinkService RPCs
  analytics_service.go       # AnalyticsService RPCs
  portal_auth_service.go     # PortalAuthService RPCs
  tenant_portal_service.go   # TenantPortalService RPCs
  ops_http.go                # OpsService HTTP endpoints
  analytics.go               # Shared analytics query types
```

This file-per-service convention keeps individual files focused. The `server.go` file handles the original `SettlementService` and `TreasuryService` (read-only position queries). When the `TreasuryService` gained self-service RPCs (`RequestTopUp`, `RequestWithdrawal`, `GetPositionTransaction`, `ListPositionTransactions`, `GetPositionEventHistory`), those went into `position_service.go` to avoid bloating `server.go`.

### The Options Pattern for Optional Dependencies

Not every server instance needs every dependency. The auth service needs an `APIKeyValidator`, but a test server might not. Settla uses the functional options pattern:

```go
// ServerOption configures optional Server dependencies.
type ServerOption func(*Server)

// WithAuthStore sets the API key validator for the AuthService.
func WithAuthStore(v APIKeyValidator) ServerOption {
    return func(s *Server) { s.authStore = v }
}

// WithAccountStore sets the account store for account listing.
func WithAccountStore(s AccountStore) ServerOption {
    return func(srv *Server) { srv.accountStore = s }
}

// WithPositionEngine sets the position transaction engine.
func WithPositionEngine(e PositionEngine) ServerOption {
    return func(s *Server) { s.positionEngine = e }
}

// WithPositionEventStore sets the position event store for event history queries.
func WithPositionEventStore(s PositionEventStore) ServerOption {
    return func(srv *Server) { srv.positionEventStore = s }
}

// WithAPIKeyHMACSecret sets the server-side secret used for HMAC-SHA256 API key
// hashing. When set, API keys are hashed with HMAC(secret, rawKey) instead of
// plain SHA-256, providing defense-in-depth if the key_hash database is leaked.
func WithAPIKeyHMACSecret(secret []byte) ServerOption {
    return func(s *Server) { s.apiKeyHMACSecret = secret }
}

func NewServer(
    engine *core.Engine,
    treasury domain.TreasuryManager,
    ledger domain.Ledger,
    logger *slog.Logger,
    opts ...ServerOption,
) *Server {
    if engine == nil {
        panic("settla-grpc: NewServer requires a non-nil engine")
    }
    if treasury == nil {
        panic("settla-grpc: NewServer requires a non-nil treasury manager")
    }
    if ledger == nil {
        panic("settla-grpc: NewServer requires a non-nil ledger service")
    }

    s := &Server{
        engine:   engine,
        treasury: treasury,
        ledger:   ledger,
        logger:   logger.With("module", "api.grpc"),
    }
    for _, opt := range opts {
        opt(s)
    }
    return s
}
```

This pattern is idiomatic Go. The required dependencies (`engine`, `treasury`, `ledger`, `logger`) are positional parameters -- the constructor panics if any is nil. Optional dependencies are passed as variadic options. In production, the server is created with all options. In tests, you can omit the auth store and test the settlement RPCs in isolation:

```go
// Production -- all 15+ options
srv := grpc.NewServer(engine, treasury, ledger, logger,
    grpc.WithAuthStore(authValidator),
    grpc.WithAccountStore(accountStore),
    grpc.WithPositionEngine(positionEngine),
    grpc.WithPositionEventStore(eventStore),
    grpc.WithBankingPartnerStore(bankingPartnerStore),
    grpc.WithAPIKeyHMACSecret(hmacSecret),
    // ... other options ...
)

// Test -- only settlement RPCs, no auth
srv := grpc.NewServer(engine, treasury, ledger, logger)
```

### Interface Boundaries

The server defines its own interface for API key validation rather than importing a concrete type:

```go
type APIKeyValidator interface {
    ValidateAPIKey(ctx context.Context, keyHash string) (APIKeyResult, error)
}

type APIKeyResult struct {
    TenantID         string
    Slug             string
    Status           string
    FeeScheduleJSON  string
    DailyLimitUSD    string
    PerTransferLimit string
}
```

This is the Go proverb "accept interfaces, return structs" in action. The server does not care whether the validator is backed by a database, a mock, or a Redis cache. It only needs something that satisfies `ValidateAPIKey`. This makes the gRPC layer completely independent of the storage layer.

---

## Request Handling: The CreateTransfer RPC

Let us trace the most important RPC end to end. This is the `CreateTransfer` handler:

```go
func (s *Server) CreateTransfer(
    ctx context.Context,
    req *pb.CreateTransferRequest,
) (*pb.CreateTransferResponse, error) {
    tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
    if err != nil {
        return nil, err
    }

    sourceAmount, err := parseDecimal(req.GetSourceAmount(), "source_amount")
    if err != nil {
        return nil, err
    }

    if req.GetIdempotencyKey() == "" {
        return nil, status.Error(codes.InvalidArgument, "idempotency_key is required")
    }
    if req.GetSourceCurrency() == "" {
        return nil, status.Error(codes.InvalidArgument, "source_currency is required")
    }
    if req.GetDestCurrency() == "" {
        return nil, status.Error(codes.InvalidArgument, "dest_currency is required")
    }
    if req.GetRecipient() == nil {
        return nil, status.Error(codes.InvalidArgument, "recipient is required")
    }

    coreReq := core.CreateTransferRequest{
        ExternalRef:    req.GetExternalRef(),
        IdempotencyKey: req.GetIdempotencyKey(),
        SourceCurrency: domain.Currency(req.GetSourceCurrency()),
        SourceAmount:   sourceAmount,
        DestCurrency:   domain.Currency(req.GetDestCurrency()),
        Sender:         senderFromProto(req.GetSender()),
        Recipient:      recipientFromProto(req.GetRecipient()),
    }

    if req.GetQuoteId() != "" {
        qid, err := parseUUID(req.GetQuoteId(), "quote_id")
        if err != nil {
            return nil, err
        }
        coreReq.QuoteID = &qid
    }

    transfer, err := s.engine.CreateTransfer(ctx, tenantID, coreReq)
    if err != nil {
        return nil, mapDomainError(err)
    }

    return &pb.CreateTransferResponse{Transfer: transferToProto(transfer)}, nil
}
```

The handler follows a strict three-phase pattern:

```
  Phase 1: VALIDATE
  +---------------------------------+
  | Parse UUIDs, decimals           |
  | Check required string fields    |
  | Return codes.InvalidArgument    |
  +---------------------------------+
            |
  Phase 2: EXECUTE
  +---------------------------------+
  | Build domain request struct     |
  | Call engine.CreateTransfer()    |
  | Map domain errors to gRPC      |
  +---------------------------------+
            |
  Phase 3: RESPOND
  +---------------------------------+
  | Convert domain.Transfer to      |
  | pb.Transfer via transferToProto |
  +---------------------------------+
```

### Validation Helpers

Two validation helpers appear throughout the server:

```go
func parseUUID(s, field string) (uuid.UUID, error) {
    if s == "" {
        return uuid.Nil, status.Errorf(codes.InvalidArgument, "%s is required", field)
    }
    id, err := uuid.Parse(s)
    if err != nil {
        return uuid.Nil, status.Errorf(codes.InvalidArgument,
            "%s must be a valid UUID", field)
    }
    return id, nil
}

func parseDecimal(s, field string) (decimal.Decimal, error) {
    if s == "" {
        return decimal.Zero, status.Errorf(codes.InvalidArgument,
            "%s is required", field)
    }
    d, err := decimal.NewFromString(s)
    if err != nil {
        return decimal.Zero, status.Errorf(codes.InvalidArgument,
            "%s must be a valid decimal", field)
    }
    return d, nil
}
```

These are intentionally simple. They do not validate business rules (minimum amounts, supported currencies). That logic belongs in the domain layer (`core.Engine`). The gRPC layer only validates that the wire format is syntactically correct: UUIDs are valid UUIDs, decimals are valid decimals, required fields are present.

> **Key Insight: Validation Layering**
>
> Settla validates at two levels: (1) the gRPC server checks syntactic validity (is this a UUID? is this a number?), and (2) the domain engine checks semantic validity (is this amount above the minimum? is this currency pair supported?). This separation means the domain layer never has to deal with malformed input, and the gRPC layer never encodes business rules.

---

## Domain-to-Proto Mapping

Converting domain types to proto messages is pure data transformation with no logic. The `transferToProto` function demonstrates the pattern:

```go
func transferToProto(t *domain.Transfer) *pb.Transfer {
    if t == nil {
        return nil
    }
    pbT := &pb.Transfer{
        Id:             t.ID.String(),
        TenantId:       t.TenantID.String(),
        ExternalRef:    t.ExternalRef,
        IdempotencyKey: t.IdempotencyKey,
        Status:         transferStatusToProto(t.Status),
        Version:        t.Version,
        SourceCurrency: string(t.SourceCurrency),
        SourceAmount:   t.SourceAmount.String(),
        DestCurrency:   string(t.DestCurrency),
        DestAmount:     t.DestAmount.String(),
        StableCoin:     string(t.StableCoin),
        StableAmount:   t.StableAmount.String(),
        Chain:          t.Chain,
        FxRate:         t.FXRate.String(),
        Fees:           feeBreakdownToProto(t.Fees),
        Sender:         senderToProto(t.Sender),
        Recipient:      recipientToProto(t.Recipient),
        CreatedAt:      timestamppb.New(t.CreatedAt),
        UpdatedAt:      timestamppb.New(t.UpdatedAt),
        FailureReason:  t.FailureReason,
        FailureCode:    t.FailureCode,
    }

    if t.QuoteID != nil {
        pbT.QuoteId = t.QuoteID.String()
    }
    if t.FundedAt != nil {
        pbT.FundedAt = timestamppb.New(*t.FundedAt)
    }
    if t.CompletedAt != nil {
        pbT.CompletedAt = timestamppb.New(*t.CompletedAt)
    }
    if t.FailedAt != nil {
        pbT.FailedAt = timestamppb.New(*t.FailedAt)
    }

    return pbT
}
```

Notice the pattern for optional timestamps: domain uses `*time.Time` (nil when not set), proto uses `google.protobuf.Timestamp` (nil pointer means not set in proto3). The `nil` checks prevent panics from calling `timestamppb.New` on a nil `*time.Time`.

The reverse mapping (proto to domain) is similarly mechanical:

```go
func recipientFromProto(r *pb.Recipient) domain.Recipient {
    if r == nil {
        return domain.Recipient{}
    }
    return domain.Recipient{
        Name:          r.GetName(),
        AccountNumber: r.GetAccountNumber(),
        SortCode:      r.GetSortCode(),
        BankName:      r.GetBankName(),
        Country:       r.GetCountry(),
        IBAN:          r.GetIban(),
    }
}
```

The `Get*()` methods are generated by protoc. They return the zero value if the field is not set, avoiding nil pointer dereferences on the proto message.

---

## Error Mapping: Domain Errors to gRPC Status Codes

This is the most nuanced part of the server. Domain errors carry semantic meaning (TRANSFER_NOT_FOUND, INSUFFICIENT_FUNDS, QUOTE_EXPIRED). gRPC has its own status code system (NOT_FOUND, RESOURCE_EXHAUSTED, FAILED_PRECONDITION). The mapping must be precise because the TypeScript gateway maps gRPC status codes to HTTP status codes, and those reach the end user.

```go
func mapDomainError(err error) error {
    if err == nil {
        return nil
    }

    var domErr *domain.DomainError
    if !errors.As(err, &domErr) {
        return status.Error(codes.Internal, err.Error())
    }

    msg := domainErrorMessage(domErr)

    switch domErr.Code() {
    case domain.CodeQuoteExpired, domain.CodeInvalidTransition,
         domain.CodePositionLocked, domain.CodeCorridorDisabled,
         domain.CodeTenantSuspended, domain.CodeOptimisticLock:
        return status.Error(codes.FailedPrecondition, msg)

    case domain.CodeInsufficientFunds, domain.CodeReservationFailed,
         domain.CodeDailyLimitExceeded, domain.CodeRateLimitExceeded:
        return status.Error(codes.ResourceExhausted, msg)

    case domain.CodeTransferNotFound, domain.CodeAccountNotFound,
         domain.CodeTenantNotFound:
        return status.Error(codes.NotFound, msg)

    case domain.CodeIdempotencyConflict:
        return status.Error(codes.AlreadyExists, msg)

    case domain.CodeAmountTooLow, domain.CodeAmountTooHigh,
         domain.CodeCurrencyMismatch, domain.CodeLedgerImbalance:
        return status.Error(codes.InvalidArgument, msg)

    case domain.CodeUnauthorized, domain.CodeInvalidCredentials:
        return status.Error(codes.Unauthenticated, msg)

    case domain.CodeProviderError, domain.CodeChainError,
         domain.CodeProviderUnavailable, domain.CodeNetworkError:
        return status.Error(codes.Unavailable, msg)

    case domain.CodeBlockchainReorg, domain.CodeCompensationFailed:
        return status.Error(codes.Internal, msg)

    default:
        return status.Error(codes.Internal, msg)
    }
}
```

The error message includes the domain code in brackets:

```go
func domainErrorMessage(domErr *domain.DomainError) string {
    return fmt.Sprintf("[%s] %s", domErr.Code(), domErr.Error())
}
```

This produces messages like `[TRANSFER_NOT_FOUND] transfer a1b2c3d4-... not found`. The gateway's `mapGrpcError` function (in `api/gateway/src/errors.ts`) parses the `[CODE]` prefix to extract the machine-readable error code for the REST response. This is a lightweight convention that avoids the complexity of gRPC error details or custom metadata while still carrying structured error information across the language boundary.

```
  Domain Error                     gRPC Status              HTTP Response
  +-----------------------+        +------------------+     +-------------------+
  | Code: TRANSFER_NOT_   |  --->  | codes.NotFound   | --> | 404 Not Found     |
  |       FOUND           |        | "[TRANSFER_NOT_  | --> | { error:          |
  | Msg: "transfer ...    |        |  FOUND] ..."     |     |   "TRANSFER_NOT_  |
  |       not found"      |        +------------------+     |    FOUND" }       |
  +-----------------------+                                  +-------------------+
```

### Why Specific Groupings Matter

The groupings are deliberate:

- **FailedPrecondition** = the request is valid, but the system state prevents it (quote expired, wrong transfer state, position locked). The client should not retry without changing state.
- **ResourceExhausted** = a limit was hit (insufficient funds, daily limit). The client may retry after the condition changes.
- **InvalidArgument** = the request itself is wrong. The client must fix the request.
- **Unavailable** = a downstream system is down. The client should retry with backoff.

Getting these wrong causes real problems. If `INSUFFICIENT_FUNDS` returns `InvalidArgument`, client retry logic will not back off. If `PROVIDER_UNAVAILABLE` returns `FailedPrecondition`, the client will not retry at all.

---

## The Auth RPC: Graceful Degradation

The `ValidateAPIKey` handler shows a pattern for services that might not be configured:

```go
func (s *Server) ValidateAPIKey(
    ctx context.Context,
    req *pb.ValidateAPIKeyRequest,
) (*pb.ValidateAPIKeyResponse, error) {
    if req.GetKeyHash() == "" {
        return nil, status.Error(codes.InvalidArgument, "key_hash is required")
    }

    if s.authStore == nil {
        return &pb.ValidateAPIKeyResponse{Valid: false}, nil
    }

    result, err := s.authStore.ValidateAPIKey(ctx, req.GetKeyHash())
    if err != nil {
        // Key not found -- return invalid, not an error
        return &pb.ValidateAPIKeyResponse{Valid: false}, nil
    }

    return &pb.ValidateAPIKeyResponse{
        Valid:            true,
        TenantId:         result.TenantID,
        Slug:             result.Slug,
        Status:           result.Status,
        FeeScheduleJson:  result.FeeScheduleJSON,
        DailyLimitUsd:    result.DailyLimitUSD,
        PerTransferLimit: result.PerTransferLimit,
    }, nil
}
```

Two design decisions:

1. **If `authStore` is nil, return `Valid: false` instead of an error.** This lets the server start without an auth store (in tests, during development), while the gateway interprets `Valid: false` as "reject the request."

2. **If the key is not found, return `Valid: false` instead of an error.** An invalid API key is not a server error. It is a normal authentication outcome. The distinction matters: errors trigger circuit breakers and alerts; `Valid: false` does not.

---

## The Position Service: Graceful Feature Gating

The position transaction RPCs (in `api/grpc/position_service.go`) demonstrate how Settla gates features that may not be configured in all deployments:

```go
func (s *Server) RequestTopUp(ctx context.Context, req *pb.RequestTopUpRequest) (*pb.RequestTopUpResponse, error) {
    tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
    if err != nil {
        return nil, err
    }

    if s.positionEngine == nil {
        return nil, status.Error(codes.Unimplemented, "position management not configured")
    }

    if err := validateDecimalAmount(req.GetAmount()); err != nil {
        return nil, err
    }
    if err := validateCurrencyCode(req.GetCurrency()); err != nil {
        return nil, err
    }
    if err := validateNonEmpty("location", req.GetLocation()); err != nil {
        return nil, err
    }

    amount, err := parseDecimal(req.GetAmount(), "amount")
    if err != nil {
        return nil, err
    }

    tx, err := s.positionEngine.RequestTopUp(ctx, tenantID, domain.TopUpRequest{
        Currency: domain.Currency(req.GetCurrency()),
        Location: req.GetLocation(),
        Amount:   amount,
        Method:   req.GetMethod(),
    })
    if err != nil {
        return nil, mapDomainError(err)
    }

    return &pb.RequestTopUpResponse{
        Transaction: positionTransactionToProto(tx),
    }, nil
}
```

The `positionEngine == nil` check returns `codes.Unimplemented` rather than panicking. This pattern appears in every position-related RPC and lets the server start without a position engine configured (useful in tests or minimal deployments). The `PositionEngine` interface is defined locally in the gRPC package:

```go
type PositionEngine interface {
    RequestTopUp(ctx context.Context, tenantID uuid.UUID, req domain.TopUpRequest) (*domain.PositionTransaction, error)
    RequestWithdrawal(ctx context.Context, tenantID uuid.UUID, req domain.WithdrawalRequest) (*domain.PositionTransaction, error)
    GetTransaction(ctx context.Context, tenantID, txID uuid.UUID) (*domain.PositionTransaction, error)
    ListTransactions(ctx context.Context, tenantID uuid.UUID, limit, offset int32) ([]domain.PositionTransaction, error)
}
```

The `GetPositionEventHistory` RPC demonstrates a two-step lookup: it first resolves the position ID from the treasury manager (using currency + location), then queries the event store. This keeps the proto API user-friendly (callers specify currency and location, not internal position IDs):

```go
func (s *Server) GetPositionEventHistory(ctx context.Context, req *pb.GetPositionEventHistoryRequest) (*pb.GetPositionEventHistoryResponse, error) {
    // ... validation ...

    // Look up position ID from treasury manager.
    pos, err := s.treasury.GetPosition(ctx, tenantID, domain.Currency(req.GetCurrency()), req.GetLocation())
    if err != nil {
        return nil, mapDomainError(err)
    }

    from := time.Now().AddDate(0, 0, -30) // default: last 30 days
    if req.GetFrom() != nil {
        from = req.GetFrom().AsTime()
    }
    // ... fetch events using pos.ID ...
}
```

---

## Pagination: Cursor-Based with Offset Encoding

The `ListTransfers` handler shows how Settla implements pagination:

```go
func (s *Server) ListTransfers(
    ctx context.Context,
    req *pb.ListTransfersRequest,
) (*pb.ListTransfersResponse, error) {
    tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
    if err != nil {
        return nil, err
    }

    pageSize := int(req.GetPageSize())
    if pageSize <= 0 {
        pageSize = 50
    }
    if pageSize > 1000 {
        pageSize = 1000
    }

    offset := 0
    if req.GetPageToken() != "" {
        offset, err = strconv.Atoi(req.GetPageToken())
        if err != nil {
            return nil, status.Error(codes.InvalidArgument, "invalid page_token")
        }
    }

    transfers, err := s.engine.ListTransfers(ctx, tenantID, pageSize, offset)
    if err != nil {
        return nil, mapDomainError(err)
    }

    pbTransfers := make([]*pb.Transfer, len(transfers))
    for i := range transfers {
        pbTransfers[i] = transferToProto(&transfers[i])
    }

    var nextToken string
    if len(transfers) == pageSize {
        nextToken = strconv.Itoa(offset + pageSize)
    }

    return &pb.ListTransfersResponse{
        Transfers:     pbTransfers,
        NextPageToken: nextToken,
        TotalCount:    int32(len(pbTransfers)),
    }, nil
}
```

The page token is currently an offset encoded as a string. The `next_page_token` is only set when the returned batch is full (meaning there may be more results). An empty `next_page_token` signals "no more pages." The page size is clamped between 1 and 1000 with a default of 50, preventing clients from requesting unbounded result sets.

---

## The Transfer Status Mapping

The status enum mapping between domain and proto is a switch statement, deliberately verbose:

```go
func transferStatusToProto(s domain.TransferStatus) pb.TransferStatus {
    switch s {
    case domain.TransferStatusCreated:
        return pb.TransferStatus_TRANSFER_STATUS_CREATED
    case domain.TransferStatusFunded:
        return pb.TransferStatus_TRANSFER_STATUS_FUNDED
    case domain.TransferStatusOnRamping:
        return pb.TransferStatus_TRANSFER_STATUS_ON_RAMPING
    case domain.TransferStatusSettling:
        return pb.TransferStatus_TRANSFER_STATUS_SETTLING
    case domain.TransferStatusOffRamping:
        return pb.TransferStatus_TRANSFER_STATUS_OFF_RAMPING
    case domain.TransferStatusCompleted:
        return pb.TransferStatus_TRANSFER_STATUS_COMPLETED
    case domain.TransferStatusFailed:
        return pb.TransferStatus_TRANSFER_STATUS_FAILED
    case domain.TransferStatusRefunding:
        return pb.TransferStatus_TRANSFER_STATUS_REFUNDING
    case domain.TransferStatusRefunded:
        return pb.TransferStatus_TRANSFER_STATUS_REFUNDED
    default:
        return pb.TransferStatus_TRANSFER_STATUS_UNSPECIFIED
    }
}
```

A map would be more concise, but the switch has an advantage: the Go compiler warns if a new `domain.TransferStatus` constant is added but not handled in the switch. With a map, the omission would silently map to the zero value. In a financial system, silently returning `UNSPECIFIED` for a valid status is a bug that could cause downstream systems to mishandle the transfer.

---

## Common Mistakes

1. **Putting business logic in the gRPC handler.** The handler should validate inputs, call the domain, and map outputs. If you find yourself writing `if sourceAmount.LessThan(minimumAmount)` in a handler, that check belongs in `core.Engine`.

2. **Returning raw Go errors as gRPC errors.** An `errors.New("database connection failed")` becomes `codes.Internal` with a message that leaks infrastructure details. Always use `mapDomainError` which sanitizes the message.

3. **Forgetting nil checks on optional proto fields.** `req.GetSender()` can return nil. Calling `req.GetSender().GetName()` without the nil check panics the server. The `senderFromProto` helper handles this.

4. **Using `codes.Internal` as a catch-all.** If `PROVIDER_UNAVAILABLE` maps to `Internal`, clients will not retry. If `INSUFFICIENT_FUNDS` maps to `Internal`, monitoring will fire false "server error" alerts. The mapping must be precise.

5. **Ignoring the `Unimplemented` embed.** Removing the `pb.Unimplemented*Server` embeds means the server must implement every RPC to compile. This is brittle as the proto evolves.

---

## Exercises

1. **Implement a new RPC.** Write a `GetTransferSummary` handler that returns aggregated statistics (count, total volume) for a tenant. Follow the three-phase validate/execute/respond pattern.

2. **Error mapping audit.** Review `mapDomainError` and identify what happens if a new domain error code is added but not included in the switch. What status code does the caller receive? Is this acceptable?

3. **Test the auth degradation.** Write a test that creates a `Server` without `WithAuthStore`, calls `ValidateAPIKey`, and verifies it returns `Valid: false` without an error.

4. **Proto-to-domain round-trip.** Prove that `recipientFromProto(recipientToProto(r))` preserves all fields for any valid `domain.Recipient`. What fields, if any, are lost in the round trip?

---

## What's Next

In Chapter 6.3, we move to the TypeScript side: the Fastify REST gateway that receives HTTP requests from tenants, translates them to gRPC calls, and translates the responses back to REST JSON. We will see how the BFF pattern keeps the API layer thin while handling the impedance mismatch between REST conventions and gRPC semantics.

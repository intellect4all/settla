package grpc

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/intellect4all/settla/gen/settla/v1"

	"github.com/intellect4all/settla/core"
	"github.com/intellect4all/settla/domain"
)

// APIKeyValidator can validate API key hashes against the database.
type APIKeyValidator interface {
	ValidateAPIKey(ctx context.Context, keyHash string) (APIKeyResult, error)
}

// APIKeyResult contains the result of validating an API key.
type APIKeyResult struct {
	TenantID         string
	Slug             string
	Status           string
	FeeScheduleJSON  string
	DailyLimitUSD    string
	PerTransferLimit string
}

// Server implements the gRPC SettlementService, TreasuryService, LedgerService, and AuthService.
type Server struct {
	pb.UnimplementedSettlementServiceServer
	pb.UnimplementedTreasuryServiceServer
	pb.UnimplementedLedgerServiceServer
	pb.UnimplementedAuthServiceServer

	engine    *core.Engine
	treasury  domain.TreasuryManager
	ledger    domain.Ledger
	authStore APIKeyValidator
	logger    *slog.Logger
}

// NewServer creates a gRPC server backed by the given domain services.
func NewServer(
	engine *core.Engine,
	treasury domain.TreasuryManager,
	ledger domain.Ledger,
	logger *slog.Logger,
	opts ...ServerOption,
) *Server {
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

// ServerOption configures optional Server dependencies.
type ServerOption func(*Server)

// WithAuthStore sets the API key validator for the AuthService.
func WithAuthStore(v APIKeyValidator) ServerOption {
	return func(s *Server) { s.authStore = v }
}

// ──────────────────────────────────────────────────────────────────────────────
// SettlementService
// ──────────────────────────────────────────────────────────────────────────────

func (s *Server) CreateQuote(ctx context.Context, req *pb.CreateQuoteRequest) (*pb.CreateQuoteResponse, error) {
	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}

	sourceAmount, err := parseDecimal(req.GetSourceAmount(), "source_amount")
	if err != nil {
		return nil, err
	}

	if req.GetSourceCurrency() == "" {
		return nil, status.Error(codes.InvalidArgument, "source_currency is required")
	}
	if req.GetDestCurrency() == "" {
		return nil, status.Error(codes.InvalidArgument, "dest_currency is required")
	}

	quote, err := s.engine.GetQuote(ctx, tenantID, domain.QuoteRequest{
		SourceCurrency: domain.Currency(req.GetSourceCurrency()),
		SourceAmount:   sourceAmount,
		DestCurrency:   domain.Currency(req.GetDestCurrency()),
		DestCountry:    req.GetDestCountry(),
	})
	if err != nil {
		return nil, mapDomainError(err)
	}

	return &pb.CreateQuoteResponse{Quote: quoteToProto(quote)}, nil
}

func (s *Server) GetQuote(ctx context.Context, req *pb.GetQuoteRequest) (*pb.GetQuoteResponse, error) {
	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}

	quoteID, err := parseUUID(req.GetQuoteId(), "quote_id")
	if err != nil {
		return nil, err
	}

	quote, err := s.engine.GetQuoteByID(ctx, tenantID, quoteID)
	if err != nil {
		return nil, mapDomainError(err)
	}

	return &pb.GetQuoteResponse{Quote: quoteToProto(quote)}, nil
}

func (s *Server) CreateTransfer(ctx context.Context, req *pb.CreateTransferRequest) (*pb.CreateTransferResponse, error) {
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

func (s *Server) GetTransfer(ctx context.Context, req *pb.GetTransferRequest) (*pb.GetTransferResponse, error) {
	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}

	transferID, err := parseUUID(req.GetTransferId(), "transfer_id")
	if err != nil {
		return nil, err
	}

	transfer, err := s.engine.GetTransfer(ctx, tenantID, transferID)
	if err != nil {
		return nil, mapDomainError(err)
	}

	return &pb.GetTransferResponse{Transfer: transferToProto(transfer)}, nil
}

func (s *Server) ListTransfers(ctx context.Context, req *pb.ListTransfersRequest) (*pb.ListTransfersResponse, error) {
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

func (s *Server) CancelTransfer(ctx context.Context, req *pb.CancelTransferRequest) (*pb.CancelTransferResponse, error) {
	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}

	transferID, err := parseUUID(req.GetTransferId(), "transfer_id")
	if err != nil {
		return nil, err
	}

	reason := req.GetReason()
	if reason == "" {
		reason = "cancelled by API"
	}

	if err := s.engine.FailTransfer(ctx, transferID, reason, "CANCELLED"); err != nil {
		return nil, mapDomainError(err)
	}

	transfer, err := s.engine.GetTransfer(ctx, tenantID, transferID)
	if err != nil {
		return nil, mapDomainError(err)
	}

	return &pb.CancelTransferResponse{Transfer: transferToProto(transfer)}, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// TreasuryService
// ──────────────────────────────────────────────────────────────────────────────

func (s *Server) GetPositions(ctx context.Context, req *pb.GetPositionsRequest) (*pb.GetPositionsResponse, error) {
	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}

	positions, err := s.treasury.GetPositions(ctx, tenantID)
	if err != nil {
		return nil, mapDomainError(err)
	}

	pbPositions := make([]*pb.Position, len(positions))
	for i := range positions {
		pbPositions[i] = positionToProto(&positions[i])
	}

	return &pb.GetPositionsResponse{Positions: pbPositions}, nil
}

func (s *Server) GetPosition(ctx context.Context, req *pb.GetPositionRequest) (*pb.GetPositionResponse, error) {
	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}

	if req.GetCurrency() == "" {
		return nil, status.Error(codes.InvalidArgument, "currency is required")
	}
	if req.GetLocation() == "" {
		return nil, status.Error(codes.InvalidArgument, "location is required")
	}

	position, err := s.treasury.GetPosition(ctx, tenantID, domain.Currency(req.GetCurrency()), req.GetLocation())
	if err != nil {
		return nil, mapDomainError(err)
	}

	return &pb.GetPositionResponse{Position: positionToProto(position)}, nil
}

func (s *Server) GetLiquidityReport(ctx context.Context, req *pb.GetLiquidityReportRequest) (*pb.GetLiquidityReportResponse, error) {
	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}

	report, err := s.treasury.GetLiquidityReport(ctx, tenantID)
	if err != nil {
		return nil, mapDomainError(err)
	}

	pbPositions := make([]*pb.Position, len(report.Positions))
	for i := range report.Positions {
		pbPositions[i] = positionToProto(&report.Positions[i])
	}

	pbAlerts := make([]*pb.Position, len(report.AlertPositions))
	for i := range report.AlertPositions {
		pbAlerts[i] = positionToProto(&report.AlertPositions[i])
	}

	totalAvailable := make(map[string]string, len(report.TotalAvailable))
	for currency, amount := range report.TotalAvailable {
		totalAvailable[string(currency)] = amount.String()
	}

	return &pb.GetLiquidityReportResponse{
		TenantId:       tenantID.String(),
		Positions:      pbPositions,
		TotalAvailable: totalAvailable,
		AlertPositions: pbAlerts,
		GeneratedAt:    timestamppb.New(report.GeneratedAt),
	}, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// LedgerService
// ──────────────────────────────────────────────────────────────────────────────

func (s *Server) GetAccounts(ctx context.Context, req *pb.GetAccountsRequest) (*pb.GetAccountsResponse, error) {
	_, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}

	// The ledger interface doesn't expose account listing yet.
	// Returns empty; will be wired when account store is implemented.
	return &pb.GetAccountsResponse{
		Accounts:      []*pb.Account{},
		NextPageToken: "",
		TotalCount:    0,
	}, nil
}

func (s *Server) GetAccountBalance(ctx context.Context, req *pb.GetAccountBalanceRequest) (*pb.GetAccountBalanceResponse, error) {
	_, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}

	if req.GetAccountCode() == "" {
		return nil, status.Error(codes.InvalidArgument, "account_code is required")
	}

	balance, err := s.ledger.GetBalance(ctx, req.GetAccountCode())
	if err != nil {
		return nil, mapDomainError(err)
	}

	return &pb.GetAccountBalanceResponse{
		AccountBalance: &pb.AccountBalance{
			AccountCode: req.GetAccountCode(),
			Balance:     balance.String(),
		},
	}, nil
}

func (s *Server) GetTransactions(ctx context.Context, req *pb.GetTransactionsRequest) (*pb.GetTransactionsResponse, error) {
	_, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}

	if req.GetAccountCode() == "" {
		return nil, status.Error(codes.InvalidArgument, "account_code is required")
	}

	var from, to time.Time
	if req.GetFrom() != nil {
		from = req.GetFrom().AsTime()
	}
	if req.GetTo() != nil {
		to = req.GetTo().AsTime()
	} else {
		to = time.Now().UTC()
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

	entries, err := s.ledger.GetEntries(ctx, req.GetAccountCode(), from, to, pageSize, offset)
	if err != nil {
		return nil, mapDomainError(err)
	}

	pbEntries := make([]*pb.EntryLine, len(entries))
	for i := range entries {
		pbEntries[i] = entryLineToProto(&entries[i])
	}

	var nextToken string
	if len(entries) == pageSize {
		nextToken = strconv.Itoa(offset + pageSize)
	}

	return &pb.GetTransactionsResponse{
		Entries:       pbEntries,
		NextPageToken: nextToken,
		TotalCount:    int32(len(pbEntries)),
	}, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// AuthService
// ──────────────────────────────────────────────────────────────────────────────

func (s *Server) ValidateAPIKey(ctx context.Context, req *pb.ValidateAPIKeyRequest) (*pb.ValidateAPIKeyResponse, error) {
	if req.GetKeyHash() == "" {
		return nil, status.Error(codes.InvalidArgument, "key_hash is required")
	}

	if s.authStore == nil {
		return &pb.ValidateAPIKeyResponse{Valid: false}, nil
	}

	result, err := s.authStore.ValidateAPIKey(ctx, req.GetKeyHash())
	if err != nil {
		// Key not found — return invalid, not an error
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

// ──────────────────────────────────────────────────────────────────────────────
// Mapping helpers: domain → proto
// ──────────────────────────────────────────────────────────────────────────────

func quoteToProto(q *domain.Quote) *pb.Quote {
	if q == nil {
		return nil
	}
	return &pb.Quote{
		Id:             q.ID.String(),
		TenantId:       q.TenantID.String(),
		SourceCurrency: string(q.SourceCurrency),
		SourceAmount:   q.SourceAmount.String(),
		DestCurrency:   string(q.DestCurrency),
		DestAmount:     q.DestAmount.String(),
		FxRate:         q.FXRate.String(),
		Fees:           feeBreakdownToProto(q.Fees),
		Route:          routeInfoToProto(q.Route),
		ExpiresAt:      timestamppb.New(q.ExpiresAt),
		CreatedAt:      timestamppb.New(q.CreatedAt),
	}
}

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

func positionToProto(p *domain.Position) *pb.Position {
	if p == nil {
		return nil
	}
	return &pb.Position{
		Id:            p.ID.String(),
		TenantId:      p.TenantID.String(),
		Currency:      string(p.Currency),
		Location:      p.Location,
		Balance:       p.Balance.String(),
		Locked:        p.Locked.String(),
		Available:     p.Available().String(),
		MinBalance:    p.MinBalance.String(),
		TargetBalance: p.TargetBalance.String(),
		UpdatedAt:     timestamppb.New(p.UpdatedAt),
	}
}

func feeBreakdownToProto(f domain.FeeBreakdown) *pb.FeeBreakdown {
	return &pb.FeeBreakdown{
		OnRampFee:   f.OnRampFee.String(),
		NetworkFee:  f.NetworkFee.String(),
		OffRampFee:  f.OffRampFee.String(),
		TotalFeeUsd: f.TotalFeeUSD.String(),
	}
}

func routeInfoToProto(r domain.RouteInfo) *pb.RouteInfo {
	return &pb.RouteInfo{
		Chain:            r.Chain,
		StableCoin:       string(r.StableCoin),
		EstimatedTimeMin: int32(r.EstimatedTimeMin),
		OnRampProvider:   r.OnRampProvider,
		OffRampProvider:  r.OffRampProvider,
	}
}

func senderToProto(s domain.Sender) *pb.Sender {
	return &pb.Sender{
		Id:      s.ID.String(),
		Name:    s.Name,
		Email:   s.Email,
		Country: s.Country,
	}
}

func recipientToProto(r domain.Recipient) *pb.Recipient {
	return &pb.Recipient{
		Name:          r.Name,
		AccountNumber: r.AccountNumber,
		SortCode:      r.SortCode,
		BankName:      r.BankName,
		Country:       r.Country,
		Iban:          r.IBAN,
	}
}

func entryLineToProto(e *domain.EntryLine) *pb.EntryLine {
	return &pb.EntryLine{
		Id:          e.ID.String(),
		AccountId:   e.AccountID.String(),
		AccountCode: e.AccountCode,
		EntryType:   entryTypeToProto(e.EntryType),
		Amount:      e.Amount.String(),
		Currency:    string(e.Currency),
		Description: e.Description,
	}
}

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
	case domain.TransferStatusCompleting:
		return pb.TransferStatus_TRANSFER_STATUS_COMPLETING
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

func entryTypeToProto(t domain.EntryType) pb.EntryType {
	switch t {
	case domain.EntryTypeDebit:
		return pb.EntryType_ENTRY_TYPE_DEBIT
	case domain.EntryTypeCredit:
		return pb.EntryType_ENTRY_TYPE_CREDIT
	default:
		return pb.EntryType_ENTRY_TYPE_UNSPECIFIED
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Mapping helpers: proto → domain
// ──────────────────────────────────────────────────────────────────────────────

func senderFromProto(s *pb.Sender) domain.Sender {
	if s == nil {
		return domain.Sender{}
	}
	id, _ := uuid.Parse(s.GetId())
	return domain.Sender{
		ID:      id,
		Name:    s.GetName(),
		Email:   s.GetEmail(),
		Country: s.GetCountry(),
	}
}

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

// ──────────────────────────────────────────────────────────────────────────────
// Validation helpers
// ──────────────────────────────────────────────────────────────────────────────

func parseUUID(s, field string) (uuid.UUID, error) {
	if s == "" {
		return uuid.Nil, status.Errorf(codes.InvalidArgument, "%s is required", field)
	}
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, status.Errorf(codes.InvalidArgument, "%s must be a valid UUID", field)
	}
	return id, nil
}

func parseDecimal(s, field string) (decimal.Decimal, error) {
	if s == "" {
		return decimal.Zero, status.Errorf(codes.InvalidArgument, "%s is required", field)
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Zero, status.Errorf(codes.InvalidArgument, "%s must be a valid decimal", field)
	}
	return d, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Error mapping: domain errors → gRPC status codes
// ──────────────────────────────────────────────────────────────────────────────

func mapDomainError(err error) error {
	if err == nil {
		return nil
	}

	var domErr *domain.DomainError
	if !errors.As(err, &domErr) {
		return status.Error(codes.Internal, err.Error())
	}

	switch domErr.Code() {
	case domain.CodeQuoteExpired, domain.CodeInvalidTransition, domain.CodePositionLocked,
		domain.CodeCorridorDisabled, domain.CodeTenantSuspended, domain.CodeOptimisticLock:
		return status.Error(codes.FailedPrecondition, domErr.Error())

	case domain.CodeInsufficientFunds, domain.CodeReservationFailed, domain.CodeDailyLimitExceeded:
		return status.Error(codes.ResourceExhausted, domErr.Error())

	case domain.CodeTransferNotFound, domain.CodeAccountNotFound, domain.CodeTenantNotFound:
		return status.Error(codes.NotFound, domErr.Error())

	case domain.CodeIdempotencyConflict:
		return status.Error(codes.AlreadyExists, domErr.Error())

	case domain.CodeAmountTooLow, domain.CodeAmountTooHigh, domain.CodeCurrencyMismatch,
		domain.CodeLedgerImbalance:
		return status.Error(codes.InvalidArgument, domErr.Error())

	case domain.CodeUnauthorized:
		return status.Error(codes.PermissionDenied, domErr.Error())

	case domain.CodeProviderError, domain.CodeChainError:
		return status.Error(codes.Unavailable, domErr.Error())

	default:
		return status.Error(codes.Internal, domErr.Error())
	}
}

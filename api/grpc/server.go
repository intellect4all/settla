package grpc

import (
	"context"
	"errors"
	"fmt"
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
	bankdepositcore "github.com/intellect4all/settla/core/bankdeposit"
	depositcore "github.com/intellect4all/settla/core/deposit"
	paymentlinkcore "github.com/intellect4all/settla/core/paymentlink"
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

// AccountInfo is a simplified account view for the gRPC response.
type AccountInfo struct {
	ID       uuid.UUID
	TenantID *uuid.UUID
	Code     string
	Name     string
	Type     string
	Currency string
	IsActive bool
}

// AccountStore provides account enumeration from the ledger read model.
type AccountStore interface {
	ListAccountsByTenant(ctx context.Context, tenantID uuid.UUID) ([]AccountInfo, error)
}

// WithAccountStore sets the account store for account listing.
func WithAccountStore(s AccountStore) ServerOption {
	return func(srv *Server) { srv.accountStore = s }
}

// BankingPartnerStore provides banking partner lookups for the BankDepositService.
type BankingPartnerStore interface {
	GetBankingPartner(ctx context.Context, id uuid.UUID) (BankingPartnerRow, error)
}

// BankingPartnerRow is a simplified row type for banking partner queries.
type BankingPartnerRow struct {
	ID                  uuid.UUID
	Name                string
	WebhookSecret       string
	SupportedCurrencies []string
	IsActive            bool
}

// WithBankingPartnerStore sets the banking partner store for the BankDepositService.
func WithBankingPartnerStore(s BankingPartnerStore) ServerOption {
	return func(srv *Server) { srv.bankingPartnerStore = s }
}

// Server implements the gRPC SettlementService, TreasuryService, LedgerService, AuthService, TenantPortalService, and PortalAuthService.
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

	engine             *core.Engine
	depositEngine      *depositcore.Engine
	bankDepositEngine  *bankdepositcore.Engine
	treasury        domain.TreasuryManager
	ledger          domain.Ledger
	authStore       APIKeyValidator
	portalStore     TenantPortalStore
	portalAuthStore PortalAuthStore
	webhookStore    WebhookManagementStore
	analyticsStore     AnalyticsStore
	extAnalyticsStore  ExtendedAnalyticsStore
	exportStore        ExportStore
	accountStore       AccountStore
	bankingPartnerStore  BankingPartnerStore
	paymentLinkService   *paymentlinkcore.Service
	paymentLinkBaseURL   string
	auditLogger          domain.AuditLogger
	jwtSecret            []byte
	logger               *slog.Logger
}

// NewServer creates a gRPC server backed by the given domain services.
// Panics if any critical dependency (engine, treasury, ledger) is nil.
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

// ServerOption configures optional Server dependencies.
type ServerOption func(*Server)

// WithAuthStore sets the API key validator for the AuthService.
func WithAuthStore(v APIKeyValidator) ServerOption {
	return func(s *Server) { s.authStore = v }
}

// WithAuditLogger sets the audit logger for recording administrative actions.
func WithAuditLogger(l domain.AuditLogger) ServerOption {
	return func(s *Server) { s.auditLogger = l }
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
	if err := validateDecimalAmount(req.GetSourceAmount()); err != nil {
		return nil, err
	}

	if err := validateCurrencyCode(req.GetSourceCurrency()); err != nil {
		return nil, err
	}
	if err := validateCurrencyCode(req.GetDestCurrency()); err != nil {
		return nil, err
	}
	if req.GetDestCountry() != "" {
		if err := validateCountryCode(req.GetDestCountry()); err != nil {
			return nil, err
		}
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
	if err := validateDecimalAmount(req.GetSourceAmount()); err != nil {
		return nil, err
	}

	if err := validateNonEmpty("idempotency_key", req.GetIdempotencyKey()); err != nil {
		return nil, err
	}
	if err := validateCurrencyCode(req.GetSourceCurrency()); err != nil {
		return nil, err
	}
	if err := validateCurrencyCode(req.GetDestCurrency()); err != nil {
		return nil, err
	}
	if req.GetRecipient() == nil {
		return nil, status.Error(codes.InvalidArgument, "recipient is required")
	}
	if req.GetRecipient().GetCountry() != "" {
		if err := validateCountryCode(req.GetRecipient().GetCountry()); err != nil {
			return nil, err
		}
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

func (s *Server) GetTransferByExternalRef(ctx context.Context, req *pb.GetTransferByExternalRefRequest) (*pb.GetTransferByExternalRefResponse, error) {
	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}

	if req.GetExternalRef() == "" {
		return nil, status.Error(codes.InvalidArgument, "external_ref is required")
	}

	transfer, err := s.engine.GetTransferByExternalRef(ctx, tenantID, req.GetExternalRef())
	if err != nil {
		return nil, mapDomainError(err)
	}

	return &pb.GetTransferByExternalRefResponse{Transfer: transferToProto(transfer)}, nil
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

	// Use filtered listing when status or search filters are provided.
	var transfers []domain.Transfer
	statusFilter := req.GetStatusFilter()
	searchQuery := req.GetSearchQuery()

	if statusFilter != "" || searchQuery != "" {
		transfers, err = s.engine.ListTransfersFiltered(ctx, tenantID, statusFilter, searchQuery, pageSize)
	} else {
		transfers, err = s.engine.ListTransfers(ctx, tenantID, pageSize, offset)
	}
	if err != nil {
		return nil, mapDomainError(err)
	}

	pbTransfers := make([]*pb.Transfer, len(transfers))
	for i := range transfers {
		pbTransfers[i] = transferToProto(&transfers[i])
	}

	var nextToken string
	if statusFilter == "" && searchQuery == "" && len(transfers) == pageSize {
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

	if err := s.engine.FailTransfer(ctx, tenantID, transferID, reason, "CANCELLED"); err != nil {
		return nil, mapDomainError(err)
	}

	transfer, err := s.engine.GetTransfer(ctx, tenantID, transferID)
	if err != nil {
		return nil, mapDomainError(err)
	}

	return &pb.CancelTransferResponse{Transfer: transferToProto(transfer)}, nil
}

func (s *Server) ListTransferEvents(ctx context.Context, req *pb.ListTransferEventsRequest) (*pb.ListTransferEventsResponse, error) {
	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}

	transferID, err := parseUUID(req.GetTransferId(), "transfer_id")
	if err != nil {
		return nil, err
	}

	events, err := s.engine.GetTransferEvents(ctx, tenantID, transferID)
	if err != nil {
		return nil, mapDomainError(err)
	}

	pbEvents := make([]*pb.TransferEvent, len(events))
	for i, e := range events {
		pbEvents[i] = &pb.TransferEvent{
			Id:          e.ID.String(),
			TransferId:  e.TransferID.String(),
			FromStatus:  string(e.FromStatus),
			ToStatus:    string(e.ToStatus),
			OccurredAt:  timestamppb.New(e.OccurredAt),
			ProviderRef: e.ProviderRef,
			Metadata:    e.Metadata,
		}
	}

	return &pb.ListTransferEventsResponse{Events: pbEvents}, nil
}

func (s *Server) GetRoutingOptions(ctx context.Context, req *pb.GetRoutingOptionsRequest) (*pb.GetRoutingOptionsResponse, error) {
	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}

	amount, err := parseDecimal(req.GetAmount(), "amount")
	if err != nil {
		return nil, err
	}
	if err := validateDecimalAmount(req.GetAmount()); err != nil {
		return nil, err
	}

	if err := validateCurrencyCode(req.GetFromCurrency()); err != nil {
		return nil, err
	}
	if err := validateCurrencyCode(req.GetToCurrency()); err != nil {
		return nil, err
	}

	result, err := s.engine.GetRoutingOptions(ctx, tenantID, domain.QuoteRequest{
		SourceCurrency: domain.Currency(req.GetFromCurrency()),
		SourceAmount:   amount,
		DestCurrency:   domain.Currency(req.GetToCurrency()),
	})
	if err != nil {
		return nil, mapDomainError(err)
	}

	// Extract stablecoin from corridor (format: "GBP→USDT→NGN" → "USDT")
	stablecoin := result.Corridor
	if c, err := domain.ParseCorridor(result.Corridor); err == nil {
		stablecoin = string(c.StableCoin)
	}

	// Build routes: primary + alternatives
	routes := make([]*pb.RoutingOption, 0, 1+len(result.Alternatives))
	routes = append(routes, &pb.RoutingOption{
		Provider:                    result.ProviderID,
		OffRampProvider:             result.OffRampProvider,
		Chain:                       result.BlockchainChain,
		Stablecoin:                  stablecoin,
		Score:                       result.Score.StringFixed(4),
		EstimatedFeeUsd:             result.Fee.Amount.StringFixed(2),
		EstimatedSettlementSeconds:  int32(result.EstimatedSeconds),
		ScoreBreakdown:              scoreBreakdownToProto(result.ScoreBreakdown),
	})

	for _, alt := range result.Alternatives {
		routes = append(routes, &pb.RoutingOption{
			Provider:                    alt.OnRampProvider,
			OffRampProvider:             alt.OffRampProvider,
			Chain:                       alt.Chain,
			Stablecoin:                  string(alt.StableCoin),
			Score:                       alt.Score.StringFixed(4),
			EstimatedFeeUsd:             alt.Fee.Amount.StringFixed(2),
			ScoreBreakdown:              scoreBreakdownToProto(alt.ScoreBreakdown),
		})
	}

	return &pb.GetRoutingOptionsResponse{
		Routes:          routes,
		QuotedAt:        timestamppb.Now(),
		ValidForSeconds: 300,
	}, nil
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

	if err := validateCurrencyCode(req.GetCurrency()); err != nil {
		return nil, err
	}
	if err := validateNonEmpty("location", req.GetLocation()); err != nil {
		return nil, err
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
	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}

	if s.accountStore == nil {
		return &pb.GetAccountsResponse{
			Accounts:      []*pb.Account{},
			NextPageToken: "",
			TotalCount:    0,
		}, nil
	}

	accounts, err := s.accountStore.ListAccountsByTenant(ctx, tenantID)
	if err != nil {
		return nil, mapDomainError(err)
	}

	pbAccounts := make([]*pb.Account, len(accounts))
	for i, a := range accounts {
		pbAccounts[i] = &pb.Account{
			Id:       a.ID.String(),
			Code:     a.Code,
			Name:     a.Name,
			Currency: a.Currency,
			IsActive: a.IsActive,
		}
		if a.TenantID != nil {
			pbAccounts[i].TenantId = a.TenantID.String()
		}
	}

	return &pb.GetAccountsResponse{
		Accounts:   pbAccounts,
		TotalCount: int32(len(pbAccounts)),
	}, nil
}

func (s *Server) GetAccountBalance(ctx context.Context, req *pb.GetAccountBalanceRequest) (*pb.GetAccountBalanceResponse, error) {
	_, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}

	if err := validateNonEmpty("account_code", req.GetAccountCode()); err != nil {
		return nil, err
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

	if err := validateNonEmpty("account_code", req.GetAccountCode()); err != nil {
		return nil, err
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
	if err := validateNonEmpty("key_hash", req.GetKeyHash()); err != nil {
		return nil, err
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

func scoreBreakdownToProto(b domain.ScoreBreakdown) *pb.ScoreBreakdown {
	return &pb.ScoreBreakdown{
		Cost:        b.Cost.StringFixed(4),
		Speed:       b.Speed.StringFixed(4),
		Liquidity:   b.Liquidity.StringFixed(4),
		Reliability: b.Reliability.StringFixed(4),
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

// domainErrorMessage formats a gRPC error message that embeds the domain error
// code as a bracket-prefixed tag: "[CODE] human-readable message". The gateway
// parses this prefix to extract the machine-readable code for API responses.
func domainErrorMessage(domErr *domain.DomainError) string {
	return fmt.Sprintf("[%s] %s", domErr.Code(), domErr.Error())
}

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
	case domain.CodeQuoteExpired, domain.CodeInvalidTransition, domain.CodePositionLocked,
		domain.CodeCorridorDisabled, domain.CodeTenantSuspended, domain.CodeOptimisticLock,
		domain.CodeEmailNotVerified, domain.CodeTokenExpired,
		domain.CodeDepositExpired, domain.CodePaymentLinkExpired,
		domain.CodePaymentLinkExhausted, domain.CodePaymentLinkDisabled:
		return status.Error(codes.FailedPrecondition, msg)

	case domain.CodeInsufficientFunds, domain.CodeReservationFailed,
		domain.CodeReservationLockTimeout, domain.CodeReservationInsufficientFunds,
		domain.CodeDailyLimitExceeded, domain.CodeRateLimitExceeded:
		return status.Error(codes.ResourceExhausted, msg)

	case domain.CodeTransferNotFound, domain.CodeAccountNotFound, domain.CodeTenantNotFound,
		domain.CodeDepositNotFound, domain.CodeBankDepositNotFound, domain.CodePaymentLinkNotFound:
		return status.Error(codes.NotFound, msg)

	case domain.CodeIdempotencyConflict, domain.CodeEmailAlreadyExists, domain.CodeSlugConflict:
		return status.Error(codes.AlreadyExists, msg)

	case domain.CodeAmountTooLow, domain.CodeAmountTooHigh, domain.CodeCurrencyMismatch,
		domain.CodeLedgerImbalance, domain.CodePaymentMismatch,
		domain.CodeCryptoDisabled, domain.CodeChainNotSupported,
		domain.CodeBankDepositsDisabled, domain.CodeCurrencyNotSupported:
		return status.Error(codes.InvalidArgument, msg)

	case domain.CodeUnauthorized, domain.CodeInvalidCredentials:
		return status.Error(codes.Unauthenticated, msg)

	case domain.CodeProviderError, domain.CodeChainError, domain.CodeProviderUnavailable,
		domain.CodeNetworkError, domain.CodeAddressPoolEmpty, domain.CodeVirtualAccountPoolEmpty:
		return status.Error(codes.Unavailable, msg)

	case domain.CodeBlockchainReorg, domain.CodeCompensationFailed:
		return status.Error(codes.Internal, msg)

	default:
		return status.Error(codes.Internal, msg)
	}
}

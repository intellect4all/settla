package grpc

import (
	"context"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	bankdepositcore "github.com/intellect4all/settla/core/bankdeposit"
	"github.com/intellect4all/settla/domain"
	pb "github.com/intellect4all/settla/gen/settla/v1"
)

// WithBankDepositEngine sets the bank deposit engine for the BankDepositService.
func WithBankDepositEngine(e *bankdepositcore.Engine) ServerOption {
	return func(s *Server) { s.bankDepositEngine = e }
}

// CreateBankDepositSession creates a new bank deposit session with a virtual account.
func (s *Server) CreateBankDepositSession(ctx context.Context, req *pb.CreateBankDepositSessionRequest) (*pb.CreateBankDepositSessionResponse, error) {
	if s.bankDepositEngine == nil {
		return nil, status.Error(codes.Unimplemented, "bank deposit service not configured")
	}

	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}

	expectedAmount, err := parseDecimal(req.GetExpectedAmount(), "expected_amount")
	if err != nil {
		return nil, err
	}
	if err := validateDecimalAmount(req.GetExpectedAmount()); err != nil {
		return nil, err
	}

	if err := validateNonEmpty("currency", req.GetCurrency()); err != nil {
		return nil, err
	}

	var settlementPref domain.SettlementPreference
	if req.GetSettlementPref() != "" {
		settlementPref = domain.SettlementPreference(req.GetSettlementPref())
	}

	var mismatchPolicy domain.PaymentMismatchPolicy
	if req.GetMismatchPolicy() != "" {
		mismatchPolicy = domain.PaymentMismatchPolicy(req.GetMismatchPolicy())
	}

	var accountType domain.VirtualAccountType
	if req.GetAccountType() != "" {
		accountType = domain.VirtualAccountType(req.GetAccountType())
	}

	createReq := bankdepositcore.CreateSessionRequest{
		Currency:         domain.Currency(req.GetCurrency()),
		BankingPartnerID: req.GetBankingPartnerId(),
		AccountType:      accountType,
		ExpectedAmount:   expectedAmount,
		MismatchPolicy:   mismatchPolicy,
		SettlementPref:   settlementPref,
		IdempotencyKey:   domain.IdempotencyKey(req.GetIdempotencyKey()),
		TTLSeconds:       req.GetTtlSeconds(),
	}

	// Parse optional min/max amounts
	if req.GetMinAmount() != "" {
		minAmount, err := parseDecimal(req.GetMinAmount(), "min_amount")
		if err != nil {
			return nil, err
		}
		createReq.MinAmount = minAmount
	}
	if req.GetMaxAmount() != "" {
		maxAmount, err := parseDecimal(req.GetMaxAmount(), "max_amount")
		if err != nil {
			return nil, err
		}
		createReq.MaxAmount = maxAmount
	}

	session, err := s.bankDepositEngine.CreateSession(ctx, tenantID, createReq)
	if err != nil {
		return nil, mapBankDepositError(err)
	}

	return &pb.CreateBankDepositSessionResponse{
		Session: bankDepositSessionToProto(session),
	}, nil
}

// GetBankDepositSession retrieves a bank deposit session by ID.
func (s *Server) GetBankDepositSession(ctx context.Context, req *pb.GetBankDepositSessionRequest) (*pb.GetBankDepositSessionResponse, error) {
	if s.bankDepositEngine == nil {
		return nil, status.Error(codes.Unimplemented, "bank deposit service not configured")
	}

	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}
	sessionID, err := parseUUID(req.GetSessionId(), "session_id")
	if err != nil {
		return nil, err
	}

	session, err := s.bankDepositEngine.GetSession(ctx, tenantID, sessionID)
	if err != nil {
		return nil, mapBankDepositError(err)
	}

	return &pb.GetBankDepositSessionResponse{
		Session: bankDepositSessionToProto(session),
	}, nil
}

// ListBankDepositSessions lists bank deposit sessions for a tenant.
func (s *Server) ListBankDepositSessions(ctx context.Context, req *pb.ListBankDepositSessionsRequest) (*pb.ListBankDepositSessionsResponse, error) {
	if s.bankDepositEngine == nil {
		return nil, status.Error(codes.Unimplemented, "bank deposit service not configured")
	}

	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}

	pageSize := int(req.GetPageSize())
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 20
	}

	var sessions []domain.BankDepositSession
	pageToken := req.GetPageToken()
	if pageToken == "" {
		// First page: use offset-based query.
		var err error
		sessions, err = s.bankDepositEngine.ListSessions(ctx, tenantID, pageSize, 0)
		if err != nil {
			return nil, mapBankDepositError(err)
		}
	} else {
		// Subsequent pages: cursor-based pagination using created_at.
		cursor, err := time.Parse(time.RFC3339Nano, pageToken)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid page_token: %v", err)
		}
		sessions, err = s.bankDepositEngine.ListSessionsCursor(ctx, tenantID, pageSize, cursor)
		if err != nil {
			return nil, mapBankDepositError(err)
		}
	}

	pbSessions := make([]*pb.BankDepositSession, len(sessions))
	for i := range sessions {
		pbSessions[i] = bankDepositSessionToProto(&sessions[i])
	}

	var nextPageToken string
	if len(sessions) == pageSize {
		last := sessions[len(sessions)-1]
		nextPageToken = last.CreatedAt.Format("2006-01-02T15:04:05.999999Z")
	}

	return &pb.ListBankDepositSessionsResponse{
		Sessions:      pbSessions,
		NextPageToken: nextPageToken,
		TotalCount:    int32(len(sessions)),
	}, nil
}

// CancelBankDepositSession cancels a pending bank deposit session.
func (s *Server) CancelBankDepositSession(ctx context.Context, req *pb.CancelBankDepositSessionRequest) (*pb.CancelBankDepositSessionResponse, error) {
	if s.bankDepositEngine == nil {
		return nil, status.Error(codes.Unimplemented, "bank deposit service not configured")
	}

	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}
	sessionID, err := parseUUID(req.GetSessionId(), "session_id")
	if err != nil {
		return nil, err
	}

	if err := s.bankDepositEngine.CancelSession(ctx, tenantID, sessionID); err != nil {
		return nil, mapBankDepositError(err)
	}

	// Fetch updated session for response.
	session, err := s.bankDepositEngine.GetSession(ctx, tenantID, sessionID)
	if err != nil {
		return nil, mapBankDepositError(err)
	}

	return &pb.CancelBankDepositSessionResponse{
		Session: bankDepositSessionToProto(session),
	}, nil
}

// ListVirtualAccounts lists virtual accounts for a tenant.
// Supports cursor-based pagination (page_size/page_token) with fallback to legacy limit/offset.
func (s *Server) ListVirtualAccounts(ctx context.Context, req *pb.ListVirtualAccountsRequest) (*pb.ListVirtualAccountsResponse, error) {
	if s.bankDepositEngine == nil {
		return nil, status.Error(codes.Unimplemented, "bank deposit service not configured")
	}

	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}

	// Support cursor-based pagination (page_size/page_token) with fallback to legacy limit/offset.
	pageSize := req.GetPageSize()
	if pageSize <= 0 {
		pageSize = req.GetLimit()
	}
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 20
	}

	pageToken := req.GetPageToken()
	if pageToken == "" {
		// Legacy fallback: use offset-based pagination.
		accounts, total, err := s.bankDepositEngine.ListVirtualAccounts(ctx, bankdepositcore.VirtualAccountListParams{
			TenantID:    tenantID,
			Currency:    req.GetCurrency(),
			AccountType: req.GetAccountType(),
			Limit:       pageSize,
			Offset:      req.GetOffset(),
		})
		if err != nil {
			return nil, mapBankDepositError(err)
		}

		pbAccounts := virtualAccountsToProto(accounts)

		var nextToken string
		if int32(len(accounts)) == pageSize && len(accounts) > 0 {
			nextToken = accounts[len(accounts)-1].CreatedAt.Format(time.RFC3339Nano)
		}

		return &pb.ListVirtualAccountsResponse{
			Accounts:      pbAccounts,
			Total:         int32(total),
			NextPageToken: nextToken,
		}, nil
	}

	// Cursor-based path: parse timestamp from page_token.
	cursor, err := time.Parse(time.RFC3339Nano, pageToken)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid page_token: %v", err)
	}

	accounts, err := s.bankDepositEngine.ListVirtualAccountsCursor(ctx, bankdepositcore.VirtualAccountCursorParams{
		TenantID:    tenantID,
		Currency:    req.GetCurrency(),
		AccountType: req.GetAccountType(),
		PageSize:    pageSize,
		Cursor:      cursor,
	})
	if err != nil {
		return nil, mapBankDepositError(err)
	}

	pbAccounts := virtualAccountsToProto(accounts)

	var nextToken string
	if int32(len(accounts)) == pageSize && len(accounts) > 0 {
		nextToken = accounts[len(accounts)-1].CreatedAt.Format(time.RFC3339Nano)
	}

	return &pb.ListVirtualAccountsResponse{
		Accounts:      pbAccounts,
		Total:         int32(len(accounts)),
		NextPageToken: nextToken,
	}, nil
}

// virtualAccountsToProto converts a slice of domain virtual accounts to proto.
func virtualAccountsToProto(accounts []domain.VirtualAccountPool) []*pb.VirtualAccount {
	pbAccounts := make([]*pb.VirtualAccount, len(accounts))
	for i := range accounts {
		a := &accounts[i]
		pbAccounts[i] = &pb.VirtualAccount{
			Id:               a.ID.String(),
			TenantId:         a.TenantID.String(),
			BankingPartnerId: a.BankingPartnerID,
			AccountNumber:    a.AccountNumber,
			AccountName:      a.AccountName,
			SortCode:         a.SortCode,
			Iban:             a.IBAN,
			Currency:         string(a.Currency),
			AccountType:      string(a.AccountType),
			Available:        a.Available,
		}
	}
	return pbAccounts
}

// GetBankingPartner retrieves a banking partner by ID.
func (s *Server) GetBankingPartner(ctx context.Context, req *pb.GetBankingPartnerRequest) (*pb.GetBankingPartnerResponse, error) {
	partnerID, err := parseUUID(req.GetPartnerId(), "partner_id")
	if err != nil {
		return nil, err
	}

	if s.bankingPartnerStore == nil {
		return nil, status.Error(codes.Unimplemented, "banking partner store not configured")
	}

	partner, err := s.bankingPartnerStore.GetBankingPartner(ctx, partnerID)
	if err != nil {
		return nil, status.Error(codes.NotFound, "banking partner not found")
	}

	return &pb.GetBankingPartnerResponse{
		Id:                  partner.ID.String(),
		Name:                partner.Name,
		WebhookSecret:       partner.WebhookSecret,
		SupportedCurrencies: partner.SupportedCurrencies,
		IsActive:            partner.IsActive,
	}, nil
}


func bankDepositSessionToProto(s *domain.BankDepositSession) *pb.BankDepositSession {
	ps := &pb.BankDepositSession{
		Id:               s.ID.String(),
		TenantId:         s.TenantID.String(),
		Status:           string(s.Status),
		BankingPartnerId: s.BankingPartnerID,
		AccountNumber:    s.AccountNumber,
		AccountName:      s.AccountName,
		SortCode:         s.SortCode,
		Iban:             s.IBAN,
		AccountType:      string(s.AccountType),
		Currency:         string(s.Currency),
		ExpectedAmount:   s.ExpectedAmount.String(),
		MinAmount:        s.MinAmount.String(),
		MaxAmount:        s.MaxAmount.String(),
		ReceivedAmount:   s.ReceivedAmount.String(),
		FeeAmount:        s.FeeAmount.String(),
		NetAmount:        s.NetAmount.String(),
		MismatchPolicy:   string(s.MismatchPolicy),
		CollectionFeeBps: int32(s.CollectionFeeBPS),
		SettlementPref:   string(s.SettlementPref),
		IdempotencyKey:   string(s.IdempotencyKey),
		PayerName:        s.PayerName,
		PayerReference:   s.PayerReference,
		BankReference:    s.BankReference,
		ExpiresAt:        timestamppb.New(s.ExpiresAt),
		CreatedAt:        timestamppb.New(s.CreatedAt),
		UpdatedAt:        timestamppb.New(s.UpdatedAt),
		FailureReason:    s.FailureReason,
		FailureCode:      s.FailureCode,
	}
	if s.PaymentReceivedAt != nil {
		ps.PaymentReceivedAt = timestamppb.New(*s.PaymentReceivedAt)
	}
	if s.CreditedAt != nil {
		ps.CreditedAt = timestamppb.New(*s.CreditedAt)
	}
	if s.SettledAt != nil {
		ps.SettledAt = timestamppb.New(*s.SettledAt)
	}
	if s.ExpiredAt != nil {
		ps.ExpiredAt = timestamppb.New(*s.ExpiredAt)
	}
	if s.FailedAt != nil {
		ps.FailedAt = timestamppb.New(*s.FailedAt)
	}
	return ps
}

func mapBankDepositError(err error) error {
	if err == nil {
		return nil
	}

	errStr := err.Error()

	// Map domain errors to gRPC status codes.
	switch {
	case containsCode(errStr, domain.CodeBankDepositNotFound):
		return status.Error(codes.NotFound, errStr)
	case containsCode(errStr, domain.CodeBankDepositsDisabled):
		return status.Error(codes.PermissionDenied, errStr)
	case containsCode(errStr, domain.CodeCurrencyNotSupported):
		return status.Error(codes.InvalidArgument, errStr)
	case containsCode(errStr, domain.CodeVirtualAccountPoolEmpty):
		return status.Error(codes.ResourceExhausted, errStr)
	case containsCode(errStr, domain.CodeInvalidTransition):
		return status.Error(codes.FailedPrecondition, errStr)
	case containsCode(errStr, domain.CodePaymentMismatch):
		return status.Error(codes.FailedPrecondition, errStr)
	case containsCode(errStr, domain.CodeTenantSuspended):
		return status.Error(codes.FailedPrecondition, errStr)
	case containsCode(errStr, domain.CodeAmountTooLow):
		return status.Error(codes.InvalidArgument, errStr)
	case containsCode(errStr, domain.CodeAmountTooHigh):
		return status.Error(codes.InvalidArgument, errStr)
	case containsCode(errStr, domain.CodeIdempotencyConflict):
		return status.Error(codes.AlreadyExists, errStr)
	default:
		return status.Error(codes.Internal, errStr)
	}
}

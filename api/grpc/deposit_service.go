package grpc

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	depositcore "github.com/intellect4all/settla/core/deposit"
	"github.com/intellect4all/settla/domain"
	pb "github.com/intellect4all/settla/gen/settla/v1"
)

// WithDepositEngine sets the deposit engine for the DepositService.
func WithDepositEngine(e *depositcore.Engine) ServerOption {
	return func(s *Server) { s.depositEngine = e }
}

// CreateDepositSession creates a new crypto deposit session.
func (s *Server) CreateDepositSession(ctx context.Context, req *pb.CreateDepositSessionRequest) (*pb.CreateDepositSessionResponse, error) {
	if s.depositEngine == nil {
		return nil, status.Error(codes.Unimplemented, "deposit service not configured")
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

	if err := validateNonEmpty("chain", req.GetChain()); err != nil {
		return nil, err
	}
	if err := validateNonEmpty("token", req.GetToken()); err != nil {
		return nil, err
	}

	var settlementPref domain.SettlementPreference
	if req.GetSettlementPref() != "" {
		settlementPref = domain.SettlementPreference(req.GetSettlementPref())
	}

	createReq := depositcore.CreateSessionRequest{
		Chain:          req.GetChain(),
		Token:          req.GetToken(),
		ExpectedAmount: expectedAmount,
		SettlementPref: settlementPref,
		IdempotencyKey: req.GetIdempotencyKey(),
		TTLSeconds:     int32(req.GetTtlSeconds()),
	}

	session, err := s.depositEngine.CreateSession(ctx, tenantID, createReq)
	if err != nil {
		return nil, mapDepositError(err)
	}

	return &pb.CreateDepositSessionResponse{
		Session: depositSessionToProto(session),
	}, nil
}

// GetDepositSession retrieves a deposit session by ID.
func (s *Server) GetDepositSession(ctx context.Context, req *pb.GetDepositSessionRequest) (*pb.GetDepositSessionResponse, error) {
	if s.depositEngine == nil {
		return nil, status.Error(codes.Unimplemented, "deposit service not configured")
	}

	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}
	sessionID, err := parseUUID(req.GetSessionId(), "session_id")
	if err != nil {
		return nil, err
	}

	session, err := s.depositEngine.GetSession(ctx, tenantID, sessionID)
	if err != nil {
		return nil, mapDepositError(err)
	}

	return &pb.GetDepositSessionResponse{
		Session: depositSessionToProto(session),
	}, nil
}

// ListDepositSessions lists deposit sessions for a tenant.
func (s *Server) ListDepositSessions(ctx context.Context, req *pb.ListDepositSessionsRequest) (*pb.ListDepositSessionsResponse, error) {
	if s.depositEngine == nil {
		return nil, status.Error(codes.Unimplemented, "deposit service not configured")
	}

	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}

	limit := int(req.GetLimit())
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	offset := int(req.GetOffset())
	if offset < 0 {
		offset = 0
	}

	sessions, err := s.depositEngine.ListSessions(ctx, tenantID, limit, offset)
	if err != nil {
		return nil, mapDepositError(err)
	}

	pbSessions := make([]*pb.DepositSession, len(sessions))
	for i := range sessions {
		pbSessions[i] = depositSessionToProto(&sessions[i])
	}

	return &pb.ListDepositSessionsResponse{
		Sessions: pbSessions,
		Total:    int32(len(sessions)),
	}, nil
}

// CancelDepositSession cancels a pending deposit session.
func (s *Server) CancelDepositSession(ctx context.Context, req *pb.CancelDepositSessionRequest) (*pb.CancelDepositSessionResponse, error) {
	if s.depositEngine == nil {
		return nil, status.Error(codes.Unimplemented, "deposit service not configured")
	}

	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}
	sessionID, err := parseUUID(req.GetSessionId(), "session_id")
	if err != nil {
		return nil, err
	}

	if err := s.depositEngine.CancelSession(ctx, tenantID, sessionID); err != nil {
		return nil, mapDepositError(err)
	}

	// Fetch updated session for response.
	session, err := s.depositEngine.GetSession(ctx, tenantID, sessionID)
	if err != nil {
		return nil, mapDepositError(err)
	}

	return &pb.CancelDepositSessionResponse{
		Session: depositSessionToProto(session),
	}, nil
}

// GetDepositSessionByTxHash looks up a deposit session by on-chain transaction hash.
func (s *Server) GetDepositSessionByTxHash(ctx context.Context, req *pb.GetDepositSessionByTxHashRequest) (*pb.GetDepositSessionByTxHashResponse, error) {
	if s.depositEngine == nil {
		return nil, status.Error(codes.Unimplemented, "deposit service not configured")
	}

	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}

	if err := validateNonEmpty("chain", req.GetChain()); err != nil {
		return nil, err
	}

	if err := validateNonEmpty("tx_hash", req.GetTxHash()); err != nil {
		return nil, err
	}

	chain := req.GetChain()
	txHash := req.GetTxHash()

	session, err := s.depositEngine.GetSessionByTxHash(ctx, tenantID, chain, txHash)
	if err != nil {
		return nil, mapDepositError(err)
	}

	return &pb.GetDepositSessionByTxHashResponse{
		Session: depositSessionToProto(session),
	}, nil
}

// GetDepositSessionPublicStatus retrieves public-safe fields of a deposit session by ID.
func (s *Server) GetDepositSessionPublicStatus(ctx context.Context, req *pb.GetDepositSessionPublicStatusRequest) (*pb.GetDepositSessionPublicStatusResponse, error) {
	if s.depositEngine == nil {
		return nil, status.Error(codes.Unimplemented, "deposit service not configured")
	}

	sessionID, err := parseUUID(req.GetSessionId(), "session_id")
	if err != nil {
		return nil, err
	}

	session, err := s.depositEngine.GetSessionPublicStatus(ctx, sessionID)
	if err != nil {
		return nil, mapDepositError(err)
	}

	return &pb.GetDepositSessionPublicStatusResponse{
		Id:             session.ID.String(),
		Status:         string(session.Status),
		Chain:          session.Chain,
		Token:          session.Token,
		DepositAddress: session.DepositAddress,
		ExpectedAmount: session.ExpectedAmount.String(),
		ReceivedAmount: session.ReceivedAmount.String(),
		ExpiresAt:      timestamppb.New(session.ExpiresAt),
	}, nil
}

// ── Proto conversion helpers ─────────────────────────────────────────────────

func depositSessionToProto(s *domain.DepositSession) *pb.DepositSession {
	ps := &pb.DepositSession{
		Id:               s.ID.String(),
		TenantId:         s.TenantID.String(),
		Status:           string(s.Status),
		Chain:            s.Chain,
		Token:            s.Token,
		DepositAddress:   s.DepositAddress,
		ExpectedAmount:   s.ExpectedAmount.String(),
		ReceivedAmount:   s.ReceivedAmount.String(),
		Currency:         string(s.Currency),
		CollectionFeeBps: int32(s.CollectionFeeBPS),
		FeeAmount:        s.FeeAmount.String(),
		NetAmount:        s.NetAmount.String(),
		SettlementPref:   string(s.SettlementPref),
		IdempotencyKey:   s.IdempotencyKey,
		ExpiresAt:        timestamppb.New(s.ExpiresAt),
		CreatedAt:        timestamppb.New(s.CreatedAt),
		UpdatedAt:        timestamppb.New(s.UpdatedAt),
		FailureReason:    s.FailureReason,
		FailureCode:      s.FailureCode,
	}
	if s.DetectedAt != nil {
		ps.DetectedAt = timestamppb.New(*s.DetectedAt)
	}
	if s.ConfirmedAt != nil {
		ps.ConfirmedAt = timestamppb.New(*s.ConfirmedAt)
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

func mapDepositError(err error) error {
	if err == nil {
		return nil
	}

	errStr := err.Error()

	// Map domain errors to gRPC status codes.
	switch {
	case containsCode(errStr, domain.CodeDepositNotFound):
		return status.Error(codes.NotFound, errStr)
	case containsCode(errStr, domain.CodeDepositExpired):
		return status.Error(codes.FailedPrecondition, errStr)
	case containsCode(errStr, domain.CodeCryptoDisabled):
		return status.Error(codes.PermissionDenied, errStr)
	case containsCode(errStr, domain.CodeChainNotSupported):
		return status.Error(codes.InvalidArgument, errStr)
	case containsCode(errStr, domain.CodeAddressPoolEmpty):
		return status.Error(codes.ResourceExhausted, errStr)
	case containsCode(errStr, domain.CodeInvalidTransition):
		return status.Error(codes.FailedPrecondition, errStr)
	default:
		return status.Error(codes.Internal, errStr)
	}
}

func containsCode(errStr string, code string) bool {
	return len(errStr) >= len(code) && contains(errStr, code)
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}


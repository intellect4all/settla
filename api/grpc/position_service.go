package grpc

import (
	"context"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/intellect4all/settla/gen/settla/v1"

	"github.com/intellect4all/settla/domain"
)

// PositionEngine handles tenant position transaction requests.
type PositionEngine interface {
	RequestTopUp(ctx context.Context, tenantID uuid.UUID, req domain.TopUpRequest) (*domain.PositionTransaction, error)
	RequestWithdrawal(ctx context.Context, tenantID uuid.UUID, req domain.WithdrawalRequest) (*domain.PositionTransaction, error)
	GetTransaction(ctx context.Context, tenantID, txID uuid.UUID) (*domain.PositionTransaction, error)
	ListTransactions(ctx context.Context, tenantID uuid.UUID, limit, offset int32) ([]domain.PositionTransaction, error)
	ListTransactionsCursor(ctx context.Context, tenantID uuid.UUID, pageSize int32, cursor time.Time) ([]domain.PositionTransaction, error)
}

// PositionEventStore provides position event history for tenant queries.
type PositionEventStore interface {
	GetPositionEventHistory(ctx context.Context, tenantID, positionID uuid.UUID, from, to time.Time, limit, offset int32) ([]domain.PositionEvent, error)
}

// WithPositionEngine sets the position transaction engine.
func WithPositionEngine(e PositionEngine) ServerOption {
	return func(s *Server) { s.positionEngine = e }
}

// WithPositionEventStore sets the position event store for event history queries.
func WithPositionEventStore(s PositionEventStore) ServerOption {
	return func(srv *Server) { srv.positionEventStore = s }
}

// Position Transaction RPCs

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

func (s *Server) RequestWithdrawal(ctx context.Context, req *pb.RequestWithdrawalRequest) (*pb.RequestWithdrawalResponse, error) {
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
	if err := validateNonEmpty("destination", req.GetDestination()); err != nil {
		return nil, err
	}

	amount, err := parseDecimal(req.GetAmount(), "amount")
	if err != nil {
		return nil, err
	}

	tx, err := s.positionEngine.RequestWithdrawal(ctx, tenantID, domain.WithdrawalRequest{
		Currency:    domain.Currency(req.GetCurrency()),
		Location:    req.GetLocation(),
		Amount:      amount,
		Method:      req.GetMethod(),
		Destination: req.GetDestination(),
	})
	if err != nil {
		return nil, mapDomainError(err)
	}

	return &pb.RequestWithdrawalResponse{
		Transaction: positionTransactionToProto(tx),
	}, nil
}

func (s *Server) GetPositionTransaction(ctx context.Context, req *pb.GetPositionTransactionRequest) (*pb.GetPositionTransactionResponse, error) {
	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}

	if s.positionEngine == nil {
		return nil, status.Error(codes.Unimplemented, "position management not configured")
	}

	txID, err := parseUUID(req.GetTransactionId(), "transaction_id")
	if err != nil {
		return nil, err
	}

	tx, err := s.positionEngine.GetTransaction(ctx, tenantID, txID)
	if err != nil {
		return nil, mapDomainError(err)
	}

	return &pb.GetPositionTransactionResponse{
		Transaction: positionTransactionToProto(tx),
	}, nil
}

func (s *Server) ListPositionTransactions(ctx context.Context, req *pb.ListPositionTransactionsRequest) (*pb.ListPositionTransactionsResponse, error) {
	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}

	if s.positionEngine == nil {
		return nil, status.Error(codes.Unimplemented, "position management not configured")
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
		offset := req.GetOffset()
		if offset < 0 {
			offset = 0
		}
		txns, err := s.positionEngine.ListTransactions(ctx, tenantID, pageSize, offset)
		if err != nil {
			return nil, mapDomainError(err)
		}

		pbTxns := make([]*pb.PositionTransaction, len(txns))
		for i := range txns {
			pbTxns[i] = positionTransactionToProto(&txns[i])
		}

		var nextToken string
		if int32(len(txns)) == pageSize && len(txns) > 0 {
			nextToken = txns[len(txns)-1].CreatedAt.Format(time.RFC3339Nano)
		}

		return &pb.ListPositionTransactionsResponse{
			Transactions:  pbTxns,
			TotalCount:    int32(len(txns)),
			NextPageToken: nextToken,
		}, nil
	}

	// Cursor-based path: parse timestamp from page_token.
	cursor, err := time.Parse(time.RFC3339Nano, pageToken)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid page_token: %v", err)
	}

	txns, err := s.positionEngine.ListTransactionsCursor(ctx, tenantID, pageSize, cursor)
	if err != nil {
		return nil, mapDomainError(err)
	}

	pbTxns := make([]*pb.PositionTransaction, len(txns))
	for i := range txns {
		pbTxns[i] = positionTransactionToProto(&txns[i])
	}

	var nextToken string
	if int32(len(txns)) == pageSize && len(txns) > 0 {
		nextToken = txns[len(txns)-1].CreatedAt.Format(time.RFC3339Nano)
	}

	return &pb.ListPositionTransactionsResponse{
		Transactions:  pbTxns,
		TotalCount:    int32(len(txns)),
		NextPageToken: nextToken,
	}, nil
}

func (s *Server) GetPositionEventHistory(ctx context.Context, req *pb.GetPositionEventHistoryRequest) (*pb.GetPositionEventHistoryResponse, error) {
	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}

	if s.positionEventStore == nil {
		return nil, status.Error(codes.Unimplemented, "position event history not configured")
	}

	if err := validateCurrencyCode(req.GetCurrency()); err != nil {
		return nil, err
	}
	if err := validateNonEmpty("location", req.GetLocation()); err != nil {
		return nil, err
	}

	// Look up position ID from treasury manager.
	pos, err := s.treasury.GetPosition(ctx, tenantID, domain.Currency(req.GetCurrency()), req.GetLocation())
	if err != nil {
		return nil, mapDomainError(err)
	}

	from := time.Now().AddDate(0, 0, -30) // default: last 30 days
	if req.GetFrom() != nil {
		from = req.GetFrom().AsTime()
	}
	to := time.Now()
	if req.GetTo() != nil {
		to = req.GetTo().AsTime()
	}

	limit := req.GetLimit()
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	offset := req.GetOffset()
	if offset < 0 {
		offset = 0
	}

	events, err := s.positionEventStore.GetPositionEventHistory(ctx, tenantID, pos.ID, from, to, limit, offset)
	if err != nil {
		return nil, mapDomainError(err)
	}

	pbEvents := make([]*pb.PositionEventEntry, len(events))
	for i := range events {
		pbEvents[i] = positionEventToProto(&events[i])
	}

	return &pb.GetPositionEventHistoryResponse{
		Events:     pbEvents,
		TotalCount: int32(len(events)),
	}, nil
}

// Proto conversion helpers

func positionTransactionToProto(tx *domain.PositionTransaction) *pb.PositionTransaction {
	if tx == nil {
		return nil
	}
	return &pb.PositionTransaction{
		Id:            tx.ID.String(),
		TenantId:      tx.TenantID.String(),
		Type:          string(tx.Type),
		Currency:      string(tx.Currency),
		Location:      tx.Location,
		Amount:        tx.Amount.String(),
		Status:        string(tx.Status),
		Method:        tx.Method,
		Destination:   tx.Destination,
		Reference:     tx.Reference,
		FailureReason: tx.FailureReason,
		CreatedAt:     timestamppb.New(tx.CreatedAt),
		UpdatedAt:     timestamppb.New(tx.UpdatedAt),
	}
}

func positionEventToProto(e *domain.PositionEvent) *pb.PositionEventEntry {
	if e == nil {
		return nil
	}
	return &pb.PositionEventEntry{
		Id:            e.ID.String(),
		PositionId:    e.PositionID.String(),
		TenantId:      e.TenantID.String(),
		EventType:     string(e.EventType),
		Amount:        e.Amount.String(),
		BalanceAfter:  e.BalanceAfter.String(),
		LockedAfter:   e.LockedAfter.String(),
		ReferenceId:   e.ReferenceID.String(),
		ReferenceType: e.ReferenceType,
		RecordedAt:    timestamppb.New(e.RecordedAt),
	}
}

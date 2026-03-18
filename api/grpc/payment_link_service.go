package grpc

import (
	"context"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	paymentlinkcore "github.com/intellect4all/settla/core/paymentlink"
	"github.com/intellect4all/settla/domain"
	pb "github.com/intellect4all/settla/gen/settla/v1"
)

// WithPaymentLinkService sets the payment link service.
func WithPaymentLinkService(svc *paymentlinkcore.Service) ServerOption {
	return func(s *Server) { s.paymentLinkService = svc }
}

// WithPaymentLinkBaseURL sets the base URL for constructing payment link URLs.
func WithPaymentLinkBaseURL(url string) ServerOption {
	return func(s *Server) { s.paymentLinkBaseURL = url }
}

// CreatePaymentLink creates a new shareable payment link.
func (s *Server) CreatePaymentLink(ctx context.Context, req *pb.CreatePaymentLinkRequest) (*pb.CreatePaymentLinkResponse, error) {
	if s.paymentLinkService == nil {
		return nil, status.Error(codes.Unimplemented, "payment link service not configured")
	}

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

	if err := validateNonEmpty("chain", req.GetChain()); err != nil {
		return nil, err
	}
	if err := validateNonEmpty("token", req.GetToken()); err != nil {
		return nil, err
	}

	createReq := paymentlinkcore.CreateRequest{
		Description:    req.GetDescription(),
		RedirectURL:    req.GetRedirectUrl(),
		Amount:         amount,
		Currency:       req.GetCurrency(),
		Chain:          req.GetChain(),
		Token:          req.GetToken(),
		SettlementPref: domain.SettlementPreference(req.GetSettlementPref()),
		TTLSeconds:     req.GetTtlSeconds(),
	}

	if req.GetUseLimit() > 0 {
		v := int(req.GetUseLimit())
		createReq.UseLimit = &v
	}

	if req.GetExpiresAtUnix() > 0 {
		v := req.GetExpiresAtUnix()
		createReq.ExpiresAt = &v
	}

	result, err := s.paymentLinkService.Create(ctx, tenantID, createReq)
	if err != nil {
		return nil, mapPaymentLinkError(err)
	}

	return &pb.CreatePaymentLinkResponse{
		Link: paymentLinkToProto(result.Link, result.URL),
	}, nil
}

// GetPaymentLink retrieves a payment link by ID.
func (s *Server) GetPaymentLink(ctx context.Context, req *pb.GetPaymentLinkRequest) (*pb.GetPaymentLinkResponse, error) {
	if s.paymentLinkService == nil {
		return nil, status.Error(codes.Unimplemented, "payment link service not configured")
	}

	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}
	linkID, err := parseUUID(req.GetLinkId(), "link_id")
	if err != nil {
		return nil, err
	}

	link, err := s.paymentLinkService.Get(ctx, tenantID, linkID)
	if err != nil {
		return nil, mapPaymentLinkError(err)
	}

	return &pb.GetPaymentLinkResponse{
		Link: paymentLinkToProto(link, fmt.Sprintf("%s/%s", s.paymentLinkBaseURL, link.ShortCode)),
	}, nil
}

// ListPaymentLinks lists payment links for a tenant.
func (s *Server) ListPaymentLinks(ctx context.Context, req *pb.ListPaymentLinksRequest) (*pb.ListPaymentLinksResponse, error) {
	if s.paymentLinkService == nil {
		return nil, status.Error(codes.Unimplemented, "payment link service not configured")
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

	result, err := s.paymentLinkService.List(ctx, tenantID, limit, offset)
	if err != nil {
		return nil, mapPaymentLinkError(err)
	}

	pbLinks := make([]*pb.PaymentLink, len(result.Links))
	for i := range result.Links {
		pbLinks[i] = paymentLinkToProto(&result.Links[i], fmt.Sprintf("%s/%s", s.paymentLinkBaseURL, result.Links[i].ShortCode))
	}

	return &pb.ListPaymentLinksResponse{
		Links: pbLinks,
		Total: result.Total,
	}, nil
}

// ResolvePaymentLink resolves a payment link by short code (public).
func (s *Server) ResolvePaymentLink(ctx context.Context, req *pb.ResolvePaymentLinkRequest) (*pb.ResolvePaymentLinkResponse, error) {
	if s.paymentLinkService == nil {
		return nil, status.Error(codes.Unimplemented, "payment link service not configured")
	}

	if err := validateNonEmpty("short_code", req.GetShortCode()); err != nil {
		return nil, err
	}

	link, err := s.paymentLinkService.Resolve(ctx, req.GetShortCode())
	if err != nil {
		return nil, mapPaymentLinkError(err)
	}

	return &pb.ResolvePaymentLinkResponse{
		Link: paymentLinkToProto(link, fmt.Sprintf("%s/%s", s.paymentLinkBaseURL, link.ShortCode)),
	}, nil
}

// RedeemPaymentLink creates a deposit session from a payment link (public).
func (s *Server) RedeemPaymentLink(ctx context.Context, req *pb.RedeemPaymentLinkRequest) (*pb.RedeemPaymentLinkResponse, error) {
	if s.paymentLinkService == nil {
		return nil, status.Error(codes.Unimplemented, "payment link service not configured")
	}

	if err := validateNonEmpty("short_code", req.GetShortCode()); err != nil {
		return nil, err
	}

	result, err := s.paymentLinkService.Redeem(ctx, req.GetShortCode())
	if err != nil {
		return nil, mapPaymentLinkError(err)
	}

	return &pb.RedeemPaymentLinkResponse{
		Session: depositSessionToProto(result.Session),
		Link:    paymentLinkToProto(result.Link, fmt.Sprintf("%s/%s", s.paymentLinkBaseURL, result.Link.ShortCode)),
	}, nil
}

// DisablePaymentLink disables a payment link.
func (s *Server) DisablePaymentLink(ctx context.Context, req *pb.DisablePaymentLinkRequest) (*pb.DisablePaymentLinkResponse, error) {
	if s.paymentLinkService == nil {
		return nil, status.Error(codes.Unimplemented, "payment link service not configured")
	}

	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}
	linkID, err := parseUUID(req.GetLinkId(), "link_id")
	if err != nil {
		return nil, err
	}

	if err := s.paymentLinkService.Disable(ctx, tenantID, linkID); err != nil {
		return nil, mapPaymentLinkError(err)
	}

	return &pb.DisablePaymentLinkResponse{}, nil
}

// ── Proto conversion helper ──────────────────────────────────────────────────

func paymentLinkToProto(link *domain.PaymentLink, url string) *pb.PaymentLink {
	pl := &pb.PaymentLink{
		Id:          link.ID.String(),
		TenantId:    link.TenantID.String(),
		ShortCode:   link.ShortCode,
		Description: link.Description,
		RedirectUrl: link.RedirectURL,
		Status:      string(link.Status),
		SessionConfig: &pb.PaymentLinkSessionConfig{
			Amount:         link.SessionConfig.Amount.String(),
			Currency:       link.SessionConfig.Currency,
			Chain:          link.SessionConfig.Chain,
			Token:          link.SessionConfig.Token,
			SettlementPref: string(link.SessionConfig.SettlementPref),
			TtlSeconds:     link.SessionConfig.TTLSeconds,
		},
		UseCount:  int32(link.UseCount),
		CreatedAt: timestamppb.New(link.CreatedAt),
		UpdatedAt: timestamppb.New(link.UpdatedAt),
		Url:       url,
	}

	if link.UseLimit != nil {
		pl.UseLimit = int32(*link.UseLimit)
		pl.HasUseLimit = true
	}
	if link.ExpiresAt != nil {
		pl.ExpiresAt = timestamppb.New(*link.ExpiresAt)
		pl.HasExpiresAt = true
	}

	return pl
}

// ── Error mapping ────────────────────────────────────────────────────────────

func mapPaymentLinkError(err error) error {
	if err == nil {
		return nil
	}

	errStr := err.Error()

	switch {
	case containsCode(errStr, domain.CodePaymentLinkNotFound):
		return status.Error(codes.NotFound, errStr)
	case containsCode(errStr, domain.CodePaymentLinkExpired):
		return status.Error(codes.FailedPrecondition, errStr)
	case containsCode(errStr, domain.CodePaymentLinkExhausted):
		return status.Error(codes.ResourceExhausted, errStr)
	case containsCode(errStr, domain.CodePaymentLinkDisabled):
		return status.Error(codes.FailedPrecondition, errStr)
	case containsCode(errStr, domain.CodeCryptoDisabled):
		return status.Error(codes.PermissionDenied, errStr)
	case containsCode(errStr, domain.CodeTenantSuspended):
		return status.Error(codes.FailedPrecondition, errStr)
	case containsCode(errStr, domain.CodeAddressPoolEmpty):
		return status.Error(codes.ResourceExhausted, errStr)
	default:
		return mapDomainError(err)
	}
}

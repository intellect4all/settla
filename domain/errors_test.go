package domain

import (
	"errors"
	"fmt"
	"testing"
)

func TestDomainError_ErrorWithoutWrapped(t *testing.T) {
	err := ErrQuoteExpired("q-123")
	got := err.Error()
	if got == "" {
		t.Fatal("expected non-empty error message")
	}
	if err.Unwrap() != nil {
		t.Error("expected nil Unwrap for error without wrapped cause")
	}
}

func TestDomainError_ErrorWithWrapped(t *testing.T) {
	cause := fmt.Errorf("connection refused")
	err := ErrProviderError("moonpay", cause)
	got := err.Error()
	if got == "" {
		t.Fatal("expected non-empty error message")
	}
	if !errors.Is(err.Unwrap(), cause) {
		t.Errorf("Unwrap: expected %v, got %v", cause, err.Unwrap())
	}
}

func TestDomainError_CodeAndUnwrap(t *testing.T) {
	sentinel := fmt.Errorf("underlying")

	tests := []struct {
		name       string
		err        *DomainError
		wantCode   string
		wantUnwrap bool // true if Unwrap should return non-nil
	}{
		// Simple message constructors
		{"QuoteExpired", ErrQuoteExpired("q1"), CodeQuoteExpired, false},
		{"InsufficientFunds", ErrInsufficientFunds("USD", "bank:clearing"), CodeInsufficientFunds, false},
		{"InvalidTransition", ErrInvalidTransition("CREATED", "COMPLETED"), CodeInvalidTransition, false},
		{"TenantSuspended", ErrTenantSuspended("t1"), CodeTenantSuspended, false},
		{"TenantNotFound", ErrTenantNotFound("t2"), CodeTenantNotFound, false},
		{"AmountTooLow", ErrAmountTooLow("5", "10"), CodeAmountTooLow, false},
		{"AmountTooHigh", ErrAmountTooHigh("1000000", "50000"), CodeAmountTooHigh, false},
		{"IdempotencyConflict", ErrIdempotencyConflict("key-1"), CodeIdempotencyConflict, false},
		{"OptimisticLock", ErrOptimisticLock("transfer", "id-1"), CodeOptimisticLock, false},
		{"DailyLimitExceeded", ErrDailyLimitExceeded("t3"), CodeDailyLimitExceeded, false},
		{"CorridorDisabled", ErrCorridorDisabled("GBP-NGN"), CodeCorridorDisabled, false},
		{"LedgerImbalance", ErrLedgerImbalance("debits != credits"), CodeLedgerImbalance, false},
		{"PositionLocked", ErrPositionLocked("GBP", "bank:clearing"), CodePositionLocked, false},
		{"Unauthorized", ErrUnauthorized("invalid token"), CodeUnauthorized, false},
		{"ReservationFailed", ErrReservationFailed("timeout"), CodeReservationFailed, false},
		{"ReservationLockTimeout", ErrReservationLockTimeout("GBP", "bank"), CodeReservationLockTimeout, false},
		{"ReservationInsufficientFunds", ErrReservationInsufficientFunds("GBP", "bank"), CodeReservationInsufficientFunds, false},
		{"CurrencyMismatch", ErrCurrencyMismatch("add", "GBP", "USD"), CodeCurrencyMismatch, false},
		{"AccountNotFound", ErrAccountNotFound("tenant:x:assets"), CodeAccountNotFound, false},
		{"TransferNotFound", ErrTransferNotFound("tf-1"), CodeTransferNotFound, false},
		{"BlockchainReorg", ErrBlockchainReorg("tron", 12345), CodeBlockchainReorg, false},
		{"EmailAlreadyExists", ErrEmailAlreadyExists("a@b.com"), CodeEmailAlreadyExists, false},
		{"EmailNotVerified", ErrEmailNotVerified("a@b.com"), CodeEmailNotVerified, false},
		{"SlugConflict", ErrSlugConflict("lemfi"), CodeSlugConflict, false},
		{"DepositNotFound", ErrDepositNotFound("d1"), CodeDepositNotFound, false},
		{"DepositExpired", ErrDepositExpired("d2"), CodeDepositExpired, false},
		{"CryptoDisabled", ErrCryptoDisabled("t1"), CodeCryptoDisabled, false},
		{"ChainNotSupported", ErrChainNotSupported("avalanche", "t1"), CodeChainNotSupported, false},
		{"AddressPoolEmpty", ErrAddressPoolEmpty("tron", "t1"), CodeAddressPoolEmpty, false},
		{"BankDepositsDisabled", ErrBankDepositsDisabled("t1"), CodeBankDepositsDisabled, false},
		{"CurrencyNotSupported", ErrCurrencyNotSupported("XYZ", "t1"), CodeCurrencyNotSupported, false},
		{"VirtualAccountPoolEmpty", ErrVirtualAccountPoolEmpty("GBP", "t1"), CodeVirtualAccountPoolEmpty, false},
		{"BankDepositNotFound", ErrBankDepositNotFound("bd1"), CodeBankDepositNotFound, false},
		{"PaymentLinkNotFound", ErrPaymentLinkNotFound("pl1"), CodePaymentLinkNotFound, false},
		{"PaymentLinkExpired", ErrPaymentLinkExpired("pl2"), CodePaymentLinkExpired, false},
		{"PaymentLinkExhausted", ErrPaymentLinkExhausted("pl3"), CodePaymentLinkExhausted, false},
		{"PaymentLinkDisabled", ErrPaymentLinkDisabled("pl4"), CodePaymentLinkDisabled, false},
		{"PaymentMismatch", ErrPaymentMismatch("s1", "100", "50"), CodePaymentMismatch, false},
		{"RateLimitExceeded", ErrRateLimitExceeded("t1"), CodeRateLimitExceeded, false},

		// No-args constructors
		{"InvalidCredentials", ErrInvalidCredentials(), CodeInvalidCredentials, false},
		{"TokenExpired", ErrTokenExpired(), CodeTokenExpired, false},
		{"ShortCodeCollision", ErrShortCodeCollision(), CodeShortCodeCollision, false},

		// With wrapped error
		{"ProviderError", ErrProviderError("moonpay", sentinel), CodeProviderError, true},
		{"ChainError", ErrChainError("tron", sentinel), CodeChainError, true},
		{"NetworkError", ErrNetworkError("dns lookup", sentinel), CodeNetworkError, true},
		{"CompensationFailed", ErrCompensationFailed("tf-1", sentinel), CodeCompensationFailed, true},
		{"ProviderUnavailable", ErrProviderUnavailable("yellowcard"), CodeProviderUnavailable, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err.Code() != tt.wantCode {
				t.Errorf("Code: got %q, want %q", tt.err.Code(), tt.wantCode)
			}
			if tt.err.Error() == "" {
				t.Error("Error() returned empty string")
			}
			if tt.wantUnwrap && tt.err.Unwrap() == nil {
				t.Error("expected non-nil Unwrap")
			}
			if !tt.wantUnwrap && tt.err.Unwrap() != nil {
				t.Errorf("expected nil Unwrap, got %v", tt.err.Unwrap())
			}
		})
	}
}

func TestDomainError_IsRetriable(t *testing.T) {
	tests := []struct {
		name string
		err  *DomainError
		want bool
	}{
		// Retriable
		{"ProviderError", ErrProviderError("p", nil), true},
		{"ProviderUnavailable", ErrProviderUnavailable("p"), true},
		{"NetworkError", ErrNetworkError("op", nil), true},
		{"ReservationLockTimeout", ErrReservationLockTimeout("GBP", "bank"), true},
		{"OptimisticLock", ErrOptimisticLock("transfer", "id"), true},
		{"RateLimitExceeded", ErrRateLimitExceeded("t1"), true},

		// Non-retriable
		{"QuoteExpired", ErrQuoteExpired("q"), false},
		{"InsufficientFunds", ErrInsufficientFunds("USD", "bank"), false},
		{"InvalidTransition", ErrInvalidTransition("A", "B"), false},
		{"LedgerImbalance", ErrLedgerImbalance("details"), false},
		{"TenantNotFound", ErrTenantNotFound("t"), false},
		{"TenantSuspended", ErrTenantSuspended("t"), false},
		{"DailyLimitExceeded", ErrDailyLimitExceeded("t"), false},
		{"IdempotencyConflict", ErrIdempotencyConflict("k"), false},
		{"ReservationInsufficientFunds", ErrReservationInsufficientFunds("GBP", "bank"), false},
		{"CompensationFailed", ErrCompensationFailed("tf", nil), false},
		{"ChainError", ErrChainError("tron", nil), false},
		{"BlockchainReorg", ErrBlockchainReorg("tron", 100), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.IsRetriable(); got != tt.want {
				t.Errorf("IsRetriable: got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsShortCodeCollision(t *testing.T) {
	if !IsShortCodeCollision(ErrShortCodeCollision()) {
		t.Error("expected true for ErrShortCodeCollision")
	}
	if IsShortCodeCollision(ErrTenantNotFound("t")) {
		t.Error("expected false for non-collision DomainError")
	}
	if IsShortCodeCollision(fmt.Errorf("plain error")) {
		t.Error("expected false for plain error")
	}
}

func TestDomainError_ErrorsAs(t *testing.T) {
	orig := ErrProviderError("moonpay", fmt.Errorf("timeout"))
	wrapped := fmt.Errorf("settla-worker: handling event: %w", orig)

	var de *DomainError
	if !errors.As(wrapped, &de) {
		t.Fatal("errors.As failed to extract DomainError from wrapped chain")
	}
	if de.Code() != CodeProviderError {
		t.Errorf("Code: got %q, want %q", de.Code(), CodeProviderError)
	}
}

package domain

import (
	"errors"
	"fmt"
)

// DomainError represents a structured domain-level error with a machine-readable
// code and a human-readable message. It optionally wraps an underlying error.
type DomainError struct {
	code    string
	message string
	err     error
}

// Error returns the human-readable error message.
func (e *DomainError) Error() string {
	if e.err != nil {
		return fmt.Sprintf("%s: %v", e.message, e.err)
	}
	return e.message
}

// Code returns the machine-readable error code.
func (e *DomainError) Code() string {
	return e.code
}

// Unwrap returns the underlying error, if any.
func (e *DomainError) Unwrap() error {
	return e.err
}

// IsRetriable returns true if the error represents a transient condition that
// may resolve on retry. Workers use this to decide whether to NAK (retry) or
// ACK (permanent failure) an event.
func (e *DomainError) IsRetriable() bool {
	switch e.code {
	case CodeProviderError, CodeProviderUnavailable, CodeNetworkError,
		CodeReservationLockTimeout, CodeOptimisticLock, CodeRateLimitExceeded:
		return true
	default:
		return false
	}
}

// Error code constants.
const (
	CodeQuoteExpired                 = "QUOTE_EXPIRED"
	CodeInsufficientFunds            = "INSUFFICIENT_FUNDS"
	CodeInvalidTransition            = "INVALID_TRANSITION"
	CodeProviderError                = "PROVIDER_ERROR"
	CodeChainError                   = "CHAIN_ERROR"
	CodeLedgerImbalance              = "LEDGER_IMBALANCE"
	CodePositionLocked               = "POSITION_LOCKED"
	CodeCorridorDisabled             = "CORRIDOR_DISABLED"
	CodeAmountTooLow                 = "AMOUNT_TOO_LOW"
	CodeAmountTooHigh                = "AMOUNT_TOO_HIGH"
	CodeIdempotencyConflict          = "IDEMPOTENCY_CONFLICT"
	CodeOptimisticLock               = "OPTIMISTIC_LOCK"
	CodeTenantSuspended              = "TENANT_SUSPENDED"
	CodeTenantNotFound               = "TENANT_NOT_FOUND"
	CodeDailyLimitExceeded           = "DAILY_LIMIT_EXCEEDED"
	CodeUnauthorized                 = "UNAUTHORIZED"
	CodeReservationFailed            = "RESERVATION_FAILED"             // Deprecated: use CodeReservationLockTimeout or CodeReservationInsufficientFunds
	CodeReservationLockTimeout       = "RESERVATION_LOCK_TIMEOUT"       // retryable — temporary lock contention
	CodeReservationInsufficientFunds = "RESERVATION_INSUFFICIENT_FUNDS" // NOT retryable — insufficient treasury balance
	CodeCurrencyMismatch             = "CURRENCY_MISMATCH"
	CodeAccountNotFound              = "ACCOUNT_NOT_FOUND"
	CodeTransferNotFound             = "TRANSFER_NOT_FOUND"
	CodeProviderUnavailable          = "PROVIDER_UNAVAILABLE"
	CodeNetworkError                 = "NETWORK_ERROR"
	CodeBlockchainReorg              = "BLOCKCHAIN_REORG"
	CodeCompensationFailed           = "COMPENSATION_FAILED"
	CodeRateLimitExceeded            = "RATE_LIMIT_EXCEEDED"
	CodeEmailAlreadyExists           = "EMAIL_ALREADY_EXISTS"
	CodeInvalidCredentials           = "INVALID_CREDENTIALS"
	CodeEmailNotVerified             = "EMAIL_NOT_VERIFIED"
	CodeTokenExpired                 = "TOKEN_EXPIRED"
	CodeSlugConflict                 = "SLUG_CONFLICT"
	CodeDepositNotFound              = "DEPOSIT_NOT_FOUND"
	CodeDepositExpired               = "DEPOSIT_EXPIRED"
	CodeCryptoDisabled               = "CRYPTO_DISABLED"
	CodeChainNotSupported            = "CHAIN_NOT_SUPPORTED"
	CodeAddressPoolEmpty             = "ADDRESS_POOL_EMPTY"
	CodeBankDepositsDisabled         = "BANK_DEPOSITS_DISABLED"
	CodeCurrencyNotSupported         = "CURRENCY_NOT_SUPPORTED"
	CodeVirtualAccountPoolEmpty      = "VIRTUAL_ACCOUNT_POOL_EMPTY"
	CodeBankDepositNotFound          = "BANK_DEPOSIT_NOT_FOUND"
	CodePaymentMismatch              = "PAYMENT_MISMATCH"
	CodePaymentLinkNotFound          = "PAYMENT_LINK_NOT_FOUND"
	CodePaymentLinkExpired           = "PAYMENT_LINK_EXPIRED"
	CodePaymentLinkExhausted         = "PAYMENT_LINK_EXHAUSTED"
	CodePaymentLinkDisabled          = "PAYMENT_LINK_DISABLED"
	CodeShortCodeCollision           = "SHORT_CODE_COLLISION"
)

// ErrQuoteExpired creates a domain error for an expired quote.
func ErrQuoteExpired(quoteID string) *DomainError {
	return &DomainError{code: CodeQuoteExpired, message: fmt.Sprintf("settla-domain: quote %s has expired", quoteID)}
}

// ErrInsufficientFunds creates a domain error for insufficient balance.
func ErrInsufficientFunds(currency, location string) *DomainError {
	return &DomainError{code: CodeInsufficientFunds, message: fmt.Sprintf("settla-domain: insufficient %s funds at %s", currency, location)}
}

// ErrInvalidTransition creates a domain error for an invalid state machine transition.
func ErrInvalidTransition(from, to string) *DomainError {
	return &DomainError{code: CodeInvalidTransition, message: fmt.Sprintf("settla-domain: invalid transition from %s to %s", from, to)}
}

// ErrProviderError creates a domain error wrapping a provider failure.
func ErrProviderError(providerID string, err error) *DomainError {
	return &DomainError{code: CodeProviderError, message: fmt.Sprintf("settla-domain: provider %s error", providerID), err: err}
}

// ErrChainError creates a domain error wrapping a blockchain failure.
func ErrChainError(chain string, err error) *DomainError {
	return &DomainError{code: CodeChainError, message: fmt.Sprintf("settla-domain: chain %s error", chain), err: err}
}

// ErrLedgerImbalance creates a domain error for an unbalanced journal entry.
func ErrLedgerImbalance(details string) *DomainError {
	return &DomainError{code: CodeLedgerImbalance, message: fmt.Sprintf("settla-domain: ledger imbalance: %s", details)}
}

// ErrPositionLocked creates a domain error for a locked treasury position.
func ErrPositionLocked(currency, location string) *DomainError {
	return &DomainError{code: CodePositionLocked, message: fmt.Sprintf("settla-domain: position %s at %s is locked", currency, location)}
}

// ErrCorridorDisabled creates a domain error for a disabled corridor.
func ErrCorridorDisabled(corridor string) *DomainError {
	return &DomainError{code: CodeCorridorDisabled, message: fmt.Sprintf("settla-domain: corridor %s is disabled", corridor)}
}

// ErrAmountTooLow creates a domain error when the amount is below the minimum.
func ErrAmountTooLow(amount, minimum string) *DomainError {
	return &DomainError{code: CodeAmountTooLow, message: fmt.Sprintf("settla-domain: amount %s below minimum %s", amount, minimum)}
}

// ErrAmountTooHigh creates a domain error when the amount exceeds the maximum.
func ErrAmountTooHigh(amount, maximum string) *DomainError {
	return &DomainError{code: CodeAmountTooHigh, message: fmt.Sprintf("settla-domain: amount %s exceeds maximum %s", amount, maximum)}
}

// ErrIdempotencyConflict creates a domain error for a duplicate idempotency key.
func ErrIdempotencyConflict(key string) *DomainError {
	return &DomainError{code: CodeIdempotencyConflict, message: fmt.Sprintf("settla-domain: idempotency key %s already used", key)}
}

// ErrOptimisticLock creates a domain error for an optimistic locking conflict.
func ErrOptimisticLock(entityType string, id string) *DomainError {
	return &DomainError{code: CodeOptimisticLock, message: fmt.Sprintf("settla-domain: optimistic lock conflict on %s %s", entityType, id)}
}

// ErrTenantSuspended creates a domain error for a suspended tenant.
func ErrTenantSuspended(tenantID string) *DomainError {
	return &DomainError{code: CodeTenantSuspended, message: fmt.Sprintf("settla-domain: tenant %s is suspended", tenantID)}
}

// ErrTenantNotFound creates a domain error for a missing tenant.
func ErrTenantNotFound(tenantID string) *DomainError {
	return &DomainError{code: CodeTenantNotFound, message: fmt.Sprintf("settla-domain: tenant %s not found", tenantID)}
}

// ErrDailyLimitExceeded creates a domain error when the daily limit is exceeded.
func ErrDailyLimitExceeded(tenantID string) *DomainError {
	return &DomainError{code: CodeDailyLimitExceeded, message: fmt.Sprintf("settla-domain: daily limit exceeded for tenant %s", tenantID)}
}

// ErrUnauthorized creates a domain error for unauthorized access.
func ErrUnauthorized(reason string) *DomainError {
	return &DomainError{code: CodeUnauthorized, message: fmt.Sprintf("settla-domain: unauthorized: %s", reason)}
}

// ErrReservationFailed creates a domain error for a failed treasury reservation.
func ErrReservationFailed(reason string) *DomainError {
	return &DomainError{code: CodeReservationFailed, message: fmt.Sprintf("settla-domain: reservation failed: %s", reason)}
}

// ErrReservationLockTimeout creates a retryable domain error for temporary lock
// contention on a treasury position. Workers should NAK and retry.
func ErrReservationLockTimeout(currency, location string) *DomainError {
	return &DomainError{code: CodeReservationLockTimeout, message: fmt.Sprintf("settla-domain: reservation lock timeout for %s at %s", currency, location)}
}

// ErrReservationInsufficientFunds creates a non-retryable domain error when a
// treasury position does not have enough available balance to cover the reservation.
func ErrReservationInsufficientFunds(currency, location string) *DomainError {
	return &DomainError{code: CodeReservationInsufficientFunds, message: fmt.Sprintf("settla-domain: insufficient treasury balance for %s at %s", currency, location)}
}

// ErrCurrencyMismatch creates a domain error for a currency mismatch.
func ErrCurrencyMismatch(operation, expected, actual string) *DomainError {
	return &DomainError{code: CodeCurrencyMismatch, message: fmt.Sprintf("settla-domain: cannot %s %s and %s: currency mismatch", operation, expected, actual)}
}

// ErrAccountNotFound creates a domain error for a missing account.
func ErrAccountNotFound(code string) *DomainError {
	return &DomainError{code: CodeAccountNotFound, message: fmt.Sprintf("settla-domain: account %s not found", code)}
}

// ErrTransferNotFound creates a domain error for a missing transfer.
func ErrTransferNotFound(transferID string) *DomainError {
	return &DomainError{code: CodeTransferNotFound, message: fmt.Sprintf("settla-domain: transfer %s not found", transferID)}
}

// ErrProviderUnavailable creates a domain error when a provider is unreachable.
func ErrProviderUnavailable(providerID string) *DomainError {
	return &DomainError{code: CodeProviderUnavailable, message: fmt.Sprintf("settla-domain: provider %s is unavailable", providerID)}
}

// ErrNetworkError creates a domain error wrapping a network failure.
func ErrNetworkError(operation string, err error) *DomainError {
	return &DomainError{code: CodeNetworkError, message: fmt.Sprintf("settla-domain: network error during %s", operation), err: err}
}

// ErrBlockchainReorg creates a domain error for a detected blockchain reorganization.
func ErrBlockchainReorg(chain string, blockNumber uint64) *DomainError {
	return &DomainError{code: CodeBlockchainReorg, message: fmt.Sprintf("settla-domain: blockchain reorg detected on %s at block %d", chain, blockNumber)}
}

// ErrCompensationFailed creates a domain error when a compensation action fails.
func ErrCompensationFailed(transferID string, err error) *DomainError {
	return &DomainError{code: CodeCompensationFailed, message: fmt.Sprintf("settla-domain: compensation failed for transfer %s", transferID), err: err}
}

// ErrRateLimitExceeded creates a domain error when a tenant exceeds their rate limit.
func ErrRateLimitExceeded(tenantID string) *DomainError {
	return &DomainError{code: CodeRateLimitExceeded, message: fmt.Sprintf("settla-domain: rate limit exceeded for tenant %s", tenantID)}
}

// ErrEmailAlreadyExists creates a domain error for a duplicate email registration.
func ErrEmailAlreadyExists(email string) *DomainError {
	return &DomainError{code: CodeEmailAlreadyExists, message: fmt.Sprintf("settla-domain: email %s is already registered", email)}
}

// ErrInvalidCredentials creates a domain error for failed authentication.
func ErrInvalidCredentials() *DomainError {
	return &DomainError{code: CodeInvalidCredentials, message: "settla-domain: invalid email or password"}
}

// ErrEmailNotVerified creates a domain error when login is attempted with unverified email.
func ErrEmailNotVerified(email string) *DomainError {
	return &DomainError{code: CodeEmailNotVerified, message: fmt.Sprintf("settla-domain: email %s is not verified", email)}
}

// ErrTokenExpired creates a domain error for an expired verification token.
func ErrTokenExpired() *DomainError {
	return &DomainError{code: CodeTokenExpired, message: "settla-domain: verification token has expired"}
}

// ErrSlugConflict creates a domain error when a generated slug already exists.
func ErrSlugConflict(slug string) *DomainError {
	return &DomainError{code: CodeSlugConflict, message: fmt.Sprintf("settla-domain: slug %s is already taken", slug)}
}

// ErrDepositNotFound creates a domain error for a missing deposit session.
func ErrDepositNotFound(sessionID string) *DomainError {
	return &DomainError{code: CodeDepositNotFound, message: fmt.Sprintf("settla-domain: deposit session %s not found", sessionID)}
}

// ErrDepositExpired creates a domain error for an expired deposit session.
func ErrDepositExpired(sessionID string) *DomainError {
	return &DomainError{code: CodeDepositExpired, message: fmt.Sprintf("settla-domain: deposit session %s has expired", sessionID)}
}

// ErrCryptoDisabled creates a domain error when crypto is not enabled for a tenant.
func ErrCryptoDisabled(tenantID string) *DomainError {
	return &DomainError{code: CodeCryptoDisabled, message: fmt.Sprintf("settla-domain: crypto deposits not enabled for tenant %s", tenantID)}
}

// ErrChainNotSupported creates a domain error when a chain is not supported by a tenant.
func ErrChainNotSupported(chain, tenantID string) *DomainError {
	return &DomainError{code: CodeChainNotSupported, message: fmt.Sprintf("settla-domain: chain %s not supported for tenant %s", chain, tenantID)}
}

// ErrAddressPoolEmpty creates a domain error when no addresses are available in the pool.
func ErrAddressPoolEmpty(chain, tenantID string) *DomainError {
	return &DomainError{code: CodeAddressPoolEmpty, message: fmt.Sprintf("settla-domain: no available addresses for chain %s tenant %s", chain, tenantID)}
}

// ErrBankDepositsDisabled creates a domain error when bank deposits are not enabled for a tenant.
func ErrBankDepositsDisabled(tenantID string) *DomainError {
	return &DomainError{code: CodeBankDepositsDisabled, message: fmt.Sprintf("settla-domain: bank deposits not enabled for tenant %s", tenantID)}
}

// ErrCurrencyNotSupported creates a domain error when a currency is not supported by a tenant or partner.
func ErrCurrencyNotSupported(currency, tenantID string) *DomainError {
	return &DomainError{code: CodeCurrencyNotSupported, message: fmt.Sprintf("settla-domain: currency %s not supported for tenant %s", currency, tenantID)}
}

// ErrVirtualAccountPoolEmpty creates a domain error when no virtual accounts are available in the pool.
func ErrVirtualAccountPoolEmpty(currency, tenantID string) *DomainError {
	return &DomainError{code: CodeVirtualAccountPoolEmpty, message: fmt.Sprintf("settla-domain: no available virtual accounts for currency %s tenant %s", currency, tenantID)}
}

// ErrBankDepositNotFound creates a domain error for a missing bank deposit session.
func ErrBankDepositNotFound(sessionID string) *DomainError {
	return &DomainError{code: CodeBankDepositNotFound, message: fmt.Sprintf("settla-domain: bank deposit session %s not found", sessionID)}
}

// ErrPaymentLinkNotFound creates a domain error for a missing payment link.
func ErrPaymentLinkNotFound(linkID string) *DomainError {
	return &DomainError{code: CodePaymentLinkNotFound, message: fmt.Sprintf("settla-domain: payment link %s not found", linkID)}
}

// ErrPaymentLinkExpired creates a domain error for an expired payment link.
func ErrPaymentLinkExpired(linkID string) *DomainError {
	return &DomainError{code: CodePaymentLinkExpired, message: fmt.Sprintf("settla-domain: payment link %s has expired", linkID)}
}

// ErrPaymentLinkExhausted creates a domain error when a payment link has reached its use limit.
func ErrPaymentLinkExhausted(linkID string) *DomainError {
	return &DomainError{code: CodePaymentLinkExhausted, message: fmt.Sprintf("settla-domain: payment link %s has reached its use limit", linkID)}
}

// ErrPaymentLinkDisabled creates a domain error for a disabled payment link.
func ErrPaymentLinkDisabled(linkID string) *DomainError {
	return &DomainError{code: CodePaymentLinkDisabled, message: fmt.Sprintf("settla-domain: payment link %s is disabled", linkID)}
}

// ErrShortCodeCollision creates a domain error when a generated short code already exists.
func ErrShortCodeCollision() *DomainError {
	return &DomainError{code: CodeShortCodeCollision, message: "settla-domain: generated short code already exists"}
}

// IsShortCodeCollision returns true if the error is a short code collision.
func IsShortCodeCollision(err error) bool {
	var domErr *DomainError
	if errors.As(err, &domErr) {
		return domErr.Code() == CodeShortCodeCollision
	}
	return false
}

// ErrPaymentMismatch creates a domain error when the received amount does not match the expected amount.
func ErrPaymentMismatch(sessionID, expected, received string) *DomainError {
	return &DomainError{code: CodePaymentMismatch, message: fmt.Sprintf("settla-domain: payment mismatch for session %s: expected %s, received %s", sessionID, expected, received)}
}

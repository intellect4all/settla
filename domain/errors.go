package domain

import "fmt"

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

// Error code constants.
const (
	CodeQuoteExpired        = "QUOTE_EXPIRED"
	CodeInsufficientFunds   = "INSUFFICIENT_FUNDS"
	CodeInvalidTransition   = "INVALID_TRANSITION"
	CodeProviderError       = "PROVIDER_ERROR"
	CodeChainError          = "CHAIN_ERROR"
	CodeLedgerImbalance     = "LEDGER_IMBALANCE"
	CodePositionLocked      = "POSITION_LOCKED"
	CodeCorridorDisabled    = "CORRIDOR_DISABLED"
	CodeAmountTooLow        = "AMOUNT_TOO_LOW"
	CodeAmountTooHigh       = "AMOUNT_TOO_HIGH"
	CodeIdempotencyConflict = "IDEMPOTENCY_CONFLICT"
	CodeOptimisticLock      = "OPTIMISTIC_LOCK"
	CodeTenantSuspended     = "TENANT_SUSPENDED"
	CodeTenantNotFound      = "TENANT_NOT_FOUND"
	CodeDailyLimitExceeded  = "DAILY_LIMIT_EXCEEDED"
	CodeUnauthorized        = "UNAUTHORIZED"
	CodeReservationFailed   = "RESERVATION_FAILED"
	CodeCurrencyMismatch    = "CURRENCY_MISMATCH"
	CodeAccountNotFound     = "ACCOUNT_NOT_FOUND"
	CodeTransferNotFound    = "TRANSFER_NOT_FOUND"
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

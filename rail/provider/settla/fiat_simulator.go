package settla

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// defaultDelay is used for currencies not in the delay table.
const defaultDelay = 10 * time.Second

// defaultCurrencyDelays defines the min/max processing delays for each fiat currency,
// modelling realistic banking rail timings.
var defaultCurrencyDelays = map[string][2]time.Duration{
	"NGN": {3 * time.Second, 5 * time.Second},   // NIP instant payments
	"GBP": {5 * time.Second, 10 * time.Second},  // UK Faster Payments
	"USD": {10 * time.Second, 30 * time.Second}, // ACH
	"EUR": {5 * time.Second, 10 * time.Second},  // SEPA Instant
	"GHS": {5 * time.Second, 10 * time.Second},  // GhIPSS
}

// SimulatorConfig holds tuneable knobs for the fiat simulator.
type SimulatorConfig struct {
	// FailureRate is the fraction of transactions that should fail [0,1].
	// Default: 0.02 (2%).
	FailureRate float64

	// CurrencyDelays overrides per-currency [min, max] processing delays.
	// If nil, defaultCurrencyDelays is used. Useful for testing with short delays.
	CurrencyDelays map[string][2]time.Duration
}

// DefaultSimulatorConfig returns a config with production-like defaults.
func DefaultSimulatorConfig() SimulatorConfig {
	return SimulatorConfig{FailureRate: 0.02}
}

// FiatSimulator simulates fiat banking rails with realistic timing and failure rates.
// All methods are safe for concurrent use.
type FiatSimulator struct {
	cfg SimulatorConfig

	mu  sync.RWMutex
	txs map[string]*FiatTransaction
}

// NewFiatSimulator creates a simulator with the provided config.
func NewFiatSimulator(cfg SimulatorConfig) *FiatSimulator {
	return &FiatSimulator{
		cfg: cfg,
		txs: make(map[string]*FiatTransaction),
	}
}

// SimulateCollection initiates a fiat collection (inbound):
//
//	PENDING → PROCESSING → COLLECTED  (or FAILED)
//
// The status progression runs asynchronously; callers poll via GetStatus.
func (s *FiatSimulator) SimulateCollection(ctx context.Context, amount decimal.Decimal, currency, ref string) (*FiatTransaction, error) {
	if amount.IsNegative() || amount.IsZero() {
		return nil, fmt.Errorf("settla-fiat-simulator: amount must be positive, got %s", amount)
	}

	tx := s.newTx(FiatTxCollection, amount, currency, ref)

	// Snapshot before storing+launching so the caller gets an immutable initial view.
	snap := s.snapshot(tx)

	s.store(tx)

	go s.runCollection(tx.ID, currency)

	return snap, nil
}

// SimulatePayout initiates a fiat payout (outbound):
//
//	PAYOUT_INITIATED → PAYOUT_PROCESSING → COMPLETED  (or FAILED)
//
// recipient is a free-form reference such as a bank account or phone number.
func (s *FiatSimulator) SimulatePayout(ctx context.Context, amount decimal.Decimal, currency, recipient string) (*FiatTransaction, error) {
	if amount.IsNegative() || amount.IsZero() {
		return nil, fmt.Errorf("settla-fiat-simulator: amount must be positive, got %s", amount)
	}

	tx := s.newTx(FiatTxPayout, amount, currency, recipient)

	// Snapshot before storing+launching so the caller gets an immutable initial view.
	snap := s.snapshot(tx)

	s.store(tx)

	go s.runPayout(tx.ID, currency)

	return snap, nil
}

// GetStatus returns a snapshot of the current state of a simulated fiat transaction.
func (s *FiatSimulator) GetStatus(txID string) (*FiatTransaction, error) {
	s.mu.RLock()
	tx, ok := s.txs[txID]
	if !ok {
		s.mu.RUnlock()
		return nil, fmt.Errorf("settla-fiat-simulator: transaction %q not found", txID)
	}
	cp := s.snapshot(tx)
	s.mu.RUnlock()
	return cp, nil
}

// --- internal helpers ---

// snapshot returns a copy of tx. Must be called with at least s.mu.RLock held
// (or before the pointer is published to other goroutines).
func (s *FiatSimulator) snapshot(tx *FiatTransaction) *FiatTransaction {
	cp := *tx
	history := make([]StatusChange, len(tx.History))
	copy(history, tx.History)
	cp.History = history
	return &cp
}

func (s *FiatSimulator) newTx(txType FiatTxType, amount decimal.Decimal, currency, ref string) *FiatTransaction {
	now := time.Now().UTC()
	tx := &FiatTransaction{
		ID:        uuid.New().String(),
		Type:      txType,
		Amount:    amount,
		Currency:  currency,
		Reference: ref,
		BankRef:   generateBankRef(),
		CreatedAt: now,
	}

	initialStatus := FiatStatusPending
	if txType == FiatTxPayout {
		initialStatus = FiatStatusPayoutInitiated
	}

	tx.Status = initialStatus
	tx.History = []StatusChange{{Status: initialStatus, Timestamp: now}}

	return tx
}

func (s *FiatSimulator) store(tx *FiatTransaction) {
	s.mu.Lock()
	s.txs[tx.ID] = tx
	s.mu.Unlock()
}

func (s *FiatSimulator) transition(txID string, status FiatStatus, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, ok := s.txs[txID]
	if !ok {
		return
	}

	now := time.Now().UTC()
	tx.Status = status
	tx.History = append(tx.History, StatusChange{
		Status:    status,
		Timestamp: now,
		Reason:    reason,
	})

	if status == FiatStatusCollected || status == FiatStatusCompleted || status == FiatStatusFailed {
		tx.CompletedAt = &now
	}
}

// runCollection drives a collection through its status lifecycle.
func (s *FiatSimulator) runCollection(txID, currency string) {
	// Simulate processing delay — half for first hop, half for second.
	total := s.delay(currency)
	half := total / 2

	time.Sleep(half)
	s.transition(txID, FiatStatusProcessing, "bank transfer received")

	time.Sleep(half)

	if s.shouldFail() {
		s.transition(txID, FiatStatusFailed, "bank rejected transfer")
		return
	}
	s.transition(txID, FiatStatusCollected, "funds cleared")
}

// runPayout drives a payout through its status lifecycle.
func (s *FiatSimulator) runPayout(txID, currency string) {
	total := s.delay(currency)
	third := total / 3

	time.Sleep(third)
	s.transition(txID, FiatStatusPayoutProcessing, "payout instruction sent to bank")

	time.Sleep(total - third)

	if s.shouldFail() {
		s.transition(txID, FiatStatusFailed, "payout rejected by beneficiary bank")
		return
	}
	s.transition(txID, FiatStatusCompleted, "payout confirmed by bank")
}

// shouldFail returns true with probability equal to the configured failure rate.
func (s *FiatSimulator) shouldFail() bool {
	return rand.Float64() < s.cfg.FailureRate
}

// delay returns a random duration within the currency's delay range.
func (s *FiatSimulator) delay(currency string) time.Duration {
	table := s.cfg.CurrencyDelays
	if table == nil {
		table = defaultCurrencyDelays
	}
	bounds, ok := table[currency]
	if !ok {
		return defaultDelay
	}
	min, max := bounds[0], bounds[1]
	span := max - min
	return min + time.Duration(rand.Int64N(int64(span)))
}

// generateBankRef produces a short alphanumeric bank reference, e.g. "BNK-A3F9C2".
func generateBankRef() string {
	const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, 6)
	for i := range b {
		b[i] = chars[rand.IntN(len(chars))]
	}
	return "BNK-" + string(b)
}

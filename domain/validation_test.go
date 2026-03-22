package domain

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// ── IsStablecoin ───────────────────────────────────────────────────────

func TestIsStablecoin(t *testing.T) {
	tests := []struct {
		currency Currency
		want     bool
	}{
		{CurrencyUSDT, true},
		{CurrencyUSDC, true},
		{CurrencyGBP, false},
		{CurrencyNGN, false},
		{CurrencyUSD, false},
		{CurrencyEUR, false},
		{Currency("UNKNOWN"), false},
	}
	for _, tt := range tests {
		t.Run(string(tt.currency), func(t *testing.T) {
			if got := IsStablecoin(tt.currency); got != tt.want {
				t.Errorf("IsStablecoin(%s): got %v, want %v", tt.currency, got, tt.want)
			}
		})
	}
}

// ── Sender.Validate ────────────────────────────────────────────────────

func TestSenderValidate(t *testing.T) {
	tests := []struct {
		name    string
		sender  Sender
		wantErr bool
		errMsg  string
	}{
		{"valid_name_only", Sender{Name: "Alice"}, false, ""},
		{"valid_name_email", Sender{Name: "Alice", Email: "a@b.com"}, false, ""},
		{"empty_name", Sender{Name: ""}, true, "name is required"},
		{"invalid_email", Sender{Name: "Alice", Email: "not-an-email"}, true, "not a valid email"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.sender.Validate()
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("expected error containing %q, got: %v", tt.errMsg, err)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// ── Recipient.Validate ─────────────────────────────────────────────────

func TestRecipientValidate(t *testing.T) {
	tests := []struct {
		name    string
		r       Recipient
		wantErr bool
		errMsg  string
	}{
		{"valid_minimal", Recipient{Name: "Bob", Country: "NG"}, false, ""},
		{"valid_full", Recipient{Name: "Bob", Country: "NG", AccountNumber: "12345678", BankName: "GTBank"}, false, ""},
		{"valid_with_dash", Recipient{Name: "Bob", Country: "NG", AccountNumber: "1234-5678", BankName: "GTBank"}, false, ""},
		{"empty_name", Recipient{Name: "", Country: "NG"}, true, "name is required"},
		{"empty_country", Recipient{Name: "Bob", Country: ""}, true, "country is required"},
		{"bank_name_too_long", Recipient{Name: "Bob", Country: "NG", BankName: strings.Repeat("A", 129)}, true, "at most 128"},
		{"account_too_short", Recipient{Name: "Bob", Country: "NG", AccountNumber: "123", BankName: "GTBank"}, true, "4-34 characters"},
		{"account_too_long", Recipient{Name: "Bob", Country: "NG", AccountNumber: strings.Repeat("1", 35), BankName: "GTBank"}, true, "4-34 characters"},
		{"account_special_char", Recipient{Name: "Bob", Country: "NG", AccountNumber: "1234@567", BankName: "GTBank"}, true, "invalid character"},
		{"account_without_bank", Recipient{Name: "Bob", Country: "NG", AccountNumber: "12345678"}, true, "bank_name is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.r.Validate()
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("expected error containing %q, got: %v", tt.errMsg, err)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// ── FeeBreakdown.Validate ──────────────────────────────────────────────

func TestFeeBreakdownValidate(t *testing.T) {
	d := func(v float64) decimal.Decimal { return decimal.NewFromFloat(v) }

	tests := []struct {
		name    string
		fb      FeeBreakdown
		wantErr bool
		errMsg  string
	}{
		{"valid", FeeBreakdown{OnRampFee: d(1), NetworkFee: d(2), OffRampFee: d(3), TotalFeeUSD: d(6)}, false, ""},
		{"valid_zero_fees", FeeBreakdown{}, false, ""},
		{"negative_onramp", FeeBreakdown{OnRampFee: d(-1), NetworkFee: d(2), OffRampFee: d(3), TotalFeeUSD: d(4)}, true, "OnRampFee must be non-negative"},
		{"negative_network", FeeBreakdown{OnRampFee: d(1), NetworkFee: d(-2), OffRampFee: d(3), TotalFeeUSD: d(2)}, true, "NetworkFee must be non-negative"},
		{"negative_offramp", FeeBreakdown{OnRampFee: d(1), NetworkFee: d(2), OffRampFee: d(-3), TotalFeeUSD: d(0)}, true, "OffRampFee must be non-negative"},
		{"total_mismatch", FeeBreakdown{OnRampFee: d(1), NetworkFee: d(2), OffRampFee: d(3), TotalFeeUSD: d(7)}, true, "does not equal sum"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.fb.Validate()
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("expected error containing %q, got: %v", tt.errMsg, err)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestFeeBreakdownValidateWithSchedule(t *testing.T) {
	d := func(v float64) decimal.Decimal { return decimal.NewFromFloat(v) }

	validFB := FeeBreakdown{OnRampFee: d(1), NetworkFee: d(2), OffRampFee: d(3), TotalFeeUSD: d(6)}

	tests := []struct {
		name     string
		fb       FeeBreakdown
		schedule FeeSchedule
		wantErr  bool
		errMsg   string
	}{
		{"valid_within_bounds", validFB, FeeSchedule{MinFeeUSD: d(1), MaxFeeUSD: d(10)}, false, ""},
		{"valid_zero_bounds", validFB, FeeSchedule{}, false, ""},
		{"below_minimum", validFB, FeeSchedule{MinFeeUSD: d(10)}, true, "below schedule minimum"},
		{"above_maximum", validFB, FeeSchedule{MaxFeeUSD: d(5)}, true, "exceeds schedule maximum"},
		{"propagates_validate_error", FeeBreakdown{OnRampFee: d(-1)}, FeeSchedule{}, true, "non-negative"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.fb.ValidateWithSchedule(tt.schedule)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("expected error containing %q, got: %v", tt.errMsg, err)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// ── IdempotencyKey ─────────────────────────────────────────────────────

func TestNewIdempotencyKey(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		key, err := NewIdempotencyKey("my-key-123")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if key.String() != "my-key-123" {
			t.Errorf("got %q, want %q", key.String(), "my-key-123")
		}
	})

	t.Run("empty", func(t *testing.T) {
		_, err := NewIdempotencyKey("")
		if err == nil {
			t.Fatal("expected error for empty key")
		}
	})

	t.Run("too_long", func(t *testing.T) {
		_, err := NewIdempotencyKey(strings.Repeat("x", 257))
		if err == nil {
			t.Fatal("expected error for key >256 chars")
		}
	})

	t.Run("max_length", func(t *testing.T) {
		_, err := NewIdempotencyKey(strings.Repeat("x", 256))
		if err != nil {
			t.Fatalf("256 chars should be valid: %v", err)
		}
	})
}

// ── CryptoChain ────────────────────────────────────────────────────────

func TestCryptoChain_IsEVM(t *testing.T) {
	tests := []struct {
		chain CryptoChain
		want  bool
	}{
		{ChainEthereum, true},
		{ChainBase, true},
		{ChainPolygon, true},
		{ChainArbitrum, true},
		{ChainTron, false},
		{ChainSolana, false},
	}
	for _, tt := range tests {
		t.Run(string(tt.chain), func(t *testing.T) {
			if got := tt.chain.IsEVM(); got != tt.want {
				t.Errorf("IsEVM: got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidateChain(t *testing.T) {
	supported := []CryptoChain{ChainEthereum, ChainTron, ChainSolana, ChainBase, ChainPolygon, ChainArbitrum}
	for _, chain := range supported {
		if err := ValidateChain(chain); err != nil {
			t.Errorf("ValidateChain(%s): unexpected error: %v", chain, err)
		}
	}

	if err := ValidateChain(CryptoChain("avalanche")); err == nil {
		t.Error("expected error for unsupported chain")
	}
}

func TestValidChains(t *testing.T) {
	chains := ValidChains()
	if len(chains) != 6 {
		t.Fatalf("expected 6 chains, got %d", len(chains))
	}
	expected := map[CryptoChain]bool{
		ChainEthereum: true, ChainTron: true, ChainSolana: true,
		ChainBase: true, ChainPolygon: true, ChainArbitrum: true,
	}
	for _, c := range chains {
		if !expected[c] {
			t.Errorf("unexpected chain: %s", c)
		}
	}
}

// ── Outbox Must* variants ──────────────────────────────────────────────

func TestMustNewOutboxEvent_Valid(t *testing.T) {
	entry := MustNewOutboxEvent("transfer", uuid.New(), uuid.New(), EventTransferCreated, []byte(`{}`))
	if entry.ID == uuid.Nil {
		t.Error("expected non-nil ID for valid event type")
	}
	if entry.IsIntent {
		t.Error("expected IsIntent=false for event")
	}
}

func TestMustNewOutboxEvent_InvalidType(t *testing.T) {
	entry := MustNewOutboxEvent("transfer", uuid.New(), uuid.New(), "invalid.event.type", []byte(`{}`))
	if entry.ID != uuid.Nil {
		t.Error("expected nil ID for invalid event type")
	}
}

func TestMustNewOutboxIntent_Valid(t *testing.T) {
	entry := MustNewOutboxIntent("transfer", uuid.New(), uuid.New(), IntentTreasuryReserve, []byte(`{}`))
	if entry.ID == uuid.Nil {
		t.Error("expected non-nil ID for valid intent type")
	}
	if !entry.IsIntent {
		t.Error("expected IsIntent=true for intent")
	}
}

func TestMustNewOutboxIntent_InvalidType(t *testing.T) {
	entry := MustNewOutboxIntent("transfer", uuid.New(), uuid.New(), "invalid.intent.type", []byte(`{}`))
	if entry.ID != uuid.Nil {
		t.Error("expected nil ID for invalid intent type")
	}
}

func TestOutboxEntry_WithCorrelationID(t *testing.T) {
	original, err := NewOutboxEvent("transfer", uuid.New(), uuid.New(), EventTransferCreated, []byte(`{}`))
	if err != nil {
		t.Fatalf("NewOutboxEvent: %v", err)
	}

	corrID := uuid.New()
	withCorr := original.WithCorrelationID(corrID)

	if withCorr.CorrelationID != corrID {
		t.Errorf("CorrelationID: got %s, want %s", withCorr.CorrelationID, corrID)
	}
	// Original should be unchanged (value receiver returns a copy)
	if original.CorrelationID == corrID {
		t.Error("original entry should not be mutated")
	}
}

func TestValidateEventType(t *testing.T) {
	// Valid
	if err := ValidateEventType(EventTransferCreated); err != nil {
		t.Errorf("expected nil for valid event type, got: %v", err)
	}
	if err := ValidateEventType(IntentTreasuryReserve); err != nil {
		t.Errorf("expected nil for valid intent type, got: %v", err)
	}

	// Invalid
	if err := ValidateEventType("bogus.event.type"); err == nil {
		t.Error("expected error for invalid event type")
	}
}

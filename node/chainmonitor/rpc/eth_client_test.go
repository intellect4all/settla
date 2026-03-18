package rpc

import (
	"math/big"
	"testing"

	"github.com/shopspring/decimal"
)

func TestParseHexInt64(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    int64
		wantErr bool
	}{
		{"zero", "0x0", 0, false},
		{"one", "0x1", 1, false},
		{"block number", "0x10d4f1", 1103089, false},
		{"large number", "0xfffffffe", 4294967294, false},
		{"no prefix", "ff", 255, false},
		{"empty after prefix", "0x", 0, false},
		{"empty string", "", 0, false},
		{"uppercase prefix", "0XFF", 255, false},
		{"mixed case", "0xAbCdEf", 11259375, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseHexInt64(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseHexInt64(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("ParseHexInt64(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestEncodeHexInt64(t *testing.T) {
	tests := []struct {
		input int64
		want  string
	}{
		{0, "0x0"},
		{1, "0x1"},
		{255, "0xff"},
		{1103089, "0x10d4f1"},
		{4294967294, "0xfffffffe"},
	}

	for _, tt := range tests {
		got := EncodeHexInt64(tt.input)
		if got != tt.want {
			t.Errorf("EncodeHexInt64(%d) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestPadAddress(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			"with 0x prefix",
			"0xAbC123def456789012345678901234567890AbCd",
			"0x000000000000000000000000abc123def456789012345678901234567890abcd",
		},
		{
			"without prefix",
			"AbC123def456789012345678901234567890AbCd",
			"0x000000000000000000000000abc123def456789012345678901234567890abcd",
		},
		{
			"short address",
			"0x1234",
			"0x000000000000000000000000000000000000000000000000000000000000" + "1234",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PadAddress(tt.input)
			if got != tt.want {
				t.Errorf("PadAddress(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestExtractAddress(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			"full 32-byte topic",
			"0x000000000000000000000000abc123def456789012345678901234567890abcd",
			"0xabc123def456789012345678901234567890abcd",
		},
		{
			"already 20 bytes",
			"0xabc123def456789012345678901234567890abcd",
			"0xabc123def456789012345678901234567890abcd",
		},
		{
			"uppercase",
			"0x000000000000000000000000ABC123DEF456789012345678901234567890ABCD",
			"0xabc123def456789012345678901234567890abcd",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractAddress(tt.input)
			if got != tt.want {
				t.Errorf("ExtractAddress(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseHexAmount(t *testing.T) {
	tests := []struct {
		name     string
		data     string
		decimals int32
		want     string
	}{
		{
			"100 USDT (6 decimals)",
			"0x" + new(big.Int).Mul(big.NewInt(100), new(big.Int).Exp(big.NewInt(10), big.NewInt(6), nil)).Text(16),
			6,
			"100",
		},
		{
			"1.5 USDT (6 decimals)",
			"0x" + new(big.Int).SetInt64(1500000).Text(16),
			6,
			"1.5",
		},
		{
			"zero amount",
			"0x0",
			6,
			"0",
		},
		{
			"empty data",
			"0x",
			6,
			"0",
		},
		{
			"18 decimals (like ETH)",
			"0x" + new(big.Int).Mul(big.NewInt(1), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)).Text(16),
			18,
			"1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseHexAmount(tt.data, tt.decimals)
			want, _ := decimal.NewFromString(tt.want)
			if !got.Equal(want) {
				t.Errorf("ParseHexAmount(%q, %d) = %s, want %s", tt.data, tt.decimals, got, want)
			}
		})
	}
}

func TestParseERC20TransferLog(t *testing.T) {
	// Construct a valid ERC-20 Transfer log
	from := "0x000000000000000000000000aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	to := "0x000000000000000000000000bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	// 100 USDT = 100 * 10^6 = 100000000 = 0x5F5E100
	amount := "0x0000000000000000000000000000000000000000000000000000000005f5e100"

	log := ethLog{
		Address: "0xdac17f958d2ee523a2206206994597c13d831ec7",
		Topics: []string{
			erc20TransferTopic,
			from,
			to,
		},
		Data:            amount,
		BlockNumber:     "0x10d4f1",
		BlockHash:       "0xabcdef",
		TransactionHash: "0xtxhash123",
		LogIndex:        "0x3",
	}

	transfer, err := ParseERC20TransferLog(log, 6)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if transfer.TxHash != "0xtxhash123" {
		t.Errorf("TxHash = %q, want %q", transfer.TxHash, "0xtxhash123")
	}
	if transfer.BlockNumber != 1103089 {
		t.Errorf("BlockNumber = %d, want %d", transfer.BlockNumber, 1103089)
	}
	if transfer.LogIndex != 3 {
		t.Errorf("LogIndex = %d, want %d", transfer.LogIndex, 3)
	}
	if transfer.From != "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("From = %q, want %q", transfer.From, "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	}
	if transfer.To != "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Errorf("To = %q, want %q", transfer.To, "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	}
	if !transfer.Amount.Equal(decimal.NewFromInt(100)) {
		t.Errorf("Amount = %s, want 100", transfer.Amount)
	}
	if transfer.TokenContract != "0xdac17f958d2ee523a2206206994597c13d831ec7" {
		t.Errorf("TokenContract = %q, want %q", transfer.TokenContract, "0xdac17f958d2ee523a2206206994597c13d831ec7")
	}
}

func TestParseERC20TransferLog_InsufficientTopics(t *testing.T) {
	log := ethLog{
		Topics: []string{erc20TransferTopic}, // only 1 topic, need 3
	}

	_, err := ParseERC20TransferLog(log, 6)
	if err == nil {
		t.Fatal("expected error for insufficient topics")
	}
}

func TestParseERC20TransferLog_WrongTopic(t *testing.T) {
	log := ethLog{
		Topics: []string{
			"0xdeadbeef",
			"0x000000000000000000000000aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"0x000000000000000000000000bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		},
	}

	_, err := ParseERC20TransferLog(log, 6)
	if err == nil {
		t.Fatal("expected error for wrong topic signature")
	}
}

func TestRoundTripHexInt64(t *testing.T) {
	values := []int64{0, 1, 42, 255, 65535, 1000000, 1<<32 - 1}
	for _, v := range values {
		encoded := EncodeHexInt64(v)
		decoded, err := ParseHexInt64(encoded)
		if err != nil {
			t.Errorf("round-trip failed for %d: encode=%q, error=%v", v, encoded, err)
			continue
		}
		if decoded != v {
			t.Errorf("round-trip mismatch: %d -> %q -> %d", v, encoded, decoded)
		}
	}
}

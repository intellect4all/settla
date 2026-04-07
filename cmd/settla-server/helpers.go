package main

import (
	"encoding/hex"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
)

// hexDecodeSecure decodes a hex string to bytes. Used for master seed loading.
func hexDecodeSecure(s string) ([]byte, error) {
	return hex.DecodeString(s)
}

// parseSyncThresholds parses a comma-separated list of CURRENCY:AMOUNT pairs.
// Example: "NGN:200000000,GHS:2000000,USD:150000"
func parseSyncThresholds(raw string) map[domain.Currency]decimal.Decimal {
	if raw == "" {
		return nil
	}
	result := make(map[domain.Currency]decimal.Decimal)
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		parts := strings.SplitN(entry, ":", 2)
		if len(parts) != 2 {
			continue
		}
		currency := domain.Currency(strings.TrimSpace(parts[0]))
		amount, err := decimal.NewFromString(strings.TrimSpace(parts[1]))
		if err != nil {
			continue
		}
		result[currency] = amount
	}
	return result
}

func decimalFromNumeric(n pgtype.Numeric) decimal.Decimal {
	if !n.Valid || n.Int == nil {
		return decimal.Zero
	}
	return decimal.NewFromBigInt(n.Int, n.Exp)
}

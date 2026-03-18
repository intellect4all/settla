package grpc

import (
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var countryCodeRegex = regexp.MustCompile(`^[A-Z]{2}$`)
var currencyCodeRegex = regexp.MustCompile(`^[A-Z]{3}$`)

func validateUUID(field, value string) error {
	if _, err := uuid.Parse(value); err != nil {
		return status.Errorf(codes.InvalidArgument, "[INVALID_%s] must be a valid UUID", strings.ToUpper(field))
	}
	return nil
}

func validateDecimalAmount(value string) error {
	d, err := decimal.NewFromString(value)
	if err != nil || d.LessThanOrEqual(decimal.Zero) {
		return status.Errorf(codes.InvalidArgument, "[AMOUNT_INVALID] must be a positive decimal")
	}
	return nil
}

func validateCountryCode(value string) error {
	if !countryCodeRegex.MatchString(value) {
		return status.Errorf(codes.InvalidArgument, "[INVALID_COUNTRY] must be ISO 3166-1 alpha-2")
	}
	return nil
}

func validateCurrencyCode(value string) error {
	if !currencyCodeRegex.MatchString(value) {
		return status.Errorf(codes.InvalidArgument, "[INVALID_CURRENCY] must be ISO 4217 alpha-3")
	}
	return nil
}

func validateNonEmpty(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return status.Errorf(codes.InvalidArgument, "[REQUIRED_%s] must not be empty", strings.ToUpper(field))
	}
	return nil
}

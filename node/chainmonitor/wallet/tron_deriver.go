// Package wallet provides HD wallet address derivation implementations
// for the deposit address pool system.
package wallet

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"

	"github.com/google/uuid"
	"github.com/intellect4all/settla/store/transferdb"
)

// Compile-time checks that both derivers satisfy HDWalletDeriver.
var (
	_ transferdb.HDWalletDeriver = (*TronDeriver)(nil)
	_ transferdb.HDWalletDeriver = (*StaticPoolDeriver)(nil)
)

// ── TronDeriver (production) ─────────────────────────────────────────────────

// TronDeriver derives Tron TRC-20 deposit addresses by calling an external
// signing service (HSM/vault). Keys never leave the signing service.
//
// The signing service is expected to accept POST requests with a JSON body:
//
//	{
//	  "tenant_id": "<uuid>",
//	  "chain": "tron",
//	  "derivation_path": "m/44'/195'/0'/0/42",
//	  "index": 42
//	}
//
// And return:
//
//	{ "address": "TXyz..." }
type TronDeriver struct {
	serviceURL string
	client     *http.Client
	logger     *slog.Logger
}

// NewTronDeriver creates a production Tron address deriver that delegates
// to an external signing service at serviceURL.
func NewTronDeriver(serviceURL string, client *http.Client, logger *slog.Logger) *TronDeriver {
	if client == nil {
		client = http.DefaultClient
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &TronDeriver{
		serviceURL: serviceURL,
		client:     client,
		logger:     logger,
	}
}

// deriveRequest is the JSON body sent to the signing service.
type deriveRequest struct {
	TenantID       string `json:"tenant_id"`
	Chain          string `json:"chain"`
	DerivationPath string `json:"derivation_path"`
	Index          int64  `json:"index"`
}

// deriveResponse is the JSON body returned by the signing service.
type deriveResponse struct {
	Address string `json:"address"`
	Error   string `json:"error,omitempty"`
}

// DeriveAddress calls the external signing service to derive a Tron address
// at BIP-44 path m/44'/195'/0'/0/{index}.
func (d *TronDeriver) DeriveAddress(ctx context.Context, tenantID uuid.UUID, chain string, index int64) (string, error) {
	path := fmt.Sprintf("m/44'/195'/0'/0/%d", index)

	reqBody := deriveRequest{
		TenantID:       tenantID.String(),
		Chain:          chain,
		DerivationPath: path,
		Index:          index,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("settla-wallet: marshalling derive request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.serviceURL, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("settla-wallet: creating HTTP request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := d.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("settla-wallet: calling signing service at %s: %w", d.serviceURL, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16)) // 64 KiB max
	if err != nil {
		return "", fmt.Errorf("settla-wallet: reading signing service response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("settla-wallet: signing service returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var result deriveResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("settla-wallet: decoding signing service response: %w", err)
	}

	if result.Error != "" {
		return "", fmt.Errorf("settla-wallet: signing service error: %s", result.Error)
	}

	if result.Address == "" {
		return "", fmt.Errorf("settla-wallet: signing service returned empty address for index %d", index)
	}

	d.logger.Debug("settla-wallet: derived address",
		"tenant_id", tenantID,
		"chain", chain,
		"index", index,
		"address", result.Address,
	)

	return result.Address, nil
}

// ── StaticPoolDeriver (development) ──────────────────────────────────────────

// StaticPoolDeriver returns addresses from a pre-configured list, cycling
// through them by index. This is intended for development and testing only.
type StaticPoolDeriver struct {
	addresses []string
	mu        sync.RWMutex
	logger    *slog.Logger
}

// NewStaticPoolDeriver creates a development deriver that returns addresses
// from the provided list. The index parameter in DeriveAddress is used modulo
// the list length, so any index is valid.
func NewStaticPoolDeriver(addresses []string, logger *slog.Logger) *StaticPoolDeriver {
	if logger == nil {
		logger = slog.Default()
	}
	return &StaticPoolDeriver{
		addresses: addresses,
		logger:    logger,
	}
}

// DeriveAddress returns a static address from the pre-configured pool.
// The address is selected as addresses[index % len(addresses)].
func (d *StaticPoolDeriver) DeriveAddress(_ context.Context, tenantID uuid.UUID, chain string, index int64) (string, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if len(d.addresses) == 0 {
		return "", fmt.Errorf("settla-wallet: static pool is empty, cannot derive address")
	}

	addr := d.addresses[int(index)%len(d.addresses)]

	d.logger.Debug("settla-wallet: static pool address",
		"tenant_id", tenantID,
		"chain", chain,
		"index", index,
		"address", addr,
	)

	return addr, nil
}

// DefaultTestAddresses returns a small set of Tron testnet addresses for development.
func DefaultTestAddresses() []string {
	return []string{
		"TN7hAYuEADM1EBQ9GgqRkfKkMkGSzPB5gH",
		"TVDGpn4hCSzJ5nkHPETYhne186xB5QXqmN",
		"TKrMoarSxVtpRpWaGqFGFYN3bXYVLpESmN",
		"TGehVcNhud84JDCGrNHKVz9jEAVKUpbuiv",
		"TJNAuBhbC5kWKjqrMWZCvN7YoSJW3SqfFD",
		"TSNEe5Tf4rnc9RDzuXcwtKw1JRajuSdsFi",
		"TFRYeU4CsmVp5DVhqiJ3amcLJ3FxkzbCpT",
		"TQn9Y2khEsLJW1ChVWFMSMeRDow5KcbLSE",
	}
}

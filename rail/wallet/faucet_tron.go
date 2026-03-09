package wallet

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const (
	defaultTronNileFaucetBase = "https://nileex.io"
	tronNileFaucetSendPath    = "/send/getJoinPage"
)

// tronNileFaucet requests TRX from the Tron Nile testnet faucet.
// Nile testnet docs: https://nileex.io
type tronNileFaucet struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	maxRetries int
	retryDelay time.Duration
}

func newTronFaucet(cfg FaucetConfig) *tronNileFaucet {
	base := cfg.TronNileFaucetURL
	if base == "" {
		base = defaultTronNileFaucetBase
	}

	return &tronNileFaucet{
		baseURL:    base,
		apiKey:     cfg.TronNileAPIKey,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		maxRetries: cfg.MaxRetries,
		retryDelay: cfg.RetryDelay,
	}
}

func (f *tronNileFaucet) Chain() Chain      { return ChainTron }
func (f *tronNileFaucet) IsAutomated() bool { return true }
func (f *tronNileFaucet) FaucetURL() string { return f.baseURL + "/join/getJoinPage" }

func (f *tronNileFaucet) Fund(ctx context.Context, address string) error {
	if !ValidateTronAddress(address) {
		return fmt.Errorf("settla-wallet: invalid Tron address: %s", address)
	}

	return withRetry(ctx, f.maxRetries, f.retryDelay, func() error {
		return f.requestTRX(ctx, address)
	})
}

func (f *tronNileFaucet) requestTRX(ctx context.Context, address string) error {
	formData := url.Values{"address": {address}}
	endpoint := f.baseURL + tronNileFaucetSendPath

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
		stringReader(formData.Encode()))
	if err != nil {
		return fmt.Errorf("settla-wallet: failed to build Tron faucet request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if f.apiKey != "" {
		req.Header.Set("TRON-PRO-API-KEY", f.apiKey)
	}

	resp, err := f.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("settla-wallet: Tron faucet request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))

	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusTooManyRequests:
		return fmt.Errorf("settla-wallet: Tron Nile faucet rate limited — retry after a few minutes")
	case http.StatusNotFound:
		return fmt.Errorf("settla-wallet: Tron Nile faucet endpoint not found at %s", endpoint)
	default:
		return fmt.Errorf("settla-wallet: Tron Nile faucet returned HTTP %d: %s",
			resp.StatusCode, string(body))
	}
}

// stringReader wraps a string as an io.Reader to avoid importing strings in every file.
type stringReaderT struct {
	s   string
	pos int
}

func stringReader(s string) io.Reader {
	return &stringReaderT{s: s}
}

func (r *stringReaderT) Read(p []byte) (n int, err error) {
	if r.pos >= len(r.s) {
		return 0, io.EOF
	}
	n = copy(p, r.s[r.pos:])
	r.pos += n
	return n, nil
}

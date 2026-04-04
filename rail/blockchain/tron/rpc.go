package tron

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

const (
	defaultHTTPTimeout = 30 * time.Second
	defaultMaxRetries  = 3
	defaultRetryDelay  = 2 * time.Second
)

// rpcClient handles HTTP communication with TronGrid API.
// All calls include retry logic and structured logging.
type rpcClient struct {
	httpClient *http.Client
	baseURL    string
	apiKey     string
	logger     *slog.Logger
}

func newRPCClient(baseURL, apiKey string, logger *slog.Logger) *rpcClient {
	return &rpcClient{
		httpClient: &http.Client{
			Timeout: defaultHTTPTimeout,
			Transport: &http.Transport{
				MaxIdleConns:        50,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		baseURL: baseURL,
		apiKey:  apiKey,
		logger:  logger,
	}
}

// post makes a POST request to the TronGrid API with retry logic.
func (r *rpcClient) post(ctx context.Context, path string, reqBody, respBody any) error {
	return r.doWithRetry(ctx, http.MethodPost, path, reqBody, respBody)
}

// get makes a GET request to the TronGrid API with retry logic.
func (r *rpcClient) get(ctx context.Context, path string, respBody any) error {
	return r.doWithRetry(ctx, http.MethodGet, path, nil, respBody)
}

func (r *rpcClient) doWithRetry(ctx context.Context, method, path string, reqBody, respBody any) error {
	var lastErr error
	for attempt := range defaultMaxRetries {
		if attempt > 0 {
			delay := time.Duration(attempt) * defaultRetryDelay
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}

		if err := r.do(ctx, method, path, reqBody, respBody); err == nil {
			return nil
		} else {
			lastErr = err
			r.logger.Warn("settla-tron: RPC call failed, will retry",
				"method", method,
				"path", path,
				"attempt", attempt+1,
				"max_attempts", defaultMaxRetries,
				"error", err,
			)
		}
	}
	return fmt.Errorf("settla-tron: RPC failed after %d attempts: %w", defaultMaxRetries, lastErr)
}

func (r *rpcClient) do(ctx context.Context, method, path string, reqBody, respBody any) error {
	url := r.baseURL + path
	start := time.Now()

	var bodyReader io.Reader
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("marshaling request body: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return fmt.Errorf("creating HTTP request: %w", err)
	}

	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if r.apiKey != "" {
		req.Header.Set("TRON-PRO-API-KEY", r.apiKey)
	}

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP request: %w", err)
	}
	defer resp.Body.Close()

	r.logger.Debug("settla-tron: RPC call",
		"method", method,
		"path", path,
		"status", resp.StatusCode,
		"duration_ms", time.Since(start).Milliseconds(),
	)

	// Limit response to 10 MB to prevent OOM
	const maxBody = 10 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return fmt.Errorf("reading response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, path, string(body))
	}

	if respBody != nil {
		if err := json.Unmarshal(body, respBody); err != nil {
			return fmt.Errorf("decoding response from %s: %w", path, err)
		}
	}
	return nil
}

// getAccount fetches account info (TRX balance + TRC20 balances).
func (r *rpcClient) getAccount(ctx context.Context, address string) (*TronAccount, error) {
	var resp TronAccount
	if err := r.get(ctx, "/v1/accounts/"+address, &resp); err != nil {
		return nil, fmt.Errorf("getAccount(%s): %w", address, err)
	}
	return &resp, nil
}

// triggerSmartContract calls POST /wallet/triggersmartcontract to build a TRC20 transaction.
func (r *rpcClient) triggerSmartContract(ctx context.Context, req TriggerSmartContractReq) (*TriggerSmartContractResp, error) {
	var resp TriggerSmartContractResp
	if err := r.post(ctx, "/wallet/triggersmartcontract", req, &resp); err != nil {
		return nil, fmt.Errorf("triggerSmartContract: %w", err)
	}
	if !resp.Result.Result {
		return nil, fmt.Errorf("triggerSmartContract rejected: %s", resp.Result.Message)
	}
	if resp.Transaction.RawDataHex == "" {
		return nil, fmt.Errorf("triggerSmartContract returned empty raw_data_hex")
	}
	return &resp, nil
}

// broadcastTransaction calls POST /wallet/broadcasttransaction.
func (r *rpcClient) broadcastTransaction(ctx context.Context, tx TronTx) (*BroadcastResp, error) {
	var resp BroadcastResp
	if err := r.post(ctx, "/wallet/broadcasttransaction", tx, &resp); err != nil {
		return nil, fmt.Errorf("broadcastTransaction: %w", err)
	}
	if !resp.Result {
		return nil, fmt.Errorf("broadcast rejected: code=%s message=%s", resp.Code, resp.Message)
	}
	return &resp, nil
}

// getTransactionInfo calls POST /wallet/gettransactioninfobyid.
func (r *rpcClient) getTransactionInfo(ctx context.Context, txHash string) (*GetTransactionInfoResp, error) {
	var resp GetTransactionInfoResp
	if err := r.post(ctx, "/wallet/gettransactioninfobyid", map[string]string{"value": txHash}, &resp); err != nil {
		return nil, fmt.Errorf("getTransactionInfo(%s): %w", txHash, err)
	}
	return &resp, nil
}

// getNowBlock returns the current block number.
func (r *rpcClient) getNowBlock(ctx context.Context) (int64, error) {
	var resp NowBlockResp
	if err := r.post(ctx, "/wallet/getnowblock", struct{}{}, &resp); err != nil {
		return 0, fmt.Errorf("getNowBlock: %w", err)
	}
	return resp.BlockHeader.RawData.Number, nil
}

// createTRXTransaction calls POST /wallet/createtransaction to build a native TRX transfer.
func (r *rpcClient) createTRXTransaction(ctx context.Context, ownerHex, toHex string, amountSUN int64) (*TronTx, error) {
	reqBody := map[string]any{
		"owner_address": ownerHex,
		"to_address":    toHex,
		"amount":        amountSUN,
	}
	var tx TronTx
	if err := r.post(ctx, "/wallet/createtransaction", reqBody, &tx); err != nil {
		return nil, fmt.Errorf("createTRXTransaction: %w", err)
	}
	if tx.RawDataHex == "" {
		return nil, fmt.Errorf("createTRXTransaction returned empty raw_data_hex")
	}
	return &tx, nil
}

// getTRC20Transfers fetches recent TRC20 transfers for an address.
// If contractAddress is non-empty, filters to only that token contract.
func (r *rpcClient) getTRC20Transfers(ctx context.Context, address, contractAddress string, limit int) (*TRC20TransfersResp, error) {
	path := fmt.Sprintf("/v1/accounts/%s/transactions/trc20?limit=%d&only_confirmed=true", address, limit)
	if contractAddress != "" {
		path += "&contract_address=" + contractAddress
	}
	var resp TRC20TransfersResp
	if err := r.get(ctx, path, &resp); err != nil {
		return nil, fmt.Errorf("getTRC20Transfers(%s): %w", address, err)
	}
	return &resp, nil
}

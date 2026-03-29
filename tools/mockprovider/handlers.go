package main

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// providerQuoteResponse mirrors domain.ProviderQuote for JSON serialization.
type providerQuoteResponse struct {
	ProviderID       string `json:"provider_id"`
	Rate             string `json:"rate"`
	Fee              string `json:"fee"`
	EstimatedSeconds int    `json:"estimated_seconds"`
}

// providerTxResponse mirrors domain.ProviderTx for JSON serialization.
type providerTxResponse struct {
	ID         string            `json:"id"`
	ExternalID string            `json:"external_id"`
	Status     string            `json:"status"`
	Amount     string            `json:"amount"`
	Currency   string            `json:"currency"`
	TxHash     string            `json:"tx_hash"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// chainTxResponse mirrors domain.ChainTx for JSON serialization.
type chainTxResponse struct {
	Hash          string `json:"hash"`
	Status        string `json:"status"`
	Confirmations int    `json:"confirmations"`
	BlockNumber   uint64 `json:"block_number"`
	Fee           string `json:"fee"`
}

// quoteRequest is the incoming quote request body.
type quoteRequest struct {
	ProviderID     string `json:"provider_id"`
	SourceCurrency string `json:"source_currency"`
	SourceAmount   string `json:"source_amount"`
	DestCurrency   string `json:"dest_currency"`
}

// executeRequest is the incoming execute request body.
type executeRequest struct {
	ProviderID     string `json:"provider_id"`
	Amount         string `json:"amount"`
	FromCurrency   string `json:"from_currency"`
	ToCurrency     string `json:"to_currency"`
	Reference      string `json:"reference"`
	IdempotencyKey string `json:"idempotency_key"`
}

// blockchainSendRequest is the incoming blockchain send request body.
type blockchainSendRequest struct {
	Chain  string `json:"chain"`
	From   string `json:"from"`
	To     string `json:"to"`
	Token  string `json:"token"`
	Amount string `json:"amount"`
}

// Known mock provider rates.
var mockRates = map[string]struct {
	rate string
	fee  string
}{
	"mock-onramp-gbp":  {rate: "1.25", fee: "0.50"},
	"mock-onramp-ngn":  {rate: "0.00065", fee: "0.50"},
	"mock-offramp-ngn": {rate: "1550", fee: "0.50"},
	"mock-offramp-gbp": {rate: "0.80", fee: "0.30"},
}

var txCounter atomic.Int64

// ProviderHandlers holds the provider-facing HTTP handlers.
type ProviderHandlers struct {
	cfg *Config
	log *RequestLog
}

// NewProviderHandlers creates a new ProviderHandlers.
func NewProviderHandlers(cfg *Config, log *RequestLog) *ProviderHandlers {
	return &ProviderHandlers{cfg: cfg, log: log}
}

// Register registers all provider-facing routes on the given mux.
func (h *ProviderHandlers) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/onramp/quote", h.handleQuote)
	mux.HandleFunc("POST /api/onramp/execute", h.handleExecute)
	mux.HandleFunc("POST /api/offramp/quote", h.handleQuote)
	mux.HandleFunc("POST /api/offramp/execute", h.handleExecute)
	mux.HandleFunc("GET /api/status/{txId}", h.handleGetStatus)
	mux.HandleFunc("POST /api/blockchain/send", h.handleBlockchainSend)
	mux.HandleFunc("GET /api/blockchain/balance", h.handleBlockchainBalance)
	mux.HandleFunc("GET /api/blockchain/transactions", h.handleBlockchainTransactions)
}

// applyLatencyAndErrors applies configured latency and checks for error injection.
// Returns true if an error was injected (response already written).
func (h *ProviderHandlers) applyLatencyAndErrors(w http.ResponseWriter, providerID string, start time.Time, path string) bool {
	snap := h.cfg.Get()

	// Apply latency.
	if snap.LatencyMs > 0 {
		time.Sleep(time.Duration(snap.LatencyMs) * time.Millisecond)
	}

	// Check provider-specific forced failure.
	if snap.ShouldFail(providerID) {
		h.writeError(w, snap.ErrorCode, snap.ErrorMessage, providerID, start, path)
		return true
	}

	// Check global error rate.
	if snap.ErrorRate > 0 && rand.Float64() < snap.ErrorRate {
		h.writeError(w, snap.ErrorCode, snap.ErrorMessage, providerID, start, path)
		return true
	}

	return false
}

func (h *ProviderHandlers) writeError(w http.ResponseWriter, code int, msg, providerID string, start time.Time, path string) {
	h.log.Add(LogEntry{
		Timestamp:  time.Now().UTC(),
		Method:     "POST",
		Path:       path,
		ProviderID: providerID,
		StatusCode: code,
		LatencyMs:  time.Since(start).Milliseconds(),
		Error:      msg,
	})
	writeJSON(w, code, map[string]string{"error": msg})
}

func (h *ProviderHandlers) handleQuote(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var req quoteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	if h.applyLatencyAndErrors(w, req.ProviderID, start, r.URL.Path) {
		return
	}

	rates, ok := mockRates[req.ProviderID]
	if !ok {
		rates = mockRates["mock-onramp-gbp"] // fallback
	}

	resp := providerQuoteResponse{
		ProviderID:       req.ProviderID,
		Rate:             rates.rate,
		Fee:              rates.fee,
		EstimatedSeconds: 30,
	}

	h.log.Add(LogEntry{
		Timestamp:  time.Now().UTC(),
		Method:     r.Method,
		Path:       r.URL.Path,
		ProviderID: req.ProviderID,
		StatusCode: http.StatusOK,
		LatencyMs:  time.Since(start).Milliseconds(),
	})
	writeJSON(w, http.StatusOK, resp)
}

func (h *ProviderHandlers) handleExecute(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var req executeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	if h.applyLatencyAndErrors(w, req.ProviderID, start, r.URL.Path) {
		return
	}

	txNum := txCounter.Add(1)
	txID := fmt.Sprintf("mock-tx-%d", txNum)
	txType := "onramp"
	if strings.Contains(r.URL.Path, "offramp") {
		txType = "offramp"
	}

	resp := providerTxResponse{
		ID:         txID,
		ExternalID: fmt.Sprintf("ext-%s-%d", txType, txNum),
		Status:     "completed",
		Amount:     req.Amount,
		Currency:   req.ToCurrency,
		TxHash:     fmt.Sprintf("0x%040d", txNum),
		Metadata: map[string]string{
			"provider":   req.ProviderID,
			"type":       txType,
			"reference":  req.Reference,
			"mock":       "true",
			"latency_ms": fmt.Sprintf("%d", time.Since(start).Milliseconds()),
		},
	}

	h.log.Add(LogEntry{
		Timestamp:  time.Now().UTC(),
		Method:     r.Method,
		Path:       r.URL.Path,
		ProviderID: req.ProviderID,
		StatusCode: http.StatusOK,
		LatencyMs:  time.Since(start).Milliseconds(),
	})
	writeJSON(w, http.StatusOK, resp)
}

func (h *ProviderHandlers) handleGetStatus(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	txID := r.PathValue("txId")

	providerID := r.URL.Query().Get("provider_id")
	if h.applyLatencyAndErrors(w, providerID, start, r.URL.Path) {
		return
	}

	resp := providerTxResponse{
		ID:         txID,
		ExternalID: "ext-" + txID,
		Status:     "completed",
		Amount:     "0",
		Currency:   "USDT",
		TxHash:     "0x" + txID,
	}

	h.log.Add(LogEntry{
		Timestamp:  time.Now().UTC(),
		Method:     r.Method,
		Path:       r.URL.Path,
		ProviderID: providerID,
		StatusCode: http.StatusOK,
		LatencyMs:  time.Since(start).Milliseconds(),
	})
	writeJSON(w, http.StatusOK, resp)
}

func (h *ProviderHandlers) handleBlockchainSend(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var req blockchainSendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	chain := req.Chain
	if chain == "" {
		chain = "tron"
	}

	if h.applyLatencyAndErrors(w, "mock-"+chain, start, r.URL.Path) {
		return
	}

	txNum := txCounter.Add(1)
	resp := chainTxResponse{
		Hash:          fmt.Sprintf("mock-tx-%s-%d", chain, txNum),
		Status:        "confirmed",
		Confirmations: 20,
		BlockNumber:   uint64(1000000 + txNum),
		Fee:           "0.10",
	}

	h.log.Add(LogEntry{
		Timestamp:  time.Now().UTC(),
		Method:     r.Method,
		Path:       r.URL.Path,
		ProviderID: "mock-" + chain,
		StatusCode: http.StatusOK,
		LatencyMs:  time.Since(start).Milliseconds(),
	})
	writeJSON(w, http.StatusOK, resp)
}

func (h *ProviderHandlers) handleBlockchainBalance(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	chain := r.URL.Query().Get("chain")
	if chain == "" {
		chain = "tron"
	}

	if h.applyLatencyAndErrors(w, "mock-"+chain, start, r.URL.Path) {
		return
	}

	h.log.Add(LogEntry{
		Timestamp:  time.Now().UTC(),
		Method:     r.Method,
		Path:       r.URL.Path,
		ProviderID: "mock-" + chain,
		StatusCode: http.StatusOK,
		LatencyMs:  time.Since(start).Milliseconds(),
	})
	writeJSON(w, http.StatusOK, map[string]string{"balance": "999999999.00"})
}

// handleBlockchainTransactions simulates blockchain RPC transaction scanning.
// This is what a chain monitor (EVM/Tron poller) would poll for deposit detection.
// Returns mock stablecoin transfer events for watched addresses.
//
// Query params:
//   - address: the watched deposit address
//   - from_block: block number to scan from (ignored, returns mock data)
//   - chain: blockchain identifier (default: tron)
func (h *ProviderHandlers) handleBlockchainTransactions(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	address := r.URL.Query().Get("address")
	chain := r.URL.Query().Get("chain")
	if chain == "" {
		chain = "tron"
	}

	if h.applyLatencyAndErrors(w, "mock-"+chain, start, r.URL.Path) {
		return
	}

	// Check if there are any pending simulated deposits for this address.
	deposits := h.cfg.GetPendingDeposits(address)

	type txEvent struct {
		TxHash        string `json:"tx_hash"`
		From          string `json:"from"`
		To            string `json:"to"`
		Token         string `json:"token"`
		Amount        string `json:"amount"`
		BlockNumber   uint64 `json:"block_number"`
		Confirmations int    `json:"confirmations"`
		Status        string `json:"status"`
	}

	events := make([]txEvent, len(deposits))
	for i, d := range deposits {
		events[i] = txEvent{
			TxHash:        d.TxHash,
			From:          d.From,
			To:            d.To,
			Token:         d.Token,
			Amount:        d.Amount,
			BlockNumber:   d.BlockNumber,
			Confirmations: 20,
			Status:        "confirmed",
		}
	}

	h.log.Add(LogEntry{
		Timestamp:  time.Now().UTC(),
		Method:     r.Method,
		Path:       r.URL.Path,
		ProviderID: "mock-" + chain,
		StatusCode: http.StatusOK,
		LatencyMs:  time.Since(start).Milliseconds(),
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"transactions": events,
		"block_number": 1000000 + txCounter.Load(),
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

package tron

import (
	"encoding/json"
	"time"
)

// TronConfig holds configuration for the Tron Nile testnet client.
type TronConfig struct {
	// RPCURL is the TronGrid API base URL.
	RPCURL string

	// APIKey is the TronGrid API key (free tier available at trongrid.io).
	APIKey string

	// ExplorerURL is the block explorer base URL for transaction links.
	ExplorerURL string

	// USDTContract is the TRC20 USDT contract address (Base58Check format).
	USDTContract string

	// BlockTime is the approximate block time for polling.
	BlockTime time.Duration

	// Confirmations is the number of block confirmations required for finality.
	Confirmations int

	// TRXUSDRate is the TRX/USD exchange rate used for gas cost estimates.
	// If zero, defaults to 0.115. In production this should come from the FX oracle.
	TRXUSDRate float64
}

// NileConfig is the default configuration for the Tron Nile testnet.
var NileConfig = TronConfig{
	RPCURL:        "https://nile.trongrid.io",
	ExplorerURL:   "https://nile.tronscan.org",
	USDTContract:  "TXLAQ63Xg1NAzckPwKHvzw7CSEmLMEqcdj", // Nile TRC20 USDT
	BlockTime:     3 * time.Second,
	Confirmations: 19,
}

// TronAccount is the response from GET /v1/accounts/{address}.
type TronAccount struct {
	Data []struct {
		Address string              `json:"address"`
		Balance int64               `json:"balance"` // TRX balance in SUN
		TRC20   []map[string]string `json:"trc20"`
	} `json:"data"`
	Success bool `json:"success"`
}

// TriggerSmartContractReq is the request body for POST /wallet/triggersmartcontract.
type TriggerSmartContractReq struct {
	OwnerAddress     string `json:"owner_address"`
	ContractAddress  string `json:"contract_address"`
	FunctionSelector string `json:"function_selector"`
	Parameter        string `json:"parameter"`
	FeeLimit         int64  `json:"fee_limit"`
	CallValue        int64  `json:"call_value"`
}

// TriggerConstantReq is the request body for POST /wallet/triggerconstantcontract
// (read-only call simulation, no on-chain effect).
type TriggerConstantReq struct {
	OwnerAddress     string `json:"owner_address"`
	ContractAddress  string `json:"contract_address"`
	FunctionSelector string `json:"function_selector"`
	Parameter        string `json:"parameter"`
	CallValue        int64  `json:"call_value"`
}

// TriggerConstantResp is the response from POST /wallet/triggerconstantcontract.
type TriggerConstantResp struct {
	Result struct {
		Result bool `json:"result"`
	} `json:"result"`
	EnergyUsed     int64    `json:"energy_used"`
	ConstantResult []string `json:"constant_result"`
}

// TronTx represents a Tron transaction, signed or unsigned.
// The Signature field is populated after signing before broadcast.
type TronTx struct {
	Visible    bool            `json:"visible"`
	TxID       string          `json:"txID"`
	RawData    json.RawMessage `json:"raw_data"`
	RawDataHex string          `json:"raw_data_hex"`
	Signature  []string        `json:"signature,omitempty"`
}

// TriggerSmartContractResp is the response from POST /wallet/triggersmartcontract.
type TriggerSmartContractResp struct {
	Result struct {
		Result  bool   `json:"result"`
		Message string `json:"message,omitempty"`
	} `json:"result"`
	Transaction TronTx `json:"transaction"`
}

// BroadcastResp is the response from POST /wallet/broadcasttransaction.
type BroadcastResp struct {
	Result  bool   `json:"result"`
	TxID    string `json:"txid"` // lowercase in broadcast response
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// GetTransactionInfoResp is the response from POST /wallet/gettransactioninfobyid.
type GetTransactionInfoResp struct {
	ID             string `json:"id"`
	Fee            int64  `json:"fee"` // total fee in SUN
	BlockNumber    int64  `json:"blockNumber"`
	BlockTimeStamp int64  `json:"blockTimeStamp"`
	Receipt        struct {
		Result      string `json:"result"`
		EnergyUsage int64  `json:"energy_usage"`
		EnergyFee   int64  `json:"energy_fee"`
		NetUsage    int64  `json:"net_usage"`
		NetFee      int64  `json:"net_fee"`
	} `json:"receipt"`
}

// NowBlockResp is the response from POST /wallet/getnowblock.
type NowBlockResp struct {
	BlockHeader struct {
		RawData struct {
			Number int64 `json:"number"`
		} `json:"raw_data"`
	} `json:"block_header"`
}

// TRC20TransfersResp is the response from GET /v1/accounts/{address}/transactions/trc20.
type TRC20TransfersResp struct {
	Data []struct {
		TransactionID  string `json:"transaction_id"`
		BlockTimestamp int64  `json:"block_timestamp"`
		From           string `json:"from"`
		To             string `json:"to"`
		Value          string `json:"value"`
		TokenInfo      struct {
			Symbol   string `json:"symbol"`
			Address  string `json:"address"`
			Decimals int    `json:"decimals"`
		} `json:"token_info"`
	} `json:"data"`
	Success bool `json:"success"`
}

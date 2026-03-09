package wallet

import (
	"context"
	"fmt"
)

const (
	defaultBaseSepoliaFaucetURL = "https://www.coinbase.com/faucets/base-ethereum-goerli-faucet"
)

// baseSepoliaFaucet returns instructions for manually requesting Base Sepolia ETH.
//
// The Coinbase faucet for Base Sepolia requires a Coinbase account with on-chain
// history on mainnet. Use the FaucetURL to request ETH manually.
//
// Alternative: Bridge ETH from Ethereum Sepolia using the Base Bridge:
// https://sepolia-bridge.base.org
type baseSepoliaFaucet struct {
	faucetURL string
}

func newBaseFaucet(cfg FaucetConfig) *baseSepoliaFaucet {
	url := cfg.BaseSepoliaFaucetURL
	if url == "" {
		url = defaultBaseSepoliaFaucetURL
	}
	return &baseSepoliaFaucet{faucetURL: url}
}

func (f *baseSepoliaFaucet) Chain() Chain      { return ChainBase }
func (f *baseSepoliaFaucet) IsAutomated() bool { return false }
func (f *baseSepoliaFaucet) FaucetURL() string { return f.faucetURL }

func (f *baseSepoliaFaucet) Fund(_ context.Context, address string) error {
	return &ErrManualRequired{
		FaucetChain: ChainBase,
		URL:         f.faucetURL,
		Message: fmt.Sprintf(
			"request Base Sepolia ETH for %s — options: "+
				"Coinbase faucet (%s) requires mainnet history, "+
				"or bridge from Sepolia via https://sepolia-bridge.base.org",
			address, f.faucetURL,
		),
	}
}

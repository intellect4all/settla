package wallet

import (
	"context"
	"fmt"
)

const (
	sepoliaFaucetDocURL = "https://sepoliafaucet.com"
)

// ethereumSepoliaFaucet returns instructions for manually requesting Sepolia ETH.
//
// All public Sepolia ETH faucets require either:
//   - A Coinbase/Alchemy/Infura account
//   - Social media authentication (Twitter/Discord)
//   - CAPTCHA verification
//
// These requirements prevent fully automated funding. Use the FaucetURL to
// request ETH manually before running integration tests.
type ethereumSepoliaFaucet struct {
	faucetURL string
}

func newEthereumFaucet(cfg FaucetConfig) *ethereumSepoliaFaucet {
	url := cfg.EthereumFaucetURL
	if url == "" {
		url = sepoliaFaucetDocURL
	}
	return &ethereumSepoliaFaucet{faucetURL: url}
}

func (f *ethereumSepoliaFaucet) Chain() Chain      { return ChainEthereum }
func (f *ethereumSepoliaFaucet) IsAutomated() bool { return false }
func (f *ethereumSepoliaFaucet) FaucetURL() string { return f.faucetURL }

func (f *ethereumSepoliaFaucet) Fund(_ context.Context, address string) error {
	return &ErrManualRequired{
		FaucetChain: ChainEthereum,
		URL:         f.faucetURL,
		Message: fmt.Sprintf(
			"request Sepolia ETH for %s — options: "+
				"Alchemy (%s), Infura (https://www.infura.io/faucet/sepolia), "+
				"or QuickNode (https://faucet.quicknode.com/ethereum/sepolia)",
			address, f.faucetURL,
		),
	}
}

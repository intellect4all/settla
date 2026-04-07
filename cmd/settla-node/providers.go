package main

import (
	"context"
	"encoding/hex"
	"log/slog"
	"os"

	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/internal/appconfig"
	"github.com/intellect4all/settla/rail/blockchain"
	"github.com/intellect4all/settla/rail/provider"
	"github.com/intellect4all/settla/rail/provider/factory"
	railwallet "github.com/intellect4all/settla/rail/wallet"
)

func buildOnRampMap(reg *provider.Registry, logger *slog.Logger) map[string]domain.OnRampProvider {
	m := make(map[string]domain.OnRampProvider)
	for _, id := range reg.ListOnRampIDs(context.Background()) {
		p, err := reg.GetOnRamp(id)
		if err != nil {
			logger.Error("settla-node: failed to get on-ramp provider", "id", id, "error", err)
			continue
		}
		m[id] = p
	}
	return m
}

func buildOffRampMap(reg *provider.Registry, logger *slog.Logger) map[string]domain.OffRampProvider {
	m := make(map[string]domain.OffRampProvider)
	for _, id := range reg.ListOffRampIDs(context.Background()) {
		p, err := reg.GetOffRamp(id)
		if err != nil {
			logger.Error("settla-node: failed to get off-ramp provider", "id", id, "error", err)
			continue
		}
		m[id] = p
	}
	return m
}

func buildBlockchainMap(reg *provider.Registry, logger *slog.Logger) map[string]domain.BlockchainClient {
	m := make(map[string]domain.BlockchainClient)
	for _, chain := range reg.ListBlockchainChains() {
		c, err := reg.GetBlockchainClient(chain)
		if err != nil {
			logger.Error("settla-node: failed to get blockchain client", "chain", chain, "error", err)
			continue
		}
		m[string(chain)] = c
	}
	return m
}

func bootstrapProviders(cfg *appconfig.NodeConfig, logger *slog.Logger) (*provider.Registry, *blockchain.Registry, *railwallet.Manager) {
	providerMode := provider.ProviderMode(cfg.ProviderMode)
	var chainReg *blockchain.Registry
	var walletMgr *railwallet.Manager

	if providerMode == provider.ProviderModeTestnet || providerMode == provider.ProviderModeLive {
		if cfg.WalletEncryptionKey != "" && cfg.MasterSeedHex != "" {
			masterSeed, err := hex.DecodeString(cfg.MasterSeedHex)
			if err != nil {
				logger.Error("settla-node: invalid SETTLA_MASTER_SEED hex", "error", err)
				os.Exit(1)
			}
			walletMgr, err = railwallet.NewManager(railwallet.ManagerConfig{
				MasterSeed:    masterSeed,
				EncryptionKey: cfg.WalletEncryptionKey,
				StoragePath:   cfg.WalletStoragePath,
				Logger:        logger,
			})
			if err != nil {
				logger.Error("settla-node: failed to create wallet manager", "error", err)
				os.Exit(1)
			}
			logger.Info("settla-node: wallet manager initialized", "storage_path", cfg.WalletStoragePath)
		} else {
			logger.Warn("settla-node: wallet keys not set — blockchain clients will be read-only")
		}

		chainCfg := blockchain.LoadConfigFromEnv()
		var err error
		chainReg, err = blockchain.NewRegistryFromConfig(chainCfg, walletMgr, logger)
		if err != nil {
			logger.Error("settla-node: failed to create blockchain registry", "error", err)
			os.Exit(1)
		}
		if walletMgr != nil {
			if err := chainReg.RegisterSystemWallets(walletMgr); err != nil {
				logger.Warn("settla-node: some system wallets failed to register", "error", err)
			}
		}
	}

	bootstrapResult, err := factory.Bootstrap(factory.ProviderMode(providerMode), factory.Deps{
		Logger:        logger,
		BlockchainReg: chainReg,
	})
	if err != nil {
		logger.Error("settla-node: provider bootstrap failed", "error", err)
		os.Exit(1)
	}
	providerReg := provider.NewRegistry()
	for _, p := range bootstrapResult.OnRamps {
		providerReg.RegisterOnRamp(p)
	}
	for _, p := range bootstrapResult.OffRamps {
		providerReg.RegisterOffRamp(p)
	}
	for _, c := range bootstrapResult.Blockchains {
		providerReg.RegisterBlockchainClient(c)
	}
	for slug, n := range bootstrapResult.Normalizers {
		providerReg.RegisterNormalizer(slug, n)
	}
	for slug, l := range bootstrapResult.Listeners {
		providerReg.RegisterListener(slug, l)
	}
	if chainReg != nil {
		for _, ch := range chainReg.Chains() {
			c, _ := chainReg.GetClient(ch)
			if c != nil {
				providerReg.RegisterBlockchainClient(c)
			}
		}
	}
	logger.Info("settla-node: provider mode", "mode", cfg.ProviderMode)
	return providerReg, chainReg, walletMgr
}

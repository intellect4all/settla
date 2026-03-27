package chainmonitor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/node/chainmonitor/rpc"
)

// EVMPoller scans EVM-compatible chains (Ethereum, Base, etc.) for ERC-20
// transfers to watched addresses using eth_getLogs. On match, it writes a
// deposit transaction + outbox entry atomically via the OutboxWriter.
type EVMPoller struct {
	chain        string
	cfg          ChainConfig
	client       *rpc.EVMClient
	addresses    *AddressSet
	tokens       *TokenRegistry
	checkpoint   *CheckpointManager
	outboxWriter OutboxWriter
	logger       *slog.Logger
}

// NewEVMPoller creates a new EVM chain poller.
func NewEVMPoller(
	cfg ChainConfig,
	client *rpc.EVMClient,
	addresses *AddressSet,
	tokens *TokenRegistry,
	checkpoint *CheckpointManager,
	writer OutboxWriter,
	logger *slog.Logger,
) *EVMPoller {
	if logger == nil {
		logger = slog.Default()
	}
	return &EVMPoller{
		chain:        cfg.Chain,
		cfg:          cfg,
		client:       client,
		addresses:    addresses,
		tokens:       tokens,
		checkpoint:   checkpoint,
		outboxWriter: writer,
		logger:       logger.With("module", "evm-poller", "chain", cfg.Chain),
	}
}

// Poll executes one poll cycle: fetch new blocks since last checkpoint,
// scan for ERC-20 transfers to watched addresses, and write detected deposits.
func (p *EVMPoller) Poll(ctx context.Context) error {
	// 1. Load checkpoint
	lastBlock, lastHash, err := p.checkpoint.Load(ctx, p.chain)
	if err != nil {
		return fmt.Errorf("settla-evm-poller[%s]: loading checkpoint: %w", p.chain, err)
	}

	// 2. Get current block height
	currentBlock, err := p.client.GetLatestBlockNumber(ctx)
	if err != nil {
		return fmt.Errorf("settla-evm-poller[%s]: getting current block: %w", p.chain, err)
	}

	// Determine safe block (current - confirmations to avoid scanning unconfirmed blocks)
	safeBlock := currentBlock - int64(p.cfg.Confirmations)
	if safeBlock <= 0 {
		return nil // chain too young
	}

	// Start scanning from last checkpoint + 1, but re-scan reorgDepth blocks for safety
	startBlock := lastBlock + 1
	if lastBlock > 0 && p.cfg.ReorgDepth > 0 {
		reorgStart := lastBlock - int64(p.cfg.ReorgDepth)
		if reorgStart > 0 && reorgStart < startBlock {
			startBlock = reorgStart
		}
	}
	if startBlock <= 0 {
		startBlock = safeBlock - 10 // start near tip if no checkpoint
		if startBlock <= 0 {
			startBlock = 1
		}
	}

	if startBlock > safeBlock {
		return nil // already caught up
	}

	// Capture address snapshot for this cycle (lock-free)
	addrSnap := p.addresses.Snapshot()
	if len(addrSnap) == 0 {
		return nil // no addresses to watch
	}

	// 3. Scan blocks using eth_getLogs for ERC-20 transfers to watched addresses
	processed, err := p.scanTransfers(ctx, addrSnap, startBlock, safeBlock)
	if err != nil {
		return err
	}

	// 4. Save checkpoint at the safe block
	if processed > 0 || startBlock <= safeBlock {
		// Get the block hash for the safe block (for reorg detection)
		block, err := p.client.GetBlockByNumber(ctx, safeBlock)
		if err != nil {
			p.logger.Warn("settla-evm-poller: failed to get block for checkpoint",
				"block", safeBlock,
				"error", err,
			)
			// Still save with empty hash - better than not saving at all
			if err := p.checkpoint.Save(ctx, p.chain, safeBlock, ""); err != nil {
				return fmt.Errorf("settla-evm-poller[%s]: saving checkpoint: %w", p.chain, err)
			}
		} else {
			if err := p.checkpoint.Save(ctx, p.chain, safeBlock, block.Hash); err != nil {
				return fmt.Errorf("settla-evm-poller[%s]: saving checkpoint: %w", p.chain, err)
			}

			// Reorg detection: verify parent hash chain
			if lastHash != "" && block.ParentHash != "" {
				p.detectReorg(ctx, block, lastBlock, lastHash)
			}
		}
	}

	if processed > 0 {
		p.logger.Info("settla-evm-poller: poll cycle complete",
			"processed", processed,
			"start_block", startBlock,
			"safe_block", safeBlock,
		)
	}

	return nil
}

// scanTransfers uses eth_getLogs to batch-query for ERC-20 transfers to watched
// addresses across the block range. This is more efficient than per-address
// queries because a single RPC call covers all watched addresses.
func (p *EVMPoller) scanTransfers(ctx context.Context, addrSnap map[string]AddressInfo, startBlock, safeBlock int64) (int, error) {
	processed := 0

	// Get all contract addresses we're watching on this chain
	contracts := p.tokens.ContractAddresses(p.chain)
	if len(contracts) == 0 {
		return 0, nil
	}

	// Collect all watched addresses for this chain
	var watchedAddresses []string
	for key, info := range addrSnap {
		if !strings.HasPrefix(key, p.chain+":") {
			continue // skip addresses from other chains
		}
		watchedAddresses = append(watchedAddresses, info.Address)
	}
	if len(watchedAddresses) == 0 {
		return 0, nil
	}

	// Query each contract's ERC-20 Transfer events to our watched addresses
	for _, contract := range contracts {
		// Lookup token decimals for amount parsing
		token, ok := p.tokens.LookupByContract(p.chain, contract)
		decimals := int32(erc20DefaultDecimals)
		if ok && token.Decimals > 0 {
			decimals = token.Decimals
		}

		transfers, err := p.client.GetERC20Transfers(ctx, contract, watchedAddresses, startBlock, safeBlock, decimals)
		if err != nil {
			p.logger.Warn("settla-evm-poller: failed to get ERC20 transfers",
				"contract", contract,
				"from_block", startBlock,
				"to_block", safeBlock,
				"error", err,
			)
			continue
		}

		for _, t := range transfers {
			// Convert to domain type
			incoming := rpc.ParseEVMTransfer(t, p.chain)

			// Verify the recipient is one of our watched addresses
			normalizedTo := strings.ToLower(incoming.ToAddress)
			found := false
			for _, addr := range watchedAddresses {
				if strings.EqualFold(addr, normalizedTo) {
					found = true
					break
				}
			}
			if !found {
				continue
			}

			if err := p.processIncomingTx(ctx, incoming); err != nil {
				p.logger.Warn("settla-evm-poller: failed to process incoming tx",
					"tx_hash", incoming.TxHash,
					"to", incoming.ToAddress,
					"error", err,
				)
				continue
			}
			processed++
		}
	}

	return processed, nil
}

// processIncomingTx checks if the transaction has already been recorded,
// looks up the session, and writes the deposit tx + outbox entry atomically.
func (p *EVMPoller) processIncomingTx(ctx context.Context, incoming domain.IncomingTransaction) error {
	// Idempotency: check if already recorded
	existing, err := p.outboxWriter.GetDepositTxByHash(ctx, string(incoming.Chain), incoming.TxHash)
	if err == nil && existing != nil {
		return nil // already processed
	}

	// Look up which session this address belongs to
	session, err := p.outboxWriter.GetSessionByAddress(ctx, incoming.ToAddress)
	if err != nil {
		return fmt.Errorf("no session for address %s: %w", incoming.ToAddress, err)
	}

	// Verify the token is one we're watching
	_, tokenOK := p.tokens.LookupByContract(string(incoming.Chain), incoming.TokenContract)
	if !tokenOK {
		p.logger.Debug("settla-evm-poller: ignoring transfer for unwatched token",
			"tx_hash", incoming.TxHash,
			"contract", incoming.TokenContract,
		)
		return nil
	}

	// Build the deposit transaction
	dtx := &domain.DepositTransaction{
		SessionID:       session.ID,
		TenantID:        session.TenantID,
		Chain:           incoming.Chain,
		TxHash:          incoming.TxHash,
		FromAddress:     incoming.FromAddress,
		ToAddress:       incoming.ToAddress,
		TokenContract:   incoming.TokenContract,
		Amount:          incoming.Amount,
		BlockNumber:     incoming.BlockNumber,
		BlockHash:       incoming.BlockHash,
		Confirmations:   int32(p.cfg.Confirmations), // we only scan confirmed blocks
		RequiredConfirm: int32(p.cfg.Confirmations),
		DetectedAt:      incoming.Timestamp,
	}

	// Build outbox entry for deposit.tx.detected
	payloadBytes, err := json.Marshal(domain.DepositTxDetectedPayload{
		SessionID:   session.ID,
		TenantID:    session.TenantID,
		TxHash:      incoming.TxHash,
		Chain:       incoming.Chain,
		Token:       incoming.TokenContract,
		Amount:      incoming.Amount,
		BlockNumber: incoming.BlockNumber,
	})
	if err != nil {
		return fmt.Errorf("marshaling outbox payload: %w", err)
	}

	entries := []domain.OutboxEntry{
		{
			ID:            uuid.New(),
			AggregateType: "deposit_session",
			AggregateID:   session.ID,
			TenantID:      session.TenantID,
			EventType:     domain.EventDepositTxDetected,
			Payload:       payloadBytes,
			IsIntent:      false,
		},
	}

	// Atomic write: deposit tx + outbox entry
	if err := p.outboxWriter.WriteDetectedTx(ctx, dtx, entries); err != nil {
		return fmt.Errorf("writing detected tx %s: %w", incoming.TxHash, err)
	}

	p.logger.Info("settla-evm-poller: deposit transaction detected",
		"tx_hash", incoming.TxHash,
		"session_id", session.ID,
		"tenant_id", session.TenantID,
		"amount", incoming.Amount,
		"from", incoming.FromAddress,
		"to", incoming.ToAddress,
	)

	return nil
}

// detectReorg checks if the parent hash of the current block matches expectations,
// and logs a warning if a reorganisation is detected.
func (p *EVMPoller) detectReorg(ctx context.Context, block *rpc.EVMBlock, lastCheckpointBlock int64, lastCheckpointHash string) {
	// Only check if the block is right after our last checkpoint
	if block.Number != lastCheckpointBlock+1 {
		return
	}
	if block.ParentHash != lastCheckpointHash {
		p.logger.Warn("settla-evm-poller: possible chain reorganisation detected",
			"block", block.Number,
			"expected_parent_hash", lastCheckpointHash,
			"actual_parent_hash", block.ParentHash,
			"last_checkpoint_block", lastCheckpointBlock,
		)
	}
	_ = ctx // available for future reorg handling
}

// PollInterval returns the configured poll interval for this chain.
func (p *EVMPoller) PollInterval() time.Duration {
	return p.cfg.PollInterval
}

// Chain returns the chain identifier.
func (p *EVMPoller) Chain() string {
	return p.chain
}

const erc20DefaultDecimals = 6

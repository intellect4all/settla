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

// TronPoller scans TronGrid for TRC-20 transfers to watched addresses.
// On match, it writes a deposit transaction + outbox entry atomically via the OutboxWriter.
type TronPoller struct {
	chain       string
	cfg         ChainConfig
	client      *rpc.TronClient
	addresses   *AddressSet
	tokens      *TokenRegistry
	checkpoint  *CheckpointManager
	outboxWriter OutboxWriter
	logger      *slog.Logger
}

// OutboxWriter abstracts the atomic deposit-tx + outbox insertion.
// The implementation uses a single database transaction.
type OutboxWriter interface {
	// WriteDetectedTx atomically inserts a deposit transaction and outbox entries.
	WriteDetectedTx(ctx context.Context, dtx *domain.DepositTransaction, entries []domain.OutboxEntry) error
	// GetDepositTxByHash checks if a transaction has already been recorded.
	GetDepositTxByHash(ctx context.Context, chain, txHash string) (*domain.DepositTransaction, error)
	// GetSessionByAddress returns the active session for a deposit address.
	GetSessionByAddress(ctx context.Context, address string) (*domain.DepositSession, error)
}

// NewTronPoller creates a new Tron chain poller.
func NewTronPoller(
	cfg ChainConfig,
	client *rpc.TronClient,
	addresses *AddressSet,
	tokens *TokenRegistry,
	checkpoint *CheckpointManager,
	writer OutboxWriter,
	logger *slog.Logger,
) *TronPoller {
	if logger == nil {
		logger = slog.Default()
	}
	return &TronPoller{
		chain:        cfg.Chain,
		cfg:          cfg,
		client:       client,
		addresses:    addresses,
		tokens:       tokens,
		checkpoint:   checkpoint,
		outboxWriter: writer,
		logger:       logger.With("module", "tron-poller"),
	}
}

// Poll executes one poll cycle: fetch new blocks since last checkpoint,
// scan for TRC-20 transfers to watched addresses, and write detected deposits.
func (p *TronPoller) Poll(ctx context.Context) error {
	// 1. Load checkpoint
	lastBlock, lastHash, err := p.checkpoint.Load(ctx, p.chain)
	if err != nil {
		return fmt.Errorf("settla-tron-poller: loading checkpoint: %w", err)
	}

	// 2. Get current block height
	currentBlock, err := p.client.GetNowBlock(ctx)
	if err != nil {
		return fmt.Errorf("settla-tron-poller: getting current block: %w", err)
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

	// 3. Scan blocks using TRC20 transfer API per watched address
	processed, err := p.scanTransfers(ctx, addrSnap, lastBlock, lastHash, safeBlock)
	if err != nil {
		return err
	}

	// 4. Save checkpoint at the safe block
	if processed > 0 || startBlock <= safeBlock {
		// Get the block hash for the safe block
		block, err := p.client.GetBlockByNum(ctx, safeBlock)
		if err != nil {
			p.logger.Warn("settla-tron-poller: failed to get block for checkpoint",
				"block", safeBlock,
				"error", err,
			)
			// Still save with empty hash - better than not saving at all
			if err := p.checkpoint.Save(ctx, p.chain, safeBlock, ""); err != nil {
				return fmt.Errorf("settla-tron-poller: saving checkpoint: %w", err)
			}
		} else {
			if err := p.checkpoint.Save(ctx, p.chain, safeBlock, block.BlockID); err != nil {
				return fmt.Errorf("settla-tron-poller: saving checkpoint: %w", err)
			}

			// Reorg detection: verify parent hash chain
			if lastHash != "" && block.BlockHeader.RawData.ParentHash != "" {
				p.detectReorg(ctx, block, lastBlock, lastHash)
			}
		}
	}

	if processed > 0 {
		p.logger.Info("settla-tron-poller: poll cycle complete",
			"processed", processed,
			"start_block", startBlock,
			"safe_block", safeBlock,
		)
	}

	return nil
}

// scanTransfers queries TRC20 transfers for all watched addresses and processes matches.
func (p *TronPoller) scanTransfers(ctx context.Context, addrSnap map[string]AddressInfo, lastBlock int64, _ string, safeBlock int64) (int, error) {
	processed := 0

	// Calculate min timestamp from lastBlock (approximate: Tron ~3s per block)
	var minTimestamp int64
	if lastBlock > 0 {
		// Each block is ~3 seconds; use lastBlock to approximate the timestamp
		blocksAgo := safeBlock - lastBlock
		minTimestamp = time.Now().Add(-time.Duration(blocksAgo) * 3 * time.Second).UnixMilli()
	}

	// Get all contract addresses we're watching on this chain
	contracts := p.tokens.ContractAddresses(p.chain)
	if len(contracts) == 0 {
		return 0, nil
	}

	// Scan each watched address for TRC20 transfers
	for key, info := range addrSnap {
		if !strings.HasPrefix(key, p.chain+":") {
			continue // skip addresses from other chains
		}

		for _, contract := range contracts {
			transfers, err := p.client.GetTRC20Transfers(ctx, info.Address, contract, minTimestamp, 200)
			if err != nil {
				p.logger.Warn("settla-tron-poller: failed to get TRC20 transfers",
					"address", info.Address,
					"contract", contract,
					"error", err,
				)
				continue
			}

			for _, t := range transfers {
				// Only process transfers TO our watched address
				if !strings.EqualFold(t.To, info.Address) {
					continue
				}

				incoming := rpc.ParseTRC20Transfer(t, p.chain)

				if err := p.processIncomingTx(ctx, incoming); err != nil {
					p.logger.Warn("settla-tron-poller: failed to process incoming tx",
						"tx_hash", incoming.TxHash,
						"address", info.Address,
						"error", err,
					)
					continue
				}
				processed++
			}
		}
	}

	return processed, nil
}

// processIncomingTx checks if the transaction has already been recorded,
// looks up the session, and writes the deposit tx + outbox entry atomically.
func (p *TronPoller) processIncomingTx(ctx context.Context, incoming domain.IncomingTransaction) error {
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
		p.logger.Debug("settla-tron-poller: ignoring transfer for unwatched token",
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

	p.logger.Info("settla-tron-poller: deposit transaction detected",
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
func (p *TronPoller) detectReorg(ctx context.Context, block *rpc.BlockResp, lastCheckpointBlock int64, lastCheckpointHash string) {
	// Only check if the block is right after our last checkpoint
	if block.BlockHeader.RawData.Number != lastCheckpointBlock+1 {
		return
	}
	if block.BlockHeader.RawData.ParentHash != lastCheckpointHash {
		p.logger.Warn("settla-tron-poller: possible chain reorganisation detected",
			"block", block.BlockHeader.RawData.Number,
			"expected_parent_hash", lastCheckpointHash,
			"actual_parent_hash", block.BlockHeader.RawData.ParentHash,
			"last_checkpoint_block", lastCheckpointBlock,
		)
	}
	_ = ctx // available for future reorg handling
}

// PollInterval returns the configured poll interval for this chain.
func (p *TronPoller) PollInterval() time.Duration {
	return p.cfg.PollInterval
}

// Chain returns the chain identifier.
func (p *TronPoller) Chain() string {
	return p.chain
}

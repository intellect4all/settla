package messaging

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// Subject prefix constants for each stream.
const (
	SubjectPrefixTransfer        = "settla.transfer"
	SubjectPrefixProvider        = "settla.provider.command"
	SubjectPrefixLedger          = "settla.ledger"
	SubjectPrefixTreasury        = "settla.treasury"
	SubjectPrefixBlockchain      = "settla.blockchain"
	SubjectPrefixWebhook         = "settla.webhook"
	SubjectPrefixProviderInbound = "settla.provider.inbound"
)

// TransferSubject builds the NATS subject for a transfer event, partitioned by tenant.
// Format: settla.transfer.partition.{hash(tenantID)%8}.{eventType}
func TransferSubject(tenantID uuid.UUID, numPartitions int, eventType string) string {
	partition := TenantPartition(tenantID, numPartitions)
	return PartitionSubject(partition, eventType)
}

// ProviderSubject builds the NATS subject for a provider command event, partitioned by tenant.
// Format: settla.provider.command.partition.{hash(tenantID)%N}.{eventType}
func ProviderSubject(tenantID uuid.UUID, numPartitions int, eventType string) string {
	partition := TenantPartition(tenantID, numPartitions)
	return fmt.Sprintf("%s.partition.%d.%s", SubjectPrefixProvider, partition, eventType)
}

// LedgerSubject builds the NATS subject for a ledger event, partitioned by tenant.
// Format: settla.ledger.partition.{hash(tenantID)%N}.{eventType}
func LedgerSubject(tenantID uuid.UUID, numPartitions int, eventType string) string {
	partition := TenantPartition(tenantID, numPartitions)
	return fmt.Sprintf("%s.partition.%d.%s", SubjectPrefixLedger, partition, eventType)
}

// TreasurySubject builds the NATS subject for a treasury event.
// Format: settla.treasury.{eventType}
func TreasurySubject(eventType string) string {
	return fmt.Sprintf("%s.%s", SubjectPrefixTreasury, eventType)
}

// BlockchainSubject builds the NATS subject for a blockchain event, partitioned by tenant.
// Format: settla.blockchain.partition.{hash(tenantID)%N}.{eventType}
func BlockchainSubject(tenantID uuid.UUID, numPartitions int, eventType string) string {
	partition := TenantPartition(tenantID, numPartitions)
	return fmt.Sprintf("%s.partition.%d.%s", SubjectPrefixBlockchain, partition, eventType)
}

// WebhookSubject builds the NATS subject for an outbound webhook event, partitioned by tenant.
// Format: settla.webhook.partition.{hash(tenantID)%N}.{eventType}
func WebhookSubject(tenantID uuid.UUID, numPartitions int, eventType string) string {
	partition := TenantPartition(tenantID, numPartitions)
	return fmt.Sprintf("%s.partition.%d.%s", SubjectPrefixWebhook, partition, eventType)
}

// ProviderWebhookSubject builds the NATS subject for an inbound provider webhook notification, partitioned by tenant.
// Format: settla.provider.inbound.partition.{hash(tenantID)%N}.{eventType}
func ProviderWebhookSubject(tenantID uuid.UUID, numPartitions int, eventType string) string {
	partition := TenantPartition(tenantID, numPartitions)
	return fmt.Sprintf("%s.partition.%d.%s", SubjectPrefixProviderInbound, partition, eventType)
}

// SubjectForEventType maps a domain event type to its NATS subject.
// This is the primary routing function used by the outbox relay to determine
// where to publish each event.
//
// Event type prefixes determine routing:
//
//	transfer.*, settlement.*, onramp.*, offramp.*, refund.* → SETTLA_TRANSFERS (partitioned by tenant)
//	provider.inbound.*                                       → SETTLA_PROVIDER_WEBHOOKS
//	provider.*                                               → SETTLA_PROVIDERS
//	ledger.*                                                 → SETTLA_LEDGER
//	treasury.*, position.*, liquidity.*                      → SETTLA_TREASURY
//	blockchain.*                                             → SETTLA_BLOCKCHAIN
//	webhook.*                                                → SETTLA_WEBHOOKS
func SubjectForEventType(eventType string, tenantID uuid.UUID, numPartitions int) string {
	prefix := eventPrefix(eventType)

	switch prefix {
	// Transfer-related events go to the partitioned transfer stream.
	case "transfer", "settlement", "onramp", "offramp", "refund":
		return TransferSubject(tenantID, numPartitions, eventType)

	// Provider inbound webhooks use a distinct namespace.
	case "provider":
		if strings.HasPrefix(eventType, "provider.inbound.") {
			rest := strings.TrimPrefix(eventType, "provider.inbound.")
			return ProviderWebhookSubject(tenantID, numPartitions, rest)
		}
		return ProviderSubject(tenantID, numPartitions, eventType)

	case "ledger":
		return LedgerSubject(tenantID, numPartitions, eventType)

	case "treasury", "position", "liquidity":
		return TreasurySubject(eventType)

	case "blockchain":
		return BlockchainSubject(tenantID, numPartitions, eventType)

	case "webhook":
		return WebhookSubject(tenantID, numPartitions, eventType)

	default:
		// Fallback: route unknown events to the transfer stream (preserves
		// backward compatibility with the single-stream topology).
		return TransferSubject(tenantID, numPartitions, eventType)
	}
}

// eventPrefix extracts the first segment before the dot from an event type.
// e.g., "transfer.created" → "transfer", "settlement.completed" → "settlement"
func eventPrefix(eventType string) string {
	if idx := strings.IndexByte(eventType, '.'); idx > 0 {
		return eventType[:idx]
	}
	return eventType
}

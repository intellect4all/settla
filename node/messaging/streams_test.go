package messaging

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/intellect4all/settla/domain"
)

func TestAllStreams_Returns8Streams(t *testing.T) {
	streams := AllStreams()
	if len(streams) != 8 {
		t.Fatalf("expected 8 streams (7 domain + DLQ), got %d", len(streams))
	}

	// Verify all expected stream names are present.
	expected := map[string]bool{
		StreamTransfers:        false,
		StreamProviders:        false,
		StreamLedger:           false,
		StreamTreasury:         false,
		StreamBlockchain:       false,
		StreamWebhooks:         false,
		StreamProviderWebhooks: false,
		StreamNameDLQ:          false,
	}
	for _, s := range streams {
		if _, ok := expected[s.Name]; !ok {
			t.Errorf("unexpected stream name: %s", s.Name)
		}
		expected[s.Name] = true
	}
	for name, found := range expected {
		if !found {
			t.Errorf("missing stream: %s", name)
		}
	}
}

func TestAllStreams_NoSubjectOverlap(t *testing.T) {
	// SETTLA_PROVIDERS uses settla.provider.command.> and
	// SETTLA_PROVIDER_WEBHOOKS uses settla.provider.inbound.>
	// Verify they don't overlap.
	streams := AllStreams()
	var providerSubjects, inboundSubjects []string
	for _, s := range streams {
		if s.Name == StreamProviders {
			providerSubjects = s.Subjects
		}
		if s.Name == StreamProviderWebhooks {
			inboundSubjects = s.Subjects
		}
	}

	if len(providerSubjects) == 0 || len(inboundSubjects) == 0 {
		t.Fatal("provider or inbound stream missing subjects")
	}

	// The provider subjects should use "command" prefix, not overlap with "inbound".
	for _, subj := range providerSubjects {
		if subj == "settla.provider.>" {
			t.Error("SETTLA_PROVIDERS should not use settla.provider.> (overlaps with inbound)")
		}
	}
}

func TestStreamSettings(t *testing.T) {
	if StreamMaxAge != 168*time.Hour {
		t.Errorf("StreamMaxAge = %v, want 168h", StreamMaxAge)
	}
	if StreamMaxMsgSize != 1_048_576 {
		t.Errorf("StreamMaxMsgSize = %d, want 1048576", StreamMaxMsgSize)
	}
	if StreamDuplicateWindow != 5*time.Minute {
		t.Errorf("StreamDuplicateWindow = %v, want 5m", StreamDuplicateWindow)
	}
}

func TestBackoffSchedule(t *testing.T) {
	expected := []time.Duration{
		1 * time.Second,
		5 * time.Second,
		30 * time.Second,
		2 * time.Minute,
		10 * time.Minute,
	}
	if len(BackoffSchedule) != len(expected) {
		t.Fatalf("BackoffSchedule length = %d, want %d", len(BackoffSchedule), len(expected))
	}
	for i, d := range expected {
		if BackoffSchedule[i] != d {
			t.Errorf("BackoffSchedule[%d] = %v, want %v", i, BackoffSchedule[i], d)
		}
	}
}

func TestMaxRetries(t *testing.T) {
	// MaxDeliver = 6 (1 initial + 5 retries)
	if MaxRetries != 6 {
		t.Errorf("MaxRetries = %d, want 6", MaxRetries)
	}
}

func TestDLQSubject(t *testing.T) {
	tests := []struct {
		stream    string
		eventType string
		want      string
	}{
		{StreamTransfers, "transfer.created", "settla.dlq.SETTLA_TRANSFERS.transfer.created"},
		{StreamLedger, "ledger.entry.created", "settla.dlq.SETTLA_LEDGER.ledger.entry.created"},
		{StreamWebhooks, "webhook.sent", "settla.dlq.SETTLA_WEBHOOKS.webhook.sent"},
	}
	for _, tt := range tests {
		got := DLQSubject(tt.stream, tt.eventType)
		if got != tt.want {
			t.Errorf("DLQSubject(%q, %q) = %q, want %q", tt.stream, tt.eventType, got, tt.want)
		}
	}
}

func TestStreamForSubject(t *testing.T) {
	tests := []struct {
		subject string
		want    string
	}{
		{"settla.transfer.partition.0.transfer.created", StreamTransfers},
		{"settla.transfer.partition.7.settlement.completed", StreamTransfers},
		{"settla.provider.command.partition.0.onramp.initiated", StreamProviders},
		{"settla.provider.inbound.partition.3.payment.received", StreamProviderWebhooks},
		{"settla.ledger.partition.2.entry.created", StreamLedger},
		{"settla.treasury.position.updated", StreamTreasury},
		{"settla.blockchain.partition.0.tx.confirmed", StreamBlockchain},
		{"settla.webhook.partition.3.transfer.completed", StreamWebhooks},
		{"settla.dlq.SETTLA_TRANSFERS.transfer.created", StreamNameDLQ},
		{"unknown.subject", ""},
	}
	for _, tt := range tests {
		got := StreamForSubject(tt.subject)
		if got != tt.want {
			t.Errorf("StreamForSubject(%q) = %q, want %q", tt.subject, got, tt.want)
		}
	}
}

func TestSubjectBuilders(t *testing.T) {
	t.Run("ProviderSubject", func(t *testing.T) {
		tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")
		got := ProviderSubject(tenantID, 8, "onramp.initiated")
		partition := TenantPartition(tenantID, 8)
		want := fmt.Sprintf("settla.provider.command.partition.%d.onramp.initiated", partition)
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("LedgerSubject", func(t *testing.T) {
		tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")
		got := LedgerSubject(tenantID, 8, "entry.created")
		partition := TenantPartition(tenantID, 8)
		want := fmt.Sprintf("settla.ledger.partition.%d.entry.created", partition)
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("TreasurySubject", func(t *testing.T) {
		got := TreasurySubject("position.updated")
		want := "settla.treasury.position.updated"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("BlockchainSubject", func(t *testing.T) {
		tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")
		got := BlockchainSubject(tenantID, 8, "tx.confirmed")
		partition := TenantPartition(tenantID, 8)
		want := fmt.Sprintf("settla.blockchain.partition.%d.tx.confirmed", partition)
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("WebhookSubject", func(t *testing.T) {
		tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")
		got := WebhookSubject(tenantID, 8, "transfer.completed")
		partition := TenantPartition(tenantID, 8)
		want := fmt.Sprintf("settla.webhook.partition.%d.transfer.completed", partition)
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("ProviderWebhookSubject", func(t *testing.T) {
		tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")
		got := ProviderWebhookSubject(tenantID, 8, "payment.received")
		partition := TenantPartition(tenantID, 8)
		want := fmt.Sprintf("settla.provider.inbound.partition.%d.payment.received", partition)
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("TransferSubject", func(t *testing.T) {
		tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")
		partition := TenantPartition(tenantID, 8)
		got := TransferSubject(tenantID, 8, "transfer.created")
		want := PartitionSubject(partition, "transfer.created")
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

func TestSubjectForEventType_AllDomainEvents(t *testing.T) {
	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")
	numPartitions := 8
	partition := TenantPartition(tenantID, numPartitions)

	tests := []struct {
		eventType  string
		wantPrefix string
		wantStream string
	}{
		// Transfer-related events → SETTLA_TRANSFERS (partitioned)
		{domain.EventTransferCreated, "settla.transfer.partition.", StreamTransfers},
		{domain.EventTransferFunded, "settla.transfer.partition.", StreamTransfers},
		{domain.EventTransferCompleted, "settla.transfer.partition.", StreamTransfers},
		{domain.EventTransferFailed, "settla.transfer.partition.", StreamTransfers},
		{domain.EventOnRampInitiated, "settla.transfer.partition.", StreamTransfers},
		{domain.EventOnRampCompleted, "settla.transfer.partition.", StreamTransfers},
		{domain.EventOffRampInitiated, "settla.transfer.partition.", StreamTransfers},
		{domain.EventOffRampCompleted, "settla.transfer.partition.", StreamTransfers},
		{domain.EventSettlementStarted, "settla.transfer.partition.", StreamTransfers},
		{domain.EventSettlementCompleted, "settla.transfer.partition.", StreamTransfers},
		{domain.EventRefundInitiated, "settla.transfer.partition.", StreamTransfers},
		{domain.EventRefundCompleted, "settla.transfer.partition.", StreamTransfers},

		// Treasury events → SETTLA_TREASURY
		{domain.EventPositionUpdated, "settla.treasury.", StreamTreasury},
		{domain.EventLiquidityAlert, "settla.treasury.", StreamTreasury},
	}

	for _, tt := range tests {
		t.Run(tt.eventType, func(t *testing.T) {
			got := SubjectForEventType(tt.eventType, tenantID, numPartitions)

			// Check prefix
			if !matchPrefix(got, tt.wantPrefix) {
				t.Errorf("SubjectForEventType(%q) = %q, want prefix %q", tt.eventType, got, tt.wantPrefix)
			}

			// Check stream mapping
			gotStream := StreamForSubject(got)
			if gotStream != tt.wantStream {
				t.Errorf("StreamForSubject(%q) = %q, want %q", got, gotStream, tt.wantStream)
			}
		})
	}

	// Transfer events should include the partition number.
	transferSubject := SubjectForEventType(domain.EventTransferCreated, tenantID, numPartitions)
	expectedTransfer := PartitionSubject(partition, domain.EventTransferCreated)
	if transferSubject != expectedTransfer {
		t.Errorf("transfer subject = %q, want %q", transferSubject, expectedTransfer)
	}
}

func TestSubjectForEventType_PartitionConsistency(t *testing.T) {
	tenantID := uuid.MustParse("b0000000-0000-0000-0000-000000000002")
	numPartitions := 8

	// Same tenant + same event type → same subject always.
	first := SubjectForEventType(domain.EventTransferCreated, tenantID, numPartitions)
	for i := 0; i < 1000; i++ {
		got := SubjectForEventType(domain.EventTransferCreated, tenantID, numPartitions)
		if got != first {
			t.Fatalf("iteration %d: subject changed from %q to %q", i, first, got)
		}
	}
}

func TestSubjectForEventType_ProviderEvents(t *testing.T) {
	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")
	partition := TenantPartition(tenantID, 8)

	// Regular provider event → SETTLA_PROVIDERS (partitioned)
	got := SubjectForEventType("provider.quote.requested", tenantID, 8)
	want := fmt.Sprintf("settla.provider.command.partition.%d.provider.quote.requested", partition)
	if got != want {
		t.Errorf("provider event subject = %q, want %q", got, want)
	}

	// Inbound provider webhook → SETTLA_PROVIDER_WEBHOOKS (partitioned)
	got = SubjectForEventType("provider.inbound.payment.received", tenantID, 8)
	want = fmt.Sprintf("settla.provider.inbound.partition.%d.payment.received", partition)
	if got != want {
		t.Errorf("inbound webhook subject = %q, want %q", got, want)
	}
}

func TestSubjectForEventType_StreamSpecificEvents(t *testing.T) {
	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")
	partition := TenantPartition(tenantID, 8)

	tests := []struct {
		eventType string
		want      string
	}{
		{"ledger.entry.created", fmt.Sprintf("settla.ledger.partition.%d.ledger.entry.created", partition)},
		{"treasury.position.snapshot", "settla.treasury.treasury.position.snapshot"},
		{"blockchain.tx.submitted", fmt.Sprintf("settla.blockchain.partition.%d.blockchain.tx.submitted", partition)},
		{"webhook.delivery.completed", fmt.Sprintf("settla.webhook.partition.%d.webhook.delivery.completed", partition)},
	}

	for _, tt := range tests {
		t.Run(tt.eventType, func(t *testing.T) {
			got := SubjectForEventType(tt.eventType, tenantID, 8)
			if got != tt.want {
				t.Errorf("SubjectForEventType(%q) = %q, want %q", tt.eventType, got, tt.want)
			}
		})
	}
}

func TestSubjectForEventType_UnknownEventFallback(t *testing.T) {
	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")

	// Unknown event types fall back to the transfer stream.
	got := SubjectForEventType("unknown.event", tenantID, 8)
	if !matchPrefix(got, "settla.transfer.partition.") {
		t.Errorf("unknown event should route to transfer stream, got %q", got)
	}
}

func TestNakDelay(t *testing.T) {
	tests := []struct {
		delivery uint64
		want     time.Duration
	}{
		{0, 1 * time.Second},
		{1, 1 * time.Second},
		{2, 5 * time.Second},
		{3, 30 * time.Second},
		{4, 2 * time.Minute},
		{5, 10 * time.Minute},
		{6, 10 * time.Minute}, // capped at last backoff
		{100, 10 * time.Minute},
	}
	for _, tt := range tests {
		got := nakDelay(tt.delivery)
		if got != tt.want {
			t.Errorf("nakDelay(%d) = %v, want %v", tt.delivery, got, tt.want)
		}
	}
}

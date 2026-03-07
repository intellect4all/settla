package messaging

import (
	"testing"

	"github.com/google/uuid"
)

func TestTenantPartition_SameTenantAlwaysSamePartition(t *testing.T) {
	tenantID := uuid.MustParse("a0000000-0000-0000-0000-000000000001")
	numPartitions := 8

	first := TenantPartition(tenantID, numPartitions)

	// Run 1000 times — must always produce the same partition
	for i := 0; i < 1000; i++ {
		got := TenantPartition(tenantID, numPartitions)
		if got != first {
			t.Fatalf("iteration %d: expected partition %d, got %d", i, first, got)
		}
	}
}

func TestTenantPartition_DifferentTenantsCanDiffer(t *testing.T) {
	numPartitions := 8
	tenants := []uuid.UUID{
		uuid.MustParse("a0000000-0000-0000-0000-000000000001"),
		uuid.MustParse("b0000000-0000-0000-0000-000000000002"),
		uuid.MustParse("c0000000-0000-0000-0000-000000000003"),
		uuid.MustParse("d0000000-0000-0000-0000-000000000004"),
		uuid.MustParse("e0000000-0000-0000-0000-000000000005"),
		uuid.MustParse("f0000000-0000-0000-0000-000000000006"),
	}

	partitions := make(map[int]bool)
	for _, tenantID := range tenants {
		p := TenantPartition(tenantID, numPartitions)
		partitions[p] = true
		if p < 0 || p >= numPartitions {
			t.Errorf("partition %d out of range [0, %d)", p, numPartitions)
		}
	}

	// With 6 tenants and 8 partitions, we should get at least 2 distinct partitions
	// (probability of all 6 hitting the same partition is extremely low)
	if len(partitions) < 2 {
		t.Errorf("expected at least 2 distinct partitions for 6 tenants, got %d", len(partitions))
	}
}

func TestTenantPartition_AllPartitionsInRange(t *testing.T) {
	numPartitions := 8

	// Generate many tenant IDs and verify all partitions are in range
	for i := 0; i < 100; i++ {
		tenantID := uuid.New()
		p := TenantPartition(tenantID, numPartitions)
		if p < 0 || p >= numPartitions {
			t.Errorf("tenant %s: partition %d out of range [0, %d)", tenantID, p, numPartitions)
		}
	}
}

func TestPartitionSubject(t *testing.T) {
	tests := []struct {
		partition int
		eventType string
		want      string
	}{
		{0, "transfer.created", "settla.transfer.partition.0.transfer.created"},
		{3, "transfer.funded", "settla.transfer.partition.3.transfer.funded"},
		{7, "settlement.completed", "settla.transfer.partition.7.settlement.completed"},
	}

	for _, tt := range tests {
		got := PartitionSubject(tt.partition, tt.eventType)
		if got != tt.want {
			t.Errorf("PartitionSubject(%d, %q) = %q, want %q", tt.partition, tt.eventType, got, tt.want)
		}
	}
}

func TestPartitionFilter(t *testing.T) {
	tests := []struct {
		partition int
		want      string
	}{
		{0, "settla.transfer.partition.0.>"},
		{5, "settla.transfer.partition.5.>"},
	}

	for _, tt := range tests {
		got := PartitionFilter(tt.partition)
		if got != tt.want {
			t.Errorf("PartitionFilter(%d) = %q, want %q", tt.partition, got, tt.want)
		}
	}
}

func TestConsumerName(t *testing.T) {
	got := ConsumerName(3)
	want := "settla-worker-partition-3"
	if got != want {
		t.Errorf("ConsumerName(3) = %q, want %q", got, want)
	}
}

func TestTenantPartition_Distribution(t *testing.T) {
	numPartitions := 8
	counts := make(map[int]int, numPartitions)

	// Generate 10,000 tenant IDs and check distribution
	for i := 0; i < 10000; i++ {
		tenantID := uuid.New()
		p := TenantPartition(tenantID, numPartitions)
		counts[p]++
	}

	// Each partition should get roughly 10000/8 = 1250 tenants.
	// Allow wide margin: 500-2000 (this is just a sanity check, not a statistical test)
	for p, count := range counts {
		if count < 500 || count > 2000 {
			t.Errorf("partition %d has %d tenants (expected ~1250)", p, count)
		}
	}
}

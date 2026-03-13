// Package outbox implements the transactional outbox relay for Settla.
//
// The outbox pattern decouples state transitions from event publishing:
// the settlement engine writes outbox entries atomically with state changes
// in a single Postgres transaction, and the relay polls those entries and
// publishes them to NATS JetStream.
//
// This guarantees exactly-once semantics (at the publishing boundary) because:
//   - State + outbox writes are atomic (same Postgres transaction).
//   - NATS deduplication uses the outbox entry UUID as message ID.
//   - The relay marks entries as published only after NATS confirms receipt.
//
// The relay runs as a goroutine within settla-server, polling every 50ms
// for unpublished entries (batch of 100). Each entry is routed to the correct
// NATS subject based on event_type using the subject routing functions from
// the messaging package.
//
// The cleanup goroutine manages outbox partition lifecycle:
//   - Drops daily partitions older than 48 hours (instant DDL, no vacuum).
//   - Creates new daily partitions 3 days ahead.
//   - Warns if the default partition contains rows (partition gap detected).
package outbox

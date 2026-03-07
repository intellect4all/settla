// Package worker implements event consumers for Settla Node.
//
// Each worker subscribes to a NATS JetStream subject and processes domain
// events as part of the settlement saga (e.g. initiating ledger postings
// after a transfer is created, triggering rail execution after funding).
package worker

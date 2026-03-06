// Package core implements Settla Core — the settlement engine and state machine.
//
// It owns the transfer lifecycle: validating settlement requests, orchestrating
// transitions through the state machine (e.g. initiated → processing → settled),
// and coordinating with the ledger, rail, and treasury modules to fulfil each step.
//
// Key types:
//   - Engine: the top-level settlement orchestrator
//   - Transfer: domain aggregate representing a settlement request
//   - StateMachine: enforces valid state transitions
package core

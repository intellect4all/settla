// Package ledger implements Settla Ledger — the immutable double-entry ledger.
//
// It uses CQRS: an append-only journal for writes and materialized balance
// snapshots for reads. Every posting must balance (sum of debits = sum of credits).
// Monetary amounts use shopspring/decimal exclusively — floats are never permitted.
//
// Key types:
//   - Ledger: service façade for recording and querying entries
//   - Entry: a journal entry composed of balanced Postings
//   - Posting: a single debit or credit against an account
//   - Account: a named ledger account with a balance
package ledger

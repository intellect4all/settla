package db

import "embed"

//go:embed migrations/ledger/*.sql
var LedgerMigrations embed.FS

//go:embed migrations/transfer/*.sql
var TransferMigrations embed.FS

//go:embed migrations/treasury/*.sql
var TreasuryMigrations embed.FS

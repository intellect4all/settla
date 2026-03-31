package dbpool

import (
	"github.com/intellect4all/settla/store/ledgerdb"
	"github.com/intellect4all/settla/store/transferdb"
	"github.com/intellect4all/settla/store/treasurydb"
)

// Compile-time interface checks: RoutedPool satisfies all SQLC DBTX interfaces.
var (
	_ transferdb.DBTX = (*RoutedPool)(nil)
	_ treasurydb.DBTX = (*RoutedPool)(nil)
	_ ledgerdb.DBTX   = (*RoutedPool)(nil)
)

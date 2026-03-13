package ledger

import (
	"encoding/binary"
	"fmt"
	"strings"

	tb "github.com/tigerbeetle/tigerbeetle-go"
	tb_types "github.com/tigerbeetle/tigerbeetle-go/pkg/types"
)

// Compile-time check: realTBClient implements TBClient.
var _ TBClient = (*realTBClient)(nil)

// realTBClient adapts the tigerbeetle-go Client to our TBClient interface.
// The adapter converts between our internal types (ID128, uint64 amounts) and
// TigerBeetle's native types (Uint128 for IDs and amounts).
type realTBClient struct {
	client tb.Client
}

// NewRealTBClient creates a TBClient backed by a real TigerBeetle cluster.
// clusterID is typically 0 for single-cluster deployments.
// addresses is a list of "host:port" strings for the TigerBeetle replicas.
func NewRealTBClient(clusterID uint64, addresses []string) (TBClient, error) {
	client, err := tb.NewClient(tb_types.ToUint128(clusterID), addresses)
	if err != nil {
		return nil, fmt.Errorf("settla-ledger: creating TigerBeetle client: %w", err)
	}
	return &realTBClient{client: client}, nil
}

func (r *realTBClient) CreateAccounts(accounts []TBAccount) ([]TBCreateResult, error) {
	tbAccounts := make([]tb_types.Account, len(accounts))
	for i, a := range accounts {
		tbAccounts[i] = tb_types.Account{
			ID:          id128ToUint128(a.ID),
			UserData128: id128ToUint128(a.UserData128),
			UserData64:  a.UserData64,
			UserData32:  a.UserData32,
			Ledger:      a.Ledger,
			Code:        a.Code,
			Flags:       a.Flags,
		}
	}

	results, err := r.client.CreateAccounts(tbAccounts)
	if err != nil {
		return nil, err
	}

	// TB only returns failed items. Empty results = all succeeded.
	out := make([]TBCreateResult, 0, len(results))
	for _, res := range results {
		result := uint32(res.Result)
		// Map TB's "AccountExists" (exact duplicate, idempotent) to our TBResultExists.
		if res.Result == tb_types.AccountExists {
			result = TBResultExists
		}
		out = append(out, TBCreateResult{Index: res.Index, Result: result})
	}
	return out, nil
}

func (r *realTBClient) CreateTransfers(transfers []TBTransfer) ([]TBCreateResult, error) {
	tbTransfers := make([]tb_types.Transfer, len(transfers))
	for i, t := range transfers {
		tbTransfers[i] = tb_types.Transfer{
			ID:              id128ToUint128(t.ID),
			DebitAccountID:  id128ToUint128(t.DebitAccountID),
			CreditAccountID: id128ToUint128(t.CreditAccountID),
			UserData128:     id128ToUint128(t.UserData128),
			UserData64:      t.UserData64,
			UserData32:      t.UserData32,
			Amount:          tb_types.ToUint128(t.Amount),
			Ledger:          t.Ledger,
			Code:            t.Code,
			Flags:           t.Flags,
		}
	}

	results, err := r.client.CreateTransfers(tbTransfers)
	if err != nil {
		return nil, err
	}

	out := make([]TBCreateResult, 0, len(results))
	for _, res := range results {
		result := uint32(res.Result)
		if res.Result == tb_types.TransferExists {
			result = TBResultExists
		}
		out = append(out, TBCreateResult{Index: res.Index, Result: result})
	}
	return out, nil
}

func (r *realTBClient) LookupAccounts(ids []ID128) ([]TBAccount, error) {
	tbIDs := make([]tb_types.Uint128, len(ids))
	for i, id := range ids {
		tbIDs[i] = id128ToUint128(id)
	}

	accounts, err := r.client.LookupAccounts(tbIDs)
	if err != nil {
		return nil, err
	}

	out := make([]TBAccount, len(accounts))
	for i, a := range accounts {
		out[i] = TBAccount{
			ID:             uint128ToID128(a.ID),
			UserData128:    uint128ToID128(a.UserData128),
			UserData64:     a.UserData64,
			UserData32:     a.UserData32,
			Ledger:         a.Ledger,
			Code:           a.Code,
			Flags:          a.Flags,
			DebitsPending:  uint128ToUint64(a.DebitsPending),
			DebitsPosted:   uint128ToUint64(a.DebitsPosted),
			CreditsPending: uint128ToUint64(a.CreditsPending),
			CreditsPosted:  uint128ToUint64(a.CreditsPosted),
		}
	}
	return out, nil
}

func (r *realTBClient) Close() {
	r.client.Close()
}

// id128ToUint128 converts our ID128 ([16]byte) to TigerBeetle's Uint128.
func id128ToUint128(id ID128) tb_types.Uint128 {
	return tb_types.BytesToUint128(id)
}

// uint128ToID128 converts TigerBeetle's Uint128 to our ID128 ([16]byte).
func uint128ToID128(u tb_types.Uint128) ID128 {
	return ID128(u.Bytes())
}

// uint128ToUint64 extracts the lower 64 bits from a Uint128.
// Settla's amounts fit in uint64 (max ~$9.2T with 1e6 scale),
// so the upper 64 bits are always zero.
func uint128ToUint64(u tb_types.Uint128) uint64 {
	b := u.Bytes()
	return binary.LittleEndian.Uint64(b[:8])
}

// ParseTBAddresses splits a comma-separated address string into a slice.
func ParseTBAddresses(raw string) []string {
	parts := strings.Split(raw, ",")
	var addrs []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			addrs = append(addrs, p)
		}
	}
	return addrs
}

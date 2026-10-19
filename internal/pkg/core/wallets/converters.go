package wallets

import (
	"time"

	"github.com/psyb0t/docker-monero/internal/pkg/http/api"
	"github.com/psyb0t/docker-monero/internal/pkg/monero/amount"
	"github.com/psyb0t/docker-monero/internal/pkg/monero/walletrpc"
)

// Wallet amounts and indices are unsigned on the wire; heights and counts
// stay far below 2^63, so the int64 the API uses is lossless in practice.

//nolint:gosec // see above
func balanceToAPI(account uint32, balance walletrpc.Balance) api.Balance {
	subaddresses := make([]api.SubaddressBalance, 0, len(balance.PerSubaddress))

	for _, sub := range balance.PerSubaddress {
		subaddresses = append(subaddresses, api.SubaddressBalance{
			AddressIndex:       int64(sub.AddressIndex),
			Address:            sub.Address,
			Label:              sub.Label,
			Balance:            amount.FormatAtomic(sub.Balance),
			BalanceXmr:         amount.FormatXMR(sub.Balance),
			UnlockedBalance:    amount.FormatAtomic(sub.UnlockedBalance),
			UnlockedBalanceXmr: amount.FormatXMR(sub.UnlockedBalance),
			UnspentOutputs:     int64(sub.NumUnspentOutputs),
			BlocksToUnlock:     int64(sub.BlocksToUnlock),
		})
	}

	return api.Balance{
		AccountIndex:       int64(account),
		Balance:            amount.FormatAtomic(balance.Balance),
		BalanceXmr:         amount.FormatXMR(balance.Balance),
		UnlockedBalance:    amount.FormatAtomic(balance.UnlockedBalance),
		UnlockedBalanceXmr: amount.FormatXMR(balance.UnlockedBalance),
		BlocksToUnlock:     int64(balance.BlocksToUnlock),
		Subaddresses:       subaddresses,
	}
}

func addressesToAPI(
	account uint32,
	addresses walletrpc.Addresses,
) api.AddressList {
	list := make([]api.Address, 0, len(addresses.Addresses))

	for _, address := range addresses.Addresses {
		list = append(list, api.Address{
			Address:      address.Address,
			AddressIndex: int64(address.AddressIndex),
			Label:        address.Label,
			Used:         address.Used,
		})
	}

	return api.AddressList{
		AccountIndex:   int64(account),
		PrimaryAddress: addresses.Address,
		Addresses:      list,
	}
}

func transfersToAPI(transfers walletrpc.Transfers) []api.TransferEntry {
	groups := []struct {
		direction api.TransferDirection
		entries   []walletrpc.TransferEntry
	}{
		{api.TransferDirectionIn, transfers.In},
		{api.TransferDirectionOut, transfers.Out},
		{api.TransferDirectionPending, transfers.Pending},
		{api.TransferDirectionFailed, transfers.Failed},
		{api.TransferDirectionPool, transfers.Pool},
	}

	total := 0
	for _, group := range groups {
		total += len(group.entries)
	}

	result := make([]api.TransferEntry, 0, total)

	for _, group := range groups {
		for _, entry := range group.entries {
			result = append(result, transferEntryToAPI(group.direction, entry))
		}
	}

	return result
}

//nolint:gosec // see above
func transferEntryToAPI(
	direction api.TransferDirection,
	entry walletrpc.TransferEntry,
) api.TransferEntry {
	result := api.TransferEntry{
		TxHash:          entry.TxID,
		Direction:       direction,
		Amount:          amount.FormatAtomic(entry.Amount),
		AmountXmr:       amount.FormatXMR(entry.Amount),
		Fee:             amount.FormatAtomic(entry.Fee),
		FeeXmr:          amount.FormatXMR(entry.Fee),
		Height:          int64(entry.Height),
		Timestamp:       time.Unix(int64(entry.Timestamp), 0).UTC(),
		Confirmations:   int64(entry.Confirmations),
		Locked:          entry.Locked,
		Address:         entry.Address,
		AccountIndex:    int64(entry.SubaddrIndex.Major),
		SubaddressIndex: int64(entry.SubaddrIndex.Minor),
		DoubleSpendSeen: entry.DoubleSpendSeen,
	}

	if entry.PaymentID != "" && entry.PaymentID != emptyPaymentID {
		paymentID := entry.PaymentID
		result.PaymentId = &paymentID
	}

	if entry.Note != "" {
		note := entry.Note
		result.Note = &note
	}

	return result
}

// emptyPaymentID is what wallet-rpc reports for a transfer with no payment
// ID.
const emptyPaymentID = "0000000000000000"

//
//nolint:gosec // see above
func transferResultToAPI(
	result walletrpc.TransferResult,
	relayed bool,
) api.TransferResult {
	return api.TransferResult{
		TxHash:    result.TxHash,
		TxKey:     result.TxKey,
		Amount:    amount.FormatAtomic(result.Amount),
		AmountXmr: amount.FormatXMR(result.Amount),
		Fee:       amount.FormatAtomic(result.Fee),
		FeeXmr:    amount.FormatXMR(result.Fee),
		Weight:    int64(result.Weight),
		Relayed:   relayed,
	}
}

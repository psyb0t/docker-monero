package wallets

import (
	"strings"
	"testing"
	"time"

	"github.com/psyb0t/docker-monero/internal/pkg/http/api"
	"github.com/psyb0t/docker-monero/internal/pkg/monero/walletrpc"
	"github.com/stretchr/testify/assert"
)

func TestBalanceToAPI(t *testing.T) {
	t.Parallel()

	got := balanceToAPI(3, walletrpc.Balance{
		Balance:         1_500_000_000_000,
		UnlockedBalance: 500_000_000_000,
		BlocksToUnlock:  9,
		PerSubaddress: []walletrpc.SubaddressBalance{
			{
				AccountIndex:      3,
				AddressIndex:      0,
				Address:           "primary",
				Balance:           1_000_000_000_000,
				UnlockedBalance:   250_000_000_000,
				Label:             "Primary account",
				NumUnspentOutputs: 2,
				BlocksToUnlock:    4,
			},
			{
				AddressIndex:      5,
				Address:           "sub",
				Balance:           500_000_000_000,
				UnlockedBalance:   250_000_000_000,
				Label:             "savings",
				NumUnspentOutputs: 1,
				BlocksToUnlock:    9,
			},
		},
	})

	assert.Equal(t, api.Balance{
		AccountIndex:       3,
		Balance:            "1500000000000",
		BalanceXmr:         "1.500000000000",
		UnlockedBalance:    "500000000000",
		UnlockedBalanceXmr: "0.500000000000",
		BlocksToUnlock:     9,
		Subaddresses: []api.SubaddressBalance{
			{
				AddressIndex:       0,
				Address:            "primary",
				Label:              "Primary account",
				Balance:            "1000000000000",
				BalanceXmr:         "1.000000000000",
				UnlockedBalance:    "250000000000",
				UnlockedBalanceXmr: "0.250000000000",
				UnspentOutputs:     2,
				BlocksToUnlock:     4,
			},
			{
				AddressIndex:       5,
				Address:            "sub",
				Label:              "savings",
				Balance:            "500000000000",
				BalanceXmr:         "0.500000000000",
				UnlockedBalance:    "250000000000",
				UnlockedBalanceXmr: "0.250000000000",
				UnspentOutputs:     1,
				BlocksToUnlock:     9,
			},
		},
	}, got)
}

func TestBalanceToAPIWithoutSubaddressesIsAnEmptyList(t *testing.T) {
	t.Parallel()

	got := balanceToAPI(0, walletrpc.Balance{})

	assert.NotNil(t, got.Subaddresses)
	assert.Empty(t, got.Subaddresses)
	assert.Equal(t, "0", got.Balance)
	assert.Equal(t, "0.000000000000", got.BalanceXmr)
	assert.Equal(t, "0", got.UnlockedBalance)
	assert.Equal(t, "0.000000000000", got.UnlockedBalanceXmr)
}

func TestAddressesToAPI(t *testing.T) {
	t.Parallel()

	got := addressesToAPI(2, walletrpc.Addresses{
		Address: "primary",
		Addresses: []walletrpc.AddressInfo{
			{Address: "primary", Label: "Primary account", AddressIndex: 0},
			{Address: "sub-one", Label: "rent", AddressIndex: 1, Used: true},
		},
	})

	assert.Equal(t, api.AddressList{
		AccountIndex:   2,
		PrimaryAddress: "primary",
		Addresses: []api.Address{
			{
				Address:      "primary",
				AddressIndex: 0,
				Label:        "Primary account",
			},
			{
				Address:      "sub-one",
				AddressIndex: 1,
				Label:        "rent",
				Used:         true,
			},
		},
	}, got)
}

func TestAddressesToAPIWithoutAddressesIsAnEmptyList(t *testing.T) {
	t.Parallel()

	got := addressesToAPI(0, walletrpc.Addresses{Address: "primary"})

	assert.NotNil(t, got.Addresses)
	assert.Empty(t, got.Addresses)
	assert.Equal(t, "primary", got.PrimaryAddress)
}

func TestTransfersToAPIKeepsGroupOrderAndLabelsDirection(t *testing.T) {
	t.Parallel()

	entry := func(txID string) walletrpc.TransferEntry {
		return walletrpc.TransferEntry{TxID: txID}
	}

	got := transfersToAPI(walletrpc.Transfers{
		In:      []walletrpc.TransferEntry{entry("in-1"), entry("in-2")},
		Out:     []walletrpc.TransferEntry{entry("out-1")},
		Pending: []walletrpc.TransferEntry{entry("pending-1")},
		Failed:  []walletrpc.TransferEntry{entry("failed-1")},
		Pool:    []walletrpc.TransferEntry{entry("pool-1")},
	})

	type tagged struct {
		txHash    string
		direction api.TransferDirection
	}

	gotTagged := make([]tagged, 0, len(got))
	for _, converted := range got {
		gotTagged = append(gotTagged, tagged{
			txHash: converted.TxHash, direction: converted.Direction,
		})
	}

	assert.Equal(t, []tagged{
		{"in-1", api.TransferDirectionIn},
		{"in-2", api.TransferDirectionIn},
		{"out-1", api.TransferDirectionOut},
		{"pending-1", api.TransferDirectionPending},
		{"failed-1", api.TransferDirectionFailed},
		{"pool-1", api.TransferDirectionPool},
	}, gotTagged)
}

func TestTransfersToAPIWithoutTransfersIsAnEmptyList(t *testing.T) {
	t.Parallel()

	got := transfersToAPI(walletrpc.Transfers{})

	assert.NotNil(t, got)
	assert.Empty(t, got)
}

func TestTransferEntryToAPI(t *testing.T) {
	t.Parallel()

	got := transferEntryToAPI(api.TransferDirectionOut, walletrpc.TransferEntry{
		TxID:            "txid",
		PaymentID:       "1122334455667788",
		Height:          3_000_001,
		Timestamp:       1_700_000_000,
		Amount:          2_000_000_000_000,
		Fee:             30_000_000,
		Note:            "rent",
		Locked:          true,
		SubaddrIndex:    walletrpc.SubaddressIndex{Major: 2, Minor: 7},
		Address:         "recipient",
		DoubleSpendSeen: true,
		Confirmations:   12,
	})

	paymentID := "1122334455667788"
	note := "rent"
	wantTime := time.Date(2023, time.November, 14, 22, 13, 20, 0, time.UTC)

	assert.Equal(t, api.TransferEntry{
		TxHash:          "txid",
		Direction:       api.TransferDirectionOut,
		Amount:          "2000000000000",
		AmountXmr:       "2.000000000000",
		Fee:             "30000000",
		FeeXmr:          "0.000030000000",
		Height:          3_000_001,
		Timestamp:       wantTime,
		Confirmations:   12,
		Locked:          true,
		Address:         "recipient",
		AccountIndex:    2,
		SubaddressIndex: 7,
		DoubleSpendSeen: true,
		PaymentId:       &paymentID,
		Note:            &note,
	}, got)
}

func TestTransferEntryToAPIOptionalFields(t *testing.T) {
	t.Parallel()

	longPaymentID := strings.Repeat("0", 63) + "1"

	testCases := []struct {
		name          string
		paymentID     string
		note          string
		wantPaymentID *string
		wantNote      *string
	}{
		{name: "neither set"},
		{
			name:      "wallet's all-zero payment id means none",
			paymentID: "0000000000000000",
		},
		{
			name:          "real payment id",
			paymentID:     "00000000000000ff",
			wantPaymentID: new("00000000000000ff"),
		},
		{
			name:          "long payment id",
			paymentID:     longPaymentID,
			wantPaymentID: new(longPaymentID),
		},
		{
			name:     "note only",
			note:     "coffee",
			wantNote: new("coffee"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := transferEntryToAPI(
				api.TransferDirectionIn,
				walletrpc.TransferEntry{PaymentID: tc.paymentID, Note: tc.note},
			)

			assert.Equal(t, tc.wantPaymentID, got.PaymentId)
			assert.Equal(t, tc.wantNote, got.Note)
		})
	}
}

func TestTransferEntryToAPITimestampIsUTC(t *testing.T) {
	t.Parallel()

	got := transferEntryToAPI(
		api.TransferDirectionPool, walletrpc.TransferEntry{Timestamp: 86_400},
	)

	assert.Equal(t, time.UTC, got.Timestamp.Location())
	assert.Equal(t, int64(86_400), got.Timestamp.Unix())
}

func TestTransferResultToAPI(t *testing.T) {
	t.Parallel()

	result := walletrpc.TransferResult{
		TxHash: "hash",
		TxKey:  "key",
		Amount: 1_000_000_000_000,
		Fee:    30_000_000,
		Weight: 1500,
	}

	assert.Equal(t, api.TransferResult{
		TxHash:    "hash",
		TxKey:     "key",
		Amount:    "1000000000000",
		AmountXmr: "1.000000000000",
		Fee:       "30000000",
		FeeXmr:    "0.000030000000",
		Weight:    1500,
		Relayed:   true,
	}, transferResultToAPI(result, true))

	assert.False(t, transferResultToAPI(result, false).Relayed)
}

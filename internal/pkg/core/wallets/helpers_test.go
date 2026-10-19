package wallets

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/docker-monero/internal/pkg/common/invalid"
	"github.com/psyb0t/docker-monero/internal/pkg/http/api"
	"github.com/psyb0t/docker-monero/internal/pkg/monero/daemonrpc"
	"github.com/psyb0t/docker-monero/internal/pkg/monero/jsonrpc"
	"github.com/psyb0t/docker-monero/internal/pkg/monero/walletrpc"
	"github.com/psyb0t/docker-monero/internal/pkg/walletpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func requireInvalid(t *testing.T, err error, wantMessage string) {
	t.Helper()

	require.ErrorIs(t, err, commerr.ErrInvalidArgument)

	message, ok := invalid.Message(err)
	require.True(t, ok, "error carries no client-safe message: %v", err)
	assert.Equal(t, wantMessage, message)
}

func rpcError(code int) error {
	return &jsonrpc.Error{Code: code, Message: "wallet said no"}
}

func TestValidatePassword(t *testing.T) {
	t.Parallel()

	longest := strings.Repeat("p", api.MaxPasswordLength)

	require.NoError(t, validatePassword(""))
	require.NoError(t, validatePassword("pw"))
	require.NoError(t, validatePassword(longest))

	err := validatePassword(longest + "p")

	requireInvalid(t, err, "password is longer than 256 characters")
}

func TestIndexValue(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		value   *int64
		want    uint32
		wantErr bool
	}{
		{name: "absent defaults to zero", value: nil, want: 0},
		{name: "zero", value: new(int64(0)), want: 0},
		{name: "small", value: new(int64(7)), want: 7},
		{
			name:  "largest index",
			value: new(int64(maxIndex - 1)),
			want:  maxIndex - 1,
		},
		{
			name:    "first index past the limit",
			value:   new(int64(maxIndex)),
			wantErr: true,
		},
		{name: "negative", value: new(int64(-1)), wantErr: true},
		{
			name:    "uint32 overflow",
			value:   new(int64(math.MaxUint32) + 1),
			wantErr: true,
		},
		{
			name:    "int64 maximum",
			value:   new(int64(math.MaxInt64)),
			wantErr: true,
		},
		{
			name:    "int64 minimum",
			value:   new(int64(math.MinInt64)),
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := indexValue(tc.value)

			if tc.wantErr {
				require.ErrorIs(t, err, ErrInvalidAccount)
				assert.Zero(t, got)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestAccountIndexOf(t *testing.T) {
	t.Parallel()

	got, err := accountIndexOf(nil)
	require.NoError(t, err)
	assert.Equal(t, uint32(0), got)

	got, err = accountIndexOf(new(int64(3)))
	require.NoError(t, err)
	assert.Equal(t, uint32(3), got)

	for _, bad := range []int64{-1, maxIndex, math.MaxInt64} {
		got, err = accountIndexOf(new(bad))

		require.ErrorIs(t, err, ErrInvalidAccount)
		require.ErrorIs(t, err, commerr.ErrInvalidArgument)
		assert.ErrorContains(t, err, "accountIndex")
		assert.Zero(t, got)
	}
}

func TestNonNegative(t *testing.T) {
	t.Parallel()

	got, err := nonNegative(nil, "restoreHeight")
	require.NoError(t, err)
	assert.Equal(t, uint64(0), got)

	got, err = nonNegative(new(int64(0)), "restoreHeight")
	require.NoError(t, err)
	assert.Equal(t, uint64(0), got)

	got, err = nonNegative(new(int64(math.MaxInt64)), "restoreHeight")
	require.NoError(t, err)
	assert.Equal(t, uint64(math.MaxInt64), got)

	got, err = nonNegative(new(int64(-1)), "restoreHeight")
	requireInvalid(t, err, "restoreHeight must not be negative")
	assert.Zero(t, got)

	got, err = nonNegative(new(int64(-7)), "otherField")
	requireInvalid(t, err, "otherField must not be negative")
	assert.Zero(t, got)
}

func TestPage(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		limit      *int
		offset     *int
		wantLimit  int
		wantOffset int
		wantMsg    string
	}{
		{
			name:       "defaults",
			wantLimit:  api.DefaultListLimit,
			wantOffset: 0,
		},
		{
			name:       "explicit values",
			limit:      new(25),
			offset:     new(50),
			wantLimit:  25,
			wantOffset: 50,
		},
		{
			name:       "smallest limit",
			limit:      new(1),
			wantLimit:  1,
			wantOffset: 0,
		},
		{
			name:       "largest limit",
			limit:      new(api.MaxListLimit),
			wantLimit:  api.MaxListLimit,
			wantOffset: 0,
		},
		{
			name:       "offset far past any dataset",
			offset:     new(math.MaxInt32),
			wantLimit:  api.DefaultListLimit,
			wantOffset: math.MaxInt32,
		},
		{
			name:    "zero limit",
			limit:   new(0),
			wantMsg: "limit must be 1-1000",
		},
		{
			name:    "negative limit",
			limit:   new(-5),
			wantMsg: "limit must be 1-1000",
		},
		{
			name:    "limit above the maximum",
			limit:   new(api.MaxListLimit + 1),
			wantMsg: "limit must be 1-1000",
		},
		{
			name:    "negative offset",
			offset:  new(-1),
			wantMsg: "offset must not be negative",
		},
		{
			name:    "bad limit is reported before a bad offset",
			limit:   new(0),
			offset:  new(-1),
			wantMsg: "limit must be 1-1000",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			limit, offset, err := page(tc.limit, tc.offset)

			if tc.wantMsg != "" {
				requireInvalid(t, err, tc.wantMsg)
				assert.Zero(t, limit)
				assert.Zero(t, offset)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.wantLimit, limit)
			assert.Equal(t, tc.wantOffset, offset)
		})
	}
}

func TestTransferFilter(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		directions []api.TransferDirection
		account    uint32
		want       walletrpc.TransferFilter
	}{
		{
			name: "no direction selects every category",
			want: walletrpc.TransferFilter{
				In: true, Out: true, Pending: true, Failed: true, Pool: true,
			},
		},
		{
			name:       "empty list selects every category",
			directions: []api.TransferDirection{},
			account:    4,
			want: walletrpc.TransferFilter{
				In: true, Out: true, Pending: true, Failed: true, Pool: true,
				AccountIndex: 4,
			},
		},
		{
			name:       "in only",
			directions: []api.TransferDirection{api.TransferDirectionIn},
			want:       walletrpc.TransferFilter{In: true},
		},
		{
			name:       "out only",
			directions: []api.TransferDirection{api.TransferDirectionOut},
			want:       walletrpc.TransferFilter{Out: true},
		},
		{
			name:       "pending only",
			directions: []api.TransferDirection{api.TransferDirectionPending},
			want:       walletrpc.TransferFilter{Pending: true},
		},
		{
			name:       "failed only",
			directions: []api.TransferDirection{api.TransferDirectionFailed},
			want:       walletrpc.TransferFilter{Failed: true},
		},
		{
			name:       "pool only",
			directions: []api.TransferDirection{api.TransferDirectionPool},
			want:       walletrpc.TransferFilter{Pool: true},
		},
		{
			name: "several directions on one account",
			directions: []api.TransferDirection{
				api.TransferDirectionIn, api.TransferDirectionPool,
			},
			account: 2,
			want: walletrpc.TransferFilter{
				In: true, Pool: true, AccountIndex: 2,
			},
		},
		{
			name: "repeated direction",
			directions: []api.TransferDirection{
				api.TransferDirectionOut, api.TransferDirectionOut,
			},
			want: walletrpc.TransferFilter{Out: true},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := transferFilter(tc.directions, tc.account)

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestTransferFilterRejectsUnknownDirection(t *testing.T) {
	t.Parallel()

	got, err := transferFilter([]api.TransferDirection{
		api.TransferDirectionIn, "sideways",
	}, 0)

	requireInvalid(t, err, `unknown direction "sideways"`)
	assert.Equal(t, walletrpc.TransferFilter{}, got)
}

func TestParseDestinations(t *testing.T) {
	t.Parallel()

	got, err := parseDestinations([]api.Destination{
		{Address: "addr-one", Amount: "1"},
		{Address: "addr-two", Amount: "1500000000000"},
		{Address: "addr-three", Amount: "18446744073709551615"},
	})

	require.NoError(t, err)
	assert.Equal(t, []walletrpc.Destination{
		{Address: "addr-one", Amount: 1},
		{Address: "addr-two", Amount: 1_500_000_000_000},
		{Address: "addr-three", Amount: math.MaxUint64},
	}, got)
}

func TestParseDestinationsEntryCount(t *testing.T) {
	t.Parallel()

	destinations := func(count int) []api.Destination {
		list := make([]api.Destination, count)
		for i := range list {
			list[i] = api.Destination{Address: "addr", Amount: "1"}
		}

		return list
	}

	got, err := parseDestinations(destinations(api.MaxDestinations))
	require.NoError(t, err)
	assert.Len(t, got, api.MaxDestinations)

	for _, count := range []int{0, api.MaxDestinations + 1} {
		got, err = parseDestinations(destinations(count))

		requireInvalid(t, err, "destinations must hold 1-16 entries")
		assert.Nil(t, got)
	}

	_, err = parseDestinations(nil)
	requireInvalid(t, err, "destinations must hold 1-16 entries")
}

func TestParseDestinationsRejectsBadAmounts(t *testing.T) {
	t.Parallel()

	for _, amount := range []string{
		"", "0", "00", "-1", "+1", "1.5", "1e12", " 1", "1 ", "abc",
		"18446744073709551616", "0x10", "١٢٣",
	} {
		t.Run(amount, func(t *testing.T) {
			t.Parallel()

			got, err := parseDestinations([]api.Destination{
				{Address: "addr", Amount: "1"},
				{Address: "addr", Amount: amount},
			})

			requireInvalid(t, err, fmt.Sprintf(
				"amount %q must be a positive integer of atomic units", amount,
			))
			assert.Nil(t, got)
		})
	}
}

func TestParseDestinationsRejectsEmptyAddress(t *testing.T) {
	t.Parallel()

	got, err := parseDestinations([]api.Destination{
		{Address: "addr", Amount: "1"},
		{Address: "", Amount: "1"},
	})

	require.ErrorIs(t, err, ErrInvalidAddress)
	require.ErrorIs(t, err, commerr.ErrInvalidArgument)
	assert.Nil(t, got)
}

func TestPriorityOf(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		priority *api.TransferPriority
		want     uint32
	}{
		{name: "absent", priority: nil, want: walletrpc.PriorityDefault},
		{
			name:     "default",
			priority: new(api.TransferPriorityDefault),
			want:     walletrpc.PriorityDefault,
		},
		{
			name:     "low",
			priority: new(api.TransferPriorityLow),
			want:     walletrpc.PriorityUnimport,
		},
		{
			name:     "normal",
			priority: new(api.TransferPriorityNormal),
			want:     walletrpc.PriorityNormal,
		},
		{
			name:     "high",
			priority: new(api.TransferPriorityHigh),
			want:     walletrpc.PriorityElevated,
		},
		{
			name:     "highest",
			priority: new(api.TransferPriorityHighest),
			want:     walletrpc.PriorityPriority,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := priorityOf(tc.priority)

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}

	got, err := priorityOf(new(api.TransferPriority("urgent")))
	requireInvalid(t, err, `unknown priority "urgent"`)
	assert.Zero(t, got)
}

func TestSubaddressIndices(t *testing.T) {
	t.Parallel()

	got, err := subaddressIndices(nil)
	require.NoError(t, err)
	assert.Nil(t, got)

	got, err = subaddressIndices(&[]int64{})
	require.NoError(t, err)
	assert.Equal(t, []uint32{}, got)

	got, err = subaddressIndices(&[]int64{0, 5, maxIndex - 1})
	require.NoError(t, err)
	assert.Equal(t, []uint32{0, 5, maxIndex - 1}, got)

	for _, bad := range [][]int64{{-1}, {1, maxIndex}, {2, 3, -9}} {
		got, err = subaddressIndices(&bad)

		require.ErrorIs(t, err, ErrInvalidAccount)
		assert.ErrorContains(t, err, "subaddressIndices")
		assert.Nil(t, got)
	}
}

func TestBuildTransfer(t *testing.T) {
	t.Parallel()

	got, err := buildTransfer(api.TransferRequest{
		Destinations: []api.Destination{
			{Address: "addr", Amount: "250"},
		},
		AccountIndex:      new(int64(2)),
		SubaddressIndices: &[]int64{1, 4},
		Priority:          new(api.TransferPriorityHigh),
		DryRun:            new(true),
	})

	require.NoError(t, err)
	assert.Equal(t, walletrpc.TransferRequest{
		Destinations:   []walletrpc.Destination{{Address: "addr", Amount: 250}},
		AccountIndex:   2,
		SubaddrIndices: []uint32{1, 4},
		Priority:       walletrpc.PriorityElevated,
		DoNotRelay:     true,
	}, got)
}

func TestBuildTransferDefaults(t *testing.T) {
	t.Parallel()

	for _, dryRun := range []*bool{nil, new(false)} {
		got, err := buildTransfer(api.TransferRequest{
			Destinations: []api.Destination{{Address: "addr", Amount: "1"}},
			DryRun:       dryRun,
		})

		require.NoError(t, err)
		assert.False(t, got.DoNotRelay)
		assert.Equal(t, uint32(0), got.AccountIndex)
		assert.Equal(t, walletrpc.PriorityDefault, got.Priority)
		assert.Nil(t, got.SubaddrIndices)
	}
}

func TestBuildTransferRejects(t *testing.T) {
	t.Parallel()

	valid := []api.Destination{{Address: "addr", Amount: "1"}}

	testCases := []struct {
		name    string
		request api.TransferRequest
		wantErr error
		wantMsg string
	}{
		{
			name:    "no destinations",
			request: api.TransferRequest{},
			wantMsg: "destinations must hold 1-16 entries",
		},
		{
			name: "zero amount",
			request: api.TransferRequest{
				Destinations: []api.Destination{{Address: "a", Amount: "0"}},
			},
			wantMsg: `amount "0" must be a positive integer of atomic units`,
		},
		{
			name: "empty address",
			request: api.TransferRequest{
				Destinations: []api.Destination{{Address: "", Amount: "5"}},
			},
			wantErr: ErrInvalidAddress,
		},
		{
			name: "account index out of range",
			request: api.TransferRequest{
				Destinations: valid,
				AccountIndex: new(int64(-1)),
			},
			wantErr: ErrInvalidAccount,
		},
		{
			name: "subaddress index out of range",
			request: api.TransferRequest{
				Destinations:      valid,
				SubaddressIndices: &[]int64{maxIndex},
			},
			wantErr: ErrInvalidAccount,
		},
		{
			name: "unknown priority",
			request: api.TransferRequest{
				Destinations: valid,
				Priority:     new(api.TransferPriority("asap")),
			},
			wantMsg: `unknown priority "asap"`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := buildTransfer(tc.request)

			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			} else {
				requireInvalid(t, err, tc.wantMsg)
			}

			assert.Equal(t, walletrpc.TransferRequest{}, got)
		})
	}
}

func TestMapWalletError(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		err     error
		wantIs  error
		wantMsg string
	}{
		{
			name:   "account index out of bound",
			err:    rpcError(walletrpc.CodeAccountIndexOutOfBound),
			wantIs: ErrInvalidAccount,
		},
		{
			name:   "address index out of bound",
			err:    rpcError(walletrpc.CodeAddressIndexOutOfBound),
			wantIs: ErrInvalidAccount,
		},
		{
			name:   "wrong address",
			err:    rpcError(walletrpc.CodeWrongAddress),
			wantIs: ErrInvalidAddress,
		},
		{
			name:    "wrong key",
			err:     rpcError(walletrpc.CodeWrongKey),
			wantMsg: "the supplied key is not valid",
		},
		{
			name:   "no daemon connection",
			err:    rpcError(walletrpc.CodeNoDaemonConnection),
			wantIs: commerr.ErrUnavailable,
		},
		{
			name:   "daemon is busy",
			err:    rpcError(walletrpc.CodeDaemonIsBusy),
			wantIs: commerr.ErrUnavailable,
		},
		{
			name: "code reached through the client's wrapping",
			err: ctxerrors.Wrap(
				rpcError(walletrpc.CodeWrongAddress), "get addresses",
			),
			wantIs: ErrInvalidAddress,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := mapWalletError(tc.err, "an action")

			if tc.wantMsg != "" {
				requireInvalid(t, got, tc.wantMsg)

				return
			}

			require.ErrorIs(t, got, tc.wantIs)
		})
	}
}

func TestMapWalletErrorLeavesOtherErrorsAlone(t *testing.T) {
	t.Parallel()

	sentinels := []error{
		ErrInvalidAccount, ErrInvalidAddress, ErrInsufficientFunds,
		ErrViewOnly, ErrTransferRejected, ErrNodeNotSynced, ErrWalletSyncing,
		ErrTransfersDisabled, commerr.ErrUnavailable,
	}

	for _, original := range []error{
		rpcError(walletrpc.CodeUnknownError),
		rpcError(walletrpc.CodeNotOpen),
		rpcError(walletrpc.CodeInvalidPassword),
		rpcError(-32601),
		errors.New("plain failure"),
		commerr.ErrTimeout,
		walletpool.ErrWalletNotOpen,
	} {
		got := mapWalletError(original, "an action")

		require.ErrorIs(t, got, original)
		assert.ErrorContains(t, got, "an action")

		for _, sentinel := range sentinels {
			if errors.Is(original, sentinel) {
				continue
			}

			require.NotErrorIs(t, got, sentinel, original.Error())
		}

		_, isClientSafe := invalid.Message(got)
		assert.False(t, isClientSafe, original.Error())
	}
}

func TestMapTransferError(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		err     error
		wantIs  error
		wantMsg string
	}{
		{
			name:   "not enough money",
			err:    rpcError(walletrpc.CodeNotEnoughMoney),
			wantIs: ErrInsufficientFunds,
		},
		{
			name:   "not enough unlocked money",
			err:    rpcError(walletrpc.CodeNotEnoughUnlockedMoney),
			wantIs: ErrInsufficientFunds,
		},
		{
			name:   "watch-only wallet",
			err:    rpcError(walletrpc.CodeWatchOnly),
			wantIs: ErrViewOnly,
		},
		{
			name:    "zero destination",
			err:     rpcError(walletrpc.CodeZeroDestination),
			wantMsg: "every destination amount must be above zero",
		},
		{
			name:    "zero amount",
			err:     rpcError(walletrpc.CodeZeroAmount),
			wantMsg: "every destination amount must be above zero",
		},
		{
			name:   "generic transfer error",
			err:    rpcError(walletrpc.CodeGenericTransferError),
			wantIs: ErrTransferRejected,
		},
		{
			name:   "transaction not possible",
			err:    rpcError(walletrpc.CodeTxNotPossible),
			wantIs: ErrTransferRejected,
		},
		{
			name:   "transaction too large",
			err:    rpcError(walletrpc.CodeTxTooLarge),
			wantIs: ErrTransferRejected,
		},
		{
			name:   "falls back to the wallet mapping for a bad address",
			err:    rpcError(walletrpc.CodeWrongAddress),
			wantIs: ErrInvalidAddress,
		},
		{
			name:   "falls back to the wallet mapping for a lost daemon",
			err:    rpcError(walletrpc.CodeNoDaemonConnection),
			wantIs: commerr.ErrUnavailable,
		},
		{
			name: "code reached through the client's wrapping",
			err: ctxerrors.Wrap(
				rpcError(walletrpc.CodeNotEnoughUnlockedMoney), "transfer",
			),
			wantIs: ErrInsufficientFunds,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := mapTransferError(tc.err)

			if tc.wantMsg != "" {
				requireInvalid(t, got, tc.wantMsg)

				return
			}

			require.ErrorIs(t, got, tc.wantIs)
		})
	}
}

func TestMapTransferErrorKeepsUnknownErrors(t *testing.T) {
	t.Parallel()

	boom := rpcError(walletrpc.CodeUnknownError)

	got := mapTransferError(boom)

	require.ErrorIs(t, got, boom)
	require.NotErrorIs(t, got, ErrTransferRejected)
	require.NotErrorIs(t, got, ErrInsufficientFunds)
}

func TestIsNotOpen(t *testing.T) {
	t.Parallel()

	assert.True(t, isNotOpen(walletpool.ErrWalletNotOpen))
	assert.True(t, isNotOpen(ctxerrors.Wrap(walletpool.ErrWalletNotOpen, "w")))
	assert.False(t, isNotOpen(walletpool.ErrWalletBusy))
	assert.False(t, isNotOpen(walletpool.ErrInvalidName))
	assert.False(t, isNotOpen(errors.New("other")))
	assert.False(t, isNotOpen(nil))
}

func TestApplySync(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name         string
		walletHeight uint64
		info         daemonrpc.Info
		wantHeight   int64
		wantSynced   bool
		wantBehind   int64
		wantPercent  float64
		wantSyncing  bool
	}{
		{
			name:         "wallet at the node's height",
			walletHeight: 100,
			info:         daemonrpc.Info{Height: 100, Synchronized: true},
			wantHeight:   100,
			wantSynced:   true,
			wantBehind:   0,
			wantPercent:  100,
			wantSyncing:  false,
		},
		{
			name:         "wallet behind a synchronized node",
			walletHeight: 25,
			info:         daemonrpc.Info{Height: 100, Synchronized: true},
			wantHeight:   100,
			wantSynced:   true,
			wantBehind:   75,
			wantPercent:  25,
			wantSyncing:  true,
		},
		{
			name:         "wallet level with a node that is still syncing",
			walletHeight: 100,
			info:         daemonrpc.Info{Height: 100},
			wantHeight:   100,
			wantSynced:   false,
			wantBehind:   0,
			wantPercent:  100,
			wantSyncing:  true,
		},
		{
			name:         "wallet ahead of the node",
			walletHeight: 110,
			info:         daemonrpc.Info{Height: 100, Synchronized: true},
			wantHeight:   100,
			wantSynced:   true,
			wantBehind:   0,
			wantPercent:  100,
			wantSyncing:  false,
		},
		{
			name:         "node with no blocks",
			walletHeight: 0,
			info:         daemonrpc.Info{Synchronized: true},
			wantHeight:   0,
			wantSynced:   true,
			wantBehind:   0,
			wantPercent:  100,
			wantSyncing:  false,
		},
		{
			name:         "wallet with no blocks scanned",
			walletHeight: 0,
			info:         daemonrpc.Info{Height: 400, Synchronized: true},
			wantHeight:   400,
			wantSynced:   true,
			wantBehind:   400,
			wantPercent:  0,
			wantSyncing:  true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var status api.WalletStatus

			applySync(&status, tc.walletHeight, tc.info)

			require.NotNil(t, status.NodeHeight)
			require.NotNil(t, status.NodeSynchronized)
			require.NotNil(t, status.BlocksBehind)
			require.NotNil(t, status.SyncPercent)
			require.NotNil(t, status.Syncing)

			assert.Equal(t, tc.wantHeight, *status.NodeHeight)
			assert.Equal(t, tc.wantSynced, *status.NodeSynchronized)
			assert.Equal(t, tc.wantBehind, *status.BlocksBehind)
			assert.InDelta(t, tc.wantPercent, *status.SyncPercent, 1e-9)
			assert.Equal(t, tc.wantSyncing, *status.Syncing)
		})
	}
}

func TestApplySyncDoesNotTouchWalletFields(t *testing.T) {
	t.Parallel()

	name := "kept"
	status := api.WalletStatus{Name: name, Open: true}

	applySync(&status, 5, daemonrpc.Info{Height: 10})

	assert.Equal(t, name, status.Name)
	assert.True(t, status.Open)
	assert.Nil(t, status.WalletHeight)
}

func TestToInt64Ptr(t *testing.T) {
	t.Parallel()

	first := toInt64Ptr(7)
	second := toInt64Ptr(7)

	require.NotNil(t, first)
	assert.Equal(t, int64(7), *first)
	assert.NotSame(t, first, second)
	assert.Equal(t, int64(0), *toInt64Ptr(0))
}

func TestDeref(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "", deref(nil))
	assert.Equal(t, "", deref(new("")))
	assert.Equal(t, "value", deref(new("value")))
}

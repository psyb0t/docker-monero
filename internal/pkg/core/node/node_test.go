package node

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/docker-monero/internal/pkg/common/access"
	"github.com/psyb0t/docker-monero/internal/pkg/common/invalid"
	"github.com/psyb0t/docker-monero/internal/pkg/config"
	"github.com/psyb0t/docker-monero/internal/pkg/http/api"
	"github.com/psyb0t/docker-monero/internal/pkg/monero/daemonrpc"
	"github.com/psyb0t/docker-monero/internal/pkg/monero/jsonrpc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Three spellings of one 64-character hash.
var (
	hashLower = strings.Repeat("0123456789abcdef", 4)
	hashUpper = strings.Repeat("0123456789ABCDEF", 4)
	hashMixed = strings.Repeat("0123456789aBcDeF", 4)
)

// fakeDaemon answers from fixed values and records what it was asked.
type fakeDaemon struct {
	info      daemonrpc.Info
	header    daemonrpc.BlockHeader
	txs       daemonrpc.Transactions
	fee       daemonrpc.FeeEstimate
	pool      daemonrpc.PoolStats
	generated daemonrpc.GeneratedBlocks

	// err is returned by every method when set.
	err error

	calls      int
	gotHeight  uint64
	gotHash    string
	gotHashes  []string
	gotCount   uint64
	gotAddress string
}

func (f *fakeDaemon) GetInfo(context.Context) (daemonrpc.Info, error) {
	f.calls++

	return f.info, f.err
}

func (f *fakeDaemon) GetLastBlockHeader(
	context.Context,
) (daemonrpc.BlockHeader, error) {
	f.calls++

	return f.header, f.err
}

func (f *fakeDaemon) GetBlockHeaderByHeight(
	_ context.Context,
	height uint64,
) (daemonrpc.BlockHeader, error) {
	f.calls++
	f.gotHeight = height

	return f.header, f.err
}

func (f *fakeDaemon) GetBlockHeaderByHash(
	_ context.Context,
	hash string,
) (daemonrpc.BlockHeader, error) {
	f.calls++
	f.gotHash = hash

	return f.header, f.err
}

func (f *fakeDaemon) GetTransactions(
	_ context.Context,
	hashes []string,
) (daemonrpc.Transactions, error) {
	f.calls++
	f.gotHashes = hashes

	return f.txs, f.err
}

func (f *fakeDaemon) GetFeeEstimate(
	context.Context,
) (daemonrpc.FeeEstimate, error) {
	f.calls++

	return f.fee, f.err
}

func (f *fakeDaemon) GetTransactionPoolStats(
	context.Context,
) (daemonrpc.PoolStats, error) {
	f.calls++

	return f.pool, f.err
}

func (f *fakeDaemon) GenerateBlocks(
	_ context.Context,
	count uint64,
	address string,
) (daemonrpc.GeneratedBlocks, error) {
	f.calls++
	f.gotCount = count
	f.gotAddress = address

	return f.generated, f.err
}

func options(network config.Network) Options {
	return Options{
		Network:  network,
		Restarts: func() uint64 { return 0 },
	}
}

func rpcError(code int) error {
	return &jsonrpc.Error{Code: code, Message: "daemon said no"}
}

func requireInvalid(t *testing.T, err error, wantMessage string) {
	t.Helper()

	require.ErrorIs(t, err, commerr.ErrInvalidArgument)

	message, ok := invalid.Message(err)
	require.True(t, ok, "error carries no client-safe message: %v", err)
	assert.Equal(t, wantMessage, message)
}

func TestInfoConvertsNodeState(t *testing.T) {
	t.Parallel()

	daemon := &fakeDaemon{info: daemonrpc.Info{
		Height:                   1500,
		TargetHeight:             3000,
		Difficulty:               123456789,
		TxPoolSize:               7,
		OutgoingConnectionsCount: 12,
		IncomingConnectionsCount: 3,
		DatabaseSize:             5_000_000_000,
		Version:                  "0.18.4.0-release",
		TopBlockHash:             hashLower,
		StartTime:                1_700_000_000,
	}}
	service := New(daemon, Options{
		Network:  config.NetworkTestnet,
		Restarts: func() uint64 { return 4 },
	})

	got, err := service.Info(context.Background())
	require.NoError(t, err)

	startedAt := time.Date(2023, time.November, 14, 22, 13, 20, 0, time.UTC)
	assert.Equal(t, api.NodeInfo{
		Network:             api.NetworkTestnet,
		Height:              1500,
		TargetHeight:        3000,
		SyncPercent:         50,
		Difficulty:          123456789,
		TxPoolSize:          7,
		OutgoingConnections: 12,
		IncomingConnections: 3,
		DatabaseSize:        5_000_000_000,
		Version:             "0.18.4.0-release",
		TopBlockHash:        hashLower,
		Restarts:            4,
		StartedAt:           &startedAt,
	}, got)
}

func TestInfoFlagsAreNotSwapped(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		info daemonrpc.Info
		want api.NodeInfo
	}{
		{
			name: "synchronized",
			info: daemonrpc.Info{Synchronized: true},
			want: api.NodeInfo{Synchronized: true, SyncPercent: 100},
		},
		{
			name: "busy syncing",
			info: daemonrpc.Info{BusySyncing: true},
			want: api.NodeInfo{BusySyncing: true},
		},
		{
			name: "offline",
			info: daemonrpc.Info{Offline: true},
			want: api.NodeInfo{Offline: true},
		},
		{
			name: "update available",
			info: daemonrpc.Info{UpdateAvailable: true},
			want: api.NodeInfo{UpdateAvailable: true},
		},
		{
			name: "restricted",
			info: daemonrpc.Info{Restricted: true},
			want: api.NodeInfo{Restricted: true},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			service := New(&fakeDaemon{info: tc.info}, options(""))

			got, err := service.Info(context.Background())

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestInfoOmitsStartedAtWithoutStartTime(t *testing.T) {
	t.Parallel()

	service := New(&fakeDaemon{}, options(config.NetworkMainnet))

	got, err := service.Info(context.Background())

	require.NoError(t, err)
	assert.Nil(t, got.StartedAt)
	assert.Equal(t, api.NetworkMainnet, got.Network)
}

func TestInfoReadsRestartsEveryCall(t *testing.T) {
	t.Parallel()

	var restarts atomic.Uint64

	service := New(&fakeDaemon{}, Options{
		Restarts: restarts.Load,
	})

	first, err := service.Info(context.Background())
	require.NoError(t, err)

	restarts.Store(9)

	second, err := service.Info(context.Background())
	require.NoError(t, err)

	assert.Equal(t, int64(0), first.Restarts)
	assert.Equal(t, int64(9), second.Restarts)
}

func TestInfoWrapsDaemonError(t *testing.T) {
	t.Parallel()

	boom := commerr.ErrConnectFailed
	service := New(&fakeDaemon{err: boom}, options(config.NetworkMainnet))

	got, err := service.Info(context.Background())

	require.ErrorIs(t, err, boom)
	assert.Equal(t, api.NodeInfo{}, got)
}

func TestLatestBlockConvertsHeader(t *testing.T) {
	t.Parallel()

	daemon := &fakeDaemon{header: daemonrpc.BlockHeader{
		MajorVersion: 16,
		MinorVersion: 2,
		Timestamp:    1_700_000_123,
		PrevHash:     hashUpper,
		OrphanStatus: true,
		Height:       3_000_000,
		Depth:        9,
		Hash:         hashLower,
		Difficulty:   111,
		Reward:       600_000_000_000,
		BlockSize:    1500,
		BlockWeight:  1600,
		NumTxes:      4,
		MinerTxHash:  hashMixed,
	}}
	service := New(daemon, options(config.NetworkMainnet))

	got, err := service.LatestBlock(context.Background())
	require.NoError(t, err)

	assert.Equal(t, api.BlockHeader{
		Height:       3_000_000,
		Hash:         hashLower,
		PrevHash:     hashUpper,
		Timestamp:    time.Unix(1_700_000_123, 0).UTC(),
		MajorVersion: 16,
		MinorVersion: 2,
		Difficulty:   111,
		Reward:       "600000000000",
		RewardXmr:    "0.600000000000",
		BlockSize:    1500,
		BlockWeight:  1600,
		NumTxes:      4,
		Depth:        9,
		Orphan:       true,
		MinerTxHash:  hashMixed,
	}, got)
}

func TestLatestBlockWrapsDaemonError(t *testing.T) {
	t.Parallel()

	service := New(
		&fakeDaemon{err: commerr.ErrTimeout}, options(config.NetworkMainnet),
	)

	got, err := service.LatestBlock(context.Background())

	require.ErrorIs(t, err, commerr.ErrTimeout)
	assert.Equal(t, api.BlockHeader{}, got)
}

// lookupErrorCases drives every path that maps monerod's reply to not found.
var lookupErrorCases = []struct {
	name         string
	err          error
	wantNotFound bool
}{
	{
		name:         "height above the tip",
		err:          rpcError(daemonrpc.CodeTooBigHeight),
		wantNotFound: true,
	},
	{
		name:         "internal error reported for an unknown hash",
		err:          rpcError(daemonrpc.CodeInternalError),
		wantNotFound: true,
	},
	{
		name:         "wrong parameter",
		err:          rpcError(daemonrpc.CodeWrongParam),
		wantNotFound: true,
	},
	{
		name: "rpc error wrapped by the client",
		err: ctxerrors.Wrap(
			rpcError(daemonrpc.CodeTooBigHeight), "call get_block_header",
		),
		wantNotFound: true,
	},
	{
		name: "core busy",
		err:  rpcError(daemonrpc.CodeCoreBusy),
	},
	{
		name: "restricted access",
		err:  rpcError(daemonrpc.CodeRestrictedAccess),
	},
	{
		name: "wrong wallet address code",
		err:  rpcError(daemonrpc.CodeWrongWalletAddress),
	},
	{
		name: "unknown rpc code",
		err:  rpcError(-12345),
	},
	{
		name: "transport failure",
		err:  commerr.ErrConnectFailed,
	},
	{
		name: "plain error",
		err:  errors.New("boom"),
	},
}

func TestMapLookupError(t *testing.T) {
	t.Parallel()

	for _, tc := range lookupErrorCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := mapLookupError(tc.err, "block at height")

			if tc.wantNotFound {
				require.ErrorIs(t, got, ErrNotFound)
				require.ErrorIs(t, got, commerr.ErrNotFound)

				return
			}

			require.NotErrorIs(t, got, ErrNotFound)
			require.ErrorIs(t, got, tc.err)
		})
	}
}

func TestBlockByHeight(t *testing.T) {
	t.Parallel()

	t.Run("passes the height through and converts", func(t *testing.T) {
		t.Parallel()

		daemon := &fakeDaemon{header: daemonrpc.BlockHeader{
			Height: 42, Hash: hashLower, Reward: 1_500_000_000_000,
		}}
		service := New(daemon, options(config.NetworkMainnet))

		got, err := service.BlockByHeight(context.Background(), 42)

		require.NoError(t, err)
		assert.Equal(t, uint64(42), daemon.gotHeight)
		assert.Equal(t, int64(42), got.Height)
		assert.Equal(t, hashLower, got.Hash)
		assert.Equal(t, "1500000000000", got.Reward)
		assert.Equal(t, "1.500000000000", got.RewardXmr)
	})

	t.Run("height zero is the genesis block, not an error", func(t *testing.T) {
		t.Parallel()

		daemon := &fakeDaemon{}
		service := New(daemon, options(config.NetworkMainnet))

		_, err := service.BlockByHeight(context.Background(), 0)

		require.NoError(t, err)
		assert.Equal(t, 1, daemon.calls)
		assert.Equal(t, uint64(0), daemon.gotHeight)
	})

	t.Run("negative height never reaches the daemon", func(t *testing.T) {
		t.Parallel()

		daemon := &fakeDaemon{}
		service := New(daemon, options(config.NetworkMainnet))

		got, err := service.BlockByHeight(context.Background(), -1)

		requireInvalid(t, err, "height -1 is negative")
		assert.Equal(t, api.BlockHeader{}, got)
		assert.Zero(t, daemon.calls)
	})

	for _, tc := range lookupErrorCases {
		t.Run("daemon error: "+tc.name, func(t *testing.T) {
			t.Parallel()

			service := New(
				&fakeDaemon{err: tc.err}, options(config.NetworkMainnet),
			)

			_, err := service.BlockByHeight(context.Background(), 5)

			if tc.wantNotFound {
				require.ErrorIs(t, err, ErrNotFound)

				return
			}

			require.NotErrorIs(t, err, ErrNotFound)
			require.ErrorIs(t, err, tc.err)
		})
	}
}

func TestBlockByHash(t *testing.T) {
	t.Parallel()

	t.Run("lowercases the hash for the daemon", func(t *testing.T) {
		t.Parallel()

		for _, hash := range []string{hashLower, hashUpper, hashMixed} {
			daemon := &fakeDaemon{header: daemonrpc.BlockHeader{Hash: hash}}
			service := New(daemon, options(config.NetworkMainnet))

			got, err := service.BlockByHash(context.Background(), hash)

			require.NoError(t, err)
			assert.Equal(t, strings.ToLower(hash), daemon.gotHash)
			assert.Equal(t, hash, got.Hash)
		}
	})

	badHashes := map[string]string{
		"empty":             "",
		"too short":         hashLower[:63],
		"too long":          hashLower + "0",
		"non-hex character": hashLower[:63] + "g",
		"leading space":     " " + hashLower[:63],
		"trailing newline":  hashLower + "\n",
		"0x prefix":         "0x" + hashLower[:62],
		"unicode digit":     hashLower[:63] + "٣",
	}

	for name, hash := range badHashes {
		t.Run("rejects "+name, func(t *testing.T) {
			t.Parallel()

			daemon := &fakeDaemon{}
			service := New(daemon, options(config.NetworkMainnet))

			got, err := service.BlockByHash(context.Background(), hash)

			require.ErrorIs(t, err, ErrInvalidHash)
			require.ErrorIs(t, err, commerr.ErrInvalidArgument)
			assert.Equal(t, api.BlockHeader{}, got)
			assert.Zero(t, daemon.calls)
		})
	}

	for _, tc := range lookupErrorCases {
		t.Run("daemon error: "+tc.name, func(t *testing.T) {
			t.Parallel()

			service := New(
				&fakeDaemon{err: tc.err}, options(config.NetworkMainnet),
			)

			_, err := service.BlockByHash(context.Background(), hashLower)

			if tc.wantNotFound {
				require.ErrorIs(t, err, ErrNotFound)

				return
			}

			require.NotErrorIs(t, err, ErrNotFound)
			require.ErrorIs(t, err, tc.err)
		})
	}
}

func TestTransaction(t *testing.T) {
	t.Parallel()

	t.Run("confirmed transaction", func(t *testing.T) {
		t.Parallel()

		daemon := &fakeDaemon{txs: daemonrpc.Transactions{
			Txs: []daemonrpc.Transaction{{
				TxHash:          hashLower,
				BlockHeight:     2_999_990,
				Confirmations:   11,
				BlockTimestamp:  1_700_000_000,
				Relayed:         true,
				DoubleSpendSeen: true,
			}},
		}}
		service := New(daemon, options(config.NetworkMainnet))

		got, err := service.Transaction(context.Background(), hashUpper)
		require.NoError(t, err)

		blockHeight := int64(2_999_990)
		blockTime := time.Unix(1_700_000_000, 0).UTC()

		assert.Equal(t, []string{hashLower}, daemon.gotHashes)
		assert.Equal(t, api.Transaction{
			TxHash:          hashLower,
			BlockHeight:     &blockHeight,
			BlockTimestamp:  &blockTime,
			Confirmations:   11,
			DoubleSpendSeen: true,
			Relayed:         true,
		}, got)
	})

	t.Run("pool transaction has no block fields", func(t *testing.T) {
		t.Parallel()

		daemon := &fakeDaemon{txs: daemonrpc.Transactions{
			Txs: []daemonrpc.Transaction{{
				TxHash:            hashLower,
				InPool:            true,
				ReceivedTimestamp: 1_700_000_500,
			}},
		}}
		service := New(daemon, options(config.NetworkMainnet))

		got, err := service.Transaction(context.Background(), hashLower)
		require.NoError(t, err)

		receivedAt := time.Unix(1_700_000_500, 0).UTC()

		assert.Equal(t, api.Transaction{
			TxHash:     hashLower,
			InPool:     true,
			ReceivedAt: &receivedAt,
		}, got)
	})

	t.Run("no result is not found", func(t *testing.T) {
		t.Parallel()

		daemon := &fakeDaemon{txs: daemonrpc.Transactions{
			MissedTx: []string{hashLower},
		}}
		service := New(daemon, options(config.NetworkMainnet))

		got, err := service.Transaction(context.Background(), hashLower)

		require.ErrorIs(t, err, ErrNotFound)
		require.ErrorIs(t, err, commerr.ErrNotFound)
		assert.Equal(t, api.Transaction{}, got)
	})

	t.Run("invalid hash never reaches the daemon", func(t *testing.T) {
		t.Parallel()

		daemon := &fakeDaemon{}
		service := New(daemon, options(config.NetworkMainnet))

		_, err := service.Transaction(context.Background(), "nothex")

		require.ErrorIs(t, err, ErrInvalidHash)
		assert.Zero(t, daemon.calls)
	})

	t.Run("daemon error is not turned into not found", func(t *testing.T) {
		t.Parallel()

		boom := rpcError(daemonrpc.CodeWrongParam)
		service := New(&fakeDaemon{err: boom}, options(config.NetworkMainnet))

		_, err := service.Transaction(context.Background(), hashLower)

		require.ErrorIs(t, err, boom)
		require.NotErrorIs(t, err, ErrNotFound)
	})
}

func TestFeeEstimate(t *testing.T) {
	t.Parallel()

	t.Run("converts every priority", func(t *testing.T) {
		t.Parallel()

		daemon := &fakeDaemon{fee: daemonrpc.FeeEstimate{
			Fee:              20,
			QuantizationMask: 10000,
			Fees:             []uint64{20, 80, 320, 4000},
		}}
		service := New(daemon, options(config.NetworkMainnet))

		got, err := service.FeeEstimate(context.Background())

		require.NoError(t, err)
		assert.Equal(t, api.FeeEstimate{
			FeePerByte:       "20",
			QuantizationMask: 10000,
			FeesPerByte:      []api.AtomicAmount{"20", "80", "320", "4000"},
		}, got)
	})

	t.Run("no priorities is an empty list, not null", func(t *testing.T) {
		t.Parallel()

		service := New(&fakeDaemon{}, options(config.NetworkMainnet))

		got, err := service.FeeEstimate(context.Background())

		require.NoError(t, err)
		assert.NotNil(t, got.FeesPerByte)
		assert.Empty(t, got.FeesPerByte)
		assert.Equal(t, "0", got.FeePerByte)
	})

	t.Run("daemon error", func(t *testing.T) {
		t.Parallel()

		service := New(
			&fakeDaemon{err: commerr.ErrNotReady},
			options(config.NetworkMainnet),
		)

		got, err := service.FeeEstimate(context.Background())

		require.ErrorIs(t, err, commerr.ErrNotReady)
		assert.Equal(t, api.FeeEstimate{}, got)
	})
}

func TestMempool(t *testing.T) {
	t.Parallel()

	t.Run("converts pool statistics", func(t *testing.T) {
		t.Parallel()

		daemon := &fakeDaemon{pool: daemonrpc.PoolStats{
			BytesTotal:      123456,
			FeeTotal:        2_500_000_000_000,
			Oldest:          1_700_000_000,
			TxsTotal:        11,
			NumFailing:      2,
			NumNotRelayed:   3,
			NumDoubleSpends: 1,
		}}
		service := New(daemon, options(config.NetworkMainnet))

		got, err := service.Mempool(context.Background())
		require.NoError(t, err)

		oldest := time.Unix(1_700_000_000, 0).UTC()

		assert.Equal(t, api.MempoolStats{
			TxCount:          11,
			BytesTotal:       123456,
			FeeTotal:         "2500000000000",
			FeeTotalXmr:      "2.500000000000",
			Failing:          2,
			NotRelayed:       3,
			DoubleSpends:     1,
			OldestReceivedAt: &oldest,
		}, got)
	})

	t.Run("empty pool has no oldest timestamp", func(t *testing.T) {
		t.Parallel()

		service := New(&fakeDaemon{}, options(config.NetworkMainnet))

		got, err := service.Mempool(context.Background())

		require.NoError(t, err)
		assert.Nil(t, got.OldestReceivedAt)
		assert.Equal(t, "0", got.FeeTotal)
		assert.Equal(t, "0.000000000000", got.FeeTotalXmr)
	})

	t.Run("daemon error", func(t *testing.T) {
		t.Parallel()

		service := New(
			&fakeDaemon{err: commerr.ErrUnavailable},
			options(config.NetworkMainnet),
		)

		got, err := service.Mempool(context.Background())

		require.ErrorIs(t, err, commerr.ErrUnavailable)
		assert.Equal(t, api.MempoolStats{}, got)
	})
}

func TestGenerateBlocksRefusesOutsideRegtest(t *testing.T) {
	t.Parallel()

	networks := []config.Network{
		config.NetworkMainnet,
		config.NetworkStagenet,
		config.NetworkTestnet,
		"",
	}

	for _, network := range networks {
		t.Run(string(network), func(t *testing.T) {
			t.Parallel()

			daemon := &fakeDaemon{}
			service := New(daemon, options(network))

			got, err := service.GenerateBlocks(
				context.Background(),
				api.GenerateBlocksRequest{Count: 1, Address: "addr"},
			)

			require.ErrorIs(t, err, ErrNotRegtest)
			require.ErrorIs(t, err, commerr.ErrInvalidState)
			assert.Equal(t, api.GeneratedBlocks{}, got)
			assert.Zero(t, daemon.calls)
		})
	}
}

func TestGenerateBlocksRefusedInReadOnlyMode(t *testing.T) {
	t.Parallel()

	daemon := &fakeDaemon{}
	service := New(daemon, Options{
		Network:  config.NetworkRegtest,
		ReadOnly: true,
		Restarts: func() uint64 { return 0 },
	})

	got, err := service.GenerateBlocks(
		context.Background(),
		api.GenerateBlocksRequest{Count: 1, Address: "addr"},
	)

	require.ErrorIs(t, err, access.ErrReadOnly)
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	assert.Equal(t, api.GeneratedBlocks{}, got)
	assert.Zero(t, daemon.calls)
}

func TestGenerateBlocksValidatesRequest(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		request     api.GenerateBlocksRequest
		wantCount   error
		wantMessage string
	}{
		{
			name:      "zero blocks",
			request:   api.GenerateBlocksRequest{Count: 0, Address: "addr"},
			wantCount: ErrInvalidCount,
		},
		{
			name:      "negative blocks",
			request:   api.GenerateBlocksRequest{Count: -5, Address: "addr"},
			wantCount: ErrInvalidCount,
		},
		{
			name: "one block above the limit",
			request: api.GenerateBlocksRequest{
				Count: int64(api.MaxGenerateBlocks) + 1, Address: "addr",
			},
			wantCount: ErrInvalidCount,
		},
		{
			name:        "missing address",
			request:     api.GenerateBlocksRequest{Count: 1},
			wantMessage: "address is required",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			daemon := &fakeDaemon{}
			service := New(daemon, options(config.NetworkRegtest))

			got, err := service.GenerateBlocks(context.Background(), tc.request)

			if tc.wantCount != nil {
				require.ErrorIs(t, err, tc.wantCount)
			} else {
				requireInvalid(t, err, tc.wantMessage)
			}

			assert.Equal(t, api.GeneratedBlocks{}, got)
			assert.Zero(t, daemon.calls)
		})
	}
}

func TestGenerateBlocksMines(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		count int64
		want  uint64
	}{
		{count: 1, want: 1},
		{
			count: int64(api.MaxGenerateBlocks),
			want:  uint64(api.MaxGenerateBlocks),
		},
	}

	for _, tc := range testCases {
		t.Run(strconv.FormatInt(tc.count, 10), func(t *testing.T) {
			t.Parallel()

			daemon := &fakeDaemon{generated: daemonrpc.GeneratedBlocks{
				Height: 250,
				Blocks: []string{hashLower, hashUpper},
			}}
			service := New(daemon, options(config.NetworkRegtest))

			got, err := service.GenerateBlocks(
				context.Background(),
				api.GenerateBlocksRequest{
					Count:   tc.count,
					Address: "miner-address",
				},
			)

			require.NoError(t, err)
			assert.Equal(t, tc.want, daemon.gotCount)
			assert.Equal(t, "miner-address", daemon.gotAddress)
			assert.Equal(t, api.GeneratedBlocks{
				Height:      250,
				BlockHashes: []string{hashLower, hashUpper},
			}, got)
		})
	}
}

func TestGenerateBlocksNoHashesIsAnEmptyList(t *testing.T) {
	t.Parallel()

	service := New(&fakeDaemon{}, options(config.NetworkRegtest))

	got, err := service.GenerateBlocks(
		context.Background(),
		api.GenerateBlocksRequest{Count: 1, Address: "addr"},
	)

	require.NoError(t, err)
	assert.NotNil(t, got.BlockHashes)
	assert.Empty(t, got.BlockHashes)
}

func TestGenerateBlocksDaemonErrors(t *testing.T) {
	t.Parallel()

	t.Run("wrong address becomes a client-safe message", func(t *testing.T) {
		t.Parallel()

		daemon := &fakeDaemon{
			err: ctxerrors.Wrap(
				rpcError(daemonrpc.CodeWrongWalletAddress),
				"call generateblocks",
			),
		}
		service := New(daemon, options(config.NetworkRegtest))

		got, err := service.GenerateBlocks(
			context.Background(),
			api.GenerateBlocksRequest{Count: 1, Address: "bad"},
		)

		requireInvalid(t, err, "the address is not valid")
		assert.Equal(t, api.GeneratedBlocks{}, got)
	})

	t.Run("other rpc errors stay internal", func(t *testing.T) {
		t.Parallel()

		boom := rpcError(daemonrpc.CodeRegtestRequired)
		service := New(&fakeDaemon{err: boom}, options(config.NetworkRegtest))

		_, err := service.GenerateBlocks(
			context.Background(),
			api.GenerateBlocksRequest{Count: 1, Address: "addr"},
		)

		require.ErrorIs(t, err, boom)

		_, isClientSafe := invalid.Message(err)
		assert.False(t, isClientSafe)
	})

	t.Run("transport errors keep their class", func(t *testing.T) {
		t.Parallel()

		service := New(
			&fakeDaemon{err: commerr.ErrTimeout},
			options(config.NetworkRegtest),
		)

		_, err := service.GenerateBlocks(
			context.Background(),
			api.GenerateBlocksRequest{Count: 1, Address: "addr"},
		)

		require.ErrorIs(t, err, commerr.ErrTimeout)
	})
}

func TestInfoWithoutRestartCounter(t *testing.T) {
	service := New(&fakeDaemon{}, Options{Network: config.NetworkMainnet})

	got, err := service.Info(context.Background())

	require.NoError(t, err)
	assert.Zero(t, got.Restarts)
}

package daemonrpc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/docker-monero/internal/pkg/monero/jsonrpc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testTimeout = 5 * time.Second

type recordedCall struct {
	httpMethod  string
	path        string
	contentType string
	rpcMethod   string
	params      json.RawMessage
}

type replyFunc func(call recordedCall) (status int, body string)

// fakeMonerod answers the JSON-RPC and plain endpoints the client uses and
// records every request it receives.
type fakeMonerod struct {
	t     *testing.T
	reply replyFunc

	mu    sync.Mutex
	calls []recordedCall
}

func newFakeMonerod(t *testing.T, reply replyFunc) (*Client, *fakeMonerod) {
	t.Helper()

	fake := &fakeMonerod{t: t, reply: reply}
	server := httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(server.Close)

	return New(server.URL, testTimeout), fake
}

func (f *fakeMonerod) serve(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	assert.NoError(f.t, err)

	call := recordedCall{
		httpMethod:  r.Method,
		path:        r.URL.Path,
		contentType: r.Header.Get("Content-Type"),
		params:      body,
	}

	if r.URL.Path == "/json_rpc" {
		var envelope struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      string          `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}

		assert.NoError(f.t, json.Unmarshal(body, &envelope))
		assert.Equal(f.t, "2.0", envelope.JSONRPC)
		assert.Equal(f.t, "0", envelope.ID)

		call.rpcMethod = envelope.Method
		call.params = envelope.Params
	}

	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.mu.Unlock()

	status, reply := f.reply(call)

	w.WriteHeader(status)
	_, err = io.WriteString(w, reply)
	assert.NoError(f.t, err)
}

func (f *fakeMonerod) recorded() []recordedCall {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]recordedCall(nil), f.calls...)
}

// only returns the single request the fake saw.
func (f *fakeMonerod) only(t *testing.T) recordedCall {
	t.Helper()

	calls := f.recorded()
	require.Len(t, calls, 1)

	return calls[0]
}

func always(status int, body string) replyFunc {
	return func(recordedCall) (int, string) { return status, body }
}

func rpcResult(payload string) string {
	return `{"jsonrpc":"2.0","id":"0","result":` + payload + `}`
}

func rpcFailure(code int, message string) string {
	return fmt.Sprintf(
		`{"jsonrpc":"2.0","id":"0","error":{"code":%d,"message":%q}}`,
		code, message,
	)
}

func requireRPCCall(
	t *testing.T,
	call recordedCall,
	method, wantParams string,
) {
	t.Helper()

	assert.Equal(t, http.MethodPost, call.httpMethod)
	assert.Equal(t, "/json_rpc", call.path)
	assert.Equal(t, "application/json", call.contentType)
	assert.Equal(t, method, call.rpcMethod)

	if wantParams == "" {
		assert.Empty(t, call.params, "no params expected")

		return
	}

	assert.JSONEq(t, wantParams, string(call.params))
}

func TestNewTrimsTrailingSlashFromBaseURL(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/json_rpc", r.URL.Path)

			_, err := io.WriteString(w, rpcResult(`{"version":1}`))
			assert.NoError(t, err)
		},
	))
	t.Cleanup(server.Close)

	client := New(server.URL+"/", testTimeout)

	version, err := client.GetVersion(context.Background())
	require.NoError(t, err)
	assert.Equal(t, uint32(1), version.Version)
}

func TestGetInfo(t *testing.T) {
	t.Parallel()

	client, fake := newFakeMonerod(t, always(http.StatusOK, rpcResult(`{
		"status": "OK",
		"height": 3000000,
		"target_height": 3000010,
		"difficulty": 400000000000,
		"target": 120,
		"tx_count": 11,
		"tx_pool_size": 12,
		"alt_blocks_count": 1,
		"outgoing_connections_count": 13,
		"incoming_connections_count": 14,
		"white_peerlist_size": 15,
		"grey_peerlist_size": 16,
		"nettype": "mainnet",
		"top_block_hash": "aa",
		"cumulative_difficulty": 99,
		"block_weight_limit": 600000,
		"block_weight_median": 300000,
		"adjusted_time": 1700000000,
		"start_time": 1690000000,
		"free_space": 5000,
		"offline": true,
		"database_size": 123456789,
		"update_available": true,
		"busy_syncing": true,
		"version": "0.18.4.0-release",
		"synchronized": true,
		"restricted": true
	}`)))

	info, err := client.GetInfo(context.Background())
	require.NoError(t, err)

	assert.Equal(t, Info{
		Height:                   3000000,
		TargetHeight:             3000010,
		Difficulty:               400000000000,
		Target:                   120,
		TxCount:                  11,
		TxPoolSize:               12,
		AltBlocksCount:           1,
		OutgoingConnectionsCount: 13,
		IncomingConnectionsCount: 14,
		WhitePeerlistSize:        15,
		GreyPeerlistSize:         16,
		Nettype:                  "mainnet",
		TopBlockHash:             "aa",
		CumulativeDifficulty:     99,
		BlockWeightLimit:         600000,
		BlockWeightMedian:        300000,
		AdjustedTime:             1700000000,
		StartTime:                1690000000,
		FreeSpace:                5000,
		Offline:                  true,
		DatabaseSize:             123456789,
		UpdateAvailable:          true,
		BusySyncing:              true,
		Version:                  "0.18.4.0-release",
		Synchronized:             true,
		Restricted:               true,
	}, info)

	requireRPCCall(t, fake.only(t), "get_info", "")
}

func TestGetVersion(t *testing.T) {
	t.Parallel()

	client, fake := newFakeMonerod(t, always(http.StatusOK, rpcResult(
		`{"status":"OK","version":196617,"release":true}`,
	)))

	version, err := client.GetVersion(context.Background())
	require.NoError(t, err)

	assert.Equal(t, Version{Version: 196617, Release: true}, version)
	requireRPCCall(t, fake.only(t), "get_version", "")
}

const headerJSON = `{
	"major_version": 16,
	"minor_version": 17,
	"timestamp": 1700000123,
	"prev_hash": "prev",
	"nonce": 4294967295,
	"orphan_status": true,
	"height": 777,
	"depth": 3,
	"hash": "hash",
	"difficulty": 1000,
	"reward": 600000000000,
	"block_size": 1500,
	"block_weight": 1600,
	"num_txes": 4,
	"miner_tx_hash": "miner"
}`

var wantHeader = BlockHeader{
	MajorVersion: 16,
	MinorVersion: 17,
	Timestamp:    1700000123,
	PrevHash:     "prev",
	Nonce:        4294967295,
	OrphanStatus: true,
	Height:       777,
	Depth:        3,
	Hash:         "hash",
	Difficulty:   1000,
	Reward:       600000000000,
	BlockSize:    1500,
	BlockWeight:  1600,
	NumTxes:      4,
	MinerTxHash:  "miner",
}

func TestBlockHeaderLookups(t *testing.T) {
	t.Parallel()

	reply := always(http.StatusOK, rpcResult(
		`{"status":"OK","block_header":`+headerJSON+`}`,
	))

	testCases := []struct {
		name       string
		call       func(*Client) (BlockHeader, error)
		wantMethod string
		wantParams string
	}{
		{
			name: "last block",
			call: func(c *Client) (BlockHeader, error) {
				return c.GetLastBlockHeader(context.Background())
			},
			wantMethod: "get_last_block_header",
		},
		{
			name: "by height",
			call: func(c *Client) (BlockHeader, error) {
				return c.GetBlockHeaderByHeight(context.Background(), 777)
			},
			wantMethod: "get_block_header_by_height",
			wantParams: `{"height":777}`,
		},
		{
			name: "by height at the uint64 limit",
			call: func(c *Client) (BlockHeader, error) {
				return c.GetBlockHeaderByHeight(
					context.Background(), 18446744073709551615,
				)
			},
			wantMethod: "get_block_header_by_height",
			wantParams: `{"height":18446744073709551615}`,
		},
		{
			name: "by hash",
			call: func(c *Client) (BlockHeader, error) {
				return c.GetBlockHeaderByHash(context.Background(), "hash")
			},
			wantMethod: "get_block_header_by_hash",
			wantParams: `{"hash":"hash"}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			client, fake := newFakeMonerod(t, reply)

			header, err := tc.call(client)
			require.NoError(t, err)

			assert.Equal(t, wantHeader, header)
			requireRPCCall(t, fake.only(t), tc.wantMethod, tc.wantParams)
		})
	}
}

func TestGetFeeEstimate(t *testing.T) {
	t.Parallel()

	client, fake := newFakeMonerod(t, always(http.StatusOK, rpcResult(`{
		"status": "OK",
		"fee": 20000,
		"quantization_mask": 10000,
		"fees": [20000, 80000, 320000, 4000000]
	}`)))

	estimate, err := client.GetFeeEstimate(context.Background())
	require.NoError(t, err)

	assert.Equal(t, FeeEstimate{
		Fee:              20000,
		QuantizationMask: 10000,
		Fees:             []uint64{20000, 80000, 320000, 4000000},
	}, estimate)
	requireRPCCall(t, fake.only(t), "get_fee_estimate", "")
}

func TestGetTransactionPoolStats(t *testing.T) {
	t.Parallel()

	client, fake := newFakeMonerod(t, always(http.StatusOK, `{
		"status": "OK",
		"pool_stats": {
			"bytes_total": 9000,
			"bytes_min": 1,
			"bytes_max": 4000000000,
			"bytes_med": 300,
			"fee_total": 777,
			"oldest": 1700000000,
			"txs_total": 6,
			"num_failing": 1,
			"num_10m": 2,
			"num_not_relayed": 3,
			"num_double_spends": 4
		}
	}`))

	stats, err := client.GetTransactionPoolStats(context.Background())
	require.NoError(t, err)

	assert.Equal(t, PoolStats{
		BytesTotal:      9000,
		BytesMin:        1,
		BytesMax:        4000000000,
		BytesMed:        300,
		FeeTotal:        777,
		Oldest:          1700000000,
		TxsTotal:        6,
		NumFailing:      1,
		Num10m:          2,
		NumNotRelayed:   3,
		NumDoubleSpends: 4,
	}, stats)

	call := fake.only(t)
	assert.Equal(t, http.MethodPost, call.httpMethod)
	assert.Equal(t, "/get_transaction_pool_stats", call.path)
	assert.Equal(t, "application/json", call.contentType)
	assert.JSONEq(t, `{}`, string(call.params))
}

func TestGetTransactions(t *testing.T) {
	t.Parallel()

	client, fake := newFakeMonerod(t, always(http.StatusOK, `{
		"status": "OK",
		"txs": [
			{
				"tx_hash": "t1",
				"in_pool": false,
				"double_spend_seen": true,
				"block_height": 10,
				"confirmations": 5,
				"block_timestamp": 1700000001,
				"relayed": true,
				"received_timestamp": 1700000002
			},
			{"tx_hash": "t2", "in_pool": true}
		],
		"missed_tx": ["t3"]
	}`))

	result, err := client.GetTransactions(
		context.Background(), []string{"t1", "t2", "t3"},
	)
	require.NoError(t, err)

	assert.Equal(t, Transactions{
		Txs: []Transaction{
			{
				TxHash:            "t1",
				DoubleSpendSeen:   true,
				BlockHeight:       10,
				Confirmations:     5,
				BlockTimestamp:    1700000001,
				Relayed:           true,
				ReceivedTimestamp: 1700000002,
			},
			{TxHash: "t2", InPool: true},
		},
		MissedTx: []string{"t3"},
	}, result)

	call := fake.only(t)
	assert.Equal(t, http.MethodPost, call.httpMethod)
	assert.Equal(t, "/get_transactions", call.path)
	assert.Equal(t, "application/json", call.contentType)
	assert.JSONEq(t, `{"txs_hashes":["t1","t2","t3"]}`, string(call.params))
}

func TestGetTransactionsLimit(t *testing.T) {
	t.Parallel()

	hashes := func(count int) []string {
		result := make([]string, count)
		for i := range result {
			result[i] = fmt.Sprintf("hash-%d", i)
		}

		return result
	}

	t.Run("exactly the limit is sent", func(t *testing.T) {
		t.Parallel()

		client, fake := newFakeMonerod(
			t, always(http.StatusOK, `{"status":"OK","txs":[]}`),
		)

		_, err := client.GetTransactions(context.Background(), hashes(100))
		require.NoError(t, err)

		var sent map[string][]string

		require.NoError(t, json.Unmarshal(fake.only(t).params, &sent))
		assert.Len(t, sent["txs_hashes"], 100)
	})

	t.Run("one past the limit never reaches monerod", func(t *testing.T) {
		t.Parallel()

		client, fake := newFakeMonerod(
			t, always(http.StatusOK, `{"status":"OK","txs":[]}`),
		)

		_, err := client.GetTransactions(context.Background(), hashes(101))
		require.ErrorIs(t, err, ErrTooManyTransactions)
		assert.Contains(t, err.Error(), "at most 100 transactions per request")
		assert.Empty(t, fake.recorded())
	})
}

func TestGenerateBlocks(t *testing.T) {
	t.Parallel()

	client, fake := newFakeMonerod(t, always(http.StatusOK, rpcResult(`{
		"status": "OK",
		"height": 105,
		"blocks": ["b1", "b2", "b3"]
	}`)))

	result, err := client.GenerateBlocks(context.Background(), 3, "4Address")
	require.NoError(t, err)

	assert.Equal(t, GeneratedBlocks{
		Height: 105,
		Blocks: []string{"b1", "b2", "b3"},
	}, result)
	requireRPCCall(
		t, fake.only(t), "generateblocks",
		`{"amount_of_blocks":3,"wallet_address":"4Address"}`,
	)
}

type namedCall struct {
	name string
	call func(*Client) error
}

// jsonRPCCalls are the client methods that reach monerod through /json_rpc.
var jsonRPCCalls = []namedCall{
	{"GetInfo", func(c *Client) error {
		_, err := c.GetInfo(context.Background())

		return err
	}},
	{"GetVersion", func(c *Client) error {
		_, err := c.GetVersion(context.Background())

		return err
	}},
	{"GetLastBlockHeader", func(c *Client) error {
		_, err := c.GetLastBlockHeader(context.Background())

		return err
	}},
	{"GetBlockHeaderByHeight", func(c *Client) error {
		_, err := c.GetBlockHeaderByHeight(context.Background(), 1)

		return err
	}},
	{"GetBlockHeaderByHash", func(c *Client) error {
		_, err := c.GetBlockHeaderByHash(context.Background(), "h")

		return err
	}},
	{"GetFeeEstimate", func(c *Client) error {
		_, err := c.GetFeeEstimate(context.Background())

		return err
	}},
	{"GenerateBlocks", func(c *Client) error {
		_, err := c.GenerateBlocks(context.Background(), 1, "addr")

		return err
	}},
}

// plainCalls are the client methods that reach monerod through a plain
// endpoint, whose failures arrive as a status field instead of an error
// object.
var plainCalls = []namedCall{
	{"GetTransactionPoolStats", func(c *Client) error {
		_, err := c.GetTransactionPoolStats(context.Background())

		return err
	}},
	{"GetTransactions", func(c *Client) error {
		_, err := c.GetTransactions(context.Background(), []string{"h"})

		return err
	}},
}

func everyCall() []namedCall {
	return append(append([]namedCall{}, jsonRPCCalls...), plainCalls...)
}

func TestDaemonErrorObjectSurfacesCodeAndMessage(t *testing.T) {
	t.Parallel()

	for _, tc := range jsonRPCCalls {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			client, _ := newFakeMonerod(t, always(
				http.StatusOK, rpcFailure(CodeTooBigHeight, "height too big"),
			))

			err := tc.call(client)
			require.Error(t, err)
			require.ErrorIs(t, err, jsonrpc.ErrRPC)

			var rpcErr *jsonrpc.Error
			require.ErrorAs(t, err, &rpcErr)
			assert.Equal(t, CodeTooBigHeight, rpcErr.Code)
			assert.Equal(t, "height too big", rpcErr.Message)
		})
	}
}

func TestPlainEndpointStatusFailures(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		body    string
		wantErr error
	}{
		{
			name:    "busy daemon is not ready",
			body:    `{"status":"BUSY"}`,
			wantErr: commerr.ErrNotReady,
		},
		{
			name:    "any other status is a failure",
			body:    `{"status":"Failed"}`,
			wantErr: jsonrpc.ErrStatus,
		},
	}

	for _, tc := range testCases {
		for _, plain := range plainCalls {
			t.Run(tc.name+"/"+plain.name, func(t *testing.T) {
				t.Parallel()

				client, _ := newFakeMonerod(t, always(http.StatusOK, tc.body))

				require.ErrorIs(t, plain.call(client), tc.wantErr)
			})
		}
	}
}

func TestTransportFailuresAreClassified(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		reply   replyFunc
		wantErr error
	}{
		{
			name:    "http error status means unavailable",
			reply:   always(http.StatusInternalServerError, "boom"),
			wantErr: commerr.ErrUnavailable,
		},
		{
			name:    "garbage envelope",
			reply:   always(http.StatusOK, "not json"),
			wantErr: commerr.ErrUnmarshalFailed,
		},
		{
			name: "result that does not fit the type",
			reply: always(
				http.StatusOK, rpcResult(`{"height":"not a number"}`),
			),
			wantErr: commerr.ErrUnmarshalFailed,
		},
		{
			name:    "busy daemon is not ready",
			reply:   always(http.StatusOK, rpcResult(`{"status":"BUSY"}`)),
			wantErr: commerr.ErrNotReady,
		},
		{
			name:    "unknown status",
			reply:   always(http.StatusOK, rpcResult(`{"status":"NOPE"}`)),
			wantErr: jsonrpc.ErrStatus,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			client, _ := newFakeMonerod(t, tc.reply)

			_, err := client.GetInfo(context.Background())
			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

func TestUnreachableDaemon(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.NotFoundHandler())
	url := server.URL

	server.Close()

	client := New(url, testTimeout)

	for _, tc := range everyCall() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.ErrorIs(t, tc.call(client), commerr.ErrConnectFailed)
		})
	}
}

func TestSlowDaemonTimesOut(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(
		func(_ http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)

			select {
			case <-r.Context().Done():
			case <-release:
			}
		},
	))

	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })

	client := New(server.URL, 50*time.Millisecond)

	_, err := client.GetInfo(context.Background())
	require.ErrorIs(t, err, commerr.ErrTimeout)
}

func TestCancelledContextIsNotReportedAsADaemonFault(t *testing.T) {
	t.Parallel()

	client, fake := newFakeMonerod(
		t, always(http.StatusOK, rpcResult(`{}`)),
	)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.GetInfo(ctx)
	require.ErrorIs(t, err, context.Canceled)
	assert.NotErrorIs(t, err, commerr.ErrConnectFailed)
	assert.NotErrorIs(t, err, commerr.ErrTimeout)
	assert.Empty(t, fake.recorded())
}

func TestErrorContextNamesTheFailedOperation(t *testing.T) {
	t.Parallel()

	client, _ := newFakeMonerod(t, always(
		http.StatusOK, rpcFailure(CodeInternalError, "internal"),
	))

	_, err := client.GetBlockHeaderByHeight(context.Background(), 42)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "get block header at height 42")

	_, err = client.GenerateBlocks(context.Background(), 7, "addr")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "generate 7 blocks")
}

func TestCoreErrorCodesMatchMonerod(t *testing.T) {
	t.Parallel()

	assert.Equal(t, -1, CodeWrongParam)
	assert.Equal(t, -2, CodeTooBigHeight)
	assert.Equal(t, -4, CodeWrongWalletAddress)
	assert.Equal(t, -5, CodeInternalError)
	assert.Equal(t, -9, CodeCoreBusy)
	assert.Equal(t, -13, CodeRegtestRequired)
	assert.Equal(t, -19, CodeRestrictedAccess)
}

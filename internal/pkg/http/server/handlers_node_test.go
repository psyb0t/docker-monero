package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/psyb0t/docker-monero/internal/pkg/config"
	"github.com/psyb0t/docker-monero/internal/pkg/http/api"
	"github.com/psyb0t/docker-monero/internal/pkg/monero/daemonrpc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	codeBadRequest  = "BAD_REQUEST"
	codeNotFound    = "NOT_FOUND"
	messageBadHash  = "hashes are 64 hexadecimal characters"
	messageNotFound = "no such block or transaction"
	messageDecode   = "the request could not be decoded"
	messageNoMonero = "monerod is not available yet"
	messageCount    = "count is out of range"
)

func TestGetNodeInfoOfASyncedNode(t *testing.T) {
	s := newStack(t, stackOptions{})
	s.restarts.Store(4)

	resp := s.get(t, "/v1/node/info")
	requireStatus(t, resp, http.StatusOK)
	assert.Equal(t, "application/json", resp.header.Get("Content-Type"))

	info := decodeAs[api.NodeInfo](t, resp)
	require.NotNil(t, info.StartedAt)

	assert.True(t, info.StartedAt.Equal(time.Unix(1700000000, 0)))
	assert.Equal(t, api.Network("regtest"), info.Network)
	assert.EqualValues(t, 1000, info.Height)
	assert.True(t, info.Synchronized)
	assert.InDelta(t, 100, info.SyncPercent, 0)
	assert.EqualValues(t, 4, info.Restarts)
	assert.EqualValues(t, 8, info.OutgoingConnections)
	assert.EqualValues(t, 2, info.IncomingConnections)
	assert.EqualValues(t, 3, info.TxPoolSize)
	assert.EqualValues(t, 123456, info.DatabaseSize)
	assert.Equal(t, "0.18.4.0-release", info.Version)
	assert.Equal(t, testHash, info.TopBlockHash)
}

func TestGetNodeInfoOfASyncingNode(t *testing.T) {
	s := newStack(t, stackOptions{network: config.NetworkMainnet})
	s.daemon.update(func(fake *fakeMonerod) {
		fake.info.Height = 500
		fake.info.TargetHeight = 2000
		fake.info.Synchronized = false
		fake.info.BusySyncing = true
		fake.info.StartTime = 0
	})

	resp := s.get(t, "/v1/node/info")
	requireStatus(t, resp, http.StatusOK)

	info := decodeAs[api.NodeInfo](t, resp)
	assert.Equal(t, api.Network("mainnet"), info.Network)
	assert.False(t, info.Synchronized)
	assert.True(t, info.BusySyncing)
	assert.InDelta(t, 25, info.SyncPercent, 1e-9)
	assert.EqualValues(t, 2000, info.TargetHeight)
	assert.Nil(t, info.StartedAt)
	assert.NotContains(t, resp.text(), "startedAt")
}

func TestGetLatestBlock(t *testing.T) {
	s := newStack(t, stackOptions{})

	resp := s.get(t, "/v1/node/blocks/latest")
	requireStatus(t, resp, http.StatusOK)

	assert.Equal(t, wantHeader(999), decodeAs[api.BlockHeader](t, resp))
}

func wantHeader(height int64) api.BlockHeader {
	return api.BlockHeader{
		Height:       height,
		Hash:         testHash,
		PrevHash:     strings.Repeat("cd", 32),
		Timestamp:    time.Unix(1700000500, 0).UTC(),
		MajorVersion: 16,
		MinorVersion: 16,
		Difficulty:   1,
		Reward:       "600000000000",
		RewardXmr:    "0.600000000000",
		BlockSize:    1500,
		BlockWeight:  1600,
		NumTxes:      2,
		Depth:        1,
		MinerTxHash:  strings.Repeat("ef", 32),
	}
}

func TestGetBlockByHeight(t *testing.T) {
	s := newStack(t, stackOptions{})

	testCases := []struct {
		name       string
		path       string
		wantHeight int64
	}{
		{name: "genesis", path: "0", wantHeight: 0},
		{name: "ordinary", path: "5", wantHeight: 5},
		{
			name:       "largest int64",
			path:       "9223372036854775807",
			wantHeight: 9223372036854775807,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resp := s.get(t, "/v1/node/blocks/height/"+tc.path)
			requireStatus(t, resp, http.StatusOK)

			assert.Equal(
				t, wantHeader(tc.wantHeight),
				decodeAs[api.BlockHeader](t, resp),
			)
		})
	}

	sent := s.daemon.paramsFor("get_block_header_by_height")
	require.Len(t, sent, len(testCases))
	assert.JSONEq(t, `{"height":0}`, string(sent[0]))
	assert.JSONEq(t, `{"height":5}`, string(sent[1]))
	assert.JSONEq(t, `{"height":9223372036854775807}`, string(sent[2]))
}

func TestGetBlockByHeightRejectsBadHeights(t *testing.T) {
	s := newStack(t, stackOptions{})

	testCases := []struct {
		name        string
		path        string
		wantMessage string
	}{
		{
			name:        "negative",
			path:        "-1",
			wantMessage: "height -1 is negative",
		},
		{
			name:        "not a number",
			path:        "abc",
			wantMessage: messageDecode,
		},
		{
			name:        "fraction",
			path:        "1.5",
			wantMessage: messageDecode,
		},
		{
			name:        "past int64",
			path:        "9223372036854775808",
			wantMessage: messageDecode,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resp := s.get(t, "/v1/node/blocks/height/"+tc.path)

			requireError(
				t, resp, http.StatusBadRequest, codeBadRequest, tc.wantMessage,
			)
		})
	}

	assert.Empty(t, s.daemon.paramsFor("get_block_header_by_height"))
}

func TestGetBlockByHeightMapsDaemonLookupFailures(t *testing.T) {
	testCases := []struct {
		name       string
		code       int
		wantStatus int
		wantCode   string
		wantText   string
	}{
		{
			name:       "height above the tip",
			code:       daemonrpc.CodeTooBigHeight,
			wantStatus: http.StatusNotFound,
			wantCode:   codeNotFound,
			wantText:   messageNotFound,
		},
		{
			name:       "invalid parameter",
			code:       daemonrpc.CodeWrongParam,
			wantStatus: http.StatusNotFound,
			wantCode:   codeNotFound,
			wantText:   messageNotFound,
		},
		{
			name:       "daemon busy",
			code:       daemonrpc.CodeCoreBusy,
			wantStatus: http.StatusInternalServerError,
			wantCode:   "INTERNAL_SERVER_ERROR",
			wantText:   "the request could not be completed",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			s := newStack(t, stackOptions{})
			s.daemon.fail(
				"get_block_header_by_height", tc.code, "monerod says no",
			)

			resp := s.get(t, "/v1/node/blocks/height/50000")

			requireError(t, resp, tc.wantStatus, tc.wantCode, tc.wantText)
			assert.NotContains(t, resp.text(), "monerod says no")
		})
	}
}

func TestGetBlockByHash(t *testing.T) {
	s := newStack(t, stackOptions{})
	upper := strings.ToUpper(testHash)

	resp := s.get(t, "/v1/node/blocks/hash/"+upper)
	requireStatus(t, resp, http.StatusOK)

	assert.Equal(t, wantHeader(999), decodeAs[api.BlockHeader](t, resp))

	sent := s.daemon.paramsFor("get_block_header_by_hash")
	require.Len(t, sent, 1)
	assert.JSONEq(t, `{"hash":"`+testHash+`"}`, string(sent[0]))
}

func TestHashEndpointsRejectMalformedHashes(t *testing.T) {
	s := newStack(t, stackOptions{})

	hashes := map[string]string{
		"too short":  strings.Repeat("a", 63),
		"too long":   strings.Repeat("a", 65),
		"not hex":    strings.Repeat("g", 64),
		"hex prefix": "0x" + strings.Repeat("a", 62),
		"padded":     "%20" + strings.Repeat("a", 63),
	}

	for _, endpoint := range []string{
		"/v1/node/blocks/hash/", "/v1/node/transactions/",
	} {
		for name, hash := range hashes {
			t.Run(endpoint+name, func(t *testing.T) {
				resp := s.get(t, endpoint+hash)

				requireError(
					t, resp, http.StatusBadRequest, codeBadRequest,
					messageBadHash,
				)
			})
		}
	}

	assert.Empty(t, s.daemon.paramsFor("get_block_header_by_hash"))
	assert.Empty(t, s.daemon.paramsFor("/get_transactions"))
}

func TestGetBlockByHashOfAnUnknownBlock(t *testing.T) {
	s := newStack(t, stackOptions{})
	s.daemon.fail(
		"get_block_header_by_hash", daemonrpc.CodeInternalError, "not found",
	)

	resp := s.get(t, "/v1/node/blocks/hash/"+testHash)

	requireError(t, resp, http.StatusNotFound, codeNotFound, messageNotFound)
}

func TestGetTransactionInABlock(t *testing.T) {
	s := newStack(t, stackOptions{})
	s.daemon.update(func(fake *fakeMonerod) {
		fake.txs.Txs = []daemonrpc.Transaction{{
			TxHash:         testHash,
			BlockHeight:    990,
			Confirmations:  10,
			BlockTimestamp: 1700000400,
			Relayed:        true,
		}}
	})

	resp := s.get(t, "/v1/node/transactions/"+strings.ToUpper(testHash))
	requireStatus(t, resp, http.StatusOK)

	tx := decodeAs[api.Transaction](t, resp)
	require.NotNil(t, tx.BlockHeight)
	require.NotNil(t, tx.BlockTimestamp)

	assert.Equal(t, testHash, tx.TxHash)
	assert.False(t, tx.InPool)
	assert.EqualValues(t, 990, *tx.BlockHeight)
	assert.True(t, tx.BlockTimestamp.Equal(time.Unix(1700000400, 0)))
	assert.EqualValues(t, 10, tx.Confirmations)
	assert.True(t, tx.Relayed)
	assert.Nil(t, tx.ReceivedAt)

	sent := s.daemon.paramsFor("/get_transactions")
	require.Len(t, sent, 1)
	assert.JSONEq(t, `{"txs_hashes":["`+testHash+`"]}`, string(sent[0]))
}

func TestGetTransactionInThePool(t *testing.T) {
	s := newStack(t, stackOptions{})
	s.daemon.update(func(fake *fakeMonerod) {
		fake.txs.Txs = []daemonrpc.Transaction{{
			TxHash:            testHash,
			InPool:            true,
			DoubleSpendSeen:   true,
			ReceivedTimestamp: 1700000300,
		}}
	})

	resp := s.get(t, "/v1/node/transactions/"+testHash)
	requireStatus(t, resp, http.StatusOK)

	tx := decodeAs[api.Transaction](t, resp)
	require.NotNil(t, tx.ReceivedAt)

	assert.True(t, tx.InPool)
	assert.True(t, tx.DoubleSpendSeen)
	assert.True(t, tx.ReceivedAt.Equal(time.Unix(1700000300, 0)))
	assert.Nil(t, tx.BlockHeight)
	assert.NotContains(t, resp.text(), "blockHeight")
	assert.NotContains(t, resp.text(), "blockTimestamp")
}

func TestGetTransactionTheNodeDoesNotKnow(t *testing.T) {
	s := newStack(t, stackOptions{})
	s.daemon.update(func(fake *fakeMonerod) {
		fake.txs.MissedTx = []string{testHash}
	})

	resp := s.get(t, "/v1/node/transactions/"+testHash)

	requireError(t, resp, http.StatusNotFound, codeNotFound, messageNotFound)
}

func TestGetFeeEstimate(t *testing.T) {
	s := newStack(t, stackOptions{})

	resp := s.get(t, "/v1/node/fee-estimate")
	requireStatus(t, resp, http.StatusOK)

	assert.Equal(t, api.FeeEstimate{
		FeePerByte:       "20000",
		QuantizationMask: 10000,
		FeesPerByte: []api.AtomicAmount{
			"20000", "80000", "320000", "4000000",
		},
	}, decodeAs[api.FeeEstimate](t, resp))
}

func TestGetMempoolStats(t *testing.T) {
	s := newStack(t, stackOptions{})
	s.daemon.update(func(fake *fakeMonerod) {
		fake.pool = daemonrpc.PoolStats{
			BytesTotal:      9000,
			FeeTotal:        1500000000000,
			Oldest:          1700000100,
			TxsTotal:        6,
			NumFailing:      1,
			NumNotRelayed:   3,
			NumDoubleSpends: 4,
		}
	})

	resp := s.get(t, "/v1/node/mempool")
	requireStatus(t, resp, http.StatusOK)

	stats := decodeAs[api.MempoolStats](t, resp)
	require.NotNil(t, stats.OldestReceivedAt)

	assert.EqualValues(t, 6, stats.TxCount)
	assert.EqualValues(t, 9000, stats.BytesTotal)
	assert.Equal(t, "1500000000000", stats.FeeTotal)
	assert.Equal(t, "1.500000000000", stats.FeeTotalXmr)
	assert.EqualValues(t, 1, stats.Failing)
	assert.EqualValues(t, 3, stats.NotRelayed)
	assert.EqualValues(t, 4, stats.DoubleSpends)
	assert.True(t, stats.OldestReceivedAt.Equal(time.Unix(1700000100, 0)))
}

func TestGetMempoolStatsOfAnEmptyPool(t *testing.T) {
	s := newStack(t, stackOptions{})

	resp := s.get(t, "/v1/node/mempool")
	requireStatus(t, resp, http.StatusOK)

	stats := decodeAs[api.MempoolStats](t, resp)
	assert.Zero(t, stats.TxCount)
	assert.Equal(t, "0", stats.FeeTotal)
	assert.Equal(t, "0.000000000000", stats.FeeTotalXmr)
	assert.Nil(t, stats.OldestReceivedAt)
	assert.NotContains(t, resp.text(), "oldestReceivedAt")
}

func TestNodeEndpointsReportAnUnavailableMonerod(t *testing.T) {
	endpoints := []struct {
		path string
		key  string
	}{
		{"/v1/node/info", "get_info"},
		{"/v1/node/blocks/latest", "get_last_block_header"},
		{"/v1/node/blocks/height/1", "get_block_header_by_height"},
		{"/v1/node/blocks/hash/" + testHash, "get_block_header_by_hash"},
		{"/v1/node/transactions/" + testHash, "/get_transactions"},
		{"/v1/node/fee-estimate", "get_fee_estimate"},
		{"/v1/node/mempool", "/get_transaction_pool_stats"},
	}

	for _, endpoint := range endpoints {
		t.Run(endpoint.path, func(t *testing.T) {
			s := newStack(t, stackOptions{})
			s.daemon.failHTTP(endpoint.key, http.StatusInternalServerError)

			resp := s.get(t, endpoint.path)

			requireError(
				t, resp, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE",
				messageNoMonero,
			)
			assert.NotContains(t, resp.text(), "unwell")
		})
	}
}

func TestSlowMonerodIsAGatewayTimeout(t *testing.T) {
	s := newStack(t, stackOptions{rpcTimeout: 100 * time.Millisecond})
	s.daemon.update(func(fake *fakeMonerod) { fake.delay = 2 * time.Second })

	resp := s.get(t, "/v1/node/info")

	requireError(
		t, resp, http.StatusGatewayTimeout, "GATEWAY_TIMEOUT",
		"monerod or the wallet did not answer in time",
	)
}

func TestGenerateBlocks(t *testing.T) {
	s := newStack(t, stackOptions{})

	hashes := []string{strings.Repeat("11", 32), strings.Repeat("22", 32)}

	s.daemon.update(func(fake *fakeMonerod) {
		fake.generated = daemonrpc.GeneratedBlocks{Height: 1002, Blocks: hashes}
	})

	resp := s.post(
		t, "/v1/node/regtest/generate-blocks",
		`{"count":2,"address":"4MinerAddress"}`,
	)
	requireStatus(t, resp, http.StatusOK)

	assert.Equal(t, api.GeneratedBlocks{
		Height: 1002, BlockHashes: hashes,
	}, decodeAs[api.GeneratedBlocks](t, resp))

	sent := s.daemon.paramsFor("generateblocks")
	require.Len(t, sent, 1)
	assert.JSONEq(
		t, `{"amount_of_blocks":2,"wallet_address":"4MinerAddress"}`,
		string(sent[0]),
	)
}

func TestGenerateBlocksAcceptsTheCountBoundaries(t *testing.T) {
	s := newStack(t, stackOptions{})

	for _, count := range []string{"1", "1000"} {
		t.Run(count, func(t *testing.T) {
			resp := s.post(
				t, "/v1/node/regtest/generate-blocks",
				`{"count":`+count+`,"address":"4Miner"}`,
			)

			requireStatus(t, resp, http.StatusOK)
			assert.Contains(t, resp.text(), `"blockHashes":[]`)
		})
	}
}

func TestGenerateBlocksRejectsBadRequests(t *testing.T) {
	s := newStack(t, stackOptions{})

	testCases := []struct {
		name        string
		body        string
		wantMessage string
	}{
		{"zero count", `{"count":0,"address":"4A"}`, messageCount},
		{"negative count", `{"count":-1,"address":"4A"}`, messageCount},
		{"count over the cap", `{"count":1001,"address":"4A"}`, messageCount},
		{"count missing", `{"address":"4A"}`, messageCount},
		{"null body", `null`, messageCount},
		{"address missing", `{"count":1}`, "address is required"},
		{"empty address", `{"count":1,"address":""}`, "address is required"},
		{"count is a string", `{"count":"1","address":"4A"}`, messageDecode},
		{
			"count overflows int64",
			`{"count":99999999999999999999,"address":"4A"}`,
			messageDecode,
		},
		{"truncated json", `{"count":1,`, messageDecode},
		{"not json", `count=1`, messageDecode},
		{"json array", `[1]`, messageDecode},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resp := s.post(t, "/v1/node/regtest/generate-blocks", tc.body)

			requireError(
				t, resp, http.StatusBadRequest, codeBadRequest, tc.wantMessage,
			)
		})
	}

	assert.Empty(t, s.daemon.paramsFor("generateblocks"))
}

func TestGenerateBlocksRejectsAnEmptyBody(t *testing.T) {
	s := newStack(t, stackOptions{})

	resp := s.post(t, "/v1/node/regtest/generate-blocks", "")

	requireError(
		t, resp, http.StatusBadRequest, codeBadRequest, messageDecode,
	)
}

func TestGenerateBlocksRefusals(t *testing.T) {
	testCases := []struct {
		name        string
		options     stackOptions
		wantStatus  int
		wantCode    string
		wantMessage string
	}{
		{
			name:        "not regtest",
			options:     stackOptions{network: config.NetworkMainnet},
			wantStatus:  http.StatusConflict,
			wantCode:    "NOT_REGTEST",
			wantMessage: "block generation is only available on regtest",
		},
		{
			name:        "read-only deployment",
			options:     stackOptions{readOnly: true},
			wantStatus:  http.StatusForbidden,
			wantCode:    "READ_ONLY",
			wantMessage: "this deployment is read-only",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			s := newStack(t, tc.options)

			resp := s.post(
				t, "/v1/node/regtest/generate-blocks",
				`{"count":1,"address":"4Miner"}`,
			)

			requireError(t, resp, tc.wantStatus, tc.wantCode, tc.wantMessage)
			assert.Empty(t, s.daemon.paramsFor("generateblocks"))
		})
	}
}

func TestGenerateBlocksWithAnAddressMonerodRejects(t *testing.T) {
	s := newStack(t, stackOptions{})
	s.daemon.fail(
		"generateblocks", daemonrpc.CodeWrongWalletAddress, "bad address",
	)

	resp := s.post(
		t, "/v1/node/regtest/generate-blocks", `{"count":1,"address":"nope"}`,
	)

	requireError(
		t, resp, http.StatusBadRequest, codeBadRequest,
		"the address is not valid",
	)
}

func TestGenerateBlocksWithMonerodDown(t *testing.T) {
	s := newStack(t, stackOptions{})
	s.daemon.failHTTP("generateblocks", http.StatusBadGateway)

	resp := s.post(
		t, "/v1/node/regtest/generate-blocks", `{"count":1,"address":"4Miner"}`,
	)

	requireError(
		t, resp, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE",
		messageNoMonero,
	)
}

func TestSuccessResponsesAreCompactJSON(t *testing.T) {
	s := newStack(t, stackOptions{})

	resp := s.get(t, "/v1/node/fee-estimate")
	requireStatus(t, resp, http.StatusOK)

	var generic map[string]json.RawMessage

	require.NoError(t, json.Unmarshal(resp.body, &generic))
	assert.ElementsMatch(
		t, []string{"feePerByte", "feesPerByte", "quantizationMask"},
		keysOf(generic),
	)
}

func keysOf(values map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}

	return keys
}

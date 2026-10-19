package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/psyb0t/docker-monero/internal/pkg/config"
	"github.com/psyb0t/docker-monero/internal/pkg/http/api"
	"github.com/psyb0t/docker-monero/internal/pkg/monero/daemonrpc"
	"github.com/psyb0t/docker-monero/internal/pkg/monero/walletrpc"
	"github.com/psyb0t/docker-monero/internal/pkg/monero/walletrpc/walletrpctest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var readOnlyTools = []string{
	"monero_node_info",
	"monero_get_block",
	"monero_get_transaction",
	"monero_fee_estimate",
	"monero_list_wallets",
	"monero_open_wallet",
	"monero_close_wallet",
	"monero_wallet_status",
	"monero_wallet_balance",
	"monero_wallet_addresses",
	"monero_wallet_transfers",
}

func withTools(extra ...string) []string {
	return append(append([]string{}, readOnlyTools...), extra...)
}

func TestToolsFollowTheDeployment(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		options Options
		want    []string
	}{
		{
			name:    "read-only registers only the readers",
			options: Options{ReadOnly: true},
			want:    readOnlyTools,
		},
		{
			name: "read-only wins over every opt-in",
			options: Options{
				ReadOnly: true, AllowTransfers: true, Regtest: true,
			},
			want: readOnlyTools,
		},
		{
			name:    "plain writable deployment",
			options: Options{},
			want: withTools(
				"monero_create_wallet", "monero_create_address",
			),
		},
		{
			name:    "regtest adds block generation",
			options: Options{Regtest: true},
			want: withTools(
				"monero_create_wallet", "monero_create_address",
				"monero_generate_blocks",
			),
		},
		{
			name:    "transfers are opt-in",
			options: Options{AllowTransfers: true},
			want: withTools(
				"monero_create_wallet", "monero_create_address",
				"monero_transfer",
			),
		},
		{
			name:    "everything",
			options: Options{AllowTransfers: true, Regtest: true},
			want: withTools(
				"monero_create_wallet", "monero_create_address",
				"monero_generate_blocks", "monero_transfer",
			),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t, fixtureOptions{})
			session := connect(t, f.newServer(t, tc.options))

			assert.ElementsMatch(t, tc.want, toolNames(t, session))
		})
	}
}

func TestToolNameConstantsAreThePublicSurface(t *testing.T) {
	t.Parallel()

	assert.ElementsMatch(t,
		withTools(
			"monero_create_wallet", "monero_create_address",
			"monero_generate_blocks", "monero_transfer",
		),
		[]string{
			ToolNodeInfo, ToolGetBlock, ToolGetTransaction, ToolFeeEstimate,
			ToolGenerateBlocks, ToolListWallets, ToolCreateWallet,
			ToolOpenWallet, ToolCloseWallet, ToolWalletStatus,
			ToolWalletBalance, ToolWalletAddresses, ToolCreateAddress,
			ToolWalletTransfers, ToolTransfer,
		},
	)
}

func TestEveryToolIsDocumentedAndTyped(t *testing.T) {
	t.Parallel()

	f := newFixture(t, fixtureOptions{})
	session := f.session(t)

	listed, err := session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, listed.Tools, 15)

	for _, tool := range listed.Tools {
		t.Run(tool.Name, func(t *testing.T) {
			t.Parallel()

			assert.NotEmpty(t, tool.Description)

			input := schemaMap(t, tool.InputSchema)
			assert.Equal(t, "object", input["type"])
			assert.Equal(t, false, input["additionalProperties"])

			output := schemaMap(t, tool.OutputSchema)
			assert.Equal(t, "object", output["type"])
			assert.NotEmpty(t, output["properties"])
		})
	}
}

func schemaMap(t *testing.T, schema any) map[string]any {
	t.Helper()

	require.NotNil(t, schema)

	encoded, err := json.Marshal(schema)
	require.NoError(t, err)

	var decoded map[string]any

	require.NoError(t, json.Unmarshal(encoded, &decoded))

	return decoded
}

func TestOutputSchemasDescribeTimestampsAsDateTimeStrings(t *testing.T) {
	t.Parallel()

	f := newFixture(t, fixtureOptions{})
	session := f.session(t)

	listed, err := session.ListTools(context.Background(), nil)
	require.NoError(t, err)

	timestamps := map[string]string{
		"monero_get_block":       "timestamp",
		"monero_node_info":       "startedAt",
		"monero_wallet_status":   "openedAt",
		"monero_get_transaction": "blockTimestamp",
	}

	for _, tool := range listed.Tools {
		field, ok := timestamps[tool.Name]
		if !ok {
			continue
		}

		properties, ok := schemaProperties(t, tool.OutputSchema)
		require.True(t, ok, tool.Name)

		property, ok := properties[field].(map[string]any)
		require.True(t, ok, "%s.%s", tool.Name, field)

		assert.Equal(t, "date-time", property["format"], tool.Name)
	}
}

func TestInputSchemasListTheirArguments(t *testing.T) {
	t.Parallel()

	f := newFixture(t, fixtureOptions{})
	session := f.session(t)

	listed, err := session.ListTools(context.Background(), nil)
	require.NoError(t, err)

	wantArguments := map[string][]string{
		"monero_node_info":        nil,
		"monero_fee_estimate":     nil,
		"monero_list_wallets":     nil,
		"monero_get_block":        {"hash", "height"},
		"monero_get_transaction":  {"txHash"},
		"monero_generate_blocks":  {"address", "count"},
		"monero_create_wallet":    {"language", "name", "password"},
		"monero_open_wallet":      {"name", "password"},
		"monero_close_wallet":     {"name"},
		"monero_wallet_status":    {"name"},
		"monero_wallet_balance":   {"accountIndex", "name"},
		"monero_wallet_addresses": {"accountIndex", "name"},
		"monero_create_address":   {"accountIndex", "label", "name"},
		"monero_wallet_transfers": {
			"accountIndex", "directions", "limit", "name", "offset",
		},
		"monero_transfer": {
			"accountIndex", "destinations", "dryRun", "name", "priority",
		},
	}

	for _, tool := range listed.Tools {
		properties, _ := schemaProperties(t, tool.InputSchema)

		got := make([]string, 0, len(properties))
		for name := range properties {
			got = append(got, name)
		}

		assert.ElementsMatch(t, wantArguments[tool.Name], got, tool.Name)
	}
}

func TestToolsRejectArgumentsThatBreakTheirSchema(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	session := f.session(t)

	testCases := []struct {
		name      string
		tool      string
		arguments map[string]any
	}{
		{
			"missing required name", "monero_open_wallet",
			map[string]any{"password": "x"},
		},
		{
			"wrong type", "monero_open_wallet",
			map[string]any{"name": 5, "password": "x"},
		},
		{"unknown argument", "monero_node_info", map[string]any{"extra": 1}},
		{"missing tx hash", "monero_get_transaction", map[string]any{}},
		{
			"fractional height", "monero_get_block",
			map[string]any{"height": 1.5},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := call(t, session, tc.tool, tc.arguments)

			assert.True(t, result.IsError)
			assert.Nil(t, result.StructuredContent)
		})
	}

	assert.Zero(t, f.pool.OpenCount())
	assert.Zero(t, f.wallets.Called("open_wallet"), "nothing reached a wallet")
}

func TestCallingAToolThatIsNotRegisteredIsAProtocolError(t *testing.T) {
	f := newFixture(t, fixtureOptions{readOnly: true})
	session := connect(t, f.newServer(t, Options{ReadOnly: true}))

	for _, name := range []string{
		"monero_create_wallet", "monero_transfer", "monero_generate_blocks",
		"monero_restore_wallet", "does_not_exist",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := session.CallTool(
				context.Background(),
				&mcpsdk.CallToolParams{
					Name: name, Arguments: map[string]any{},
				},
			)

			require.Error(t, err)
			assert.Contains(t, err.Error(), "unknown tool")
		})
	}

	assert.Zero(t, f.pool.OpenCount())
}

func TestNodeInfoTool(t *testing.T) {
	t.Parallel()

	f := newFixture(t, fixtureOptions{})
	session := f.session(t)

	info := callAs[api.NodeInfo](t, session, "monero_node_info", nil)

	require.NotNil(t, info.StartedAt)

	assert.Equal(t, api.Network("regtest"), info.Network)
	assert.EqualValues(t, nodeHeight, info.Height)
	assert.True(t, info.Synchronized)
	assert.InDelta(t, 100, info.SyncPercent, 0)
	assert.EqualValues(t, 4, info.Restarts)
	assert.EqualValues(t, 8, info.OutgoingConnections)
	assert.True(t, info.StartedAt.Equal(time.Unix(1700000000, 0)))
}

func TestNodeToolsReportMonerodFailures(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		tool      string
		arguments map[string]any
		key       string
	}{
		{"monero_node_info", nil, "get_info"},
		{"monero_get_block", nil, "get_last_block_header"},
		{
			"monero_get_block",
			map[string]any{"height": 3},
			"get_block_header_by_height",
		},
		{
			"monero_get_block",
			map[string]any{"hash": testHash},
			"get_block_header_by_hash",
		},
		{
			"monero_get_transaction",
			map[string]any{"txHash": testHash},
			"/get_transactions",
		},
		{"monero_fee_estimate", nil, "get_fee_estimate"},
		{
			"monero_generate_blocks",
			map[string]any{"count": 1, "address": "4A"},
			"generateblocks",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.tool+"/"+tc.key, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t, fixtureOptions{})
			f.daemon.failHTTP(tc.key, 500)

			result := call(t, f.session(t), tc.tool, tc.arguments)

			requireToolError(
				t, result, "SERVICE_UNAVAILABLE: monerod is not available yet",
			)
			assert.NotContains(t, text(t, result), "unwell")
		})
	}
}

func TestGetBlockTool(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	session := f.session(t)

	t.Run("no arguments means the tip", func(t *testing.T) {
		block := structured[api.BlockHeader](
			t, call(t, session, "monero_get_block", nil),
		)

		assert.EqualValues(t, 999, block.Height)
		assert.Equal(t, testHash, block.Hash)
		assert.Equal(t, "600000000000", block.Reward)
		assert.Equal(t, "0.600000000000", block.RewardXmr)
		assert.True(t, block.Timestamp.Equal(time.Unix(1700000500, 0)))
	})

	t.Run("by height", func(t *testing.T) {
		block := structured[api.BlockHeader](
			t, call(t, session, "monero_get_block", heightArg(7)),
		)

		assert.EqualValues(t, 7, block.Height)
	})

	t.Run("height zero is a height, not an omitted one", func(t *testing.T) {
		block := structured[api.BlockHeader](
			t, call(t, session, "monero_get_block", heightArg(0)),
		)

		assert.EqualValues(t, 0, block.Height)
	})

	t.Run("by hash, lower-cased for monerod", func(t *testing.T) {
		upper := strings.ToUpper(strings.Repeat("ab", 32))
		block := structured[api.BlockHeader](
			t, call(t, session, "monero_get_block", hashArg(upper)),
		)

		assert.Equal(t, testHash, block.Hash)
	})

	t.Run("height wins when both are given", func(t *testing.T) {
		before := len(f.daemon.paramsFor("get_block_header_by_hash"))
		block := structured[api.BlockHeader](t, call(
			t, session, "monero_get_block",
			map[string]any{"height": 12, "hash": testHash},
		))

		assert.EqualValues(t, 12, block.Height)
		assert.Len(t, f.daemon.paramsFor("get_block_header_by_hash"), before)
	})
}

func TestGetBlockToolErrors(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		arguments map[string]any
		setup     func(f *fixture)
		want      string
	}{
		{
			name:      "negative height",
			arguments: map[string]any{"height": -1},
			want:      "BAD_REQUEST: height -1 is negative",
		},
		{
			name:      "short hash",
			arguments: map[string]any{"hash": "abc"},
			want:      "BAD_REQUEST: hashes are 64 hexadecimal characters",
		},
		{
			name:      "non-hex hash",
			arguments: map[string]any{"hash": strings.Repeat("z", 64)},
			want:      "BAD_REQUEST: hashes are 64 hexadecimal characters",
		},
		{
			name:      "height above the tip",
			arguments: map[string]any{"height": 99999},
			setup: func(f *fixture) {
				f.daemon.fail(
					"get_block_header_by_height",
					daemonrpc.CodeTooBigHeight, "big",
				)
			},
			want: "NOT_FOUND: no such block or transaction",
		},
		{
			name:      "unknown hash",
			arguments: map[string]any{"hash": testHash},
			setup: func(f *fixture) {
				f.daemon.fail(
					"get_block_header_by_hash",
					daemonrpc.CodeInternalError, "x",
				)
			},
			want: "NOT_FOUND: no such block or transaction",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t, fixtureOptions{})
			if tc.setup != nil {
				tc.setup(f)
			}

			requireToolError(
				t, call(t, f.session(t), "monero_get_block", tc.arguments),
				tc.want,
			)
		})
	}
}

func TestGetTransactionTool(t *testing.T) {
	t.Parallel()

	f := newFixture(t, fixtureOptions{})
	f.daemon.update(func(fake *fakeMonerod) {
		fake.txs.Txs = []daemonrpc.Transaction{{
			TxHash:         testHash,
			BlockHeight:    990,
			Confirmations:  10,
			BlockTimestamp: 1700000400,
			Relayed:        true,
		}}
	})

	session := f.session(t)

	tx := structured[api.Transaction](t, call(
		t, session, "monero_get_transaction", txArg(testHash),
	))

	require.NotNil(t, tx.BlockHeight)
	assert.Equal(t, testHash, tx.TxHash)
	assert.EqualValues(t, 990, *tx.BlockHeight)
	assert.EqualValues(t, 10, tx.Confirmations)
	assert.False(t, tx.InPool)

	invalid := call(
		t, session, "monero_get_transaction", map[string]any{"txHash": "nope"},
	)
	requireToolError(
		t, invalid, "BAD_REQUEST: hashes are 64 hexadecimal characters",
	)

	f.daemon.update(func(fake *fakeMonerod) {
		fake.txs = daemonrpc.Transactions{MissedTx: []string{testHash}}
	})

	missing := call(
		t, session, "monero_get_transaction", txArg(testHash),
	)
	requireToolError(t, missing, "NOT_FOUND: no such block or transaction")
}

func TestFeeEstimateTool(t *testing.T) {
	t.Parallel()

	f := newFixture(t, fixtureOptions{})

	estimate := structured[api.FeeEstimate](
		t, call(t, f.session(t), "monero_fee_estimate", nil),
	)

	assert.Equal(t, "20000", estimate.FeePerByte)
	assert.EqualValues(t, 10000, estimate.QuantizationMask)
	assert.Equal(
		t, []string{"20000", "80000", "320000", "4000000"},
		estimate.FeesPerByte,
	)
}

func TestGenerateBlocksTool(t *testing.T) {
	t.Parallel()

	hashes := []string{strings.Repeat("11", 32), strings.Repeat("22", 32)}
	f := newFixture(t, fixtureOptions{})
	f.daemon.update(func(fake *fakeMonerod) {
		fake.generated = daemonrpc.GeneratedBlocks{Height: 1002, Blocks: hashes}
	})

	result := structured[api.GeneratedBlocks](t, call(
		t, f.session(t), "monero_generate_blocks",
		map[string]any{"count": 2, "address": "4Miner"},
	))

	assert.EqualValues(t, 1002, result.Height)
	assert.Equal(t, hashes, result.BlockHashes)

	sent := f.daemon.paramsFor("generateblocks")
	require.Len(t, sent, 1)
	assert.JSONEq(
		t, `{"amount_of_blocks":2,"wallet_address":"4Miner"}`, string(sent[0]),
	)
}

func TestGenerateBlocksToolErrors(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		options   fixtureOptions
		arguments map[string]any
		setup     func(f *fixture)
		want      string
	}{
		{
			name:      "count too small",
			arguments: map[string]any{"count": 0, "address": "4A"},
			want:      "BAD_REQUEST: count is out of range",
		},
		{
			name:      "count too large",
			arguments: map[string]any{"count": 1001, "address": "4A"},
			want:      "BAD_REQUEST: count is out of range",
		},
		{
			name:      "no address",
			arguments: map[string]any{"count": 1, "address": ""},
			want:      "BAD_REQUEST: address is required",
		},
		{
			name:      "address monerod refuses",
			arguments: map[string]any{"count": 1, "address": "bad"},
			setup: func(f *fixture) {
				f.daemon.fail(
					"generateblocks", daemonrpc.CodeWrongWalletAddress, "bad",
				)
			},
			want: "BAD_REQUEST: the address is not valid",
		},
		{
			name:      "tool registered but the node is not regtest",
			options:   fixtureOptions{network: config.NetworkMainnet},
			arguments: map[string]any{"count": 1, "address": "4A"},
			want: "NOT_REGTEST: block generation is only available " +
				"on regtest",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t, tc.options)
			if tc.setup != nil {
				tc.setup(f)
			}

			result := call(
				t, f.session(t), "monero_generate_blocks", tc.arguments,
			)

			requireToolError(t, result, tc.want)
		})
	}
}

func TestWalletLifecycleTools(t *testing.T) {
	t.Parallel()

	f := newFixture(t, fixtureOptions{})
	f.wallets.Set(func(state *walletrpctest.State) {
		state.Height = nodeHeight
	})

	session := f.session(t)

	created := structured[api.WalletStatus](t, call(
		t, session, "monero_create_wallet",
		map[string]any{
			"name": "alice", "password": "pw", "language": "English",
		},
	))
	require.NotNil(t, created.Address)

	assert.Equal(t, "alice", created.Name)
	assert.True(t, created.Open)
	assert.Equal(t, walletrpctest.FakeAddress, *created.Address)

	listed := structured[api.WalletList](
		t, call(t, session, "monero_list_wallets", nil),
	)
	assert.Equal(t, api.WalletList{
		MaxOpen:   maxOpenWallets,
		OpenCount: 1,
		Wallets:   []api.WalletSummary{{Name: "alice", Open: true}},
	}, listed)

	status := structured[api.WalletStatus](t, call(
		t, session, "monero_wallet_status", map[string]any{"name": "alice"},
	))
	require.NotNil(t, status.Syncing)
	assert.True(t, status.Open)
	assert.False(t, *status.Syncing)

	closed := structured[api.WalletStatus](t, call(
		t, session, "monero_close_wallet", map[string]any{"name": "alice"},
	))
	assert.Equal(t, api.WalletStatus{Name: "alice"}, closed)

	wrong := call(t, session, "monero_open_wallet",
		map[string]any{"name": "alice", "password": "nope"})
	requireToolError(
		t, wrong, "INVALID_WALLET_PASSWORD: the wallet password is wrong",
	)

	reopened := structured[api.WalletStatus](t, call(
		t, session, "monero_open_wallet",
		map[string]any{"name": "alice", "password": "pw"},
	))
	assert.True(t, reopened.Open)
	assert.Equal(t, 1, f.pool.OpenCount())
}

func TestWalletToolErrors(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	session := f.session(t)

	_, err := f.pool.Create(context.Background(), "alice", "pw", "")
	require.NoError(t, err)

	testCases := []struct {
		name      string
		tool      string
		arguments map[string]any
		want      string
	}{
		{
			name:      "create a duplicate",
			tool:      "monero_create_wallet",
			arguments: map[string]any{"name": "alice", "password": "pw"},
			want:      "WALLET_EXISTS: a wallet with that name already exists",
		},
		{
			name:      "open an open wallet",
			tool:      "monero_open_wallet",
			arguments: map[string]any{"name": "alice", "password": "pw"},
			want:      "WALLET_ALREADY_OPEN: the wallet is already open",
		},
		{
			name:      "open a wallet that does not exist",
			tool:      "monero_open_wallet",
			arguments: map[string]any{"name": "ghost", "password": "pw"},
			want:      "NOT_FOUND: no such wallet",
		},
		{
			name:      "close a wallet that is not open",
			tool:      "monero_close_wallet",
			arguments: map[string]any{"name": "ghost"},
			want:      "WALLET_NOT_OPEN: the wallet is not open",
		},
		{
			name:      "status of a wallet that does not exist",
			tool:      "monero_wallet_status",
			arguments: map[string]any{"name": "ghost"},
			want:      "NOT_FOUND: no such wallet",
		},
		{
			name:      "balance of a wallet that is not open",
			tool:      "monero_wallet_balance",
			arguments: map[string]any{"name": "ghost"},
			want:      "WALLET_NOT_OPEN: the wallet is not open",
		},
		{
			name:      "addresses of a wallet that is not open",
			tool:      "monero_wallet_addresses",
			arguments: map[string]any{"name": "ghost"},
			want:      "WALLET_NOT_OPEN: the wallet is not open",
		},
		{
			name:      "create an address in a wallet that is not open",
			tool:      "monero_create_address",
			arguments: map[string]any{"name": "ghost"},
			want:      "WALLET_NOT_OPEN: the wallet is not open",
		},
		{
			name:      "transfers of a wallet that is not open",
			tool:      "monero_wallet_transfers",
			arguments: map[string]any{"name": "ghost"},
			want:      "WALLET_NOT_OPEN: the wallet is not open",
		},
		{
			name:      "negative account",
			tool:      "monero_wallet_balance",
			arguments: map[string]any{"name": "alice", "accountIndex": -1},
			want: "BAD_REQUEST: the account or subaddress index is " +
				"out of range",
		},
		{
			name: "label too long",
			tool: "monero_create_address",
			arguments: map[string]any{
				"name": "alice", "label": strings.Repeat("l", 129),
			},
			want: "BAD_REQUEST: label is longer than 128 characters",
		},
		{
			name: "password too long",
			tool: "monero_create_wallet",
			arguments: map[string]any{
				"name": "bob", "password": strings.Repeat("p", 257),
			},
			want: "BAD_REQUEST: password is longer than 256 characters",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := call(t, session, tc.tool, tc.arguments)

			requireToolError(t, result, tc.want)
		})
	}

	t.Run("invalid wallet name", func(t *testing.T) {
		result := call(t, session, "monero_create_wallet",
			map[string]any{"name": "../escape", "password": "pw"})

		require.True(t, result.IsError)
		assert.True(t, strings.HasPrefix(
			text(t, result), "BAD_REQUEST: wallet names use letters, digits",
		), text(t, result))
	})

	assert.Equal(t, 1, f.pool.OpenCount(), "no failed call left a wallet open")
}

func TestWalletBalanceAndAddressTools(t *testing.T) {
	t.Parallel()

	f := newFixture(t, fixtureOptions{})
	session := f.session(t)

	_, err := f.pool.Create(context.Background(), "alice", "pw", "")
	require.NoError(t, err)

	f.wallets.Set(func(state *walletrpctest.State) {
		state.Balance = 1500000000000
		state.UnlockedBalance = 1000000000000
	})

	balance := structured[api.Balance](t, call(
		t, session, "monero_wallet_balance",
		map[string]any{"name": "alice", "accountIndex": 2},
	))

	assert.EqualValues(t, 2, balance.AccountIndex)
	assert.Equal(t, "1500000000000", balance.Balance)
	assert.Equal(t, "1.500000000000", balance.BalanceXmr)
	assert.Equal(t, "1000000000000", balance.UnlockedBalance)
	assert.Equal(t, "1.000000000000", balance.UnlockedBalanceXmr)

	defaultAccount := structured[api.Balance](t, call(
		t, session, "monero_wallet_balance", map[string]any{"name": "alice"},
	))
	assert.EqualValues(t, 0, defaultAccount.AccountIndex)

	created := structured[api.Address](t, call(
		t, session, "monero_create_address",
		map[string]any{"name": "alice", "label": "shop"},
	))
	assert.Equal(t, api.Address{
		Address: "8FakeSubaddress1", AddressIndex: 1, Label: "shop",
	}, created)

	addresses := structured[api.AddressList](t, call(
		t, session, "monero_wallet_addresses", map[string]any{"name": "alice"},
	))
	assert.Equal(t, walletrpctest.FakeAddress, addresses.PrimaryAddress)
	assert.Equal(t, []api.Address{
		{Address: walletrpctest.FakeAddress, Label: "Primary account"},
		{Address: "8FakeSubaddress1", AddressIndex: 1, Label: "shop"},
	}, addresses.Addresses)
}

func TestWalletToolsMapWalletRPCFailures(t *testing.T) {
	t.Parallel()

	f := newFixture(t, fixtureOptions{})
	session := f.session(t)

	_, err := f.pool.Create(context.Background(), "alice", "pw", "")
	require.NoError(t, err)

	f.wallets.Fail("get_balance", walletrpc.CodeNoDaemonConnection, "no daemon")
	requireToolError(
		t,
		call(t, session, "monero_wallet_balance", aliceArg()),
		"SERVICE_UNAVAILABLE: monerod is not available yet",
	)

	f.wallets.Fail("get_balance", -99, "secret wallet detail")

	result := call(
		t, session, "monero_wallet_balance", map[string]any{"name": "alice"},
	)
	requireToolError(
		t, result, "INTERNAL_SERVER_ERROR: the request could not be completed",
	)
	assert.NotContains(t, text(t, result), "secret")
}

func seedTransfers(state *walletrpctest.State, count uint64) {
	seeded := make([]walletrpc.TransferEntry, 0, count)

	for i := range count {
		seeded = append(seeded, walletrpc.TransferEntry{
			TxID:      fmt.Sprintf("tx%02d", i),
			Height:    100 + i,
			Timestamp: 1700000000 + i*10,
			Amount:    i + 1,
			Type:      "in",
		})
	}

	state.Transfers = walletrpc.Transfers{In: seeded}
}

func TestWalletTransfersToolPaginatesNewestFirst(t *testing.T) {
	t.Parallel()

	f := newFixture(t, fixtureOptions{})
	session := f.session(t)

	_, err := f.pool.Create(context.Background(), "alice", "pw", "")
	require.NoError(t, err)

	f.wallets.Set(func(state *walletrpctest.State) { seedTransfers(state, 13) })

	page := func(arguments map[string]any) api.TransferList {
		arguments["name"] = "alice"

		return structured[api.TransferList](
			t, call(t, session, "monero_wallet_transfers", arguments),
		)
	}

	hashes := func(list api.TransferList) []string {
		result := make([]string, 0, len(list.Transfers))
		for _, entry := range list.Transfers {
			result = append(result, entry.TxHash)
		}

		return result
	}

	first := page(map[string]any{"limit": 5})
	second := page(map[string]any{"limit": 5, "offset": 5})
	last := page(map[string]any{"limit": 5, "offset": 10})
	beyond := page(map[string]any{"limit": 5, "offset": 100000})

	assert.Equal(
		t, []string{"tx12", "tx11", "tx10", "tx09", "tx08"}, hashes(first),
	)
	assert.Equal(
		t, []string{"tx07", "tx06", "tx05", "tx04", "tx03"}, hashes(second),
	)
	assert.Equal(t, []string{"tx02", "tx01", "tx00"}, hashes(last))
	assert.Empty(t, hashes(beyond))

	for _, list := range []api.TransferList{first, second, last, beyond} {
		assert.Equal(t, 13, list.Total)
	}

	assert.Equal(t, 100, page(map[string]any{}).Limit)
}

func TestWalletTransfersToolErrors(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	session := f.session(t)

	_, err := f.pool.Create(context.Background(), "alice", "pw", "")
	require.NoError(t, err)

	testCases := []struct {
		name      string
		arguments map[string]any
		want      string
	}{
		{
			name:      "zero limit",
			arguments: map[string]any{"limit": 0},
			want:      "BAD_REQUEST: limit must be 1-1000",
		},
		{
			name:      "limit past the cap",
			arguments: map[string]any{"limit": 1001},
			want:      "BAD_REQUEST: limit must be 1-1000",
		},
		{
			name:      "negative offset",
			arguments: map[string]any{"offset": -1},
			want:      "BAD_REQUEST: offset must not be negative",
		},
		{
			name:      "unknown direction",
			arguments: map[string]any{"directions": []string{"sideways"}},
			want:      `BAD_REQUEST: unknown direction "sideways"`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tc.arguments["name"] = "alice"

			requireToolError(
				t, call(t, session, "monero_wallet_transfers", tc.arguments),
				tc.want,
			)
		})
	}

	t.Run("known directions are accepted", func(t *testing.T) {
		result := call(t, session, "monero_wallet_transfers", map[string]any{
			"name": "alice", "directions": []string{"in", "out", "pool"},
		})

		assert.False(t, result.IsError, "%v", result.Content)
	})
}

func (f *fixture) fundAlice(t *testing.T) {
	t.Helper()

	_, err := f.pool.Create(context.Background(), "alice", "pw", "")
	require.NoError(t, err)

	f.wallets.Set(func(state *walletrpctest.State) {
		state.Height = nodeHeight
		state.Balance = 5000000000000
		state.UnlockedBalance = 5000000000000
	})
}

func TestTransferTool(t *testing.T) {
	t.Parallel()

	f := newFixture(t, fixtureOptions{})
	f.fundAlice(t)

	session := f.session(t)

	result := structured[api.TransferResult](t, call(
		t, session, "monero_transfer", map[string]any{
			"name": "alice",
			"destinations": []map[string]any{
				{"address": "4First", "amount": "1000000000000"},
				{"address": "4Second", "amount": "500000000000"},
			},
			"accountIndex": 0,
			"priority":     "highest",
		},
	))

	assert.Equal(t, api.TransferResult{
		TxHash:    walletrpctest.FakeTxHash,
		TxKey:     "fakekey",
		Amount:    "1500000000000",
		AmountXmr: "1.500000000000",
		Fee:       "30000000",
		FeeXmr:    "0.000030000000",
		Weight:    1500,
		Relayed:   true,
	}, result)

	sent := f.lastTransfer()

	assert.InDelta(t, 4, sent["priority"], 0)
	assert.Equal(t, false, sent["do_not_relay"])

	dry := structured[api.TransferResult](t, call(
		t, session, "monero_transfer", map[string]any{
			"name": "alice",
			"destinations": []map[string]any{
				{"address": "4A", "amount": "1000"},
			},
			"dryRun": true,
		},
	))
	assert.False(t, dry.Relayed)

	sent = f.lastTransfer()

	assert.Equal(t, true, sent["do_not_relay"])
}

func TestTransferToolRefusals(t *testing.T) {
	t.Parallel()

	oneDestination := []map[string]any{
		{"address": "4A", "amount": "1000000000"},
	}

	testCases := []struct {
		name         string
		options      fixtureOptions
		destinations any
		priority     string
		setup        func(f *fixture)
		want         string
	}{
		{
			name:         "transfers disabled in the service",
			options:      fixtureOptions{disableTransfers: true},
			destinations: oneDestination,
			want: "TRANSFERS_DISABLED: this deployment does not " +
				"allow transfers",
		},
		{
			name:         "node not synchronized",
			destinations: oneDestination,
			setup: func(f *fixture) {
				f.daemon.update(func(fake *fakeMonerod) {
					fake.info.Synchronized = false
				})
			},
			want: "NODE_NOT_SYNCED: the node is not synchronized yet",
		},
		{
			name:         "wallet behind the node",
			destinations: oneDestination,
			setup: func(f *fixture) {
				f.wallets.Set(func(state *walletrpctest.State) {
					state.Height = nodeHeight - 1
				})
			},
			want: "WALLET_SYNCING: the wallet has not caught up with " +
				"the node yet",
		},
		{
			name:         "not enough unlocked funds",
			destinations: oneDestination,
			setup: func(f *fixture) {
				f.wallets.Set(func(state *walletrpctest.State) {
					state.UnlockedBalance = 1
				})
			},
			want: "INSUFFICIENT_FUNDS: not enough unlocked funds for the " +
				"amount plus fee",
		},
		{
			name:         "view-only wallet",
			destinations: oneDestination,
			setup: func(f *fixture) {
				f.wallets.Fail(
					"transfer", walletrpc.CodeWatchOnly, "watch only",
				)
			},
			want: "VIEW_ONLY_WALLET: a view-only wallet cannot send",
		},
		{
			name:         "no destinations",
			destinations: []map[string]any{},
			want:         "BAD_REQUEST: destinations must hold 1-16 entries",
		},
		{
			name: "zero amount",
			destinations: []map[string]any{
				{"address": "4A", "amount": "0"},
			},
			want: `BAD_REQUEST: amount "0" must be a positive integer of ` +
				`atomic units`,
		},
		{
			name:         "unknown priority",
			destinations: oneDestination,
			priority:     "turbo",
			want:         `BAD_REQUEST: unknown priority "turbo"`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t, tc.options)
			f.fundAlice(t)

			if tc.setup != nil {
				tc.setup(f)
			}

			arguments := map[string]any{
				"name": "alice", "destinations": tc.destinations,
			}
			if tc.priority != "" {
				arguments["priority"] = tc.priority
			}

			result := call(t, f.session(t), "monero_transfer", arguments)

			requireToolError(t, result, tc.want)
		})
	}
}

func schemaProperties(t *testing.T, schema any) (map[string]any, bool) {
	t.Helper()

	properties, ok := schemaMap(t, schema)["properties"].(map[string]any)

	return properties, ok
}

func heightArg(height int) map[string]any {
	return map[string]any{"height": height}
}

func hashArg(hash string) map[string]any {
	return map[string]any{"hash": hash}
}

func txArg(hash string) map[string]any {
	return map[string]any{"txHash": hash}
}

func aliceArg() map[string]any {
	return map[string]any{"name": "alice"}
}

// lastTransfer is the parameter set of the last transfer the fake
// wallet-rpc received.
func (f *fixture) lastTransfer() map[string]any {
	var sent map[string]any

	f.wallets.Set(func(state *walletrpctest.State) {
		sent = state.LastTransfer
	})

	return sent
}

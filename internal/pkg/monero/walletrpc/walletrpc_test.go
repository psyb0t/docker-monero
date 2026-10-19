package walletrpc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/psyb0t/ctxerrors"
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

// anyMethod is the replies key that answers every method without its own
// entry.
const anyMethod = "*"

// fakeWalletRPC answers /json_rpc with a canned reply per method and records
// every request it receives.
type fakeWalletRPC struct {
	t       *testing.T
	replies map[string]string

	mu    sync.Mutex
	calls []recordedCall
}

func newFakeWalletRPC(
	t *testing.T,
	replies map[string]string,
) (*Client, *fakeWalletRPC) {
	t.Helper()

	fake := &fakeWalletRPC{t: t, replies: replies}
	server := httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(server.Close)

	return New(server.URL, testTimeout), fake
}

func (f *fakeWalletRPC) serve(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	assert.NoError(f.t, err)

	var envelope struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      string          `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}

	assert.NoError(f.t, json.Unmarshal(body, &envelope))
	assert.Equal(f.t, "2.0", envelope.JSONRPC)
	assert.Equal(f.t, "0", envelope.ID)

	f.mu.Lock()
	f.calls = append(f.calls, recordedCall{
		httpMethod:  r.Method,
		path:        r.URL.Path,
		contentType: r.Header.Get("Content-Type"),
		rpcMethod:   envelope.Method,
		params:      envelope.Params,
	})
	f.mu.Unlock()

	reply, ok := f.replies[envelope.Method]
	if !ok {
		reply, ok = f.replies[anyMethod]
	}

	if !ok {
		reply = rpcFailure(-32601, "Method not found")
	}

	_, err = io.WriteString(w, reply)
	assert.NoError(f.t, err)
}

// only returns the single request the fake saw.
func (f *fakeWalletRPC) only(t *testing.T) recordedCall {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()

	require.Len(t, f.calls, 1)

	return f.calls[0]
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

// singleReply is a fake that answers one method with reply.
func singleReply(
	t *testing.T,
	method, reply string,
) (*Client, *fakeWalletRPC) {
	t.Helper()

	return newFakeWalletRPC(t, map[string]string{method: reply})
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

func TestPing(t *testing.T) {
	t.Parallel()

	client, fake := singleReply(
		t, "get_version", rpcResult(`{"version":65562,"release":true}`),
	)

	require.NoError(t, client.Ping(context.Background()))
	requireRPCCall(t, fake.only(t), "get_version", "")
}

func TestWalletLifecycleRequests(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		call       func(*Client) error
		wantMethod string
		wantParams string
	}{
		{
			name: "create with a language",
			call: func(c *Client) error {
				return c.CreateWallet(
					context.Background(), "alice", "pw", "Deutsch",
				)
			},
			wantMethod: "create_wallet",
			wantParams: `{
				"filename": "alice",
				"password": "pw",
				"language": "Deutsch"
			}`,
		},
		{
			name: "create falls back to English",
			call: func(c *Client) error {
				return c.CreateWallet(context.Background(), "alice", "pw", "")
			},
			wantMethod: "create_wallet",
			wantParams: `{
				"filename": "alice",
				"password": "pw",
				"language": "English"
			}`,
		},
		{
			name: "open saves the wallet it replaces",
			call: func(c *Client) error {
				return c.OpenWallet(context.Background(), "alice", "pw")
			},
			wantMethod: "open_wallet",
			wantParams: `{
				"filename": "alice",
				"password": "pw",
				"autosave_current": true
			}`,
		},
		{
			name: "close saves",
			call: func(c *Client) error {
				return c.CloseWallet(context.Background())
			},
			wantMethod: "close_wallet",
			wantParams: `{"autosave_current": true}`,
		},
		{
			name: "store",
			call: func(c *Client) error {
				return c.Store(context.Background())
			},
			wantMethod: "store",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			client, fake := singleReply(t, tc.wantMethod, rpcResult(`{}`))

			require.NoError(t, tc.call(client))
			requireRPCCall(t, fake.only(t), tc.wantMethod, tc.wantParams)
		})
	}
}

func TestRestoreFromSeed(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		language   string
		wantParams string
	}{
		{
			name:     "explicit language",
			language: "Spanish",
			wantParams: `{
				"filename": "restored",
				"password": "pw",
				"seed": "alpha bravo charlie",
				"seed_offset": "offset",
				"restore_height": 1234,
				"language": "Spanish",
				"autosave_current": true
			}`,
		},
		{
			name: "default language",
			wantParams: `{
				"filename": "restored",
				"password": "pw",
				"seed": "alpha bravo charlie",
				"seed_offset": "offset",
				"restore_height": 1234,
				"language": "English",
				"autosave_current": true
			}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			client, fake := singleReply(
				t, "restore_deterministic_wallet",
				rpcResult(`{"address":"4Restored","info":"ignored"}`),
			)

			address, err := client.RestoreFromSeed(
				context.Background(),
				RestoreFromSeedRequest{
					Filename:      "restored",
					Password:      "pw",
					Seed:          "alpha bravo charlie",
					SeedOffset:    "offset",
					RestoreHeight: 1234,
					Language:      tc.language,
				},
			)
			require.NoError(t, err)

			assert.Equal(t, "4Restored", address)
			requireRPCCall(
				t, fake.only(t), "restore_deterministic_wallet", tc.wantParams,
			)
		})
	}
}

func TestGenerateFromKeys(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		spendKey   string
		wantParams string
	}{
		{
			name:     "full wallet",
			spendKey: "spend",
			wantParams: `{
				"filename": "keys",
				"password": "pw",
				"address": "4Address",
				"viewkey": "view",
				"spendkey": "spend",
				"restore_height": 99,
				"language": "English",
				"autosave_current": true
			}`,
		},
		{
			name: "empty spend key requests a view-only wallet",
			wantParams: `{
				"filename": "keys",
				"password": "pw",
				"address": "4Address",
				"viewkey": "view",
				"spendkey": "",
				"restore_height": 99,
				"language": "English",
				"autosave_current": true
			}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			client, fake := singleReply(
				t, "generate_from_keys", rpcResult(`{"address":"4Address"}`),
			)

			address, err := client.GenerateFromKeys(
				context.Background(),
				GenerateFromKeysRequest{
					Filename:      "keys",
					Password:      "pw",
					Address:       "4Address",
					ViewKey:       "view",
					SpendKey:      tc.spendKey,
					RestoreHeight: 99,
				},
			)
			require.NoError(t, err)

			assert.Equal(t, "4Address", address)
			requireRPCCall(
				t, fake.only(t), "generate_from_keys", tc.wantParams,
			)
		})
	}
}

func TestGetHeight(t *testing.T) {
	t.Parallel()

	client, fake := singleReply(
		t, "get_height", rpcResult(`{"height":3141592}`),
	)

	height, err := client.GetHeight(context.Background())
	require.NoError(t, err)

	assert.Equal(t, uint64(3141592), height)
	requireRPCCall(t, fake.only(t), "get_height", "")
}

func TestGetBalance(t *testing.T) {
	t.Parallel()

	client, fake := singleReply(t, "get_balance", rpcResult(`{
		"balance": 1500000000000,
		"unlocked_balance": 1000000000000,
		"blocks_to_unlock": 7,
		"multisig_import_needed": false,
		"per_subaddress": [
			{
				"account_index": 2,
				"address_index": 0,
				"address": "4Primary",
				"balance": 1000000000000,
				"unlocked_balance": 1000000000000,
				"label": "Primary account",
				"num_unspent_outputs": 3,
				"blocks_to_unlock": 0
			},
			{
				"account_index": 2,
				"address_index": 1,
				"address": "8Sub",
				"balance": 500000000000,
				"unlocked_balance": 0,
				"label": "",
				"num_unspent_outputs": 1,
				"blocks_to_unlock": 7
			}
		]
	}`))

	balance, err := client.GetBalance(context.Background(), 2)
	require.NoError(t, err)

	assert.Equal(t, Balance{
		Balance:         1500000000000,
		UnlockedBalance: 1000000000000,
		BlocksToUnlock:  7,
		PerSubaddress: []SubaddressBalance{
			{
				AccountIndex:      2,
				AddressIndex:      0,
				Address:           "4Primary",
				Balance:           1000000000000,
				UnlockedBalance:   1000000000000,
				Label:             "Primary account",
				NumUnspentOutputs: 3,
			},
			{
				AccountIndex:      2,
				AddressIndex:      1,
				Address:           "8Sub",
				Balance:           500000000000,
				NumUnspentOutputs: 1,
				BlocksToUnlock:    7,
			},
		},
	}, balance)
	requireRPCCall(t, fake.only(t), "get_balance", `{"account_index":2}`)
}

func TestGetAddresses(t *testing.T) {
	t.Parallel()

	client, fake := singleReply(t, "get_address", rpcResult(`{
		"address": "4Primary",
		"addresses": [
			{
				"address": "4Primary",
				"label": "Primary account",
				"address_index": 0,
				"used": true
			},
			{
				"address": "8Sub",
				"label": "shop",
				"address_index": 1,
				"used": false
			}
		]
	}`))

	addresses, err := client.GetAddresses(context.Background(), 4)
	require.NoError(t, err)

	assert.Equal(t, Addresses{
		Address: "4Primary",
		Addresses: []AddressInfo{
			{
				Address:      "4Primary",
				Label:        "Primary account",
				AddressIndex: 0,
				Used:         true,
			},
			{Address: "8Sub", Label: "shop", AddressIndex: 1},
		},
	}, addresses)
	requireRPCCall(t, fake.only(t), "get_address", `{"account_index":4}`)
}

func TestCreateAddress(t *testing.T) {
	t.Parallel()

	client, fake := singleReply(t, "create_address", rpcResult(
		`{"address":"8New","address_index":5,"address_indices":[5]}`,
	))

	created, err := client.CreateAddress(context.Background(), 1, "invoices")
	require.NoError(t, err)

	assert.Equal(t, CreatedAddress{Address: "8New", AddressIndex: 5}, created)
	requireRPCCall(
		t, fake.only(t), "create_address",
		`{"account_index":1,"label":"invoices"}`,
	)
}

func TestGetTransfers(t *testing.T) {
	t.Parallel()

	client, fake := singleReply(t, "get_transfers", rpcResult(`{
		"in": [
			{
				"txid": "in1",
				"payment_id": "0000000000000000",
				"height": 100,
				"timestamp": 1700000000,
				"amount": 5,
				"fee": 1,
				"note": "salary",
				"destinations": [],
				"type": "in",
				"unlock_time": 0,
				"locked": true,
				"subaddr_index": {"major": 0, "minor": 2},
				"address": "8Sub",
				"double_spend_seen": true,
				"confirmations": 12
			}
		],
		"out": [
			{
				"txid": "out1",
				"amount": 9,
				"fee": 2,
				"destinations": [{"amount": 9, "address": "4Dest"}],
				"type": "out"
			}
		],
		"pool": [{"txid": "pool1", "type": "pool"}]
	}`))

	transfers, err := client.GetTransfers(
		context.Background(),
		TransferFilter{In: true, Out: true, Pool: true, AccountIndex: 3},
	)
	require.NoError(t, err)

	assert.Equal(t, Transfers{
		In: []TransferEntry{{
			TxID:            "in1",
			PaymentID:       "0000000000000000",
			Height:          100,
			Timestamp:       1700000000,
			Amount:          5,
			Fee:             1,
			Note:            "salary",
			Destinations:    []Destination{},
			Type:            "in",
			Locked:          true,
			SubaddrIndex:    SubaddressIndex{Major: 0, Minor: 2},
			Address:         "8Sub",
			DoubleSpendSeen: true,
			Confirmations:   12,
		}},
		Out: []TransferEntry{{
			TxID:         "out1",
			Amount:       9,
			Fee:          2,
			Destinations: []Destination{{Amount: 9, Address: "4Dest"}},
			Type:         "out",
		}},
		Pool: []TransferEntry{{TxID: "pool1", Type: "pool"}},
	}, transfers)
	assert.Nil(t, transfers.Pending)
	assert.Nil(t, transfers.Failed)

	requireRPCCall(t, fake.only(t), "get_transfers", `{
		"in": true,
		"out": true,
		"pending": false,
		"failed": false,
		"pool": true,
		"account_index": 3
	}`)
}

func TestTransfer(t *testing.T) {
	t.Parallel()

	client, fake := singleReply(t, "transfer", rpcResult(`{
		"tx_hash": "hash",
		"tx_key": "key",
		"amount": 1000,
		"fee": 30,
		"weight": 1500,
		"unsigned_txset": ""
	}`))

	result, err := client.Transfer(context.Background(), TransferRequest{
		Destinations: []Destination{
			{Amount: 600, Address: "4A"},
			{Amount: 400, Address: "4B"},
		},
		AccountIndex:   2,
		SubaddrIndices: []uint32{0, 3},
		Priority:       PriorityElevated,
		DoNotRelay:     true,
	})
	require.NoError(t, err)

	assert.Equal(t, TransferResult{
		TxHash: "hash",
		TxKey:  "key",
		Amount: 1000,
		Fee:    30,
		Weight: 1500,
	}, result)
	requireRPCCall(t, fake.only(t), "transfer", `{
		"destinations": [
			{"amount": 600, "address": "4A"},
			{"amount": 400, "address": "4B"}
		],
		"account_index": 2,
		"subaddr_indices": [0, 3],
		"priority": 3,
		"get_tx_key": true,
		"do_not_relay": true
	}`)
}

func TestTransferAlwaysAsksForTheTxKey(t *testing.T) {
	t.Parallel()

	client, fake := singleReply(t, "transfer", rpcResult(`{"tx_hash":"h"}`))

	_, err := client.Transfer(context.Background(), TransferRequest{
		Destinations: []Destination{{Amount: 1, Address: "4A"}},
	})
	require.NoError(t, err)

	var sent map[string]any

	require.NoError(t, json.Unmarshal(fake.only(t).params, &sent))
	assert.Equal(t, true, sent["get_tx_key"])
	assert.Equal(t, false, sent["do_not_relay"])
	assert.InDelta(t, 0, sent["priority"], 0)
}

func TestSetAutoRefreshClampsThePeriodToWholeSeconds(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		period     time.Duration
		wantPeriod uint32
	}{
		{name: "ten seconds", period: 10 * time.Second, wantPeriod: 10},
		{name: "zero", period: 0, wantPeriod: 0},
		{name: "negative", period: -5 * time.Second, wantPeriod: 0},
		{
			name:       "fraction rounds down",
			period:     1900 * time.Millisecond,
			wantPeriod: 1,
		},
		{
			name:       "below one second",
			period:     999 * time.Millisecond,
			wantPeriod: 0,
		},
		{
			name:       "exactly the uint32 limit",
			period:     math.MaxUint32 * time.Second,
			wantPeriod: math.MaxUint32,
		},
		{
			name:       "past the uint32 limit",
			period:     (math.MaxUint32 + 1) * time.Second,
			wantPeriod: math.MaxUint32,
		},
		{
			name:       "largest duration",
			period:     time.Duration(math.MaxInt64),
			wantPeriod: math.MaxUint32,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			client, fake := singleReply(t, "auto_refresh", rpcResult(`{}`))

			err := client.SetAutoRefresh(context.Background(), tc.period)
			require.NoError(t, err)
			requireRPCCall(
				t, fake.only(t), "auto_refresh",
				fmt.Sprintf(`{"enable":true,"period":%d}`, tc.wantPeriod),
			)
		})
	}
}

type namedCall struct {
	name    string
	context string
	call    func(*Client) error
}

var everyCall = []namedCall{
	{"Ping", "ping wallet rpc", func(c *Client) error {
		return c.Ping(context.Background())
	}},
	{"CreateWallet", "create wallet", func(c *Client) error {
		return c.CreateWallet(context.Background(), "w", "p", "")
	}},
	{"OpenWallet", "open wallet", func(c *Client) error {
		return c.OpenWallet(context.Background(), "w", "p")
	}},
	{"CloseWallet", "close wallet", func(c *Client) error {
		return c.CloseWallet(context.Background())
	}},
	{"Store", "store wallet", func(c *Client) error {
		return c.Store(context.Background())
	}},
	{
		"RestoreFromSeed", "restore wallet from seed",
		func(c *Client) error {
			_, err := c.RestoreFromSeed(
				context.Background(), RestoreFromSeedRequest{},
			)

			return err
		},
	},
	{
		"GenerateFromKeys", "restore wallet from keys",
		func(c *Client) error {
			_, err := c.GenerateFromKeys(
				context.Background(), GenerateFromKeysRequest{},
			)

			return err
		},
	},
	{"GetHeight", "get wallet height", func(c *Client) error {
		_, err := c.GetHeight(context.Background())

		return err
	}},
	{"GetBalance", "get balance", func(c *Client) error {
		_, err := c.GetBalance(context.Background(), 0)

		return err
	}},
	{"GetAddresses", "get addresses", func(c *Client) error {
		_, err := c.GetAddresses(context.Background(), 0)

		return err
	}},
	{"CreateAddress", "create address", func(c *Client) error {
		_, err := c.CreateAddress(context.Background(), 0, "")

		return err
	}},
	{"GetTransfers", "get transfers", func(c *Client) error {
		_, err := c.GetTransfers(context.Background(), TransferFilter{})

		return err
	}},
	{"Transfer", "transfer", func(c *Client) error {
		_, err := c.Transfer(context.Background(), TransferRequest{})

		return err
	}},
	{"SetAutoRefresh", "set auto refresh", func(c *Client) error {
		return c.SetAutoRefresh(context.Background(), time.Second)
	}},
}

func TestEveryMethodSurfacesTheWalletErrorCode(t *testing.T) {
	t.Parallel()

	for _, tc := range everyCall {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			client, _ := singleReply(
				t, anyMethod, rpcFailure(CodeNotOpen, "no wallet open"),
			)

			err := tc.call(client)
			require.Error(t, err)
			require.ErrorIs(t, err, jsonrpc.ErrRPC)
			assert.Equal(t, CodeNotOpen, Code(err))
			assert.Contains(t, err.Error(), tc.context)
		})
	}
}

func TestEveryMethodReportsAnUnreachableWalletRPC(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.NotFoundHandler())
	url := server.URL

	server.Close()

	client := New(url, testTimeout)

	for _, tc := range everyCall {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := tc.call(client)
			require.ErrorIs(t, err, commerr.ErrConnectFailed)
			assert.Zero(t, Code(err))
		})
	}
}

func TestIsInvalidPassword(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{
			name: "not a wallet rpc error",
			err:  assert.AnError,
			want: false,
		},
		{
			name: "dedicated code from other methods",
			err:  &jsonrpc.Error{Code: CodeInvalidPassword, Message: "x"},
			want: true,
		},
		{
			name: "generic code with the open_wallet text",
			err: &jsonrpc.Error{
				Code:    CodeUnknownError,
				Message: "Failed to open wallet : Invalid password.",
			},
			want: true,
		},
		{
			name: "text match ignores case",
			err: &jsonrpc.Error{
				Code: CodeUnknownError, Message: "INVALID PASSWORD",
			},
			want: true,
		},
		{
			name: "generic code with another reason",
			err: &jsonrpc.Error{
				Code:    CodeUnknownError,
				Message: "Failed to open wallet : file not found",
			},
			want: false,
		},
		{
			name: "unrelated code",
			err:  &jsonrpc.Error{Code: CodeNotOpen, Message: "no wallet open"},
			want: false,
		},
		{
			name: "wrapped dedicated code",
			err: ctxerrors.Wrap(
				&jsonrpc.Error{Code: CodeInvalidPassword}, "open wallet",
			),
			want: true,
		},
		{
			name: "wrapped text match",
			err: ctxerrors.Wrap(&jsonrpc.Error{
				Code: CodeUnknownError, Message: "invalid password",
			}, "open wallet"),
			want: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, IsInvalidPassword(tc.err))
		})
	}
}

func TestOpenWalletWithTheWrongPasswordIsRecognised(t *testing.T) {
	t.Parallel()

	client, _ := singleReply(t, "open_wallet", rpcFailure(
		CodeUnknownError, "Failed to open wallet : Invalid password.",
	))

	err := client.OpenWallet(context.Background(), "alice", "wrong")
	require.Error(t, err)

	assert.True(t, IsInvalidPassword(err))
	assert.Equal(t, CodeUnknownError, Code(err))
}

func TestCode(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		err  error
		want int
	}{
		{name: "nil", err: nil, want: 0},
		{name: "not a wallet rpc error", err: assert.AnError, want: 0},
		{
			name: "direct",
			err:  &jsonrpc.Error{Code: CodeNotEnoughMoney},
			want: CodeNotEnoughMoney,
		},
		{
			name: "wrapped",
			err: ctxerrors.Wrap(
				&jsonrpc.Error{Code: CodeWatchOnly}, "transfer",
			),
			want: CodeWatchOnly,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, Code(tc.err))
		})
	}
}

func TestConstantsMatchWalletRPC(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "English", DefaultLanguage)
	assert.Equal(t, uint32(0), PriorityDefault)
	assert.Equal(t, uint32(1), PriorityUnimport)
	assert.Equal(t, uint32(2), PriorityNormal)
	assert.Equal(t, uint32(3), PriorityElevated)
	assert.Equal(t, uint32(4), PriorityPriority)
	assert.Equal(t, -13, CodeNotOpen)
	assert.Equal(t, -22, CodeInvalidPassword)
}

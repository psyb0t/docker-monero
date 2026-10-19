package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/psyb0t/docker-monero/internal/pkg/config"
	corenode "github.com/psyb0t/docker-monero/internal/pkg/core/node"
	corewallets "github.com/psyb0t/docker-monero/internal/pkg/core/wallets"
	"github.com/psyb0t/docker-monero/internal/pkg/metrics"
	"github.com/psyb0t/docker-monero/internal/pkg/monero/daemonrpc"
	"github.com/psyb0t/docker-monero/internal/pkg/monero/walletrpc/walletrpctest"
	"github.com/psyb0t/docker-monero/internal/pkg/walletpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	maxOpenWallets    = 3
	defaultRPCTimeout = 5 * time.Second
	startTimeout      = 5 * time.Second
	nodeHeight        = 1000
)

var testHash = strings.Repeat("ab", 32)

func TestMain(m *testing.M) {
	flag.Parse()

	if !testing.Verbose() {
		slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	}

	os.Exit(m.Run())
}

type rpcFailure struct {
	code    int
	message string
}

type daemonRequest struct {
	key    string
	params json.RawMessage
}

// fakeMonerod speaks the monerod endpoints the daemon client uses. Fixtures
// are plain fields, adjusted through update while the server runs.
type fakeMonerod struct {
	t      *testing.T
	server *httptest.Server

	mu           sync.Mutex
	info         daemonrpc.Info
	header       daemonrpc.BlockHeader
	fee          daemonrpc.FeeEstimate
	pool         daemonrpc.PoolStats
	txs          daemonrpc.Transactions
	generated    daemonrpc.GeneratedBlocks
	rpcFailures  map[string]rpcFailure
	httpFailures map[string]int
	requests     []daemonRequest
}

func newFakeMonerod(t *testing.T) *fakeMonerod {
	t.Helper()

	fake := &fakeMonerod{
		t: t,
		info: daemonrpc.Info{
			Height:                   nodeHeight,
			Synchronized:             true,
			Version:                  "0.18.4.0-release",
			StartTime:                1700000000,
			OutgoingConnectionsCount: 8,
			IncomingConnectionsCount: 2,
			TxPoolSize:               3,
			DatabaseSize:             123456,
			TopBlockHash:             testHash,
			Difficulty:               1,
		},
		header: daemonrpc.BlockHeader{
			MajorVersion: 16,
			MinorVersion: 16,
			Timestamp:    1700000500,
			PrevHash:     strings.Repeat("cd", 32),
			Height:       999,
			Depth:        1,
			Hash:         testHash,
			Difficulty:   1,
			Reward:       600000000000,
			BlockSize:    1500,
			BlockWeight:  1600,
			NumTxes:      2,
			MinerTxHash:  strings.Repeat("ef", 32),
		},
		fee: daemonrpc.FeeEstimate{
			Fee:              20000,
			QuantizationMask: 10000,
			Fees:             []uint64{20000, 80000, 320000, 4000000},
		},
		rpcFailures:  map[string]rpcFailure{},
		httpFailures: map[string]int{},
	}

	fake.server = httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(fake.server.Close)

	return fake
}

func (f *fakeMonerod) update(fn func(fake *fakeMonerod)) {
	f.mu.Lock()
	defer f.mu.Unlock()

	fn(f)
}

// fail makes a JSON-RPC method answer with an error object.
func (f *fakeMonerod) fail(method string, code int, message string) {
	f.update(func(fake *fakeMonerod) {
		fake.rpcFailures[method] = rpcFailure{code: code, message: message}
	})
}

// failHTTP makes a JSON-RPC method or plain path answer with an HTTP status.
func (f *fakeMonerod) failHTTP(key string, status int) {
	f.update(func(fake *fakeMonerod) { fake.httpFailures[key] = status })
}

func (f *fakeMonerod) paramsFor(key string) []json.RawMessage {
	f.mu.Lock()
	defer f.mu.Unlock()

	var params []json.RawMessage

	for _, request := range f.requests {
		if request.key == key {
			params = append(params, request.params)
		}
	}

	return params
}

func (f *fakeMonerod) serve(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	assert.NoError(f.t, err)

	isRPC := r.URL.Path == "/json_rpc"
	key := r.URL.Path
	params := json.RawMessage(body)

	if isRPC {
		var envelope struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}

		assert.NoError(f.t, json.Unmarshal(body, &envelope))

		key, params = envelope.Method, envelope.Params
	}

	f.mu.Lock()
	f.requests = append(f.requests, daemonRequest{key: key, params: params})
	status, failingHTTP := f.httpFailures[key]
	failure, failingRPC := f.rpcFailures[key]
	payload, known := f.payload(key, params)
	f.mu.Unlock()

	if failingHTTP {
		http.Error(w, "monerod is unwell", status)

		return
	}

	if !isRPC {
		writeJSON(f.t, w, payload)

		return
	}

	envelope := map[string]any{"jsonrpc": "2.0", "id": "0"}

	switch {
	case failingRPC:
		envelope["error"] = map[string]any{
			"code": failure.code, "message": failure.message,
		}
	case !known:
		envelope["error"] = map[string]any{
			"code": -32601, "message": "Method not found",
		}
	default:
		envelope["result"] = payload
	}

	writeJSON(f.t, w, envelope)
}

// payload builds the success reply for key. It runs with f.mu held.
func (f *fakeMonerod) payload(key string, params json.RawMessage) (any, bool) {
	switch key {
	case "get_info":
		return withStatus(f.t, f.info), true
	case "get_version":
		return map[string]any{
			"status": "OK", "version": 196617, "release": true,
		}, true
	case "get_last_block_header":
		return map[string]any{"status": "OK", "block_header": f.header}, true
	case "get_block_header_by_height":
		var request struct {
			Height uint64 `json:"height"`
		}

		assert.NoError(f.t, json.Unmarshal(params, &request))

		header := f.header
		header.Height = request.Height

		return map[string]any{"status": "OK", "block_header": header}, true
	case "get_block_header_by_hash":
		var request struct {
			Hash string `json:"hash"`
		}

		assert.NoError(f.t, json.Unmarshal(params, &request))

		header := f.header
		header.Hash = request.Hash

		return map[string]any{"status": "OK", "block_header": header}, true
	case "get_fee_estimate":
		return withStatus(f.t, f.fee), true
	case "generateblocks":
		return withStatus(f.t, f.generated), true
	case "/get_transactions":
		return withStatus(f.t, f.txs), true
	case "/get_transaction_pool_stats":
		return map[string]any{"status": "OK", "pool_stats": f.pool}, true
	}

	return nil, false
}

// withStatus adds the "status":"OK" field monerod puts next to the fields of
// every successful reply. The payload is re-decoded with UseNumber so large
// values stay exact.
func withStatus(t *testing.T, value any) map[string]any {
	t.Helper()

	raw, err := json.Marshal(value)
	assert.NoError(t, err)

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()

	reply := map[string]any{}
	assert.NoError(t, decoder.Decode(&reply))

	reply["status"] = "OK"

	return reply
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()

	encoded, err := json.Marshal(value)
	assert.NoError(t, err)

	w.Header().Set("Content-Type", "application/json")

	_, err = w.Write(encoded)
	assert.NoError(t, err)
}

// Loopback ports this package's tests listen on come from [24000, 28000),
// below the kernel's ephemeral range. A port picked from that range can be
// taken by an unrelated client connection between the moment it is chosen
// and the moment a fake server binds it, so these are handed out by a
// counter instead and probed free.
const (
	portRangeStart = 24000
	portRangeSize  = 4000
	portTries      = 200
)

var (
	portsMu  sync.Mutex
	portNext = portRangeStart + os.Getpid()*7%(portRangeSize/2)
)

// reservePorts returns the first of count consecutive free loopback ports no
// other test of this process was given.
func reservePorts(t *testing.T, count int) int {
	t.Helper()

	portsMu.Lock()
	defer portsMu.Unlock()

	for range portTries {
		start := portNext

		portNext += count
		if portNext+count > portRangeStart+portRangeSize {
			portNext = portRangeStart
		}

		if portsAreFree(t, start, count) {
			return start
		}
	}

	require.FailNow(t, "no free loopback port range")

	return 0
}

func portsAreFree(t *testing.T, start, count int) bool {
	t.Helper()

	free := true

	for port := start; port < start+count; port++ {
		listener, err := (&net.ListenConfig{}).Listen(
			context.Background(), "tcp", net.JoinHostPort(
				"127.0.0.1", strconv.Itoa(port),
			),
		)
		if err != nil {
			free = false

			continue
		}

		require.NoError(t, listener.Close())
	}

	return free
}

// fixtureOptions tunes the deployment the services run as. The zero value is
// a regtest node with writes and transfers on.
type fixtureOptions struct {
	network          config.Network
	readOnly         bool
	disableTransfers bool
}

// fixture is the core services wired as production wires them: the real
// wallet pool over fake wallet-rpc processes and the real daemon client over
// a fake monerod.
type fixture struct {
	daemon  *fakeMonerod
	wallets *walletrpctest.State
	pool    *walletpool.Pool
	node    *corenode.Service
	service *corewallets.Service
	metrics *metrics.Metrics
}

func newFixture(t *testing.T, options fixtureOptions) *fixture {
	t.Helper()

	if options.network == "" {
		options.network = config.NetworkRegtest
	}

	current := &fixture{
		daemon:  newFakeMonerod(t),
		metrics: metrics.New(metrics.Gauges{}),
	}
	daemon := daemonrpc.New(current.daemon.server.URL, defaultRPCTimeout)

	walletDir := t.TempDir()
	current.wallets = walletrpctest.NewState(walletDir)

	portStart := reservePorts(t, maxOpenWallets)

	var err error

	current.pool, err = walletpool.New(walletpool.Settings{
		WalletRPCPath: "monero-wallet-rpc",
		WalletDir:     walletDir,
		DaemonAddress: "127.0.0.1:1",
		Network:       options.network,
		PortStart:     portStart,
		MaxOpen:       maxOpenWallets,
		StartTimeout:  startTimeout,
		RPCTimeout:    defaultRPCTimeout,
	}, walletrpctest.NewLauncher(current.wallets))
	require.NoError(t, err)

	t.Cleanup(func() { current.pool.CloseAll(context.Background()) })

	current.node = corenode.New(daemon, corenode.Options{
		Network:  options.network,
		ReadOnly: options.readOnly,
		Restarts: func() uint64 { return 4 },
	})
	current.service = corewallets.New(
		current.pool, daemon, corewallets.Options{
			ReadOnly:       options.readOnly,
			AllowTransfers: !options.disableTransfers,
		},
	)

	return current
}

// allTools registers every optional tool.
var allTools = Options{AllowTransfers: true, Regtest: true}

func (f *fixture) newServer(t *testing.T, options Options) *mcpsdk.Server {
	t.Helper()

	server, err := NewServer(f.node, f.service, f.metrics, options)
	require.NoError(t, err)

	return server
}

// connect joins a client to server over the SDK's in-memory transport.
func connect(t *testing.T, server *mcpsdk.Server) *mcpsdk.ClientSession {
	t.Helper()

	clientTransport, serverTransport := mcpsdk.NewInMemoryTransports()

	serverSession, err := server.Connect(
		context.Background(), serverTransport, nil,
	)
	require.NoError(t, err)

	client := mcpsdk.NewClient(
		&mcpsdk.Implementation{Name: "test-client", Version: "1"}, nil,
	)

	session, err := client.Connect(
		context.Background(), clientTransport, nil,
	)
	require.NoError(t, err)

	t.Cleanup(func() {
		assert.NoError(t, session.Close())
		assert.NoError(t, serverSession.Wait())
	})

	return session
}

// session is a connected client over a server with every optional tool.
func (f *fixture) session(t *testing.T) *mcpsdk.ClientSession {
	t.Helper()

	return connect(t, f.newServer(t, allTools))
}

func toolNames(t *testing.T, session *mcpsdk.ClientSession) []string {
	t.Helper()

	listed, err := session.ListTools(context.Background(), nil)
	require.NoError(t, err)

	names := make([]string, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}

	return names
}

// call invokes a tool and returns its result; a protocol failure fails the
// test.
func call(
	t *testing.T,
	session *mcpsdk.ClientSession,
	name string,
	arguments any,
) *mcpsdk.CallToolResult {
	t.Helper()

	result, err := session.CallTool(
		context.Background(),
		&mcpsdk.CallToolParams{Name: name, Arguments: arguments},
	)
	require.NoError(t, err)

	return result
}

// text is the single text block of a tool result.
func text(t *testing.T, result *mcpsdk.CallToolResult) string {
	t.Helper()

	require.Len(t, result.Content, 1)

	content, ok := result.Content[0].(*mcpsdk.TextContent)
	require.True(t, ok, "content is %T", result.Content[0])

	return content.Text
}

// structured decodes the typed output of a successful tool call.
//
//nolint:ireturn // a generic decode helper returns whatever it decodes.
func structured[T any](t *testing.T, result *mcpsdk.CallToolResult) T {
	t.Helper()

	require.False(t, result.IsError, "tool failed: %v", result.Content)

	encoded, err := json.Marshal(result.StructuredContent)
	require.NoError(t, err)

	var decoded T

	require.NoError(t, json.Unmarshal(encoded, &decoded), string(encoded))

	return decoded
}

// requireToolError asserts a tool failure reported in-band as "CODE: message".
func requireToolError(
	t *testing.T,
	result *mcpsdk.CallToolResult,
	want string,
) {
	t.Helper()

	require.True(t, result.IsError, "expected a tool error")
	assert.Equal(t, want, text(t, result))
	assert.Nil(t, result.StructuredContent)
	assert.NotContains(t, text(t, result), ".go:")
}

// callAs invokes a tool and decodes its typed output.
//
//nolint:ireturn // a generic decode helper returns whatever it decodes.
func callAs[T any](
	t *testing.T,
	session *mcpsdk.ClientSession,
	name string,
	arguments any,
) T {
	t.Helper()

	return structured[T](t, call(t, session, name, arguments))
}

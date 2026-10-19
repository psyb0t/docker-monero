package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/aichteeteapee/serbewr"
	"github.com/psyb0t/docker-monero/internal/pkg/config"
	corenode "github.com/psyb0t/docker-monero/internal/pkg/core/node"
	corewallets "github.com/psyb0t/docker-monero/internal/pkg/core/wallets"
	"github.com/psyb0t/docker-monero/internal/pkg/mcp"
	"github.com/psyb0t/docker-monero/internal/pkg/metrics"
	"github.com/psyb0t/docker-monero/internal/pkg/monero/daemonrpc"
	"github.com/psyb0t/docker-monero/internal/pkg/monero/walletrpc/walletrpctest"
	"github.com/psyb0t/docker-monero/internal/pkg/process/processtest"
	"github.com/psyb0t/docker-monero/internal/pkg/walletpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	maxOpenWallets    = 3
	defaultBodyLimit  = 1 << 20
	defaultRPCTimeout = 5 * time.Second
	clientTimeout     = 15 * time.Second
	startTimeout      = 5 * time.Second
)

var (
	testAPIToken = strings.Repeat("t", 32)
	testHash     = strings.Repeat("ab", 32)
)

func TestMain(m *testing.M) {
	flag.Parse()

	if !testing.Verbose() {
		slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	}

	os.Exit(m.Run())
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
	delay        time.Duration
	requests     []daemonRequest
}

func newFakeMonerod(t *testing.T) *fakeMonerod {
	t.Helper()

	fake := &fakeMonerod{
		t: t,
		info: daemonrpc.Info{
			Height:                   1000,
			Synchronized:             true,
			Nettype:                  "fakechain",
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
	delay := f.delay
	status, failingHTTP := f.httpFailures[key]
	failure, failingRPC := f.rpcFailures[key]
	payload, known := f.payload(key, params)
	f.mu.Unlock()

	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
	}

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

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()

	encoded, err := json.Marshal(value)
	assert.NoError(t, err)

	w.Header().Set("Content-Type", "application/json")

	_, err = w.Write(encoded)
	assert.NoError(t, err)
}

// Loopback ports this package's tests listen on come from [20000, 24000),
// below the kernel's ephemeral range. A port picked from that range can be
// taken by an unrelated client connection between the moment it is chosen
// and the moment a fake server binds it, so these are handed out by a
// counter instead and probed free.
const (
	portRangeStart = 20000
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

// stackOptions tunes the deployment a stack runs as. The zero value is a
// regtest node with writes and transfers on.
type stackOptions struct {
	network          config.Network
	readOnly         bool
	disableTransfers bool
	withoutMetrics   bool
	maxRequestBytes  int64
	rpcTimeout       time.Duration
	ready            func(ctx context.Context) bool
}

// stack is the whole service wired as production wires it: the real wallet
// pool over fake wallet-rpc processes, the real daemon client over a fake
// monerod, the core services, MCP, and the router served over HTTP.
type stack struct {
	baseURL  string
	client   *http.Client
	daemon   *fakeMonerod
	wallets  *walletrpctest.State
	launcher *processtest.Launcher
	pool     *walletpool.Pool
	metrics  *metrics.Metrics
	router   *Router
	restarts atomic.Uint64
}

func newStack(t *testing.T, options stackOptions) *stack {
	t.Helper()

	if options.network == "" {
		options.network = config.NetworkRegtest
	}

	if options.maxRequestBytes == 0 {
		options.maxRequestBytes = defaultBodyLimit
	}

	if options.rpcTimeout == 0 {
		options.rpcTimeout = defaultRPCTimeout
	}

	current := &stack{daemon: newFakeMonerod(t)}
	daemon := daemonrpc.New(current.daemon.server.URL, options.rpcTimeout)

	walletDir := t.TempDir()
	current.wallets = walletrpctest.NewState(walletDir)
	current.launcher = walletrpctest.NewLauncher(current.wallets)

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
	}, current.launcher)
	require.NoError(t, err)

	t.Cleanup(func() { current.pool.CloseAll(context.Background()) })

	nodeService := corenode.New(daemon, corenode.Options{
		Network:  options.network,
		ReadOnly: options.readOnly,
		Restarts: current.restarts.Load,
	})
	walletService := corewallets.New(
		current.pool, daemon, corewallets.Options{
			ReadOnly:       options.readOnly,
			AllowTransfers: !options.disableTransfers,
		},
	)

	if !options.withoutMetrics {
		current.metrics = metrics.New(metrics.Gauges{})
	}

	mcpHandler, err := mcp.Handler(
		nodeService, walletService, current.metrics, mcp.Options{
			ReadOnly:        options.readOnly,
			AllowTransfers:  !options.disableTransfers,
			Regtest:         options.network == config.NetworkRegtest,
			MaxRequestBytes: options.maxRequestBytes,
		},
	)
	require.NoError(t, err)

	ready := options.ready
	if ready == nil {
		ready = func(ctx context.Context) bool {
			_, err := daemon.GetVersion(ctx)

			return err == nil
		}
	}

	current.router = NewRouter(RouterConfig{
		APIToken:        testAPIToken,
		MaxRequestBytes: options.maxRequestBytes,
		Server:          New(nodeService, walletService),
		MCPHandler:      mcpHandler,
		Metrics:         current.metrics,
		Ready:           ready,
	})

	current.baseURL = serveRouter(t, current.router.Serbewr())
	current.client = newClient()

	return current
}

func newClient() *http.Client {
	return &http.Client{
		Timeout: clientTimeout,
		// A redirect is a finding, not something to follow: a redirected POST
		// loses its body.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// serveRouter runs router on a free loopback port until the test ends and
// returns its base URL.
func serveRouter(t *testing.T, router *serbewr.Router) string {
	t.Helper()

	listener, err := serbewr.NewWithConfigAndLogger(
		serbewr.Config{
			ListenAddress:     "127.0.0.1:0",
			ReadHeaderTimeout: startTimeout,
			WriteTimeout:      clientTimeout,
			ShutdownTimeout:   startTimeout,
			MaxHeaderBytes:    aichteeteapee.DefaultHTTPServerMaxHeaderBytes,
			ServiceName:       "test",
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		defer close(done)

		_ = listener.Start(ctx, router)
	}()

	t.Cleanup(func() {
		cancel()
		<-done
	})

	require.Eventually(t, func() bool {
		return listener.GetHTTPListenerAddr() != nil
	}, startTimeout, time.Millisecond, "server did not start listening")

	return "http://" + listener.GetHTTPListenerAddr().String()
}

// response is one finished HTTP exchange.
type response struct {
	status int
	header http.Header
	body   []byte
}

func (r response) text() string { return string(r.body) }

type requestMod func(req *http.Request)

func withoutAuth() requestMod {
	return func(req *http.Request) { req.Header.Del("Authorization") }
}

func withAuth(value string) requestMod {
	return func(req *http.Request) { req.Header.Set("Authorization", value) }
}

func withHeader(name, value string) requestMod {
	return func(req *http.Request) { req.Header.Set(name, value) }
}

// try performs one request and reports transport failures as an error, so it
// is safe to call from goroutines the test does not own.
func (s *stack) try(
	method, path, body string,
	mods ...requestMod,
) (response, error) {
	reader := io.Reader(http.NoBody)
	if body != "" {
		reader = strings.NewReader(body)
	}

	req, err := http.NewRequestWithContext(
		context.Background(), method, s.baseURL+path, reader,
	)
	if err != nil {
		return response{}, fmt.Errorf("build request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+testAPIToken)

	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	for _, mod := range mods {
		mod(req)
	}

	return doRequest(s.client, req)
}

func (s *stack) send(
	t *testing.T,
	method, path, body string,
	mods ...requestMod,
) response {
	t.Helper()

	resp, err := s.try(method, path, body, mods...)
	require.NoError(t, err)

	return resp
}

func (s *stack) get(t *testing.T, path string, mods ...requestMod) response {
	t.Helper()

	return s.send(t, http.MethodGet, path, "", mods...)
}

func (s *stack) post(
	t *testing.T,
	path, body string,
	mods ...requestMod,
) response {
	t.Helper()

	return s.send(t, http.MethodPost, path, body, mods...)
}

func doRequest(client *http.Client, req *http.Request) (response, error) {
	resp, err := client.Do(req)
	if err != nil {
		return response{}, fmt.Errorf("send request: %w", err)
	}

	body, readErr := io.ReadAll(resp.Body)
	closeErr := resp.Body.Close()

	if err := errors.Join(readErr, closeErr); err != nil {
		return response{}, fmt.Errorf("read response: %w", err)
	}

	return response{
		status: resp.StatusCode, header: resp.Header, body: body,
	}, nil
}

//nolint:ireturn // a generic decode helper returns whatever it decodes.
func decodeAs[T any](t *testing.T, resp response) T {
	t.Helper()

	var decoded T

	require.NoError(t, json.Unmarshal(resp.body, &decoded), resp.text())

	return decoded
}

// requireError asserts the aichteeteapee error envelope and that no
// internal detail leaked into it.
func requireError(
	t *testing.T,
	resp response,
	status int,
	code, message string,
) {
	t.Helper()

	require.Equal(t, status, resp.status, resp.text())
	assert.Equal(t, "application/json", resp.header.Get("Content-Type"))

	envelope := decodeAs[aichteeteapee.ErrorResponse](t, resp)
	assert.Equal(t, code, envelope.Code)
	assert.Equal(t, message, envelope.Message)
	assert.NotContains(t, resp.text(), ".go:")
	assert.NotContains(t, resp.text(), "internal/pkg")
}

func requireStatus(t *testing.T, resp response, status int) {
	t.Helper()

	require.Equal(t, status, resp.status, resp.text())
}

// jsonBody renders a request body from a Go value.
func jsonBody(t *testing.T, value any) string {
	t.Helper()

	encoded, err := json.Marshal(value)
	require.NoError(t, err)

	return string(encoded)
}

func (s *stack) createWallet(t *testing.T, name string) {
	t.Helper()

	resp := s.post(t, "/v1/wallets", jsonBody(t, map[string]any{
		"name": name, "password": "pw",
	}))
	requireStatus(t, resp, http.StatusCreated)
}

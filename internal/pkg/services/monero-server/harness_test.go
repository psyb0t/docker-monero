package moneroserver

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/psyb0t/docker-monero/internal/pkg/config"
	"github.com/psyb0t/docker-monero/internal/pkg/monero/daemonrpc"
	"github.com/psyb0t/docker-monero/internal/pkg/monero/walletrpc/walletrpctest"
	"github.com/psyb0t/docker-monero/internal/pkg/process/processtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testTimeout    = 5 * time.Second
	testMaxWallets = 3
	fakeMonerodBin = "/fake/monerod"
	fakeWalletBin  = "/fake/monero-wallet-rpc"
)

var testAPIToken = strings.Repeat("t", 32)

func TestMain(m *testing.M) {
	flag.Parse()

	if !testing.Verbose() {
		slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	}

	os.Exit(m.Run())
}

// fakeMonerod answers the two monerod calls the service itself makes: the
// readiness probe and the node info the gauges scrape.
type fakeMonerod struct {
	t *testing.T

	mu         sync.Mutex
	info       daemonrpc.Info
	failMethod map[string]int
	delay      time.Duration
}

func newFakeMonerod(t *testing.T) *fakeMonerod {
	t.Helper()

	return &fakeMonerod{
		t: t,
		info: daemonrpc.Info{
			Height: 1000, Synchronized: true, Version: "0.18.4.0",
		},
		failMethod: map[string]int{},
	}
}

func (f *fakeMonerod) update(fn func(fake *fakeMonerod)) {
	f.mu.Lock()
	defer f.mu.Unlock()

	fn(f)
}

func (f *fakeMonerod) failHTTP(method string, status int) {
	f.update(func(fake *fakeMonerod) { fake.failMethod[method] = status })
}

func (f *fakeMonerod) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var envelope struct {
		Method string `json:"method"`
	}

	assert.NoError(f.t, json.NewDecoder(r.Body).Decode(&envelope))

	f.mu.Lock()
	status, failing := f.failMethod[envelope.Method]
	delay := f.delay
	info := f.info
	f.mu.Unlock()

	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
	}

	if failing {
		http.Error(w, "monerod is unwell", status)

		return
	}

	reply := map[string]any{"jsonrpc": "2.0", "id": "0"}

	switch envelope.Method {
	case "get_version":
		reply["result"] = map[string]any{
			"status": "OK", "version": 196617, "release": true,
		}
	case "get_info":
		reply["result"] = infoReply(f.t, info)
	default:
		reply["error"] = map[string]any{
			"code": -32601, "message": "Method not found",
		}
	}

	encoded, err := json.Marshal(reply)
	assert.NoError(f.t, err)

	w.Header().Set("Content-Type", "application/json")

	_, err = w.Write(encoded)
	assert.NoError(f.t, err)
}

// infoReply is the get_info result: the fields plus monerod's status.
func infoReply(t *testing.T, info daemonrpc.Info) map[string]any {
	t.Helper()

	raw, err := json.Marshal(info)
	assert.NoError(t, err)

	reply := map[string]any{}
	assert.NoError(t, json.Unmarshal(raw, &reply))

	reply["status"] = "OK"

	return reply
}

// listen serves the fake on a loopback port, the way the real monerod would
// once the supervisor starts it, and returns what stops it.
func (f *fakeMonerod) listen(port int) (func(), error) {
	listener, err := (&net.ListenConfig{}).Listen(
		context.Background(), "tcp", address(port),
	)
	if err != nil {
		return nil, err
	}

	server := &http.Server{Handler: f, ReadHeaderTimeout: testTimeout}

	go func() { _ = server.Serve(listener) }()

	return func() { _ = server.Close() }, nil
}

// freePorts returns count distinct free loopback ports.
func freePorts(t *testing.T, count int) []int {
	t.Helper()

	ports := make([]int, 0, count)
	for range count {
		ports = append(ports, reservePorts(t, 1))
	}

	return ports
}

// Loopback ports this package's tests listen on come from [28000, 32000),
// below the kernel's ephemeral range. A port picked from that range can be
// taken by an unrelated client connection between the moment it is chosen
// and the moment a fake server binds it, so these are handed out by a
// counter instead and probed free.
const (
	portRangeStart = 28000
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

func address(port int) string {
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
}

// testConfig is a valid regtest deployment whose every listener and RPC port
// is a free loopback port.
func testConfig(t *testing.T, mods ...func(cfg *config.Config)) config.Config {
	t.Helper()

	ports := freePorts(t, 3)

	walletStart := reservePorts(t, testMaxWallets)

	cfg := config.Config{
		HTTPListenAddress:    address(ports[0]),
		MetricsListenAddress: address(ports[1]),
		APIToken:             testAPIToken,
		Network:              string(config.NetworkRegtest),
		MonerodPath:          fakeMonerodBin,
		WalletRPCPath:        fakeWalletBin,
		DataDir:              t.TempDir(),
		WalletDir:            t.TempDir(),
		P2PPort:              18080,
		RPCPort:              ports[2],
		RestrictedRPCPort:    18089,
		MonerodStopTimeout:   testTimeout,
		WalletRPCPortStart:   walletStart,
		MaxOpenWallets:       testMaxWallets,
		WalletStartTimeout:   testTimeout,
		RPCTimeout:           testTimeout,
		AllowTransfers:       true,
		MCPAllowTransfers:    true,
		MaxRequestBytes:      config.DefaultMaxRequestBytes,
	}

	for _, mod := range mods {
		mod(&cfg)
	}

	require.NoError(t, cfg.Validate())

	return cfg
}

// newLauncher is the fake process seam: a launch of the monerod binary
// serves the fake monerod on the configured RPC port, and a launch of
// wallet-rpc serves a fake wallet on the port it was asked for.
func newLauncher(
	cfg config.Config,
	monerod *fakeMonerod,
	state *walletrpctest.State,
) *processtest.Launcher {
	launcher := walletrpctest.NewLauncher(state)
	launchWallet := launcher.OnLaunch

	launcher.OnLaunch = func(launch processtest.Launch) (func(), error) {
		if launch.Name == cfg.MonerodPath {
			return monerod.listen(cfg.RPCPort)
		}

		return launchWallet(launch)
	}

	return launcher
}

// httpGet fetches url with the bearer token unless token is empty.
func httpGet(t *testing.T, url, token string) (int, []byte) {
	t.Helper()

	status, body, err := tryGet(url, token)
	require.NoError(t, err)

	return status, body
}

func tryGet(url, token string) (int, []byte, error) {
	return tryRequest(http.MethodGet, url, "", token)
}

func tryRequest(
	method, url, body, token string,
) (int, []byte, error) {
	req, err := http.NewRequestWithContext(
		context.Background(), method, url, strings.NewReader(body),
	)
	if err != nil {
		return 0, nil, err
	}

	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	if body != "" {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
	}

	resp, err := (&http.Client{Timeout: testTimeout}).Do(req)
	if err != nil {
		return 0, nil, err
	}

	payload, readErr := io.ReadAll(resp.Body)
	closeErr := resp.Body.Close()

	if readErr != nil {
		return 0, nil, readErr
	}

	return resp.StatusCode, payload, closeErr
}

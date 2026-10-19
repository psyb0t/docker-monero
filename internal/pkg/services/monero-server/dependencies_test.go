package moneroserver

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/aichteeteapee/serbewr"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/docker-monero/internal/pkg/config"
	"github.com/psyb0t/docker-monero/internal/pkg/metrics"
	"github.com/psyb0t/docker-monero/internal/pkg/monero/daemonrpc"
	"github.com/psyb0t/docker-monero/internal/pkg/monero/walletrpc/walletrpctest"
	"github.com/psyb0t/docker-monero/internal/pkg/node"
	"github.com/psyb0t/docker-monero/internal/pkg/process/processtest"
	"github.com/psyb0t/docker-monero/internal/pkg/walletpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServerConfig(t *testing.T) {
	t.Parallel()

	cfg := serverConfig("127.0.0.1:8080", listenerAPI, 42*time.Second)

	assert.Equal(t, "127.0.0.1:8080", cfg.ListenAddress)
	assert.Equal(t, "api", cfg.ServiceName)
	assert.Equal(t, 42*time.Second, cfg.WriteTimeout)
	assert.Equal(t, aichteeteapee.DefaultHTTPServerReadTimeout, cfg.ReadTimeout)
	assert.Equal(
		t, aichteeteapee.DefaultHTTPServerReadHeaderTimeout,
		cfg.ReadHeaderTimeout,
	)
	assert.Equal(t, aichteeteapee.DefaultHTTPServerIdleTimeout, cfg.IdleTimeout)
	assert.Equal(
		t, aichteeteapee.DefaultHTTPServerShutdownTimeout, cfg.ShutdownTimeout,
	)
	assert.Equal(
		t, aichteeteapee.DefaultHTTPServerMaxHeaderBytes, cfg.MaxHeaderBytes,
	)
	assert.Equal(
		t, aichteeteapee.DefaultFileUploadMaxMemory, cfg.FileUploadMaxMemory,
	)
	assert.False(t, cfg.TLSEnabled, "TLS is terminated outside the service")
}

func TestServerConfigIgnoresTheHTTPServerEnvironment(t *testing.T) {
	t.Setenv(aichteeteapee.EnvVarNameHTTPServerListenAddress, "0.0.0.0:1")
	t.Setenv(aichteeteapee.EnvVarNameHTTPServerWriteTimeout, "1ms")
	t.Setenv(aichteeteapee.EnvVarNameHTTPServerTLSEnabled, "true")

	cfg := serverConfig("127.0.0.1:9091", listenerMetrics, time.Minute)

	assert.Equal(t, "127.0.0.1:9091", cfg.ListenAddress)
	assert.Equal(t, time.Minute, cfg.WriteTimeout)
	assert.Equal(t, "metrics", cfg.ServiceName)
	assert.False(t, cfg.TLSEnabled)
}

// serveRouter runs router on a free loopback port until the test ends and
// returns its base URL.
func serveRouter(t *testing.T, router *serbewr.Router) string {
	t.Helper()

	address := address(freePorts(t, 1)[0])

	server, err := serbewr.NewWithConfig(
		serverConfig(address, "test", testTimeout),
	)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		defer close(done)

		_ = server.Start(ctx, router)
	}()

	t.Cleanup(func() {
		cancel()
		<-done
	})

	require.Eventually(t, func() bool {
		return server.GetHTTPListenerAddr() != nil
	}, testTimeout, time.Millisecond)

	return "http://" + server.GetHTTPListenerAddr().String()
}

// scrape parses a Prometheus text exposition into series -> value.
func scrape(t *testing.T, body []byte) map[string]string {
	t.Helper()

	series := map[string]string{}

	for line := range strings.SplitSeq(string(body), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		cut := strings.LastIndex(line, " ")
		require.Positive(t, cut, line)

		series[line[:cut]] = line[cut+1:]
	}

	return series
}

func TestMetricsRouterServesTheRegistry(t *testing.T) {
	t.Parallel()

	recorder := metrics.New(metrics.Gauges{
		OpenWallets:    func() float64 { return 2 },
		MaxOpenWallets: 8,
	})
	recorder.ObserveHTTP(http.MethodGet, "GET /v1/node/info", 200, 0.1)

	url := serveRouter(t, metricsRouter(recorder))

	status, body := httpGet(t, url+"/metrics", "")
	require.Equal(t, http.StatusOK, status)

	series := scrape(t, body)

	assert.Equal(t, "2", series["monero_open_wallets"])
	assert.Equal(t, "8", series["monero_max_open_wallets"])
	assert.Equal(t, "1", series[`monero_api_http_requests_total{method="GET",`+
		`route="GET /v1/node/info",status="200"}`])
	assert.Contains(t, series, "go_goroutines")
}

func TestMetricsRouterContentType(t *testing.T) {
	t.Parallel()

	url := serveRouter(t, metricsRouter(metrics.New(metrics.Gauges{})))

	req, err := http.NewRequestWithContext(
		context.Background(), http.MethodGet, url+"/metrics", nil,
	)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	t.Cleanup(func() { assert.NoError(t, resp.Body.Close()) })

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "text/plain")
}

func TestMetricsRouterOnlyServesMetricsOverGet(t *testing.T) {
	t.Parallel()

	url := serveRouter(t, metricsRouter(metrics.New(metrics.Gauges{})))

	status, _, err := tryRequest(http.MethodPost, url+"/metrics", "x", "")
	require.NoError(t, err)
	assert.Equal(t, http.StatusMethodNotAllowed, status)

	status, _, err = tryGet(url+"/anything-else", "")
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, status)

	status, _, err = tryGet(url+"/v1/node/info", "")
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, status, "the API is not on this port")
}

// brokenCollector yields one sample Prometheus cannot encode.
type brokenCollector struct{}

func (brokenCollector) Describe(chan<- *prometheus.Desc) {}

func (brokenCollector) Collect(out chan<- prometheus.Metric) {
	out <- prometheus.NewInvalidMetric(
		prometheus.NewDesc("monero_broken", "a broken collector", nil, nil),
		assert.AnError,
	)
}

func TestMetricsScrapeSurvivesACollectorError(t *testing.T) {
	t.Parallel()

	recorder := metrics.New(metrics.Gauges{MaxOpenWallets: 8})
	require.NoError(t, recorder.Registry().Register(brokenCollector{}))

	url := serveRouter(t, metricsRouter(recorder))

	status, body := httpGet(t, url+"/metrics", "")

	require.Equal(t, http.StatusOK, status, string(body))
	assert.Equal(t, "8", scrape(t, body)["monero_max_open_wallets"])
}

func TestDaemonProber(t *testing.T) {
	t.Parallel()

	monerod := newFakeMonerod(t)
	address, err := monerod.serveOnFreePort(t)
	require.NoError(t, err)

	prober := daemonProber{daemon: daemonrpc.New(address, testTimeout)}

	require.NoError(t, prober.Probe(context.Background()))

	monerod.failHTTP("get_version", http.StatusServiceUnavailable)

	err = prober.Probe(context.Background())

	require.ErrorIs(t, err, commerr.ErrUnavailable)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	require.ErrorIs(t, prober.Probe(ctx), context.Canceled)
}

func TestDaemonProberOfAMonerodThatIsNotListening(t *testing.T) {
	t.Parallel()

	prober := daemonProber{daemon: daemonrpc.New(
		"http://"+address(freePorts(t, 1)[0]), testTimeout,
	)}

	err := prober.Probe(context.Background())

	require.ErrorIs(t, err, commerr.ErrConnectFailed)
}

// serveOnFreePort serves the fake on a free port and returns its URL.
func (f *fakeMonerod) serveOnFreePort(t *testing.T) (string, error) {
	t.Helper()

	port := freePorts(t, 1)[0]

	stop, err := f.listen(port)
	if err != nil {
		return "", err
	}

	t.Cleanup(stop)

	return "http://" + address(port), nil
}

type gaugeFixture struct {
	monerod    *fakeMonerod
	daemon     *daemonrpc.Client
	pool       *walletpool.Pool
	supervisor *node.Supervisor
	launcher   *processtest.Launcher
	cfg        config.Config
}

func newGaugeFixture(t *testing.T) *gaugeFixture {
	t.Helper()

	current := &gaugeFixture{
		monerod: newFakeMonerod(t),
		cfg:     testConfig(t),
	}

	state := walletrpctest.NewState(current.cfg.WalletDir)
	current.launcher = newLauncher(current.cfg, current.monerod, state)
	current.daemon = daemonrpc.New(current.cfg.MonerodRPCURL(), testTimeout)

	var err error

	current.pool, err = walletpool.New(walletpool.Settings{
		WalletRPCPath: current.cfg.WalletRPCPath,
		WalletDir:     current.cfg.WalletDir,
		DaemonAddress: current.cfg.MonerodRPCAddress(),
		Network:       current.cfg.NetworkType(),
		PortStart:     current.cfg.WalletRPCPortStart,
		MaxOpen:       current.cfg.MaxOpenWallets,
		StartTimeout:  testTimeout,
		RPCTimeout:    testTimeout,
	}, current.launcher)
	require.NoError(t, err)

	t.Cleanup(func() { current.pool.CloseAll(context.Background()) })

	current.supervisor = node.New(node.Settings{
		Path:         current.cfg.MonerodPath,
		StopTimeout:  testTimeout,
		RestartDelay: 5 * time.Millisecond,
	}, current.launcher, daemonProber{daemon: current.daemon})

	return current
}

func (g *gaugeFixture) gauges(ctx context.Context) metrics.Gauges {
	return gauges(ctx, g.daemon, g.pool, g.supervisor, g.cfg)
}

func TestGaugesReportNodeAndPoolState(t *testing.T) {
	t.Parallel()

	fixture := newGaugeFixture(t)

	stop, err := fixture.monerod.listen(fixture.cfg.RPCPort)
	require.NoError(t, err)

	t.Cleanup(stop)

	scraped := fixture.gauges(context.Background())

	assert.InDelta(t, 1000, scraped.NodeHeight(), 0)
	assert.InDelta(t, 1, scraped.NodeSynced(), 0)
	assert.InDelta(t, 0, scraped.OpenWallets(), 0)
	assert.InDelta(t, 0, scraped.NodeRestarts(), 0)
	assert.InDelta(t, testMaxWallets, scraped.MaxOpenWallets, 0)

	fixture.monerod.update(func(fake *fakeMonerod) {
		fake.info.Height = 2500
		fake.info.Synchronized = false
	})

	_, err = fixture.pool.Create(context.Background(), "alice", "pw", "")
	require.NoError(t, err)

	assert.InDelta(t, 2500, scraped.NodeHeight(), 0, "read at scrape time")
	assert.InDelta(t, 0, scraped.NodeSynced(), 0)
	assert.InDelta(t, 1, scraped.OpenWallets(), 0)
}

func TestGaugesFallBackToZeroWhenTheNodeFails(t *testing.T) {
	t.Parallel()

	fixture := newGaugeFixture(t)

	stop, err := fixture.monerod.listen(fixture.cfg.RPCPort)
	require.NoError(t, err)

	t.Cleanup(stop)

	fixture.monerod.failHTTP("get_info", http.StatusInternalServerError)

	scraped := fixture.gauges(context.Background())

	assert.InDelta(t, 0, scraped.NodeHeight(), 0)
	assert.InDelta(t, 0, scraped.NodeSynced(), 0)
	assert.InDelta(t, testMaxWallets, scraped.MaxOpenWallets, 0)
}

func TestGaugesFallBackToZeroWhenTheNodeIsNotListening(t *testing.T) {
	t.Parallel()

	fixture := newGaugeFixture(t)
	scraped := fixture.gauges(context.Background())

	assert.InDelta(t, 0, scraped.NodeHeight(), 0)
	assert.InDelta(t, 0, scraped.NodeSynced(), 0)
}

func TestGaugesSurviveTheServiceContextEnding(t *testing.T) {
	t.Parallel()

	fixture := newGaugeFixture(t)

	stop, err := fixture.monerod.listen(fixture.cfg.RPCPort)
	require.NoError(t, err)

	t.Cleanup(stop)

	ctx, cancel := context.WithCancel(context.Background())
	scraped := fixture.gauges(ctx)

	cancel()

	assert.InDelta(
		t, 1000, scraped.NodeHeight(), 0,
		"a scrape during shutdown still reads the node",
	)
}

func TestGaugesGiveUpOnAHungNode(t *testing.T) {
	t.Parallel()

	fixture := newGaugeFixture(t)
	fixture.monerod.update(func(fake *fakeMonerod) { fake.delay = time.Minute })

	stop, err := fixture.monerod.listen(fixture.cfg.RPCPort)
	require.NoError(t, err)

	t.Cleanup(stop)

	scraped := fixture.gauges(context.Background())
	started := time.Now()

	assert.InDelta(t, 0, scraped.NodeHeight(), 0)
	assert.Less(t, time.Since(started), 2*gaugeTimeout+time.Second)
	assert.GreaterOrEqual(t, time.Since(started), gaugeTimeout/2)
}

func TestGaugesCountSupervisorRestarts(t *testing.T) {
	t.Parallel()

	fixture := newGaugeFixture(t)
	scraped := fixture.gauges(context.Background())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() { done <- fixture.supervisor.Run(ctx) }()

	t.Cleanup(func() {
		cancel()
		require.NoError(t, <-done)
	})

	require.Eventually(t, func() bool {
		return fixture.launcher.Last() != nil
	}, testTimeout, time.Millisecond)

	fixture.launcher.Last().Exit(assert.AnError)

	require.Eventually(t, func() bool {
		return scraped.NodeRestarts() == 1
	}, testTimeout, time.Millisecond)
}

func tools(t *testing.T, url, token string) ([]string, int) {
	t.Helper()

	status, body, err := tryRequest(
		http.MethodPost, url+"/mcp",
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, token,
	)
	require.NoError(t, err)

	var reply struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}

	if status == http.StatusOK {
		require.NoError(t, json.Unmarshal(body, &reply), string(body))
	}

	names := make([]string, 0, len(reply.Result.Tools))
	for _, tool := range reply.Result.Tools {
		names = append(names, tool.Name)
	}

	return names, status
}

var readTools = []string{
	"monero_node_info", "monero_get_block", "monero_get_transaction",
	"monero_fee_estimate", "monero_list_wallets", "monero_open_wallet",
	"monero_close_wallet", "monero_wallet_status", "monero_wallet_balance",
	"monero_wallet_addresses", "monero_wallet_transfers",
}

func TestBuildWiresTheDeploymentIntoTheMCPToolSet(t *testing.T) {
	testCases := []struct {
		name string
		mod  func(cfg *config.Config)
		want []string
	}{
		{
			name: "regtest with transfers allowed on both transports",
			mod:  func(*config.Config) {},
			want: append(append([]string{}, readTools...),
				"monero_create_wallet", "monero_create_address",
				"monero_generate_blocks", "monero_transfer"),
		},
		{
			name: "transfers stay off MCP unless MCP opts in",
			mod:  func(cfg *config.Config) { cfg.MCPAllowTransfers = false },
			want: append(append([]string{}, readTools...),
				"monero_create_wallet", "monero_create_address",
				"monero_generate_blocks"),
		},
		{
			name: "MCP opt-in does not override a deployment without transfers",
			mod:  func(cfg *config.Config) { cfg.AllowTransfers = false },
			want: append(append([]string{}, readTools...),
				"monero_create_wallet", "monero_create_address",
				"monero_generate_blocks"),
		},
		{
			name: "mainnet has no block generation",
			mod: func(cfg *config.Config) {
				cfg.Network = string(config.NetworkMainnet)
			},
			want: append(append([]string{}, readTools...),
				"monero_create_wallet", "monero_create_address",
				"monero_transfer"),
		},
		{
			name: "read-only registers the readers",
			mod:  func(cfg *config.Config) { cfg.ReadOnly = true },
			want: readTools,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig(t, tc.mod)
			launcher := newLauncher(
				cfg, newFakeMonerod(t), walletrpctest.NewState(cfg.WalletDir),
			)

			deps, err := build(context.Background(), cfg, launcher)
			require.NoError(t, err)

			url := serveRouter(t, deps.apiRouter)

			names, status := tools(t, url, testAPIToken)

			require.Equal(t, http.StatusOK, status)
			assert.ElementsMatch(t, tc.want, names)
		})
	}
}

func TestBuildProtectsTheAPIAndLeavesTheProbesOpen(t *testing.T) {
	cfg := testConfig(t)
	monerod := newFakeMonerod(t)
	launcher := newLauncher(cfg, monerod, walletrpctest.NewState(cfg.WalletDir))

	deps, err := build(context.Background(), cfg, launcher)
	require.NoError(t, err)

	stop, err := monerod.listen(cfg.RPCPort)
	require.NoError(t, err)

	t.Cleanup(stop)

	url := serveRouter(t, deps.apiRouter)

	for _, path := range []string{"/v1/node/info", "/v1/wallets"} {
		status, _ := httpGet(t, url+path, "")
		assert.Equal(t, http.StatusUnauthorized, status, path)

		status, _ = httpGet(t, url+path, strings.Repeat("x", 32))
		assert.Equal(t, http.StatusUnauthorized, status, path)
	}

	_, status := tools(t, url, "")
	assert.Equal(t, http.StatusUnauthorized, status)

	status, _ = httpGet(t, url+"/healthz", "")
	assert.Equal(t, http.StatusOK, status)

	status, _ = httpGet(t, url+"/ready", "")
	assert.Equal(t, http.StatusOK, status, "monerod answers the probe")

	status, body := httpGet(t, url+"/v1/node/info", testAPIToken)
	require.Equal(t, http.StatusOK, status, string(body))

	var info struct {
		Network string `json:"network"`
		Height  int64  `json:"height"`
	}

	require.NoError(t, json.Unmarshal(body, &info))
	assert.Equal(t, "regtest", info.Network)
	assert.EqualValues(t, 1000, info.Height)
}

func TestReadyFollowsTheMonerodProbeAndShutdown(t *testing.T) {
	cfg := testConfig(t)
	monerod := newFakeMonerod(t)
	launcher := newLauncher(cfg, monerod, walletrpctest.NewState(cfg.WalletDir))

	deps, err := build(context.Background(), cfg, launcher)
	require.NoError(t, err)

	url := serveRouter(t, deps.apiRouter)

	status, _ := httpGet(t, url+"/ready", "")
	assert.Equal(t, http.StatusServiceUnavailable, status, "monerod is down")

	stop, err := monerod.listen(cfg.RPCPort)
	require.NoError(t, err)

	t.Cleanup(stop)

	status, _ = httpGet(t, url+"/ready", "")
	assert.Equal(t, http.StatusOK, status)

	deps.router.BeginShutdown()

	status, _ = httpGet(t, url+"/ready", "")
	assert.Equal(t, http.StatusServiceUnavailable, status)
}

func TestBuildKeepsRESTTransfersSeparateFromMCP(t *testing.T) {
	cfg := testConfig(t, func(cfg *config.Config) {
		cfg.AllowTransfers = true
		cfg.MCPAllowTransfers = false
	})
	launcher := newLauncher(
		cfg, newFakeMonerod(t), walletrpctest.NewState(cfg.WalletDir),
	)

	deps, err := build(context.Background(), cfg, launcher)
	require.NoError(t, err)

	url := serveRouter(t, deps.apiRouter)

	status, body, err := tryRequest(
		http.MethodPost, url+"/v1/wallets/ghost/transfers",
		`{"destinations":[{"address":"4A","amount":"1"}]}`, testAPIToken,
	)
	require.NoError(t, err)

	assert.Equal(t, http.StatusConflict, status, "past the transfer switch")
	assert.Contains(t, string(body), "WALLET_NOT_OPEN")
}

func TestBuildRefusesTransfersWhenTheDeploymentDoes(t *testing.T) {
	cfg := testConfig(t, func(cfg *config.Config) {
		cfg.AllowTransfers = false
	})
	launcher := newLauncher(
		cfg, newFakeMonerod(t), walletrpctest.NewState(cfg.WalletDir),
	)

	deps, err := build(context.Background(), cfg, launcher)
	require.NoError(t, err)

	url := serveRouter(t, deps.apiRouter)

	status, body, err := tryRequest(
		http.MethodPost, url+"/v1/wallets/ghost/transfers",
		`{"destinations":[{"address":"4A","amount":"1"}]}`, testAPIToken,
	)
	require.NoError(t, err)

	assert.Equal(t, http.StatusForbidden, status)
	assert.Contains(t, string(body), "TRANSFERS_DISABLED")
}

func TestBuildAppliesTheRequestBodyLimit(t *testing.T) {
	cfg := testConfig(t, func(cfg *config.Config) { cfg.MaxRequestBytes = 64 })
	launcher := newLauncher(
		cfg, newFakeMonerod(t), walletrpctest.NewState(cfg.WalletDir),
	)

	deps, err := build(context.Background(), cfg, launcher)
	require.NoError(t, err)

	url := serveRouter(t, deps.apiRouter)

	oversized := `{"name":"a","password":"` + strings.Repeat("p", 100) + `"}`

	status, _, err := tryRequest(
		http.MethodPost, url+"/v1/wallets", oversized, testAPIToken,
	)
	require.NoError(t, err)
	assert.Equal(t, http.StatusRequestEntityTooLarge, status)
}

func TestBuildFailures(t *testing.T) {
	t.Parallel()

	t.Run("a monerod argument that is not allowed", func(t *testing.T) {
		t.Parallel()

		cfg := testConfig(t)
		cfg.MonerodExtraArgs = "--ok bad;arg"

		_, err := build(context.Background(), cfg, &processtest.Launcher{})

		require.ErrorIs(t, err, commerr.ErrInvalidValue)
		assert.Contains(t, err.Error(), "build monerod arguments")
	})

	t.Run("a wallet directory that cannot exist", func(t *testing.T) {
		t.Parallel()

		cfg := testConfig(t)
		blocker := filepath.Join(t.TempDir(), "file")
		require.NoError(t, os.WriteFile(blocker, nil, 0o600))

		cfg.WalletDir = filepath.Join(blocker, "wallets")

		_, err := build(context.Background(), cfg, &processtest.Launcher{})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "build wallet pool")
	})
}

func TestBuildSuppliesEveryDependencyRunNeeds(t *testing.T) {
	t.Parallel()

	cfg := testConfig(t)
	launcher := newLauncher(
		cfg, newFakeMonerod(t), walletrpctest.NewState(cfg.WalletDir),
	)

	deps, err := build(context.Background(), cfg, launcher)
	require.NoError(t, err)

	assert.NotNil(t, deps.supervisor)
	assert.NotNil(t, deps.pool)
	assert.NotNil(t, deps.router)
	assert.NotNil(t, deps.apiServer)
	assert.NotNil(t, deps.apiRouter)
	assert.NotNil(t, deps.metricsServer)
	assert.NotNil(t, deps.metricsRouter)
	assert.Equal(t, testMaxWallets, deps.pool.MaxOpen())
	assert.Zero(t, deps.supervisor.Restarts())

	_, err = os.Stat(cfg.WalletDir)
	require.NoError(t, err)
}

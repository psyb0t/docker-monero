package moneroserver

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/docker-monero/internal/pkg/config"
	"github.com/psyb0t/docker-monero/internal/pkg/monero/walletrpc/walletrpctest"
	"github.com/psyb0t/docker-monero/internal/pkg/process"
	"github.com/psyb0t/docker-monero/internal/pkg/process/processtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errRunDidNotReturn = errors.New("Run did not return")

func TestName(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "monero-server", (&MoneroServer{}).Name())
	assert.Equal(t, "monero-server", ServiceName)
}

// setEnv exports cfg through the DOCKER_MONERO_* variables New reads.
func setEnv(t *testing.T, cfg config.Config) {
	t.Helper()

	for name, value := range map[string]string{
		config.EnvHTTPListenAddress:    cfg.HTTPListenAddress,
		config.EnvMetricsListenAddress: cfg.MetricsListenAddress,
		config.EnvAPIToken:             cfg.APIToken,
		config.EnvNetwork:              cfg.Network,
		config.EnvMonerodPath:          cfg.MonerodPath,
		config.EnvWalletRPCPath:        cfg.WalletRPCPath,
		config.EnvDataDir:              cfg.DataDir,
		config.EnvWalletDir:            cfg.WalletDir,
		config.EnvRestrictedRPCEnabled: "false",
		config.EnvP2PPort:              strconv.Itoa(cfg.P2PPort),
		config.EnvRPCPort:              strconv.Itoa(cfg.RPCPort),
		config.EnvWalletRPCPortStart:   strconv.Itoa(cfg.WalletRPCPortStart),
		config.EnvMaxOpenWallets:       strconv.Itoa(cfg.MaxOpenWallets),
		config.EnvMonerodStopTimeout:   cfg.MonerodStopTimeout.String(),
		config.EnvWalletStartTimeout:   cfg.WalletStartTimeout.String(),
		config.EnvRPCTimeout:           cfg.RPCTimeout.String(),
		config.EnvMaxRequestBytes:      strconv.Itoa(cfg.MaxRequestBytes),
	} {
		t.Setenv(name, value)
	}
}

func TestNewFailsOnAnInvalidConfiguration(t *testing.T) {
	testCases := []struct {
		name    string
		mod     func(cfg *config.Config)
		wantErr error
	}{
		{
			name:    "no api token",
			mod:     func(cfg *config.Config) { cfg.APIToken = "" },
			wantErr: commerr.ErrRequiredConfigValueNotSet,
		},
		{
			name:    "api token too short",
			mod:     func(cfg *config.Config) { cfg.APIToken = "short" },
			wantErr: commerr.ErrInvalidValue,
		},
		{
			name:    "unknown network",
			mod:     func(cfg *config.Config) { cfg.Network = "moonnet" },
			wantErr: commerr.ErrInvalidValue,
		},
		{
			name: "both listeners on one address",
			mod: func(cfg *config.Config) {
				cfg.MetricsListenAddress = cfg.HTTPListenAddress
			},
			wantErr: commerr.ErrInvalidValue,
		},
		{
			name:    "no wallet slots",
			mod:     func(cfg *config.Config) { cfg.MaxOpenWallets = 0 },
			wantErr: commerr.ErrInvalidValue,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig(t)

			tc.mod(&cfg)
			setEnv(t, cfg)

			service, err := New()

			require.ErrorIs(t, err, tc.wantErr)
			assert.Nil(t, service)
			assert.Contains(t, err.Error(), "parse monero-server config")
		})
	}
}

func TestNewFailsWhenNothingIsConfigured(t *testing.T) {
	t.Setenv(config.EnvAPIToken, "")

	service, err := New()

	require.ErrorIs(t, err, commerr.ErrRequiredConfigValueNotSet)
	assert.Nil(t, service)
}

func TestNewUsesTheProductionLauncher(t *testing.T) {
	setEnv(t, testConfig(t))

	service, err := New()
	require.NoError(t, err)

	assert.Equal(t, "monero-server", service.Name())
	assert.IsType(t, &process.CommanderLauncher{}, service.launcher)
	assert.Equal(t, testAPIToken, service.config.APIToken)
}

// runningService is a MoneroServer started with the fake process seam.
type runningService struct {
	cfg      config.Config
	service  *MoneroServer
	launcher *processtest.Launcher
	wallets  *walletrpctest.State
	monerod  *fakeMonerod

	started atomic.Bool
	done    chan error
	once    sync.Once
	err     error

	eventsMu sync.Mutex
	events   []string
}

func (r *runningService) record(event string) {
	r.eventsMu.Lock()
	defer r.eventsMu.Unlock()

	r.events = append(r.events, event)
}

func (r *runningService) stopOrder() []string {
	r.eventsMu.Lock()
	defer r.eventsMu.Unlock()

	return append([]string(nil), r.events...)
}

func (r *runningService) apiURL() string {
	return "http://" + r.cfg.HTTPListenAddress
}

func (r *runningService) metricsURL() string {
	return "http://" + r.cfg.MetricsListenAddress
}

// newRunningService wires a service to the fake launcher without starting
// it. Every child that stops is recorded, in order.
func newRunningService(t *testing.T, cfg config.Config) *runningService {
	t.Helper()

	current := &runningService{
		cfg:     cfg,
		wallets: walletrpctest.NewState(cfg.WalletDir),
		monerod: newFakeMonerod(t),
		done:    make(chan error, 1),
	}

	walletLauncher := walletrpctest.NewLauncher(current.wallets)
	launchWallet := walletLauncher.OnLaunch

	walletLauncher.OnLaunch = func(launch processtest.Launch) (func(), error) {
		if launch.Name == cfg.MonerodPath {
			stop, err := current.monerod.listen(cfg.RPCPort)
			if err != nil {
				return nil, err
			}

			return func() {
				stop()
				current.record("monerod")
			}, nil
		}

		stop, err := launchWallet(launch)
		if err != nil {
			return nil, err
		}

		return func() {
			stop()
			current.record("wallet-rpc")
		}, nil
	}

	current.launcher = walletLauncher
	current.service = &MoneroServer{
		config:   cfg,
		launcher: walletLauncher,
		stopped:  make(chan struct{}),
	}

	t.Cleanup(func() { _ = current.shutdown() })

	return current
}

// run starts the service in the background.
func (r *runningService) run(ctx context.Context) {
	r.started.Store(true)

	go func() { r.done <- r.service.Run(ctx) }()
}

// awaitReady waits until the API answers its readiness probe.
func (r *runningService) awaitReady(t *testing.T) {
	t.Helper()

	require.Eventually(t, func() bool {
		status, _, err := tryGet(r.apiURL()+"/ready", "")

		return err == nil && status == http.StatusOK
	}, testTimeout, 5*time.Millisecond, "the service never became ready")
}

// start runs the service and waits until it is ready.
func (r *runningService) start(t *testing.T) {
	t.Helper()

	r.run(context.Background())
	r.awaitReady(t)
}

// wait returns what the background Run returned.
func (r *runningService) wait() error {
	r.once.Do(func() {
		select {
		case r.err = <-r.done:
		case <-time.After(2 * testTimeout):
			r.err = errRunDidNotReturn
		}
	})

	return r.err
}

// shutdown asks the service to stop and returns what Run returned.
func (r *runningService) shutdown() error {
	if !r.started.Load() {
		return nil
	}

	if err := r.service.Stop(context.Background()); err != nil {
		return err
	}

	return r.wait()
}

func (r *runningService) requireListenersClosed(t *testing.T) {
	t.Helper()

	for _, address := range []string{
		r.cfg.HTTPListenAddress, r.cfg.MetricsListenAddress,
	} {
		require.Eventually(t, func() bool {
			conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(
				context.Background(), "tcp", address,
			)
			if err != nil {
				return true
			}

			_ = conn.Close()

			return false
		}, testTimeout, 5*time.Millisecond, "%s still accepts", address)
	}
}

func TestRunServesTheServiceAndShutsItDownInOrder(t *testing.T) {
	setEnv(t, testConfig(t))

	service, err := New()
	require.NoError(t, err)

	running := newRunningService(t, service.config)
	running.start(t)

	token := testAPIToken

	t.Run("the probes are open and the API is protected", func(t *testing.T) {
		status, _ := httpGet(t, running.apiURL()+"/healthz", "")
		assert.Equal(t, http.StatusOK, status)

		status, _ = httpGet(t, running.apiURL()+"/v1/node/info", "")
		assert.Equal(t, http.StatusUnauthorized, status)

		status, body := httpGet(t, running.apiURL()+"/v1/node/info", token)
		require.Equal(t, http.StatusOK, status, string(body))
		assert.Contains(t, string(body), `"network":"regtest"`)
		assert.Contains(t, string(body), `"height":1000`)
	})

	t.Run("MCP is mounted on the same listener", func(t *testing.T) {
		names, status := tools(t, running.apiURL(), token)

		require.Equal(t, http.StatusOK, status)
		assert.Contains(t, names, "monero_node_info")
	})

	t.Run("wallets open through the pool", func(t *testing.T) {
		status, body, err := tryRequest(
			http.MethodPost, running.apiURL()+"/v1/wallets",
			`{"name":"alice","password":"pw"}`, token,
		)
		require.NoError(t, err)
		require.Equal(t, http.StatusCreated, status, string(body))
	})

	t.Run("metrics are served on their own listener", func(t *testing.T) {
		status, _ := httpGet(t, running.apiURL()+"/metrics", token)
		assert.Equal(t, http.StatusNotFound, status, "not on the API port")

		status, body := httpGet(t, running.metricsURL()+"/metrics", "")
		require.Equal(t, http.StatusOK, status)

		series := scrape(t, body)

		assert.Equal(t, "1", series["monero_open_wallets"])
		assert.Equal(t, "3", series["monero_max_open_wallets"])
		assert.Equal(t, "1000", series["monero_node_height"])
		assert.Equal(t, "1", series["monero_node_synchronized"])
		assert.Equal(t, "0", series["monero_node_restarts"])
		assert.Equal(t, "1", series[requestSeries(
			http.MethodGet, "GET /v1/node/info", "200",
		)])
		assert.Equal(t, "1", series[requestSeries(
			http.MethodPost, "POST /v1/wallets", "201",
		)])
	})

	require.NoError(t, running.shutdown())

	assert.Equal(t, 1, running.wallets.Called("close_wallet"), "wallet saved")
	assert.Equal(
		t, []string{"wallet-rpc", "monerod"}, running.stopOrder(),
		"wallets stop before monerod gets its SIGTERM",
	)

	for _, proc := range running.launcher.Procs() {
		assert.True(t, proc.Exited())
		assert.Equal(t, 1, proc.Stops())
	}

	running.requireListenersClosed(t)
}

func TestRunStopsWhenItsContextEnds(t *testing.T) {
	cfg := testConfig(t)
	running := newRunningService(t, cfg)

	ctx, cancel := context.WithCancel(context.Background())

	running.run(ctx)
	running.awaitReady(t)

	cancel()

	require.NoError(t, running.wait())
	assert.Equal(t, []string{"monerod"}, running.stopOrder())
	running.requireListenersClosed(t)
}

func TestRunReportsAMonerodThatCannotStart(t *testing.T) {
	cfg := testConfig(t)
	running := newRunningService(t, cfg)
	running.launcher.FailNext(1)

	err := running.service.Run(context.Background())

	require.ErrorIs(t, err, processtest.ErrLaunchRefused)
	assert.Contains(t, err.Error(), "monerod supervisor")
	running.requireListenersClosed(t)
}

func TestRunReportsAListenerThatCannotBind(t *testing.T) {
	testCases := []struct {
		name     string
		occupy   func(cfg config.Config) string
		wantText string
	}{
		{
			name: "api listener",
			occupy: func(cfg config.Config) string {
				return cfg.HTTPListenAddress
			},
			wantText: "api listener failed",
		},
		{
			name: "metrics listener",
			occupy: func(cfg config.Config) string {
				return cfg.MetricsListenAddress
			},
			wantText: "metrics listener failed",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig(t)
			running := newRunningService(t, cfg)

			squatter, err := (&net.ListenConfig{}).Listen(
				context.Background(), "tcp", tc.occupy(cfg),
			)
			require.NoError(t, err)

			t.Cleanup(func() { _ = squatter.Close() })

			running.run(context.Background())

			err = running.wait()

			require.Error(t, err)
			require.NotErrorIs(t, err, errRunDidNotReturn)
			assert.Contains(t, err.Error(), tc.wantText)

			assert.Empty(t, running.wallets.Calls, "no wallet was touched")
		})
	}
}

func TestRunFailsFastOnAnInvalidDeployment(t *testing.T) {
	cfg := testConfig(t)
	cfg.MonerodExtraArgs = "--ok bad;arg"

	running := newRunningService(t, cfg)

	err := running.service.Run(context.Background())

	require.ErrorIs(t, err, commerr.ErrInvalidValue)
	assert.Contains(t, err.Error(), "build monerod arguments")
	assert.Empty(t, running.launcher.Procs(), "nothing was started")

	status, _, dialErr := tryGet(running.apiURL()+"/healthz", "")
	require.Error(t, dialErr, "no listener came up, got status %d", status)
}

func TestRunReportsAMonerodThatDoesNotStopCleanly(t *testing.T) {
	cfg := testConfig(t)
	running := newRunningService(t, cfg)

	running.start(t)

	procs := running.launcher.Procs()
	require.Len(t, procs, 1)

	procs[0].StopErr = assert.AnError

	err := running.shutdown()

	require.ErrorIs(t, err, assert.AnError)
	assert.Contains(t, err.Error(), "stop monerod")
	running.requireListenersClosed(t)
}

func TestStopIsIdempotentAndNeverBlocks(t *testing.T) {
	t.Parallel()

	service := &MoneroServer{stopped: make(chan struct{})}

	require.NoError(t, service.Stop(context.Background()))
	require.NoError(t, service.Stop(context.Background()))

	select {
	case <-service.stopped:
	default:
		require.Fail(t, "Stop did not signal Run")
	}
}

func TestAStoppedServiceStartsAndStopsCleanly(t *testing.T) {
	cfg := testConfig(t)
	running := newRunningService(t, cfg)

	require.NoError(t, running.service.Stop(context.Background()))

	running.run(context.Background())

	require.NoError(t, running.wait())
	running.requireListenersClosed(t)
}

func TestWaitForStop(t *testing.T) {
	t.Parallel()

	t.Run("context ends", func(t *testing.T) {
		t.Parallel()

		service := &MoneroServer{stopped: make(chan struct{})}
		ctx, cancel := context.WithCancel(context.Background())

		cancel()

		require.NoError(t, service.waitForStop(ctx, make(chan error)))
	})

	t.Run("stop requested", func(t *testing.T) {
		t.Parallel()

		service := &MoneroServer{stopped: make(chan struct{})}

		require.NoError(t, service.Stop(context.Background()))
		require.NoError(t, service.waitForStop(
			context.Background(), make(chan error),
		))
	})

	t.Run("component failed", func(t *testing.T) {
		t.Parallel()

		service := &MoneroServer{stopped: make(chan struct{})}

		failures := make(chan error, 1)
		failures <- assert.AnError

		require.ErrorIs(
			t, service.waitForStop(context.Background(), failures),
			assert.AnError,
		)
	})
}

// requestSeries is the exposition name of one request counter series.
func requestSeries(method, route, status string) string {
	return `monero_api_http_requests_total{method="` + method +
		`",route="` + route + `",status="` + status + `"}`
}

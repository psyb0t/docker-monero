package moneroserver

import (
	"context"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/aichteeteapee/serbewr"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/docker-monero/internal/pkg/config"
	corenode "github.com/psyb0t/docker-monero/internal/pkg/core/node"
	corewallets "github.com/psyb0t/docker-monero/internal/pkg/core/wallets"
	"github.com/psyb0t/docker-monero/internal/pkg/http/server"
	"github.com/psyb0t/docker-monero/internal/pkg/mcp"
	"github.com/psyb0t/docker-monero/internal/pkg/metrics"
	"github.com/psyb0t/docker-monero/internal/pkg/monero/daemonrpc"
	"github.com/psyb0t/docker-monero/internal/pkg/monero/jsonrpc"
	"github.com/psyb0t/docker-monero/internal/pkg/node"
	"github.com/psyb0t/docker-monero/internal/pkg/process"
	"github.com/psyb0t/docker-monero/internal/pkg/walletpool"
)

const (
	metricsPath = "/metrics"

	// gaugeTimeout bounds the node calls a scrape makes.
	gaugeTimeout = 2 * time.Second

	// writeTimeoutSlack covers encoding and the request's own overhead on
	// top of the slowest wallet operation.
	writeTimeoutSlack = 15 * time.Second

	listenerAPI     = "api"
	listenerMetrics = "metrics"
)

// dependencies is everything Run starts and shutdown stops.
type dependencies struct {
	supervisor    *node.Supervisor
	pool          *walletpool.Pool
	router        *server.Router
	apiServer     *serbewr.Server
	apiRouter     *serbewr.Router
	metricsServer *serbewr.Server
	metricsRouter *serbewr.Router
}

// daemonProber adapts the daemon client to the supervisor's readiness probe.
type daemonProber struct {
	daemon *daemonrpc.Client
}

func (p daemonProber) Probe(ctx context.Context) error {
	_, err := p.daemon.GetVersion(ctx)

	return err //nolint:wrapcheck // the supervisor only checks for nil
}

func build(
	ctx context.Context,
	cfg config.Config,
	launcher process.Launcher,
) (*dependencies, error) {
	monerodArgs, err := node.Args(cfg)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "build monerod arguments")
	}

	daemon := daemonrpc.New(
		cfg.MonerodRPCURL(), cfg.RPCTimeout, jsonrpc.WithLogin(cfg.RPCLogin),
	)

	supervisor := node.New(node.Settings{
		Path:        cfg.MonerodPath,
		Args:        monerodArgs,
		Env:         node.Environment(cfg),
		StopTimeout: cfg.MonerodStopTimeout,
	}, launcher, daemonProber{daemon: daemon})

	pool, err := walletpool.New(walletpool.Settings{
		WalletRPCPath: cfg.WalletRPCPath,
		WalletDir:     cfg.WalletDir,
		DaemonAddress: cfg.MonerodRPCAddress(),
		RPCLogin:      cfg.RPCLogin,
		Network:       cfg.NetworkType(),
		PortStart:     cfg.WalletRPCPortStart,
		MaxOpen:       cfg.MaxOpenWallets,
		StartTimeout:  cfg.WalletStartTimeout,
		RPCTimeout:    cfg.RPCTimeout,
	}, launcher)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "build wallet pool")
	}

	recorder := metrics.New(gauges(ctx, daemon, pool, supervisor, cfg))

	router, err := buildRouter(cfg, daemon, pool, supervisor, recorder)
	if err != nil {
		return nil, err
	}

	return buildListeners(cfg, router, recorder, supervisor, pool)
}

// buildRouter builds the core services and the transports over them.
func buildRouter(
	cfg config.Config,
	daemon *daemonrpc.Client,
	pool *walletpool.Pool,
	supervisor *node.Supervisor,
	recorder *metrics.Metrics,
) (*server.Router, error) {
	nodeService := corenode.New(daemon, corenode.Options{
		Network:  cfg.NetworkType(),
		ReadOnly: cfg.ReadOnly,
		Restarts: supervisor.Restarts,
	})

	walletService := corewallets.New(pool, daemon, corewallets.Options{
		ReadOnly:       cfg.ReadOnly,
		AllowTransfers: cfg.AllowTransfers,
	})

	mcpHandler, err := mcp.Handler(
		nodeService, walletService, recorder, mcp.Options{
			ReadOnly:        cfg.ReadOnly,
			AllowTransfers:  cfg.AllowTransfers && cfg.MCPAllowTransfers,
			Regtest:         cfg.NetworkType() == config.NetworkRegtest,
			MaxRequestBytes: int64(cfg.MaxRequestBytes),
		},
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "build mcp handler")
	}

	return server.NewRouter(server.RouterConfig{
		APIToken:        cfg.APIToken,
		MaxRequestBytes: int64(cfg.MaxRequestBytes),
		Server:          server.New(nodeService, walletService),
		MCPHandler:      mcpHandler,
		Metrics:         recorder,
		Ready: func(ctx context.Context) bool {
			_, err := daemon.GetVersion(ctx)

			return err == nil
		},
	}), nil
}

// buildListeners builds the public and internal serbewr servers.
func buildListeners(
	cfg config.Config,
	router *server.Router,
	recorder *metrics.Metrics,
	supervisor *node.Supervisor,
	pool *walletpool.Pool,
) (*dependencies, error) {
	writeTimeout := cfg.WalletStartTimeout + cfg.RPCTimeout + writeTimeoutSlack

	apiServer, err := serbewr.NewWithConfig(serverConfig(
		cfg.HTTPListenAddress, listenerAPI, writeTimeout,
	))
	if err != nil {
		return nil, ctxerrors.Wrap(err, "build api listener")
	}

	metricsServer, err := serbewr.NewWithConfig(serverConfig(
		cfg.MetricsListenAddress, listenerMetrics,
		aichteeteapee.DefaultHTTPServerWriteTimeout,
	))
	if err != nil {
		return nil, ctxerrors.Wrap(err, "build metrics listener")
	}

	return &dependencies{
		supervisor:    supervisor,
		pool:          pool,
		router:        router,
		apiServer:     apiServer,
		apiRouter:     router.Serbewr(),
		metricsServer: metricsServer,
		metricsRouter: metricsRouter(recorder),
	}, nil
}

// serverConfig builds the serbewr config directly instead of from its
// HTTP_SERVER_* variables, so the DOCKER_MONERO_* addresses are the only
// knobs and the two listeners cannot pick up each other's settings.
func serverConfig(
	address, name string,
	writeTimeout time.Duration,
) serbewr.Config {
	return serbewr.Config{
		ListenAddress:       address,
		ReadTimeout:         aichteeteapee.DefaultHTTPServerReadTimeout,
		ReadHeaderTimeout:   aichteeteapee.DefaultHTTPServerReadHeaderTimeout,
		WriteTimeout:        writeTimeout,
		IdleTimeout:         aichteeteapee.DefaultHTTPServerIdleTimeout,
		MaxHeaderBytes:      aichteeteapee.DefaultHTTPServerMaxHeaderBytes,
		ShutdownTimeout:     aichteeteapee.DefaultHTTPServerShutdownTimeout,
		ServiceName:         name,
		FileUploadMaxMemory: aichteeteapee.DefaultFileUploadMaxMemory,
	}
}

func metricsRouter(recorder *metrics.Metrics) *serbewr.Router {
	handler := promhttp.HandlerFor(recorder.Registry(), promhttp.HandlerOpts{
		// A collector error must not take the scrape endpoint down.
		ErrorHandling: promhttp.ContinueOnError,
	})

	return &serbewr.Router{
		Groups: []serbewr.GroupConfig{{
			Routes: []serbewr.RouteConfig{
				{
					Method:  http.MethodGet,
					Path:    metricsPath,
					Handler: handler.ServeHTTP,
				},
			},
		}},
	}
}

func gauges(
	ctx context.Context,
	daemon *daemonrpc.Client,
	pool *walletpool.Pool,
	supervisor *node.Supervisor,
	cfg config.Config,
) metrics.Gauges {
	// Scrapes outlive any one request, so they hang off the service context
	// without its cancellation and carry their own timeout.
	base := context.WithoutCancel(ctx)

	info := func() (daemonrpc.Info, bool) {
		scrapeCtx, cancel := context.WithTimeout(base, gaugeTimeout)
		defer cancel()

		result, err := daemon.GetInfo(scrapeCtx)

		return result, err == nil
	}

	return metrics.Gauges{
		OpenWallets: func() float64 { return float64(pool.OpenCount()) },
		NodeRestarts: func() float64 {
			return float64(supervisor.Restarts())
		},
		NodeHeight: func() float64 {
			result, ok := info()
			if !ok {
				return 0
			}

			return float64(result.Height)
		},
		NodeSynced: func() float64 {
			result, ok := info()
			if !ok || !result.Synchronized {
				return 0
			}

			return 1
		},
		MaxOpenWallets: float64(cfg.MaxOpenWallets),
	}
}

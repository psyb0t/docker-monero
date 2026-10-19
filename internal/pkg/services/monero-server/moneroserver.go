// Package moneroserver is docker-monero's runnable service.
//
// It owns startup and shutdown order and nothing else. Startup: supervise
// monerod, build the wallet pool, and serve REST and MCP on the public
// listener with Prometheus on the internal one. Shutdown runs in reverse:
// fail readiness, drain the listeners, save and close every wallet, then
// give monerod its SIGTERM and time to flush.
package moneroserver

import (
	"context"
	"errors"
	"sync"

	"github.com/psyb0t/aichteeteapee/serbewr"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/docker-monero/internal/pkg/config"
	"github.com/psyb0t/docker-monero/internal/pkg/process"
)

// ServiceName is the registry key.
const ServiceName = "monero-server"

const scopeKeyService = "service"

// MoneroServer runs the node, the wallet pool, and the API.
type MoneroServer struct {
	config   config.Config
	launcher process.Launcher

	stopped  chan struct{}
	stopOnce sync.Once
}

// New parses and validates the configuration, so a bad value fails at
// startup instead of on the first request.
func New() (*MoneroServer, error) {
	cfg, err := config.Parse()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "parse monero-server config")
	}

	return &MoneroServer{
		config:   cfg,
		launcher: process.NewCommanderLauncher(),
		stopped:  make(chan struct{}),
	}, nil
}

// Name returns the registry key.
func (s *MoneroServer) Name() string {
	return ServiceName
}

// Run brings everything up and blocks until a stop is requested, the
// context ends, or a component fails.
func (s *MoneroServer) Run(ctx context.Context) error {
	ctx = ctxscope.Set(ctx, ctxscope.Attr(scopeKeyService, ServiceName))
	logger := ctxscope.GetLogger(ctx)
	logger.Info("starting service", "network", s.config.Network)

	deps, err := build(ctx, s.config, s.launcher)
	if err != nil {
		return err
	}

	serveCtx, stopServing := context.WithCancel(context.WithoutCancel(ctx))
	defer stopServing()

	errs := make(chan error, 3) //nolint:mnd // supervisor plus two listeners

	var running sync.WaitGroup

	running.Go(func() {
		if err := deps.supervisor.Run(serveCtx); err != nil {
			errs <- ctxerrors.Wrap(err, "monerod supervisor")
		}
	})

	running.Go(func() {
		serve(serveCtx, "api", deps.apiServer, deps.apiRouter, errs)
	})

	running.Go(func() {
		serve(serveCtx, "metrics", deps.metricsServer, deps.metricsRouter, errs)
	})

	logger.Info(
		"service ready",
		"api",
		s.config.HTTPListenAddress,
		"metrics",
		s.config.MetricsListenAddress,
	)

	runErr := s.waitForStop(ctx, errs)

	shutdownErr := shutdown(ctx, deps)

	stopServing()
	running.Wait()

	logger.Info("service stopped")

	return errors.Join(runErr, shutdownErr)
}

// waitForStop blocks until shutdown is requested or a component fails.
func (s *MoneroServer) waitForStop(
	ctx context.Context,
	errs <-chan error,
) error {
	logger := ctxscope.GetLogger(ctx)

	select {
	case <-ctx.Done():
		logger.Info("context cancelled, stopping service")

		return nil
	case <-s.stopped:
		logger.Info("stop requested, stopping service")

		return nil
	case err := <-errs:
		logger.Error("component failed, stopping service", "err", err)

		return err
	}
}

// shutdown stops the components in dependency order.
func shutdown(ctx context.Context, deps *dependencies) error {
	logger := ctxscope.GetLogger(ctx)
	stopCtx := context.WithoutCancel(ctx)

	deps.router.BeginShutdown()

	var errs []error

	if err := deps.apiServer.Stop(stopCtx); err != nil {
		errs = append(errs, ctxerrors.Wrap(err, "stop api listener"))
	}

	logger.Info("closing wallets", "open", deps.pool.OpenCount())
	deps.pool.CloseAll(stopCtx)

	logger.Info("stopping monerod")

	if err := deps.supervisor.Stop(stopCtx); err != nil {
		errs = append(errs, err)
	}

	if err := deps.metricsServer.Stop(stopCtx); err != nil {
		errs = append(errs, ctxerrors.Wrap(err, "stop metrics listener"))
	}

	return errors.Join(errs...)
}

// serve runs one serbewr listener until ctx ends. A listener that fails
// while the service should be running is reported.
func serve(
	ctx context.Context,
	label string,
	server *serbewr.Server,
	router *serbewr.Router,
	errs chan<- error,
) {
	err := server.Start(ctx, router)
	if err == nil || errors.Is(err, context.Canceled) || ctx.Err() != nil {
		return
	}

	errs <- ctxerrors.Wrapf(err, "%s listener failed", label)
}

// Stop asks Run to shut down. It never blocks on the drain and is safe to
// call more than once.
func (s *MoneroServer) Stop(ctx context.Context) error {
	ctx = ctxscope.Set(ctx, ctxscope.Attr(scopeKeyService, ServiceName))
	ctxscope.GetLogger(ctx).Info("stopping service")

	s.stopOnce.Do(func() { close(s.stopped) })

	return nil
}

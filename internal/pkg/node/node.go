// Package node supervises the monerod child process.
//
// The supervisor starts monerod, restarts it with backoff when it dies
// unexpectedly, reports readiness once its RPC answers, and stops it with
// SIGTERM and a generous deadline so the database is flushed cleanly.
package node

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/docker-monero/internal/pkg/process"
)

// Restart and probe timing.
const (
	DefaultRestartDelay    = 2 * time.Second
	DefaultMaxRestartDelay = time.Minute
	DefaultProbeInterval   = time.Second
	DefaultProbeTimeout    = 5 * time.Second

	// backoffFactor doubles the restart delay after each crash.
	backoffFactor = 2

	// stableRunDuration resets the restart backoff. A daemon that ran this
	// long before dying is treated as a fresh failure, not a crash loop.
	stableRunDuration = 10 * time.Minute

	// outputComponent labels monerod's forwarded output lines.
	outputComponent = "monerod"
	scopeKeyChild   = "child"
)

// Prober reports whether monerod's RPC currently answers.
type Prober interface {
	Probe(ctx context.Context) error
}

// Settings configures one supervisor.
type Settings struct {
	Path            string
	Args            []string
	Env             []string
	StopTimeout     time.Duration
	RestartDelay    time.Duration
	MaxRestartDelay time.Duration
	ProbeInterval   time.Duration
	ProbeTimeout    time.Duration
}

// Supervisor runs monerod for the lifetime of the service.
type Supervisor struct {
	settings Settings
	launcher process.Launcher
	prober   Prober

	ready     chan struct{}
	readyOnce sync.Once

	stopping atomic.Bool
	stopped  chan struct{}
	stopOnce sync.Once

	mu      sync.Mutex
	current process.Process

	restarts atomic.Uint64
}

// New builds a supervisor. Zero timing values fall back to the defaults.
func New(
	settings Settings,
	launcher process.Launcher,
	prober Prober,
) *Supervisor {
	if settings.RestartDelay <= 0 {
		settings.RestartDelay = DefaultRestartDelay
	}

	if settings.MaxRestartDelay <= 0 {
		settings.MaxRestartDelay = DefaultMaxRestartDelay
	}

	if settings.ProbeInterval <= 0 {
		settings.ProbeInterval = DefaultProbeInterval
	}

	if settings.ProbeTimeout <= 0 {
		settings.ProbeTimeout = DefaultProbeTimeout
	}

	return &Supervisor{
		settings: settings,
		launcher: launcher,
		prober:   prober,
		ready:    make(chan struct{}),
		stopped:  make(chan struct{}),
	}
}

// Ready closes the first time monerod's RPC answers.
func (s *Supervisor) Ready() <-chan struct{} {
	return s.ready
}

// Restarts counts unexpected exits that were followed by a restart.
func (s *Supervisor) Restarts() uint64 {
	return s.restarts.Load()
}

// Run starts monerod and keeps it running until ctx ends or Stop is called.
// It returns an error only when monerod cannot be started at all.
func (s *Supervisor) Run(ctx context.Context) error {
	ctx = ctxscope.Set(ctx, ctxscope.Attr(scopeKeyChild, outputComponent))
	logger := ctxscope.GetLogger(ctx)
	delay := s.settings.RestartDelay

	for {
		startedAt := time.Now()

		err := s.runOnce(ctx, logger)
		if err != nil {
			return err
		}

		if s.isStopping(ctx) {
			return nil
		}

		if time.Since(startedAt) >= stableRunDuration {
			delay = s.settings.RestartDelay
		}

		s.restarts.Add(1)
		logger.Error("monerod exited unexpectedly, restarting", "delay", delay)

		if !s.sleep(ctx, delay) {
			return nil
		}

		delay = min(delay*backoffFactor, s.settings.MaxRestartDelay)
	}
}

// runOnce starts one monerod process and waits for it to exit. A start
// failure is returned; an exit is logged and reported as nil.
func (s *Supervisor) runOnce(ctx context.Context, logger *slog.Logger) error {
	if s.isStopping(ctx) {
		return nil
	}

	child, err := s.launcher.Launch(
		ctx, s.settings.Path, s.settings.Args,
		logger.With("stream", outputComponent),
		s.settings.Env...,
	)
	if err != nil {
		return ctxerrors.Wrap(err, "launch monerod")
	}

	s.setCurrent(child)
	defer s.setCurrent(nil)

	logger.Info("monerod started", "pid", child.PID())

	// Stop may have run between the check above and setCurrent, in which
	// case it found no child to stop. Stop this one now instead.
	if s.isStopping(ctx) {
		if err := s.Stop(ctx); err != nil {
			logger.Error("stop monerod started during shutdown", "err", err)
		}
	}

	probeCtx, cancelProbe := context.WithCancel(ctx)
	defer cancelProbe()

	go s.probeUntilReady(probeCtx, logger)

	exited := make(chan struct{})
	defer close(exited)

	go s.stopOnCancel(ctx, exited, logger)

	waitErr := child.Wait()
	if waitErr != nil && !s.isStopping(ctx) {
		logger.Error("monerod exited", "err", waitErr)
	}

	return nil
}

// stopOnCancel stops monerod when ctx ends before it exits on its own. The
// child is detached from ctx, so without this a cancelled supervisor would
// wait on a daemon nobody told to stop.
func (s *Supervisor) stopOnCancel(
	ctx context.Context,
	exited <-chan struct{},
	logger *slog.Logger,
) {
	select {
	case <-exited:
	case <-ctx.Done():
		if err := s.Stop(ctx); err != nil {
			logger.Error("stop monerod on cancellation", "err", err)
		}
	}
}

// probeUntilReady polls the RPC until it answers once, then closes Ready.
func (s *Supervisor) probeUntilReady(ctx context.Context, logger *slog.Logger) {
	ticker := time.NewTicker(s.settings.ProbeInterval)
	defer ticker.Stop()

	for {
		if s.probe(ctx) {
			s.readyOnce.Do(func() {
				logger.Info("monerod rpc is answering")
				close(s.ready)
			})

			return
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Supervisor) probe(ctx context.Context) bool {
	probeCtx, cancel := context.WithTimeout(ctx, s.settings.ProbeTimeout)
	defer cancel()

	return s.prober.Probe(probeCtx) == nil
}

// Stop sends SIGTERM to monerod and waits up to the stop timeout for it to
// flush and exit. It is safe to call more than once.
func (s *Supervisor) Stop(ctx context.Context) error {
	s.stopping.Store(true)
	s.stopOnce.Do(func() { close(s.stopped) })

	child := s.getCurrent()
	if child == nil {
		return nil
	}

	stopCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx), s.settings.StopTimeout,
	)
	defer cancel()

	if err := child.Stop(stopCtx); err != nil {
		return ctxerrors.Wrap(err, "stop monerod")
	}

	return nil
}

func (s *Supervisor) isStopping(ctx context.Context) bool {
	return s.stopping.Load() || ctx.Err() != nil
}

// sleep waits for delay and reports false when the supervisor was asked to
// stop meanwhile.
func (s *Supervisor) sleep(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-timer.C:
		return !s.isStopping(ctx)
	case <-ctx.Done():
		return false
	case <-s.stopped:
		return false
	}
}

func (s *Supervisor) setCurrent(child process.Process) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.current = child
}

func (s *Supervisor) getCurrent() process.Process {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.current
}

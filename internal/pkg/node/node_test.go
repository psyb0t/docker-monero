package node

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/psyb0t/docker-monero/internal/pkg/config"
	"github.com/psyb0t/docker-monero/internal/pkg/process/processtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errNotYet = errors.New("not yet")

type fakeProber struct {
	answer atomic.Bool
}

func (p *fakeProber) Probe(_ context.Context) error {
	if p.answer.Load() {
		return nil
	}

	return errNotYet
}

func fastSettings() Settings {
	return Settings{
		Path:            "monerod",
		Args:            []string{"--x"},
		Env:             []string{"RPC_LOGIN=example:EXAMPLE-DO-NOT-USE"},
		StopTimeout:     time.Second,
		RestartDelay:    5 * time.Millisecond,
		MaxRestartDelay: 20 * time.Millisecond,
		ProbeInterval:   5 * time.Millisecond,
		ProbeTimeout:    time.Second,
	}
}

func runAsync(
	ctx context.Context,
	t *testing.T,
	supervisor *Supervisor,
) <-chan error {
	t.Helper()

	done := make(chan error, 1)

	go func() { done <- supervisor.Run(ctx) }()

	return done
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	require.Eventually(t, condition, 5*time.Second, 2*time.Millisecond)
}

func TestNewAppliesDefaults(t *testing.T) {
	supervisor := New(Settings{}, &processtest.Launcher{}, &fakeProber{})

	assert.Equal(t, DefaultRestartDelay, supervisor.settings.RestartDelay)
	assert.Equal(t, DefaultMaxRestartDelay, supervisor.settings.MaxRestartDelay)
	assert.Equal(t, DefaultProbeInterval, supervisor.settings.ProbeInterval)
	assert.Equal(t, DefaultProbeTimeout, supervisor.settings.ProbeTimeout)
}

func TestReadyAndStop(t *testing.T) {
	launcher := &processtest.Launcher{}
	prober := &fakeProber{}
	supervisor := New(fastSettings(), launcher, prober)

	done := runAsync(context.Background(), t, supervisor)

	waitFor(t, func() bool { return launcher.Last() != nil })
	assert.Equal(t, "monerod", launcher.Last().Launched().Name)
	assert.Equal(t, []string{"--x"}, launcher.Last().Launched().Args)
	assert.Equal(t, fastSettings().Env, launcher.Last().Launched().Env)

	select {
	case <-supervisor.Ready():
		t.Fatal("ready before the rpc answered")
	case <-time.After(20 * time.Millisecond):
	}

	prober.answer.Store(true)

	select {
	case <-supervisor.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("never became ready")
	}

	require.NoError(t, supervisor.Stop(context.Background()))
	require.NoError(t, <-done)
	assert.Equal(t, 1, launcher.Last().Stops())
	assert.Len(t, launcher.Procs(), 1)
	assert.Zero(t, supervisor.Restarts())

	// A second Stop is harmless.
	require.NoError(t, supervisor.Stop(context.Background()))
}

func TestRestartsAfterCrash(t *testing.T) {
	launcher := &processtest.Launcher{}
	supervisor := New(fastSettings(), launcher, &fakeProber{})

	done := runAsync(context.Background(), t, supervisor)

	waitFor(t, func() bool { return launcher.Last() != nil })
	launcher.Last().Exit(errors.New("segfault"))

	waitFor(t, func() bool { return len(launcher.Procs()) == 2 })
	launcher.Last().Exit(nil)

	waitFor(t, func() bool { return len(launcher.Procs()) == 3 })
	assert.Equal(t, uint64(2), supervisor.Restarts())

	require.NoError(t, supervisor.Stop(context.Background()))
	require.NoError(t, <-done)
}

func TestLaunchFailureIsFatal(t *testing.T) {
	launcher := &processtest.Launcher{}
	launcher.FailNext(1)

	supervisor := New(fastSettings(), launcher, &fakeProber{})

	err := supervisor.Run(context.Background())
	require.ErrorIs(t, err, processtest.ErrLaunchRefused)
}

func TestContextCancelStopsChild(t *testing.T) {
	launcher := &processtest.Launcher{}
	supervisor := New(fastSettings(), launcher, &fakeProber{})

	ctx, cancel := context.WithCancel(context.Background())
	done := runAsync(ctx, t, supervisor)

	waitFor(t, func() bool { return launcher.Last() != nil })
	cancel()

	require.NoError(t, <-done)
	assert.True(t, launcher.Last().Exited())
}

func TestStopDuringBackoff(t *testing.T) {
	launcher := &processtest.Launcher{}
	settings := fastSettings()
	settings.RestartDelay = time.Hour
	settings.MaxRestartDelay = time.Hour

	supervisor := New(settings, launcher, &fakeProber{})
	done := runAsync(context.Background(), t, supervisor)

	waitFor(t, func() bool { return launcher.Last() != nil })
	launcher.Last().Exit(errors.New("crash"))

	waitFor(t, func() bool { return supervisor.Restarts() == 1 })
	require.NoError(t, supervisor.Stop(context.Background()))
	require.NoError(t, <-done)
	assert.Len(t, launcher.Procs(), 1)
}

func TestCancelDuringBackoff(t *testing.T) {
	launcher := &processtest.Launcher{}
	settings := fastSettings()
	settings.RestartDelay = time.Hour

	supervisor := New(settings, launcher, &fakeProber{})

	ctx, cancel := context.WithCancel(context.Background())
	done := runAsync(ctx, t, supervisor)

	waitFor(t, func() bool { return launcher.Last() != nil })
	launcher.Last().Exit(errors.New("crash"))
	waitFor(t, func() bool { return supervisor.Restarts() == 1 })

	cancel()
	require.NoError(t, <-done)
}

func TestStopBeforeRun(t *testing.T) {
	launcher := &processtest.Launcher{}
	supervisor := New(fastSettings(), launcher, &fakeProber{})

	require.NoError(t, supervisor.Stop(context.Background()))
	require.NoError(t, supervisor.Run(context.Background()))
	assert.Empty(t, launcher.Procs())
}

func TestStopError(t *testing.T) {
	launcher := &processtest.Launcher{}
	supervisor := New(fastSettings(), launcher, &fakeProber{})

	done := runAsync(context.Background(), t, supervisor)

	waitFor(t, func() bool { return launcher.Last() != nil })

	launcher.Last().StopErr = errors.New("kill failed")

	require.Error(t, supervisor.Stop(context.Background()))
	require.NoError(t, <-done)
}

func baseConfig() config.Config {
	return config.Config{
		Network:              string(config.NetworkMainnet),
		DataDir:              "/data/monerod",
		P2PPort:              18080,
		RPCPort:              18081,
		RestrictedRPCEnabled: true,
		RestrictedRPCPort:    18089,
		Prune:                true,
	}
}

func TestArgsMainnet(t *testing.T) {
	args, err := Args(baseConfig())
	require.NoError(t, err)

	assert.Subset(t, args, []string{
		"--non-interactive",
		"--data-dir", "/data/monerod",
		"--rpc-bind-ip", "127.0.0.1",
		"--rpc-bind-port", "18081",
		"--rpc-restricted-bind-ip", "0.0.0.0",
		"--rpc-restricted-bind-port", "18089",
		"--prune-blockchain",
		"--enable-dns-blocklist",
		"--no-igd",
	})
	assert.NotContains(t, args, "--public-node")
	assert.NotContains(t, args, "--confirm-external-bind")
}

func TestArgsVariants(t *testing.T) {
	cfg := baseConfig()
	cfg.PublicNode = true
	cfg.Prune = false
	cfg.MonerodExtraArgs = "--out-peers=16 --log-level 1"

	args, err := Args(cfg)
	require.NoError(t, err)
	assert.Contains(t, args, "--public-node")
	assert.NotContains(t, args, "--prune-blockchain")
	assert.Equal(t,
		[]string{"--out-peers=16", "--log-level", "1"}, args[len(args)-3:],
	)

	cfg.RestrictedRPCEnabled = false

	args, err = Args(cfg)
	require.NoError(t, err)
	assert.NotContains(t, args, "--rpc-restricted-bind-port")
	assert.NotContains(t, args, "--public-node")
}

func TestArgsNetworks(t *testing.T) {
	tests := map[config.Network][]string{
		config.NetworkStagenet: {"--stagenet"},
		config.NetworkTestnet:  {"--testnet"},
		config.NetworkRegtest: {
			"--regtest", "--offline", "--keep-fakechain", "--fixed-difficulty",
		},
		config.NetworkMainnet: {"--enable-dns-blocklist"},
	}

	for network, want := range tests {
		t.Run(string(network), func(t *testing.T) {
			cfg := baseConfig()
			cfg.Network = string(network)

			args, err := Args(cfg)
			require.NoError(t, err)
			assert.Subset(t, args, want)
		})
	}

	assert.Nil(t, networkArgs(config.Network("bogus")))
}

func TestArgsRejectsBadExtra(t *testing.T) {
	cfg := baseConfig()
	cfg.MonerodExtraArgs = "--log-file=$(id)"

	_, err := Args(cfg)
	require.Error(t, err)
}

func TestRPCLoginIsOnlyInEnvironment(t *testing.T) {
	t.Parallel()

	cfg := baseConfig()
	assert.Empty(t, Environment(cfg))

	cfg.RPCLogin = "example:EXAMPLE-DO-NOT-USE"
	assert.Equal(t, []string{"RPC_LOGIN=" + cfg.RPCLogin}, Environment(cfg))

	args, err := Args(cfg)
	require.NoError(t, err)

	for _, arg := range args {
		assert.NotContains(t, arg, cfg.RPCLogin)
		assert.NotContains(t, arg, "EXAMPLE-DO-NOT-USE")
	}
}

package walletpool

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/psyb0t/docker-monero/internal/pkg/config"
	"github.com/psyb0t/docker-monero/internal/pkg/monero/walletrpc"
	"github.com/psyb0t/docker-monero/internal/pkg/monero/walletrpc/walletrpctest"
	"github.com/psyb0t/docker-monero/internal/pkg/process/processtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testMaxOpen = 3

type harness struct {
	pool     *Pool
	state    *walletrpctest.State
	launcher *processtest.Launcher
	dir      string
}

func newHarness(t *testing.T, network config.Network) *harness {
	t.Helper()

	dir := t.TempDir()
	state := walletrpctest.NewState(dir)
	launcher := walletrpctest.NewLauncher(state)

	portStart, err := walletrpctest.FreePortRange(testMaxOpen)
	require.NoError(t, err)

	pool, err := New(Settings{
		WalletRPCPath: "monero-wallet-rpc",
		WalletDir:     dir,
		DaemonAddress: "127.0.0.1:18081",
		Network:       network,
		PortStart:     portStart,
		MaxOpen:       testMaxOpen,
		StartTimeout:  5 * time.Second,
		RPCTimeout:    5 * time.Second,
	}, launcher)
	require.NoError(t, err)

	t.Cleanup(func() { pool.CloseAll(context.Background()) })

	return &harness{pool: pool, state: state, launcher: launcher, dir: dir}
}

func TestValidateName(t *testing.T) {
	for _, good := range []string{"a", "main", "Wallet_1", "x-y", "9"} {
		require.NoError(t, ValidateName(good), good)
	}

	for _, bad := range []string{
		"", ".hidden", "-x", "a/b", "../x", "a b", string(make([]byte, 65)),
	} {
		require.ErrorIs(t, ValidateName(bad), ErrInvalidName, bad)
	}
}

func TestNewCreatesDirectoryAndDefaults(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "wallets")

	pool, err := New(
		Settings{WalletDir: dir, MaxOpen: 1}, &processtest.Launcher{},
	)
	require.NoError(t, err)

	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
	assert.Equal(t, DefaultStopTimeout, pool.settings.StopTimeout)
	assert.Equal(t, DefaultAutoRefreshPeriod, pool.settings.AutoRefreshPeriod)
	assert.Equal(t, 1, pool.MaxOpen())
}

func TestNewFailsOnUnwritableDirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(file, nil, 0o600))

	_, err := New(
		Settings{WalletDir: filepath.Join(file, "x")}, &processtest.Launcher{},
	)
	require.Error(t, err)
}

func TestCreateOpenCloseLifecycle(t *testing.T) {
	h := newHarness(t, config.NetworkMainnet)
	ctx := context.Background()

	wallet, err := h.pool.Create(ctx, "alice", "pw", "")
	require.NoError(t, err)
	assert.Equal(t, "alice", wallet.Name)
	assert.Positive(t, wallet.PID())
	assert.Equal(t, 1, h.pool.OpenCount())
	assert.Equal(t, 1, h.state.Called("auto_refresh"))

	got, err := h.pool.Get("alice")
	require.NoError(t, err)
	assert.Same(t, wallet, got)

	_, err = h.pool.Create(ctx, "alice", "pw", "")
	require.ErrorIs(t, err, ErrWalletExists)

	_, err = h.pool.Open(ctx, "alice", "pw")
	require.ErrorIs(t, err, ErrWalletAlreadyOpen)

	entries, err := h.pool.List()
	require.NoError(t, err)
	assert.Equal(t, []Entry{{Name: "alice", Open: true}}, entries)

	require.NoError(t, h.pool.Close(ctx, "alice"))
	assert.Equal(t, 0, h.pool.OpenCount())
	assert.True(t, h.launcher.Last().Exited())

	_, err = h.pool.Get("alice")
	require.ErrorIs(t, err, ErrWalletNotOpen)

	require.ErrorIs(t, h.pool.Close(ctx, "alice"), ErrWalletNotOpen)

	entries, err = h.pool.List()
	require.NoError(t, err)
	assert.Equal(t, []Entry{{Name: "alice", Open: false}}, entries)

	_, err = h.pool.Open(ctx, "alice", "wrong")
	require.ErrorIs(t, err, ErrInvalidPassword)
	assert.Equal(t, 0, h.pool.OpenCount())

	wallet, err = h.pool.Open(ctx, "alice", "pw")
	require.NoError(t, err)
	assert.Equal(t, "alice", wallet.Name)
}

func TestOpenMissingWallet(t *testing.T) {
	h := newHarness(t, config.NetworkMainnet)

	_, err := h.pool.Open(context.Background(), "ghost", "pw")
	require.ErrorIs(t, err, ErrWalletNotFound)
	assert.Empty(t, h.launcher.Procs())

	_, err = h.pool.Open(context.Background(), "../ghost", "pw")
	require.ErrorIs(t, err, ErrInvalidName)
}

func TestRestore(t *testing.T) {
	h := newHarness(t, config.NetworkMainnet)
	ctx := context.Background()

	_, err := h.pool.RestoreFromSeed(ctx, walletrpc.RestoreFromSeedRequest{
		Filename: "seeded", Password: "pw", Seed: "words",
	})
	require.NoError(t, err)

	_, err = h.pool.RestoreFromKeys(ctx, walletrpc.GenerateFromKeysRequest{
		Filename: "viewonly", Password: "pw", Address: "4x", ViewKey: "v",
	})
	require.NoError(t, err)

	_, err = h.pool.RestoreFromSeed(ctx, walletrpc.RestoreFromSeedRequest{
		Filename: "seeded",
	})
	require.ErrorIs(t, err, ErrWalletExists)

	_, err = h.pool.RestoreFromKeys(ctx, walletrpc.GenerateFromKeysRequest{
		Filename: "viewonly",
	})
	require.ErrorIs(t, err, ErrWalletExists)

	assert.Equal(t, 2, h.pool.OpenCount())
}

func TestMaxOpen(t *testing.T) {
	h := newHarness(t, config.NetworkMainnet)
	ctx := context.Background()

	for _, name := range []string{"a", "b", "c"} {
		_, err := h.pool.Create(ctx, name, "pw", "")
		require.NoError(t, err)
	}

	_, err := h.pool.Create(ctx, "d", "pw", "")
	require.ErrorIs(t, err, ErrTooManyOpen)

	require.NoError(t, h.pool.Close(ctx, "b"))

	_, err = h.pool.Create(ctx, "d", "pw", "")
	require.NoError(t, err)
}

func TestSetupFailureUnwinds(t *testing.T) {
	h := newHarness(t, config.NetworkMainnet)
	h.state.Fail("create_wallet", walletrpc.CodeWalletAlreadyExists, "exists")

	_, err := h.pool.Create(context.Background(), "x", "pw", "")
	require.ErrorIs(t, err, ErrWalletExists)
	assert.Equal(t, 0, h.pool.OpenCount())
	assert.True(t, h.launcher.Last().Exited())

	h.state.Fail("create_wallet", walletrpc.CodeUnknownError, "boom")

	_, err = h.pool.Create(context.Background(), "y", "pw", "")
	require.Error(t, err)

	h.state.Set(func(state *walletrpctest.State) {
		delete(state.FailMethod, "create_wallet")
	})
	h.state.Fail("auto_refresh", walletrpc.CodeUnknownError, "no")

	_, err = h.pool.Create(context.Background(), "z", "pw", "")
	require.Error(t, err)
	assert.Equal(t, 0, h.pool.OpenCount())
}

func TestLaunchFailure(t *testing.T) {
	h := newHarness(t, config.NetworkMainnet)
	h.launcher.FailNext(1)

	_, err := h.pool.Create(context.Background(), "x", "pw", "")
	require.ErrorIs(t, err, processtest.ErrLaunchRefused)

	_, err = h.pool.Create(context.Background(), "x", "pw", "")
	require.NoError(t, err)
}

func TestStartTimeoutWhenRPCNeverAnswers(t *testing.T) {
	h := newHarness(t, config.NetworkMainnet)
	h.pool.settings.StartTimeout = 300 * time.Millisecond
	h.launcher.OnLaunch = nil

	_, err := h.pool.Create(context.Background(), "x", "pw", "")
	require.ErrorIs(t, err, ErrStartTimeout)
	assert.Equal(t, 0, h.pool.OpenCount())
}

func TestProcessExitDuringStart(t *testing.T) {
	h := newHarness(t, config.NetworkMainnet)
	h.launcher.OnLaunch = nil

	go func() {
		require.Eventually(t, func() bool { return h.launcher.Last() != nil },
			5*time.Second, time.Millisecond)
		h.launcher.Last().Exit(errors.New("crashed"))
	}()

	_, err := h.pool.Create(context.Background(), "x", "pw", "")
	require.ErrorIs(t, err, ErrProcessExited)
}

func TestCancelledStart(t *testing.T) {
	h := newHarness(t, config.NetworkMainnet)
	h.launcher.OnLaunch = nil

	ctx, cancel := context.WithTimeout(
		context.Background(), 200*time.Millisecond,
	)
	defer cancel()

	_, err := h.pool.Create(ctx, "x", "pw", "")
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestUnexpectedExitDropsWallet(t *testing.T) {
	h := newHarness(t, config.NetworkMainnet)

	_, err := h.pool.Create(context.Background(), "x", "pw", "")
	require.NoError(t, err)

	h.launcher.Last().Exit(errors.New("oom"))

	require.Eventually(t, func() bool { return h.pool.OpenCount() == 0 },
		5*time.Second, time.Millisecond)

	_, err = h.pool.Open(context.Background(), "x", "pw")
	require.NoError(t, err)
}

func TestCloseReportsSaveFailure(t *testing.T) {
	h := newHarness(t, config.NetworkMainnet)

	_, err := h.pool.Create(context.Background(), "x", "pw", "")
	require.NoError(t, err)

	h.state.Fail("close_wallet", walletrpc.CodeUnknownError, "disk full")
	h.launcher.Last().StopErr = errors.New("stuck")

	require.Error(t, h.pool.Close(context.Background(), "x"))
	assert.Equal(t, 0, h.pool.OpenCount())
}

func TestBusyWhileOpening(t *testing.T) {
	h := newHarness(t, config.NetworkMainnet)

	release := make(chan struct{})
	original := h.launcher.OnLaunch
	h.launcher.OnLaunch = func(launch processtest.Launch) (func(), error) {
		<-release

		return original(launch)
	}

	var wg sync.WaitGroup

	wg.Go(func() {
		_, err := h.pool.Create(context.Background(), "slow", "pw", "")
		assert.NoError(t, err)
	})

	require.Eventually(t, func() bool {
		_, err := h.pool.Get("slow")

		return errors.Is(err, ErrWalletBusy)
	}, 5*time.Second, time.Millisecond)

	_, err := h.pool.Create(context.Background(), "slow", "pw", "")
	require.ErrorIs(t, err, ErrWalletBusy)
	require.ErrorIs(t,
		h.pool.Close(context.Background(), "slow"), ErrWalletBusy,
	)

	close(release)
	wg.Wait()

	_, err = h.pool.Get("slow")
	require.NoError(t, err)
}

func TestCloseAllRefusesNewWallets(t *testing.T) {
	h := newHarness(t, config.NetworkMainnet)
	ctx := context.Background()

	_, err := h.pool.Create(ctx, "a", "pw", "")
	require.NoError(t, err)

	h.state.Fail("close_wallet", walletrpc.CodeUnknownError, "disk full")
	h.pool.CloseAll(ctx)
	assert.Equal(t, 0, h.pool.OpenCount())

	_, err = h.pool.Create(ctx, "b", "pw", "")
	require.ErrorIs(t, err, ErrPoolClosed)
}

func TestShutdownDuringOpen(t *testing.T) {
	h := newHarness(t, config.NetworkMainnet)

	release := make(chan struct{})
	original := h.launcher.OnLaunch
	h.launcher.OnLaunch = func(launch processtest.Launch) (func(), error) {
		<-release

		return original(launch)
	}

	done := make(chan error, 1)

	go func() {
		_, err := h.pool.Create(context.Background(), "late", "pw", "")
		done <- err
	}()

	require.Eventually(t, func() bool {
		_, err := h.pool.Get("late")

		return errors.Is(err, ErrWalletBusy)
	}, 5*time.Second, time.Millisecond)

	h.pool.CloseAll(context.Background())
	close(release)

	require.ErrorIs(t, <-done, ErrPoolClosed)
	assert.True(t, h.launcher.Last().Exited())
}

func TestListIgnoresJunk(t *testing.T) {
	h := newHarness(t, config.NetworkMainnet)

	for _, name := range []string{
		"good.keys", ".bad.keys", "cache-only", "x.txt",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(h.dir, name), nil, 0o600))
	}

	require.NoError(t, os.Mkdir(filepath.Join(h.dir, "dir.keys"), 0o700))

	entries, err := h.pool.List()
	require.NoError(t, err)
	assert.Equal(t, []Entry{{Name: "good"}}, entries)

	require.NoError(t, os.RemoveAll(h.dir))

	_, err = h.pool.List()
	require.Error(t, err)
}

func TestExistsStatError(t *testing.T) {
	h := newHarness(t, config.NetworkMainnet)

	_, err := h.pool.Exists("../x")
	require.ErrorIs(t, err, ErrInvalidName)

	require.NoError(t, os.Chmod(h.dir, 0o000))
	t.Cleanup(func() {
		_ = os.Chmod(h.dir, 0o700) //nolint:gosec // a directory needs x
	})

	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}

	_, err = h.pool.Exists("x")
	require.Error(t, err)
}

func TestNetworkArgs(t *testing.T) {
	tests := map[config.Network]string{
		config.NetworkStagenet: "--stagenet",
		config.NetworkTestnet:  "--testnet",
		config.NetworkRegtest:  "--allow-mismatched-daemon-version",
	}

	for network, flag := range tests {
		h := newHarness(t, network)
		assert.Contains(t, h.pool.args(1), flag)
	}

	mainnet := newHarness(t, config.NetworkMainnet).pool.args(1)
	assert.Subset(t, mainnet, []string{
		"--disable-rpc-login", "--trusted-daemon", "--rpc-bind-ip", "127.0.0.1",
	})
	assert.NotContains(t, mainnet, "--stagenet")
}

func TestGetValidatesName(t *testing.T) {
	h := newHarness(t, config.NetworkMainnet)

	_, err := h.pool.Get("a/b")
	require.ErrorIs(t, err, ErrInvalidName)
	require.ErrorIs(t,
		h.pool.Close(context.Background(), "a/b"), ErrInvalidName,
	)
}

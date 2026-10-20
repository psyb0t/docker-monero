package walletpool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/psyb0t/docker-monero/internal/pkg/config"
	"github.com/psyb0t/docker-monero/internal/pkg/monero/walletrpc"
	"github.com/psyb0t/docker-monero/internal/pkg/process/processtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testDaemonLogin = "example-user:EXAMPLE-DO-NOT-USE"

func TestWalletDaemonConfigLifecycle(t *testing.T) {
	testCases := []struct {
		name       string
		failLaunch bool
		failReady  bool
		failSetup  bool
		cancelled  bool
		exited     bool
	}{
		{name: "success"},
		{name: "launch failure", failLaunch: true},
		{name: "readiness timeout", failReady: true},
		{name: "setup failure", failSetup: true},
		{name: "cancelled", cancelled: true},
		{name: "process exited", exited: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, config.NetworkMainnet)
			h.pool.settings.RPCLogin = testDaemonLogin
			h.pool.settings.StartTimeout = 200 * time.Millisecond

			if tc.failSetup {
				h.state.Fail(
					"create_wallet", walletrpc.CodeUnknownError, "failed",
				)
			}

			original := h.launcher.OnLaunch

			var configPath string

			h.launcher.OnLaunch = func(
				launch processtest.Launch,
			) (func(), error) {
				configPath = checkDaemonConfig(t, launch.Args)

				if tc.failLaunch {
					return nil, processtest.ErrLaunchRefused
				}

				if tc.exited {
					h.launcher.Last().Exit(processtest.ErrStopped)
				}

				if tc.failReady || tc.cancelled || tc.exited {
					return func() {}, nil
				}

				return original(launch)
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			if tc.cancelled {
				cancel()
			}

			_, err := h.pool.start(
				ctx, "example-wallet",
				func(ctx context.Context, client *walletrpc.Client) error {
					assert.NoFileExists(t, configPath)

					return client.CreateWallet(ctx, "example-wallet", "", "")
				},
			)
			if tc.failLaunch || tc.failReady || tc.failSetup ||
				tc.cancelled || tc.exited {
				require.Error(t, err)
				assert.NotContains(t, err.Error(), testDaemonLogin)
			} else {
				require.NoError(t, err)
			}

			require.NotEmpty(t, configPath)
			assert.NoFileExists(t, configPath)
			assert.NoDirExists(t, filepath.Dir(configPath))
		})
	}
}

func checkDaemonConfig(t *testing.T, args []string) string {
	t.Helper()

	_, password, _ := strings.Cut(testDaemonLogin, ":")
	for _, arg := range args {
		assert.NotContains(t, arg, password)
	}

	index := -1

	for i, arg := range args {
		if arg == "--config-file" {
			index = i

			break
		}
	}

	require.NotEqual(t, -1, index)
	require.Less(t, index+1, len(args))
	path := args[index+1]
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "daemon-login="+testDaemonLogin+"\n", string(contents))

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	info, err = os.Stat(filepath.Dir(path))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
	assert.Equal(t, os.TempDir(), filepath.Dir(filepath.Dir(path)))

	return path
}

func TestWalletWithoutDaemonConfig(t *testing.T) {
	h := newHarness(t, config.NetworkMainnet)
	_, err := h.pool.Create(context.Background(), "example-wallet", "", "")
	require.NoError(t, err)
	assert.NotContains(t, h.launcher.Last().Launched().Args, "--config-file")
}

func TestDaemonConfigCreationFailureReleasesReservation(t *testing.T) {
	h := newHarness(t, config.NetworkMainnet)
	h.pool.settings.RPCLogin = testDaemonLogin

	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))

	_, err := h.pool.Create(context.Background(), "example-wallet", "", "")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), testDaemonLogin)
	assert.Empty(t, h.launcher.Procs())
	assert.Empty(t, h.pool.reserving)
	assert.Empty(t, h.pool.ports)
}

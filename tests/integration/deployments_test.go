//go:build integration

package integration

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/psyb0t/docker-monero/pkg/http/api/client"
	"github.com/psyb0t/docker-monero/tests/testinfra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const stopTimeout = 60 * time.Second

func startApp(t *testing.T, env map[string]string) *testinfra.App {
	t.Helper()

	app, err := testinfra.StartApp(
		context.Background(), testinfra.AppConfig{Env: env},
	)
	require.NoError(t, err)

	t.Cleanup(func() { app.Terminate(context.Background()) })

	return app
}

// TestReadOnlyMode checks that a read-only deployment refuses every write
// with READ_ONLY, keeps reads and open/close working, and offers no write
// tool over MCP.
func TestReadOnlyMode(t *testing.T) {
	app := startApp(t, map[string]string{
		"DOCKER_MONERO_READ_ONLY":           "true",
		"DOCKER_MONERO_MCP_ALLOW_TRANSFERS": "true",
	})

	writes := []struct {
		path string
		body map[string]any
	}{
		{"/v1/wallets", map[string]any{"name": "w", "password": "p"}},
		{"/v1/wallets/restore/seed", map[string]any{
			"name": "w", "password": "p", "seed": "words",
		}},
		{"/v1/wallets/restore/keys", map[string]any{
			"name": "w", "password": "p", "address": "a", "viewKey": "v",
		}},
		{"/v1/wallets/w/addresses", map[string]any{"label": "x"}},
		{"/v1/wallets/w/transfers", map[string]any{
			"destinations": []map[string]string{
				{"address": "a", "amount": "1"},
			},
		}},
		{"/v1/node/regtest/generate-blocks", map[string]any{
			"count": 1, "address": "a",
		}},
	}

	for _, write := range writes {
		status, body := rawRequest(
			t, app, http.MethodPost, write.path, write.body, true,
		)
		requireError(t, status, http.StatusForbidden, body, "READ_ONLY")
	}

	info, err := app.Client.GetNodeInfoWithResponse(context.Background())
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, info.StatusCode())

	names := toolNames(t, app, "/mcp")
	assert.NotContains(t, names, "monero_create_wallet")
	assert.NotContains(t, names, "monero_create_address")
	assert.NotContains(t, names, "monero_transfer")
	assert.NotContains(t, names, "monero_generate_blocks")
	assert.Contains(t, names, "monero_open_wallet")
	assert.Contains(t, names, "monero_wallet_balance")
}

// TestTransfersDisabled checks the transfer switch on its own: other writes
// work, sends are refused.
func TestTransfersDisabled(t *testing.T) {
	app := startApp(t, map[string]string{
		"DOCKER_MONERO_ALLOW_TRANSFERS": "false",
	})

	address := createWallet(t, app, "nosend")
	require.NotEmpty(t, address)

	status, body := rawRequest(t, app, http.MethodPost,
		"/v1/wallets/nosend/transfers", map[string]any{
			"destinations": []map[string]string{
				{"address": address, "amount": "1"},
			},
		}, true)
	requireError(t, status, http.StatusForbidden, body,
		"TRANSFERS_DISABLED")
}

// TestMCPTransfersOptIn checks monero_transfer appears only when the
// deployment opts in.
func TestMCPTransfersOptIn(t *testing.T) {
	app := startApp(t, map[string]string{
		"DOCKER_MONERO_MCP_ALLOW_TRANSFERS": "true",
	})

	assert.Contains(t, toolNames(t, app, "/mcp"), "monero_transfer")
}

// TestStatePersistsAcrossRestart stops the container cleanly and starts it
// again: the chain and the wallet files live in /data and must survive, and
// a stop must save open wallets rather than lose them.
func TestStatePersistsAcrossRestart(t *testing.T) {
	ctx := context.Background()
	app := startApp(t, nil)

	address := createWallet(t, app, "survivor")
	mine(t, app, address, 5)
	waitSynced(t, app, "survivor")

	before, err := app.Client.GetNodeInfoWithResponse(ctx)
	require.NoError(t, err)

	timeout := stopTimeout
	require.NoError(t, app.Container.Stop(ctx, &timeout))

	logs, err := app.Logs(ctx)
	require.NoError(t, err)
	assert.Contains(t, logs, "shutdown completed")

	require.NoError(t, app.Container.Start(ctx))

	restarted, err := testinfra.Reconnect(ctx, app)
	require.NoError(t, err)

	after, err := restarted.Client.GetNodeInfoWithResponse(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, after.StatusCode())
	assert.Equal(t, before.JSON200.Height, after.JSON200.Height)

	opened, err := restarted.Client.OpenWalletWithResponse(
		ctx, "survivor", client.OpenWalletRequest{Password: testPassword},
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, opened.StatusCode(), string(opened.Body))
	assert.Equal(t, address, *opened.JSON200.Address)
}

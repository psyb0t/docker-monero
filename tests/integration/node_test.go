//go:build integration

package integration

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNodeEndpoints(t *testing.T) {
	app := sharedApp
	ctx := context.Background()

	info, err := app.Client.GetNodeInfoWithResponse(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, info.StatusCode(), string(info.Body))
	assert.Equal(t, "regtest", string(info.JSON200.Network))
	assert.True(t, info.JSON200.Synchronized)
	assert.InDelta(t, 100.0, info.JSON200.SyncPercent, 0.001)
	assert.False(t, info.JSON200.Restricted)
	assert.Zero(t, info.JSON200.Restarts)

	latest, err := app.Client.GetLatestBlockWithResponse(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, latest.StatusCode())

	byHeight, err := app.Client.GetBlockByHeightWithResponse(
		ctx, latest.JSON200.Height,
	)
	require.NoError(t, err)
	assert.Equal(t, latest.JSON200.Hash, byHeight.JSON200.Hash)

	byHash, err := app.Client.GetBlockByHashWithResponse(
		ctx, latest.JSON200.Hash,
	)
	require.NoError(t, err)
	assert.Equal(t, latest.JSON200.Height, byHash.JSON200.Height)

	fee, err := app.Client.GetFeeEstimateWithResponse(ctx)
	require.NoError(t, err)
	assert.NotEmpty(t, fee.JSON200.FeesPerByte)

	status, body := rawRequest(t, app, http.MethodGet,
		"/v1/node/blocks/height/999999999", nil, true)
	requireError(t, status, http.StatusNotFound, body, "NOT_FOUND")

	status, body = rawRequest(t, app, http.MethodGet,
		"/v1/node/blocks/hash/zz", nil, true)
	requireError(t, status, http.StatusBadRequest, body, "BAD_REQUEST")

	status, body = rawRequest(t, app, http.MethodGet,
		"/v1/node/transactions/"+strings.Repeat("ab", 32), nil, true)
	requireError(t, status, http.StatusNotFound, body, "NOT_FOUND")

	status, body = rawRequest(t, app, http.MethodGet,
		"/v1/node/blocks/height/notanumber", nil, true)
	requireError(t, status, http.StatusBadRequest, body, "BAD_REQUEST")

	status, body = rawRequest(t, app, http.MethodPost,
		"/v1/node/regtest/generate-blocks",
		map[string]any{"count": 0, "address": "x"}, true)
	requireError(t, status, http.StatusBadRequest, body, "BAD_REQUEST")
}

func TestAuthAndProbes(t *testing.T) {
	app := sharedApp

	status, body := rawRequest(t, app, http.MethodGet,
		"/v1/node/info", nil, false)
	requireError(t, status, http.StatusUnauthorized, body, "UNAUTHORIZED")

	status, body = rawRequest(t, app, http.MethodPost, "/mcp",
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"},
		false)
	requireError(t, status, http.StatusUnauthorized, body, "UNAUTHORIZED")

	status, body = rawRequest(t, app, http.MethodGet, "/healthz", nil, false)
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, "ok", body["status"])

	status, body = rawRequest(t, app, http.MethodGet, "/ready", nil, false)
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, "ready", body["status"])

	status, body = rawRequest(t, app, http.MethodPost, "/v1/wallets",
		map[string]any{"name": 5}, true)
	requireError(t, status, http.StatusBadRequest, body, "BAD_REQUEST")
}

func TestMetricsEndpoint(t *testing.T) {
	request, err := http.NewRequestWithContext(
		context.Background(), http.MethodGet, sharedApp.MetricsURL, nil,
	)
	require.NoError(t, err)

	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)

	defer response.Body.Close()

	raw, err := io.ReadAll(response.Body)
	require.NoError(t, err)

	text := string(raw)
	assert.Equal(t, http.StatusOK, response.StatusCode)
	assert.Contains(t, text, "monero_node_height")
	assert.Contains(t, text, "monero_open_wallets")
	assert.Contains(t, text, "monero_api_http_requests_total")
}

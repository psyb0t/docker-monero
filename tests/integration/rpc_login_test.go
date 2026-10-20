//go:build integration

package integration

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/psyb0t/docker-monero/internal/pkg/monero/jsonrpc"
	"github.com/psyb0t/docker-monero/pkg/http/api/client"
	"github.com/psyb0t/docker-monero/tests/testinfra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRPCLogin(t *testing.T) {
	const (
		login          = "example-user:EXAMPLE-DO-NOT-USE"
		walletName     = "rpc-login-wallet"
		walletPassword = "EXAMPLE-DO-NOT-USE"
		rpcTimeout     = 10 * time.Second
	)

	ctx := context.Background()
	app, err := testinfra.StartApp(ctx, testinfra.AppConfig{
		Env:                 map[string]string{"DOCKER_MONERO_RPC_LOGIN": login},
		ExposeRestrictedRPC: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { app.Terminate(context.Background()) })

	info, err := app.Client.GetNodeInfoWithResponse(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, info.StatusCode(), string(info.Body))
	require.NotNil(t, info.JSON200)

	created, err := app.Client.CreateWalletWithResponse(
		ctx, client.CreateWalletRequest{
			Name: walletName, Password: walletPassword,
		},
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, created.StatusCode(),
		string(created.Body))
	require.NotNil(t, created.JSON201.Address)
	mine(t, app, *created.JSON201.Address, 3)
	waitSynced(t, app, walletName)

	closed, err := app.Client.CloseWalletWithResponse(ctx, walletName)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, closed.StatusCode(),
		string(closed.Body))

	opened, err := app.Client.OpenWalletWithResponse(
		ctx, walletName, client.OpenWalletRequest{Password: walletPassword},
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, opened.StatusCode(), string(opened.Body))
	waitSynced(t, app, walletName)

	host, err := app.Container.Host(ctx)
	require.NoError(t, err)
	port, err := app.Container.MappedPort(ctx, "18089/tcp")
	require.NoError(t, err)
	rpcURL := "http://" + net.JoinHostPort(host, port.Port())

	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost, rpcURL+"/get_info", strings.NewReader("{}"),
	)
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: rpcTimeout}).Do(request)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	assert.Equal(t, http.StatusUnauthorized, response.StatusCode)
	assert.Contains(t, response.Header.Get("WWW-Authenticate"), "Digest")

	var restrictedInfo struct {
		Status string `json:"status"`
		Height uint64 `json:"height"`
	}

	rpc := jsonrpc.New(rpcURL, rpcTimeout, jsonrpc.WithLogin(login))
	// Post rejects every status except 200 before decoding the response.
	require.NoError(t, rpc.Post(ctx, "/get_info", struct{}{}, &restrictedInfo))
	assert.Equal(t, "OK", restrictedInfo.Status)
	assert.Positive(t, restrictedInfo.Height)
}

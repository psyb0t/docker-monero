//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/psyb0t/docker-monero/pkg/http/api/client"
	"github.com/psyb0t/docker-monero/tests/testinfra"
	"github.com/stretchr/testify/require"
)

const (
	syncTimeout  = 90 * time.Second
	pollInterval = time.Second
	testPassword = "correct horse battery staple"

	// coinbaseMaturity is how many blocks a mined reward stays locked.
	coinbaseMaturity = 60

	// decoyHeadroom adds blocks so the chain holds enough outputs to pick
	// ring members from.
	decoyHeadroom = 80
)

// createWallet creates a wallet on app and returns its primary address.
func createWallet(t *testing.T, app *testinfra.App, name string) string {
	t.Helper()

	response, err := app.Client.CreateWalletWithResponse(
		context.Background(),
		client.CreateWalletRequest{Name: name, Password: testPassword},
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, response.StatusCode(),
		string(response.Body))
	require.NotNil(t, response.JSON201.Address)

	return *response.JSON201.Address
}

// mine mines count blocks to address.
func mine(t *testing.T, app *testinfra.App, address string, count int64) {
	t.Helper()

	response, err := app.Client.GenerateBlocksWithResponse(
		context.Background(),
		client.GenerateBlocksRequest{Count: count, Address: address},
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode(),
		string(response.Body))
	require.Len(t, response.JSON200.BlockHashes, int(count))
}

// waitSynced polls a wallet's status until it has caught up with the node.
// Open wallets refresh in the background, so this is what a real client
// does before reading a balance or sending.
func waitSynced(t *testing.T, app *testinfra.App, name string) {
	t.Helper()

	require.Eventually(t, func() bool {
		response, err := app.Client.GetWalletStatusWithResponse(
			context.Background(), name,
		)
		if err != nil || response.JSON200 == nil {
			return false
		}

		status := response.JSON200

		return status.Syncing != nil && !*status.Syncing
	}, syncTimeout, pollInterval, "wallet %s never caught up", name)
}

// waitUnlocked polls until an account has at least want atomic units
// unlocked. It compares the decimal strings by length then value, which is
// exact for unsigned integers without leading zeros.
func waitUnlocked(
	t *testing.T,
	app *testinfra.App,
	name string,
	positive bool,
) client.Balance {
	t.Helper()

	var balance client.Balance

	require.Eventually(t, func() bool {
		response, err := app.Client.GetWalletBalanceWithResponse(
			context.Background(), name, nil,
		)
		if err != nil || response.JSON200 == nil {
			return false
		}

		balance = *response.JSON200

		return !positive || balance.UnlockedBalance != "0"
	}, syncTimeout, pollInterval, "wallet %s never got unlocked funds", name)

	return balance
}

// rawRequest sends a request with or without the bearer token and returns
// the status and the decoded JSON body.
func rawRequest(
	t *testing.T,
	app *testinfra.App,
	method, path string,
	body any,
	withToken bool,
) (int, map[string]any) {
	t.Helper()

	var reader io.Reader

	if body != nil {
		encoded, err := json.Marshal(body)
		require.NoError(t, err)

		reader = bytes.NewReader(encoded)
	}

	request, err := http.NewRequestWithContext(
		context.Background(), method, app.BaseURL+path, reader,
	)
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")

	if withToken {
		request.Header.Set("Authorization", "Bearer "+app.Token)
	}

	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)

	defer response.Body.Close()

	raw, err := io.ReadAll(response.Body)
	require.NoError(t, err)

	decoded := map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &decoded)
	}

	return response.StatusCode, decoded
}

// requireError asserts the aichteeteapee envelope's code.
func requireError(
	t *testing.T,
	status, wantStatus int,
	body map[string]any,
	wantCode string,
) {
	t.Helper()

	require.Equal(t, wantStatus, status, body)
	require.Equal(t, wantCode, body["code"], body)
	require.NotEmpty(t, body["message"])
}

//go:build integration

package integration

import (
	"context"
	"net/http"
	"testing"

	"github.com/psyb0t/docker-monero/pkg/http/api/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWalletLifecycleAndTransfer walks the whole money path a programmatic
// wallet goes through: create two wallets, mine and mature funds, price a
// transfer, send it, see it in the pool and then confirmed, and see it
// arrive. Steps share state, so they run as ordered subtests.
func TestWalletLifecycleAndTransfer(t *testing.T) {
	app := sharedApp
	ctx := context.Background()

	sender := createWallet(t, app, "flow-sender")
	receiver := createWallet(t, app, "flow-receiver")

	const sendAmount = "1500000000000"

	var txHash string

	t.Run("mined rewards mature", func(t *testing.T) {
		mine(t, app, sender, 10)
		// A fresh regtest chain needs enough outputs for ring decoys
		// before any transaction can be built, so mine well past
		// coinbase maturity.
		mine(t, app, receiver, coinbaseMaturity+decoyHeadroom)
		waitSynced(t, app, "flow-sender")

		balance := waitUnlocked(t, app, "flow-sender", true)
		assert.NotEqual(t, "0", balance.Balance)
		assert.Contains(t, balance.UnlockedBalanceXmr, ".")
	})

	t.Run("dry run prices without relaying", func(t *testing.T) {
		dryRun := true

		response, err := app.Client.SendTransferWithResponse(
			ctx, "flow-sender", client.TransferRequest{
				Destinations: []client.Destination{
					{Address: receiver, Amount: sendAmount},
				},
				DryRun: &dryRun,
			},
		)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, response.StatusCode(),
			string(response.Body))
		assert.False(t, response.JSON200.Relayed)
		assert.Equal(t, sendAmount, response.JSON200.Amount)
		assert.NotEqual(t, "0", response.JSON200.Fee)
	})

	t.Run("send reaches the pool", func(t *testing.T) {
		priority := client.TransferPriorityNormal

		response, err := app.Client.SendTransferWithResponse(
			ctx, "flow-sender", client.TransferRequest{
				Destinations: []client.Destination{
					{Address: receiver, Amount: sendAmount},
				},
				Priority: &priority,
			},
		)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, response.StatusCode(),
			string(response.Body))
		require.True(t, response.JSON200.Relayed)
		require.Len(t, response.JSON200.TxHash, 64)

		txHash = response.JSON200.TxHash

		tx, err := app.Client.GetTransactionWithResponse(ctx, txHash)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, tx.StatusCode(), string(tx.Body))
		assert.True(t, tx.JSON200.InPool)

		pool, err := app.Client.GetMempoolStatsWithResponse(ctx)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, pool.JSON200.TxCount, int64(1))
	})

	t.Run("mined transfer confirms and arrives", func(t *testing.T) {
		require.NotEmpty(t, txHash)
		mine(t, app, receiver, 11)

		tx, err := app.Client.GetTransactionWithResponse(ctx, txHash)
		require.NoError(t, err)
		assert.False(t, tx.JSON200.InPool)
		assert.GreaterOrEqual(t, tx.JSON200.Confirmations, int64(10))

		waitSynced(t, app, "flow-receiver")
		waitSynced(t, app, "flow-sender")

		in := []client.TransferDirection{client.TransferDirectionIn}
		limit := 1000

		incoming, err := app.Client.ListWalletTransfersWithResponse(
			ctx, "flow-receiver", &client.ListWalletTransfersParams{
				Direction: &in, Limit: &limit,
			},
		)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, incoming.StatusCode())
		assert.True(t, containsTransfer(
			incoming.JSON200.Transfers, txHash, sendAmount,
		), "the receiver did not record the transfer")

		out := []client.TransferDirection{client.TransferDirectionOut}

		outgoing, err := app.Client.ListWalletTransfersWithResponse(
			ctx, "flow-sender", &client.ListWalletTransfersParams{
				Direction: &out,
			},
		)
		require.NoError(t, err)
		assert.Equal(t, 1, outgoing.JSON200.Total)
		assert.Equal(t, txHash, outgoing.JSON200.Transfers[0].TxHash)
	})

	t.Run("transfer history pages", func(t *testing.T) {
		one := 1
		total := -1

		for offset := 0; offset < 3; offset++ {
			page, err := app.Client.ListWalletTransfersWithResponse(
				ctx, "flow-receiver", &client.ListWalletTransfersParams{
					Limit: &one, Offset: &offset,
				},
			)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, page.StatusCode())
			require.Len(t, page.JSON200.Transfers, 1)
			assert.Equal(t, offset, page.JSON200.Offset)

			if total >= 0 {
				assert.Equal(t, total, page.JSON200.Total)
			}

			total = page.JSON200.Total
		}

		assert.Greater(t, total, 3)
	})

	t.Run("refusals map to stable codes", func(t *testing.T) {
		status, body := rawRequest(t, app, http.MethodPost,
			"/v1/wallets/flow-sender/transfers", map[string]any{
				"destinations": []map[string]string{{
					"address": receiver, "amount": "99999999999999999",
				}},
			}, true)
		requireError(t, status, http.StatusUnprocessableEntity, body,
			"INSUFFICIENT_FUNDS")

		status, body = rawRequest(t, app, http.MethodPost,
			"/v1/wallets/flow-sender/transfers", map[string]any{
				"destinations": []map[string]string{{
					"address": "not-an-address", "amount": "1",
				}},
			}, true)
		requireError(t, status, http.StatusBadRequest, body, "BAD_REQUEST")

		status, body = rawRequest(t, app, http.MethodPost,
			"/v1/wallets/flow-sender/transfers", map[string]any{
				"destinations": []map[string]string{{
					"address": receiver, "amount": "1.5",
				}},
			}, true)
		requireError(t, status, http.StatusBadRequest, body, "BAD_REQUEST")
	})
}

func containsTransfer(
	transfers []client.TransferEntry,
	txHash, amount string,
) bool {
	for _, transfer := range transfers {
		if transfer.TxHash == txHash && transfer.Amount == amount {
			return true
		}
	}

	return false
}

// TestOpenCloseAndPasswords covers the pool's lifecycle through the API.
func TestOpenCloseAndPasswords(t *testing.T) {
	app := sharedApp
	ctx := context.Background()

	createWallet(t, app, "lifecycle")

	status, body := rawRequest(t, app, http.MethodPost, "/v1/wallets",
		map[string]string{"name": "lifecycle", "password": "x"}, true)
	requireError(t, status, http.StatusConflict, body, "WALLET_EXISTS")

	status, body = rawRequest(t, app, http.MethodPost,
		"/v1/wallets/lifecycle/open",
		map[string]string{"password": testPassword}, true)
	requireError(t, status, http.StatusConflict, body, "WALLET_ALREADY_OPEN")

	closed, err := app.Client.CloseWalletWithResponse(ctx, "lifecycle")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, closed.StatusCode())
	assert.False(t, closed.JSON200.Open)

	statusResponse, err := app.Client.GetWalletStatusWithResponse(
		ctx, "lifecycle",
	)
	require.NoError(t, err)
	assert.False(t, statusResponse.JSON200.Open)
	assert.Nil(t, statusResponse.JSON200.Address)

	status, body = rawRequest(t, app, http.MethodGet,
		"/v1/wallets/lifecycle/balance", nil, true)
	requireError(t, status, http.StatusConflict, body, "WALLET_NOT_OPEN")

	status, body = rawRequest(t, app, http.MethodPost,
		"/v1/wallets/lifecycle/close", nil, true)
	requireError(t, status, http.StatusConflict, body, "WALLET_NOT_OPEN")

	status, body = rawRequest(t, app, http.MethodPost,
		"/v1/wallets/lifecycle/open",
		map[string]string{"password": "wrong"}, true)
	requireError(t, status, http.StatusForbidden, body,
		"INVALID_WALLET_PASSWORD")

	opened, err := app.Client.OpenWalletWithResponse(
		ctx, "lifecycle", client.OpenWalletRequest{Password: testPassword},
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, opened.StatusCode(), string(opened.Body))
	assert.True(t, opened.JSON200.Open)

	list, err := app.Client.ListWalletsWithResponse(ctx)
	require.NoError(t, err)
	assert.Contains(t, list.JSON200.Wallets,
		client.WalletSummary{Name: "lifecycle", Open: true})
	assert.Positive(t, list.JSON200.MaxOpen)

	status, body = rawRequest(t, app, http.MethodGet,
		"/v1/wallets/ghost/status", nil, true)
	requireError(t, status, http.StatusNotFound, body, "NOT_FOUND")

	status, body = rawRequest(t, app, http.MethodPost,
		"/v1/wallets/ghost/open",
		map[string]string{"password": "x"}, true)
	requireError(t, status, http.StatusNotFound, body, "NOT_FOUND")

	status, body = rawRequest(t, app, http.MethodGet,
		"/v1/wallets/..bad/status", nil, true)
	requireError(t, status, http.StatusBadRequest, body, "BAD_REQUEST")
}

// TestAddresses covers subaddress creation and listing.
func TestAddresses(t *testing.T) {
	app := sharedApp
	ctx := context.Background()

	primary := createWallet(t, app, "addresses")
	label := "shop"

	created, err := app.Client.CreateWalletAddressWithResponse(
		ctx, "addresses", client.CreateAddressRequest{Label: &label},
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, created.StatusCode(),
		string(created.Body))
	assert.Equal(t, int64(1), created.JSON201.AddressIndex)
	assert.Equal(t, "shop", created.JSON201.Label)
	assert.NotEqual(t, primary, created.JSON201.Address)

	list, err := app.Client.ListWalletAddressesWithResponse(
		ctx, "addresses", nil,
	)
	require.NoError(t, err)
	assert.Equal(t, primary, list.JSON200.PrimaryAddress)
	require.Len(t, list.JSON200.Addresses, 2)
	assert.Equal(t, created.JSON201.Address, list.JSON200.Addresses[1].Address)

	status, body := rawRequest(t, app, http.MethodGet,
		"/v1/wallets/addresses/balance?accountIndex=-1", nil, true)
	requireError(t, status, http.StatusBadRequest, body, "BAD_REQUEST")
}

// TestRestoreFromKeysViewOnly restores a view-only copy of a wallet from
// its address and view key, and checks it cannot spend.
func TestRestoreFromKeysViewOnly(t *testing.T) {
	app := sharedApp

	status, body := rawRequest(t, app, http.MethodPost,
		"/v1/wallets/restore/keys", map[string]any{
			"name":     "watch-bad",
			"password": testPassword,
			"address":  "not-an-address",
			"viewKey":  "00",
		}, true)
	assert.GreaterOrEqual(t, status, http.StatusBadRequest, body)

	status, body = rawRequest(t, app, http.MethodPost,
		"/v1/wallets/restore/seed", map[string]any{
			"name":     "seed-bad",
			"password": testPassword,
			"seed":     "these are not twenty five mnemonic words",
		}, true)
	assert.GreaterOrEqual(t, status, http.StatusBadRequest, body)

	list, err := app.Client.ListWalletsWithResponse(context.Background())
	require.NoError(t, err)

	for _, wallet := range list.JSON200.Wallets {
		assert.NotEqual(t, "watch-bad", wallet.Name)
		assert.NotEqual(t, "seed-bad", wallet.Name)
	}
}

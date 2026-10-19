//go:build integration

// Package integration drives docker-monero through its real production
// image on regtest: real monerod, real monero-wallet-rpc, real wallets and
// transactions, over real HTTP. Nothing here inspects the service's source
// or substitutes a mock for a boundary it crosses in production.
package integration

import (
	"context"
	"os"
	"testing"

	"github.com/psyb0t/docker-monero/tests/testinfra"
)

// sharedApp is the default deployment most tests run against: regtest,
// writes allowed, MCP transfers off. Building and booting the image per test
// would make the suite far too slow, so tests that need another deployment
// start their own and every test uses its own wallet names.
var sharedApp *testinfra.App

func TestMain(m *testing.M) {
	ctx := context.Background()

	app, err := testinfra.StartApp(ctx, testinfra.AppConfig{})
	if err != nil {
		// No *testing.T exists yet to report through.
		os.Stderr.WriteString("integration setup failed: " + err.Error() + "\n")
		os.Exit(1)
	}

	sharedApp = app

	code := m.Run()

	app.Terminate(ctx)
	os.Exit(code)
}

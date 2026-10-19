package wallets

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/docker-monero/internal/pkg/common/access"
	"github.com/psyb0t/docker-monero/internal/pkg/http/api"
	"github.com/psyb0t/docker-monero/internal/pkg/monero/daemonrpc"
	"github.com/psyb0t/docker-monero/internal/pkg/monero/jsonrpc"
	"github.com/psyb0t/docker-monero/internal/pkg/monero/walletrpc"
	"github.com/psyb0t/docker-monero/internal/pkg/monero/walletrpc/walletrpctest"
	"github.com/psyb0t/docker-monero/internal/pkg/process/processtest"
	"github.com/psyb0t/docker-monero/internal/pkg/walletpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testMaxOpen    = 3
	walletPassword = "pw"
	oneXMR         = 1_000_000_000_000
	chainHeight    = 100
	baseTimestamp  = 1_700_000_000
)

// writeMethods are the wallet-rpc calls a read-only deployment must never
// make on behalf of a client.
var writeMethods = []string{
	"create_wallet",
	"restore_deterministic_wallet",
	"generate_from_keys",
	"create_address",
	"transfer",
}

// fakeDaemon is the node the wallet service compares wallet heights to.
type fakeDaemon struct {
	info  daemonrpc.Info
	err   error
	calls int
}

func (d *fakeDaemon) GetInfo(context.Context) (daemonrpc.Info, error) {
	d.calls++

	return d.info, d.err
}

// spyPool is the real pool with the arguments of the calls the service
// builds recorded, and one failure it can be told to inject.
type spyPool struct {
	*walletpool.Pool

	existsErr      error
	createLanguage string
	seedRequest    walletrpc.RestoreFromSeedRequest
	keysRequest    walletrpc.GenerateFromKeysRequest
	openName       string
	openPassword   string
}

func (p *spyPool) Exists(name string) (bool, error) {
	if p.existsErr != nil {
		return false, p.existsErr
	}

	return p.Pool.Exists(name)
}

func (p *spyPool) Create(
	ctx context.Context,
	name, password, language string,
) (*walletpool.Wallet, error) {
	p.createLanguage = language

	return p.Pool.Create(ctx, name, password, language)
}

func (p *spyPool) RestoreFromSeed(
	ctx context.Context,
	request walletrpc.RestoreFromSeedRequest,
) (*walletpool.Wallet, error) {
	p.seedRequest = request

	return p.Pool.RestoreFromSeed(ctx, request)
}

func (p *spyPool) RestoreFromKeys(
	ctx context.Context,
	request walletrpc.GenerateFromKeysRequest,
) (*walletpool.Wallet, error) {
	p.keysRequest = request

	return p.Pool.RestoreFromKeys(ctx, request)
}

func (p *spyPool) Open(
	ctx context.Context,
	name, password string,
) (*walletpool.Wallet, error) {
	p.openName = name
	p.openPassword = password

	return p.Pool.Open(ctx, name, password)
}

// harness is a wallet service over the real pool, with the fake wallet-rpc
// behind it and a node that is synchronized at chainHeight.
type harness struct {
	dir      string
	state    *walletrpctest.State
	launcher *processtest.Launcher
	pool     *spyPool
	daemon   *fakeDaemon
	service  *Service
}

func defaultOptions() Options {
	return Options{AllowTransfers: true}
}

func newHarness(t *testing.T, options Options) *harness {
	t.Helper()

	dir := t.TempDir()
	state := walletrpctest.NewState(dir)
	state.Set(func(s *walletrpctest.State) { s.Height = chainHeight })

	launcher := walletrpctest.NewLauncher(state)

	portStart, err := walletrpctest.FreePortRange(testMaxOpen)
	require.NoError(t, err)

	pool, err := walletpool.New(walletpool.Settings{
		WalletRPCPath: "monero-wallet-rpc",
		WalletDir:     dir,
		DaemonAddress: "127.0.0.1:18081",
		Network:       "mainnet",
		PortStart:     portStart,
		MaxOpen:       testMaxOpen,
		StartTimeout:  5 * time.Second,
		RPCTimeout:    5 * time.Second,
	}, launcher)
	require.NoError(t, err)

	t.Cleanup(func() { pool.CloseAll(context.Background()) })

	spy := &spyPool{Pool: pool}
	daemon := &fakeDaemon{info: daemonrpc.Info{
		Height: chainHeight, Synchronized: true,
	}}

	return &harness{
		dir:      dir,
		state:    state,
		launcher: launcher,
		pool:     spy,
		daemon:   daemon,
		service:  New(spy, daemon, options),
	}
}

// serviceWith is another service over the same pool, node, and wallets.
func (h *harness) serviceWith(options Options) *Service {
	return New(h.pool, h.daemon, options)
}

// create opens wallets through a service that may write, whatever the
// service under test allows.
func (h *harness) create(t *testing.T, names ...string) {
	t.Helper()

	writer := h.serviceWith(defaultOptions())

	for _, name := range names {
		_, err := writer.Create(context.Background(), api.CreateWalletRequest{
			Name: name, Password: walletPassword,
		})
		require.NoError(t, err)
	}
}

// fund sets the unlocked balance every fake wallet reports.
func (h *harness) fund(amount uint64) {
	h.state.Set(func(s *walletrpctest.State) { s.UnlockedBalance = amount })
}

func (h *harness) seedTransfers(transfers walletrpc.Transfers) {
	h.state.Set(func(s *walletrpctest.State) { s.Transfers = transfers })
}

func (h *harness) setWalletHeight(height uint64) {
	h.state.Set(func(s *walletrpctest.State) { s.Height = height })
}

func (h *harness) transfers(query TransferQuery) (api.TransferList, error) {
	return h.service.Transfers(context.Background(), "main", query)
}

func (h *harness) transfer(
	request api.TransferRequest,
) (api.TransferResult, error) {
	return h.service.Transfer(context.Background(), "main", request)
}

func (h *harness) lastTransfer() map[string]any {
	var sent map[string]any

	h.state.Set(func(s *walletrpctest.State) { sent = s.LastTransfer })

	return sent
}

func (h *harness) writeCalls() map[string]int {
	calls := map[string]int{}
	for _, method := range writeMethods {
		calls[method] = h.state.Called(method)
	}

	return calls
}

// requireOpenedNow checks the open time is recent and clears it, so the rest
// of the status can be compared whole.
func requireOpenedNow(t *testing.T, status *api.WalletStatus) {
	t.Helper()

	require.NotNil(t, status.OpenedAt)
	assert.WithinDuration(t, time.Now(), *status.OpenedAt, time.Minute)
	assert.Equal(t, time.UTC, status.OpenedAt.Location())

	status.OpenedAt = nil
}

// caughtUpStatus is the status of an open wallet level with a synchronized
// node, minus the open time.
func caughtUpStatus(name string) api.WalletStatus {
	height := int64(chainHeight)

	return api.WalletStatus{
		Name:             name,
		Open:             true,
		Address:          new(walletrpctest.FakeAddress),
		WalletHeight:     new(height),
		NodeHeight:       new(height),
		NodeSynchronized: new(true),
		BlocksBehind:     new(int64(0)),
		SyncPercent:      new(100.0),
		Syncing:          new(false),
	}
}

func transferHashes(list api.TransferList) []string {
	hashes := make([]string, 0, len(list.Transfers))
	for _, transfer := range list.Transfers {
		hashes = append(hashes, transfer.TxHash)
	}

	return hashes
}

// transferFixture is count transfers spread round-robin over the five
// categories, each one newer than the last. It also returns their hashes
// newest first, the order the service must list them in.
func transferFixture(count uint64) (walletrpc.Transfers, []string) {
	var transfers walletrpc.Transfers

	categories := []*[]walletrpc.TransferEntry{
		&transfers.In,
		&transfers.Out,
		&transfers.Pending,
		&transfers.Failed,
		&transfers.Pool,
	}
	newestFirst := make([]string, count)

	for i := range count {
		hash := fmt.Sprintf("tx%02d", i)
		category := categories[i%uint64(len(categories))]

		*category = append(*category, walletrpc.TransferEntry{
			TxID:      hash,
			Height:    1000 + i,
			Timestamp: baseTimestamp + i*100,
			Amount:    (i + 1) * oneXMR,
		})
		newestFirst[count-1-i] = hash
	}

	return transfers, newestFirst
}

func entryAt(hash string, timestamp, height uint64) walletrpc.TransferEntry {
	return walletrpc.TransferEntry{
		TxID: hash, Timestamp: timestamp, Height: height,
	}
}

func TestListWithoutWallets(t *testing.T) {
	h := newHarness(t, defaultOptions())

	got, err := h.service.List()

	require.NoError(t, err)
	assert.Equal(t, api.WalletList{
		Wallets:   []api.WalletSummary{},
		OpenCount: 0,
		MaxOpen:   testMaxOpen,
	}, got)
}

func TestListReportsOpenAndClosedWalletsByName(t *testing.T) {
	h := newHarness(t, defaultOptions())
	h.create(t, "bravo", "alpha", "charlie")

	_, err := h.service.Close(context.Background(), "alpha")
	require.NoError(t, err)

	got, err := h.service.List()

	require.NoError(t, err)
	assert.Equal(t, api.WalletList{
		Wallets: []api.WalletSummary{
			{Name: "alpha", Open: false},
			{Name: "bravo", Open: true},
			{Name: "charlie", Open: true},
		},
		OpenCount: 2,
		MaxOpen:   testMaxOpen,
	}, got)
}

func TestListFailsWhenTheWalletDirectoryIsGone(t *testing.T) {
	h := newHarness(t, defaultOptions())
	require.NoError(t, os.RemoveAll(h.dir))

	got, err := h.service.List()

	require.ErrorIs(t, err, fs.ErrNotExist)
	assert.Equal(t, api.WalletList{}, got)
}

func TestCreateOpensTheWallet(t *testing.T) {
	h := newHarness(t, defaultOptions())

	got, err := h.service.Create(context.Background(), api.CreateWalletRequest{
		Name:     "main",
		Password: walletPassword,
		Language: new("Deutsch"),
	})

	require.NoError(t, err)
	requireOpenedNow(t, &got)
	assert.Equal(t, caughtUpStatus("main"), got)
	assert.Equal(t, "Deutsch", h.pool.createLanguage)
	assert.Equal(t, 1, h.pool.OpenCount())
}

func TestCreateWithoutLanguagePassesNone(t *testing.T) {
	h := newHarness(t, defaultOptions())
	h.pool.createLanguage = "left over"

	_, err := h.service.Create(context.Background(), api.CreateWalletRequest{
		Name: "main", Password: walletPassword,
	})

	require.NoError(t, err)
	assert.Equal(t, "", h.pool.createLanguage)
}

func TestCreateAcceptsALongestPassword(t *testing.T) {
	h := newHarness(t, defaultOptions())

	_, err := h.service.Create(context.Background(), api.CreateWalletRequest{
		Name: "main", Password: strings.Repeat("p", api.MaxPasswordLength),
	})

	require.NoError(t, err)
}

func TestCreateRejectsBeforeLaunchingAnything(t *testing.T) {
	testCases := []struct {
		name    string
		options Options
		request api.CreateWalletRequest
		wantIs  error
		wantMsg string
	}{
		{
			name:    "read-only deployment",
			options: Options{ReadOnly: true, AllowTransfers: true},
			request: api.CreateWalletRequest{Name: "w", Password: "pw"},
			wantIs:  access.ErrReadOnly,
		},
		{
			name:    "password over the limit",
			options: defaultOptions(),
			request: api.CreateWalletRequest{
				Name:     "w",
				Password: strings.Repeat("p", api.MaxPasswordLength+1),
			},
			wantMsg: "password is longer than 256 characters",
		},
		{
			name:    "name that is not a file name",
			options: defaultOptions(),
			request: api.CreateWalletRequest{Name: "../evil", Password: "pw"},
			wantIs:  walletpool.ErrInvalidName,
		},
		{
			name:    "empty name",
			options: defaultOptions(),
			request: api.CreateWalletRequest{Name: "", Password: "pw"},
			wantIs:  walletpool.ErrInvalidName,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, tc.options)

			got, err := h.service.Create(context.Background(), tc.request)

			if tc.wantIs != nil {
				require.ErrorIs(t, err, tc.wantIs)
			} else {
				requireInvalid(t, err, tc.wantMsg)
			}

			assert.Equal(t, api.WalletStatus{}, got)
			assert.Empty(t, h.launcher.Procs())
			assert.Zero(t, h.state.Called("create_wallet"))
		})
	}
}

func TestCreateRefusesDuplicatesAndFullPools(t *testing.T) {
	h := newHarness(t, defaultOptions())
	h.create(t, "a", "b", "c")

	_, err := h.service.Create(context.Background(), api.CreateWalletRequest{
		Name: "a", Password: walletPassword,
	})
	require.ErrorIs(t, err, walletpool.ErrWalletExists)

	_, err = h.service.Create(context.Background(), api.CreateWalletRequest{
		Name: "d", Password: walletPassword,
	})
	require.ErrorIs(t, err, walletpool.ErrTooManyOpen)

	assert.Equal(t, testMaxOpen, h.pool.OpenCount())
}

func TestCreateSucceedsWithoutNodeFieldsWhenTheNodeIsDown(t *testing.T) {
	h := newHarness(t, defaultOptions())
	h.daemon.err = commerr.ErrConnectFailed

	got, err := h.service.Create(context.Background(), api.CreateWalletRequest{
		Name: "main", Password: walletPassword,
	})

	require.NoError(t, err)
	requireOpenedNow(t, &got)
	assert.Equal(t, api.WalletStatus{
		Name:         "main",
		Open:         true,
		Address:      new(walletrpctest.FakeAddress),
		WalletHeight: new(int64(chainHeight)),
	}, got)
}

// A wallet whose status cannot be read right after it was created is still
// created and open. Create reports the minimal open status rather than an
// error a client would retry into WALLET_EXISTS, and the same failure is
// reported, mapped, by a later Status call.
func TestCreateMapsWalletRPCFailuresWhileReadingStatus(t *testing.T) {
	testCases := []struct {
		name   string
		method string
		code   int
		wantIs error
	}{
		{
			name:   "address read: account out of range",
			method: "get_address",
			code:   walletrpc.CodeAccountIndexOutOfBound,
			wantIs: ErrInvalidAccount,
		},
		{
			name:   "height read: node connection lost",
			method: "get_height",
			code:   walletrpc.CodeNoDaemonConnection,
			wantIs: commerr.ErrUnavailable,
		},
		{
			name:   "height read: unmapped code stays an rpc error",
			method: "get_height",
			code:   walletrpc.CodeUnknownError,
			wantIs: jsonrpc.ErrRPC,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, defaultOptions())
			h.state.Fail(tc.method, tc.code, "failure")

			got, err := h.service.Create(
				context.Background(),
				api.CreateWalletRequest{Name: "main", Password: walletPassword},
			)

			require.NoError(t, err)
			assert.Equal(t, "main", got.Name)
			assert.True(t, got.Open)
			assert.NotNil(t, got.OpenedAt)
			assert.Nil(t, got.Address)

			_, err = h.service.Status(context.Background(), "main")
			require.ErrorIs(t, err, tc.wantIs)
		})
	}
}

func TestRestoreFromSeedForwardsEveryField(t *testing.T) {
	h := newHarness(t, defaultOptions())

	got, err := h.service.RestoreFromSeed(
		context.Background(),
		api.RestoreFromSeedRequest{
			Name:          "seeded",
			Password:      walletPassword,
			Seed:          "one two three",
			SeedOffset:    new("offset"),
			RestoreHeight: new(int64(12345)),
			Language:      new("Deutsch"),
		},
	)

	require.NoError(t, err)
	requireOpenedNow(t, &got)
	assert.Equal(t, caughtUpStatus("seeded"), got)
	assert.Equal(t, walletrpc.RestoreFromSeedRequest{
		Filename:      "seeded",
		Password:      walletPassword,
		Seed:          "one two three",
		SeedOffset:    "offset",
		RestoreHeight: 12345,
		Language:      "Deutsch",
	}, h.pool.seedRequest)
}

func TestRestoreFromSeedDefaultsOptionalFields(t *testing.T) {
	h := newHarness(t, defaultOptions())

	_, err := h.service.RestoreFromSeed(
		context.Background(),
		api.RestoreFromSeedRequest{
			Name: "seeded", Password: walletPassword, Seed: "words",
		},
	)

	require.NoError(t, err)
	assert.Equal(t, walletrpc.RestoreFromSeedRequest{
		Filename: "seeded", Password: walletPassword, Seed: "words",
	}, h.pool.seedRequest)
}

func TestRestoreFromSeedRejects(t *testing.T) {
	testCases := []struct {
		name    string
		options Options
		request api.RestoreFromSeedRequest
		wantIs  error
		wantMsg string
	}{
		{
			name:    "read-only deployment",
			options: Options{ReadOnly: true},
			request: api.RestoreFromSeedRequest{
				Name: "w", Password: "pw", Seed: "words",
			},
			wantIs: access.ErrReadOnly,
		},
		{
			name:    "password over the limit",
			options: defaultOptions(),
			request: api.RestoreFromSeedRequest{
				Name:     "w",
				Password: strings.Repeat("p", api.MaxPasswordLength+1),
				Seed:     "words",
			},
			wantMsg: "password is longer than 256 characters",
		},
		{
			name:    "missing seed",
			options: defaultOptions(),
			request: api.RestoreFromSeedRequest{Name: "w", Password: "pw"},
			wantMsg: "seed is required",
		},
		{
			name:    "negative restore height",
			options: defaultOptions(),
			request: api.RestoreFromSeedRequest{
				Name:          "w",
				Password:      "pw",
				Seed:          "words",
				RestoreHeight: new(int64(-1)),
			},
			wantMsg: "restoreHeight must not be negative",
		},
		{
			name:    "bad wallet name",
			options: defaultOptions(),
			request: api.RestoreFromSeedRequest{
				Name: "a/b", Password: "pw", Seed: "words",
			},
			wantIs: walletpool.ErrInvalidName,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, tc.options)

			got, err := h.service.RestoreFromSeed(
				context.Background(), tc.request,
			)

			if tc.wantIs != nil {
				require.ErrorIs(t, err, tc.wantIs)
			} else {
				requireInvalid(t, err, tc.wantMsg)
			}

			assert.Equal(t, api.WalletStatus{}, got)
			assert.Empty(t, h.launcher.Procs())
		})
	}
}

func TestRestoreFromSeedRefusesAnExistingWallet(t *testing.T) {
	h := newHarness(t, defaultOptions())
	h.create(t, "taken")

	_, err := h.service.RestoreFromSeed(
		context.Background(),
		api.RestoreFromSeedRequest{
			Name: "taken", Password: walletPassword, Seed: "words",
		},
	)

	require.ErrorIs(t, err, walletpool.ErrWalletExists)
}

func TestRestoreMapsWalletRPCFailures(t *testing.T) {
	testCases := []struct {
		name    string
		method  string
		code    int
		restore func(h *harness) error
		wantIs  error
		wantMsg string
	}{
		{
			name:   "seed with a bad key",
			method: "restore_deterministic_wallet",
			code:   walletrpc.CodeWrongKey,
			restore: func(h *harness) error {
				_, err := h.service.RestoreFromSeed(
					context.Background(),
					api.RestoreFromSeedRequest{
						Name: "w", Password: "pw", Seed: "words",
					},
				)

				return err
			},
			wantMsg: "the supplied key is not valid",
		},
		{
			name:   "keys with a bad address",
			method: "generate_from_keys",
			code:   walletrpc.CodeWrongAddress,
			restore: func(h *harness) error {
				_, err := h.service.RestoreFromKeys(
					context.Background(),
					api.RestoreFromKeysRequest{
						Name: "w", Password: "pw", Address: "bad", ViewKey: "v",
					},
				)

				return err
			},
			wantIs: ErrInvalidAddress,
		},
		{
			name:   "keys with a bad key",
			method: "generate_from_keys",
			code:   walletrpc.CodeWrongKey,
			restore: func(h *harness) error {
				_, err := h.service.RestoreFromKeys(
					context.Background(),
					api.RestoreFromKeysRequest{
						Name: "w", Password: "pw", Address: "a", ViewKey: "bad",
					},
				)

				return err
			},
			wantMsg: "the supplied key is not valid",
		},
		{
			name:   "seed with the node unreachable",
			method: "restore_deterministic_wallet",
			code:   walletrpc.CodeNoDaemonConnection,
			restore: func(h *harness) error {
				_, err := h.service.RestoreFromSeed(
					context.Background(),
					api.RestoreFromSeedRequest{
						Name: "w", Password: "pw", Seed: "words",
					},
				)

				return err
			},
			wantIs: commerr.ErrUnavailable,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, defaultOptions())
			h.state.Fail(tc.method, tc.code, "failure")

			err := tc.restore(h)

			if tc.wantIs != nil {
				require.ErrorIs(t, err, tc.wantIs)
			} else {
				requireInvalid(t, err, tc.wantMsg)
			}

			assert.Zero(t, h.pool.OpenCount())
		})
	}
}

func TestRestoreFromKeysForwardsEveryField(t *testing.T) {
	h := newHarness(t, defaultOptions())

	got, err := h.service.RestoreFromKeys(
		context.Background(),
		api.RestoreFromKeysRequest{
			Name:          "keyed",
			Password:      walletPassword,
			Address:       "4address",
			ViewKey:       "viewkey",
			SpendKey:      new("spendkey"),
			RestoreHeight: new(int64(777)),
			Language:      new("Deutsch"),
		},
	)

	require.NoError(t, err)
	requireOpenedNow(t, &got)
	assert.Equal(t, caughtUpStatus("keyed"), got)
	assert.Equal(t, walletrpc.GenerateFromKeysRequest{
		Filename:      "keyed",
		Password:      walletPassword,
		Address:       "4address",
		ViewKey:       "viewkey",
		SpendKey:      "spendkey",
		RestoreHeight: 777,
		Language:      "Deutsch",
	}, h.pool.keysRequest)
}

func TestRestoreFromKeysWithoutSpendKeyIsViewOnly(t *testing.T) {
	h := newHarness(t, defaultOptions())

	_, err := h.service.RestoreFromKeys(
		context.Background(),
		api.RestoreFromKeysRequest{
			Name:     "watch",
			Password: walletPassword,
			Address:  "4address",
			ViewKey:  "viewkey",
		},
	)

	require.NoError(t, err)
	assert.Equal(t, "", h.pool.keysRequest.SpendKey)
	assert.Equal(t, uint64(0), h.pool.keysRequest.RestoreHeight)
}

func TestRestoreFromKeysRejects(t *testing.T) {
	testCases := []struct {
		name    string
		options Options
		request api.RestoreFromKeysRequest
		wantIs  error
		wantMsg string
	}{
		{
			name:    "read-only deployment",
			options: Options{ReadOnly: true},
			request: api.RestoreFromKeysRequest{
				Name: "w", Password: "pw", Address: "a", ViewKey: "v",
			},
			wantIs: access.ErrReadOnly,
		},
		{
			name:    "password over the limit",
			options: defaultOptions(),
			request: api.RestoreFromKeysRequest{
				Name:     "w",
				Password: strings.Repeat("p", api.MaxPasswordLength+1),
				Address:  "a",
				ViewKey:  "v",
			},
			wantMsg: "password is longer than 256 characters",
		},
		{
			name:    "missing address",
			options: defaultOptions(),
			request: api.RestoreFromKeysRequest{
				Name: "w", Password: "pw", ViewKey: "v",
			},
			wantMsg: "address and viewKey are required",
		},
		{
			name:    "missing view key",
			options: defaultOptions(),
			request: api.RestoreFromKeysRequest{
				Name: "w", Password: "pw", Address: "a",
			},
			wantMsg: "address and viewKey are required",
		},
		{
			name:    "negative restore height",
			options: defaultOptions(),
			request: api.RestoreFromKeysRequest{
				Name:          "w",
				Password:      "pw",
				Address:       "a",
				ViewKey:       "v",
				RestoreHeight: new(int64(-10)),
			},
			wantMsg: "restoreHeight must not be negative",
		},
		{
			name:    "bad wallet name",
			options: defaultOptions(),
			request: api.RestoreFromKeysRequest{
				Name: "", Password: "pw", Address: "a", ViewKey: "v",
			},
			wantIs: walletpool.ErrInvalidName,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, tc.options)

			got, err := h.service.RestoreFromKeys(
				context.Background(), tc.request,
			)

			if tc.wantIs != nil {
				require.ErrorIs(t, err, tc.wantIs)
			} else {
				requireInvalid(t, err, tc.wantMsg)
			}

			assert.Equal(t, api.WalletStatus{}, got)
			assert.Empty(t, h.launcher.Procs())
		})
	}
}

func TestRestoreFromKeysRefusesAnExistingWallet(t *testing.T) {
	h := newHarness(t, defaultOptions())
	h.create(t, "taken")

	_, err := h.service.RestoreFromKeys(
		context.Background(),
		api.RestoreFromKeysRequest{
			Name:     "taken",
			Password: walletPassword,
			Address:  "a",
			ViewKey:  "v",
		},
	)

	require.ErrorIs(t, err, walletpool.ErrWalletExists)
}

func TestOpenReopensAClosedWallet(t *testing.T) {
	h := newHarness(t, defaultOptions())
	h.create(t, "main")

	_, err := h.service.Close(context.Background(), "main")
	require.NoError(t, err)

	got, err := h.service.Open(context.Background(), "main", walletPassword)

	require.NoError(t, err)
	requireOpenedNow(t, &got)
	assert.Equal(t, caughtUpStatus("main"), got)
	assert.Equal(t, "main", h.pool.openName)
	assert.Equal(t, walletPassword, h.pool.openPassword)
}

func TestOpenRejects(t *testing.T) {
	testCases := []struct {
		name     string
		wallet   string
		password string
		wantIs   error
		wantMsg  string
		pooled   bool
	}{
		{
			name:     "wrong password",
			wallet:   "main",
			password: "wrong",
			wantIs:   walletpool.ErrInvalidPassword,
			pooled:   true,
		},
		{
			name:     "unknown wallet",
			wallet:   "ghost",
			password: walletPassword,
			wantIs:   walletpool.ErrWalletNotFound,
			pooled:   true,
		},
		{
			name:     "bad wallet name",
			wallet:   "../main",
			password: walletPassword,
			wantIs:   walletpool.ErrInvalidName,
			pooled:   true,
		},
		{
			name:     "password over the limit",
			wallet:   "main",
			password: strings.Repeat("p", api.MaxPasswordLength+1),
			wantMsg:  "password is longer than 256 characters",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, defaultOptions())
			h.create(t, "main")

			_, err := h.service.Close(context.Background(), "main")
			require.NoError(t, err)

			got, err := h.service.Open(
				context.Background(), tc.wallet, tc.password,
			)

			if tc.wantIs != nil {
				require.ErrorIs(t, err, tc.wantIs)
			} else {
				requireInvalid(t, err, tc.wantMsg)
			}

			assert.Equal(t, api.WalletStatus{}, got)
			assert.Equal(t, tc.pooled, h.pool.openName != "")
			assert.Zero(t, h.pool.OpenCount())
		})
	}
}

func TestOpenRefusesAnOpenWallet(t *testing.T) {
	h := newHarness(t, defaultOptions())
	h.create(t, "main")

	_, err := h.service.Open(context.Background(), "main", walletPassword)

	require.ErrorIs(t, err, walletpool.ErrWalletAlreadyOpen)
}

func TestCloseStopsTheWallet(t *testing.T) {
	h := newHarness(t, defaultOptions())
	h.create(t, "main")

	got, err := h.service.Close(context.Background(), "main")

	require.NoError(t, err)
	assert.Equal(t, api.WalletStatus{Name: "main", Open: false}, got)
	assert.Zero(t, h.pool.OpenCount())
	assert.True(t, h.launcher.Last().Exited())
}

func TestCloseRejects(t *testing.T) {
	h := newHarness(t, defaultOptions())
	h.create(t, "main")

	_, err := h.service.Close(context.Background(), "ghost")
	require.ErrorIs(t, err, walletpool.ErrWalletNotOpen)

	_, err = h.service.Close(context.Background(), "../main")
	require.ErrorIs(t, err, walletpool.ErrInvalidName)

	assert.Equal(t, 1, h.pool.OpenCount())
}

func TestCloseReportsASaveFailureButStillClosesTheWallet(t *testing.T) {
	h := newHarness(t, defaultOptions())
	h.create(t, "main")
	h.state.Fail("close_wallet", walletrpc.CodeUnknownError, "disk full")

	got, err := h.service.Close(context.Background(), "main")

	require.ErrorIs(t, err, jsonrpc.ErrRPC)
	assert.Equal(t, api.WalletStatus{}, got)
	assert.Zero(t, h.pool.OpenCount())
}

func TestStatusOfAnOpenWallet(t *testing.T) {
	h := newHarness(t, defaultOptions())
	h.create(t, "main")

	got, err := h.service.Status(context.Background(), "main")

	require.NoError(t, err)
	requireOpenedNow(t, &got)
	assert.Equal(t, caughtUpStatus("main"), got)
}

func TestStatusReportsSyncProgress(t *testing.T) {
	testCases := []struct {
		name         string
		walletHeight uint64
		node         daemonrpc.Info
		want         api.WalletStatus
	}{
		{
			name:         "wallet behind a synchronized node",
			walletHeight: 25,
			node:         daemonrpc.Info{Height: 100, Synchronized: true},
			want: api.WalletStatus{
				WalletHeight:     new(int64(25)),
				NodeHeight:       new(int64(100)),
				NodeSynchronized: new(true),
				BlocksBehind:     new(int64(75)),
				SyncPercent:      new(25.0),
				Syncing:          new(true),
			},
		},
		{
			name:         "wallet level with a node still syncing",
			walletHeight: 100,
			node:         daemonrpc.Info{Height: 100},
			want: api.WalletStatus{
				WalletHeight:     new(int64(100)),
				NodeHeight:       new(int64(100)),
				NodeSynchronized: new(false),
				BlocksBehind:     new(int64(0)),
				SyncPercent:      new(100.0),
				Syncing:          new(true),
			},
		},
		{
			name:         "wallet ahead of the node",
			walletHeight: 120,
			node:         daemonrpc.Info{Height: 100, Synchronized: true},
			want: api.WalletStatus{
				WalletHeight:     new(int64(120)),
				NodeHeight:       new(int64(100)),
				NodeSynchronized: new(true),
				BlocksBehind:     new(int64(0)),
				SyncPercent:      new(100.0),
				Syncing:          new(false),
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, defaultOptions())
			h.create(t, "main")
			h.daemon.info = tc.node
			h.setWalletHeight(tc.walletHeight)

			got, err := h.service.Status(context.Background(), "main")

			require.NoError(t, err)
			requireOpenedNow(t, &got)

			want := tc.want
			want.Name = "main"
			want.Open = true
			want.Address = new(walletrpctest.FakeAddress)
			assert.Equal(t, want, got)
		})
	}
}

func TestStatusLeavesNodeFieldsOutWhenTheNodeIsDown(t *testing.T) {
	h := newHarness(t, defaultOptions())
	h.create(t, "main")
	h.daemon.err = commerr.ErrTimeout

	got, err := h.service.Status(context.Background(), "main")

	require.NoError(t, err)
	requireOpenedNow(t, &got)
	assert.Equal(t, api.WalletStatus{
		Name:         "main",
		Open:         true,
		Address:      new(walletrpctest.FakeAddress),
		WalletHeight: new(int64(chainHeight)),
	}, got)
}

func TestStatusOfAClosedWallet(t *testing.T) {
	h := newHarness(t, defaultOptions())
	h.create(t, "main")

	_, err := h.service.Close(context.Background(), "main")
	require.NoError(t, err)

	got, err := h.service.Status(context.Background(), "main")

	require.NoError(t, err)
	assert.Equal(t, api.WalletStatus{Name: "main", Open: false}, got)
}

func TestStatusRejects(t *testing.T) {
	h := newHarness(t, defaultOptions())

	_, err := h.service.Status(context.Background(), "ghost")
	require.ErrorIs(t, err, walletpool.ErrWalletNotFound)

	_, err = h.service.Status(context.Background(), "../ghost")
	require.ErrorIs(t, err, walletpool.ErrInvalidName)
}

func TestStatusReportsAFailedExistenceCheck(t *testing.T) {
	h := newHarness(t, defaultOptions())
	h.pool.existsErr = fs.ErrPermission

	got, err := h.service.Status(context.Background(), "ghost")

	require.ErrorIs(t, err, fs.ErrPermission)
	require.NotErrorIs(t, err, walletpool.ErrWalletNotFound)
	assert.Equal(t, api.WalletStatus{}, got)
}

func TestStatusMapsWalletRPCFailures(t *testing.T) {
	h := newHarness(t, defaultOptions())
	h.create(t, "main")
	h.state.Fail("get_address", walletrpc.CodeAccountIndexOutOfBound, "no")

	got, err := h.service.Status(context.Background(), "main")

	require.ErrorIs(t, err, ErrInvalidAccount)
	assert.Equal(t, api.WalletStatus{}, got)
}

// walletReads are the calls that need an open wallet and change nothing.
var walletReads = map[string]struct {
	method string
	call   func(s *Service, name string) error
}{
	"balance": {
		method: "get_balance",
		call: func(s *Service, name string) error {
			_, err := s.Balance(context.Background(), name, nil)

			return err
		},
	},
	"addresses": {
		method: "get_address",
		call: func(s *Service, name string) error {
			_, err := s.Addresses(context.Background(), name, nil)

			return err
		},
	},
	"transfers": {
		method: "get_transfers",
		call: func(s *Service, name string) error {
			_, err := s.Transfers(context.Background(), name, TransferQuery{})

			return err
		},
	},
	"create address": {
		method: "create_address",
		call: func(s *Service, name string) error {
			_, err := s.CreateAddress(
				context.Background(), name, api.CreateAddressRequest{},
			)

			return err
		},
	},
	"transfer": {
		method: "transfer",
		call: func(s *Service, name string) error {
			_, err := s.Transfer(
				context.Background(), name, transferRequest("1"),
			)

			return err
		},
	},
}

func TestWalletCallsNeedAnOpenWalletWithAValidName(t *testing.T) {
	for name, read := range walletReads {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, defaultOptions())
			h.create(t, "closed")

			_, err := h.service.Close(context.Background(), "closed")
			require.NoError(t, err)

			before := h.state.Called(read.method)

			for _, notOpen := range []string{"closed", "ghost"} {
				err = read.call(h.service, notOpen)
				require.ErrorIs(t, err, walletpool.ErrWalletNotOpen, notOpen)
			}

			err = read.call(h.service, "../etc")
			require.ErrorIs(t, err, walletpool.ErrInvalidName)
			assert.Equal(t, before, h.state.Called(read.method))
		})
	}
}

func TestWalletCallsMapWalletRPCFailures(t *testing.T) {
	failures := []struct {
		name   string
		code   int
		wantIs error
	}{
		{
			name:   "account out of range",
			code:   walletrpc.CodeAccountIndexOutOfBound,
			wantIs: ErrInvalidAccount,
		},
		{
			name:   "address out of range",
			code:   walletrpc.CodeAddressIndexOutOfBound,
			wantIs: ErrInvalidAccount,
		},
		{
			name:   "node connection lost",
			code:   walletrpc.CodeNoDaemonConnection,
			wantIs: commerr.ErrUnavailable,
		},
		{
			name:   "node busy",
			code:   walletrpc.CodeDaemonIsBusy,
			wantIs: commerr.ErrUnavailable,
		},
		{
			name:   "unmapped code",
			code:   walletrpc.CodeUnknownError,
			wantIs: jsonrpc.ErrRPC,
		},
	}

	for name, read := range walletReads {
		if name == "transfer" {
			continue
		}

		for _, failure := range failures {
			t.Run(name+": "+failure.name, func(t *testing.T) {
				h := newHarness(t, defaultOptions())
				h.create(t, "main")
				h.state.Fail(read.method, failure.code, "failure")

				err := read.call(h.service, "main")

				require.ErrorIs(t, err, failure.wantIs)
			})
		}
	}
}

func TestBalance(t *testing.T) {
	h := newHarness(t, defaultOptions())
	h.create(t, "main")
	h.state.Set(func(s *walletrpctest.State) {
		s.Balance = 1_500_000_000_000
		s.UnlockedBalance = 500_000_000_000
	})

	got, err := h.service.Balance(context.Background(), "main", new(int64(2)))

	require.NoError(t, err)
	assert.Equal(t, api.Balance{
		AccountIndex:       2,
		Balance:            "1500000000000",
		BalanceXmr:         "1.500000000000",
		UnlockedBalance:    "500000000000",
		UnlockedBalanceXmr: "0.500000000000",
		Subaddresses:       []api.SubaddressBalance{},
	}, got)
}

func TestBalanceDefaultsToAccountZero(t *testing.T) {
	h := newHarness(t, defaultOptions())
	h.create(t, "main")

	got, err := h.service.Balance(context.Background(), "main", nil)

	require.NoError(t, err)
	assert.Equal(t, int64(0), got.AccountIndex)
	assert.Equal(t, "0", got.Balance)
}

func TestBalanceRejectsAnAccountOutOfRange(t *testing.T) {
	h := newHarness(t, defaultOptions())
	h.create(t, "main")

	for _, bad := range []int64{-1, maxIndex} {
		_, err := h.service.Balance(context.Background(), "main", new(bad))

		require.ErrorIs(t, err, ErrInvalidAccount)
	}

	assert.Zero(t, h.state.Called("get_balance"))

	_, err := h.service.Balance(
		context.Background(), "main", new(int64(maxIndex-1)),
	)

	require.NoError(t, err)
}

func TestAddresses(t *testing.T) {
	h := newHarness(t, defaultOptions())
	h.create(t, "main")
	h.state.Set(func(s *walletrpctest.State) {
		s.Subaddresses = []walletrpc.AddressInfo{
			{Address: "8sub-one", Label: "rent", AddressIndex: 1, Used: true},
			{Address: "8sub-two", Label: "food", AddressIndex: 2},
		}
	})

	got, err := h.service.Addresses(context.Background(), "main", new(int64(0)))

	require.NoError(t, err)
	assert.Equal(t, api.AddressList{
		AccountIndex:   0,
		PrimaryAddress: walletrpctest.FakeAddress,
		Addresses: []api.Address{
			{
				Address:      walletrpctest.FakeAddress,
				AddressIndex: 0,
				Label:        "Primary account",
			},
			{Address: "8sub-one", AddressIndex: 1, Label: "rent", Used: true},
			{Address: "8sub-two", AddressIndex: 2, Label: "food"},
		},
	}, got)
}

func TestAddressesRejectsAnAccountOutOfRange(t *testing.T) {
	h := newHarness(t, defaultOptions())
	h.create(t, "main")
	before := h.state.Called("get_address")

	_, err := h.service.Addresses(context.Background(), "main", new(int64(-3)))

	require.ErrorIs(t, err, ErrInvalidAccount)
	assert.Equal(t, before, h.state.Called("get_address"))
}

func TestCreateAddress(t *testing.T) {
	h := newHarness(t, defaultOptions())
	h.create(t, "main")

	got, err := h.service.CreateAddress(
		context.Background(), "main", api.CreateAddressRequest{
			AccountIndex: new(int64(0)),
			Label:        new("savings"),
		},
	)

	require.NoError(t, err)
	assert.Equal(t, api.Address{
		Address:      "8FakeSubaddress1",
		AddressIndex: 1,
		Label:        "savings",
		Used:         false,
	}, got)

	list, err := h.service.Addresses(context.Background(), "main", nil)

	require.NoError(t, err)
	require.Len(t, list.Addresses, 2)
	assert.Equal(t, got, list.Addresses[1])
}

func TestCreateAddressWithoutLabel(t *testing.T) {
	h := newHarness(t, defaultOptions())
	h.create(t, "main")

	got, err := h.service.CreateAddress(
		context.Background(), "main", api.CreateAddressRequest{},
	)

	require.NoError(t, err)
	assert.Equal(t, "", got.Label)
	assert.Equal(t, int64(1), got.AddressIndex)
}

func TestCreateAddressLabelLimit(t *testing.T) {
	h := newHarness(t, defaultOptions())
	h.create(t, "main")

	_, err := h.service.CreateAddress(
		context.Background(), "main", api.CreateAddressRequest{
			Label: new(strings.Repeat("l", api.MaxLabelLength)),
		},
	)
	require.NoError(t, err)

	_, err = h.service.CreateAddress(
		context.Background(), "main", api.CreateAddressRequest{
			Label: new(strings.Repeat("l", api.MaxLabelLength+1)),
		},
	)

	requireInvalid(t, err, "label is longer than 128 characters")
	assert.Equal(t, 1, h.state.Called("create_address"))
}

func TestCreateAddressRejectsAnAccountOutOfRange(t *testing.T) {
	h := newHarness(t, defaultOptions())
	h.create(t, "main")

	_, err := h.service.CreateAddress(
		context.Background(), "main", api.CreateAddressRequest{
			AccountIndex: new(int64(maxIndex)),
		},
	)

	require.ErrorIs(t, err, ErrInvalidAccount)
	assert.Zero(t, h.state.Called("create_address"))
}

func TestTransfersWalksPagesNewestFirst(t *testing.T) {
	h := newHarness(t, defaultOptions())
	h.create(t, "main")

	transfers, newestFirst := transferFixture(13)
	h.seedTransfers(transfers)

	testCases := []struct {
		name   string
		limit  *int
		offset *int
		want   []string
	}{
		{"first page", new(5), new(0), newestFirst[0:5]},
		{"second page", new(5), new(5), newestFirst[5:10]},
		{"partial last page", new(5), new(10), newestFirst[10:13]},
		{"offset equals the total", new(5), new(13), []string{}},
		{"offset far past the end", new(5), new(5000), []string{}},
		{"last single row", new(5), new(12), newestFirst[12:13]},
		{"one full page of everything", new(13), new(0), newestFirst},
		{"limit above the total", new(14), new(0), newestFirst},
		{"limit of one", new(1), new(0), newestFirst[0:1]},
		{"maximum limit", new(api.MaxListLimit), new(0), newestFirst},
		{"defaults", nil, nil, newestFirst},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := h.service.Transfers(
				context.Background(), "main",
				TransferQuery{Limit: tc.limit, Offset: tc.offset},
			)

			require.NoError(t, err)
			assert.NotNil(t, got.Transfers)
			assert.Equal(t, tc.want, transferHashes(got))
			assert.Equal(t, 13, got.Total)

			wantLimit, wantOffset := api.DefaultListLimit, 0
			if tc.limit != nil {
				wantLimit = *tc.limit
			}

			if tc.offset != nil {
				wantOffset = *tc.offset
			}

			assert.Equal(t, wantLimit, got.Limit)
			assert.Equal(t, wantOffset, got.Offset)
		})
	}
}

func TestTransfersPagesCoverEveryTransferExactlyOnce(t *testing.T) {
	h := newHarness(t, defaultOptions())
	h.create(t, "main")

	transfers, newestFirst := transferFixture(13)
	h.seedTransfers(transfers)

	var walked []string

	for offset := 0; offset < 15; offset += 5 {
		got, err := h.service.Transfers(
			context.Background(), "main",
			TransferQuery{Limit: new(5), Offset: new(offset)},
		)
		require.NoError(t, err)

		walked = append(walked, transferHashes(got)...)
	}

	assert.Equal(t, newestFirst, walked)
}

func TestTransfersLabelsEachEntryWithItsCategory(t *testing.T) {
	h := newHarness(t, defaultOptions())
	h.create(t, "main")

	transfers, _ := transferFixture(5)
	h.seedTransfers(transfers)

	got, err := h.transfers(TransferQuery{})

	require.NoError(t, err)

	directions := map[string]api.TransferDirection{}
	for _, entry := range got.Transfers {
		directions[entry.TxHash] = entry.Direction
	}

	assert.Equal(t, map[string]api.TransferDirection{
		"tx00": api.TransferDirectionIn,
		"tx01": api.TransferDirectionOut,
		"tx02": api.TransferDirectionPending,
		"tx03": api.TransferDirectionFailed,
		"tx04": api.TransferDirectionPool,
	}, directions)
}

func TestTransfersEmptyAndSingleRowHistories(t *testing.T) {
	h := newHarness(t, defaultOptions())
	h.create(t, "main")

	got, err := h.transfers(TransferQuery{})

	require.NoError(t, err)
	assert.Equal(t, api.TransferList{
		Transfers: []api.TransferEntry{},
		Total:     0,
		Limit:     api.DefaultListLimit,
		Offset:    0,
	}, got)

	transfers, _ := transferFixture(1)
	h.seedTransfers(transfers)

	got, err = h.transfers(TransferQuery{})

	require.NoError(t, err)
	assert.Equal(t, []string{"tx00"}, transferHashes(got))
	assert.Equal(t, 1, got.Total)
}

func TestTransfersBreaksTimestampTiesByHeightThenCategory(t *testing.T) {
	h := newHarness(t, defaultOptions())
	h.create(t, "main")

	h.state.Set(func(s *walletrpctest.State) {
		s.Transfers = walletrpc.Transfers{
			In: []walletrpc.TransferEntry{
				entryAt("in-low", baseTimestamp, 50),
				entryAt("in-high", baseTimestamp, 70),
				entryAt("in-newer", baseTimestamp+1, 1),
			},
			Out: []walletrpc.TransferEntry{
				entryAt("out-low", baseTimestamp, 50),
			},
			Pending: []walletrpc.TransferEntry{
				entryAt("pending", baseTimestamp, 0),
			},
			Pool: []walletrpc.TransferEntry{
				entryAt("pool", baseTimestamp, 0),
			},
		}
	})

	got, err := h.transfers(TransferQuery{})

	require.NoError(t, err)
	assert.Equal(t, []string{
		"in-newer", "in-high", "in-low", "out-low", "pending", "pool",
	}, transferHashes(got))
}

func TestTransfersConvertsEntries(t *testing.T) {
	h := newHarness(t, defaultOptions())
	h.create(t, "main")
	h.state.Set(func(s *walletrpctest.State) {
		s.Transfers = walletrpc.Transfers{
			Out: []walletrpc.TransferEntry{{
				TxID:          "txid",
				PaymentID:     "0000000000000000",
				Height:        3_000_001,
				Timestamp:     baseTimestamp,
				Amount:        2 * oneXMR,
				Fee:           30_000_000,
				Note:          "rent",
				SubaddrIndex:  walletrpc.SubaddressIndex{Major: 1, Minor: 4},
				Address:       "recipient",
				Confirmations: 12,
			}},
		}
	})

	got, err := h.transfers(TransferQuery{})

	require.NoError(t, err)
	assert.Equal(t, []api.TransferEntry{{
		TxHash:          "txid",
		Direction:       api.TransferDirectionOut,
		Amount:          "2000000000000",
		AmountXmr:       "2.000000000000",
		Fee:             "30000000",
		FeeXmr:          "0.000030000000",
		Height:          3_000_001,
		Timestamp:       time.Unix(baseTimestamp, 0).UTC(),
		Confirmations:   12,
		Address:         "recipient",
		AccountIndex:    1,
		SubaddressIndex: 4,
		Note:            new("rent"),
	}}, got.Transfers)
}

func TestTransfersRejectsBadQueriesBeforeAskingTheWallet(t *testing.T) {
	testCases := []struct {
		name    string
		query   TransferQuery
		wantIs  error
		wantMsg string
	}{
		{
			name:   "account below zero",
			query:  TransferQuery{AccountIndex: new(int64(-1))},
			wantIs: ErrInvalidAccount,
		},
		{
			name:   "account past the limit",
			query:  TransferQuery{AccountIndex: new(int64(maxIndex))},
			wantIs: ErrInvalidAccount,
		},
		{
			name:    "zero limit",
			query:   TransferQuery{Limit: new(0)},
			wantMsg: "limit must be 1-1000",
		},
		{
			name:    "oversized limit",
			query:   TransferQuery{Limit: new(api.MaxListLimit + 1)},
			wantMsg: "limit must be 1-1000",
		},
		{
			name:    "negative offset",
			query:   TransferQuery{Offset: new(-1)},
			wantMsg: "offset must not be negative",
		},
		{
			name: "unknown direction",
			query: TransferQuery{
				Directions: []api.TransferDirection{"sideways"},
			},
			wantMsg: `unknown direction "sideways"`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, defaultOptions())
			h.create(t, "main")

			got, err := h.transfers(tc.query)

			if tc.wantIs != nil {
				require.ErrorIs(t, err, tc.wantIs)
			} else {
				requireInvalid(t, err, tc.wantMsg)
			}

			assert.Equal(t, api.TransferList{}, got)
			assert.Zero(t, h.state.Called("get_transfers"))
		})
	}
}

func TestTransfersAcceptsEveryDirection(t *testing.T) {
	h := newHarness(t, defaultOptions())
	h.create(t, "main")

	got, err := h.service.Transfers(
		context.Background(), "main", TransferQuery{
			Directions: []api.TransferDirection{
				api.TransferDirectionIn,
				api.TransferDirectionOut,
				api.TransferDirectionPending,
				api.TransferDirectionFailed,
				api.TransferDirectionPool,
			},
			AccountIndex: new(int64(1)),
		},
	)

	require.NoError(t, err)
	assert.Equal(t, 0, got.Total)
	assert.Equal(t, 1, h.state.Called("get_transfers"))
}

func transferRequest(amount string) api.TransferRequest {
	return api.TransferRequest{
		Destinations: []api.Destination{
			{Address: "8recipient", Amount: amount},
		},
	}
}

func TestTransferSendsAndConvertsTheResult(t *testing.T) {
	h := newHarness(t, defaultOptions())
	h.create(t, "main")
	h.fund(10 * oneXMR)

	got, err := h.transfer(api.TransferRequest{
		Destinations: []api.Destination{
			{Address: "8recipient-a", Amount: "1000000000000"},
			{Address: "8recipient-b", Amount: "250000000000"},
		},
		AccountIndex:      new(int64(1)),
		SubaddressIndices: &[]int64{2, 3},
		Priority:          new(api.TransferPriorityHigh),
	})

	require.NoError(t, err)
	assert.Equal(t, api.TransferResult{
		TxHash:    walletrpctest.FakeTxHash,
		TxKey:     "fakekey",
		Amount:    "1250000000000",
		AmountXmr: "1.250000000000",
		Fee:       "30000000",
		FeeXmr:    "0.000030000000",
		Weight:    1500,
		Relayed:   true,
	}, got)

	sent := h.lastTransfer()
	assert.Equal(t, []any{
		map[string]any{"amount": 1e12, "address": "8recipient-a"},
		map[string]any{"amount": 2.5e11, "address": "8recipient-b"},
	}, sent["destinations"])
	assert.InDelta(t, 1, sent["account_index"], 0)
	assert.Equal(t, []any{2.0, 3.0}, sent["subaddr_indices"])
	assert.InDelta(t, walletrpc.PriorityElevated, sent["priority"], 0)
	assert.Equal(t, false, sent["do_not_relay"])
	assert.Equal(t, true, sent["get_tx_key"])
}

func TestTransferDefaultsToAccountZeroAndDefaultPriority(t *testing.T) {
	h := newHarness(t, defaultOptions())
	h.create(t, "main")
	h.fund(10 * oneXMR)

	got, err := h.service.Transfer(
		context.Background(), "main", transferRequest("1000000000000"),
	)

	require.NoError(t, err)
	assert.True(t, got.Relayed)

	sent := h.lastTransfer()
	assert.InDelta(t, 0, sent["account_index"], 0)
	assert.InDelta(t, walletrpc.PriorityDefault, sent["priority"], 0)
	assert.Equal(t, false, sent["do_not_relay"])
	assert.Nil(t, sent["subaddr_indices"])
}

func TestTransferDryRunDoesNotRelay(t *testing.T) {
	h := newHarness(t, defaultOptions())
	h.create(t, "main")
	h.fund(10 * oneXMR)

	request := transferRequest("1000000000000")
	request.DryRun = new(true)

	got, err := h.service.Transfer(context.Background(), "main", request)

	require.NoError(t, err)
	assert.False(t, got.Relayed)
	assert.Equal(t, true, h.lastTransfer()["do_not_relay"])
}

func TestTransferPriorities(t *testing.T) {
	testCases := map[api.TransferPriority]uint32{
		api.TransferPriorityDefault: walletrpc.PriorityDefault,
		api.TransferPriorityLow:     walletrpc.PriorityUnimport,
		api.TransferPriorityNormal:  walletrpc.PriorityNormal,
		api.TransferPriorityHigh:    walletrpc.PriorityElevated,
		api.TransferPriorityHighest: walletrpc.PriorityPriority,
	}

	h := newHarness(t, defaultOptions())
	h.create(t, "main")
	h.fund(10 * oneXMR)

	for priority, want := range testCases {
		t.Run(string(priority), func(t *testing.T) {
			request := transferRequest("1000000000000")
			request.Priority = new(priority)

			_, err := h.service.Transfer(context.Background(), "main", request)

			require.NoError(t, err)
			assert.InDelta(t, want, h.lastTransfer()["priority"], 0)
		})
	}
}

func TestTransferRefusedByTheWriteSwitches(t *testing.T) {
	testCases := []struct {
		name    string
		options Options
		wantIs  error
		notIs   error
	}{
		{
			name:    "read-only with transfers allowed",
			options: Options{ReadOnly: true, AllowTransfers: true},
			wantIs:  access.ErrReadOnly,
			notIs:   ErrTransfersDisabled,
		},
		{
			name:    "read-only wins over transfers disabled",
			options: Options{ReadOnly: true, AllowTransfers: false},
			wantIs:  access.ErrReadOnly,
			notIs:   ErrTransfersDisabled,
		},
		{
			name:    "transfers disabled",
			options: Options{AllowTransfers: false},
			wantIs:  ErrTransfersDisabled,
			notIs:   access.ErrReadOnly,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, tc.options)
			h.create(t, "main")
			h.fund(10 * oneXMR)

			nodeReads := h.daemon.calls

			got, err := h.service.Transfer(
				context.Background(), "main", transferRequest("1000000000000"),
			)

			require.ErrorIs(t, err, tc.wantIs)
			require.ErrorIs(t, err, commerr.ErrPermissionDenied)
			require.NotErrorIs(t, err, tc.notIs)
			assert.Equal(t, api.TransferResult{}, got)
			assert.Zero(t, h.state.Called("transfer"))
			assert.Equal(t, nodeReads, h.daemon.calls)
		})
	}
}

func TestDisablingTransfersLeavesOtherWritesWorking(t *testing.T) {
	h := newHarness(t, Options{AllowTransfers: false})

	_, err := h.service.Create(context.Background(), api.CreateWalletRequest{
		Name: "main", Password: walletPassword,
	})
	require.NoError(t, err)

	_, err = h.service.CreateAddress(
		context.Background(), "main",
		api.CreateAddressRequest{Label: new("still allowed")},
	)
	require.NoError(t, err)

	_, err = h.service.RestoreFromSeed(
		context.Background(),
		api.RestoreFromSeedRequest{
			Name: "restored", Password: walletPassword, Seed: "words",
		},
	)
	require.NoError(t, err)
}

func TestTransferRejectsInvalidRequestsBeforeTouchingTheWallet(t *testing.T) {
	testCases := []struct {
		name    string
		request api.TransferRequest
		wantIs  error
		wantMsg string
	}{
		{
			name:    "no destinations",
			request: api.TransferRequest{},
			wantMsg: "destinations must hold 1-16 entries",
		},
		{
			name:    "zero amount",
			request: transferRequest("0"),
			wantMsg: `amount "0" must be a positive integer of atomic units`,
		},
		{
			name:    "fractional amount",
			request: transferRequest("1.5"),
			wantMsg: `amount "1.5" must be a positive integer of atomic units`,
		},
		{
			name: "empty destination address",
			request: api.TransferRequest{
				Destinations: []api.Destination{{Address: "", Amount: "1"}},
			},
			wantIs: ErrInvalidAddress,
		},
		{
			name: "account out of range",
			request: api.TransferRequest{
				Destinations: transferRequest("1").Destinations,
				AccountIndex: new(int64(-1)),
			},
			wantIs: ErrInvalidAccount,
		},
		{
			name: "subaddress out of range",
			request: api.TransferRequest{
				Destinations:      transferRequest("1").Destinations,
				SubaddressIndices: &[]int64{maxIndex},
			},
			wantIs: ErrInvalidAccount,
		},
		{
			name: "unknown priority",
			request: api.TransferRequest{
				Destinations: transferRequest("1").Destinations,
				Priority:     new(api.TransferPriority("asap")),
			},
			wantMsg: `unknown priority "asap"`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, defaultOptions())
			h.create(t, "main")

			nodeReads := h.daemon.calls

			got, err := h.transfer(tc.request)

			if tc.wantIs != nil {
				require.ErrorIs(t, err, tc.wantIs)
			} else {
				requireInvalid(t, err, tc.wantMsg)
			}

			assert.Equal(t, api.TransferResult{}, got)
			assert.Zero(t, h.state.Called("transfer"))
			assert.Equal(t, nodeReads, h.daemon.calls)
		})
	}
}

func TestTransferNeedsAnOpenWalletBeforeAskingTheNode(t *testing.T) {
	h := newHarness(t, defaultOptions())

	_, err := h.service.Transfer(
		context.Background(), "ghost", transferRequest("1"),
	)

	require.ErrorIs(t, err, walletpool.ErrWalletNotOpen)
	assert.Zero(t, h.daemon.calls)
}

func TestTransferNodeSyncGate(t *testing.T) {
	h := newHarness(t, defaultOptions())
	h.create(t, "main")
	h.fund(10 * oneXMR)

	t.Run("node not synchronized", func(t *testing.T) {
		h.daemon.info = daemonrpc.Info{Height: chainHeight, Synchronized: false}
		heightReads := h.state.Called("get_height")

		got, err := h.service.Transfer(
			context.Background(), "main", transferRequest("1000000000000"),
		)

		require.ErrorIs(t, err, ErrNodeNotSynced)
		require.ErrorIs(t, err, commerr.ErrNotReady)
		assert.Equal(t, api.TransferResult{}, got)
		assert.Equal(t, heightReads, h.state.Called("get_height"))
		assert.Zero(t, h.state.Called("transfer"))
	})

	t.Run("node info unavailable", func(t *testing.T) {
		h.daemon.err = commerr.ErrConnectFailed
		heightReads := h.state.Called("get_height")

		_, err := h.service.Transfer(
			context.Background(), "main", transferRequest("1000000000000"),
		)

		require.ErrorIs(t, err, commerr.ErrConnectFailed)
		require.NotErrorIs(t, err, ErrNodeNotSynced)
		assert.Equal(t, heightReads, h.state.Called("get_height"))
		assert.Zero(t, h.state.Called("transfer"))

		h.daemon.err = nil
	})
}

func TestTransferWalletSyncGate(t *testing.T) {
	testCases := []struct {
		name         string
		walletHeight uint64
		nodeHeight   uint64
		wantSyncing  bool
	}{
		{"one block behind", 99, 100, true},
		{"far behind", 1, 100, true},
		{"level with the node", 100, 100, false},
		{"ahead of the node", 105, 100, false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, defaultOptions())
			h.create(t, "main")
			h.daemon.info = daemonrpc.Info{
				Height: tc.nodeHeight, Synchronized: true,
			}
			h.setWalletHeight(tc.walletHeight)
			h.fund(10 * oneXMR)

			got, err := h.service.Transfer(
				context.Background(), "main", transferRequest("1000000000000"),
			)

			if tc.wantSyncing {
				require.ErrorIs(t, err, ErrWalletSyncing)
				require.ErrorIs(t, err, commerr.ErrNotReady)
				assert.Equal(t, api.TransferResult{}, got)
				assert.Zero(t, h.state.Called("transfer"))

				return
			}

			require.NoError(t, err)
			assert.Equal(t, walletrpctest.FakeTxHash, got.TxHash)
		})
	}
}

func TestTransferMapsWalletHeightFailures(t *testing.T) {
	h := newHarness(t, defaultOptions())
	h.create(t, "main")
	h.state.Fail("get_height", walletrpc.CodeNoDaemonConnection, "no node")

	_, err := h.service.Transfer(
		context.Background(), "main", transferRequest("1000000000000"),
	)

	require.ErrorIs(t, err, commerr.ErrUnavailable)
	assert.Zero(t, h.state.Called("transfer"))
}

func TestTransferMapsWalletFailures(t *testing.T) {
	testCases := []struct {
		name    string
		code    int
		wantIs  error
		wantMsg string
	}{
		{
			name:   "not enough money",
			code:   walletrpc.CodeNotEnoughMoney,
			wantIs: ErrInsufficientFunds,
		},
		{
			name:   "view-only wallet",
			code:   walletrpc.CodeWatchOnly,
			wantIs: ErrViewOnly,
		},
		{
			name:    "zero amount",
			code:    walletrpc.CodeZeroAmount,
			wantMsg: "every destination amount must be above zero",
		},
		{
			name:   "transaction too large",
			code:   walletrpc.CodeTxTooLarge,
			wantIs: ErrTransferRejected,
		},
		{
			name:   "bad address",
			code:   walletrpc.CodeWrongAddress,
			wantIs: ErrInvalidAddress,
		},
		{
			name:   "node connection lost",
			code:   walletrpc.CodeNoDaemonConnection,
			wantIs: commerr.ErrUnavailable,
		},
		{
			name:   "unmapped code",
			code:   walletrpc.CodeUnknownError,
			wantIs: jsonrpc.ErrRPC,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, defaultOptions())
			h.create(t, "main")
			h.state.Fail("transfer", tc.code, "failure")

			got, err := h.service.Transfer(
				context.Background(), "main", transferRequest("1000000000000"),
			)

			if tc.wantIs != nil {
				require.ErrorIs(t, err, tc.wantIs)
			} else {
				requireInvalid(t, err, tc.wantMsg)
			}

			assert.Equal(t, api.TransferResult{}, got)
		})
	}
}

func TestTransferReportsInsufficientFundsFromTheWallet(t *testing.T) {
	h := newHarness(t, defaultOptions())
	h.create(t, "main")
	h.fund(oneXMR)

	_, err := h.service.Transfer(
		context.Background(), "main", transferRequest("1000000000000"),
	)

	require.ErrorIs(t, err, ErrInsufficientFunds)
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
}

func TestReadOnlyRefusesEveryWriteAndRunsNoWalletRPC(t *testing.T) {
	h := newHarness(t, defaultOptions())
	h.create(t, "main")

	readOnly := h.serviceWith(Options{ReadOnly: true, AllowTransfers: true})
	callsBefore := h.writeCalls()
	procsBefore := len(h.launcher.Procs())
	ctx := context.Background()

	writes := map[string]func() error{
		"create": func() error {
			_, err := readOnly.Create(ctx, api.CreateWalletRequest{
				Name: "new", Password: walletPassword,
			})

			return err
		},
		"restore from seed": func() error {
			_, err := readOnly.RestoreFromSeed(ctx, api.RestoreFromSeedRequest{
				Name: "new", Password: walletPassword, Seed: "words",
			})

			return err
		},
		"restore from keys": func() error {
			_, err := readOnly.RestoreFromKeys(ctx, api.RestoreFromKeysRequest{
				Name:     "new",
				Password: walletPassword,
				Address:  "a",
				ViewKey:  "v",
			})

			return err
		},
		"create address": func() error {
			_, err := readOnly.CreateAddress(
				ctx, "main", api.CreateAddressRequest{},
			)

			return err
		},
		"transfer": func() error {
			_, err := readOnly.Transfer(ctx, "main", transferRequest("1"))

			return err
		},
	}

	for name, write := range writes {
		t.Run(name, func(t *testing.T) {
			err := write()

			require.ErrorIs(t, err, access.ErrReadOnly)
			require.ErrorIs(t, err, commerr.ErrPermissionDenied)
		})
	}

	assert.Equal(t, callsBefore, h.writeCalls())
	assert.Len(t, h.launcher.Procs(), procsBefore)
	assert.Equal(t, 1, h.pool.OpenCount())
}

func TestReadOnlyStillAllowsReadingOpeningAndClosing(t *testing.T) {
	h := newHarness(t, defaultOptions())
	h.create(t, "main", "spare")

	_, err := h.service.Close(context.Background(), "spare")
	require.NoError(t, err)

	readOnly := h.serviceWith(Options{ReadOnly: true})
	ctx := context.Background()

	list, err := readOnly.List()
	require.NoError(t, err)
	assert.Len(t, list.Wallets, 2)

	status, err := readOnly.Status(ctx, "main")
	require.NoError(t, err)
	assert.True(t, status.Open)

	_, err = readOnly.Balance(ctx, "main", nil)
	require.NoError(t, err)

	_, err = readOnly.Addresses(ctx, "main", nil)
	require.NoError(t, err)

	_, err = readOnly.Transfers(ctx, "main", TransferQuery{})
	require.NoError(t, err)

	opened, err := readOnly.Open(ctx, "spare", walletPassword)
	require.NoError(t, err)
	assert.True(t, opened.Open)

	closed, err := readOnly.Close(ctx, "spare")
	require.NoError(t, err)
	assert.False(t, closed.Open)
}

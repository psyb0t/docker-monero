package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/psyb0t/docker-monero/internal/pkg/http/api"
	"github.com/psyb0t/docker-monero/internal/pkg/monero/walletrpc"
	"github.com/psyb0t/docker-monero/internal/pkg/monero/walletrpc/walletrpctest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	messageInvalidName = "wallet names use letters, digits, _ and -"
	messageBadAccount  = "the account or subaddress index is out of range"
	codeWalletNotOpen  = "WALLET_NOT_OPEN"
	messageNotOpen     = "the wallet is not open"
	codeReadOnly       = "READ_ONLY"
	messageReadOnly    = "this deployment is read-only"
	nodeHeight         = 1000
)

// requireErrorContaining asserts the envelope for a message too long to spell
// out in full.
func requireErrorContaining(
	t *testing.T,
	resp response,
	status int,
	code, fragment string,
) {
	t.Helper()

	require.Equal(t, status, resp.status, resp.text())

	envelope := decodeAs[map[string]any](t, resp)
	assert.Equal(t, code, envelope["code"])
	assert.Contains(t, envelope["message"], fragment)
}

func syncedWallet(state *walletrpctest.State) { state.Height = nodeHeight }

func TestListWalletsOfAFreshService(t *testing.T) {
	s := newStack(t, stackOptions{})

	resp := s.get(t, "/v1/wallets")
	requireStatus(t, resp, http.StatusOK)

	list := decodeAs[api.WalletList](t, resp)
	assert.Equal(t, maxOpenWallets, list.MaxOpen)
	assert.Zero(t, list.OpenCount)
	assert.NotNil(t, list.Wallets)
	assert.Contains(t, resp.text(), `"wallets":[]`)
}

func TestListWalletsIsSortedAndMarksOpenOnes(t *testing.T) {
	s := newStack(t, stackOptions{})

	for _, name := range []string{"charlie", "alpha", "bravo"} {
		s.createWallet(t, name)
	}

	requireStatus(t, s.post(t, "/v1/wallets/bravo/close", ""), http.StatusOK)

	resp := s.get(t, "/v1/wallets")
	requireStatus(t, resp, http.StatusOK)

	assert.Equal(t, api.WalletList{
		MaxOpen:   maxOpenWallets,
		OpenCount: 2,
		Wallets: []api.WalletSummary{
			{Name: "alpha", Open: true},
			{Name: "bravo", Open: false},
			{Name: "charlie", Open: true},
		},
	}, decodeAs[api.WalletList](t, resp))
}

func TestListWalletsIgnoresFilesThatAreNotWallets(t *testing.T) {
	s := newStack(t, stackOptions{})
	dir := s.wallets.WalletDir

	writeFile(t, dir, "real.keys")
	writeFile(t, dir, "real")
	writeFile(t, dir, "notes.txt")
	writeFile(t, dir, "bad name.keys")
	require.NoError(t, os.Mkdir(filepath.Join(dir, "subdir.keys"), 0o700))

	resp := s.get(t, "/v1/wallets")
	requireStatus(t, resp, http.StatusOK)

	assert.Equal(t, []api.WalletSummary{{Name: "real", Open: false}},
		decodeAs[api.WalletList](t, resp).Wallets)
}

func TestListWalletsWhenTheWalletDirectoryIsGone(t *testing.T) {
	s := newStack(t, stackOptions{})
	require.NoError(t, os.RemoveAll(s.wallets.WalletDir))

	resp := s.get(t, "/v1/wallets")

	requireError(
		t, resp, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR",
		"the request could not be completed",
	)
	assert.NotContains(t, resp.text(), s.wallets.WalletDir)
}

func TestCreateWalletReportsItsSyncState(t *testing.T) {
	s := newStack(t, stackOptions{})
	s.wallets.Set(func(state *walletrpctest.State) { state.Height = 900 })

	before := time.Now().Add(-time.Minute)
	resp := s.post(t, "/v1/wallets", `{"name":"alice","password":"pw"}`)
	requireStatus(t, resp, http.StatusCreated)
	assert.Equal(t, "application/json", resp.header.Get("Content-Type"))

	status := decodeAs[api.WalletStatus](t, resp)
	require.NotNil(t, status.OpenedAt)
	require.NotNil(t, status.Address)
	require.NotNil(t, status.WalletHeight)
	require.NotNil(t, status.NodeHeight)
	require.NotNil(t, status.NodeSynchronized)
	require.NotNil(t, status.BlocksBehind)
	require.NotNil(t, status.SyncPercent)
	require.NotNil(t, status.Syncing)

	assert.Equal(t, "alice", status.Name)
	assert.True(t, status.Open)
	assert.Equal(t, walletrpctest.FakeAddress, *status.Address)
	assert.True(t, status.OpenedAt.After(before))
	assert.EqualValues(t, 900, *status.WalletHeight)
	assert.EqualValues(t, nodeHeight, *status.NodeHeight)
	assert.True(t, *status.NodeSynchronized)
	assert.EqualValues(t, 100, *status.BlocksBehind)
	assert.InDelta(t, 90, *status.SyncPercent, 1e-9)
	assert.True(t, *status.Syncing)

	assert.Equal(t, 1, s.wallets.Called("create_wallet"))
	assert.Equal(t, 1, s.wallets.Called("auto_refresh"))
}

func TestCreateWalletWhileTheNodeIsUnreachable(t *testing.T) {
	s := newStack(t, stackOptions{})
	s.daemon.failHTTP("get_info", http.StatusInternalServerError)

	resp := s.post(t, "/v1/wallets", `{"name":"alice","password":"pw"}`)
	requireStatus(t, resp, http.StatusCreated)

	status := decodeAs[api.WalletStatus](t, resp)
	assert.True(t, status.Open)
	assert.NotNil(t, status.Address)
	assert.Nil(t, status.NodeHeight)
	assert.Nil(t, status.NodeSynchronized)
	assert.Nil(t, status.Syncing)
}

func TestCreateWalletPasswordLength(t *testing.T) {
	s := newStack(t, stackOptions{})

	t.Run("empty password is allowed", func(t *testing.T) {
		resp := s.post(t, "/v1/wallets", `{"name":"empty","password":""}`)

		requireStatus(t, resp, http.StatusCreated)
	})

	t.Run("exactly the limit", func(t *testing.T) {
		resp := s.post(t, "/v1/wallets", jsonBody(t, map[string]any{
			"name": "exact", "password": strings.Repeat("p", 256),
		}))

		requireStatus(t, resp, http.StatusCreated)
	})

	t.Run("one past the limit", func(t *testing.T) {
		resp := s.post(t, "/v1/wallets", jsonBody(t, map[string]any{
			"name": "long", "password": strings.Repeat("p", 257),
		}))

		requireError(
			t, resp, http.StatusBadRequest, codeBadRequest,
			"password is longer than 256 characters",
		)
	})
}

func TestCreateWalletRejections(t *testing.T) {
	s := newStack(t, stackOptions{})
	s.createWallet(t, "taken")

	testCases := []struct {
		name       string
		body       string
		wantStatus int
		wantCode   string
		wantText   string
	}{
		{
			name:       "empty name",
			body:       `{"name":"","password":"pw"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   codeBadRequest,
			wantText:   messageInvalidName,
		},
		{
			name:       "missing name",
			body:       `{"password":"pw"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   codeBadRequest,
			wantText:   messageInvalidName,
		},
		{
			name:       "leading dot",
			body:       `{"name":".hidden","password":"pw"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   codeBadRequest,
			wantText:   messageInvalidName,
		},
		{
			name:       "leading dash",
			body:       `{"name":"-x","password":"pw"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   codeBadRequest,
			wantText:   messageInvalidName,
		},
		{
			name:       "path separator",
			body:       `{"name":"a/b","password":"pw"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   codeBadRequest,
			wantText:   messageInvalidName,
		},
		{
			name:       "path traversal",
			body:       `{"name":"../escape","password":"pw"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   codeBadRequest,
			wantText:   messageInvalidName,
		},
		{
			name:       "space",
			body:       `{"name":"a b","password":"pw"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   codeBadRequest,
			wantText:   messageInvalidName,
		},
		{
			name:       "unicode",
			body:       `{"name":"wället","password":"pw"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   codeBadRequest,
			wantText:   messageInvalidName,
		},
		{
			name: "name of 65 characters",
			body: `{"name":"` + strings.Repeat("a", 65) +
				`","password":"pw"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   codeBadRequest,
			wantText:   messageInvalidName,
		},
		{
			name:       "duplicate",
			body:       `{"name":"taken","password":"pw"}`,
			wantStatus: http.StatusConflict,
			wantCode:   "WALLET_EXISTS",
			wantText:   "a wallet with that name already exists",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resp := s.post(t, "/v1/wallets", tc.body)

			requireErrorContaining(
				t, resp, tc.wantStatus, tc.wantCode, tc.wantText,
			)
		})
	}

	entries, err := os.ReadDir(filepath.Dir(s.wallets.WalletDir))
	require.NoError(t, err)

	for _, entry := range entries {
		assert.NotEqual(t, "escape.keys", entry.Name())
	}
}

func TestCreateWalletAcceptsTheLongestName(t *testing.T) {
	s := newStack(t, stackOptions{})
	name := "Z" + strings.Repeat("a_-", 21) // 64 characters

	require.Len(t, name, 64)

	resp := s.post(t, "/v1/wallets", jsonBody(t, map[string]any{
		"name": name, "password": "pw",
	}))

	requireStatus(t, resp, http.StatusCreated)
	assert.Equal(t, name, decodeAs[api.WalletStatus](t, resp).Name)
}

func TestCreateWalletRejectsMalformedBodies(t *testing.T) {
	s := newStack(t, stackOptions{})

	for name, body := range map[string]string{
		"empty":                "",
		"truncated":            `{"name":"a"`,
		"not json":             `name=a`,
		"name is a number":     `{"name":1,"password":"pw"}`,
		"password is an array": `{"name":"a","password":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			resp := s.post(t, "/v1/wallets", body)

			requireError(
				t, resp, http.StatusBadRequest, codeBadRequest, messageDecode,
			)
		})
	}

	assert.Zero(t, s.pool.OpenCount())
}

func TestOpenWalletLimit(t *testing.T) {
	s := newStack(t, stackOptions{})

	for _, name := range []string{"one", "two", "three"} {
		s.createWallet(t, name)
	}

	resp := s.post(t, "/v1/wallets", `{"name":"four","password":"pw"}`)
	requireError(
		t, resp, http.StatusConflict, "TOO_MANY_OPEN_WALLETS",
		"the open wallet limit is reached, close a wallet first",
	)

	list := decodeAs[api.WalletList](t, s.get(t, "/v1/wallets"))
	assert.Equal(t, maxOpenWallets, list.OpenCount)

	requireStatus(t, s.post(t, "/v1/wallets/two/close", ""), http.StatusOK)
	s.createWallet(t, "four")
}

func TestWalletLifecycle(t *testing.T) {
	s := newStack(t, stackOptions{})
	s.wallets.Set(syncedWallet)

	created := decodeAs[api.WalletStatus](t, s.post(
		t, "/v1/wallets", `{"name":"alice","password":"secret"}`,
	))
	require.True(t, created.Open)
	require.NotNil(t, created.BlocksBehind)
	require.NotNil(t, created.SyncPercent)
	require.NotNil(t, created.Syncing)
	assert.EqualValues(t, 0, *created.BlocksBehind)
	assert.InDelta(t, 100, *created.SyncPercent, 0)
	assert.False(t, *created.Syncing)

	status := s.aliceStatus(t)
	assert.True(t, status.Open)
	assert.Equal(t, walletrpctest.FakeAddress, *status.Address)

	closed := s.post(t, "/v1/wallets/alice/close", "")
	requireStatus(t, closed, http.StatusOK)
	assert.Equal(t, api.WalletStatus{Name: "alice"},
		decodeAs[api.WalletStatus](t, closed))
	assert.NotContains(t, closed.text(), "address")

	status = decodeAs[api.WalletStatus](t, s.get(t, "/v1/wallets/alice/status"))
	assert.Equal(t, api.WalletStatus{Name: "alice"}, status)

	wrong := s.post(t, "/v1/wallets/alice/open", `{"password":"nope"}`)
	requireError(
		t, wrong, http.StatusForbidden, "INVALID_WALLET_PASSWORD",
		"the wallet password is wrong",
	)
	assert.Zero(t, s.pool.OpenCount())

	reopened := s.post(t, "/v1/wallets/alice/open", `{"password":"secret"}`)
	requireStatus(t, reopened, http.StatusOK)
	assert.True(t, decodeAs[api.WalletStatus](t, reopened).Open)
	assert.Equal(t, 1, s.pool.OpenCount())
}

func TestOpenWalletRejections(t *testing.T) {
	s := newStack(t, stackOptions{})
	s.createWallet(t, "alice")

	testCases := []struct {
		name       string
		path       string
		body       string
		wantStatus int
		wantCode   string
		wantText   string
	}{
		{
			name:       "already open",
			path:       "/v1/wallets/alice/open",
			body:       `{"password":"pw"}`,
			wantStatus: http.StatusConflict,
			wantCode:   "WALLET_ALREADY_OPEN",
			wantText:   "the wallet is already open",
		},
		{
			name:       "no such wallet",
			path:       "/v1/wallets/ghost/open",
			body:       `{"password":"pw"}`,
			wantStatus: http.StatusNotFound,
			wantCode:   codeNotFound,
			wantText:   "no such wallet",
		},
		{
			name:       "password too long",
			path:       "/v1/wallets/alice/open",
			body:       `{"password":"` + strings.Repeat("p", 257) + `"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   codeBadRequest,
			wantText:   "password is longer than 256 characters",
		},
		{
			name:       "empty body",
			path:       "/v1/wallets/alice/open",
			body:       "",
			wantStatus: http.StatusBadRequest,
			wantCode:   codeBadRequest,
			wantText:   messageDecode,
		},
		{
			name:       "malformed body",
			path:       "/v1/wallets/alice/open",
			body:       `{"password":`,
			wantStatus: http.StatusBadRequest,
			wantCode:   codeBadRequest,
			wantText:   messageDecode,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resp := s.post(t, tc.path, tc.body)

			requireError(t, resp, tc.wantStatus, tc.wantCode, tc.wantText)
		})
	}
}

func TestOpenWalletWhenWalletRPCFails(t *testing.T) {
	s := newStack(t, stackOptions{})
	s.createWallet(t, "alice")
	requireStatus(t, s.post(t, "/v1/wallets/alice/close", ""), http.StatusOK)

	s.wallets.Fail("open_wallet", walletrpc.CodeUnknownError, "disk exploded")

	resp := s.post(t, "/v1/wallets/alice/open", `{"password":"pw"}`)

	requireError(
		t, resp, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR",
		"the request could not be completed",
	)
	assert.NotContains(t, resp.text(), "exploded")
	assert.Zero(t, s.pool.OpenCount())
}

func TestCloseWalletThatIsNotOpen(t *testing.T) {
	s := newStack(t, stackOptions{})

	resp := s.post(t, "/v1/wallets/ghost/close", "")
	requireError(
		t, resp, http.StatusConflict, codeWalletNotOpen, messageNotOpen,
	)

	s.createWallet(t, "alice")
	requireStatus(t, s.post(t, "/v1/wallets/alice/close", ""), http.StatusOK)

	resp = s.post(t, "/v1/wallets/alice/close", "")
	requireError(
		t, resp, http.StatusConflict, codeWalletNotOpen, messageNotOpen,
	)
}

func TestCloseWalletWhenSavingFails(t *testing.T) {
	s := newStack(t, stackOptions{})
	s.createWallet(t, "alice")
	s.wallets.Fail("close_wallet", walletrpc.CodeUnknownError, "cannot save")

	resp := s.post(t, "/v1/wallets/alice/close", "")

	requireError(
		t, resp, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR",
		"the request could not be completed",
	)
	assert.Zero(t, s.pool.OpenCount(), "the process is stopped regardless")
}

func TestWalletStatusOfAWalletThatDoesNotExist(t *testing.T) {
	s := newStack(t, stackOptions{})

	resp := s.get(t, "/v1/wallets/ghost/status")

	requireError(t, resp, http.StatusNotFound, codeNotFound, "no such wallet")
}

func TestWalletStatusWhenTheNodeIsNotSynchronized(t *testing.T) {
	s := newStack(t, stackOptions{})
	s.createWallet(t, "alice")
	s.wallets.Set(syncedWallet)
	s.daemon.update(func(fake *fakeMonerod) { fake.info.Synchronized = false })

	status := s.aliceStatus(t)

	require.NotNil(t, status.Syncing)
	require.NotNil(t, status.NodeSynchronized)
	assert.True(t, *status.Syncing, "a caught-up wallet on an unsynced node")
	assert.False(t, *status.NodeSynchronized)
	assert.EqualValues(t, 0, *status.BlocksBehind)
}

func TestAWalletWhoseProcessDiesReadsAsClosed(t *testing.T) {
	s := newStack(t, stackOptions{})
	s.createWallet(t, "alice")

	s.launcher.Last().Exit(errors.New("killed"))

	require.Eventually(t, func() bool {
		resp, err := s.try(http.MethodGet, "/v1/wallets/alice/status", "")
		if err != nil || resp.status != http.StatusOK {
			return false
		}

		var status api.WalletStatus

		return json.Unmarshal(resp.body, &status) == nil && !status.Open
	}, startTimeout, 5*time.Millisecond)

	resp := s.get(t, "/v1/wallets/alice/balance")
	requireError(
		t, resp, http.StatusConflict, codeWalletNotOpen, messageNotOpen,
	)

	reopened := s.post(t, "/v1/wallets/alice/open", `{"password":"pw"}`)
	requireStatus(t, reopened, http.StatusOK)
}

func TestPathWalletNamesAreValidated(t *testing.T) {
	s := newStack(t, stackOptions{})

	endpoints := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/open", `{"password":"x"}`},
		{http.MethodPost, "/close", ""},
		{http.MethodGet, "/status", ""},
		{http.MethodGet, "/balance", ""},
		{http.MethodGet, "/addresses", ""},
		{http.MethodPost, "/addresses", `{}`},
		{http.MethodGet, "/transfers", ""},
		{
			http.MethodPost, "/transfers",
			`{"destinations":[{"address":"4A","amount":"1"}]}`,
		},
	}

	for _, name := range []string{"..%2Fescape", "bad%20name", ".hidden"} {
		for _, endpoint := range endpoints {
			t.Run(name+endpoint.path+endpoint.method, func(t *testing.T) {
				resp := s.send(
					t, endpoint.method, "/v1/wallets/"+name+endpoint.path,
					endpoint.body,
				)

				requireErrorContaining(
					t, resp, http.StatusBadRequest, codeBadRequest,
					messageInvalidName,
				)
			})
		}
	}

	entries, err := os.ReadDir(s.wallets.WalletDir)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestRestoreWalletFromSeed(t *testing.T) {
	s := newStack(t, stackOptions{})
	s.wallets.Set(syncedWallet)

	resp := s.post(t, "/v1/wallets/restore/seed", jsonBody(t, map[string]any{
		"name":          "seeded",
		"password":      "pw",
		"seed":          "alpha bravo charlie",
		"seedOffset":    "offset",
		"restoreHeight": 100,
		"language":      "English",
	}))
	requireStatus(t, resp, http.StatusCreated)

	status := decodeAs[api.WalletStatus](t, resp)
	assert.Equal(t, "seeded", status.Name)
	assert.True(t, status.Open)
	assert.Equal(t, 1, s.wallets.Called("restore_deterministic_wallet"))

	again := s.post(t, "/v1/wallets/restore/seed", jsonBody(t, map[string]any{
		"name": "seeded", "password": "pw", "seed": "alpha",
	}))
	requireError(
		t, again, http.StatusConflict, "WALLET_EXISTS",
		"a wallet with that name already exists",
	)
}

func TestRestoreWalletFromSeedRejections(t *testing.T) {
	s := newStack(t, stackOptions{})

	testCases := []struct {
		name     string
		body     string
		wantText string
	}{
		{
			name:     "missing seed",
			body:     `{"name":"w","password":"pw"}`,
			wantText: "seed is required",
		},
		{
			name: "negative restore height",
			body: `{"name":"w","password":"pw","seed":"a b",` +
				`"restoreHeight":-1}`,
			wantText: "restoreHeight must not be negative",
		},
		{
			name: "password too long",
			body: `{"name":"w","seed":"a b","password":"` +
				strings.Repeat("p", 257) + `"}`,
			wantText: "password is longer than 256 characters",
		},
		{
			name:     "malformed body",
			body:     `{"name":`,
			wantText: messageDecode,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resp := s.post(t, "/v1/wallets/restore/seed", tc.body)

			requireError(
				t, resp, http.StatusBadRequest, codeBadRequest, tc.wantText,
			)
		})
	}

	assert.Zero(t, s.wallets.Called("restore_deterministic_wallet"))
}

func TestRestoreWalletFromKeys(t *testing.T) {
	s := newStack(t, stackOptions{})

	t.Run("full wallet", func(t *testing.T) {
		resp := s.post(t, pathRestoreKeys, jsonBody(t, map[string]any{
			"name":          "full",
			"password":      "pw",
			"address":       "4Address",
			"viewKey":       "view",
			"spendKey":      "spend",
			"restoreHeight": 5,
		}))

		requireStatus(t, resp, http.StatusCreated)
		assert.Equal(t, "full", decodeAs[api.WalletStatus](t, resp).Name)
	})

	t.Run("view-only wallet", func(t *testing.T) {
		resp := s.post(t, pathRestoreKeys, jsonBody(t, map[string]any{
			"name": "watch", "password": "pw", "address": "4A", "viewKey": "v",
		}))

		requireStatus(t, resp, http.StatusCreated)
	})

	t.Run("duplicate", func(t *testing.T) {
		resp := s.post(t, pathRestoreKeys, jsonBody(t, map[string]any{
			"name": "full", "password": "pw", "address": "4A", "viewKey": "v",
		}))

		requireError(
			t, resp, http.StatusConflict, "WALLET_EXISTS",
			"a wallet with that name already exists",
		)
	})

	assert.Equal(t, 2, s.wallets.Called("generate_from_keys"))
}

func TestRestoreWalletFromKeysRejections(t *testing.T) {
	s := newStack(t, stackOptions{})

	testCases := []struct {
		name     string
		body     string
		wantText string
	}{
		{
			name:     "missing address",
			body:     `{"name":"w","password":"pw","viewKey":"v"}`,
			wantText: "address and viewKey are required",
		},
		{
			name:     "missing view key",
			body:     `{"name":"w","password":"pw","address":"4A"}`,
			wantText: "address and viewKey are required",
		},
		{
			name: "negative restore height",
			body: `{"name":"w","password":"pw","address":"4A","viewKey":"v",` +
				`"restoreHeight":-5}`,
			wantText: "restoreHeight must not be negative",
		},
		{
			name:     "malformed body",
			body:     `[`,
			wantText: messageDecode,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resp := s.post(t, pathRestoreKeys, tc.body)

			requireError(
				t, resp, http.StatusBadRequest, codeBadRequest, tc.wantText,
			)
		})
	}

	assert.Zero(t, s.wallets.Called("generate_from_keys"))
}

func TestRestoreWithAKeyWalletRPCRejects(t *testing.T) {
	s := newStack(t, stackOptions{})
	s.wallets.Fail("generate_from_keys", walletrpc.CodeWrongKey, "bad key")

	resp := s.post(t, pathRestoreKeys, jsonBody(t, map[string]any{
		"name": "w", "password": "pw", "address": "4A", "viewKey": "v",
	}))

	requireError(
		t, resp, http.StatusBadRequest, codeBadRequest,
		"the supplied key is not valid",
	)
	assert.Zero(t, s.pool.OpenCount())
}

func TestGetWalletBalance(t *testing.T) {
	s := newStack(t, stackOptions{})
	s.createWallet(t, "alice")
	s.wallets.Set(func(state *walletrpctest.State) {
		state.Balance = 1500000000000
		state.UnlockedBalance = 1000000000000
	})

	resp := s.get(t, "/v1/wallets/alice/balance")
	requireStatus(t, resp, http.StatusOK)

	assert.Equal(t, api.Balance{
		AccountIndex:       0,
		Balance:            "1500000000000",
		BalanceXmr:         "1.500000000000",
		UnlockedBalance:    "1000000000000",
		UnlockedBalanceXmr: "1.000000000000",
		Subaddresses:       []api.SubaddressBalance{},
	}, decodeAs[api.Balance](t, resp))
}

func TestWalletReadsAcceptAnAccountIndex(t *testing.T) {
	s := newStack(t, stackOptions{})
	s.createWallet(t, "alice")

	for _, index := range []string{"0", "2", "2147483647"} {
		t.Run(index, func(t *testing.T) {
			balance := s.get(t, "/v1/wallets/alice/balance?accountIndex="+index)
			requireStatus(t, balance, http.StatusOK)

			addresses := s.get(
				t, "/v1/wallets/alice/addresses?accountIndex="+index,
			)
			requireStatus(t, addresses, http.StatusOK)

			wantIndex := decodeAs[api.Balance](t, balance).AccountIndex
			assert.Equal(
				t, wantIndex,
				decodeAs[api.AddressList](t, addresses).AccountIndex,
			)
			assert.Equal(t, index, fmt.Sprint(wantIndex))
		})
	}
}

func TestWalletReadsRejectBadAccountIndexes(t *testing.T) {
	s := newStack(t, stackOptions{})
	s.createWallet(t, "alice")

	testCases := []struct {
		name     string
		query    string
		wantText string
	}{
		{"negative", "-1", messageBadAccount},
		{"at the 2^31 limit", "2147483648", messageBadAccount},
		{"not a number", "abc", messageDecode},
		{"fraction", "1.5", messageDecode},
		{"empty", "", messageDecode},
	}

	for _, endpoint := range []string{"balance", "addresses"} {
		for _, tc := range testCases {
			t.Run(endpoint+"/"+tc.name, func(t *testing.T) {
				resp := s.get(
					t, "/v1/wallets/alice/"+endpoint+"?accountIndex="+tc.query,
				)

				requireError(
					t, resp, http.StatusBadRequest, codeBadRequest, tc.wantText,
				)
			})
		}
	}
}

func TestWalletReadsOfAWalletThatIsNotOpen(t *testing.T) {
	s := newStack(t, stackOptions{})

	for _, path := range []string{"balance", "addresses", "transfers"} {
		t.Run(path, func(t *testing.T) {
			resp := s.get(t, "/v1/wallets/ghost/"+path)

			requireError(
				t, resp, http.StatusConflict, codeWalletNotOpen, messageNotOpen,
			)
		})
	}

	resp := s.post(t, "/v1/wallets/ghost/addresses", `{}`)
	requireError(
		t, resp, http.StatusConflict, codeWalletNotOpen, messageNotOpen,
	)

	resp = s.post(
		t, "/v1/wallets/ghost/transfers",
		`{"destinations":[{"address":"4A","amount":"1"}]}`,
	)
	requireError(
		t, resp, http.StatusConflict, codeWalletNotOpen, messageNotOpen,
	)
}

func TestWalletReadsMapWalletRPCFailures(t *testing.T) {
	testCases := []struct {
		name       string
		code       int
		wantStatus int
		wantCode   string
		wantText   string
	}{
		{
			name:       "account out of bounds",
			code:       walletrpc.CodeAccountIndexOutOfBound,
			wantStatus: http.StatusBadRequest,
			wantCode:   codeBadRequest,
			wantText:   messageBadAccount,
		},
		{
			name:       "address out of bounds",
			code:       walletrpc.CodeAddressIndexOutOfBound,
			wantStatus: http.StatusBadRequest,
			wantCode:   codeBadRequest,
			wantText:   messageBadAccount,
		},
		{
			name:       "no daemon connection",
			code:       walletrpc.CodeNoDaemonConnection,
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   "SERVICE_UNAVAILABLE",
			wantText:   messageNoMonero,
		},
		{
			name:       "daemon busy",
			code:       walletrpc.CodeDaemonIsBusy,
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   "SERVICE_UNAVAILABLE",
			wantText:   messageNoMonero,
		},
		{
			name:       "anything else",
			code:       -99,
			wantStatus: http.StatusInternalServerError,
			wantCode:   "INTERNAL_SERVER_ERROR",
			wantText:   "the request could not be completed",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			s := newStack(t, stackOptions{})
			s.createWallet(t, "alice")
			s.wallets.Fail("get_balance", tc.code, "wallet says no")

			resp := s.get(t, "/v1/wallets/alice/balance")

			requireError(t, resp, tc.wantStatus, tc.wantCode, tc.wantText)
			assert.NotContains(t, resp.text(), "wallet says no")
		})
	}
}

func TestWalletAddressesAndSubaddressCreation(t *testing.T) {
	s := newStack(t, stackOptions{})
	s.createWallet(t, "alice")

	before := s.aliceAddresses(t)
	assert.Equal(t, walletrpctest.FakeAddress, before.PrimaryAddress)
	assert.Equal(t, []api.Address{{
		Address: walletrpctest.FakeAddress, Label: "Primary account",
	}}, before.Addresses)

	created := s.post(t, "/v1/wallets/alice/addresses", `{"label":"shop"}`)
	requireStatus(t, created, http.StatusCreated)
	assert.Equal(t, api.Address{
		Address: "8FakeSubaddress1", AddressIndex: 1, Label: "shop",
	}, decodeAs[api.Address](t, created))

	unlabelled := s.post(t, "/v1/wallets/alice/addresses", `{}`)
	requireStatus(t, unlabelled, http.StatusCreated)
	assert.Equal(t, api.Address{
		Address: "8FakeSubaddress2", AddressIndex: 2,
	}, decodeAs[api.Address](t, unlabelled))

	after := s.aliceAddresses(t)
	assert.Equal(t, []api.Address{
		{Address: walletrpctest.FakeAddress, Label: "Primary account"},
		{Address: "8FakeSubaddress1", AddressIndex: 1, Label: "shop"},
		{Address: "8FakeSubaddress2", AddressIndex: 2},
	}, after.Addresses)
}

func TestCreateWalletAddressRejections(t *testing.T) {
	s := newStack(t, stackOptions{})
	s.createWallet(t, "alice")

	t.Run("label at the limit", func(t *testing.T) {
		resp := s.post(t, "/v1/wallets/alice/addresses", jsonBody(
			t, map[string]any{"label": strings.Repeat("l", 128)},
		))

		requireStatus(t, resp, http.StatusCreated)
	})

	testCases := []struct {
		name       string
		body       string
		wantStatus int
		wantText   string
	}{
		{
			name:       "label past the limit",
			body:       `{"label":"` + strings.Repeat("l", 129) + `"}`,
			wantStatus: http.StatusBadRequest,
			wantText:   "label is longer than 128 characters",
		},
		{
			name:       "negative account",
			body:       `{"accountIndex":-1}`,
			wantStatus: http.StatusBadRequest,
			wantText:   messageBadAccount,
		},
		{
			name:       "empty body",
			body:       "",
			wantStatus: http.StatusBadRequest,
			wantText:   messageDecode,
		},
		{
			name:       "label is not a string",
			body:       `{"label":5}`,
			wantStatus: http.StatusBadRequest,
			wantText:   messageDecode,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resp := s.post(t, "/v1/wallets/alice/addresses", tc.body)

			requireError(
				t, resp, tc.wantStatus, codeBadRequest, tc.wantText,
			)
		})
	}
}

func seedTransfers(state *walletrpctest.State, count uint64) {
	seeded := make([]walletrpc.TransferEntry, 0, count)

	for i := range count {
		seeded = append(seeded, walletrpc.TransferEntry{
			TxID:      fmt.Sprintf("tx%02d", i),
			Height:    100 + i,
			Timestamp: 1700000000 + i*10,
			Amount:    i + 1,
			Type:      "in",
		})
	}

	state.Transfers = walletrpc.Transfers{In: seeded}
}

func transferPage(
	t *testing.T,
	s *stack,
	query string,
) api.TransferList {
	t.Helper()

	resp := s.get(t, "/v1/wallets/alice/transfers"+query)
	requireStatus(t, resp, http.StatusOK)

	return decodeAs[api.TransferList](t, resp)
}

func txHashes(list api.TransferList) []string {
	hashes := make([]string, 0, len(list.Transfers))
	for _, entry := range list.Transfers {
		hashes = append(hashes, entry.TxHash)
	}

	return hashes
}

func TestWalletTransfersPaginateNewestFirst(t *testing.T) {
	s := newStack(t, stackOptions{})
	s.createWallet(t, "alice")
	s.wallets.Set(func(state *walletrpctest.State) { seedTransfers(state, 13) })

	testCases := []struct {
		name       string
		query      string
		wantHashes []string
		wantLimit  int
		wantOffset int
	}{
		{
			name:       "first page",
			query:      "?limit=5&offset=0",
			wantHashes: []string{"tx12", "tx11", "tx10", "tx09", "tx08"},
			wantLimit:  5,
			wantOffset: 0,
		},
		{
			name:       "second page",
			query:      "?limit=5&offset=5",
			wantHashes: []string{"tx07", "tx06", "tx05", "tx04", "tx03"},
			wantLimit:  5,
			wantOffset: 5,
		},
		{
			name:       "short last page",
			query:      "?limit=5&offset=10",
			wantHashes: []string{"tx02", "tx01", "tx00"},
			wantLimit:  5,
			wantOffset: 10,
		},
		{
			name:       "offset at the end",
			query:      "?limit=5&offset=13",
			wantHashes: []string{},
			wantLimit:  5,
			wantOffset: 13,
		},
		{
			name:       "offset far past the end",
			query:      "?limit=5&offset=100000",
			wantHashes: []string{},
			wantLimit:  5,
			wantOffset: 100000,
		},
		{
			name:       "page that exactly reaches the end",
			query:      "?limit=3&offset=10",
			wantHashes: []string{"tx02", "tx01", "tx00"},
			wantLimit:  3,
			wantOffset: 10,
		},
		{
			name:       "single entry",
			query:      "?limit=1",
			wantHashes: []string{"tx12"},
			wantLimit:  1,
			wantOffset: 0,
		},
		{
			name:       "defaults",
			query:      "",
			wantHashes: allThirteen,
			wantLimit:  100,
			wantOffset: 0,
		},
		{
			name:       "largest limit",
			query:      "?limit=1000",
			wantHashes: allThirteen,
			wantLimit:  1000,
			wantOffset: 0,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			page := transferPage(t, s, tc.query)

			assert.Equal(t, tc.wantHashes, txHashes(page))
			assert.Equal(t, 13, page.Total, "total is independent of the page")
			assert.Equal(t, tc.wantLimit, page.Limit)
			assert.Equal(t, tc.wantOffset, page.Offset)
		})
	}
}

func TestWalletTransfersWalkEveryPageOnce(t *testing.T) {
	s := newStack(t, stackOptions{})
	s.createWallet(t, "alice")
	s.wallets.Set(func(state *walletrpctest.State) { seedTransfers(state, 13) })

	var (
		walked []string
		sizes  []int
	)

	for offset := 0; ; offset += 5 {
		page := transferPage(t, s, fmt.Sprintf("?limit=5&offset=%d", offset))
		if len(page.Transfers) == 0 {
			break
		}

		sizes = append(sizes, len(page.Transfers))
		walked = append(walked, txHashes(page)...)
	}

	assert.Equal(t, []int{5, 5, 3}, sizes)
	require.Len(t, walked, 13)
	assert.Len(t, uniqueStrings(walked), 13, "no entry repeats across pages")
	assert.Equal(t, "tx12", walked[0])
	assert.Equal(t, "tx00", walked[12])
}

func uniqueStrings(values []string) map[string]struct{} {
	unique := make(map[string]struct{}, len(values))
	for _, value := range values {
		unique[value] = struct{}{}
	}

	return unique
}

func TestWalletTransfersPageSizes(t *testing.T) {
	testCases := []struct {
		name      string
		count     uint64
		wantFirst int
		wantPast  int
	}{
		{"no transfers", 0, 0, 0},
		{"a single transfer", 1, 1, 0},
		{"exactly one full page", 5, 5, 0},
		{"one more than a full page", 6, 5, 1},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			s := newStack(t, stackOptions{})
			s.createWallet(t, "alice")
			s.wallets.Set(func(state *walletrpctest.State) {
				seedTransfers(state, tc.count)
			})

			first := transferPage(t, s, "?limit=5")

			assert.Len(t, first.Transfers, tc.wantFirst)
			assert.EqualValues(t, tc.count, first.Total)
			assert.NotNil(t, first.Transfers)

			past := transferPage(t, s, "?limit=5&offset=5")

			assert.Len(t, past.Transfers, tc.wantPast)
			assert.EqualValues(t, tc.count, past.Total)
			assert.NotNil(t, past.Transfers)
		})
	}
}

func TestWalletTransfersRejectBadPaging(t *testing.T) {
	s := newStack(t, stackOptions{})
	s.createWallet(t, "alice")

	testCases := []struct {
		name     string
		query    string
		wantText string
	}{
		{"zero limit", "?limit=0", "limit must be 1-1000"},
		{"negative limit", "?limit=-1", "limit must be 1-1000"},
		{"limit past the cap", "?limit=1001", "limit must be 1-1000"},
		{"negative offset", "?offset=-1", "offset must not be negative"},
		{"limit not a number", "?limit=ten", messageDecode},
		{"offset not a number", "?offset=1.5", messageDecode},
		{
			"unknown direction", "?direction=sideways",
			`unknown direction "sideways"`,
		},
		{"negative account", "?accountIndex=-1", messageBadAccount},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resp := s.get(t, "/v1/wallets/alice/transfers"+tc.query)

			requireError(
				t, resp, http.StatusBadRequest, codeBadRequest, tc.wantText,
			)
		})
	}
}

func TestWalletTransfersMergeEveryCategory(t *testing.T) {
	s := newStack(t, stackOptions{})
	s.createWallet(t, "alice")
	s.wallets.Set(func(state *walletrpctest.State) {
		state.Transfers = walletrpc.Transfers{
			In: []walletrpc.TransferEntry{{
				TxID:          "received",
				PaymentID:     "0000000000000000",
				Height:        10,
				Timestamp:     1700000100,
				Amount:        2500000000000,
				Note:          "salary",
				Confirmations: 4,
				SubaddrIndex:  walletrpc.SubaddressIndex{Major: 1, Minor: 2},
				Address:       "8Sub",
				Locked:        true,
			}},
			Out: []walletrpc.TransferEntry{{
				TxID:            "sent",
				PaymentID:       "1234567890abcdef",
				Height:          12,
				Timestamp:       1700000300,
				Amount:          1000000000000,
				Fee:             30000000,
				DoubleSpendSeen: true,
			}},
			Pending: entries(walletrpc.TransferEntry{
				TxID: "pending", Timestamp: 1700000200,
			}),
			Failed: entries(walletrpc.TransferEntry{
				TxID: "failed", Timestamp: 1700000050,
			}),
			Pool: entries(walletrpc.TransferEntry{
				TxID: "pool", Timestamp: 1700000400,
			}),
		}
	})

	page := transferPage(t, s, "")

	assert.Equal(t, 5, page.Total)
	assert.Equal(
		t, []string{"pool", "sent", "pending", "received", "failed"},
		txHashes(page),
	)

	directions := make(map[string]api.TransferDirection)
	for _, entry := range page.Transfers {
		directions[entry.TxHash] = entry.Direction
	}

	assert.Equal(t, map[string]api.TransferDirection{
		"received": api.TransferDirectionIn,
		"sent":     api.TransferDirectionOut,
		"pending":  api.TransferDirectionPending,
		"failed":   api.TransferDirectionFailed,
		"pool":     api.TransferDirectionPool,
	}, directions)

	received := page.Transfers[3]
	assert.Equal(t, "2500000000000", received.Amount)
	assert.Equal(t, "2.500000000000", received.AmountXmr)
	assert.Equal(t, "0", received.Fee)
	assert.EqualValues(t, 10, received.Height)
	assert.EqualValues(t, 4, received.Confirmations)
	assert.EqualValues(t, 1, received.AccountIndex)
	assert.EqualValues(t, 2, received.SubaddressIndex)
	assert.Equal(t, "8Sub", received.Address)
	assert.True(t, received.Locked)
	assert.True(t, received.Timestamp.Equal(time.Unix(1700000100, 0)))
	require.NotNil(t, received.Note)
	assert.Equal(t, "salary", *received.Note)
	assert.Nil(t, received.PaymentId, "the all-zero payment id means none")

	sent := page.Transfers[1]
	assert.Equal(t, "30000000", sent.Fee)
	assert.Equal(t, "0.000030000000", sent.FeeXmr)
	assert.True(t, sent.DoubleSpendSeen)
	require.NotNil(t, sent.PaymentId)
	assert.Equal(t, "1234567890abcdef", *sent.PaymentId)
	assert.Nil(t, sent.Note)
}

func TestWalletTransfersBreakTimestampTiesByHeight(t *testing.T) {
	s := newStack(t, stackOptions{})
	s.createWallet(t, "alice")
	s.wallets.Set(func(state *walletrpctest.State) {
		state.Transfers = walletrpc.Transfers{
			In: []walletrpc.TransferEntry{
				{TxID: "low", Height: 5, Timestamp: 1700000000},
				{TxID: "high", Height: 9, Timestamp: 1700000000},
				{TxID: "mid", Height: 7, Timestamp: 1700000000},
			},
		}
	})

	assert.Equal(
		t, []string{"high", "mid", "low"}, txHashes(transferPage(t, s, "")),
	)
}

func (s *stack) fundAlice(t *testing.T) {
	t.Helper()

	s.createWallet(t, "alice")
	s.wallets.Set(func(state *walletrpctest.State) {
		state.Height = nodeHeight
		state.Balance = 5000000000000
		state.UnlockedBalance = 5000000000000
	})
}

func TestSendTransfer(t *testing.T) {
	s := newStack(t, stackOptions{})
	s.fundAlice(t)

	resp := s.post(t, "/v1/wallets/alice/transfers", `{
		"destinations": [
			{"address": "4First", "amount": "1000000000000"},
			{"address": "4Second", "amount": "500000000000"}
		],
		"accountIndex": 0,
		"subaddressIndices": [1, 2],
		"priority": "high"
	}`)
	requireStatus(t, resp, http.StatusOK)

	assert.Equal(t, api.TransferResult{
		TxHash:    walletrpctest.FakeTxHash,
		TxKey:     "fakekey",
		Amount:    "1500000000000",
		AmountXmr: "1.500000000000",
		Fee:       "30000000",
		FeeXmr:    "0.000030000000",
		Weight:    1500,
		Relayed:   true,
	}, decodeAs[api.TransferResult](t, resp))

	sent := s.lastTransfer()

	assert.InDelta(t, 3, sent["priority"], 0, "high is wallet2's elevated")
	assert.Equal(t, false, sent["do_not_relay"])
	assert.Equal(t, true, sent["get_tx_key"])
	assert.Equal(t, []any{float64(1), float64(2)}, sent["subaddr_indices"])
	assert.Equal(t, []any{
		map[string]any{"address": "4First", "amount": float64(1000000000000)},
		map[string]any{"address": "4Second", "amount": float64(500000000000)},
	}, sent["destinations"])
}

func TestSendTransferPriorities(t *testing.T) {
	testCases := []struct {
		priority     string
		wantPriority float64
	}{
		{"default", 0},
		{"low", 1},
		{"normal", 2},
		{"high", 3},
		{"highest", 4},
	}

	s := newStack(t, stackOptions{})
	s.fundAlice(t)

	for _, tc := range testCases {
		t.Run(tc.priority, func(t *testing.T) {
			resp := s.post(t, "/v1/wallets/alice/transfers", `{
				"destinations": [{"address": "4A", "amount": "1000"}],
				"priority": "`+tc.priority+`"
			}`)
			requireStatus(t, resp, http.StatusOK)

			sent := s.lastTransfer()

			assert.InDelta(t, tc.wantPriority, sent["priority"], 0)
		})
	}
}

func TestSendTransferDryRunDoesNotRelay(t *testing.T) {
	s := newStack(t, stackOptions{})
	s.fundAlice(t)

	resp := s.post(t, "/v1/wallets/alice/transfers", `{
		"destinations": [{"address": "4A", "amount": "1000"}],
		"dryRun": true
	}`)
	requireStatus(t, resp, http.StatusOK)

	assert.False(t, decodeAs[api.TransferResult](t, resp).Relayed)

	sent := s.lastTransfer()

	assert.Equal(t, true, sent["do_not_relay"])
}

func TestSendTransferRefusals(t *testing.T) {
	testCases := []struct {
		name       string
		options    stackOptions
		setup      func(s *stack)
		wantStatus int
		wantCode   string
		wantText   string
	}{
		{
			name:       "transfers disabled",
			options:    stackOptions{disableTransfers: true},
			wantStatus: http.StatusForbidden,
			wantCode:   "TRANSFERS_DISABLED",
			wantText:   "this deployment does not allow transfers",
		},
		{
			name:       "read-only wins over enabled transfers",
			options:    stackOptions{readOnly: true},
			wantStatus: http.StatusForbidden,
			wantCode:   codeReadOnly,
			wantText:   messageReadOnly,
		},
		{
			name: "node not synchronized",
			setup: func(s *stack) {
				s.daemon.update(func(fake *fakeMonerod) {
					fake.info.Synchronized = false
				})
			},
			wantStatus: http.StatusConflict,
			wantCode:   "NODE_NOT_SYNCED",
			wantText:   "the node is not synchronized yet",
		},
		{
			name: "wallet behind the node",
			setup: func(s *stack) {
				s.wallets.Set(func(state *walletrpctest.State) {
					state.Height = nodeHeight - 1
				})
			},
			wantStatus: http.StatusConflict,
			wantCode:   "WALLET_SYNCING",
			wantText:   "the wallet has not caught up with the node yet",
		},
		{
			name: "monerod unreachable",
			setup: func(s *stack) {
				s.daemon.failHTTP("get_info", http.StatusBadGateway)
			},
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   "SERVICE_UNAVAILABLE",
			wantText:   messageNoMonero,
		},
		{
			name: "not enough unlocked funds",
			setup: func(s *stack) {
				s.wallets.Set(func(state *walletrpctest.State) {
					state.UnlockedBalance = 100
				})
			},
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "INSUFFICIENT_FUNDS",
			wantText:   "not enough unlocked funds for the amount plus fee",
		},
		{
			name: "view-only wallet",
			setup: func(s *stack) {
				s.wallets.Fail(
					"transfer", walletrpc.CodeWatchOnly, "watch-only wallet",
				)
			},
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "VIEW_ONLY_WALLET",
			wantText:   "a view-only wallet cannot send",
		},
		{
			name: "wallet cannot build the transaction",
			setup: func(s *stack) {
				s.wallets.Fail(
					"transfer", walletrpc.CodeTxNotPossible, "not possible",
				)
			},
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "TRANSFER_REJECTED",
			wantText:   "the wallet could not build this transaction",
		},
		{
			name: "transaction too large",
			setup: func(s *stack) {
				s.wallets.Fail("transfer", walletrpc.CodeTxTooLarge, "too big")
			},
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "TRANSFER_REJECTED",
			wantText:   "the wallet could not build this transaction",
		},
		{
			name: "wallet rejects the address",
			setup: func(s *stack) {
				s.wallets.Fail("transfer", walletrpc.CodeWrongAddress, "bad")
			},
			wantStatus: http.StatusBadRequest,
			wantCode:   codeBadRequest,
			wantText:   "an address is not valid for this network",
		},
		{
			name: "wallet reports a zero amount",
			setup: func(s *stack) {
				s.wallets.Fail("transfer", walletrpc.CodeZeroAmount, "zero")
			},
			wantStatus: http.StatusBadRequest,
			wantCode:   codeBadRequest,
			wantText:   "every destination amount must be above zero",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			s := newStack(t, tc.options)
			if !tc.options.readOnly {
				s.fundAlice(t)
			} else {
				_, err := s.pool.Create(context.Background(), "alice", "pw", "")
				require.NoError(t, err)
			}

			if tc.setup != nil {
				tc.setup(s)
			}

			resp := s.post(t, "/v1/wallets/alice/transfers", `{
				"destinations": [{"address": "4A", "amount": "1000000000"}]
			}`)

			requireError(t, resp, tc.wantStatus, tc.wantCode, tc.wantText)
		})
	}
}

func TestSendTransferRejectsInvalidRequests(t *testing.T) {
	s := newStack(t, stackOptions{})
	s.fundAlice(t)

	seventeen := strings.Repeat(`{"address":"4A","amount":"1"},`, 16) +
		`{"address":"4A","amount":"1"}`

	testCases := []struct {
		name       string
		body       string
		wantStatus int
		wantText   string
	}{
		{
			name:       "no destinations",
			body:       `{"destinations":[]}`,
			wantStatus: http.StatusBadRequest,
			wantText:   "destinations must hold 1-16 entries",
		},
		{
			name:       "destinations missing",
			body:       `{}`,
			wantStatus: http.StatusBadRequest,
			wantText:   "destinations must hold 1-16 entries",
		},
		{
			name:       "seventeen destinations",
			body:       `{"destinations":[` + seventeen + `]}`,
			wantStatus: http.StatusBadRequest,
			wantText:   "destinations must hold 1-16 entries",
		},
		{
			name:       "zero amount",
			body:       `{"destinations":[{"address":"4A","amount":"0"}]}`,
			wantStatus: http.StatusBadRequest,
			wantText:   messageAmount("0"),
		},
		{
			name:       "negative amount",
			body:       `{"destinations":[{"address":"4A","amount":"-5"}]}`,
			wantStatus: http.StatusBadRequest,
			wantText:   messageAmount("-5"),
		},
		{
			name:       "decimal amount",
			body:       `{"destinations":[{"address":"4A","amount":"1.5"}]}`,
			wantStatus: http.StatusBadRequest,
			wantText:   messageAmount("1.5"),
		},
		{
			name:       "empty amount",
			body:       `{"destinations":[{"address":"4A","amount":""}]}`,
			wantStatus: http.StatusBadRequest,
			wantText:   messageAmount(""),
		},
		{
			name: "amount past uint64",
			body: `{"destinations":[{"address":"4A",` +
				`"amount":"18446744073709551616"}]}`,
			wantStatus: http.StatusBadRequest,
			wantText:   messageAmount("18446744073709551616"),
		},
		{
			name:       "amount as a json number",
			body:       `{"destinations":[{"address":"4A","amount":5}]}`,
			wantStatus: http.StatusBadRequest,
			wantText:   messageDecode,
		},
		{
			name:       "empty address",
			body:       `{"destinations":[{"address":"","amount":"1"}]}`,
			wantStatus: http.StatusBadRequest,
			wantText:   "an address is not valid for this network",
		},
		{
			name: "unknown priority",
			body: `{"destinations":[{"address":"4A","amount":"1"}],` +
				`"priority":"turbo"}`,
			wantStatus: http.StatusBadRequest,
			wantText:   `unknown priority "turbo"`,
		},
		{
			name: "negative account",
			body: `{"destinations":[{"address":"4A","amount":"1"}],` +
				`"accountIndex":-1}`,
			wantStatus: http.StatusBadRequest,
			wantText:   messageBadAccount,
		},
		{
			name: "negative subaddress index",
			body: `{"destinations":[{"address":"4A","amount":"1"}],` +
				`"subaddressIndices":[0,-1]}`,
			wantStatus: http.StatusBadRequest,
			wantText:   messageBadAccount,
		},
		{
			name:       "empty body",
			body:       "",
			wantStatus: http.StatusBadRequest,
			wantText:   messageDecode,
		},
		{
			name:       "malformed body",
			body:       `{"destinations":`,
			wantStatus: http.StatusBadRequest,
			wantText:   messageDecode,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resp := s.post(t, "/v1/wallets/alice/transfers", tc.body)

			requireError(
				t, resp, tc.wantStatus, codeBadRequest, tc.wantText,
			)
		})
	}

	assert.Zero(t, s.wallets.Called("transfer"), "nothing reached the wallet")
}

func TestSendTransferAcceptsSixteenDestinations(t *testing.T) {
	s := newStack(t, stackOptions{})
	s.fundAlice(t)

	destinations := strings.Repeat(`{"address":"4A","amount":"1000"},`, 15) +
		`{"address":"4A","amount":"1000"}`

	resp := s.post(
		t, "/v1/wallets/alice/transfers",
		`{"destinations":[`+destinations+`]}`,
	)

	requireStatus(t, resp, http.StatusOK)
}

func TestReadOnlyDeploymentRefusesEveryWrite(t *testing.T) {
	s := newStack(t, stackOptions{readOnly: true})

	_, err := s.pool.Create(context.Background(), "alice", "pw", "")
	require.NoError(t, err)

	writes := []struct {
		name string
		path string
		body string
	}{
		{"create wallet", "/v1/wallets", `{"name":"new","password":"pw"}`},
		{
			"restore from seed", "/v1/wallets/restore/seed",
			`{"name":"new","password":"pw","seed":"a b"}`,
		},
		{
			"restore from keys", "/v1/wallets/restore/keys",
			`{"name":"new","password":"pw","address":"4A","viewKey":"v"}`,
		},
		{"create subaddress", "/v1/wallets/alice/addresses", `{}`},
		{
			"transfer", "/v1/wallets/alice/transfers",
			`{"destinations":[{"address":"4A","amount":"1"}]}`,
		},
		{
			"generate blocks", "/v1/node/regtest/generate-blocks",
			`{"count":1,"address":"4A"}`,
		},
	}

	for _, tc := range writes {
		t.Run(tc.name, func(t *testing.T) {
			resp := s.post(t, tc.path, tc.body)

			requireError(
				t, resp, http.StatusForbidden, codeReadOnly, messageReadOnly,
			)
		})
	}

	assert.Equal(t, 1, s.pool.OpenCount(), "no wallet was created")

	for _, path := range []string{
		"/v1/wallets",
		"/v1/wallets/alice/status",
		"/v1/wallets/alice/balance",
		"/v1/wallets/alice/addresses",
		"/v1/wallets/alice/transfers",
		"/v1/node/info",
	} {
		requireStatus(t, s.get(t, path), http.StatusOK)
	}

	requireStatus(t, s.post(t, "/v1/wallets/alice/close", ""), http.StatusOK)
	requireStatus(
		t, s.post(t, "/v1/wallets/alice/open", `{"password":"pw"}`),
		http.StatusOK,
	)
}

func TestTransfersDisabledStillAllowsOtherWrites(t *testing.T) {
	s := newStack(t, stackOptions{disableTransfers: true})
	s.createWallet(t, "alice")

	requireStatus(
		t, s.post(t, "/v1/wallets/alice/addresses", `{"label":"x"}`),
		http.StatusCreated,
	)
}

// statusOf is send for goroutines: a transport failure is reported with
// Errorf, which unlike FailNow is safe off the test goroutine.
func (s *stack) statusOf(t *testing.T, method, path, body string) int {
	t.Helper()

	resp, err := s.try(method, path, body)
	if err != nil {
		t.Errorf("request failed: %v", err)

		return 0
	}

	return resp.status
}

func TestConcurrentCreatesRespectTheOpenLimit(t *testing.T) {
	s := newStack(t, stackOptions{})

	const attempts = 8

	statuses := make([]int, attempts)

	var wg sync.WaitGroup

	for i := range attempts {
		body := fmt.Sprintf(`{"name":"wallet%d","password":"pw"}`, i)

		wg.Go(func() {
			statuses[i] = s.statusOf(t, http.MethodPost, "/v1/wallets", body)
		})
	}

	wg.Wait()

	created, refused := 0, 0

	for _, status := range statuses {
		switch status {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			refused++
		default:
			t.Errorf("unexpected status %d", status)
		}
	}

	assert.Equal(t, maxOpenWallets, created)
	assert.Equal(t, attempts-maxOpenWallets, refused)
	assert.Equal(t, maxOpenWallets, s.pool.OpenCount())
}

func TestConcurrentCreatesOfOneNameYieldOneWallet(t *testing.T) {
	s := newStack(t, stackOptions{})

	const attempts = 6

	statuses := make([]int, attempts)

	var wg sync.WaitGroup

	for i := range attempts {
		wg.Go(func() {
			statuses[i] = s.statusOf(
				t, http.MethodPost, "/v1/wallets",
				`{"name":"same","password":"pw"}`,
			)
		})
	}

	wg.Wait()

	created := 0

	for _, status := range statuses {
		if status == http.StatusCreated {
			created++

			continue
		}

		assert.Equal(t, http.StatusConflict, status)
	}

	assert.Equal(t, 1, created)
	assert.Equal(t, 1, s.pool.OpenCount())
}

const pathRestoreKeys = "/v1/wallets/restore/keys"

func messageAmount(amount string) string {
	return fmt.Sprintf(
		"amount %q must be a positive integer of atomic units", amount,
	)
}

var allThirteen = []string{
	"tx12", "tx11", "tx10", "tx09", "tx08", "tx07", "tx06",
	"tx05", "tx04", "tx03", "tx02", "tx01", "tx00",
}

func entries(items ...walletrpc.TransferEntry) []walletrpc.TransferEntry {
	return items
}

func writeFile(t *testing.T, dir, name string) {
	t.Helper()

	require.NoError(t, os.WriteFile(filepath.Join(dir, name), nil, 0o600))
}

func (s *stack) aliceStatus(t *testing.T) api.WalletStatus {
	t.Helper()

	resp := s.get(t, "/v1/wallets/alice/status")
	requireStatus(t, resp, http.StatusOK)

	return decodeAs[api.WalletStatus](t, resp)
}

func (s *stack) aliceAddresses(t *testing.T) api.AddressList {
	t.Helper()

	resp := s.get(t, "/v1/wallets/alice/addresses")
	requireStatus(t, resp, http.StatusOK)

	return decodeAs[api.AddressList](t, resp)
}

// lastTransfer is the parameter set of the last transfer the fake
// wallet-rpc received.
func (s *stack) lastTransfer() map[string]any {
	var sent map[string]any

	s.wallets.Set(func(state *walletrpctest.State) {
		sent = state.LastTransfer
	})

	return sent
}

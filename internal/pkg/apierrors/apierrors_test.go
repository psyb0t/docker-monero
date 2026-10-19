package apierrors

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/docker-monero/internal/pkg/common/access"
	"github.com/psyb0t/docker-monero/internal/pkg/common/invalid"
	corenode "github.com/psyb0t/docker-monero/internal/pkg/core/node"
	corewallets "github.com/psyb0t/docker-monero/internal/pkg/core/wallets"
	"github.com/psyb0t/docker-monero/internal/pkg/monero/jsonrpc"
	"github.com/psyb0t/docker-monero/internal/pkg/walletpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// leakedDetail stands in for call-site text that must never reach a client.
const leakedDetail = "/srv/app/internal/secret.go:42 in main.leak"

const (
	messageUnavailable = "monerod is not available yet"
	messageInternal    = "the request could not be completed"
	messageTimeout     = "monerod or the wallet did not answer in time"
)

type classifyCase struct {
	name    string
	err     error
	status  int
	code    string
	message string
}

// classifyCases pins the status, stable code, and client text for every
// sentinel Classify knows plus the fallbacks. The codes are literals on
// purpose: clients switch on them, so a rename must fail a test.
var classifyCases = []classifyCase{
	{
		name:    "read only",
		err:     access.ErrReadOnly,
		status:  http.StatusForbidden,
		code:    "READ_ONLY",
		message: "this deployment is read-only",
	},
	{
		name:    "invalid wallet name",
		err:     walletpool.ErrInvalidName,
		status:  http.StatusBadRequest,
		code:    "BAD_REQUEST",
		message: "wallet names use letters, digits, _ and -, starting with a letter or digit, up to 64 characters", //nolint:lll // exact client-facing text
	},
	{
		name:    "wallet not found",
		err:     walletpool.ErrWalletNotFound,
		status:  http.StatusNotFound,
		code:    "NOT_FOUND",
		message: "no such wallet",
	},
	{
		name:    "wallet exists",
		err:     walletpool.ErrWalletExists,
		status:  http.StatusConflict,
		code:    "WALLET_EXISTS",
		message: "a wallet with that name already exists",
	},
	{
		name:    "wallet not open",
		err:     walletpool.ErrWalletNotOpen,
		status:  http.StatusConflict,
		code:    "WALLET_NOT_OPEN",
		message: "the wallet is not open",
	},
	{
		name:    "wallet already open",
		err:     walletpool.ErrWalletAlreadyOpen,
		status:  http.StatusConflict,
		code:    "WALLET_ALREADY_OPEN",
		message: "the wallet is already open",
	},
	{
		name:    "wallet busy",
		err:     walletpool.ErrWalletBusy,
		status:  http.StatusConflict,
		code:    "WALLET_BUSY",
		message: "the wallet is opening or closing, try again shortly",
	},
	{
		name:    "too many open wallets",
		err:     walletpool.ErrTooManyOpen,
		status:  http.StatusConflict,
		code:    "TOO_MANY_OPEN_WALLETS",
		message: "the open wallet limit is reached, close a wallet first",
	},
	{
		name:    "invalid wallet password",
		err:     walletpool.ErrInvalidPassword,
		status:  http.StatusForbidden,
		code:    "INVALID_WALLET_PASSWORD",
		message: "the wallet password is wrong",
	},
	{
		name:    "pool closed",
		err:     walletpool.ErrPoolClosed,
		status:  http.StatusServiceUnavailable,
		code:    "SHUTTING_DOWN",
		message: "the service is shutting down",
	},
	{
		name:    "wallet start timeout",
		err:     walletpool.ErrStartTimeout,
		status:  http.StatusServiceUnavailable,
		code:    "WALLET_START_TIMEOUT",
		message: "the wallet process did not start in time",
	},
	{
		name:    "wallet process exited",
		err:     walletpool.ErrProcessExited,
		status:  http.StatusServiceUnavailable,
		code:    "SERVICE_UNAVAILABLE",
		message: "the wallet process exited while starting",
	},
	{
		name:    "transfers disabled",
		err:     corewallets.ErrTransfersDisabled,
		status:  http.StatusForbidden,
		code:    "TRANSFERS_DISABLED",
		message: "this deployment does not allow transfers",
	},
	{
		name:    "node not synced",
		err:     corewallets.ErrNodeNotSynced,
		status:  http.StatusConflict,
		code:    "NODE_NOT_SYNCED",
		message: "the node is not synchronized yet",
	},
	{
		name:    "wallet syncing",
		err:     corewallets.ErrWalletSyncing,
		status:  http.StatusConflict,
		code:    "WALLET_SYNCING",
		message: "the wallet has not caught up with the node yet",
	},
	{
		name:    "insufficient funds",
		err:     corewallets.ErrInsufficientFunds,
		status:  http.StatusUnprocessableEntity,
		code:    "INSUFFICIENT_FUNDS",
		message: "not enough unlocked funds for the amount plus fee",
	},
	{
		name:    "view only wallet",
		err:     corewallets.ErrViewOnly,
		status:  http.StatusUnprocessableEntity,
		code:    "VIEW_ONLY_WALLET",
		message: "a view-only wallet cannot send",
	},
	{
		name:    "transfer rejected",
		err:     corewallets.ErrTransferRejected,
		status:  http.StatusUnprocessableEntity,
		code:    "TRANSFER_REJECTED",
		message: "the wallet could not build this transaction",
	},
	{
		name:    "invalid address",
		err:     corewallets.ErrInvalidAddress,
		status:  http.StatusBadRequest,
		code:    "BAD_REQUEST",
		message: "an address is not valid for this network",
	},
	{
		name:    "invalid account",
		err:     corewallets.ErrInvalidAccount,
		status:  http.StatusBadRequest,
		code:    "BAD_REQUEST",
		message: "the account or subaddress index is out of range",
	},
	{
		name:    "not regtest",
		err:     corenode.ErrNotRegtest,
		status:  http.StatusConflict,
		code:    "NOT_REGTEST",
		message: "block generation is only available on regtest",
	},
	{
		name:    "invalid hash",
		err:     corenode.ErrInvalidHash,
		status:  http.StatusBadRequest,
		code:    "BAD_REQUEST",
		message: "hashes are 64 hexadecimal characters",
	},
	{
		name:    "invalid count",
		err:     corenode.ErrInvalidCount,
		status:  http.StatusBadRequest,
		code:    "BAD_REQUEST",
		message: "count is out of range",
	},
	{
		name:    "node lookup not found",
		err:     corenode.ErrNotFound,
		status:  http.StatusNotFound,
		code:    "NOT_FOUND",
		message: "no such block or transaction",
	},
	{
		name:    "invalid argument carrying client text",
		err:     invalid.Newf("limit must be 1-%d", 1000),
		status:  http.StatusBadRequest,
		code:    "BAD_REQUEST",
		message: "limit must be 1-1000",
	},
	{
		name:    "context canceled",
		err:     context.Canceled,
		status:  499,
		code:    "BAD_REQUEST",
		message: "the client closed the request",
	},
	{
		name: "context canceled inside a transport error",
		err: ctxerrors.Wrap(
			fmt.Errorf("call get_info: %w", context.Canceled), "read node info",
		),
		status:  499,
		code:    "BAD_REQUEST",
		message: "the client closed the request",
	},
	{
		name:    "context deadline exceeded",
		err:     context.DeadlineExceeded,
		status:  http.StatusGatewayTimeout,
		code:    "GATEWAY_TIMEOUT",
		message: messageTimeout,
	},
	{
		name:    "timeout class",
		err:     ctxerrors.Wrap(commerr.ErrTimeout, "request timed out"),
		status:  http.StatusGatewayTimeout,
		code:    "GATEWAY_TIMEOUT",
		message: messageTimeout,
	},
	{
		name:    "not ready class",
		err:     ctxerrors.Wrap(commerr.ErrNotReady, "daemon is busy syncing"),
		status:  http.StatusServiceUnavailable,
		code:    "SERVICE_UNAVAILABLE",
		message: messageUnavailable,
	},
	{
		name:    "unavailable class",
		err:     ctxerrors.Wrap(commerr.ErrUnavailable, "bad http status"),
		status:  http.StatusServiceUnavailable,
		code:    "SERVICE_UNAVAILABLE",
		message: messageUnavailable,
	},
	{
		name:    "connect failed class",
		err:     ctxerrors.Wrap(commerr.ErrConnectFailed, "request failed"),
		status:  http.StatusServiceUnavailable,
		code:    "SERVICE_UNAVAILABLE",
		message: messageUnavailable,
	},
	{
		name:    "unmapped rpc error",
		err:     &jsonrpc.Error{Code: -9, Message: "internal monerod detail"},
		status:  http.StatusInternalServerError,
		code:    "INTERNAL_SERVER_ERROR",
		message: messageInternal,
	},
	{
		name:    "plain error",
		err:     errors.New("disk exploded at " + leakedDetail),
		status:  http.StatusInternalServerError,
		code:    "INTERNAL_SERVER_ERROR",
		message: messageInternal,
	},
}

func TestClassify(t *testing.T) {
	t.Parallel()

	for _, tc := range classifyCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := Classify(tc.err)

			assert.Equal(t, tc.status, got.Status)
			assert.Equal(t, tc.code, got.Body.Code)
			assert.Equal(t, tc.message, got.Body.Message)
			assert.Nil(t, got.Body.Details)
		})
	}
}

func TestClassifyIsStableUnderWrapping(t *testing.T) {
	t.Parallel()

	for _, tc := range classifyCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			want := Classify(tc.err)

			wrappers := map[string]error{
				"ctxerrors": ctxerrors.Wrap(tc.err, leakedDetail),
				"fmt":       fmt.Errorf("%s: %w", leakedDetail, tc.err),
				"join":      errors.Join(errors.New(leakedDetail), tc.err),
			}

			for name, wrapped := range wrappers {
				got := Classify(wrapped)

				assert.Equal(t, want, got, name)
				assert.NotContains(t, got.Text(), leakedDetail, name)
			}
		})
	}
}

func TestEveryRuleHasACase(t *testing.T) {
	t.Parallel()

	for _, candidate := range rules {
		covered := false

		for _, tc := range classifyCases {
			if errors.Is(tc.err, candidate.target) {
				covered = true

				break
			}
		}

		assert.True(t, covered, "no classify case for %v", candidate.target)
	}
}

func TestClassifyKeepsClientTextFromInvalidErrors(t *testing.T) {
	t.Parallel()

	err := ctxerrors.Wrap(
		invalid.Newf("offset must not be negative"), "list transfers",
	)

	got := Classify(err)

	assert.Equal(t, http.StatusBadRequest, got.Status)
	assert.Equal(t, aichteeteapee.ErrorCodeBadRequest, got.Body.Code)
	assert.Equal(t, "offset must not be negative", got.Body.Message)
	assert.NotContains(t, got.Text(), "list transfers")
}

func TestClassifySpecificSentinelsBeatTheirGenericClass(t *testing.T) {
	t.Parallel()

	// These wrap a commerr class the fallbacks would map to a different
	// status. The precondition keeps the test honest if a sentinel changes.
	require.ErrorIs(t, corewallets.ErrNodeNotSynced, commerr.ErrNotReady)
	require.ErrorIs(t, corewallets.ErrWalletSyncing, commerr.ErrNotReady)
	require.ErrorIs(t, access.ErrReadOnly, commerr.ErrPermissionDenied)
	require.ErrorIs(t, corenode.ErrInvalidHash, commerr.ErrInvalidArgument)
	require.ErrorIs(t, corenode.ErrNotFound, commerr.ErrNotFound)

	for sentinel, want := range map[error]int{
		corewallets.ErrNodeNotSynced: http.StatusConflict,
		corewallets.ErrWalletSyncing: http.StatusConflict,
		access.ErrReadOnly:           http.StatusForbidden,
	} {
		assert.Equal(t, want, Classify(sentinel).Status, sentinel.Error())
	}
}

func TestClassifySentinelBeatsInvalidMessageAndContextErrors(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		err  error
		want Mapped
	}{
		{
			name: "sentinel joined with an invalid message",
			err: errors.Join(
				invalid.Newf("client text that must not win"),
				walletpool.ErrWalletNotFound,
			),
			want: newMapped(http.StatusNotFound, "NOT_FOUND", "no such wallet"),
		},
		{
			name: "sentinel joined with a cancellation",
			err:  errors.Join(context.Canceled, access.ErrReadOnly),
			want: newMapped(
				http.StatusForbidden, "READ_ONLY",
				"this deployment is read-only",
			),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, Classify(tc.err))
		})
	}
}

func TestClassifyUnmappedErrorsRevealNothing(t *testing.T) {
	t.Parallel()

	got := Classify(ctxerrors.Wrap(
		&jsonrpc.Error{Code: -1, Message: "password=hunter2"}, leakedDetail,
	))

	assert.Equal(t, http.StatusInternalServerError, got.Status)
	assert.Equal(t, messageInternal, got.Body.Message)
	assert.NotContains(t, got.Text(), "hunter2")
	assert.NotContains(t, got.Text(), leakedDetail)
}

func TestMappedText(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name   string
		mapped Mapped
		want   string
	}{
		{
			name:   "code and message",
			mapped: newMapped(http.StatusTeapot, "SOME_CODE", "some words"),
			want:   "SOME_CODE: some words",
		},
		{
			name:   "empty message",
			mapped: newMapped(http.StatusTeapot, "SOME_CODE", ""),
			want:   "SOME_CODE: ",
		},
		{
			name:   "zero value",
			mapped: Mapped{},
			want:   ": ",
		},
		{
			name:   "classified error",
			mapped: Classify(access.ErrReadOnly),
			want:   "READ_ONLY: this deployment is read-only",
		},
		{
			name:   "classified cancellation",
			mapped: Classify(context.Canceled),
			want:   "BAD_REQUEST: the client closed the request",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, tc.mapped.Text())
		})
	}
}

package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/docker-monero/internal/pkg/common/access"
	"github.com/psyb0t/docker-monero/internal/pkg/common/invalid"
	corewallets "github.com/psyb0t/docker-monero/internal/pkg/core/wallets"
	"github.com/psyb0t/docker-monero/internal/pkg/walletpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// visitors is every response interface the generated strict handler asks a
// failure to satisfy, one per operation.
var visitors = []struct {
	operation string
	visit     func(failure, http.ResponseWriter) error
}{
	{"CloseWallet", failure.VisitCloseWalletResponse},
	{"CreateWalletAddress", failure.VisitCreateWalletAddressResponse},
	{"CreateWallet", failure.VisitCreateWalletResponse},
	{"GenerateBlocks", failure.VisitGenerateBlocksResponse},
	{"GetBlockByHash", failure.VisitGetBlockByHashResponse},
	{"GetBlockByHeight", failure.VisitGetBlockByHeightResponse},
	{"GetFeeEstimate", failure.VisitGetFeeEstimateResponse},
	{"GetLatestBlock", failure.VisitGetLatestBlockResponse},
	{"GetMempoolStats", failure.VisitGetMempoolStatsResponse},
	{"GetNodeInfo", failure.VisitGetNodeInfoResponse},
	{"GetTransaction", failure.VisitGetTransactionResponse},
	{"GetWalletBalance", failure.VisitGetWalletBalanceResponse},
	{"GetWalletStatus", failure.VisitGetWalletStatusResponse},
	{"ListWalletAddresses", failure.VisitListWalletAddressesResponse},
	{"ListWalletTransfers", failure.VisitListWalletTransfersResponse},
	{"ListWallets", failure.VisitListWalletsResponse},
	{"OpenWallet", failure.VisitOpenWalletResponse},
	{"RestoreWalletFromKeys", failure.VisitRestoreWalletFromKeysResponse},
	{"RestoreWalletFromSeed", failure.VisitRestoreWalletFromSeedResponse},
	{"SendTransfer", failure.VisitSendTransferResponse},
}

func TestEveryOperationWritesTheMappedFailure(t *testing.T) {
	t.Parallel()

	for _, tc := range visitors {
		t.Run(tc.operation, func(t *testing.T) {
			t.Parallel()

			mapped := failure{
				status: http.StatusConflict,
				body: aichteeteapee.ErrorResponse{
					Code: "WALLET_BUSY", Message: "try again",
				},
			}
			recorder := httptest.NewRecorder()

			require.NoError(t, tc.visit(mapped, recorder))

			assert.Equal(t, http.StatusConflict, recorder.Code)
			assert.Equal(
				t, "application/json", recorder.Header().Get("Content-Type"),
			)
			assert.JSONEq(
				t, `{"code":"WALLET_BUSY","message":"try again"}`,
				recorder.Body.String(),
			)
		})
	}
}

func TestFailMapsErrorsToTheClientEnvelope(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		err         error
		wantStatus  int
		wantCode    string
		wantMessage string
	}{
		{
			name: "service sentinel",
			err: ctxerrors.Wrap(
				walletpool.ErrWalletExists, "create wallet",
			),
			wantStatus:  http.StatusConflict,
			wantCode:    "WALLET_EXISTS",
			wantMessage: "a wallet with that name already exists",
		},
		{
			name:        "read-only deployment",
			err:         access.ErrReadOnly,
			wantStatus:  http.StatusForbidden,
			wantCode:    "READ_ONLY",
			wantMessage: "this deployment is read-only",
		},
		{
			name:        "transfers disabled",
			err:         corewallets.ErrTransfersDisabled,
			wantStatus:  http.StatusForbidden,
			wantCode:    "TRANSFERS_DISABLED",
			wantMessage: "this deployment does not allow transfers",
		},
		{
			name: "client-safe validation text is passed through",
			err: ctxerrors.Wrap(
				invalid.Newf("limit must be 1-1000"), "page",
			),
			wantStatus:  http.StatusBadRequest,
			wantCode:    "BAD_REQUEST",
			wantMessage: "limit must be 1-1000",
		},
		{
			name: "unavailable daemon",
			err: ctxerrors.Wrap(
				commerr.ErrConnectFailed, "dial monerod",
			),
			wantStatus:  http.StatusServiceUnavailable,
			wantCode:    "SERVICE_UNAVAILABLE",
			wantMessage: "monerod is not available yet",
		},
		{
			name:        "timeout",
			err:         ctxerrors.Wrap(commerr.ErrTimeout, "call monerod"),
			wantStatus:  http.StatusGatewayTimeout,
			wantCode:    "GATEWAY_TIMEOUT",
			wantMessage: "monerod or the wallet did not answer in time",
		},
		{
			name:        "cancelled request",
			err:         ctxerrors.Wrap(context.Canceled, "call monerod"),
			wantStatus:  499,
			wantCode:    "BAD_REQUEST",
			wantMessage: "the client closed the request",
		},
		{
			name:        "unknown failure hides its text",
			err:         errors.New("secret internal detail at /srv/app.go:42"),
			wantStatus:  http.StatusInternalServerError,
			wantCode:    "INTERNAL_SERVER_ERROR",
			wantMessage: "the request could not be completed",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mapped := fail(context.Background(), tc.err)

			assert.Equal(t, tc.wantStatus, mapped.status)
			assert.Equal(t, tc.wantCode, mapped.body.Code)
			assert.Equal(t, tc.wantMessage, mapped.body.Message)
			assert.NotContains(t, mapped.body.Message, "secret")
		})
	}
}

func TestFailureWriteNeverReturnsAnError(t *testing.T) {
	t.Parallel()

	mapped := fail(context.Background(), errors.New("boom"))
	recorder := httptest.NewRecorder()

	require.NoError(t, mapped.write(recorder))
	assert.Equal(t, http.StatusInternalServerError, recorder.Code)
}

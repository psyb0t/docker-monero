package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/psyb0t/docker-monero/internal/pkg/http/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// visitFunc writes a handler's response object to a writer.
type visitFunc func(http.ResponseWriter) error

const bodyRequired = `{
	"code": "BAD_REQUEST",
	"message": "a JSON request body is required"
}`

// bodylessCalls invokes each handler that takes a request body with no body
// at all. The generated binder always supplies one, so this is the guard
// against a binder that ever stops doing so.
var bodylessCalls = []struct {
	operation string
	call      func(ctx context.Context, srv *Server) (visitFunc, error)
}{
	{
		"GenerateBlocks",
		func(ctx context.Context, srv *Server) (visitFunc, error) {
			resp, err := srv.GenerateBlocks(
				ctx, api.GenerateBlocksRequestObject{},
			)
			if err != nil {
				return nil, err
			}

			return resp.VisitGenerateBlocksResponse, nil
		},
	},
	{
		"CreateWallet",
		func(ctx context.Context, srv *Server) (visitFunc, error) {
			resp, err := srv.CreateWallet(ctx, api.CreateWalletRequestObject{})
			if err != nil {
				return nil, err
			}

			return resp.VisitCreateWalletResponse, nil
		},
	},
	{
		"RestoreWalletFromSeed",
		func(ctx context.Context, srv *Server) (visitFunc, error) {
			resp, err := srv.RestoreWalletFromSeed(
				ctx, api.RestoreWalletFromSeedRequestObject{},
			)
			if err != nil {
				return nil, err
			}

			return resp.VisitRestoreWalletFromSeedResponse, nil
		},
	},
	{
		"RestoreWalletFromKeys",
		func(ctx context.Context, srv *Server) (visitFunc, error) {
			resp, err := srv.RestoreWalletFromKeys(
				ctx, api.RestoreWalletFromKeysRequestObject{},
			)
			if err != nil {
				return nil, err
			}

			return resp.VisitRestoreWalletFromKeysResponse, nil
		},
	},
	{
		"OpenWallet",
		func(ctx context.Context, srv *Server) (visitFunc, error) {
			resp, err := srv.OpenWallet(
				ctx, api.OpenWalletRequestObject{WalletName: "alice"},
			)
			if err != nil {
				return nil, err
			}

			return resp.VisitOpenWalletResponse, nil
		},
	},
	{
		"CreateWalletAddress",
		func(ctx context.Context, srv *Server) (visitFunc, error) {
			resp, err := srv.CreateWalletAddress(
				ctx, api.CreateWalletAddressRequestObject{WalletName: "alice"},
			)
			if err != nil {
				return nil, err
			}

			return resp.VisitCreateWalletAddressResponse, nil
		},
	},
	{
		"SendTransfer",
		func(ctx context.Context, srv *Server) (visitFunc, error) {
			resp, err := srv.SendTransfer(
				ctx, api.SendTransferRequestObject{WalletName: "alice"},
			)
			if err != nil {
				return nil, err
			}

			return resp.VisitSendTransferResponse, nil
		},
	},
}

func TestHandlersRefuseARequestWithoutABody(t *testing.T) {
	t.Parallel()

	for _, tc := range bodylessCalls {
		t.Run(tc.operation, func(t *testing.T) {
			t.Parallel()

			// The services are never reached, so none are needed.
			visit, err := tc.call(context.Background(), New(nil, nil))
			require.NoError(t, err)

			recorder := httptest.NewRecorder()

			require.NoError(t, visit(recorder))

			assert.Equal(t, http.StatusBadRequest, recorder.Code)
			assert.JSONEq(t, bodyRequired, recorder.Body.String())
		})
	}
}

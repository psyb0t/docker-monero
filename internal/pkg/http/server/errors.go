package server

import (
	"context"
	"net/http"

	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/docker-monero/internal/pkg/apierrors"
)

// failure is a mapped error. It implements every generated response
// interface, so a handler can return it for any operation and the client
// always receives the aichteeteapee envelope with the mapped status.
type failure struct {
	status int
	body   aichteeteapee.ErrorResponse
}

// fail logs an error and maps it. Client mistakes log at warn, the rest at
// error, with the full wrapped chain kept in the log only.
func fail(ctx context.Context, err error) failure {
	classified := apierrors.Classify(err)
	mapped := failure{status: classified.Status, body: classified.Body}

	logger := ctxscope.GetLogger(ctx)
	if mapped.status >= http.StatusInternalServerError {
		logger.Error(
			"request failed",
			"status",
			mapped.status,
			"code",
			mapped.body.Code,
			"err",
			err,
		)
	} else {
		logger.Warn(
			"request refused",
			"status", mapped.status,
			"code", mapped.body.Code,
			"err", err,
		)
	}

	return mapped
}

func (f failure) write(w http.ResponseWriter) error {
	aichteeteapee.WriteJSON(w, f.status, f.body)

	return nil
}

// The generated strict handler calls exactly one of these per operation.

func (f failure) VisitCloseWalletResponse(w http.ResponseWriter) error {
	return f.write(w)
}

func (f failure) VisitCreateWalletAddressResponse(w http.ResponseWriter) error {
	return f.write(w)
}

func (f failure) VisitCreateWalletResponse(w http.ResponseWriter) error {
	return f.write(w)
}

func (f failure) VisitGenerateBlocksResponse(w http.ResponseWriter) error {
	return f.write(w)
}

func (f failure) VisitGetBlockByHashResponse(w http.ResponseWriter) error {
	return f.write(w)
}

func (f failure) VisitGetBlockByHeightResponse(w http.ResponseWriter) error {
	return f.write(w)
}

func (f failure) VisitGetFeeEstimateResponse(w http.ResponseWriter) error {
	return f.write(w)
}

func (f failure) VisitGetLatestBlockResponse(w http.ResponseWriter) error {
	return f.write(w)
}

func (f failure) VisitGetMempoolStatsResponse(w http.ResponseWriter) error {
	return f.write(w)
}

func (f failure) VisitGetNodeInfoResponse(w http.ResponseWriter) error {
	return f.write(w)
}

func (f failure) VisitGetTransactionResponse(w http.ResponseWriter) error {
	return f.write(w)
}

func (f failure) VisitGetWalletBalanceResponse(w http.ResponseWriter) error {
	return f.write(w)
}

func (f failure) VisitGetWalletStatusResponse(w http.ResponseWriter) error {
	return f.write(w)
}

func (f failure) VisitListWalletAddressesResponse(w http.ResponseWriter) error {
	return f.write(w)
}

func (f failure) VisitListWalletTransfersResponse(w http.ResponseWriter) error {
	return f.write(w)
}

func (f failure) VisitListWalletsResponse(w http.ResponseWriter) error {
	return f.write(w)
}

func (f failure) VisitOpenWalletResponse(w http.ResponseWriter) error {
	return f.write(w)
}

func (f failure) VisitRestoreWalletFromKeysResponse(
	w http.ResponseWriter,
) error {
	return f.write(w)
}

func (f failure) VisitRestoreWalletFromSeedResponse(
	w http.ResponseWriter,
) error {
	return f.write(w)
}

func (f failure) VisitSendTransferResponse(w http.ResponseWriter) error {
	return f.write(w)
}

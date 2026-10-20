// Package apierrors translates service errors into what a client sees: a
// status and the aichteeteapee error envelope. REST and MCP share it, so a
// failure reads the same over both transports and no internal error text,
// file path, or call site ever reaches a client.
package apierrors

import (
	"context"
	"errors"
	"net/http"

	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/docker-monero/internal/pkg/common/access"
	"github.com/psyb0t/docker-monero/internal/pkg/common/invalid"
	corenode "github.com/psyb0t/docker-monero/internal/pkg/core/node"
	corewallets "github.com/psyb0t/docker-monero/internal/pkg/core/wallets"
	"github.com/psyb0t/docker-monero/internal/pkg/walletpool"
)

// statusClientClosedRequest is nginx's 499. A cancelled request is not a
// server error, so it gets its own status instead of inflating the 5xx rate.
const statusClientClosedRequest = 499

// Stable error codes this API adds to aichteeteapee's set. They are the
// values clients switch on and must not be renamed.
const (
	codeReadOnly           = "READ_ONLY"
	codeWalletExists       = "WALLET_EXISTS"
	codeWalletNotOpen      = "WALLET_NOT_OPEN"
	codeWalletAlreadyOpen  = "WALLET_ALREADY_OPEN"
	codeWalletBusy         = "WALLET_BUSY"
	codeTooManyOpen        = "TOO_MANY_OPEN_WALLETS"
	codeInvalidPassword    = "INVALID_WALLET_PASSWORD"
	codeTransfersDisabled  = "TRANSFERS_DISABLED"
	codeNodeNotSynced      = "NODE_NOT_SYNCED"
	codeWalletSyncing      = "WALLET_SYNCING"
	codeInsufficientFunds  = "INSUFFICIENT_FUNDS"
	codeTransferRejected   = "TRANSFER_REJECTED"
	codeViewOnly           = "VIEW_ONLY_WALLET"
	codeNotRegtest         = "NOT_REGTEST"
	codeShuttingDown       = "SHUTTING_DOWN"
	codeWalletStartTimeout = "WALLET_START_TIMEOUT"
	codeRPCAuthentication  = "RPC_AUTHENTICATION_FAILED"
)

// Mapped is an error translated for a client: the HTTP status and the
// envelope. MCP uses the envelope too, without the status.
type Mapped struct {
	Status int
	Body   aichteeteapee.ErrorResponse
}

func newMapped(status int, code, message string) Mapped {
	return Mapped{
		Status: status,
		Body:   aichteeteapee.ErrorResponse{Code: code, Message: message},
	}
}

// rule maps one sentinel to its response.
type rule struct {
	target  error
	status  int
	code    string
	message string
}

// rules are checked in order, so the specific service sentinels come before
// the generic commerr classes they also wrap.
//
//nolint:gochecknoglobals // a fixed lookup table, read-only after init
var rules = []rule{
	{
		target:  commerr.ErrNotAuthenticated,
		status:  http.StatusBadGateway,
		code:    codeRPCAuthentication,
		message: "monerod or wallet RPC authentication failed",
	},
	{
		target:  access.ErrReadOnly,
		status:  http.StatusForbidden,
		code:    codeReadOnly,
		message: "this deployment is read-only",
	},
	{
		target: walletpool.ErrInvalidName,
		status: http.StatusBadRequest,
		code:   aichteeteapee.ErrorCodeBadRequest,
		message: "wallet names use letters, digits, _ and -, starting " +
			"with a letter or digit, up to 64 characters",
	},
	{
		target:  walletpool.ErrWalletNotFound,
		status:  http.StatusNotFound,
		code:    aichteeteapee.ErrorCodeNotFound,
		message: "no such wallet",
	},
	{
		target:  walletpool.ErrWalletExists,
		status:  http.StatusConflict,
		code:    codeWalletExists,
		message: "a wallet with that name already exists",
	},
	{
		target:  walletpool.ErrWalletNotOpen,
		status:  http.StatusConflict,
		code:    codeWalletNotOpen,
		message: "the wallet is not open",
	},
	{
		target:  walletpool.ErrWalletAlreadyOpen,
		status:  http.StatusConflict,
		code:    codeWalletAlreadyOpen,
		message: "the wallet is already open",
	},
	{
		target:  walletpool.ErrWalletBusy,
		status:  http.StatusConflict,
		code:    codeWalletBusy,
		message: "the wallet is opening or closing, try again shortly",
	},
	{
		target:  walletpool.ErrTooManyOpen,
		status:  http.StatusConflict,
		code:    codeTooManyOpen,
		message: "the open wallet limit is reached, close a wallet first",
	},
	{
		target:  walletpool.ErrInvalidPassword,
		status:  http.StatusForbidden,
		code:    codeInvalidPassword,
		message: "the wallet password is wrong",
	},
	{
		target:  walletpool.ErrPoolClosed,
		status:  http.StatusServiceUnavailable,
		code:    codeShuttingDown,
		message: "the service is shutting down",
	},
	{
		target:  walletpool.ErrStartTimeout,
		status:  http.StatusServiceUnavailable,
		code:    codeWalletStartTimeout,
		message: "the wallet process did not start in time",
	},
	{
		target:  walletpool.ErrProcessExited,
		status:  http.StatusServiceUnavailable,
		code:    aichteeteapee.ErrorCodeServiceUnavailable,
		message: "the wallet process exited while starting",
	},
	{
		target:  corewallets.ErrTransfersDisabled,
		status:  http.StatusForbidden,
		code:    codeTransfersDisabled,
		message: "this deployment does not allow transfers",
	},
	{
		target:  corewallets.ErrNodeNotSynced,
		status:  http.StatusConflict,
		code:    codeNodeNotSynced,
		message: "the node is not synchronized yet",
	},
	{
		target:  corewallets.ErrWalletSyncing,
		status:  http.StatusConflict,
		code:    codeWalletSyncing,
		message: "the wallet has not caught up with the node yet",
	},
	{
		target:  corewallets.ErrInsufficientFunds,
		status:  http.StatusUnprocessableEntity,
		code:    codeInsufficientFunds,
		message: "not enough unlocked funds for the amount plus fee",
	},
	{
		target:  corewallets.ErrViewOnly,
		status:  http.StatusUnprocessableEntity,
		code:    codeViewOnly,
		message: "a view-only wallet cannot send",
	},
	{
		target:  corewallets.ErrTransferRejected,
		status:  http.StatusUnprocessableEntity,
		code:    codeTransferRejected,
		message: "the wallet could not build this transaction",
	},
	{
		target:  corewallets.ErrInvalidAddress,
		status:  http.StatusBadRequest,
		code:    aichteeteapee.ErrorCodeBadRequest,
		message: "an address is not valid for this network",
	},
	{
		target:  corewallets.ErrInvalidAccount,
		status:  http.StatusBadRequest,
		code:    aichteeteapee.ErrorCodeBadRequest,
		message: "the account or subaddress index is out of range",
	},
	{
		target:  corenode.ErrNotRegtest,
		status:  http.StatusConflict,
		code:    codeNotRegtest,
		message: "block generation is only available on regtest",
	},
	{
		target:  corenode.ErrInvalidHash,
		status:  http.StatusBadRequest,
		code:    aichteeteapee.ErrorCodeBadRequest,
		message: "hashes are 64 hexadecimal characters",
	},
	{
		target:  corenode.ErrInvalidCount,
		status:  http.StatusBadRequest,
		code:    aichteeteapee.ErrorCodeBadRequest,
		message: "count is out of range",
	},
	{
		target:  corenode.ErrNotFound,
		status:  http.StatusNotFound,
		code:    aichteeteapee.ErrorCodeNotFound,
		message: "no such block or transaction",
	},
}

// Classify maps an error onto its status and envelope. Nothing from the
// error's own text reaches the response except an invalid.Error message,
// which is written for clients.
func Classify(err error) Mapped {
	for _, candidate := range rules {
		if errors.Is(err, candidate.target) {
			return newMapped(
				candidate.status,
				candidate.code,
				candidate.message,
			)
		}
	}

	if message, ok := invalid.Message(err); ok {
		return newMapped(
			http.StatusBadRequest,
			aichteeteapee.ErrorCodeBadRequest,
			message,
		)
	}

	switch {
	case errors.Is(err, context.Canceled):
		return newMapped(
			statusClientClosedRequest,
			aichteeteapee.ErrorCodeBadRequest,
			"the client closed the request",
		)
	case errors.Is(err, commerr.ErrTimeout),
		errors.Is(err, context.DeadlineExceeded):
		return newMapped(
			http.StatusGatewayTimeout,
			aichteeteapee.ErrorCodeGatewayTimeout,
			"monerod or the wallet did not answer in time",
		)
	case errors.Is(err, commerr.ErrNotReady),
		errors.Is(err, commerr.ErrUnavailable),
		errors.Is(err, commerr.ErrConnectFailed):
		return newMapped(
			http.StatusServiceUnavailable,
			aichteeteapee.ErrorCodeServiceUnavailable,
			"monerod is not available yet",
		)
	}

	return newMapped(
		http.StatusInternalServerError,
		aichteeteapee.ErrorCodeInternalServerError,
		"the request could not be completed",
	)
}

// Text renders the envelope as one line for transports without a status.
func (m Mapped) Text() string {
	return m.Body.Code + ": " + m.Body.Message
}

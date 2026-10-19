package wallets

import (
	"fmt"

	"github.com/psyb0t/ctxerrors/commerr"
)

// Sentinels the transports map to status codes. Each wraps the commerr
// class it belongs to, so a transport can match either the specific error or
// its class.
var (
	ErrTransfersDisabled = fmt.Errorf(
		"transfers are disabled: %w",
		commerr.ErrPermissionDenied,
	)
	ErrNodeNotSynced = fmt.Errorf(
		"the node is not synchronized: %w",
		commerr.ErrNotReady,
	)
	ErrWalletSyncing = fmt.Errorf(
		"the wallet is still syncing: %w",
		commerr.ErrNotReady,
	)
	ErrInsufficientFunds = fmt.Errorf(
		"not enough unlocked funds: %w",
		commerr.ErrValidationFailed,
	)
	ErrTransferRejected = fmt.Errorf(
		"the wallet rejected the transfer: %w",
		commerr.ErrValidationFailed,
	)
	ErrInvalidAddress = fmt.Errorf(
		"invalid address: %w",
		commerr.ErrInvalidArgument,
	)
	ErrInvalidAccount = fmt.Errorf(
		"invalid account or subaddress index: %w",
		commerr.ErrInvalidArgument,
	)
	ErrViewOnly = fmt.Errorf(
		"the wallet is view-only: %w",
		commerr.ErrValidationFailed,
	)
)

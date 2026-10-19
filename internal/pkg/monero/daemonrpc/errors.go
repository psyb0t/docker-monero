package daemonrpc

import "errors"

// ErrTooManyTransactions is returned before a lookup monerod would refuse.
var ErrTooManyTransactions = errors.New("too many transactions requested")

// Core RPC error codes from core_rpc_server_error_codes.h that callers map.
const (
	CodeWrongParam         = -1
	CodeTooBigHeight       = -2
	CodeWrongWalletAddress = -4
	CodeInternalError      = -5
	CodeCoreBusy           = -9
	CodeRegtestRequired    = -13
	CodeRestrictedAccess   = -19
)

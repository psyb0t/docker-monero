package walletpool

import "errors"

// Sentinels the transports map onto stable error codes.
var (
	ErrInvalidName       = errors.New("invalid wallet name")
	ErrWalletExists      = errors.New("wallet already exists")
	ErrWalletNotFound    = errors.New("wallet not found")
	ErrWalletNotOpen     = errors.New("wallet is not open")
	ErrWalletAlreadyOpen = errors.New("wallet is already open")
	ErrWalletBusy        = errors.New("wallet is opening or closing")
	ErrTooManyOpen       = errors.New("too many open wallets")
	ErrInvalidPassword   = errors.New("invalid wallet password")
	ErrPoolClosed        = errors.New("wallet pool is shutting down")
	ErrStartTimeout      = errors.New("wallet rpc did not start in time")
	ErrProcessExited     = errors.New("wallet rpc process exited")
)

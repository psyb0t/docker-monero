// Package walletrpc is a typed client for the monero-wallet-rpc methods this
// service uses. Field names follow wallet_rpc_server_commands_defs.h.
//
// One monero-wallet-rpc process holds at most one open wallet, so a Client is
// bound to a single process and therefore to a single wallet.
package walletrpc

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/docker-monero/internal/pkg/monero/jsonrpc"
)

// JSON-RPC methods.
const (
	methodGetVersion          = "get_version"
	methodCreateWallet        = "create_wallet"
	methodOpenWallet          = "open_wallet"
	methodCloseWallet         = "close_wallet"
	methodRestoreFromSeed     = "restore_deterministic_wallet"
	methodGenerateFromKeys    = "generate_from_keys"
	methodGetHeight           = "get_height"
	methodGetBalance          = "get_balance"
	methodGetAddress          = "get_address"
	methodCreateAddress       = "create_address"
	methodGetTransfers        = "get_transfers"
	methodTransfer            = "transfer"
	methodStore               = "store"
	methodAutoRefresh         = "auto_refresh"
	invalidPasswordMessageTag = "invalid password"
)

// Wallet-rpc error codes from wallet_rpc_server_error_codes.h that the
// service maps to its own sentinels.
const (
	CodeUnknownError           = -1
	CodeWrongAddress           = -2
	CodeDaemonIsBusy           = -3
	CodeGenericTransferError   = -4
	CodeNotOpen                = -13
	CodeAccountIndexOutOfBound = -14
	CodeAddressIndexOutOfBound = -15
	CodeTxNotPossible          = -16
	CodeNotEnoughMoney         = -17
	CodeTxTooLarge             = -18
	CodeNotEnoughOutsToMix     = -19
	CodeZeroDestination        = -20
	CodeWalletAlreadyExists    = -21
	CodeInvalidPassword        = -22
	CodeWrongKey               = -25
	CodeWatchOnly              = -29
	CodeNotEnoughUnlockedMoney = -37
	CodeNoDaemonConnection     = -38
	CodeZeroAmount             = -46
)

// Transfer priorities as wallet2 numbers them.
const (
	PriorityDefault  uint32 = 0
	PriorityUnimport uint32 = 1
	PriorityNormal   uint32 = 2
	PriorityElevated uint32 = 3
	PriorityPriority uint32 = 4
)

// DefaultLanguage is the mnemonic language used when a caller names none.
const DefaultLanguage = "English"

// Client talks to one monero-wallet-rpc process.
type Client struct {
	rpc *jsonrpc.Client
}

// New builds a client for a wallet-rpc base URL.
func New(baseURL string, timeout time.Duration) *Client {
	return &Client{rpc: jsonrpc.New(baseURL, timeout)}
}

// Ping calls get_version, the cheapest method wallet-rpc answers. It succeeds
// as soon as the HTTP server is up, with or without an open wallet.
func (c *Client) Ping(ctx context.Context) error {
	if err := c.rpc.Call(ctx, methodGetVersion, nil, nil); err != nil {
		return ctxerrors.Wrap(err, "ping wallet rpc")
	}

	return nil
}

// CreateWallet creates a new wallet file and opens it.
func (c *Client) CreateWallet(
	ctx context.Context,
	filename, password, language string,
) error {
	request := map[string]string{
		"filename": filename,
		"password": password,
		"language": languageOrDefault(language),
	}

	if err := c.rpc.Call(ctx, methodCreateWallet, request, nil); err != nil {
		return ctxerrors.Wrap(err, "create wallet")
	}

	return nil
}

// OpenWallet opens an existing wallet file.
func (c *Client) OpenWallet(
	ctx context.Context,
	filename, password string,
) error {
	request := map[string]any{
		"filename":         filename,
		"password":         password,
		"autosave_current": true,
	}

	if err := c.rpc.Call(ctx, methodOpenWallet, request, nil); err != nil {
		return ctxerrors.Wrap(err, "open wallet")
	}

	return nil
}

// CloseWallet saves and closes the open wallet.
func (c *Client) CloseWallet(ctx context.Context) error {
	request := map[string]bool{"autosave_current": true}

	if err := c.rpc.Call(ctx, methodCloseWallet, request, nil); err != nil {
		return ctxerrors.Wrap(err, "close wallet")
	}

	return nil
}

// Store saves the open wallet's cache and keys to disk.
func (c *Client) Store(ctx context.Context) error {
	if err := c.rpc.Call(ctx, methodStore, nil, nil); err != nil {
		return ctxerrors.Wrap(err, "store wallet")
	}

	return nil
}

// RestoreFromSeed restores a wallet file from a 25-word mnemonic and opens it.
func (c *Client) RestoreFromSeed(
	ctx context.Context,
	request RestoreFromSeedRequest,
) (string, error) {
	params := map[string]any{
		"filename":         request.Filename,
		"password":         request.Password,
		"seed":             request.Seed,
		"seed_offset":      request.SeedOffset,
		"restore_height":   request.RestoreHeight,
		"language":         languageOrDefault(request.Language),
		"autosave_current": true,
	}

	var result struct {
		Address string `json:"address"`
	}

	err := c.rpc.Call(ctx, methodRestoreFromSeed, params, &result)
	if err != nil {
		return "", ctxerrors.Wrap(err, "restore wallet from seed")
	}

	return result.Address, nil
}

// GenerateFromKeys restores a wallet from its keys and opens it. An empty
// spend key produces a view-only wallet.
func (c *Client) GenerateFromKeys(
	ctx context.Context,
	request GenerateFromKeysRequest,
) (string, error) {
	params := map[string]any{
		"filename":         request.Filename,
		"password":         request.Password,
		"address":          request.Address,
		"viewkey":          request.ViewKey,
		"spendkey":         request.SpendKey,
		"restore_height":   request.RestoreHeight,
		"language":         languageOrDefault(request.Language),
		"autosave_current": true,
	}

	var result struct {
		Address string `json:"address"`
	}

	err := c.rpc.Call(ctx, methodGenerateFromKeys, params, &result)
	if err != nil {
		return "", ctxerrors.Wrap(err, "restore wallet from keys")
	}

	return result.Address, nil
}

// GetHeight returns the height the wallet has scanned up to.
func (c *Client) GetHeight(ctx context.Context) (uint64, error) {
	var result struct {
		Height uint64 `json:"height"`
	}

	if err := c.rpc.Call(ctx, methodGetHeight, nil, &result); err != nil {
		return 0, ctxerrors.Wrap(err, "get wallet height")
	}

	return result.Height, nil
}

// GetBalance returns the balance of one account.
func (c *Client) GetBalance(
	ctx context.Context,
	accountIndex uint32,
) (Balance, error) {
	request := map[string]uint32{"account_index": accountIndex}

	var balance Balance
	if err := c.rpc.Call(ctx, methodGetBalance, request, &balance); err != nil {
		return Balance{}, ctxerrors.Wrap(err, "get balance")
	}

	return balance, nil
}

// GetAddresses returns the primary address and every subaddress of one
// account.
func (c *Client) GetAddresses(
	ctx context.Context,
	accountIndex uint32,
) (Addresses, error) {
	request := map[string]uint32{"account_index": accountIndex}

	var addresses Addresses

	err := c.rpc.Call(ctx, methodGetAddress, request, &addresses)
	if err != nil {
		return Addresses{}, ctxerrors.Wrap(err, "get addresses")
	}

	return addresses, nil
}

// CreateAddress creates one subaddress in an account.
func (c *Client) CreateAddress(
	ctx context.Context,
	accountIndex uint32,
	label string,
) (CreatedAddress, error) {
	request := map[string]any{
		"account_index": accountIndex,
		"label":         label,
	}

	var created CreatedAddress

	err := c.rpc.Call(ctx, methodCreateAddress, request, &created)
	if err != nil {
		return CreatedAddress{}, ctxerrors.Wrap(err, "create address")
	}

	return created, nil
}

// GetTransfers returns the wallet's transfer history for the selected
// categories.
func (c *Client) GetTransfers(
	ctx context.Context,
	filter TransferFilter,
) (Transfers, error) {
	request := map[string]any{
		"in":            filter.In,
		"out":           filter.Out,
		"pending":       filter.Pending,
		"failed":        filter.Failed,
		"pool":          filter.Pool,
		"account_index": filter.AccountIndex,
	}

	var transfers Transfers

	err := c.rpc.Call(ctx, methodGetTransfers, request, &transfers)
	if err != nil {
		return Transfers{}, ctxerrors.Wrap(err, "get transfers")
	}

	return transfers, nil
}

// Transfer builds, signs, and (unless DoNotRelay) broadcasts a transaction.
func (c *Client) Transfer(
	ctx context.Context,
	request TransferRequest,
) (TransferResult, error) {
	params := map[string]any{
		"destinations":    request.Destinations,
		"account_index":   request.AccountIndex,
		"subaddr_indices": request.SubaddrIndices,
		"priority":        request.Priority,
		"get_tx_key":      true,
		"do_not_relay":    request.DoNotRelay,
	}

	var result TransferResult
	if err := c.rpc.Call(ctx, methodTransfer, params, &result); err != nil {
		return TransferResult{}, ctxerrors.Wrap(err, "transfer")
	}

	return result, nil
}

// SetAutoRefresh turns background refresh on with the given period.
func (c *Client) SetAutoRefresh(
	ctx context.Context,
	period time.Duration,
) error {
	seconds := int64(period / time.Second)
	seconds = max(0, min(seconds, math.MaxUint32))

	request := map[string]any{
		"enable": true,
		"period": uint32(seconds), //nolint:gosec // clamped to uint32 above
	}

	if err := c.rpc.Call(ctx, methodAutoRefresh, request, nil); err != nil {
		return ctxerrors.Wrap(err, "set auto refresh")
	}

	return nil
}

// IsInvalidPassword reports whether err is a wrong-password failure.
//
// open_wallet reports a bad password with the generic code -1 and the text
// "Failed to open wallet : Invalid password.", because it overwrites the
// specific code after the exception handler sets it. Other methods use -22.
func IsInvalidPassword(err error) bool {
	var rpcErr *jsonrpc.Error
	if !errors.As(err, &rpcErr) {
		return false
	}

	return rpcErr.Code == CodeInvalidPassword ||
		rpcErr.HasMessage(invalidPasswordMessageTag)
}

// Code returns the wallet-rpc error code carried by err, or 0 when err is
// not a daemon error.
func Code(err error) int {
	var rpcErr *jsonrpc.Error
	if !errors.As(err, &rpcErr) {
		return 0
	}

	return rpcErr.Code
}

func languageOrDefault(language string) string {
	if language == "" {
		return DefaultLanguage
	}

	return language
}

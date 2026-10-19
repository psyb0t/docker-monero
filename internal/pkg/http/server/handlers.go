// Package server implements the REST surface over the generated strict
// handler interface and mounts it, with MCP and the probes, on an
// aichteeteapee serbewr router.
//
// Handlers are deliberately dumb: each collects transport input, calls one
// core service method, and returns either the generated success type or a
// mapped failure. Validation and conversion live in the core services, which
// MCP calls too.
package server

import (
	"context"

	"github.com/psyb0t/docker-monero/internal/pkg/common/invalid"
	corenode "github.com/psyb0t/docker-monero/internal/pkg/core/node"
	corewallets "github.com/psyb0t/docker-monero/internal/pkg/core/wallets"
	"github.com/psyb0t/docker-monero/internal/pkg/http/api"
)

// Server implements api.StrictServerInterface.
type Server struct {
	node    *corenode.Service
	wallets *corewallets.Service
}

var _ api.StrictServerInterface = (*Server)(nil)

// New builds the REST surface over the core services.
func New(
	nodeService *corenode.Service,
	walletService *corewallets.Service,
) *Server {
	return &Server{node: nodeService, wallets: walletService}
}

func missingBody(ctx context.Context) failure {
	return fail(ctx, invalid.Newf("a JSON request body is required"))
}

// GetNodeInfo returns the node's state.
func (s *Server) GetNodeInfo(
	ctx context.Context,
	_ api.GetNodeInfoRequestObject,
) (api.GetNodeInfoResponseObject, error) {
	result, err := s.node.Info(ctx)
	if err != nil {
		return fail(ctx, err), nil
	}

	return api.GetNodeInfo200JSONResponse(result), nil
}

// GetLatestBlock returns the tip header.
func (s *Server) GetLatestBlock(
	ctx context.Context,
	_ api.GetLatestBlockRequestObject,
) (api.GetLatestBlockResponseObject, error) {
	result, err := s.node.LatestBlock(ctx)
	if err != nil {
		return fail(ctx, err), nil
	}

	return api.GetLatestBlock200JSONResponse(result), nil
}

// GetBlockByHeight returns a header by height.
func (s *Server) GetBlockByHeight(
	ctx context.Context,
	request api.GetBlockByHeightRequestObject,
) (api.GetBlockByHeightResponseObject, error) {
	result, err := s.node.BlockByHeight(ctx, request.Height)
	if err != nil {
		return fail(ctx, err), nil
	}

	return api.GetBlockByHeight200JSONResponse(result), nil
}

// GetBlockByHash returns a header by hash.
func (s *Server) GetBlockByHash(
	ctx context.Context,
	request api.GetBlockByHashRequestObject,
) (api.GetBlockByHashResponseObject, error) {
	result, err := s.node.BlockByHash(ctx, request.BlockHash)
	if err != nil {
		return fail(ctx, err), nil
	}

	return api.GetBlockByHash200JSONResponse(result), nil
}

// GetTransaction looks a transaction up.
func (s *Server) GetTransaction(
	ctx context.Context,
	request api.GetTransactionRequestObject,
) (api.GetTransactionResponseObject, error) {
	result, err := s.node.Transaction(ctx, request.TxHash)
	if err != nil {
		return fail(ctx, err), nil
	}

	return api.GetTransaction200JSONResponse(result), nil
}

// GetFeeEstimate returns the fee estimate.
func (s *Server) GetFeeEstimate(
	ctx context.Context,
	_ api.GetFeeEstimateRequestObject,
) (api.GetFeeEstimateResponseObject, error) {
	result, err := s.node.FeeEstimate(ctx)
	if err != nil {
		return fail(ctx, err), nil
	}

	return api.GetFeeEstimate200JSONResponse(result), nil
}

// GetMempoolStats returns mempool statistics.
func (s *Server) GetMempoolStats(
	ctx context.Context,
	_ api.GetMempoolStatsRequestObject,
) (api.GetMempoolStatsResponseObject, error) {
	result, err := s.node.Mempool(ctx)
	if err != nil {
		return fail(ctx, err), nil
	}

	return api.GetMempoolStats200JSONResponse(result), nil
}

// GenerateBlocks mines regtest blocks.
func (s *Server) GenerateBlocks(
	ctx context.Context,
	request api.GenerateBlocksRequestObject,
) (api.GenerateBlocksResponseObject, error) {
	if request.Body == nil {
		return missingBody(ctx), nil
	}

	result, err := s.node.GenerateBlocks(ctx, *request.Body)
	if err != nil {
		return fail(ctx, err), nil
	}

	return api.GenerateBlocks200JSONResponse(result), nil
}

// ListWallets lists wallets.
func (s *Server) ListWallets(
	ctx context.Context,
	_ api.ListWalletsRequestObject,
) (api.ListWalletsResponseObject, error) {
	result, err := s.wallets.List()
	if err != nil {
		return fail(ctx, err), nil
	}

	return api.ListWallets200JSONResponse(result), nil
}

// CreateWallet creates a wallet.
func (s *Server) CreateWallet(
	ctx context.Context,
	request api.CreateWalletRequestObject,
) (api.CreateWalletResponseObject, error) {
	if request.Body == nil {
		return missingBody(ctx), nil
	}

	result, err := s.wallets.Create(ctx, *request.Body)
	if err != nil {
		return fail(ctx, err), nil
	}

	return api.CreateWallet201JSONResponse(result), nil
}

// RestoreWalletFromSeed restores a wallet from its seed.
func (s *Server) RestoreWalletFromSeed(
	ctx context.Context,
	request api.RestoreWalletFromSeedRequestObject,
) (api.RestoreWalletFromSeedResponseObject, error) {
	if request.Body == nil {
		return missingBody(ctx), nil
	}

	result, err := s.wallets.RestoreFromSeed(ctx, *request.Body)
	if err != nil {
		return fail(ctx, err), nil
	}

	return api.RestoreWalletFromSeed201JSONResponse(result), nil
}

// RestoreWalletFromKeys restores a wallet from its keys.
func (s *Server) RestoreWalletFromKeys(
	ctx context.Context,
	request api.RestoreWalletFromKeysRequestObject,
) (api.RestoreWalletFromKeysResponseObject, error) {
	if request.Body == nil {
		return missingBody(ctx), nil
	}

	result, err := s.wallets.RestoreFromKeys(ctx, *request.Body)
	if err != nil {
		return fail(ctx, err), nil
	}

	return api.RestoreWalletFromKeys201JSONResponse(result), nil
}

// OpenWallet opens a wallet.
func (s *Server) OpenWallet(
	ctx context.Context,
	request api.OpenWalletRequestObject,
) (api.OpenWalletResponseObject, error) {
	if request.Body == nil {
		return missingBody(ctx), nil
	}

	result, err := s.wallets.Open(
		ctx,
		request.WalletName,
		request.Body.Password,
	)
	if err != nil {
		return fail(ctx, err), nil
	}

	return api.OpenWallet200JSONResponse(result), nil
}

// CloseWallet closes a wallet.
func (s *Server) CloseWallet(
	ctx context.Context,
	request api.CloseWalletRequestObject,
) (api.CloseWalletResponseObject, error) {
	result, err := s.wallets.Close(ctx, request.WalletName)
	if err != nil {
		return fail(ctx, err), nil
	}

	return api.CloseWallet200JSONResponse(result), nil
}

// GetWalletStatus reports a wallet's status.
func (s *Server) GetWalletStatus(
	ctx context.Context,
	request api.GetWalletStatusRequestObject,
) (api.GetWalletStatusResponseObject, error) {
	result, err := s.wallets.Status(ctx, request.WalletName)
	if err != nil {
		return fail(ctx, err), nil
	}

	return api.GetWalletStatus200JSONResponse(result), nil
}

// GetWalletBalance returns an account balance.
func (s *Server) GetWalletBalance(
	ctx context.Context,
	request api.GetWalletBalanceRequestObject,
) (api.GetWalletBalanceResponseObject, error) {
	result, err := s.wallets.Balance(
		ctx,
		request.WalletName,
		request.Params.AccountIndex,
	)
	if err != nil {
		return fail(ctx, err), nil
	}

	return api.GetWalletBalance200JSONResponse(result), nil
}

// ListWalletAddresses lists an account's addresses.
func (s *Server) ListWalletAddresses(
	ctx context.Context,
	request api.ListWalletAddressesRequestObject,
) (api.ListWalletAddressesResponseObject, error) {
	result, err := s.wallets.Addresses(
		ctx,
		request.WalletName,
		request.Params.AccountIndex,
	)
	if err != nil {
		return fail(ctx, err), nil
	}

	return api.ListWalletAddresses200JSONResponse(result), nil
}

// CreateWalletAddress creates a subaddress.
func (s *Server) CreateWalletAddress(
	ctx context.Context,
	request api.CreateWalletAddressRequestObject,
) (api.CreateWalletAddressResponseObject, error) {
	if request.Body == nil {
		return missingBody(ctx), nil
	}

	result, err := s.wallets.CreateAddress(
		ctx,
		request.WalletName,
		*request.Body,
	)
	if err != nil {
		return fail(ctx, err), nil
	}

	return api.CreateWalletAddress201JSONResponse(result), nil
}

// ListWalletTransfers lists transfers.
func (s *Server) ListWalletTransfers(
	ctx context.Context,
	request api.ListWalletTransfersRequestObject,
) (api.ListWalletTransfersResponseObject, error) {
	query := corewallets.TransferQuery{
		AccountIndex: request.Params.AccountIndex,
		Limit:        request.Params.Limit,
		Offset:       request.Params.Offset,
	}

	if request.Params.Direction != nil {
		query.Directions = *request.Params.Direction
	}

	result, err := s.wallets.Transfers(ctx, request.WalletName, query)
	if err != nil {
		return fail(ctx, err), nil
	}

	return api.ListWalletTransfers200JSONResponse(result), nil
}

// SendTransfer sends funds.
func (s *Server) SendTransfer(
	ctx context.Context,
	request api.SendTransferRequestObject,
) (api.SendTransferResponseObject, error) {
	if request.Body == nil {
		return missingBody(ctx), nil
	}

	result, err := s.wallets.Transfer(ctx, request.WalletName, *request.Body)
	if err != nil {
		return fail(ctx, err), nil
	}

	return api.SendTransfer200JSONResponse(result), nil
}

// Package node is the transport-neutral node service. REST and MCP both call
// it, and it returns the generated API types, so the two transports always
// agree on what the node reported.
package node

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/docker-monero/internal/pkg/common/access"
	"github.com/psyb0t/docker-monero/internal/pkg/common/invalid"
	"github.com/psyb0t/docker-monero/internal/pkg/config"
	"github.com/psyb0t/docker-monero/internal/pkg/http/api"
	"github.com/psyb0t/docker-monero/internal/pkg/monero/daemonrpc"
	"github.com/psyb0t/docker-monero/internal/pkg/monero/jsonrpc"
)

// Sentinels the transports map to status codes.
var (
	ErrNotRegtest = fmt.Errorf(
		"only available on regtest: %w",
		commerr.ErrInvalidState,
	)
	ErrInvalidHash = fmt.Errorf(
		"hash must be 64 hex characters: %w",
		commerr.ErrInvalidArgument,
	)
	ErrInvalidCount = fmt.Errorf(
		"block count out of range: %w",
		commerr.ErrInvalidArgument,
	)
	ErrNotFound = fmt.Errorf("not found: %w", commerr.ErrNotFound)
)

// hashPattern is a 32-byte hash in hex.
var hashPattern = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// Daemon is the monerod surface the service reads.
type Daemon interface {
	GetInfo(ctx context.Context) (daemonrpc.Info, error)
	GetLastBlockHeader(ctx context.Context) (daemonrpc.BlockHeader, error)
	GetBlockHeaderByHeight(
		ctx context.Context,
		height uint64,
	) (daemonrpc.BlockHeader, error)
	GetBlockHeaderByHash(
		ctx context.Context,
		hash string,
	) (daemonrpc.BlockHeader, error)
	GetTransactions(
		ctx context.Context,
		hashes []string,
	) (daemonrpc.Transactions, error)
	GetFeeEstimate(ctx context.Context) (daemonrpc.FeeEstimate, error)
	GetTransactionPoolStats(ctx context.Context) (daemonrpc.PoolStats, error)
	GenerateBlocks(
		ctx context.Context,
		count uint64,
		address string,
	) (daemonrpc.GeneratedBlocks, error)
}

// Options configure the service.
type Options struct {
	Network config.Network

	// ReadOnly refuses regtest mining, the node's only write.
	ReadOnly bool

	// Restarts reports how often the supervisor had to restart monerod.
	Restarts func() uint64
}

// Service answers node queries.
type Service struct {
	daemon  Daemon
	options Options
}

// New builds the service.
func New(daemon Daemon, options Options) *Service {
	if options.Restarts == nil {
		options.Restarts = func() uint64 { return 0 }
	}

	return &Service{daemon: daemon, options: options}
}

// Info returns the node's state and sync progress.
func (s *Service) Info(ctx context.Context) (api.NodeInfo, error) {
	info, err := s.daemon.GetInfo(ctx)
	if err != nil {
		return api.NodeInfo{}, ctxerrors.Wrap(err, "read node info")
	}

	return nodeInfoToAPI(info, s.options.Network, s.options.Restarts()), nil
}

// LatestBlock returns the chain tip's header.
func (s *Service) LatestBlock(ctx context.Context) (api.BlockHeader, error) {
	header, err := s.daemon.GetLastBlockHeader(ctx)
	if err != nil {
		return api.BlockHeader{}, ctxerrors.Wrap(err, "read latest block")
	}

	return blockHeaderToAPI(header), nil
}

// BlockByHeight returns the header at height.
func (s *Service) BlockByHeight(
	ctx context.Context,
	height int64,
) (api.BlockHeader, error) {
	if height < 0 {
		return api.BlockHeader{}, invalid.Newf("height %d is negative", height)
	}

	header, err := s.daemon.GetBlockHeaderByHeight(ctx, uint64(height))
	if err != nil {
		return api.BlockHeader{}, mapLookupError(err, "block at height")
	}

	return blockHeaderToAPI(header), nil
}

// BlockByHash returns the header for a block hash.
func (s *Service) BlockByHash(
	ctx context.Context,
	hash string,
) (api.BlockHeader, error) {
	if !hashPattern.MatchString(hash) {
		return api.BlockHeader{}, ErrInvalidHash
	}

	header, err := s.daemon.GetBlockHeaderByHash(ctx, strings.ToLower(hash))
	if err != nil {
		return api.BlockHeader{}, mapLookupError(err, "block by hash")
	}

	return blockHeaderToAPI(header), nil
}

// Transaction looks up one transaction by hash.
func (s *Service) Transaction(
	ctx context.Context,
	hash string,
) (api.Transaction, error) {
	if !hashPattern.MatchString(hash) {
		return api.Transaction{}, ErrInvalidHash
	}

	result, err := s.daemon.GetTransactions(
		ctx,
		[]string{strings.ToLower(hash)},
	)
	if err != nil {
		return api.Transaction{}, ctxerrors.Wrap(err, "look up transaction")
	}

	if len(result.Txs) == 0 {
		return api.Transaction{}, ctxerrors.Wrap(ErrNotFound, "transaction")
	}

	return transactionToAPI(result.Txs[0]), nil
}

// FeeEstimate returns the current fee estimate.
func (s *Service) FeeEstimate(ctx context.Context) (api.FeeEstimate, error) {
	estimate, err := s.daemon.GetFeeEstimate(ctx)
	if err != nil {
		return api.FeeEstimate{}, ctxerrors.Wrap(err, "read fee estimate")
	}

	return feeEstimateToAPI(estimate), nil
}

// Mempool returns transaction pool statistics.
func (s *Service) Mempool(ctx context.Context) (api.MempoolStats, error) {
	stats, err := s.daemon.GetTransactionPoolStats(ctx)
	if err != nil {
		return api.MempoolStats{}, ctxerrors.Wrap(err, "read mempool")
	}

	return mempoolToAPI(stats), nil
}

// GenerateBlocks mines blocks on regtest.
func (s *Service) GenerateBlocks(
	ctx context.Context,
	request api.GenerateBlocksRequest,
) (api.GeneratedBlocks, error) {
	if s.options.Network != config.NetworkRegtest {
		return api.GeneratedBlocks{}, ErrNotRegtest
	}

	if s.options.ReadOnly {
		return api.GeneratedBlocks{}, access.ErrReadOnly
	}

	if request.Count < 1 || request.Count > int64(api.MaxGenerateBlocks) {
		return api.GeneratedBlocks{}, ctxerrors.Wrapf(
			ErrInvalidCount, "count must be 1-%d", api.MaxGenerateBlocks,
		)
	}

	if request.Address == "" {
		return api.GeneratedBlocks{}, invalid.Newf("address is required")
	}

	generated, err := s.daemon.GenerateBlocks(
		ctx, uint64(request.Count), request.Address,
	)
	if err != nil {
		var rpcErr *jsonrpc.Error
		if errors.As(err, &rpcErr) &&
			rpcErr.Code == daemonrpc.CodeWrongWalletAddress {
			return api.GeneratedBlocks{}, invalid.Newf(
				"the address is not valid",
			)
		}

		return api.GeneratedBlocks{}, ctxerrors.Wrap(err, "generate blocks")
	}

	height := int64(generated.Height) //nolint:gosec // heights fit int64

	return api.GeneratedBlocks{
		Height:      height,
		BlockHashes: nonNil(generated.Blocks),
	}, nil
}

// mapLookupError turns monerod's "no such block" replies into ErrNotFound.
// A height past the tip is TOO_BIG_HEIGHT; an unknown hash is reported as
// an internal error, so only the invalid-parameter shapes map to not found.
func mapLookupError(err error, what string) error {
	var rpcErr *jsonrpc.Error
	if errors.As(err, &rpcErr) {
		switch rpcErr.Code {
		case daemonrpc.CodeTooBigHeight, daemonrpc.CodeInternalError,
			daemonrpc.CodeWrongParam:
			return ctxerrors.Wrap(ErrNotFound, what)
		}
	}

	return ctxerrors.Wrapf(err, "look up %s", what)
}

func nonNil[T any](values []T) []T {
	if values == nil {
		return []T{}
	}

	return values
}

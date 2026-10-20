// Package daemonrpc is a typed client for the subset of monerod RPC this
// service uses. Field names follow core_rpc_server_commands_defs.h.
package daemonrpc

import (
	"context"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/docker-monero/internal/pkg/monero/jsonrpc"
)

// JSON-RPC methods and plain endpoints.
const (
	methodGetInfo              = "get_info"
	methodGetLastBlockHeader   = "get_last_block_header"
	methodGetBlockHeaderHeight = "get_block_header_by_height"
	methodGetBlockHeaderHash   = "get_block_header_by_hash"
	methodGetFeeEstimate       = "get_fee_estimate"
	methodGenerateBlocks       = "generateblocks"
	methodGetVersion           = "get_version"

	pathGetTransactions          = "/get_transactions"
	pathGetTransactionPoolStats  = "/get_transaction_pool_stats"
	maxTransactionsPerRequest    = 100
	errTooManyTransactionsFormat = "at most %d transactions per request"
)

// Info is the get_info result.
type Info struct {
	Height                   uint64 `json:"height"`
	TargetHeight             uint64 `json:"target_height"`
	Difficulty               uint64 `json:"difficulty"`
	Target                   uint64 `json:"target"`
	TxCount                  uint64 `json:"tx_count"`
	TxPoolSize               uint64 `json:"tx_pool_size"`
	AltBlocksCount           uint64 `json:"alt_blocks_count"`
	OutgoingConnectionsCount uint64 `json:"outgoing_connections_count"`
	IncomingConnectionsCount uint64 `json:"incoming_connections_count"`
	WhitePeerlistSize        uint64 `json:"white_peerlist_size"`
	GreyPeerlistSize         uint64 `json:"grey_peerlist_size"`
	Nettype                  string `json:"nettype"`
	TopBlockHash             string `json:"top_block_hash"`
	CumulativeDifficulty     uint64 `json:"cumulative_difficulty"`
	BlockWeightLimit         uint64 `json:"block_weight_limit"`
	BlockWeightMedian        uint64 `json:"block_weight_median"`
	AdjustedTime             uint64 `json:"adjusted_time"`
	StartTime                uint64 `json:"start_time"`
	FreeSpace                uint64 `json:"free_space"`
	Offline                  bool   `json:"offline"`
	DatabaseSize             uint64 `json:"database_size"`
	UpdateAvailable          bool   `json:"update_available"`
	BusySyncing              bool   `json:"busy_syncing"`
	Version                  string `json:"version"`
	Synchronized             bool   `json:"synchronized"`
	Restricted               bool   `json:"restricted"`
}

// BlockHeader is block_header_response.
type BlockHeader struct {
	MajorVersion uint8  `json:"major_version"`
	MinorVersion uint8  `json:"minor_version"`
	Timestamp    uint64 `json:"timestamp"`
	PrevHash     string `json:"prev_hash"`
	Nonce        uint32 `json:"nonce"`
	OrphanStatus bool   `json:"orphan_status"`
	Height       uint64 `json:"height"`
	Depth        uint64 `json:"depth"`
	Hash         string `json:"hash"`
	Difficulty   uint64 `json:"difficulty"`
	Reward       uint64 `json:"reward"`
	BlockSize    uint64 `json:"block_size"`
	BlockWeight  uint64 `json:"block_weight"`
	NumTxes      uint64 `json:"num_txes"`
	MinerTxHash  string `json:"miner_tx_hash"`
}

// FeeEstimate is the get_fee_estimate result. Fees are atomic units per
// byte of weight; Fees holds one value per priority level.
type FeeEstimate struct {
	Fee              uint64   `json:"fee"`
	QuantizationMask uint64   `json:"quantization_mask"`
	Fees             []uint64 `json:"fees"`
}

// PoolStats is txpool_stats.
type PoolStats struct {
	BytesTotal      uint64 `json:"bytes_total"`
	BytesMin        uint32 `json:"bytes_min"`
	BytesMax        uint32 `json:"bytes_max"`
	BytesMed        uint32 `json:"bytes_med"`
	FeeTotal        uint64 `json:"fee_total"`
	Oldest          uint64 `json:"oldest"`
	TxsTotal        uint32 `json:"txs_total"`
	NumFailing      uint32 `json:"num_failing"`
	Num10m          uint32 `json:"num_10m"`
	NumNotRelayed   uint32 `json:"num_not_relayed"`
	NumDoubleSpends uint32 `json:"num_double_spends"`
}

// Transaction is one entry of /get_transactions.
type Transaction struct {
	TxHash            string `json:"tx_hash"`
	InPool            bool   `json:"in_pool"`
	DoubleSpendSeen   bool   `json:"double_spend_seen"`
	BlockHeight       uint64 `json:"block_height"`
	Confirmations     uint64 `json:"confirmations"`
	BlockTimestamp    uint64 `json:"block_timestamp"`
	Relayed           bool   `json:"relayed"`
	ReceivedTimestamp uint64 `json:"received_timestamp"`
}

// Transactions is the /get_transactions result.
type Transactions struct {
	Txs      []Transaction `json:"txs"`
	MissedTx []string      `json:"missed_tx"`
}

// GeneratedBlocks is the generateblocks result.
type GeneratedBlocks struct {
	Height uint64   `json:"height"`
	Blocks []string `json:"blocks"`
}

// Version is the get_version result. Version packs major and minor RPC
// versions as major<<16 | minor.
type Version struct {
	Version uint32 `json:"version"`
	Release bool   `json:"release"`
}

// Client talks to one monerod RPC listener.
type Client struct {
	rpc *jsonrpc.Client
}

// New builds a client for a monerod base URL.
func New(
	baseURL string,
	timeout time.Duration,
	options ...jsonrpc.Option,
) *Client {
	return &Client{rpc: jsonrpc.New(baseURL, timeout, options...)}
}

// GetInfo returns the node's general state.
func (c *Client) GetInfo(ctx context.Context) (Info, error) {
	var info Info
	if err := c.rpc.Call(ctx, methodGetInfo, nil, &info); err != nil {
		return Info{}, ctxerrors.Wrap(err, "get daemon info")
	}

	return info, nil
}

// GetVersion returns the RPC version. It is the cheapest call monerod
// answers, which makes it the readiness probe.
func (c *Client) GetVersion(ctx context.Context) (Version, error) {
	var version Version
	if err := c.rpc.Call(ctx, methodGetVersion, nil, &version); err != nil {
		return Version{}, ctxerrors.Wrap(err, "get daemon version")
	}

	return version, nil
}

type blockHeaderResult struct {
	BlockHeader BlockHeader `json:"block_header"`
}

// GetLastBlockHeader returns the chain tip.
func (c *Client) GetLastBlockHeader(ctx context.Context) (BlockHeader, error) {
	var result blockHeaderResult
	if err := c.rpc.Call(
		ctx, methodGetLastBlockHeader, nil, &result,
	); err != nil {
		return BlockHeader{}, ctxerrors.Wrap(err, "get last block header")
	}

	return result.BlockHeader, nil
}

// GetBlockHeaderByHeight returns the header at height.
func (c *Client) GetBlockHeaderByHeight(
	ctx context.Context,
	height uint64,
) (BlockHeader, error) {
	var result blockHeaderResult
	if err := c.rpc.Call(
		ctx, methodGetBlockHeaderHeight,
		map[string]uint64{"height": height}, &result,
	); err != nil {
		return BlockHeader{}, ctxerrors.Wrapf(
			err, "get block header at height %d", height,
		)
	}

	return result.BlockHeader, nil
}

// GetBlockHeaderByHash returns the header for a block hash.
func (c *Client) GetBlockHeaderByHash(
	ctx context.Context,
	hash string,
) (BlockHeader, error) {
	var result blockHeaderResult
	if err := c.rpc.Call(
		ctx, methodGetBlockHeaderHash,
		map[string]string{"hash": hash}, &result,
	); err != nil {
		return BlockHeader{}, ctxerrors.Wrap(err, "get block header by hash")
	}

	return result.BlockHeader, nil
}

// GetFeeEstimate returns the current per-byte fee estimate.
func (c *Client) GetFeeEstimate(ctx context.Context) (FeeEstimate, error) {
	var estimate FeeEstimate
	if err := c.rpc.Call(
		ctx, methodGetFeeEstimate, nil, &estimate,
	); err != nil {
		return FeeEstimate{}, ctxerrors.Wrap(err, "get fee estimate")
	}

	return estimate, nil
}

// GetTransactionPoolStats returns aggregate mempool statistics.
func (c *Client) GetTransactionPoolStats(
	ctx context.Context,
) (PoolStats, error) {
	var result struct {
		PoolStats PoolStats `json:"pool_stats"`
	}

	if err := c.rpc.Post(
		ctx, pathGetTransactionPoolStats, struct{}{}, &result,
	); err != nil {
		return PoolStats{}, ctxerrors.Wrap(err, "get transaction pool stats")
	}

	return result.PoolStats, nil
}

// GetTransactions looks transactions up by hash. Unknown hashes come back in
// MissedTx rather than as an error.
func (c *Client) GetTransactions(
	ctx context.Context,
	hashes []string,
) (Transactions, error) {
	if len(hashes) > maxTransactionsPerRequest {
		return Transactions{}, ctxerrors.Wrapf(
			ErrTooManyTransactions,
			errTooManyTransactionsFormat, maxTransactionsPerRequest,
		)
	}

	request := map[string]any{"txs_hashes": hashes}

	var result Transactions

	err := c.rpc.Post(ctx, pathGetTransactions, request, &result)
	if err != nil {
		return Transactions{}, ctxerrors.Wrap(err, "get transactions")
	}

	return result, nil
}

// GenerateBlocks mines blocks to address. monerod only accepts it in
// regtest mode and only on the unrestricted listener.
func (c *Client) GenerateBlocks(
	ctx context.Context,
	count uint64,
	address string,
) (GeneratedBlocks, error) {
	request := map[string]any{
		"amount_of_blocks": count,
		"wallet_address":   address,
	}

	var result GeneratedBlocks

	err := c.rpc.Call(ctx, methodGenerateBlocks, request, &result)
	if err != nil {
		return GeneratedBlocks{}, ctxerrors.Wrapf(
			err, "generate %d blocks", count,
		)
	}

	return result, nil
}

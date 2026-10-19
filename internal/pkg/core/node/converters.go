package node

import (
	"time"

	"github.com/psyb0t/docker-monero/internal/pkg/config"
	"github.com/psyb0t/docker-monero/internal/pkg/http/api"
	"github.com/psyb0t/docker-monero/internal/pkg/monero/amount"
	"github.com/psyb0t/docker-monero/internal/pkg/monero/daemonrpc"
)

const fullPercent = 100.0

// Chain values are uint64 on the wire. Heights, sizes, and counts stay far
// below 2^63, so the int64 the API uses is lossless in practice.
//
//nolint:gosec // see above
func nodeInfoToAPI(
	info daemonrpc.Info,
	network config.Network,
	restarts uint64,
) api.NodeInfo {
	result := api.NodeInfo{
		Network:             api.Network(network),
		Height:              int64(info.Height),
		TargetHeight:        int64(info.TargetHeight),
		Synchronized:        info.Synchronized,
		BusySyncing:         info.BusySyncing,
		SyncPercent:         SyncPercent(info),
		Offline:             info.Offline,
		Difficulty:          int64(info.Difficulty),
		TxPoolSize:          int64(info.TxPoolSize),
		OutgoingConnections: int64(info.OutgoingConnectionsCount),
		IncomingConnections: int64(info.IncomingConnectionsCount),
		DatabaseSize:        int64(info.DatabaseSize),
		Version:             info.Version,
		TopBlockHash:        info.TopBlockHash,
		UpdateAvailable:     info.UpdateAvailable,
		Restricted:          info.Restricted,
		Restarts:            int64(restarts),
	}

	if info.StartTime > 0 {
		startedAt := unixTime(info.StartTime)
		result.StartedAt = &startedAt
	}

	return result
}

// SyncPercent is how far the node has synced. monerod reports a target of
// zero once it considers itself synced, and also before it has peers, so
// the synchronized flag decides which of the two a zero means.
func SyncPercent(info daemonrpc.Info) float64 {
	if info.Synchronized && info.TargetHeight <= info.Height {
		return fullPercent
	}

	if info.TargetHeight == 0 {
		return 0
	}

	return min(
		fullPercent,
		float64(info.Height)/float64(info.TargetHeight)*fullPercent,
	)
}

//nolint:gosec // see nodeInfoToAPI
func blockHeaderToAPI(header daemonrpc.BlockHeader) api.BlockHeader {
	return api.BlockHeader{
		Height:       int64(header.Height),
		Hash:         header.Hash,
		PrevHash:     header.PrevHash,
		Timestamp:    unixTime(header.Timestamp),
		MajorVersion: int(header.MajorVersion),
		MinorVersion: int(header.MinorVersion),
		Difficulty:   int64(header.Difficulty),
		Reward:       amount.FormatAtomic(header.Reward),
		RewardXmr:    amount.FormatXMR(header.Reward),
		BlockSize:    int64(header.BlockSize),
		BlockWeight:  int64(header.BlockWeight),
		NumTxes:      int64(header.NumTxes),
		Depth:        int64(header.Depth),
		Orphan:       header.OrphanStatus,
		MinerTxHash:  header.MinerTxHash,
	}
}

//nolint:gosec // see nodeInfoToAPI
func transactionToAPI(tx daemonrpc.Transaction) api.Transaction {
	result := api.Transaction{
		TxHash:          tx.TxHash,
		InPool:          tx.InPool,
		Confirmations:   int64(tx.Confirmations),
		DoubleSpendSeen: tx.DoubleSpendSeen,
		Relayed:         tx.Relayed,
	}

	if !tx.InPool {
		height := int64(tx.BlockHeight)
		result.BlockHeight = &height
	}

	if tx.BlockTimestamp > 0 {
		blockTime := unixTime(tx.BlockTimestamp)
		result.BlockTimestamp = &blockTime
	}

	if tx.ReceivedTimestamp > 0 {
		received := unixTime(tx.ReceivedTimestamp)
		result.ReceivedAt = &received
	}

	return result
}

//nolint:gosec // see nodeInfoToAPI
func feeEstimateToAPI(estimate daemonrpc.FeeEstimate) api.FeeEstimate {
	fees := make([]api.AtomicAmount, 0, len(estimate.Fees))
	for _, fee := range estimate.Fees {
		fees = append(fees, amount.FormatAtomic(fee))
	}

	return api.FeeEstimate{
		FeePerByte:       amount.FormatAtomic(estimate.Fee),
		QuantizationMask: int64(estimate.QuantizationMask),
		FeesPerByte:      fees,
	}
}

//nolint:gosec // see nodeInfoToAPI
func mempoolToAPI(stats daemonrpc.PoolStats) api.MempoolStats {
	result := api.MempoolStats{
		TxCount:      int64(stats.TxsTotal),
		BytesTotal:   int64(stats.BytesTotal),
		FeeTotal:     amount.FormatAtomic(stats.FeeTotal),
		FeeTotalXmr:  amount.FormatXMR(stats.FeeTotal),
		Failing:      int64(stats.NumFailing),
		NotRelayed:   int64(stats.NumNotRelayed),
		DoubleSpends: int64(stats.NumDoubleSpends),
	}

	if stats.Oldest > 0 {
		oldest := unixTime(stats.Oldest)
		result.OldestReceivedAt = &oldest
	}

	return result
}

//nolint:gosec // unix seconds fit int64
func unixTime(seconds uint64) time.Time {
	return time.Unix(int64(seconds), 0).UTC()
}

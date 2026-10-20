package node

import (
	"strconv"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/docker-monero/internal/pkg/config"
)

// Bind addresses. Unrestricted RPC never leaves the container's loopback;
// P2P and restricted RPC bind every interface so Docker can publish them.
const (
	bindAll             = "0.0.0.0"
	rpcLoginEnvironment = "RPC_LOGIN"

	// regtestFixedDifficulty keeps regtest mining instant.
	regtestFixedDifficulty = "1"

	// privateConnectionLimit lifts monerod's default of 25 connections per
	// private IP. Every wallet-rpc process connects from 127.0.0.1.
	privateConnectionLimit = "100"
)

// Environment supplies only monerod's optional RPC credentials.
func Environment(cfg config.Config) []string {
	if cfg.RPCLogin == "" {
		return nil
	}

	return []string{rpcLoginEnvironment + "=" + cfg.RPCLogin}
}

// Args builds monerod's command line from the deployment configuration.
//
// The unrestricted RPC listener is pinned to loopback. Everything that can
// stop the daemon, roll back the chain, or ban peers lives there, so it must
// only be reachable by this process and the wallet-rpc children.
func Args(cfg config.Config) ([]string, error) {
	extra, err := cfg.MonerodExtraArgList()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "monerod extra arguments")
	}

	args := []string{
		"--non-interactive",
		"--data-dir", cfg.DataDir,
		"--p2p-bind-ip", bindAll,
		"--p2p-bind-port", strconv.Itoa(cfg.P2PPort),
		"--rpc-bind-ip", config.LoopbackHost,
		"--rpc-bind-port", strconv.Itoa(cfg.RPCPort),
		"--rpc-max-connections-per-private-ip", privateConnectionLimit,
		"--no-igd",
		"--no-zmq",
		"--check-updates", "disabled",
	}

	if cfg.RestrictedRPCEnabled {
		args = append(args,
			"--rpc-restricted-bind-ip", bindAll,
			"--rpc-restricted-bind-port", strconv.Itoa(cfg.RestrictedRPCPort),
		)

		if cfg.PublicNode {
			args = append(args, "--public-node")
		}
	}

	if cfg.Prune {
		args = append(args, "--prune-blockchain")
	}

	args = append(args, networkArgs(cfg.NetworkType())...)

	return append(args, extra...), nil
}

// networkArgs selects the chain. Regtest runs offline at a fixed difficulty
// and keeps its chain across restarts; without --keep-fakechain monerod
// wipes it on every start.
func networkArgs(network config.Network) []string {
	switch network {
	case config.NetworkStagenet:
		return []string{"--stagenet", "--enable-dns-blocklist"}
	case config.NetworkTestnet:
		return []string{"--testnet"}
	case config.NetworkRegtest:
		return []string{
			"--regtest",
			"--offline",
			"--keep-fakechain",
			"--fixed-difficulty", regtestFixedDifficulty,
			"--disable-dns-checkpoints",
		}
	case config.NetworkMainnet:
		return []string{"--enable-dns-blocklist"}
	default:
		return nil
	}
}

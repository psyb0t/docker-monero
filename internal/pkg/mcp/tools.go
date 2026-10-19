package mcp

import (
	"context"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	corewallets "github.com/psyb0t/docker-monero/internal/pkg/core/wallets"
	"github.com/psyb0t/docker-monero/internal/pkg/http/api"
)

// Empty is the input of a tool that takes no arguments.
type Empty struct{}

// BlockInput selects a block. With neither field set it returns the tip.
//
//nolint:lll // jsonschema descriptions are tag text
type BlockInput struct {
	Height *int64  `json:"height,omitempty" jsonschema:"block height; omit for the latest block"`
	Hash   *string `json:"hash,omitempty"   jsonschema:"block hash, 64 hex characters; used when height is omitted"`
}

// TxInput names a transaction.
//
//nolint:lll // jsonschema descriptions are tag text
type TxInput struct {
	TxHash string `json:"txHash" jsonschema:"transaction hash, 64 hex characters"`
}

// GenerateBlocksInput is the regtest mining request.
//
//nolint:lll // jsonschema descriptions are tag text
type GenerateBlocksInput struct {
	Count   int64  `json:"count"   jsonschema:"blocks to mine, 1-1000"`
	Address string `json:"address" jsonschema:"address that receives the block rewards"`
}

// WalletInput names a wallet.
type WalletInput struct {
	Name string `json:"name" jsonschema:"wallet name"`
}

// CreateWalletInput creates a wallet.
//
//nolint:lll // jsonschema descriptions are tag text
type CreateWalletInput struct {
	Name     string  `json:"name"               jsonschema:"new wallet name: letters, digits, _ and -"`
	Password string  `json:"password"           jsonschema:"password that encrypts the wallet file"`
	Language *string `json:"language,omitempty" jsonschema:"mnemonic language, English when omitted"`
}

// OpenWalletInput opens a wallet.
type OpenWalletInput struct {
	Name     string `json:"name"     jsonschema:"wallet name"`
	Password string `json:"password" jsonschema:"wallet password"`
}

// AccountInput names a wallet and an account.
//
//nolint:lll // jsonschema descriptions are tag text
type AccountInput struct {
	Name         string `json:"name"                   jsonschema:"wallet name"`
	AccountIndex *int64 `json:"accountIndex,omitempty" jsonschema:"account index, 0 when omitted"`
}

// CreateAddressInput creates a subaddress.
//
//nolint:lll // jsonschema descriptions are tag text
type CreateAddressInput struct {
	Name         string  `json:"name"                   jsonschema:"wallet name"`
	AccountIndex *int64  `json:"accountIndex,omitempty" jsonschema:"account index, 0 when omitted"`
	Label        *string `json:"label,omitempty"        jsonschema:"label for the new subaddress"`
}

// TransfersInput lists transfers.
//
//nolint:lll // jsonschema descriptions are tag text
type TransfersInput struct {
	Name         string                  `json:"name"                   jsonschema:"wallet name"`
	AccountIndex *int64                  `json:"accountIndex,omitempty" jsonschema:"account index, 0 when omitted"`
	Directions   []api.TransferDirection `json:"directions,omitempty"   jsonschema:"any of in, out, pending, failed, pool; all when omitted"`
	Limit        *int                    `json:"limit,omitempty"        jsonschema:"page size, 1-1000, default 100"`
	Offset       *int                    `json:"offset,omitempty"       jsonschema:"entries to skip"`
}

// TransferInput sends funds.
//
//nolint:lll // jsonschema descriptions are tag text
type TransferInput struct {
	Name         string                `json:"name"                   jsonschema:"wallet to send from"`
	Destinations []api.Destination     `json:"destinations"           jsonschema:"recipients; each amount is a string of atomic units"`
	AccountIndex *int64                `json:"accountIndex,omitempty" jsonschema:"account to spend from, 0 when omitted"`
	Priority     *api.TransferPriority `json:"priority,omitempty"     jsonschema:"default, low, normal, high, or highest"`
	DryRun       *bool                 `json:"dryRun,omitempty"       jsonschema:"build and price the transaction without broadcasting it"`
}

// registration adds one tool.
type registration func() error

func (t *toolset) register(server *mcpsdk.Server, options Options) error {
	registrations := t.readTools(server)

	// Read-only mode registers no write tool at all, so an agent never sees
	// an action it could not take.
	if !options.ReadOnly {
		registrations = append(registrations, t.writeTools(server, options)...)
	}

	for _, register := range registrations {
		if err := register(); err != nil {
			return err
		}
	}

	return nil
}

func (t *toolset) readTools(server *mcpsdk.Server) []registration {
	return []registration{
		func() error {
			return add(server, t.metrics, ToolNodeInfo,
				"Node height, sync progress, connections, and version.",
				t.nodeInfo)
		},
		func() error {
			return add(server, t.metrics, ToolGetBlock,
				"Block header by height or hash, or the latest block.",
				t.getBlock)
		},
		func() error {
			return add(server, t.metrics, ToolGetTransaction,
				"Whether a transaction is in the pool or a block, "+
					"and its confirmations.",
				t.getTransaction)
		},
		func() error {
			return add(server, t.metrics, ToolFeeEstimate,
				"Current per-byte fee estimate for each priority.",
				t.feeEstimate)
		},
		func() error {
			return add(server, t.metrics, ToolListWallets,
				"Every wallet on this node and whether it is open.",
				t.listWallets)
		},
		func() error {
			return add(server, t.metrics, ToolOpenWallet,
				"Open an existing wallet with its password.", t.openWallet)
		},
		func() error {
			return add(server, t.metrics, ToolCloseWallet,
				"Save and close an open wallet.", t.closeWallet)
		},
		func() error {
			return add(server, t.metrics, ToolWalletStatus,
				"Whether a wallet is open and how far it has synced "+
					"with the node.",
				t.walletStatus)
		},
		func() error {
			return add(server, t.metrics, ToolWalletBalance,
				"Total and unlocked balance of one account of an open "+
					"wallet.",
				t.walletBalance)
		},
		func() error {
			return add(server, t.metrics, ToolWalletAddresses,
				"Primary address and subaddresses of one account of an "+
					"open wallet.",
				t.walletAddresses)
		},
		func() error {
			return add(server, t.metrics, ToolWalletTransfers,
				"Transfer history of one account of an open wallet, "+
					"newest first.",
				t.walletTransfers)
		},
	}
}

func (t *toolset) writeTools(
	server *mcpsdk.Server,
	options Options,
) []registration {
	registrations := []registration{
		func() error {
			return add(server, t.metrics, ToolCreateWallet,
				"Create a new wallet and leave it open.", t.createWallet)
		},
		func() error {
			return add(server, t.metrics, ToolCreateAddress,
				"Create a new subaddress to receive funds.", t.createAddress)
		},
	}

	if options.Regtest {
		registrations = append(registrations, func() error {
			return add(server, t.metrics, ToolGenerateBlocks,
				"Mine blocks instantly on regtest and pay the rewards to "+
					"an address.",
				t.generateBlocks)
		})
	}

	if options.AllowTransfers {
		registrations = append(registrations, func() error {
			return add(server, t.metrics, ToolTransfer,
				"Send XMR from an open, fully synced wallet. Use dryRun "+
					"to see the fee first.",
				t.transfer)
		})
	}

	return registrations
}

func (t *toolset) nodeInfo(ctx context.Context, _ Empty) (api.NodeInfo, error) {
	return t.node.Info(ctx)
}

func (t *toolset) getBlock(
	ctx context.Context,
	input BlockInput,
) (api.BlockHeader, error) {
	switch {
	case input.Height != nil:
		return t.node.BlockByHeight(
			ctx,
			*input.Height,
		)
	case input.Hash != nil:
		return t.node.BlockByHash(
			ctx,
			*input.Hash,
		)
	default:
		return t.node.LatestBlock(
			ctx,
		)
	}
}

func (t *toolset) getTransaction(
	ctx context.Context,
	input TxInput,
) (api.Transaction, error) {
	return t.node.Transaction(
		ctx,
		input.TxHash,
	)
}

func (t *toolset) feeEstimate(
	ctx context.Context,
	_ Empty,
) (api.FeeEstimate, error) {
	return t.node.FeeEstimate(ctx)
}

func (t *toolset) generateBlocks(
	ctx context.Context,
	input GenerateBlocksInput,
) (api.GeneratedBlocks, error) {
	return t.node.GenerateBlocks(
		ctx,
		api.GenerateBlocksRequest{
			Count: input.Count, Address: input.Address,
		},
	)
}

func (t *toolset) listWallets(
	_ context.Context,
	_ Empty,
) (api.WalletList, error) {
	return t.wallets.List()
}

func (t *toolset) createWallet(
	ctx context.Context,
	input CreateWalletInput,
) (api.WalletStatus, error) {
	return t.wallets.Create(
		ctx,
		api.CreateWalletRequest{
			Name:     input.Name,
			Password: input.Password,
			Language: input.Language,
		},
	)
}

func (t *toolset) openWallet(
	ctx context.Context,
	input OpenWalletInput,
) (api.WalletStatus, error) {
	return t.wallets.Open(
		ctx,
		input.Name,
		input.Password,
	)
}

func (t *toolset) closeWallet(
	ctx context.Context,
	input WalletInput,
) (api.WalletStatus, error) {
	return t.wallets.Close(
		ctx,
		input.Name,
	)
}

func (t *toolset) walletStatus(
	ctx context.Context,
	input WalletInput,
) (api.WalletStatus, error) {
	return t.wallets.Status(
		ctx,
		input.Name,
	)
}

func (t *toolset) walletBalance(
	ctx context.Context,
	input AccountInput,
) (api.Balance, error) {
	return t.wallets.Balance(
		ctx,
		input.Name,
		input.AccountIndex,
	)
}

func (t *toolset) walletAddresses(
	ctx context.Context,
	input AccountInput,
) (api.AddressList, error) {
	return t.wallets.Addresses(
		ctx,
		input.Name,
		input.AccountIndex,
	)
}

func (t *toolset) createAddress(
	ctx context.Context,
	input CreateAddressInput,
) (api.Address, error) {
	return t.wallets.CreateAddress(
		ctx,
		input.Name,
		api.CreateAddressRequest{
			AccountIndex: input.AccountIndex, Label: input.Label,
		},
	)
}

func (t *toolset) walletTransfers(
	ctx context.Context,
	input TransfersInput,
) (api.TransferList, error) {
	return t.wallets.Transfers(
		ctx,
		input.Name,
		corewallets.TransferQuery{
			AccountIndex: input.AccountIndex,
			Directions:   input.Directions,
			Limit:        input.Limit,
			Offset:       input.Offset,
		},
	)
}

func (t *toolset) transfer(
	ctx context.Context,
	input TransferInput,
) (api.TransferResult, error) {
	return t.wallets.Transfer(
		ctx,
		input.Name,
		api.TransferRequest{
			Destinations: input.Destinations,
			AccountIndex: input.AccountIndex,
			Priority:     input.Priority,
			DryRun:       input.DryRun,
		},
	)
}

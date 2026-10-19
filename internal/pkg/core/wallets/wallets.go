// Package wallets is the transport-neutral wallet service. REST and MCP both
// call it, so a wallet opened, queried, or spent from over either transport
// goes through the same checks and returns the same API types.
package wallets

import (
	"context"
	"sort"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/docker-monero/internal/pkg/common/access"
	"github.com/psyb0t/docker-monero/internal/pkg/common/invalid"
	"github.com/psyb0t/docker-monero/internal/pkg/http/api"
	"github.com/psyb0t/docker-monero/internal/pkg/monero/daemonrpc"
	"github.com/psyb0t/docker-monero/internal/pkg/monero/walletrpc"
	"github.com/psyb0t/docker-monero/internal/pkg/walletpool"
)

// Daemon is the monerod surface the wallet service needs: the chain height
// and sync state that decide whether a wallet is caught up.
type Daemon interface {
	GetInfo(ctx context.Context) (daemonrpc.Info, error)
}

// Pool is the wallet process pool.
type Pool interface {
	List() ([]walletpool.Entry, error)
	OpenCount() int
	MaxOpen() int
	Get(name string) (*walletpool.Wallet, error)
	Exists(name string) (bool, error)
	Create(
		ctx context.Context,
		name, password, language string,
	) (*walletpool.Wallet, error)
	RestoreFromSeed(
		ctx context.Context,
		request walletrpc.RestoreFromSeedRequest,
	) (*walletpool.Wallet, error)
	RestoreFromKeys(
		ctx context.Context,
		request walletrpc.GenerateFromKeysRequest,
	) (*walletpool.Wallet, error)
	Open(ctx context.Context, name, password string) (*walletpool.Wallet, error)
	Close(ctx context.Context, name string) error
}

// Options are the deployment's write switches.
type Options struct {
	// ReadOnly refuses every write. It wins over AllowTransfers.
	ReadOnly bool

	// AllowTransfers false refuses sends while other writes still work.
	AllowTransfers bool
}

// Service manages wallets.
type Service struct {
	pool    Pool
	daemon  Daemon
	options Options
}

// New builds the service.
func New(pool Pool, daemon Daemon, options Options) *Service {
	return &Service{pool: pool, daemon: daemon, options: options}
}

// requireWritable refuses a write in read-only mode.
func (s *Service) requireWritable() error {
	if s.options.ReadOnly {
		return access.ErrReadOnly
	}

	return nil
}

// List returns every wallet on disk and whether it is open.
func (s *Service) List() (api.WalletList, error) {
	entries, err := s.pool.List()
	if err != nil {
		return api.WalletList{}, ctxerrors.Wrap(err, "list wallets")
	}

	wallets := make([]api.WalletSummary, 0, len(entries))
	for _, entry := range entries {
		wallets = append(wallets, api.WalletSummary{
			Name: entry.Name, Open: entry.Open,
		})
	}

	return api.WalletList{
		Wallets:   wallets,
		OpenCount: s.pool.OpenCount(),
		MaxOpen:   s.pool.MaxOpen(),
	}, nil
}

// Create creates a wallet and returns its status.
func (s *Service) Create(
	ctx context.Context,
	request api.CreateWalletRequest,
) (api.WalletStatus, error) {
	if err := s.requireWritable(); err != nil {
		return api.WalletStatus{}, err
	}

	if err := validatePassword(request.Password); err != nil {
		return api.WalletStatus{}, err
	}

	wallet, err := s.pool.Create(
		ctx, request.Name, request.Password, deref(request.Language),
	)
	if err != nil {
		return api.WalletStatus{}, ctxerrors.Wrap(err, "create wallet")
	}

	return s.openedStatus(ctx, wallet), nil
}

// RestoreFromSeed restores a wallet from its mnemonic.
func (s *Service) RestoreFromSeed(
	ctx context.Context,
	request api.RestoreFromSeedRequest,
) (api.WalletStatus, error) {
	if err := s.requireWritable(); err != nil {
		return api.WalletStatus{}, err
	}

	if err := validatePassword(request.Password); err != nil {
		return api.WalletStatus{}, err
	}

	if request.Seed == "" {
		return api.WalletStatus{}, invalid.Newf("seed is required")
	}

	restoreHeight, err := nonNegative(request.RestoreHeight, "restoreHeight")
	if err != nil {
		return api.WalletStatus{}, err
	}

	wallet, err := s.pool.RestoreFromSeed(ctx, walletrpc.RestoreFromSeedRequest{
		Filename:      request.Name,
		Password:      request.Password,
		Seed:          request.Seed,
		SeedOffset:    deref(request.SeedOffset),
		RestoreHeight: restoreHeight,
		Language:      deref(request.Language),
	})
	if err != nil {
		return api.WalletStatus{}, mapWalletError(
			err,
			"restore wallet from seed",
		)
	}

	return s.openedStatus(ctx, wallet), nil
}

// RestoreFromKeys restores a wallet from its keys.
func (s *Service) RestoreFromKeys(
	ctx context.Context,
	request api.RestoreFromKeysRequest,
) (api.WalletStatus, error) {
	if err := s.requireWritable(); err != nil {
		return api.WalletStatus{}, err
	}

	if err := validatePassword(request.Password); err != nil {
		return api.WalletStatus{}, err
	}

	if request.Address == "" || request.ViewKey == "" {
		return api.WalletStatus{}, invalid.Newf(
			"address and viewKey are required",
		)
	}

	restoreHeight, err := nonNegative(request.RestoreHeight, "restoreHeight")
	if err != nil {
		return api.WalletStatus{}, err
	}

	wallet, err := s.pool.RestoreFromKeys(
		ctx,
		walletrpc.GenerateFromKeysRequest{
			Filename:      request.Name,
			Password:      request.Password,
			Address:       request.Address,
			ViewKey:       request.ViewKey,
			SpendKey:      deref(request.SpendKey),
			RestoreHeight: restoreHeight,
			Language:      deref(request.Language),
		},
	)
	if err != nil {
		return api.WalletStatus{}, mapWalletError(
			err,
			"restore wallet from keys",
		)
	}

	return s.openedStatus(ctx, wallet), nil
}

// Open opens an existing wallet.
func (s *Service) Open(
	ctx context.Context,
	name, password string,
) (api.WalletStatus, error) {
	if err := validatePassword(password); err != nil {
		return api.WalletStatus{}, err
	}

	wallet, err := s.pool.Open(ctx, name, password)
	if err != nil {
		return api.WalletStatus{}, ctxerrors.Wrap(err, "open wallet")
	}

	return s.openedStatus(ctx, wallet), nil
}

// Close saves and closes a wallet.
func (s *Service) Close(
	ctx context.Context,
	name string,
) (api.WalletStatus, error) {
	if err := s.pool.Close(ctx, name); err != nil {
		return api.WalletStatus{}, ctxerrors.Wrap(err, "close wallet")
	}

	return api.WalletStatus{Name: name, Open: false}, nil
}

// Status reports whether a wallet is open and how far it has synced.
func (s *Service) Status(
	ctx context.Context,
	name string,
) (api.WalletStatus, error) {
	wallet, err := s.pool.Get(name)
	if err == nil {
		return s.openStatus(ctx, wallet)
	}

	if !isNotOpen(err) {
		return api.WalletStatus{}, ctxerrors.Wrap(err, "wallet status")
	}

	exists, existsErr := s.pool.Exists(name)
	if existsErr != nil {
		return api.WalletStatus{}, ctxerrors.Wrap(existsErr, "wallet status")
	}

	if !exists {
		return api.WalletStatus{}, ctxerrors.Wrap(
			walletpool.ErrWalletNotFound,
			name,
		)
	}

	return api.WalletStatus{Name: name, Open: false}, nil
}

// Balance returns one account's balance.
func (s *Service) Balance(
	ctx context.Context,
	name string,
	accountIndex *int64,
) (api.Balance, error) {
	account, err := accountIndexOf(accountIndex)
	if err != nil {
		return api.Balance{}, err
	}

	wallet, err := s.pool.Get(name)
	if err != nil {
		return api.Balance{}, ctxerrors.Wrap(err, "wallet balance")
	}

	balance, err := wallet.Client.GetBalance(ctx, account)
	if err != nil {
		return api.Balance{}, mapWalletError(err, "wallet balance")
	}

	return balanceToAPI(account, balance), nil
}

// Addresses lists one account's addresses.
func (s *Service) Addresses(
	ctx context.Context,
	name string,
	accountIndex *int64,
) (api.AddressList, error) {
	account, err := accountIndexOf(accountIndex)
	if err != nil {
		return api.AddressList{}, err
	}

	wallet, err := s.pool.Get(name)
	if err != nil {
		return api.AddressList{}, ctxerrors.Wrap(err, "wallet addresses")
	}

	addresses, err := wallet.Client.GetAddresses(ctx, account)
	if err != nil {
		return api.AddressList{}, mapWalletError(err, "wallet addresses")
	}

	return addressesToAPI(account, addresses), nil
}

// CreateAddress creates a subaddress.
func (s *Service) CreateAddress(
	ctx context.Context,
	name string,
	request api.CreateAddressRequest,
) (api.Address, error) {
	if err := s.requireWritable(); err != nil {
		return api.Address{}, err
	}

	account, err := accountIndexOf(request.AccountIndex)
	if err != nil {
		return api.Address{}, err
	}

	label := deref(request.Label)
	if len(label) > api.MaxLabelLength {
		return api.Address{}, invalid.Newf(
			"label is longer than %d characters",
			api.MaxLabelLength,
		)
	}

	wallet, err := s.pool.Get(name)
	if err != nil {
		return api.Address{}, ctxerrors.Wrap(err, "create address")
	}

	created, err := wallet.Client.CreateAddress(ctx, account, label)
	if err != nil {
		return api.Address{}, mapWalletError(err, "create address")
	}

	return api.Address{
		Address:      created.Address,
		AddressIndex: int64(created.AddressIndex),
		Label:        label,
		Used:         false,
	}, nil
}

// TransferQuery selects and pages transfer history.
type TransferQuery struct {
	AccountIndex *int64
	Directions   []api.TransferDirection
	Limit        *int
	Offset       *int
}

// Transfers lists one account's transfers, newest first.
func (s *Service) Transfers(
	ctx context.Context,
	name string,
	query TransferQuery,
) (api.TransferList, error) {
	account, err := accountIndexOf(query.AccountIndex)
	if err != nil {
		return api.TransferList{}, err
	}

	limit, offset, err := page(query.Limit, query.Offset)
	if err != nil {
		return api.TransferList{}, err
	}

	filter, err := transferFilter(query.Directions, account)
	if err != nil {
		return api.TransferList{}, err
	}

	wallet, err := s.pool.Get(name)
	if err != nil {
		return api.TransferList{}, ctxerrors.Wrap(err, "wallet transfers")
	}

	transfers, err := wallet.Client.GetTransfers(ctx, filter)
	if err != nil {
		return api.TransferList{}, mapWalletError(err, "wallet transfers")
	}

	entries := transfersToAPI(transfers)
	sort.SliceStable(entries, func(i, j int) bool {
		if !entries[i].Timestamp.Equal(entries[j].Timestamp) {
			return entries[i].Timestamp.After(entries[j].Timestamp)
		}

		return entries[i].Height > entries[j].Height
	})

	total := len(entries)
	start := min(offset, total)
	end := min(start+limit, total)

	return api.TransferList{
		Transfers: entries[start:end],
		Total:     total,
		Limit:     limit,
		Offset:    offset,
	}, nil
}

// Transfer sends funds. It refuses while the node or the wallet is not
// caught up, because a wallet that has not seen recent blocks can build a
// transaction from outputs it does not know are already spent.
func (s *Service) Transfer(
	ctx context.Context,
	name string,
	request api.TransferRequest,
) (api.TransferResult, error) {
	if err := s.requireWritable(); err != nil {
		return api.TransferResult{}, err
	}

	if !s.options.AllowTransfers {
		return api.TransferResult{}, ErrTransfersDisabled
	}

	built, err := buildTransfer(request)
	if err != nil {
		return api.TransferResult{}, err
	}

	wallet, err := s.pool.Get(name)
	if err != nil {
		return api.TransferResult{}, ctxerrors.Wrap(err, "transfer")
	}

	if err := s.requireSynced(ctx, wallet); err != nil {
		return api.TransferResult{}, err
	}

	result, err := wallet.Client.Transfer(ctx, built)
	if err != nil {
		return api.TransferResult{}, mapTransferError(err)
	}

	return transferResultToAPI(result, !built.DoNotRelay), nil
}

func (s *Service) requireSynced(
	ctx context.Context,
	wallet *walletpool.Wallet,
) error {
	info, err := s.daemon.GetInfo(ctx)
	if err != nil {
		return ctxerrors.Wrap(err, "check node sync")
	}

	if !info.Synchronized {
		return ErrNodeNotSynced
	}

	height, err := wallet.Client.GetHeight(ctx)
	if err != nil {
		return mapWalletError(err, "check wallet sync")
	}

	if height < info.Height {
		return ctxerrors.Wrapf(
			ErrWalletSyncing, "wallet at %d, node at %d", height, info.Height,
		)
	}

	return nil
}

// openedStatus is the status returned right after a wallet was created,
// restored, or opened. The wallet exists and is open at that point, so a
// failure to read its address or height must not turn the call into an
// error a client would retry and then hit "already exists" or "already
// open". It falls back to the minimal open status and logs the cause.
func (s *Service) openedStatus(
	ctx context.Context,
	wallet *walletpool.Wallet,
) api.WalletStatus {
	status, err := s.openStatus(ctx, wallet)
	if err == nil {
		return status
	}

	ctxscope.GetLogger(ctx).Warn(
		"wallet is open but its status could not be read",
		"wallet", wallet.Name,
		"err", err,
	)

	openedAt := wallet.OpenedAt

	return api.WalletStatus{Name: wallet.Name, Open: true, OpenedAt: &openedAt}
}

// openStatus builds the status of an open wallet. A node that cannot be
// reached leaves the node fields out rather than failing the call, so a
// caller can still see that the wallet itself is open.
func (s *Service) openStatus(
	ctx context.Context,
	wallet *walletpool.Wallet,
) (api.WalletStatus, error) {
	addresses, err := wallet.Client.GetAddresses(ctx, 0)
	if err != nil {
		return api.WalletStatus{}, mapWalletError(err, "read wallet address")
	}

	walletHeight, err := wallet.Client.GetHeight(ctx)
	if err != nil {
		return api.WalletStatus{}, mapWalletError(err, "read wallet height")
	}

	openedAt := wallet.OpenedAt
	status := api.WalletStatus{
		Name:         wallet.Name,
		Open:         true,
		Address:      &addresses.Address,
		OpenedAt:     &openedAt,
		WalletHeight: toInt64Ptr(walletHeight),
	}

	info, err := s.daemon.GetInfo(ctx)
	if err != nil {
		return status, nil //nolint:nilerr // optional node fields
	}

	applySync(&status, walletHeight, info)

	return status, nil
}

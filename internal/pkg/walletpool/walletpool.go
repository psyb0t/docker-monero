// Package walletpool runs one monero-wallet-rpc process per open wallet.
//
// monero-wallet-rpc holds a single wallet at a time and serves requests on a
// single thread, so concurrency comes from running several processes. The
// pool owns their lifecycle: it allocates a loopback port, starts the
// process, waits for its RPC, creates or opens the wallet, and on close saves
// the wallet and stops the process. A process that dies on its own is
// dropped from the pool and its wallet reads as closed.
package walletpool

import (
	"context"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/docker-monero/internal/pkg/config"
	"github.com/psyb0t/docker-monero/internal/pkg/monero/walletrpc"
	"github.com/psyb0t/docker-monero/internal/pkg/process"
)

// Filesystem layout of a wallet: <dir>/<name> holds the cache and
// <dir>/<name>.keys the encrypted keys. The keys file is what makes a
// wallet exist.
const (
	keysSuffix    = ".keys"
	walletDirMode = 0o700
)

// Timing defaults.
const (
	DefaultStopTimeout       = 15 * time.Second
	DefaultAutoRefreshPeriod = 10 * time.Second
	readyPollInterval        = 100 * time.Millisecond
	readyProbeTimeout        = 2 * time.Second
	closeCallTimeout         = 30 * time.Second
)

// Log keys.
const (
	scopeKeyWallet = "wallet"
	logKeyPort     = "port"
	logKeyPID      = "pid"
	outputStream   = "monero-wallet-rpc"
)

// namePattern is the shape of a wallet name. It is also the file name, so it
// excludes path separators and leading dots.
var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// ValidateName rejects a wallet name that is not safe as a file name.
func ValidateName(name string) error {
	if !namePattern.MatchString(name) {
		return ctxerrors.Wrapf(ErrInvalidName, "%q", name)
	}

	return nil
}

// Settings configures the pool.
type Settings struct {
	WalletRPCPath     string
	WalletDir         string
	DaemonAddress     string
	RPCLogin          string
	Network           config.Network
	PortStart         int
	MaxOpen           int
	StartTimeout      time.Duration
	RPCTimeout        time.Duration
	StopTimeout       time.Duration
	AutoRefreshPeriod time.Duration
}

// Wallet is one open wallet and the process serving it.
type Wallet struct {
	Name     string
	Port     int
	OpenedAt time.Time
	Client   *walletrpc.Client

	proc   process.Process
	exited chan struct{}
}

// PID is the serving process' ID.
func (w *Wallet) PID() int {
	return w.proc.PID()
}

// Pool manages the wallet-rpc processes.
type Pool struct {
	settings Settings
	launcher process.Launcher

	mu        sync.Mutex
	wallets   map[string]*Wallet
	reserving map[string]bool
	ports     map[int]bool
	closed    bool
}

// New creates the wallet directory if needed and returns an empty pool.
func New(settings Settings, launcher process.Launcher) (*Pool, error) {
	if settings.StopTimeout <= 0 {
		settings.StopTimeout = DefaultStopTimeout
	}

	if settings.AutoRefreshPeriod <= 0 {
		settings.AutoRefreshPeriod = DefaultAutoRefreshPeriod
	}

	if err := os.MkdirAll(settings.WalletDir, walletDirMode); err != nil {
		return nil, ctxerrors.Wrapf(
			err, "create wallet directory %s", settings.WalletDir,
		)
	}

	return &Pool{
		settings:  settings,
		launcher:  launcher,
		wallets:   map[string]*Wallet{},
		reserving: map[string]bool{},
		ports:     map[int]bool{},
	}, nil
}

// MaxOpen is the configured open-wallet ceiling.
func (p *Pool) MaxOpen() int {
	return p.settings.MaxOpen
}

// Entry describes one wallet known to the pool or present on disk.
type Entry struct {
	Name string
	Open bool
}

// List returns every wallet file in the wallet directory plus every open
// wallet, sorted by name.
func (p *Pool) List() ([]Entry, error) {
	files, err := os.ReadDir(p.settings.WalletDir)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read wallet directory")
	}

	names := map[string]bool{}

	for _, file := range files {
		name, isKeys := strings.CutSuffix(file.Name(), keysSuffix)
		if !isKeys || file.IsDir() || ValidateName(name) != nil {
			continue
		}

		names[name] = false
	}

	p.mu.Lock()

	for name := range p.wallets {
		names[name] = true
	}

	p.mu.Unlock()

	entries := make([]Entry, 0, len(names))
	for name, open := range names {
		entries = append(entries, Entry{Name: name, Open: open})
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})

	return entries, nil
}

// OpenCount is the number of wallets currently open.
func (p *Pool) OpenCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return len(p.wallets)
}

// Get returns an open wallet.
func (p *Pool) Get(name string) (*Wallet, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	wallet, ok := p.wallets[name]
	if !ok {
		if p.reserving[name] {
			return nil, ctxerrors.Wrap(ErrWalletBusy, name)
		}

		return nil, ctxerrors.Wrap(ErrWalletNotOpen, name)
	}

	return wallet, nil
}

// Exists reports whether a wallet's keys file is on disk.
func (p *Pool) Exists(name string) (bool, error) {
	if err := ValidateName(name); err != nil {
		return false, err
	}

	_, err := os.Stat(p.keysPath(name))
	if err == nil {
		return true, nil
	}

	if os.IsNotExist(err) {
		return false, nil
	}

	return false, ctxerrors.Wrapf(err, "stat wallet %s", name)
}

// Create creates a new wallet and leaves it open.
func (p *Pool) Create(
	ctx context.Context,
	name, password, language string,
) (*Wallet, error) {
	if err := p.requireAbsent(name); err != nil {
		return nil, err
	}

	return p.start(
		ctx,
		name,
		func(ctx context.Context, client *walletrpc.Client) error {
			err := client.CreateWallet(ctx, name, password, language)
			if err == nil {
				return nil
			}

			if walletrpc.Code(err) == walletrpc.CodeWalletAlreadyExists {
				return ctxerrors.Wrap(ErrWalletExists, name)
			}

			return ctxerrors.Wrap(err, "create wallet file")
		},
	)
}

// RestoreFromSeed restores a wallet from its mnemonic and leaves it open.
func (p *Pool) RestoreFromSeed(
	ctx context.Context,
	request walletrpc.RestoreFromSeedRequest,
) (*Wallet, error) {
	if err := p.requireAbsent(request.Filename); err != nil {
		return nil, err
	}

	return p.start(
		ctx,
		request.Filename,
		func(ctx context.Context, client *walletrpc.Client) error {
			if _, err := client.RestoreFromSeed(ctx, request); err != nil {
				return ctxerrors.Wrap(err, "restore wallet file from seed")
			}

			return nil
		},
	)
}

// RestoreFromKeys restores a wallet from its keys and leaves it open.
func (p *Pool) RestoreFromKeys(
	ctx context.Context,
	request walletrpc.GenerateFromKeysRequest,
) (*Wallet, error) {
	if err := p.requireAbsent(request.Filename); err != nil {
		return nil, err
	}

	return p.start(
		ctx,
		request.Filename,
		func(ctx context.Context, client *walletrpc.Client) error {
			if _, err := client.GenerateFromKeys(ctx, request); err != nil {
				return ctxerrors.Wrap(err, "restore wallet file from keys")
			}

			return nil
		},
	)
}

// Open opens an existing wallet.
func (p *Pool) Open(
	ctx context.Context,
	name, password string,
) (*Wallet, error) {
	exists, err := p.Exists(name)
	if err != nil {
		return nil, err
	}

	if !exists {
		return nil, ctxerrors.Wrap(ErrWalletNotFound, name)
	}

	return p.start(
		ctx,
		name,
		func(ctx context.Context, client *walletrpc.Client) error {
			err := client.OpenWallet(ctx, name, password)
			if err == nil {
				return nil
			}

			if walletrpc.IsInvalidPassword(err) {
				return ctxerrors.Wrap(ErrInvalidPassword, name)
			}

			return ctxerrors.Wrap(err, "open wallet file")
		},
	)
}

// Close saves the wallet and stops its process.
func (p *Pool) Close(ctx context.Context, name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}

	p.mu.Lock()

	wallet, ok := p.wallets[name]
	if !ok {
		busy := p.reserving[name]
		p.mu.Unlock()

		if busy {
			return ctxerrors.Wrap(ErrWalletBusy, name)
		}

		return ctxerrors.Wrap(ErrWalletNotOpen, name)
	}

	delete(p.wallets, name)
	p.reserving[name] = true
	p.mu.Unlock()

	defer p.release(name, wallet.Port)

	return p.shutdown(ctx, wallet)
}

// CloseAll refuses new wallets and closes every open one. It is the
// shutdown path, so it keeps going past individual failures.
func (p *Pool) CloseAll(ctx context.Context) {
	p.mu.Lock()
	p.closed = true

	names := make([]string, 0, len(p.wallets))
	for name := range p.wallets {
		names = append(names, name)
	}

	p.mu.Unlock()

	logger := ctxscope.GetLogger(ctx)

	for _, name := range names {
		if err := p.Close(ctx, name); err != nil {
			logger.Error(
				"close wallet on shutdown",
				scopeKeyWallet,
				name,
				"err",
				err,
			)
		}
	}
}

// shutdown saves the wallet, then stops its process whether or not the save
// worked. A failed save is still reported.
func (p *Pool) shutdown(ctx context.Context, wallet *Wallet) error {
	logger := ctxscope.GetLogger(ctx).With(scopeKeyWallet, wallet.Name)

	closeCtx, cancelClose := context.WithTimeout(
		context.WithoutCancel(ctx), closeCallTimeout,
	)
	defer cancelClose()

	closeErr := wallet.Client.CloseWallet(closeCtx)
	if closeErr != nil {
		logger.Error("save and close wallet", "err", closeErr)
	}

	stopCtx, cancelStop := context.WithTimeout(
		context.WithoutCancel(ctx), p.settings.StopTimeout,
	)
	defer cancelStop()

	if err := wallet.proc.Stop(stopCtx); err != nil {
		logger.Error("stop wallet rpc", "err", err)
	}

	logger.Info("wallet closed")

	if closeErr != nil {
		return ctxerrors.Wrapf(closeErr, "close wallet %s", wallet.Name)
	}

	return nil
}

func (p *Pool) requireAbsent(name string) error {
	exists, err := p.Exists(name)
	if err != nil {
		return err
	}

	if exists {
		return ctxerrors.Wrap(ErrWalletExists, name)
	}

	return nil
}

// start runs the shared open sequence: reserve a name and port, launch a
// process, wait for its RPC, run setup (create, open, or restore), then
// publish the wallet. Any failure unwinds every step taken.
func (p *Pool) start(
	ctx context.Context,
	name string,
	setup func(context.Context, *walletrpc.Client) error,
) (*Wallet, error) {
	port, err := p.reserve(name)
	if err != nil {
		return nil, err
	}

	logger := ctxscope.GetLogger(ctx).
		With(scopeKeyWallet, name, logKeyPort, port)

	daemonConfig, err := newDaemonConfig(p.settings.RPCLogin)
	if err != nil {
		p.release(name, port)

		return nil, err
	}

	defer daemonConfig.cleanup(ctx)

	child, err := p.launcher.Launch(
		ctx, p.settings.WalletRPCPath, daemonConfig.args(p.args(port)),
		logger.With("stream", outputStream),
	)
	if err != nil {
		p.release(name, port)

		return nil, ctxerrors.Wrap(err, "launch wallet rpc")
	}

	wallet := &Wallet{
		Name:     name,
		Port:     port,
		OpenedAt: time.Now().UTC(),
		Client:   walletrpc.New(rpcURL(port), p.settings.RPCTimeout),
		proc:     child,
		exited:   make(chan struct{}),
	}

	go p.watch(wallet, logger)

	if err := p.initialize(ctx, wallet, setup, daemonConfig); err != nil {
		p.abandon(ctx, wallet, logger)

		return nil, err
	}

	if err := p.publish(wallet); err != nil {
		// Shutdown began while this wallet was opening. Save and stop it
		// rather than leaving a process the pool no longer tracks.
		p.abandonWithSave(ctx, wallet)

		return nil, err
	}

	logger.Info("wallet open", logKeyPID, child.PID())

	return wallet, nil
}

func (p *Pool) initialize(
	ctx context.Context,
	wallet *Wallet,
	setup func(context.Context, *walletrpc.Client) error,
	daemonConfig *daemonConfig,
) error {
	if err := p.waitReady(ctx, wallet); err != nil {
		return err
	}

	if err := daemonConfig.remove(); err != nil {
		return err
	}

	if err := setup(ctx, wallet.Client); err != nil {
		return ctxerrors.Wrapf(err, "set up wallet %s", wallet.Name)
	}

	if err := wallet.Client.SetAutoRefresh(
		ctx, p.settings.AutoRefreshPeriod,
	); err != nil {
		return ctxerrors.Wrapf(err, "enable auto refresh for %s", wallet.Name)
	}

	return nil
}

// waitReady polls the new process until its RPC answers, it exits, or the
// start timeout passes.
func (p *Pool) waitReady(ctx context.Context, wallet *Wallet) error {
	deadline := time.NewTimer(p.settings.StartTimeout)
	defer deadline.Stop()

	ticker := time.NewTicker(readyPollInterval)
	defer ticker.Stop()

	for {
		probeCtx, cancel := context.WithTimeout(ctx, readyProbeTimeout)
		err := wallet.Client.Ping(probeCtx)

		cancel()

		if err == nil {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctxerrors.Wrap(ctx.Err(), "wait for wallet rpc")
		case <-wallet.exited:
			return ctxerrors.Wrap(ErrProcessExited, wallet.Name)
		case <-deadline.C:
			return ctxerrors.Wrap(ErrStartTimeout, wallet.Name)
		case <-ticker.C:
		}
	}
}

// watch drops a wallet whose process exits without being closed.
func (p *Pool) watch(wallet *Wallet, logger *slog.Logger) {
	err := wallet.proc.Wait()
	close(wallet.exited)

	p.mu.Lock()
	current, tracked := p.wallets[wallet.Name]
	unexpected := tracked && current == wallet

	if unexpected {
		delete(p.wallets, wallet.Name)
		delete(p.ports, wallet.Port)
	}

	p.mu.Unlock()

	if unexpected {
		logger.Error("wallet rpc exited unexpectedly", "err", err)
	}
}

// abandon stops a process whose wallet never finished opening.
func (p *Pool) abandon(
	ctx context.Context,
	wallet *Wallet,
	logger *slog.Logger,
) {
	stopCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx), p.settings.StopTimeout,
	)
	defer cancel()

	if err := wallet.proc.Stop(stopCtx); err != nil {
		logger.Error("stop wallet rpc after failed open", "err", err)
	}

	p.release(wallet.Name, wallet.Port)
}

// abandonWithSave closes a fully opened wallet the pool will not publish.
func (p *Pool) abandonWithSave(ctx context.Context, wallet *Wallet) {
	defer p.release(wallet.Name, wallet.Port)

	if err := p.shutdown(ctx, wallet); err != nil {
		ctxscope.GetLogger(ctx).Error(
			"close wallet opened during shutdown",
			scopeKeyWallet, wallet.Name, "err", err,
		)
	}
}

// reserve claims name and a free port, or explains why it cannot.
func (p *Pool) reserve(name string) (int, error) {
	if err := ValidateName(name); err != nil {
		return 0, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return 0, ErrPoolClosed
	}

	if _, open := p.wallets[name]; open {
		return 0, ctxerrors.Wrap(ErrWalletAlreadyOpen, name)
	}

	if p.reserving[name] {
		return 0, ctxerrors.Wrap(ErrWalletBusy, name)
	}

	if len(p.wallets)+len(p.reserving) >= p.settings.MaxOpen {
		return 0, ctxerrors.Wrapf(
			ErrTooManyOpen, "limit is %d", p.settings.MaxOpen,
		)
	}

	end := p.settings.PortStart + p.settings.MaxOpen

	for port := p.settings.PortStart; port < end; port++ {
		if p.ports[port] {
			continue
		}

		p.ports[port] = true
		p.reserving[name] = true

		return port, nil
	}

	// Unreachable while the port range is as wide as MaxOpen, but a full
	// range must still fail closed.
	return 0, ctxerrors.Wrap(ErrTooManyOpen, "no free wallet rpc port")
}

// publish moves a reserved wallet into the open set.
func (p *Pool) publish(wallet *Wallet) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return ErrPoolClosed
	}

	delete(p.reserving, wallet.Name)
	p.wallets[wallet.Name] = wallet

	return nil
}

func (p *Pool) release(name string, port int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	delete(p.reserving, name)
	delete(p.ports, port)
}

func (p *Pool) keysPath(name string) string {
	return filepath.Join(p.settings.WalletDir, name+keysSuffix)
}

// args builds one wallet-rpc command line. Login is disabled because the
// listener is bound to the container's loopback, which only this process
// and its children share.
func (p *Pool) args(port int) []string {
	args := []string{
		"--non-interactive",
		"--rpc-bind-ip", config.LoopbackHost,
		"--rpc-bind-port", strconv.Itoa(port),
		"--disable-rpc-login",
		"--rpc-ssl", "disabled",
		"--wallet-dir", p.settings.WalletDir,
		"--daemon-address", p.settings.DaemonAddress,
		"--daemon-ssl", "disabled",
		"--trusted-daemon",
		"--log-file", os.DevNull,
	}

	switch p.settings.Network {
	case config.NetworkStagenet:
		args = append(args, "--stagenet")
	case config.NetworkTestnet:
		args = append(args, "--testnet")
	case config.NetworkRegtest:
		// Regtest reports every hard fork at height 1, which a wallet built
		// for mainnet rules rejects without this flag.
		args = append(args, "--allow-mismatched-daemon-version")
	case config.NetworkMainnet:
	}

	return args
}

func rpcURL(port int) string {
	return "http://" + net.JoinHostPort(config.LoopbackHost, strconv.Itoa(port))
}

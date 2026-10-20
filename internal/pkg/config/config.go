// Package config parses and validates docker-monero's deployment
// configuration from DOCKER_MONERO_* environment variables.
//
// Everything is read once at startup. A bad value stops the process before
// monerod starts, rather than surfacing on the first request that needs it.
package config

import (
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/gonfiguration"
)

// Network is the Monero network monerod and every wallet run on.
type Network string

// Supported networks.
const (
	NetworkMainnet  Network = "mainnet"
	NetworkStagenet Network = "stagenet"
	NetworkTestnet  Network = "testnet"
	NetworkRegtest  Network = "regtest"
)

// IsValid reports whether n is a supported network.
func (n Network) IsValid() bool {
	switch n {
	case NetworkMainnet, NetworkStagenet, NetworkTestnet, NetworkRegtest:
		return true
	default:
		return false
	}
}

// Environment variable names. They are the deployment contract.
const (
	EnvHTTPListenAddress    = "DOCKER_MONERO_HTTP_LISTEN_ADDRESS"
	EnvMetricsListenAddress = "DOCKER_MONERO_METRICS_LISTEN_ADDRESS"
	// #nosec G101 -- an environment variable name, not a credential.
	EnvAPIToken             = "DOCKER_MONERO_API_TOKEN"
	EnvNetwork              = "DOCKER_MONERO_NETWORK"
	EnvMonerodPath          = "DOCKER_MONERO_MONEROD_PATH"
	EnvWalletRPCPath        = "DOCKER_MONERO_WALLET_RPC_PATH"
	EnvDataDir              = "DOCKER_MONERO_DATA_DIR"
	EnvWalletDir            = "DOCKER_MONERO_WALLET_DIR"
	EnvPrune                = "DOCKER_MONERO_PRUNE"
	EnvP2PPort              = "DOCKER_MONERO_P2P_PORT"
	EnvRPCPort              = "DOCKER_MONERO_RPC_PORT"
	EnvRPCLogin             = "DOCKER_MONERO_RPC_LOGIN"
	EnvRestrictedRPCEnabled = "DOCKER_MONERO_RESTRICTED_RPC_ENABLED"
	EnvRestrictedRPCPort    = "DOCKER_MONERO_RESTRICTED_RPC_PORT"
	EnvPublicNode           = "DOCKER_MONERO_PUBLIC_NODE"
	EnvMonerodExtraArgs     = "DOCKER_MONERO_MONEROD_EXTRA_ARGS"
	EnvMonerodStopTimeout   = "DOCKER_MONERO_MONEROD_STOP_TIMEOUT"
	EnvWalletRPCPortStart   = "DOCKER_MONERO_WALLET_RPC_PORT_START"
	EnvMaxOpenWallets       = "DOCKER_MONERO_MAX_OPEN_WALLETS"
	EnvWalletStartTimeout   = "DOCKER_MONERO_WALLET_START_TIMEOUT"
	EnvRPCTimeout           = "DOCKER_MONERO_RPC_TIMEOUT"
	EnvReadOnly             = "DOCKER_MONERO_READ_ONLY"
	EnvAllowTransfers       = "DOCKER_MONERO_ALLOW_TRANSFERS"
	EnvMCPAllowTransfers    = "DOCKER_MONERO_MCP_ALLOW_TRANSFERS"
	EnvMaxRequestBytes      = "DOCKER_MONERO_MAX_REQUEST_BYTES"
)

// Defaults. Listeners default to loopback; the image overrides them to bind
// inside the container. Monero ports are the upstream defaults.
const (
	DefaultHTTPListenAddress    = "127.0.0.1:8080"
	DefaultMetricsListenAddress = "127.0.0.1:9091"
	DefaultNetwork              = NetworkMainnet
	DefaultMonerodPath          = "/usr/local/bin/monerod"
	DefaultWalletRPCPath        = "/usr/local/bin/monero-wallet-rpc"
	DefaultDataDir              = "/data/monerod"
	DefaultWalletDir            = "/data/wallets"
	DefaultPrune                = true
	DefaultP2PPort              = 18080
	DefaultRPCPort              = 18081
	DefaultRPCLogin             = ""
	DefaultRestrictedRPCEnabled = true
	DefaultRestrictedRPCPort    = 18089
	DefaultPublicNode           = false
	DefaultMonerodStopTimeout   = 90 * time.Second
	DefaultWalletRPCPortStart   = 18100
	DefaultMaxOpenWallets       = 8
	DefaultWalletStartTimeout   = 60 * time.Second
	DefaultRPCTimeout           = 60 * time.Second
	DefaultReadOnly             = false
	DefaultAllowTransfers       = true
	DefaultMCPAllowTransfers    = false
	DefaultMaxRequestBytes      = 1 << 20
)

// Limits on values that would otherwise produce a broken deployment.
const (
	maxPort              = 65535
	maxOpenWallets       = 64
	minAPITokenLength    = 32
	minRPCPasswordLength = 16
)

// extraArgPattern is the conservative shape every extra monerod argument
// must match. It admits flags and simple values and rejects anything a
// shell would interpret, even though no shell is involved, so a value copied
// from a guide with quotes or substitutions fails loudly.
var extraArgPattern = regexp.MustCompile(`^[A-Za-z0-9_./:=,@+-]+$`)

// Config is the full deployment surface.
//
//nolint:lll // env names are the deployment contract
type Config struct {
	HTTPListenAddress    string `env:"DOCKER_MONERO_HTTP_LISTEN_ADDRESS"`
	MetricsListenAddress string `env:"DOCKER_MONERO_METRICS_LISTEN_ADDRESS"`
	APIToken             string `env:"DOCKER_MONERO_API_TOKEN"`

	Network       string `env:"DOCKER_MONERO_NETWORK"`
	MonerodPath   string `env:"DOCKER_MONERO_MONEROD_PATH"`
	WalletRPCPath string `env:"DOCKER_MONERO_WALLET_RPC_PATH"`
	DataDir       string `env:"DOCKER_MONERO_DATA_DIR"`
	WalletDir     string `env:"DOCKER_MONERO_WALLET_DIR"`

	Prune                bool          `env:"DOCKER_MONERO_PRUNE"`
	P2PPort              int           `env:"DOCKER_MONERO_P2P_PORT"`
	RPCPort              int           `env:"DOCKER_MONERO_RPC_PORT"`
	RPCLogin             string        `env:"DOCKER_MONERO_RPC_LOGIN"`
	RestrictedRPCEnabled bool          `env:"DOCKER_MONERO_RESTRICTED_RPC_ENABLED"`
	RestrictedRPCPort    int           `env:"DOCKER_MONERO_RESTRICTED_RPC_PORT"`
	PublicNode           bool          `env:"DOCKER_MONERO_PUBLIC_NODE"`
	MonerodExtraArgs     string        `env:"DOCKER_MONERO_MONEROD_EXTRA_ARGS"`
	MonerodStopTimeout   time.Duration `env:"DOCKER_MONERO_MONEROD_STOP_TIMEOUT"`

	WalletRPCPortStart int           `env:"DOCKER_MONERO_WALLET_RPC_PORT_START"`
	MaxOpenWallets     int           `env:"DOCKER_MONERO_MAX_OPEN_WALLETS"`
	WalletStartTimeout time.Duration `env:"DOCKER_MONERO_WALLET_START_TIMEOUT"`
	RPCTimeout         time.Duration `env:"DOCKER_MONERO_RPC_TIMEOUT"`

	// ReadOnly refuses every write: creating or restoring wallets, new
	// subaddresses, transfers, and regtest mining. Opening and closing
	// stay allowed because reading a wallet requires it to be open.
	ReadOnly          bool `env:"DOCKER_MONERO_READ_ONLY"`
	AllowTransfers    bool `env:"DOCKER_MONERO_ALLOW_TRANSFERS"`
	MCPAllowTransfers bool `env:"DOCKER_MONERO_MCP_ALLOW_TRANSFERS"`

	MaxRequestBytes int `env:"DOCKER_MONERO_MAX_REQUEST_BYTES"`
}

// Parse reads the environment, applies defaults, and validates.
func Parse() (Config, error) {
	setDefaults()

	config := Config{}
	if err := gonfiguration.Parse(&config); err != nil {
		return Config{}, ctxerrors.Wrap(
			err,
			"parse docker-monero configuration",
		)
	}

	if err := config.Validate(); err != nil {
		return Config{}, err
	}

	return config, nil
}

func setDefaults() {
	gonfiguration.SetDefaults(map[string]any{
		EnvHTTPListenAddress:    DefaultHTTPListenAddress,
		EnvMetricsListenAddress: DefaultMetricsListenAddress,
		EnvAPIToken:             "",
		EnvNetwork:              string(DefaultNetwork),
		EnvMonerodPath:          DefaultMonerodPath,
		EnvWalletRPCPath:        DefaultWalletRPCPath,
		EnvDataDir:              DefaultDataDir,
		EnvWalletDir:            DefaultWalletDir,
		EnvPrune:                DefaultPrune,
		EnvP2PPort:              DefaultP2PPort,
		EnvRPCPort:              DefaultRPCPort,
		EnvRPCLogin:             DefaultRPCLogin,
		EnvRestrictedRPCEnabled: DefaultRestrictedRPCEnabled,
		EnvRestrictedRPCPort:    DefaultRestrictedRPCPort,
		EnvPublicNode:           DefaultPublicNode,
		EnvMonerodExtraArgs:     "",
		EnvMonerodStopTimeout:   DefaultMonerodStopTimeout,
		EnvWalletRPCPortStart:   DefaultWalletRPCPortStart,
		EnvMaxOpenWallets:       DefaultMaxOpenWallets,
		EnvWalletStartTimeout:   DefaultWalletStartTimeout,
		EnvRPCTimeout:           DefaultRPCTimeout,
		EnvReadOnly:             DefaultReadOnly,
		EnvAllowTransfers:       DefaultAllowTransfers,
		EnvMCPAllowTransfers:    DefaultMCPAllowTransfers,
		EnvMaxRequestBytes:      DefaultMaxRequestBytes,
	})
}

// Validate rejects a configuration that cannot produce a safe, working
// service.
func (c Config) Validate() error {
	if err := c.validateListeners(); err != nil {
		return err
	}

	if err := c.validateMonero(); err != nil {
		return err
	}

	if err := c.validatePorts(); err != nil {
		return err
	}

	return c.validateLimits()
}

// validateListeners requires the API token. The API can open wallets and
// send funds, so there is no unauthenticated mode.
func (c Config) validateListeners() error {
	if c.APIToken == "" {
		return ctxerrors.Wrap(commerr.ErrRequiredConfigValueNotSet, EnvAPIToken)
	}

	if len(c.APIToken) < minAPITokenLength {
		return ctxerrors.Wrapf(
			commerr.ErrInvalidValue,
			"%s must be at least %d characters", EnvAPIToken, minAPITokenLength,
		)
	}

	listeners := []setting[string]{
		{EnvHTTPListenAddress, c.HTTPListenAddress},
		{EnvMetricsListenAddress, c.MetricsListenAddress},
	}

	for _, listener := range listeners {
		if _, _, err := net.SplitHostPort(listener.value); err != nil {
			return ctxerrors.Wrapf(
				commerr.ErrInvalidValue,
				"%s %q is not host:port",
				listener.name,
				listener.value,
			)
		}
	}

	if c.HTTPListenAddress == c.MetricsListenAddress {
		return ctxerrors.Wrap(
			commerr.ErrInvalidValue,
			"the metrics listener must not share an address with the API",
		)
	}

	return nil
}

func (c Config) validateMonero() error {
	if !Network(c.Network).IsValid() {
		return ctxerrors.Wrapf(
			commerr.ErrInvalidValue,
			"%s must be one of mainnet, stagenet, testnet, regtest, got %q",
			EnvNetwork, c.Network,
		)
	}

	required := []setting[string]{
		{EnvMonerodPath, c.MonerodPath},
		{EnvWalletRPCPath, c.WalletRPCPath},
		{EnvDataDir, c.DataDir},
		{EnvWalletDir, c.WalletDir},
	}

	for _, entry := range required {
		if entry.value == "" {
			return ctxerrors.Wrap(
				commerr.ErrRequiredConfigValueNotSet, entry.name,
			)
		}
	}

	if _, err := c.MonerodExtraArgList(); err != nil {
		return err
	}

	return c.validateRPCLogin()
}

func (c Config) validateRPCLogin() error {
	if c.RPCLogin == "" {
		return nil
	}

	username, password, found := strings.Cut(c.RPCLogin, ":")
	if !found || username == "" ||
		utf8.RuneCountInString(password) < minRPCPasswordLength {
		return ctxerrors.Wrapf(
			commerr.ErrInvalidValue,
			"%s requires username:password, password minimum %d characters",
			EnvRPCLogin, minRPCPasswordLength,
		)
	}

	// The wallet config parser treats # as a comment and trims whitespace.
	if strings.ContainsRune(c.RPCLogin, '#') ||
		strings.ContainsFunc(c.RPCLogin, unicode.IsControl) ||
		strings.TrimSpace(c.RPCLogin) != c.RPCLogin {
		return ctxerrors.Wrapf(
			commerr.ErrInvalidValue,
			"%s disallows control characters, # and surrounding whitespace",
			EnvRPCLogin,
		)
	}

	return nil
}

// validatePorts checks the listener ports in a fixed order, so the same bad
// configuration always reports the same error. A disabled restricted RPC
// listener is not checked at all.
func (c Config) validatePorts() error {
	listeners := []setting[int]{
		{EnvP2PPort, c.P2PPort},
		{EnvRPCPort, c.RPCPort},
	}

	if c.RestrictedRPCEnabled {
		listeners = append(
			listeners, setting[int]{EnvRestrictedRPCPort, c.RestrictedRPCPort},
		)
	}

	all := listeners
	all = append(
		all, setting[int]{EnvWalletRPCPortStart, c.WalletRPCPortStart},
	)

	if err := c.validatePortRange(all); err != nil {
		return err
	}

	return c.validatePortClashes(listeners)
}

// validatePortRange checks every port is a real port and the wallet-rpc
// range fits below the ceiling.
func (c Config) validatePortRange(ports []setting[int]) error {
	for _, port := range ports {
		if port.value <= 0 || port.value > maxPort {
			return ctxerrors.Wrapf(
				commerr.ErrInvalidValue,
				"%s %d is not a valid port",
				port.name,
				port.value,
			)
		}
	}

	walletPortEnd := c.walletPortEnd()
	if walletPortEnd > maxPort {
		return ctxerrors.Wrapf(
			commerr.ErrInvalidValue,
			"wallet-rpc ports %d-%d run past %d",
			c.WalletRPCPortStart, walletPortEnd, maxPort,
		)
	}

	return nil
}

func (c Config) walletPortEnd() int {
	return c.WalletRPCPortStart + c.MaxOpenWallets - 1
}

// validatePortClashes rejects two listeners on one port and any listener
// inside the wallet-rpc range.
func (c Config) validatePortClashes(listeners []setting[int]) error {
	walletPortEnd := c.walletPortEnd()
	used := map[int]string{}

	for _, listener := range listeners {
		if previous, clash := used[listener.value]; clash {
			return ctxerrors.Wrapf(
				commerr.ErrInvalidValue, "%s and %s share port %d",
				previous, listener.name, listener.value,
			)
		}

		used[listener.value] = listener.name

		if listener.value >= c.WalletRPCPortStart &&
			listener.value <= walletPortEnd {
			return ctxerrors.Wrapf(
				commerr.ErrInvalidValue,
				"%s %d falls inside the wallet-rpc port range %d-%d",
				listener.name, listener.value,
				c.WalletRPCPortStart, walletPortEnd,
			)
		}
	}

	return nil
}

func (c Config) validateLimits() error {
	if c.MaxOpenWallets <= 0 || c.MaxOpenWallets > maxOpenWallets {
		return ctxerrors.Wrapf(
			commerr.ErrInvalidValue,
			"%s must be between 1 and %d", EnvMaxOpenWallets, maxOpenWallets,
		)
	}

	durations := []setting[time.Duration]{
		{EnvMonerodStopTimeout, c.MonerodStopTimeout},
		{EnvWalletStartTimeout, c.WalletStartTimeout},
		{EnvRPCTimeout, c.RPCTimeout},
	}

	for _, duration := range durations {
		if duration.value <= 0 {
			return ctxerrors.Wrapf(
				commerr.ErrInvalidValue, "%s must be positive", duration.name,
			)
		}
	}

	if c.MaxRequestBytes <= 0 {
		return ctxerrors.Wrapf(
			commerr.ErrInvalidValue, "%s must be positive", EnvMaxRequestBytes,
		)
	}

	return nil
}

// setting pairs an environment variable name with its parsed value, so
// checks run in a fixed order and report deterministically.
type setting[T any] struct {
	name  string
	value T
}

// NetworkType returns the configured network as its typed form.
func (c Config) NetworkType() Network {
	return Network(c.Network)
}

// MonerodExtraArgList splits DOCKER_MONERO_MONEROD_EXTRA_ARGS on whitespace
// and checks every argument against a conservative character set.
func (c Config) MonerodExtraArgList() ([]string, error) {
	args := strings.Fields(c.MonerodExtraArgs)

	for _, arg := range args {
		if !extraArgPattern.MatchString(arg) {
			return nil, ctxerrors.Wrapf(
				commerr.ErrInvalidValue,
				"%s contains an unsupported argument %q",
				EnvMonerodExtraArgs, arg,
			)
		}
	}

	return args, nil
}

// MonerodRPCURL is the unrestricted RPC listener, reachable only inside the
// container.
func (c Config) MonerodRPCURL() string {
	return "http://" + c.MonerodRPCAddress()
}

// MonerodRPCAddress is host:port of the unrestricted RPC listener.
func (c Config) MonerodRPCAddress() string {
	return net.JoinHostPort(LoopbackHost, strconv.Itoa(c.RPCPort))
}

// LoopbackHost is where every internal RPC listener binds.
const LoopbackHost = "127.0.0.1"

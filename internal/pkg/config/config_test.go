package config

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testToken is the shortest API token Validate accepts.
const testToken = "test-token-test-token-test-token"

// allEnvNames is every variable Parse reads.
var allEnvNames = []string{
	EnvHTTPListenAddress,
	EnvMetricsListenAddress,
	EnvAPIToken,
	EnvNetwork,
	EnvMonerodPath,
	EnvWalletRPCPath,
	EnvDataDir,
	EnvWalletDir,
	EnvPrune,
	EnvP2PPort,
	EnvRPCPort,
	EnvRPCLogin,
	EnvRestrictedRPCEnabled,
	EnvRestrictedRPCPort,
	EnvPublicNode,
	EnvMonerodExtraArgs,
	EnvMonerodStopTimeout,
	EnvWalletRPCPortStart,
	EnvMaxOpenWallets,
	EnvWalletStartTimeout,
	EnvRPCTimeout,
	EnvReadOnly,
	EnvAllowTransfers,
	EnvMCPAllowTransfers,
	EnvMaxRequestBytes,
}

// cleanEnv removes every variable Parse reads for the length of the test, so
// the host environment cannot leak into a result. The caller must not run in
// parallel.
func cleanEnv(t *testing.T) {
	t.Helper()

	for _, name := range allEnvNames {
		t.Setenv(name, "")
		require.NoError(t, os.Unsetenv(name))
	}
}

// defaultConfig is what Parse returns when only the API token is set.
func defaultConfig() Config {
	return Config{
		HTTPListenAddress:    DefaultHTTPListenAddress,
		MetricsListenAddress: DefaultMetricsListenAddress,
		APIToken:             testToken,
		Network:              string(DefaultNetwork),
		MonerodPath:          DefaultMonerodPath,
		WalletRPCPath:        DefaultWalletRPCPath,
		DataDir:              DefaultDataDir,
		WalletDir:            DefaultWalletDir,
		Prune:                DefaultPrune,
		P2PPort:              DefaultP2PPort,
		RPCPort:              DefaultRPCPort,
		RPCLogin:             DefaultRPCLogin,
		RestrictedRPCEnabled: DefaultRestrictedRPCEnabled,
		RestrictedRPCPort:    DefaultRestrictedRPCPort,
		PublicNode:           DefaultPublicNode,
		MonerodStopTimeout:   DefaultMonerodStopTimeout,
		WalletRPCPortStart:   DefaultWalletRPCPortStart,
		MaxOpenWallets:       DefaultMaxOpenWallets,
		WalletStartTimeout:   DefaultWalletStartTimeout,
		RPCTimeout:           DefaultRPCTimeout,
		ReadOnly:             DefaultReadOnly,
		AllowTransfers:       DefaultAllowTransfers,
		MCPAllowTransfers:    DefaultMCPAllowTransfers,
		MaxRequestBytes:      DefaultMaxRequestBytes,
	}
}

func TestNetworkIsValid(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		network Network
		want    bool
	}{
		{network: NetworkMainnet, want: true},
		{network: NetworkStagenet, want: true},
		{network: NetworkTestnet, want: true},
		{network: NetworkRegtest, want: true},
		{network: "", want: false},
		{network: "MAINNET", want: false},
		{network: "devnet", want: false},
		{network: " mainnet", want: false},
	}

	for _, tc := range testCases {
		t.Run(string(tc.network), func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, tc.network.IsValid())
		})
	}
}

func TestNetworkTypeReturnsTypedNetwork(t *testing.T) {
	t.Parallel()

	cfg := Config{Network: "regtest"}

	assert.Equal(t, NetworkRegtest, cfg.NetworkType())
	assert.Equal(t, Network("bogus"), Config{Network: "bogus"}.NetworkType())
}

func TestValidateAcceptsDefaults(t *testing.T) {
	t.Parallel()

	require.NoError(t, defaultConfig().Validate())
}

func TestValidateRejects(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		mutate  func(c *Config)
		wantErr error
		wantIn  string
	}{
		{
			name:    "missing api token",
			mutate:  func(c *Config) { c.APIToken = "" },
			wantErr: commerr.ErrRequiredConfigValueNotSet,
			wantIn:  EnvAPIToken,
		},
		{
			name:    "api token one character short",
			mutate:  func(c *Config) { c.APIToken = testToken[1:] },
			wantErr: commerr.ErrInvalidValue,
			wantIn:  EnvAPIToken,
		},
		{
			name:    "http listener without a port",
			mutate:  func(c *Config) { c.HTTPListenAddress = "localhost" },
			wantErr: commerr.ErrInvalidValue,
			wantIn:  EnvHTTPListenAddress,
		},
		{
			name:    "http listener empty",
			mutate:  func(c *Config) { c.HTTPListenAddress = "" },
			wantErr: commerr.ErrInvalidValue,
			wantIn:  EnvHTTPListenAddress,
		},
		{
			name:    "metrics listener without a port",
			mutate:  func(c *Config) { c.MetricsListenAddress = "127.0.0.1" },
			wantErr: commerr.ErrInvalidValue,
			wantIn:  EnvMetricsListenAddress,
		},
		{
			name: "metrics listener shares the api address",
			mutate: func(c *Config) {
				c.MetricsListenAddress = c.HTTPListenAddress
			},
			wantErr: commerr.ErrInvalidValue,
			wantIn:  "must not share an address",
		},
		{
			name:    "unknown network",
			mutate:  func(c *Config) { c.Network = "devnet" },
			wantErr: commerr.ErrInvalidValue,
			wantIn:  EnvNetwork,
		},
		{
			name:    "empty network",
			mutate:  func(c *Config) { c.Network = "" },
			wantErr: commerr.ErrInvalidValue,
			wantIn:  EnvNetwork,
		},
		{
			name:    "missing monerod path",
			mutate:  func(c *Config) { c.MonerodPath = "" },
			wantErr: commerr.ErrRequiredConfigValueNotSet,
			wantIn:  EnvMonerodPath,
		},
		{
			name:    "missing wallet-rpc path",
			mutate:  func(c *Config) { c.WalletRPCPath = "" },
			wantErr: commerr.ErrRequiredConfigValueNotSet,
			wantIn:  EnvWalletRPCPath,
		},
		{
			name:    "missing data dir",
			mutate:  func(c *Config) { c.DataDir = "" },
			wantErr: commerr.ErrRequiredConfigValueNotSet,
			wantIn:  EnvDataDir,
		},
		{
			name:    "missing wallet dir",
			mutate:  func(c *Config) { c.WalletDir = "" },
			wantErr: commerr.ErrRequiredConfigValueNotSet,
			wantIn:  EnvWalletDir,
		},
		{
			name: "extra argument with shell syntax",
			mutate: func(c *Config) {
				c.MonerodExtraArgs = "--prune-blockchain $(reboot)"
			},
			wantErr: commerr.ErrInvalidValue,
			wantIn:  EnvMonerodExtraArgs,
		},
		{
			name:    "max open wallets zero",
			mutate:  func(c *Config) { c.MaxOpenWallets = 0 },
			wantErr: commerr.ErrInvalidValue,
			wantIn:  EnvMaxOpenWallets,
		},
		{
			name:    "max open wallets negative",
			mutate:  func(c *Config) { c.MaxOpenWallets = -1 },
			wantErr: commerr.ErrInvalidValue,
			wantIn:  EnvMaxOpenWallets,
		},
		{
			name:    "max open wallets above the cap",
			mutate:  func(c *Config) { c.MaxOpenWallets = maxOpenWallets + 1 },
			wantErr: commerr.ErrInvalidValue,
			wantIn:  EnvMaxOpenWallets,
		},
		{
			name:    "monerod stop timeout zero",
			mutate:  func(c *Config) { c.MonerodStopTimeout = 0 },
			wantErr: commerr.ErrInvalidValue,
			wantIn:  EnvMonerodStopTimeout,
		},
		{
			name: "wallet start timeout negative",
			mutate: func(c *Config) {
				c.WalletStartTimeout = -time.Second
			},
			wantErr: commerr.ErrInvalidValue,
			wantIn:  EnvWalletStartTimeout,
		},
		{
			name:    "rpc timeout zero",
			mutate:  func(c *Config) { c.RPCTimeout = 0 },
			wantErr: commerr.ErrInvalidValue,
			wantIn:  EnvRPCTimeout,
		},
		{
			name:    "max request bytes zero",
			mutate:  func(c *Config) { c.MaxRequestBytes = 0 },
			wantErr: commerr.ErrInvalidValue,
			wantIn:  EnvMaxRequestBytes,
		},
		{
			name:    "max request bytes negative",
			mutate:  func(c *Config) { c.MaxRequestBytes = -1 },
			wantErr: commerr.ErrInvalidValue,
			wantIn:  EnvMaxRequestBytes,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := defaultConfig()
			tc.mutate(&cfg)

			err := cfg.Validate()
			require.ErrorIs(t, err, tc.wantErr)
			assert.ErrorContains(t, err, tc.wantIn)
		})
	}
}

func TestValidateAcceptsBoundaries(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name   string
		mutate func(c *Config)
	}{
		{
			name: "api token exactly the minimum length",
			mutate: func(c *Config) {
				c.APIToken = strings.Repeat("x", minAPITokenLength)
			},
		},
		{
			name: "api token far longer than the minimum",
			mutate: func(c *Config) {
				c.APIToken = strings.Repeat("x", 4*minAPITokenLength)
			},
		},
		{
			name:   "one wallet",
			mutate: func(c *Config) { c.MaxOpenWallets = 1 },
		},
		{
			name:   "wallet cap",
			mutate: func(c *Config) { c.MaxOpenWallets = maxOpenWallets },
		},
		{
			name: "wallet range ends on the last port",
			mutate: func(c *Config) {
				c.WalletRPCPortStart = maxPort
				c.MaxOpenWallets = 1
			},
		},
		{
			name:   "lowest port",
			mutate: func(c *Config) { c.P2PPort = 1 },
		},
		{
			name:   "highest port",
			mutate: func(c *Config) { c.P2PPort = maxPort },
		},
		{
			name: "listeners on wildcard addresses",
			mutate: func(c *Config) {
				c.HTTPListenAddress = "0.0.0.0:8080"
				c.MetricsListenAddress = "[::]:9091"
			},
		},
		{
			name: "extra arguments in the allowed character set",
			mutate: func(c *Config) {
				c.MonerodExtraArgs = "--add-peer=10.0.0.1:18080 --out-peers=8"
			},
		},
		{
			name:   "port just below the wallet range",
			mutate: func(c *Config) { c.P2PPort = c.WalletRPCPortStart - 1 },
		},
		{
			name:   "port just above the wallet range",
			mutate: func(c *Config) { c.P2PPort = c.walletPortEnd() + 1 },
		},
		{
			name: "restricted port inside the wallet range when disabled",
			mutate: func(c *Config) {
				c.RestrictedRPCEnabled = false
				c.RestrictedRPCPort = c.WalletRPCPortStart
			},
		},
		{
			name: "restricted port equal to the rpc port when disabled",
			mutate: func(c *Config) {
				c.RestrictedRPCEnabled = false
				c.RestrictedRPCPort = c.RPCPort
			},
		},
		{
			name: "restricted port out of range when disabled",
			mutate: func(c *Config) {
				c.RestrictedRPCEnabled = false
				c.RestrictedRPCPort = maxPort + 1
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := defaultConfig()
			tc.mutate(&cfg)

			require.NoError(t, cfg.Validate())
		})
	}
}

func TestValidateRejectsInvalidPorts(t *testing.T) {
	t.Parallel()

	setters := map[string]func(c *Config, port int){
		EnvP2PPort: func(c *Config, port int) {
			c.P2PPort = port
		},
		EnvRPCPort: func(c *Config, port int) {
			c.RPCPort = port
		},
		EnvRestrictedRPCPort: func(c *Config, port int) {
			c.RestrictedRPCPort = port
		},
		EnvWalletRPCPortStart: func(c *Config, port int) {
			c.WalletRPCPortStart = port
		},
	}

	for name, set := range setters {
		for _, port := range []int{-1, 0, maxPort + 1} {
			t.Run(name+"="+strconv.Itoa(port), func(t *testing.T) {
				t.Parallel()

				cfg := defaultConfig()
				set(&cfg, port)

				err := cfg.Validate()
				require.ErrorIs(t, err, commerr.ErrInvalidValue)
				assert.ErrorContains(t, err, name)
			})
		}
	}
}

func TestValidatePortsErrorOrder(t *testing.T) {
	t.Parallel()

	cfg := defaultConfig()
	cfg.P2PPort = 0
	cfg.RPCPort = 0
	cfg.RestrictedRPCPort = 0
	cfg.WalletRPCPortStart = 0

	for _, entry := range []struct {
		name string
		port *int
	}{
		{EnvP2PPort, &cfg.P2PPort},
		{EnvRPCPort, &cfg.RPCPort},
		{EnvRestrictedRPCPort, &cfg.RestrictedRPCPort},
		{EnvWalletRPCPortStart, &cfg.WalletRPCPortStart},
	} {
		err := cfg.Validate()
		require.ErrorIs(t, err, commerr.ErrInvalidValue)
		assert.ErrorContains(t, err, entry.name)
		*entry.port = maxPort
	}
}

func TestValidateRejectsWalletRangePastTheLastPort(t *testing.T) {
	t.Parallel()

	cfg := defaultConfig()
	cfg.WalletRPCPortStart = maxPort
	cfg.MaxOpenWallets = 2

	err := cfg.Validate()

	require.ErrorIs(t, err, commerr.ErrInvalidValue)
	assert.ErrorContains(t, err, "run past")
}

func TestValidateRejectsPortClashes(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name   string
		mutate func(c *Config)
		wantIn string
	}{
		{
			name:   "p2p and rpc share a port",
			mutate: func(c *Config) { c.P2PPort = c.RPCPort },
			wantIn: "share port",
		},
		{
			name:   "p2p and restricted rpc share a port",
			mutate: func(c *Config) { c.P2PPort = c.RestrictedRPCPort },
			wantIn: "share port",
		},
		{
			name:   "rpc and restricted rpc share a port",
			mutate: func(c *Config) { c.RPCPort = c.RestrictedRPCPort },
			wantIn: "share port",
		},
		{
			name:   "p2p at the start of the wallet range",
			mutate: func(c *Config) { c.P2PPort = c.WalletRPCPortStart },
			wantIn: EnvP2PPort,
		},
		{
			name:   "rpc at the end of the wallet range",
			mutate: func(c *Config) { c.RPCPort = c.walletPortEnd() },
			wantIn: EnvRPCPort,
		},
		{
			name: "restricted rpc inside the wallet range",
			mutate: func(c *Config) {
				c.RestrictedRPCPort = c.WalletRPCPortStart + 1
			},
			wantIn: EnvRestrictedRPCPort,
		},
		{
			name: "wallet range grown over the p2p port",
			mutate: func(c *Config) {
				c.WalletRPCPortStart = c.P2PPort - 1
				c.MaxOpenWallets = 2
			},
			wantIn: EnvP2PPort,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := defaultConfig()
			tc.mutate(&cfg)

			err := cfg.Validate()
			require.ErrorIs(t, err, commerr.ErrInvalidValue)
			assert.ErrorContains(t, err, tc.wantIn)
		})
	}
}

func TestValidateChecksListenersBeforeMonero(t *testing.T) {
	t.Parallel()

	cfg := defaultConfig()
	cfg.APIToken = ""
	cfg.Network = "devnet"

	err := cfg.Validate()

	require.ErrorIs(t, err, commerr.ErrRequiredConfigValueNotSet)
	assert.ErrorContains(t, err, EnvAPIToken)
}

func TestMonerodExtraArgList(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name  string
		input string
		want  []string
	}{
		{name: "empty", input: "", want: []string{}},
		{name: "only whitespace", input: " \t\n ", want: []string{}},
		{
			name:  "one flag",
			input: "--prune-blockchain",
			want:  []string{"--prune-blockchain"},
		},
		{
			name:  "split on spaces, tabs and newlines",
			input: "  --a=1\t--b=2\n--c  ",
			want:  []string{"--a=1", "--b=2", "--c"},
		},
		{
			name:  "full allowed alphabet",
			input: "--peer=a-B_9.0/x:1,2@y+z",
			want:  []string{"--peer=a-B_9.0/x:1,2@y+z"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := Config{MonerodExtraArgs: tc.input}.MonerodExtraArgList()

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestMonerodExtraArgListRejectsUnsafeArguments(t *testing.T) {
	t.Parallel()

	for _, bad := range []string{
		`--flag="quoted"`,
		"--flag='quoted'",
		"--flag=$(id)",
		"--flag=`id`",
		"--flag;reboot",
		"--flag|cat",
		"--flag&bg",
		"--flag=a*",
		"--flag>out",
		"--flag=\\n",
		"--flag=é",
		"--flag=a\x00b",
	} {
		t.Run(bad, func(t *testing.T) {
			t.Parallel()

			args, err := Config{MonerodExtraArgs: "--ok " + bad}.
				MonerodExtraArgList()

			require.ErrorIs(t, err, commerr.ErrInvalidValue)
			assert.ErrorContains(t, err, EnvMonerodExtraArgs)
			assert.Nil(t, args)
		})
	}
}

func TestMonerodExtraArgListNamesTheOffendingArgument(t *testing.T) {
	t.Parallel()

	_, err := Config{MonerodExtraArgs: "--fine --bad;arg --also-fine"}.
		MonerodExtraArgList()

	require.Error(t, err)
	assert.ErrorContains(t, err, `"--bad;arg"`)
}

func TestMonerodRPCEndpoint(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		port        int
		wantAddress string
		wantURL     string
	}{
		{
			name:        "default port",
			port:        DefaultRPCPort,
			wantAddress: "127.0.0.1:18081",
			wantURL:     "http://127.0.0.1:18081",
		},
		{
			name:        "custom port",
			port:        28081,
			wantAddress: "127.0.0.1:28081",
			wantURL:     "http://127.0.0.1:28081",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := Config{RPCPort: tc.port}

			assert.Equal(t, tc.wantAddress, cfg.MonerodRPCAddress())
			assert.Equal(t, tc.wantURL, cfg.MonerodRPCURL())
		})
	}
}

func TestLoopbackHost(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "127.0.0.1", LoopbackHost)
}

func TestParseAppliesDefaults(t *testing.T) {
	cleanEnv(t)
	t.Setenv(EnvAPIToken, testToken)

	got, err := Parse()

	require.NoError(t, err)
	assert.Equal(t, defaultConfig(), got)
}

func TestParseDefaultsAreSafe(t *testing.T) {
	cleanEnv(t)
	t.Setenv(EnvAPIToken, testToken)

	got, err := Parse()
	require.NoError(t, err)

	assert.Equal(t, "127.0.0.1:8080", got.HTTPListenAddress)
	assert.Equal(t, "127.0.0.1:9091", got.MetricsListenAddress)
	assert.Equal(t, "mainnet", got.Network)
	assert.False(t, got.ReadOnly)
	assert.True(t, got.AllowTransfers)
	assert.False(t, got.MCPAllowTransfers)
	assert.False(t, got.PublicNode)
	assert.True(t, got.RestrictedRPCEnabled)
	assert.True(t, got.Prune)
}

func TestParseReadsEveryVariable(t *testing.T) {
	cleanEnv(t)

	env := map[string]string{
		EnvHTTPListenAddress:    "0.0.0.0:8181",
		EnvMetricsListenAddress: "0.0.0.0:9192",
		EnvAPIToken:             strings.Repeat("k", 40),
		EnvNetwork:              "regtest",
		EnvMonerodPath:          "/opt/monerod",
		EnvWalletRPCPath:        "/opt/monero-wallet-rpc",
		EnvDataDir:              "/srv/chain",
		EnvWalletDir:            "/srv/wallets",
		EnvPrune:                "false",
		EnvP2PPort:              "28080",
		EnvRPCPort:              "28081",
		EnvRPCLogin:             "example:EXAMPLE-DO-NOT-USE",
		EnvRestrictedRPCEnabled: "false",
		EnvRestrictedRPCPort:    "28089",
		EnvPublicNode:           "true",
		EnvMonerodExtraArgs:     "--prune-blockchain --db-sync-mode=safe",
		EnvMonerodStopTimeout:   "2m",
		EnvWalletRPCPortStart:   "28100",
		EnvMaxOpenWallets:       "4",
		EnvWalletStartTimeout:   "30s",
		EnvRPCTimeout:           "15s",
		EnvReadOnly:             "true",
		EnvAllowTransfers:       "false",
		EnvMCPAllowTransfers:    "true",
		EnvMaxRequestBytes:      "2048",
	}
	for name, value := range env {
		t.Setenv(name, value)
	}

	got, err := Parse()
	require.NoError(t, err)

	assert.Equal(t, Config{
		HTTPListenAddress:    "0.0.0.0:8181",
		MetricsListenAddress: "0.0.0.0:9192",
		APIToken:             strings.Repeat("k", 40),
		Network:              "regtest",
		MonerodPath:          "/opt/monerod",
		WalletRPCPath:        "/opt/monero-wallet-rpc",
		DataDir:              "/srv/chain",
		WalletDir:            "/srv/wallets",
		Prune:                false,
		P2PPort:              28080,
		RPCPort:              28081,
		RPCLogin:             "example:EXAMPLE-DO-NOT-USE",
		RestrictedRPCEnabled: false,
		RestrictedRPCPort:    28089,
		PublicNode:           true,
		MonerodExtraArgs:     "--prune-blockchain --db-sync-mode=safe",
		MonerodStopTimeout:   2 * time.Minute,
		WalletRPCPortStart:   28100,
		MaxOpenWallets:       4,
		WalletStartTimeout:   30 * time.Second,
		RPCTimeout:           15 * time.Second,
		ReadOnly:             true,
		AllowTransfers:       false,
		MCPAllowTransfers:    true,
		MaxRequestBytes:      2048,
	}, got)

	args, err := got.MonerodExtraArgList()
	require.NoError(t, err)
	assert.Equal(t, []string{"--prune-blockchain", "--db-sync-mode=safe"}, args)
	assert.Equal(t, "127.0.0.1:28081", got.MonerodRPCAddress())
}

func TestParseAcceptsEveryNetwork(t *testing.T) {
	for _, network := range []Network{
		NetworkMainnet, NetworkStagenet, NetworkTestnet, NetworkRegtest,
	} {
		t.Run(string(network), func(t *testing.T) {
			cleanEnv(t)
			t.Setenv(EnvAPIToken, testToken)
			t.Setenv(EnvNetwork, string(network))

			got, err := Parse()

			require.NoError(t, err)
			assert.Equal(t, network, got.NetworkType())
		})
	}
}

func TestParseRejectsMalformedValues(t *testing.T) {
	testCases := []struct {
		name  string
		value string
		want  error
	}{
		{name: EnvP2PPort, value: "eighteen", want: strconv.ErrSyntax},
		{name: EnvMaxOpenWallets, value: "1.5", want: strconv.ErrSyntax},
		{name: EnvMaxRequestBytes, value: "1MiB", want: strconv.ErrSyntax},
		{name: EnvPrune, value: "maybe", want: strconv.ErrSyntax},
		{name: EnvReadOnly, value: "", want: strconv.ErrSyntax},
		{name: EnvRPCTimeout, value: "soon"},
		{name: EnvMonerodStopTimeout, value: "90"},
	}

	for _, tc := range testCases {
		t.Run(tc.name+"="+tc.value, func(t *testing.T) {
			cleanEnv(t)
			t.Setenv(EnvAPIToken, testToken)
			t.Setenv(tc.name, tc.value)

			got, err := Parse()

			require.Error(t, err)
			assert.Equal(t, Config{}, got)

			if tc.want != nil {
				assert.ErrorIs(t, err, tc.want)
			}
		})
	}
}

func TestParseRunsValidation(t *testing.T) {
	testCases := []struct {
		name    string
		env     map[string]string
		wantErr error
		wantIn  string
	}{
		{
			name:    "no api token",
			env:     map[string]string{},
			wantErr: commerr.ErrRequiredConfigValueNotSet,
			wantIn:  EnvAPIToken,
		},
		{
			name:    "short api token",
			env:     map[string]string{EnvAPIToken: "short"},
			wantErr: commerr.ErrInvalidValue,
			wantIn:  EnvAPIToken,
		},
		{
			name: "unknown network",
			env: map[string]string{
				EnvAPIToken: testToken,
				EnvNetwork:  "devnet",
			},
			wantErr: commerr.ErrInvalidValue,
			wantIn:  EnvNetwork,
		},
		{
			name: "port clash",
			env: map[string]string{
				EnvAPIToken: testToken,
				EnvRPCPort:  "18080",
			},
			wantErr: commerr.ErrInvalidValue,
			wantIn:  "share port",
		},
		{
			name: "empty monerod path",
			env: map[string]string{
				EnvAPIToken:    testToken,
				EnvMonerodPath: "",
			},
			wantErr: commerr.ErrRequiredConfigValueNotSet,
			wantIn:  EnvMonerodPath,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cleanEnv(t)

			for name, value := range tc.env {
				t.Setenv(name, value)
			}

			got, err := Parse()

			require.ErrorIs(t, err, tc.wantErr)
			assert.ErrorContains(t, err, tc.wantIn)
			assert.Equal(t, Config{}, got)
		})
	}
}

func TestValidateRPCLogin(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name  string
		login string
		valid bool
	}{
		{name: "empty", valid: true},
		{
			name:  "minimum password",
			login: "example:" + strings.Repeat("x", 16),
			valid: true,
		},
		{
			name:  "long password",
			login: "example:EXAMPLE-DO-NOT-USE", valid: true,
		},
		{
			name:  "colon in password",
			login: "example:EXAMPLE:DO-NOT-USE", valid: true,
		},
		{
			name:  "unicode minimum password",
			login: "example:" + strings.Repeat("\u00e9", 16),
			valid: true,
		},
		{
			name:  "unicode short password",
			login: "example:" + strings.Repeat("\u00e9", 15),
		},
		{name: "missing separator", login: "EXAMPLE-DO-NOT-USE"},
		{name: "missing username", login: ":EXAMPLE-DO-NOT-USE"},
		{name: "empty password", login: "example:"},
		{name: "short password", login: "example:" + strings.Repeat("x", 15)},
		{
			name:  "newline injection",
			login: "example:EXAMPLE-DO-NOT-USE\nlog-level=4",
		},
		{name: "carriage return", login: "example:EXAMPLE-DO-NOT-USE\r"},
		{name: "nul", login: "example:EXAMPLE-DO-NOT-USE\x00"},
		{name: "username control", login: "example\t:EXAMPLE-DO-NOT-USE"},
		{name: "password control", login: "example:EXAMPLE\x7fDO-NOT-USE"},
		{name: "comment", login: "example:EXAMPLE-DO-NOT-USE#"},
		{name: "leading whitespace", login: " example:EXAMPLE-DO-NOT-USE"},
		{name: "trailing whitespace", login: "example:EXAMPLE-DO-NOT-USE "},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := defaultConfig()
			cfg.RPCLogin = tc.login

			err := cfg.Validate()

			if tc.valid {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, commerr.ErrInvalidValue)
			assert.ErrorContains(t, err, EnvRPCLogin)
			assert.NotContains(t, err.Error(), tc.login)
		})
	}
}

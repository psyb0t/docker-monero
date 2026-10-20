package walletpool

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
)

const (
	daemonConfigDirPattern = "docker-monero-wallet-*"
	daemonConfigName       = "daemon.conf"
	daemonConfigMode       = 0o600
)

type daemonConfig struct {
	dir  string
	path string
}

func newDaemonConfig(login string) (*daemonConfig, error) {
	if login == "" {
		return nil, nil //nolint:nilnil // no login needs no temporary file.
	}

	dir, err := os.MkdirTemp("", daemonConfigDirPattern)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "create wallet daemon config directory")
	}

	config := &daemonConfig{
		dir: dir, path: filepath.Join(dir, daemonConfigName),
	}
	if err := os.WriteFile(
		config.path, []byte("daemon-login="+login+"\n"), daemonConfigMode,
	); err != nil {
		return nil, errors.Join(
			ctxerrors.Wrap(err, "write wallet daemon config"), config.remove(),
		)
	}

	return config, nil
}

func (c *daemonConfig) args(args []string) []string {
	if c == nil {
		return args
	}

	return append(args, "--config-file", c.path)
}

func (c *daemonConfig) cleanup(ctx context.Context) {
	if err := c.remove(); err != nil {
		ctxscope.GetLogger(ctx).Error("remove wallet daemon config", "err", err)
	}
}

func (c *daemonConfig) remove() error {
	if c == nil || c.dir == "" {
		return nil
	}

	// Monero parses this file into its variables map before serving RPC.
	// Later wallet creation and opening reuse that map, not the file.
	if err := os.RemoveAll(c.dir); err != nil {
		return ctxerrors.Wrap(err, "remove wallet daemon config")
	}

	c.dir = ""

	return nil
}

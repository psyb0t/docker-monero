package main

import (
	"context"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/docker-monero/internal/pkg/config"
	"github.com/psyb0t/docker-monero/internal/pkg/healthcheck"
	"github.com/psyb0t/gonfiguration"
	"github.com/spf13/cobra"
)

// healthcheckConfig reads only the listen address, so the probe works with
// the same environment as the service without needing the API token.
type healthcheckConfig struct {
	HTTPListenAddress string `env:"DOCKER_MONERO_HTTP_LISTEN_ADDRESS"`
}

func commands() []*cobra.Command {
	return []*cobra.Command{healthcheckCommand()}
}

func healthcheckCommand() *cobra.Command {
	return &cobra.Command{
		Use:          "healthcheck",
		Short:        "Exit 0 when the running service reports ready",
		SilenceUsage: true,
		RunE:         runHealthcheck,
	}
}

func runHealthcheck(cmd *cobra.Command, _ []string) error {
	gonfiguration.SetDefaults(map[string]any{
		config.EnvHTTPListenAddress: config.DefaultHTTPListenAddress,
	})

	cfg := healthcheckConfig{}
	if err := gonfiguration.Parse(&cfg); err != nil {
		return err //nolint:wrapcheck // cobra prints it
	}

	ctx, cancel := context.WithTimeout(
		cmd.Context(), healthcheck.DefaultTimeout,
	)
	defer cancel()

	if err := healthcheck.Check(ctx, cfg.HTTPListenAddress); err != nil {
		return ctxerrors.Wrap(err, "service is not ready")
	}

	return nil
}

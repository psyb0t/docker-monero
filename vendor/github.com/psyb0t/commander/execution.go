package commander

import (
	"context"
	"errors"
	"log/slog"
	"os/exec"

	commonerrors "github.com/psyb0t/common-go/errors"
	"github.com/psyb0t/ctxerrors"
)

type executionContext struct {
	ctx  context.Context //nolint:containedctx
	cmd  *exec.Cmd
	name string
	args []string
}

func (c *commander) newExecutionContext(
	ctx context.Context,
	name string,
	args []string,
	opts *Options,
) *executionContext {
	cmd := c.createCmd(ctx, name, args, opts)

	return &executionContext{
		ctx:  ctx,
		cmd:  cmd,
		name: name,
		args: args,
	}
}

func (ec *executionContext) handleExecutionError(err error) error {
	if err == nil {
		slog.Debug("command completed successfully", "name", ec.name, "args", ec.args)

		return nil
	}

	slog.Debug("command execution failed", "name", ec.name, "args", ec.args, "error", err)

	if errors.Is(err, context.DeadlineExceeded) {
		return commonerrors.ErrTimeout
	}

	if errors.Is(ec.ctx.Err(), context.DeadlineExceeded) &&
		isKilledBySignal(err) {
		return commonerrors.ErrTimeout
	}

	return ctxerrors.Wrap(err, "command failed")
}

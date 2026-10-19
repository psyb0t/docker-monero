// Package process starts the long-running monerod and monero-wallet-rpc
// children and forwards their output into the structured log.
//
// The Launcher interface is the seam the supervisor and the wallet pool are
// tested through. Production uses commander; tests substitute a fake that
// never forks.
package process

import (
	"context"
	"errors"
	"log/slog"

	"github.com/psyb0t/commander"
	commonerrors "github.com/psyb0t/common-go/errors"
	"github.com/psyb0t/ctxerrors"
)

// Process is a running child.
type Process interface {
	// Wait blocks until the child exits.
	Wait() error

	// Stop sends SIGTERM to the child's process group and escalates to
	// SIGKILL when ctx expires.
	Stop(ctx context.Context) error

	// PID is the child's process ID.
	PID() int
}

// Launcher starts a child process.
type Launcher interface {
	Launch(
		ctx context.Context,
		name string,
		args []string,
		output *slog.Logger,
	) (Process, error)
}

// streamBuffer is the per-stream line buffer. A child that writes faster
// than the logger drains stalls on its pipe rather than losing lines.
const streamBuffer = 256

// CommanderLauncher launches children through psyb0t/commander.
type CommanderLauncher struct {
	commander commander.Commander
}

// NewCommanderLauncher builds the production launcher.
func NewCommanderLauncher() *CommanderLauncher {
	return &CommanderLauncher{commander: commander.New()}
}

// Launch starts name with args and logs every output line through output,
// stdout at info and stderr at warn.
//
// The child is detached from ctx's cancellation on purpose. commander kills
// a child the moment its context is cancelled, and monerod must instead get
// SIGTERM and time to flush its database. Callers stop children explicitly
// with Process.Stop.
func (l *CommanderLauncher) Launch(
	ctx context.Context,
	name string,
	args []string,
	output *slog.Logger,
) (Process, error) {
	stdout := make(chan string, streamBuffer)
	stderr := make(chan string, streamBuffer)

	go forward(stdout, output.Info)
	go forward(stderr, output.Warn)

	child, err := l.commander.Start(
		context.WithoutCancel(ctx),
		name,
		args,
		commander.WithStream(stdout, stderr),
	)
	if err != nil {
		// commander closes stream channels only after a successful start,
		// so a failed start leaves them to us.
		close(stdout)
		close(stderr)

		return nil, ctxerrors.Wrapf(err, "start %s", name)
	}

	return &commanderProcess{Process: child}, nil
}

// commanderProcess normalizes commander's stop result. commander reports a
// child that exited on SIGTERM as ErrTerminated, which for a requested
// graceful stop is success. A SIGKILL after the deadline stays an error.
type commanderProcess struct {
	commander.Process
}

func (p *commanderProcess) Stop(ctx context.Context) error {
	err := p.Process.Stop(ctx)
	if err == nil || errors.Is(err, commonerrors.ErrTerminated) {
		return nil
	}

	return ctxerrors.Wrap(err, "stop child")
}

// forward drains one output stream until commander closes it.
func forward(lines <-chan string, write func(msg string, args ...any)) {
	for line := range lines {
		write(line)
	}
}

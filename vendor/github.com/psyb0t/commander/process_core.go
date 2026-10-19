package commander

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	commonerrors "github.com/psyb0t/common-go/errors"
	"github.com/psyb0t/ctxerrors"
)

// outputDrainTimeout bounds how long Wait waits for the output readers after
// the process exits. A grandchild that inherited the pipes can keep them open
// long after the process itself is gone.
const outputDrainTimeout = 2 * time.Second

type Process interface {
	Start() error
	Wait() error
	StdinPipe() (io.WriteCloser, error)
	// Starts streaming from the current moment, not from the beginning
	// Multiple streams can be active simultaneously (broadcast)
	// Pass nil for channels you don't want to listen to
	Stream(stdout, stderr chan<- string)
	Stop(ctx context.Context) error
	Kill(ctx context.Context) error
	PID() int
}

type streamChannels struct {
	stdout chan<- string
	stderr chan<- string
}

type process struct {
	cmd     *exec.Cmd
	execCtx *executionContext

	internalStdout chan string
	internalStderr chan string
	streamChans    []streamChannels
	streamMu       sync.Mutex
	doneCh         chan struct{}
	terminateOnce  sync.Once
	cmdWaitOnce    sync.Once
	cmdWaitResult  error
	waitCh         chan struct{}
	stderrBuffer   []string
	stderrMu       sync.Mutex
	outputReaders  []*os.File
	// outputDone closes once the readers and the forwarder have drained
	// both streams and the stream channels are closed.
	outputDone chan struct{}
}

// cmdWait ensures cmd.Wait() is only called once, even from multiple goroutines
func (p *process) cmdWait() error {
	p.cmdWaitOnce.Do(func() {
		slog.Debug("calling cmd.Wait()")

		p.cmdWaitResult = p.cmd.Wait()
		slog.Debug("cmd.Wait() completed", "result", p.cmdWaitResult)
		close(p.waitCh)
	})

	<-p.waitCh

	return p.cmdWaitResult
}

func (p *process) Wait() error {
	slog.Debug("waiting for process to complete")

	defer func() {
		_ = p.Stop(context.Background())
	}()

	if p.cmd.Process == nil {
		slog.Debug("waiting for process to finish (no PID available)")
	} else {
		slog.Debug("waiting for process to finish", "pid", p.cmd.Process.Pid)
	}

	err := p.cmdWait()

	// Let the readers drain what the process wrote before the deferred Stop
	// closes doneCh. Bounded, because a grandchild may keep the pipes open.
	p.waitForOutputDrain()

	if p.cmd.Process == nil {
		slog.Debug("process finished")
	} else {
		slog.Debug("process finished", "pid", p.cmd.Process.Pid)
	}

	if err != nil {
		return p.handleWaitError(err)
	}

	slog.Debug("process completed successfully")

	return nil
}

// waitForOutputDrain blocks until the output streams are fully drained or
// outputDrainTimeout passes.
func (p *process) waitForOutputDrain() {
	timer := time.NewTimer(outputDrainTimeout)
	defer timer.Stop()

	select {
	case <-p.outputDone:
	case <-timer.C:
		slog.Debug("output drain timed out, a descendant may hold the pipes open")
	}
}

func (p *process) handleWaitError(err error) error {
	slog.Debug("process wait failed", "error", err)

	// Check if this is an exit error with status > 0
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() > 0 {
		p.stderrMu.Lock()
		stderrContent := strings.Join(p.stderrBuffer, "\n")
		p.stderrMu.Unlock()

		return ctxerrors.Wrap(commonerrors.ErrFailed,
			fmt.Sprintf(
				"(exit %d): %s",
				exitError.ExitCode(),
				stderrContent,
			),
		)
	}

	signal := getTerminationSignal(err)
	slog.Debug("process terminated by signal", "signal", signal)

	if signal == syscall.SIGTERM {
		slog.Debug("process was terminated by SIGTERM")

		return commonerrors.ErrTerminated
	}

	if isKilledBySignal(err) {
		slog.Debug("process was killed by SIGKILL")

		isErrDeadline := errors.Is(
			p.execCtx.ctx.Err(),
			context.DeadlineExceeded,
		)

		if p.execCtx != nil && isErrDeadline {
			return commonerrors.ErrTimeout
		}

		return commonerrors.ErrKilled
	}

	if p.execCtx != nil {
		return p.execCtx.handleExecutionError(err)
	}

	return ctxerrors.Wrap(err, "process wait failed")
}

func (p *process) StdinPipe() (io.WriteCloser, error) {
	pipe, err := p.cmd.StdinPipe()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "failed to get stdin pipe")
	}

	return pipe, nil
}

func (p *process) Kill(_ context.Context) error {
	var killErr error

	p.terminateOnce.Do(func() {
		defer p.cleanup()

		slog.Debug("performing immediate kill")

		if p.cmd.Process == nil {
			slog.Debug("kill requested but process has no PID - cleaning up anyway")

			return
		}

		pid := p.cmd.Process.Pid
		slog.Debug("force killing process", "pid", pid)
		p.forceKillProcess()

		killErr = commonerrors.ErrKilled
	})

	return killErr
}

func (p *process) PID() int {
	if p.cmd.Process == nil {
		return 0
	}

	return p.cmd.Process.Pid
}

func isHarmlessWaitError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "waitid: no child processes")
}

func getTerminationSignal(err error) syscall.Signal {
	if err == nil {
		return 0
	}

	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		return 0
	}

	status, ok := exitError.Sys().(syscall.WaitStatus)
	if !ok {
		return 0
	}

	if !status.Signaled() {
		return 0
	}

	signal := status.Signal()
	slog.Debug("process terminated by signal", "signal", signal)

	return signal
}

func isTerminatedBySignal(err error) bool {
	return getTerminationSignal(err) == syscall.SIGTERM
}

func isKilledBySignal(err error) bool {
	return getTerminationSignal(err) == syscall.SIGKILL
}

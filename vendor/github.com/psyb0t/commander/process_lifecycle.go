package commander

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"syscall"
	"time"

	commonerrors "github.com/psyb0t/common-go/errors"
	"github.com/psyb0t/ctxerrors"
)

const defaultStopTimeout = 3 * time.Second

func (p *process) Start() error {
	slog.Debug("creating process pipes for command")

	// The read ends are owned here. exec.Cmd.StdoutPipe/StderrPipe read ends are
	// closed by cmd.Wait as soon as the child exits, which discards output the
	// readers have not consumed yet.
	stdout, stdoutWriter, err := os.Pipe()
	if err != nil {
		slog.Debug("failed to create stdout pipe", "error", err)

		return ctxerrors.Wrap(err, "failed to get stdout pipe")
	}

	stderr, stderrWriter, err := os.Pipe()
	if err != nil {
		slog.Debug("failed to create stderr pipe", "error", err)

		closePipeEnds(stdout, stdoutWriter)

		return ctxerrors.Wrap(err, "failed to get stderr pipe")
	}

	p.cmd.Stdout = stdoutWriter
	p.cmd.Stderr = stderrWriter

	slog.Debug("starting process")

	startErr := p.cmd.Start()

	// The child has its own copies of the write ends. Closing ours means the
	// readers see EOF once the child and every grandchild holding them exit.
	closePipeEnds(stdoutWriter, stderrWriter)

	if startErr != nil {
		slog.Debug("failed to start process", "error", startErr)

		closePipeEnds(stdout, stderr)

		return ctxerrors.Wrap(startErr, "failed to start command")
	}

	p.outputReaders = []*os.File{stdout, stderr}

	if p.cmd.Process == nil {
		slog.Debug("process started but no PID available")
	} else {
		slog.Debug("process started successfully", "pid", p.cmd.Process.Pid)
	}

	slog.Debug("starting background goroutines for process")

	go p.readStdout(stdout)
	go p.readStderr(stderr)
	go p.discardInternalOutput()

	slog.Debug("process initialization complete")

	return nil
}

// closePipeEnds closes pipe ends, ignoring close errors: nothing can be done
// about them and the data they guarded is already consumed or abandoned.
func closePipeEnds(ends ...*os.File) {
	for _, end := range ends {
		_ = end.Close()
	}
}

func (p *process) readStdout(stdout io.ReadCloser) {
	slog.Debug("starting stdout reader goroutine")

	defer func() {
		slog.Debug("closing stdout pipe and internal channel")

		_ = stdout.Close()

		close(p.internalStdout)
	}()

	scanner := bufio.NewScanner(stdout)
	lineCount := 0

	for scanner.Scan() {
		line := scanner.Text()
		lineCount++

		select {
		case <-p.doneCh:
			slog.Debug("stdout reader stopping (process done)", "lines", lineCount)

			return
		case p.internalStdout <- line:
			slog.Debug("stdout line", "lineNum", lineCount, "line", line)
		}
	}

	if err := scanner.Err(); err != nil {
		slog.Debug("stdout scanner error", "lines", lineCount, "error", err)

		return
	}

	slog.Debug("stdout reader finished successfully", "lines", lineCount)
}

// readStderr reads from stderr pipe and sends to internal channel
func (p *process) readStderr(stderr io.ReadCloser) {
	slog.Debug("starting stderr reader goroutine")

	defer func() {
		slog.Debug("closing stderr pipe and internal channel")

		_ = stderr.Close() // Ignore close error - nothing we can do

		close(p.internalStderr) // Close internal channel when done
	}()

	scanner := bufio.NewScanner(stderr)
	lineCount := 0

	for scanner.Scan() {
		line := scanner.Text()
		lineCount++

		select {
		case <-p.doneCh:
			// Process is done, stop reading
			slog.Debug("stderr reader stopping (process done)", "lines", lineCount)

			return
		case p.internalStderr <- line:
			// Add to buffer for error reporting
			p.stderrMu.Lock()
			p.stderrBuffer = append(p.stderrBuffer, line)
			p.stderrMu.Unlock()

			slog.Debug("stderr line", "lineNum", lineCount, "line", line)
		}
	}

	if err := scanner.Err(); err != nil {
		slog.Debug("stderr scanner error", "lines", lineCount, "error", err)

		return
	}

	slog.Debug("stderr reader finished successfully", "lines", lineCount)
}

// cleanup performs all resource cleanup operations
func (p *process) cleanup() {
	slog.Debug("performing resource cleanup")

	// Signal all goroutines to stop and close channels
	slog.Debug("signaling all goroutines to stop")
	close(p.doneCh)

	// Unblocks any reader still waiting on a pipe a grandchild holds open
	closePipeEnds(p.outputReaders...)

	// Close stream channels
	p.closeStreamChannels()

	slog.Debug("cleanup complete")
}

// Stop terminates the process gracefully with context deadline, then kills forcefully
func (p *process) Stop(ctx context.Context) error {
	var stopErr error

	// Use sync.Once to ensure termination happens exactly once
	p.terminateOnce.Do(func() {
		defer p.cleanup()

		stopErr = p.performGracefulStop(ctx)
	})

	return stopErr
}

// performGracefulStop handles the main stop logic
func (p *process) performGracefulStop(ctx context.Context) error {
	slog.Debug("performing graceful stop")

	if p.cmd.Process == nil {
		slog.Debug("stop requested but process has no PID - cleaning up anyway")

		return nil
	}

	pid := p.cmd.Process.Pid
	slog.Debug("stopping process", "pid", pid)

	timeoutCtx, cancel := p.setupTimeoutContext(ctx)
	if cancel != nil {
		defer cancel()
	}

	if err := p.sendSIGTERM(); err != nil {
		return err
	}

	return p.waitForProcessExit(timeoutCtx)
}

// setupTimeoutContext creates timeout context if none provided
func (p *process) setupTimeoutContext(
	ctx context.Context,
) (context.Context, context.CancelFunc) {
	_, hasDeadline := ctx.Deadline()
	if !hasDeadline {
		return context.WithTimeout(ctx, defaultStopTimeout)
	}

	return ctx, nil
}

// sendSIGTERM sends SIGTERM signal to process group
func (p *process) sendSIGTERM() error {
	pid := p.cmd.Process.Pid
	slog.Debug("sending SIGTERM to process group", "pid", pid)

	// Kill entire process group to catch child processes
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
		slog.Debug("failed to send SIGTERM to process group", "pid", pid, "error", err)

		return ctxerrors.Wrap(err, "failed to send SIGTERM")
	}

	return nil
}

// waitForProcessExit waits for process to exit or forces kill on timeout
func (p *process) waitForProcessExit(timeoutCtx context.Context) error {
	pid := p.cmd.Process.Pid
	slog.Debug("SIGTERM sent, waiting for graceful shutdown", "pid", pid)

	done := make(chan error, 1)

	go func() {
		done <- p.cmdWait()
	}()

	select {
	case err := <-done:
		return p.handleProcessExitResult(err)
	case <-timeoutCtx.Done():
		return p.handleProcessTimeout(timeoutCtx)
	}
}

// handleProcessExitResult processes the result from process exit
func (p *process) handleProcessExitResult(err error) error {
	if isHarmlessWaitError(err) {
		slog.Debug("process exited cleanly")

		return nil
	}

	if getTerminationSignal(err) == syscall.SIGTERM {
		slog.Debug("process gracefully terminated by SIGTERM")

		return commonerrors.ErrTerminated
	}

	if isKilledBySignal(err) {
		slog.Debug("process was killed by SIGKILL")

		return commonerrors.ErrKilled
	}

	slog.Debug("process exited with error", "error", err)

	return err
}

// handleProcessTimeout handles timeout or cancellation scenarios
func (p *process) handleProcessTimeout(timeoutCtx context.Context) error {
	pid := p.cmd.Process.Pid

	if errors.Is(timeoutCtx.Err(), context.DeadlineExceeded) {
		slog.Debug("graceful shutdown timeout, force killing", "pid", pid)

		p.forceKillProcess()

		return commonerrors.ErrKilled
	}

	slog.Debug("context cancelled, force killing", "pid", pid)

	p.forceKillProcess()

	return commonerrors.ErrKilled
}

// forceKillProcess immediately kills process group with SIGKILL
func (p *process) forceKillProcess() {
	pid := p.cmd.Process.Pid
	slog.Debug("force killing process group (SIGKILL)", "pid", pid)

	// Kill entire process group to catch child processes
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			slog.Debug("process group was already finished", "pid", pid)

			return
		}

		slog.Debug("failed to force kill process group", "pid", pid, "error", err)

		return
	}

	slog.Debug("SIGKILL sent, waiting for process to exit", "pid", pid)

	// Wait for process to exit after SIGKILL
	err := p.cmdWait()
	if err == nil {
		return
	}

	if isKilledBySignal(err) || isTerminatedBySignal(err) || isHarmlessWaitError(err) {
		slog.Debug("process successfully killed")

		return
	}

	slog.Debug("process failed after SIGKILL", "error", err)
}

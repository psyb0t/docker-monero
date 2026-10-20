// Package processtest provides an in-memory process.Launcher for tests.
package processtest

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/psyb0t/docker-monero/internal/pkg/process"
)

// ErrLaunchRefused is returned by a Launcher configured to fail.
var ErrLaunchRefused = errors.New("launch refused")

// ErrStopped is what Wait returns after Stop.
var ErrStopped = errors.New("stopped")

// Launch records one Launch call.
type Launch struct {
	Name string
	Args []string
	Env  []string
}

// Proc is a fake child. It runs until Exit or Stop is called.
type Proc struct {
	pid      int
	done     chan struct{}
	once     sync.Once
	mu       sync.Mutex
	err      error
	stops    int
	onStop   func()
	StopErr  error
	launched Launch
}

// Wait blocks until the fake exits.
func (p *Proc) Wait() error {
	<-p.done

	p.mu.Lock()
	defer p.mu.Unlock()

	return p.err
}

// Stop ends the fake with ErrStopped.
func (p *Proc) Stop(_ context.Context) error {
	p.mu.Lock()
	p.stops++
	stopErr := p.StopErr
	p.mu.Unlock()

	p.Exit(ErrStopped)

	return stopErr
}

// PID is a fake process ID.
func (p *Proc) PID() int {
	return p.pid
}

// Exit ends the fake with err, releasing whatever OnLaunch started the way
// a dead process frees its port. Only the first call has an effect.
func (p *Proc) Exit(err error) {
	p.once.Do(func() {
		p.mu.Lock()
		p.err = err
		onStop := p.onStop
		p.mu.Unlock()

		if onStop != nil {
			onStop()
		}

		close(p.done)
	})
}

// Stops counts Stop calls.
func (p *Proc) Stops() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.stops
}

// Launched is the call that created this fake.
func (p *Proc) Launched() Launch {
	return p.launched
}

// Exited reports whether the fake has ended.
func (p *Proc) Exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// Launcher hands out fake children.
type Launcher struct {
	mu       sync.Mutex
	procs    []*Proc
	nextPID  int
	failNext int

	// OnLaunch runs for every successful launch before it returns. It can
	// start a fake server the child would have served, and its returned
	// func runs when the child is stopped.
	OnLaunch func(launch Launch) (onStop func(), err error)
}

// FailNext makes the next n launches fail.
func (l *Launcher) FailNext(n int) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.failNext = n
}

// Launch implements process.Launcher.
func (l *Launcher) Launch(
	_ context.Context,
	name string,
	args []string,
	_ *slog.Logger,
	env ...string,
) (process.Process, error) {
	launch := Launch{
		Name: name,
		Args: append([]string(nil), args...),
		Env:  append([]string(nil), env...),
	}

	l.mu.Lock()

	if l.failNext > 0 {
		l.failNext--
		l.mu.Unlock()

		return nil, ErrLaunchRefused
	}

	l.nextPID++
	proc := &Proc{
		pid:      l.nextPID,
		done:     make(chan struct{}),
		launched: launch,
	}
	l.procs = append(l.procs, proc)
	onLaunch := l.OnLaunch
	l.mu.Unlock()

	if onLaunch != nil {
		onStop, err := onLaunch(launch)
		if err != nil {
			proc.Exit(err)

			return nil, err
		}

		proc.mu.Lock()
		proc.onStop = onStop
		proc.mu.Unlock()
	}

	return proc, nil
}

// Procs returns every fake launched so far.
func (l *Launcher) Procs() []*Proc {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]*Proc(nil), l.procs...)
}

// Last returns the most recent fake, or nil.
func (l *Launcher) Last() *Proc {
	l.mu.Lock()
	defer l.mu.Unlock()

	if len(l.procs) == 0 {
		return nil
	}

	return l.procs[len(l.procs)-1]
}

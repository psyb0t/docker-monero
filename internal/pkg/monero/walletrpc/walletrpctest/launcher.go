package walletrpctest

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/docker-monero/internal/pkg/process/processtest"
)

const (
	flagRPCBindPort = "--rpc-bind-port"
	// Keep this separate from the harnesses' [20000, 32000) range.
	portRangeStart = 10000
	portRangeEnd   = 20000
)

var (
	errNoPortFlag  = errors.New("launch has no --rpc-bind-port")
	errNoFreeRange = errors.New("no free port range found")
)

// NewLauncher returns a fake launcher that serves a fake wallet-rpc on the
// port each launch asks for and stops it when the fake process is stopped.
func NewLauncher(state *State) *processtest.Launcher {
	launcher := &processtest.Launcher{}
	launcher.OnLaunch = func(launch processtest.Launch) (func(), error) {
		port, err := portArg(launch.Args)
		if err != nil {
			return nil, err
		}

		server, err := Start(state, port)
		if err != nil {
			return nil, err
		}

		return server.Stop, nil
	}

	return launcher
}

func portArg(args []string) (int, error) {
	for i, arg := range args {
		if arg == flagRPCBindPort && i+1 < len(args) {
			port, err := strconv.Atoi(args[i+1])
			if err != nil {
				return 0, err //nolint:wrapcheck // test helper
			}

			return port, nil
		}
	}

	return 0, errNoPortFlag
}

// Process-held byte-range locks prevent concurrent test packages from being
// handed overlapping ports before their fake servers bind them.
//
//nolint:gochecknoglobals // allocations must be shared by all test fixtures.
var portRanges = struct {
	sync.Mutex
	next int
	file *os.File
}{next: portRangeStart}

// FreePortRange returns count consecutive free loopback ports below Linux's
// ephemeral range, reserved against other callers until this process exits.
func FreePortRange(count int) (int, error) {
	if count <= 0 || count > portRangeEnd-portRangeStart {
		return 0, errNoFreeRange
	}

	portRanges.Lock()
	defer portRanges.Unlock()

	if portRanges.file == nil {
		path := filepath.Join(os.TempDir(),
			"docker-monero-ports-"+strconv.Itoa(os.Getuid())+".lock",
		)

		file, err := os.OpenFile(path,
			os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, fileMode,
		)
		if err != nil {
			return 0, ctxerrors.Wrap(err, "open test port reservations")
		}

		portRanges.file = file
	}

	for portRanges.next+count <= portRangeEnd {
		start := portRanges.next
		portRanges.next += count
		lock := syscall.Flock_t{
			Type: syscall.F_WRLCK, Start: int64(start), Len: int64(count),
		}

		err := syscall.FcntlFlock(portRanges.file.Fd(), syscall.F_SETLK, &lock)
		if errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EAGAIN) {
			continue
		}

		if err != nil {
			return 0, ctxerrors.Wrap(err, "reserve test port range")
		}

		if rangeFree(start, count) {
			return start, nil
		}
	}

	return 0, errNoFreeRange
}

func rangeFree(start, count int) bool {
	listeners := make([]net.Listener, 0, count)

	defer func() {
		for _, listener := range listeners {
			_ = listener.Close() // probe listeners have no pending I/O.
		}
	}()

	for port := start; port < start+count; port++ {
		listener, err := net.Listen(
			"tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		)
		if err != nil {
			return false
		}

		listeners = append(listeners, listener)
	}

	return true
}

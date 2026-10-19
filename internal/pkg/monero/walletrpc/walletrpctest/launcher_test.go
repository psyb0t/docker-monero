package walletrpctest

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFreePortRange(t *testing.T) {
	t.Parallel()

	const count = 3

	start, err := FreePortRange(count)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, start, 10000)
	assert.LessOrEqual(t, start+count, 20000)

	for port := start; port < start+count; port++ {
		listener, err := net.Listen("tcp",
			net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		)
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, listener.Close()) })
	}
}

func TestFreePortRangeInvalidCount(t *testing.T) {
	t.Parallel()

	for _, count := range []int{-1, 0, 10001} {
		start, err := FreePortRange(count)
		assert.ErrorIs(t, err, errNoFreeRange)
		assert.Zero(t, start)
	}
}

func TestFreePortRangeConcurrent(t *testing.T) {
	t.Parallel()

	const (
		allocations = 16
		count       = 3
	)

	type result struct {
		start int
		err   error
	}

	results := make(chan result, allocations)

	for range allocations {
		go func() {
			start, err := FreePortRange(count)
			results <- result{start: start, err: err}
		}()
	}

	seen := map[int]bool{}

	for range allocations {
		allocated := <-results
		require.NoError(t, allocated.err)

		for port := allocated.start; port < allocated.start+count; port++ {
			assert.False(t, seen[port], "port %d allocated twice", port)

			seen[port] = true
		}
	}
}

func TestFreePortRangeProcess(t *testing.T) {
	const count = 3

	start, err := FreePortRange(count)
	require.NoError(t, err)

	if len(os.Args) > 2 && os.Args[len(os.Args)-2] == "--" {
		require.NoError(t, os.WriteFile(os.Args[len(os.Args)-1],
			[]byte(strconv.Itoa(start)), 0o600,
		))

		return
	}

	path := filepath.Join(t.TempDir(), "child-port")
	//nolint:gosec // Re-execute this test binary with fixed arguments.
	command := exec.CommandContext(t.Context(), os.Args[0],
		"-test.run=^TestFreePortRangeProcess$", "--", path,
	)
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))

	value, err := os.ReadFile(path)
	require.NoError(t, err)

	child, err := strconv.Atoi(string(value))
	require.NoError(t, err)
	assert.True(t, child+count <= start || start+count <= child,
		"processes allocated overlapping ranges: %d and %d", start, child,
	)
}

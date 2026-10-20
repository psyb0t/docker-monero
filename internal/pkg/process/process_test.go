package process

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

func TestLaunchForwardsOutput(t *testing.T) {
	sink := &syncBuffer{}
	logger := slog.New(slog.NewTextHandler(sink, nil))

	child, err := NewCommanderLauncher().Launch(
		context.Background(),
		"sh",
		[]string{"-c", "echo out-line; echo err-line >&2"},
		logger,
	)
	require.NoError(t, err)
	require.NoError(t, child.Wait())

	assert.Positive(t, child.PID())
	assert.Eventually(t, func() bool {
		text := sink.String()

		return strings.Contains(text, "out-line") &&
			strings.Contains(text, "err-line")
	}, 5*time.Second, 10*time.Millisecond)
}

func TestLaunchSurvivesContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	child, err := NewCommanderLauncher().Launch(
		ctx, "sleep", []string{"30"}, slog.New(slog.DiscardHandler),
	)
	require.NoError(t, err)

	cancel()

	exited := make(chan error, 1)

	go func() { exited <- child.Wait() }()

	select {
	case <-exited:
		t.Fatal("child died with its launch context")
	case <-time.After(200 * time.Millisecond):
	}

	stopCtx, stopCancel := context.WithTimeout(
		context.Background(), 5*time.Second,
	)
	defer stopCancel()

	require.NoError(t, child.Stop(stopCtx))
}

func TestLaunchMissingBinary(t *testing.T) {
	_, err := NewCommanderLauncher().Launch(
		context.Background(),
		"/nonexistent/binary",
		nil,
		slog.New(slog.DiscardHandler),
	)
	require.Error(t, err)
}

func TestLaunchEnvironmentIsExplicit(t *testing.T) {
	t.Setenv("PROCESS_TEST_INHERITED", "must-not-be-inherited")

	testCases := []struct {
		name     string
		env      []string
		expected string
	}{
		{name: "empty"},
		{
			name:     "explicit",
			env:      []string{"PROCESS_TEST_CHILD=example"},
			expected: "example",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{
				"-c",
				`test -z "$PROCESS_TEST_INHERITED" &&
     test "$PROCESS_TEST_CHILD" = "$1"`,
				"sh", tc.expected,
			}

			child, err := NewCommanderLauncher().Launch(
				context.Background(), "sh", args,
				slog.New(slog.DiscardHandler), tc.env...,
			)
			require.NoError(t, err)
			require.NoError(t, child.Wait())
		})
	}
}

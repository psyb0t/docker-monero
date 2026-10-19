package node

import (
	"math"
	"testing"
	"time"

	"github.com/psyb0t/docker-monero/internal/pkg/monero/daemonrpc"
	"github.com/stretchr/testify/assert"
)

func TestSyncPercent(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		info daemonrpc.Info
		want float64
	}{
		{
			name: "synced node reports a zero target",
			info: daemonrpc.Info{Synchronized: true, Height: 100},
			want: 100,
		},
		{
			name: "synced node at its target",
			info: daemonrpc.Info{
				Synchronized: true, Height: 100, TargetHeight: 100,
			},
			want: 100,
		},
		{
			name: "synced node above a stale target",
			info: daemonrpc.Info{
				Synchronized: true, Height: 120, TargetHeight: 100,
			},
			want: 100,
		},
		{
			name: "synced flag with a target still ahead",
			info: daemonrpc.Info{
				Synchronized: true, Height: 100, TargetHeight: 200,
			},
			want: 50,
		},
		{
			name: "fresh node without peers",
			info: daemonrpc.Info{},
			want: 0,
		},
		{
			name: "unsynced node with a zero target",
			info: daemonrpc.Info{Height: 500},
			want: 0,
		},
		{
			name: "halfway",
			info: daemonrpc.Info{Height: 50, TargetHeight: 200},
			want: 25,
		},
		{
			name: "no blocks yet",
			info: daemonrpc.Info{TargetHeight: 100},
			want: 0,
		},
		{
			name: "height at the target before the synced flag flips",
			info: daemonrpc.Info{Height: 200, TargetHeight: 200},
			want: 100,
		},
		{
			name: "height past the target is capped",
			info: daemonrpc.Info{Height: 300, TargetHeight: 200},
			want: 100,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.InDelta(t, tc.want, SyncPercent(tc.info), 1e-9)
		})
	}
}

func TestSyncPercentFraction(t *testing.T) {
	t.Parallel()

	got := SyncPercent(daemonrpc.Info{Height: 1, TargetHeight: 3})

	assert.InDelta(t, 100.0/3.0, got, 1e-9)
}

func TestSyncPercentStaysWithinBounds(t *testing.T) {
	t.Parallel()

	heights := []uint64{0, 1, 2, 99, 100, 101, math.MaxInt32, 1 << 40}

	for _, synchronized := range []bool{false, true} {
		for _, height := range heights {
			for _, target := range heights {
				got := SyncPercent(daemonrpc.Info{
					Synchronized: synchronized,
					Height:       height,
					TargetHeight: target,
				})

				assert.GreaterOrEqual(t, got, 0.0)
				assert.LessOrEqual(t, got, 100.0)
			}
		}
	}
}

func TestUnixTimeIsUTC(t *testing.T) {
	t.Parallel()

	got := unixTime(1_700_000_000)

	assert.Equal(t, time.UTC, got.Location())
	assert.Equal(t, int64(1_700_000_000), got.Unix())
	assert.Equal(t, 0, got.Nanosecond())
}

func TestNonNil(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{}, nonNil[string](nil))
	assert.NotNil(t, nonNil[int](nil))
	assert.Equal(t, []int{1, 2}, nonNil([]int{1, 2}))
	assert.Equal(t, []int{}, nonNil([]int{}))
}

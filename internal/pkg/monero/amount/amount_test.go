package amount

import (
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormatXMR(t *testing.T) {
	tests := []struct {
		name   string
		atomic uint64
		want   string
	}{
		{name: "zero", atomic: 0, want: "0.000000000000"},
		{name: "one piconero", atomic: 1, want: "0.000000000001"},
		{name: "one xmr", atomic: AtomicPerXMR, want: "1.000000000000"},
		{name: "fractional", atomic: 1_500_000_000_000, want: "1.500000000000"},
		{
			name:   "max uint64",
			atomic: ^uint64(0),
			want:   "18446744.073709551615",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, FormatXMR(tt.atomic))
		})
	}
}

func TestFormatAtomic(t *testing.T) {
	assert.Equal(t, "18446744073709551615", FormatAtomic(^uint64(0)))
	assert.Equal(t, "0", FormatAtomic(0))
}

func TestParseAtomic(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		want    uint64
		wantErr bool
	}{
		{name: "zero", text: "0", want: 0},
		{name: "plain", text: "123456789", want: 123456789},
		{name: "max", text: "18446744073709551615", want: ^uint64(0)},
		{name: "empty", text: "", wantErr: true},
		{name: "negative", text: "-1", wantErr: true},
		{name: "plus sign", text: "+1", wantErr: true},
		{name: "decimal", text: "1.5", wantErr: true},
		{name: "space", text: " 1", wantErr: true},
		{name: "overflow", text: "18446744073709551616", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseAtomic(tt.text)
			if tt.wantErr {
				require.ErrorIs(t, err, commerr.ErrInvalidValue)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

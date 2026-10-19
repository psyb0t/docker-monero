package invalid

import (
	"errors"
	"testing"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMessageSurvivesWrapping(t *testing.T) {
	err := ctxerrors.Wrap(Newf("limit must be 1-%d", 10), "list")

	message, ok := Message(err)
	require.True(t, ok)
	assert.Equal(t, "limit must be 1-10", message)
	require.ErrorIs(t, err, commerr.ErrInvalidArgument)
	assert.Equal(t,
		"limit must be 1-10", Newf("limit must be 1-%d", 10).Error(),
	)
}

func TestMessageMissing(t *testing.T) {
	_, ok := Message(errors.New("plain"))
	assert.False(t, ok)
}

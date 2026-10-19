package healthcheck

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/stretchr/testify/require"
)

func serverAddress(t *testing.T, status int) string {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, readyPath, r.URL.Path)
			w.WriteHeader(status)
		},
	))
	t.Cleanup(server.Close)

	return server.Listener.Addr().String()
}

func TestCheckReady(t *testing.T) {
	address := serverAddress(t, http.StatusOK)
	require.NoError(t, Check(context.Background(), address))

	_, port, err := net.SplitHostPort(address)
	require.NoError(t, err)
	require.NoError(t, Check(context.Background(), "0.0.0.0:"+port))
	require.NoError(t, Check(context.Background(), ":"+port))
}

func TestCheckNotReady(t *testing.T) {
	err := Check(
		context.Background(), serverAddress(t, http.StatusServiceUnavailable),
	)
	require.ErrorIs(t, err, commerr.ErrNotReady)
}

func TestCheckBadAddress(t *testing.T) {
	require.ErrorIs(t,
		Check(context.Background(), "nope"), commerr.ErrInvalidValue,
	)
}

func TestCheckUnreachable(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	address := server.Listener.Addr().String()
	server.Close()

	require.Error(t, Check(context.Background(), address))
}

func TestCheckBadRequest(t *testing.T) {
	require.Error(t, Check(context.Background(), "bad host:1"))
}

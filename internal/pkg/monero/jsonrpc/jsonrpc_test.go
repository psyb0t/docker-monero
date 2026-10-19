package jsonrpc

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newServer(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	return New(server.URL+"/", time.Second)
}

func TestCallDecodesResult(t *testing.T) {
	client := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, pathJSONRPC, r.URL.Path)
		assert.Equal(t, contentType, r.Header.Get(headerContent))

		var got request
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		assert.Equal(t, "get_info", got.Method)
		assert.Equal(t, versionJSON, got.JSONRPC)

		_, _ = io.WriteString(w, `{"result":{"status":"OK","height":42}}`)
	})

	var result struct {
		Height uint64 `json:"height"`
	}

	require.NoError(t, client.Call(
		context.Background(), "get_info", map[string]int{"a": 1}, &result,
	))
	assert.Equal(t, uint64(42), result.Height)
	assert.NotContains(t, client.BaseURL(), "//:")
}

func TestCallNilResultDiscards(t *testing.T) {
	client := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"result":{"anything":true}}`)
	})

	require.NoError(t, client.Call(context.Background(), "store", nil, nil))
}

func TestCallEmptyResult(t *testing.T) {
	client := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id":"0"}`)
	})

	var result struct{}
	require.NoError(t, client.Call(context.Background(), "x", nil, &result))
}

func TestCallRPCError(t *testing.T) {
	client := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"error":{
			"code":-1,"message":"Failed to open wallet : Invalid password."
		}}`)
	})

	err := client.Call(context.Background(), "open_wallet", nil, nil)
	require.ErrorIs(t, err, ErrRPC)

	var rpcErr *Error
	require.ErrorAs(t, err, &rpcErr)
	assert.Equal(t, -1, rpcErr.Code)
	assert.True(t, rpcErr.HasMessage("invalid PASSWORD"))
	assert.False(t, rpcErr.HasMessage("not found"))
	assert.Contains(t, rpcErr.Error(), "-1")
}

func TestStatusHandling(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr error
	}{
		{name: "busy", body: `{"status":"BUSY"}`, wantErr: commerr.ErrNotReady},
		{name: "failed", body: `{"status":"Failed"}`, wantErr: ErrStatus},
		{
			name:    "bad json",
			body:    `{"status":`,
			wantErr: commerr.ErrUnmarshalFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newServer(t,
				func(w http.ResponseWriter, _ *http.Request) {
					_, _ = io.WriteString(w, tt.body)
				},
			)

			var result map[string]any

			err := client.Post(
				context.Background(), "/get_transactions", nil, &result,
			)
			require.ErrorIs(t, err, tt.wantErr)
		})
	}
}

func TestPostNonObjectResult(t *testing.T) {
	client := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `"text"`)
	})

	var result string
	require.NoError(t, client.Post(context.Background(), "/x", nil, &result))
	assert.Equal(t, "text", result)
}

func TestCallBadEnvelope(t *testing.T) {
	client := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `not json`)
	})

	err := client.Call(context.Background(), "x", nil, nil)
	require.ErrorIs(t, err, commerr.ErrUnmarshalFailed)
}

func TestHTTPStatusError(t *testing.T) {
	client := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	err := client.Call(context.Background(), "x", nil, nil)
	require.ErrorIs(t, err, commerr.ErrUnavailable)
}

func TestResponseTooLarge(t *testing.T) {
	client := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"result":"`+strings.Repeat("a", 64)+`"}`)
	})
	client.maxResponseBytes = 16

	err := client.Call(context.Background(), "x", nil, nil)
	require.ErrorIs(t, err, commerr.ErrOutOfRange)
}

func TestMarshalFailure(t *testing.T) {
	client := New("http://127.0.0.1:1", time.Second)

	err := client.Call(context.Background(), "x", make(chan int), nil)
	require.ErrorIs(t, err, commerr.ErrMarshalFailed)
}

func TestConnectFailure(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	url := server.URL
	server.Close()

	client := New(url, time.Second)

	err := client.Call(context.Background(), "x", nil, nil)
	require.ErrorIs(t, err, commerr.ErrConnectFailed)
}

func TestTimeout(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(
		func(_ http.ResponseWriter, _ *http.Request) { <-release },
	))

	t.Cleanup(func() {
		close(release)
		server.Close()
	})

	client := New(server.URL, 50*time.Millisecond)

	err := client.Call(context.Background(), "x", nil, nil)
	require.ErrorIs(t, err, commerr.ErrTimeout)
}

func TestCancelledContext(t *testing.T) {
	client := newServer(t, func(_ http.ResponseWriter, _ *http.Request) {})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := client.Call(ctx, "x", nil, nil)
	require.ErrorIs(t, err, context.Canceled)
}

func TestBadURL(t *testing.T) {
	client := New("http://bad host", time.Second)

	err := client.Call(context.Background(), "x", nil, nil)
	require.Error(t, err)
}

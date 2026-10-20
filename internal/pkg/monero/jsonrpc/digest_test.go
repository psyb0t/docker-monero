package jsonrpc

import (
	"context"
	"crypto/md5" //nolint:gosec // test verifies the daemon's MD5 protocol
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testRPCLogin = `example-user:EXAMPLE-DO-NOT-USE`

// md5Hex recomputes digest hashes independently of digestHash.
//
//nolint:gosec // independently verifies the daemon's MD5 wire protocol
func md5Hex(value string) string {
	// nosemgrep: go.lang.security.audit.crypto.use_of_weak_crypto.use-of-md5
	return fmt.Sprintf("%x", md5.Sum([]byte(value)))
}

func TestDigestRoundTrip(t *testing.T) {
	t.Parallel()

	for _, path := range []string{pathJSONRPC, "/get_info?test=1"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()

			var calls atomic.Int32

			server := httptest.NewServer(http.HandlerFunc(
				func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)

					raw, err := io.ReadAll(r.Body)
					assert.NoError(t, err)
					assert.JSONEq(t, `{"example":"body"}`, string(raw))

					header := r.Header.Get(
						aichteeteapee.HeaderNameAuthorization,
					)
					if header == "" {
						challenge := strings.Join([]string{
							`Digest realm="example, daemon"`,
							`nonce="example-nonce"`,
							`qop="auth-int, auth"`,
							`algorithm=MD5`,
							`opaque="example-opaque"`,
						}, ", ")
						w.Header().Set(aichteeteapee.HeaderNameWWWAuthenticate,
							challenge)
						w.WriteHeader(http.StatusUnauthorized)

						return
					}

					parameters, err := digestParameters(header)
					assert.NoError(t, err)
					assert.Equal(t, "example-user", parameters["username"])
					assert.Equal(t, r.URL.RequestURI(), parameters["uri"])
					assert.Equal(t, "00000001", parameters["nc"])
					assert.Equal(t, "auth", parameters["qop"])
					assert.Equal(t, "example-opaque", parameters["opaque"])
					assert.GreaterOrEqual(t, len(parameters["cnonce"]), 26)
					assert.NotContains(t, header, "EXAMPLE-DO-NOT-USE")

					// Verify the RFC response independently of the client.
					ha1 := md5Hex(
						"example-user:example, daemon:EXAMPLE-DO-NOT-USE")
					ha2 := md5Hex(r.Method + ":" + r.URL.RequestURI())
					expected := md5Hex(strings.Join([]string{
						ha1, "example-nonce", "00000001",
						parameters["cnonce"], "auth", ha2,
					}, ":"))
					assert.Equal(t, expected, parameters["response"])

					_, _ = io.WriteString(w, `{"status":"OK"}`)
				},
			))
			t.Cleanup(server.Close)

			client := New(server.URL, time.Second, WithLogin(testRPCLogin))
			err := client.Post(t.Context(), path,
				map[string]string{"example": "body"}, nil)
			require.NoError(t, err)
			assert.Equal(t, int32(2), calls.Load())
		})
	}
}

func TestDigestAuthenticationFailures(t *testing.T) {
	t.Parallel()

	for _, login := range []string{"", testRPCLogin} {
		t.Run(login, func(t *testing.T) {
			t.Parallel()

			var calls atomic.Int32

			server := httptest.NewServer(http.HandlerFunc(
				func(w http.ResponseWriter, _ *http.Request) {
					calls.Add(1)
					w.Header().Set(aichteeteapee.HeaderNameWWWAuthenticate,
						`Digest realm="example", nonce="example", qop="auth"`)
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = io.WriteString(w, testRPCLogin)
				},
			))
			t.Cleanup(server.Close)

			client := New(server.URL, time.Second, WithLogin(login))
			err := client.Call(t.Context(), "get_info", nil, nil)
			require.ErrorIs(t, err, commerr.ErrNotAuthenticated)
			assert.NotContains(t, err.Error(), testRPCLogin)
			assert.LessOrEqual(t, calls.Load(), int32(2))
		})
	}
}

func TestDigestRejectsUnsupportedChallenges(t *testing.T) {
	t.Parallel()

	for _, challenge := range []string{
		"", `Basic realm="example"`, `Digest realm="unterminated`,
		`Digest realm="x", nonce="x", qop="auth", algorithm=SHA-256`,
		`Digest realm="example", nonce="example", qop="auth-int"`,
		`Digest realm="example", qop="auth"`,
	} {
		t.Run(challenge, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(
				func(w http.ResponseWriter, r *http.Request) {
					assert.Empty(t, r.Header.Get(
						aichteeteapee.HeaderNameAuthorization,
					))
					w.Header().Set(
						aichteeteapee.HeaderNameWWWAuthenticate, challenge,
					)
					w.WriteHeader(http.StatusUnauthorized)
				},
			))
			t.Cleanup(server.Close)

			client := New(server.URL, time.Second, WithLogin(testRPCLogin))
			err := client.Call(t.Context(), "get_info", nil, nil)
			require.ErrorIs(t, err, commerr.ErrNotAuthenticated)
			assert.NotContains(t, err.Error(), "EXAMPLE-DO-NOT-USE")
		})
	}
}

func TestDigestQuotedParameters(t *testing.T) {
	t.Parallel()

	request, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"http://example.test/json_rpc",
		nil,
	)
	require.NoError(t, err)

	header, err := digestAuthorization(request,
		`Digest realm="example\"daemon\\", nonce="example", qop="auth"`,
		`example\"user:EXAMPLE-DO-NOT-USE`)
	require.NoError(t, err)
	parameters, err := digestParameters(header)
	require.NoError(t, err)
	assert.Equal(t, `example\"user`, parameters["username"])
	assert.Equal(t, `example"daemon\`, parameters["realm"])
	assert.NotContains(t, header, "EXAMPLE-DO-NOT-USE")

	for _, login := range []string{
		"missing-separator", ":EXAMPLE-DO-NOT-USE",
		"example\tuser:EXAMPLE-DO-NOT-USE",
	} {
		_, err := digestAuthorization(request,
			`Digest realm="example", nonce="example", qop="auth"`, login)
		require.ErrorIs(t, err, commerr.ErrNotAuthenticated)
		assert.NotContains(t, err.Error(), login)
	}
}

func TestDigestDoesNotFollowRedirects(t *testing.T) {
	t.Parallel()

	client := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(
			w, r, "http://example.test/", http.StatusTemporaryRedirect,
		)
	})
	err := client.Call(t.Context(), "get_info", nil, nil)
	require.ErrorIs(t, err, commerr.ErrUnavailable)
}

func TestDigestConcurrentConnectionNonce(t *testing.T) {
	t.Parallel()

	var connections sync.Map

	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			header := r.Header.Get(aichteeteapee.HeaderNameAuthorization)
			if header == "" {
				connections.Store(r.RemoteAddr, true)
				w.Header().Set(aichteeteapee.HeaderNameWWWAuthenticate,
					`Digest realm="x", qop="auth", nonce="`+r.RemoteAddr+`"`)
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, "example challenge body")

				return
			}

			_, exists := connections.Load(r.RemoteAddr)
			assert.True(t, exists, "authentication changed TCP connections")

			parameters, err := digestParameters(header)
			assert.NoError(t, err)
			assert.Equal(t, r.RemoteAddr, parameters["nonce"])

			_, _ = io.WriteString(w, `{"status":"OK"}`)
		},
	))
	t.Cleanup(server.Close)

	client := New(server.URL, time.Second, WithLogin(testRPCLogin))

	var calls sync.WaitGroup

	for range 8 {
		calls.Go(func() {
			err := client.Post(t.Context(), "/get_info", struct{}{}, nil)
			assert.NoError(t, err)
		})
	}

	calls.Wait()
}

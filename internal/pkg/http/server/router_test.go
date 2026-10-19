package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/psyb0t/aichteeteapee"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHealthProbeNeedsNoCredentialAndIgnoresReadiness(t *testing.T) {
	s := newStack(t, stackOptions{
		ready: func(context.Context) bool { return false },
	})

	for _, mod := range []requestMod{
		withoutAuth(), withAuth("Bearer wrong"),
	} {
		resp := s.get(t, "/healthz", mod)

		requireStatus(t, resp, http.StatusOK)
		assert.JSONEq(t, `{"status":"ok"}`, resp.text())
		assert.Equal(t, "application/json", resp.header.Get("Content-Type"))
	}

	s.router.BeginShutdown()

	requireStatus(t, s.get(t, "/healthz", withoutAuth()), http.StatusOK)
}

func TestRouterWithoutOptionalHandlers(t *testing.T) {
	router := NewRouter(RouterConfig{
		APIToken: testAPIToken,
		Server:   New(nil, nil),
	})
	s := &stack{baseURL: serveRouter(t, router.Serbewr()), client: newClient()}

	resp := s.get(t, ReadyPath, withoutAuth())
	requireStatus(t, resp, http.StatusOK)
	assert.JSONEq(t, `{"status":"ready"}`, resp.text())

	for _, path := range []string{MCPPath, "/mcp/"} {
		requireStatus(t, s.get(t, path), http.StatusNotFound)
	}

	router.BeginShutdown()

	resp = s.get(t, ReadyPath, withoutAuth())
	requireStatus(t, resp, http.StatusServiceUnavailable)
	assert.JSONEq(t, `{"status":"not_ready"}`, resp.text())
	requireStatus(t, s.get(t, HealthPath, withoutAuth()), http.StatusOK)
}

func TestReadyProbeFollowsTheReadyFunction(t *testing.T) {
	var (
		ready    atomic.Bool
		calls    atomic.Int32
		deadline atomic.Value
	)

	s := newStack(t, stackOptions{ready: func(ctx context.Context) bool {
		calls.Add(1)

		if at, ok := ctx.Deadline(); ok {
			deadline.Store(at)
		}

		return ready.Load()
	}})

	resp := s.get(t, "/ready", withoutAuth())
	requireStatus(t, resp, http.StatusServiceUnavailable)
	assert.JSONEq(t, `{"status":"not_ready"}`, resp.text())

	ready.Store(true)

	started := time.Now()
	resp = s.get(t, "/ready", withoutAuth())
	requireStatus(t, resp, http.StatusOK)
	assert.JSONEq(t, `{"status":"ready"}`, resp.text())
	assert.Equal(t, "application/json", resp.header.Get("Content-Type"))

	at, ok := deadline.Load().(time.Time)
	require.True(t, ok, "the ready function must get a bounded context")
	assert.Less(t, at.Sub(started), 3*time.Second)
	assert.Greater(t, at.Sub(started), time.Duration(0))
	assert.EqualValues(t, 2, calls.Load())
}

func TestReadyProbeFailsOnceShutdownBegins(t *testing.T) {
	var calls atomic.Int32

	s := newStack(t, stackOptions{ready: func(context.Context) bool {
		calls.Add(1)

		return true
	}})

	requireStatus(t, s.get(t, "/ready", withoutAuth()), http.StatusOK)

	s.router.BeginShutdown()

	resp := s.get(t, "/ready", withoutAuth())
	requireStatus(t, resp, http.StatusServiceUnavailable)
	assert.JSONEq(t, `{"status":"not_ready"}`, resp.text())
	assert.EqualValues(t, 1, calls.Load(), "shutdown does not ask monerod")
}

func TestReadyProbeGivesUpOnASlowDependency(t *testing.T) {
	s := newStack(t, stackOptions{ready: func(ctx context.Context) bool {
		<-ctx.Done()

		return false
	}})

	started := time.Now()
	resp := s.get(t, "/ready", withoutAuth())

	requireStatus(t, resp, http.StatusServiceUnavailable)
	assert.GreaterOrEqual(t, time.Since(started), 1500*time.Millisecond)
	assert.Less(t, time.Since(started), 10*time.Second)
}

func TestReadyProbeAsksMonerod(t *testing.T) {
	s := newStack(t, stackOptions{})

	requireStatus(t, s.get(t, "/ready", withoutAuth()), http.StatusOK)

	s.daemon.failHTTP("get_version", http.StatusInternalServerError)

	requireStatus(
		t, s.get(t, "/ready", withoutAuth()), http.StatusServiceUnavailable,
	)
}

func TestProbesOnlyAnswerGet(t *testing.T) {
	s := newStack(t, stackOptions{})

	for _, path := range []string{"/healthz", "/ready"} {
		resp := s.send(t, http.MethodPost, path, "", withoutAuth())

		requireStatus(t, resp, http.StatusMethodNotAllowed)
		assert.Contains(t, resp.header.Get("Allow"), http.MethodGet)
	}
}

func TestProtectedRoutesRequireTheBearerToken(t *testing.T) {
	s := newStack(t, stackOptions{})

	routes := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"node info", http.MethodGet, "/v1/node/info", ""},
		{"wallet list", http.MethodGet, "/v1/wallets", ""},
		{
			"wallet create", http.MethodPost, "/v1/wallets",
			`{"name":"intruder","password":"pw"}`,
		},
		{"unknown api path", http.MethodGet, "/v1/nope", ""},
		{"mcp", http.MethodPost, "/mcp", mcpToolsList},
		{"mcp with slash", http.MethodPost, "/mcp/", mcpToolsList},
	}

	credentials := []struct {
		name string
		mod  requestMod
	}{
		{"no header", withoutAuth()},
		{"wrong token", withAuth("Bearer " + strings.Repeat("x", 32))},
		{"token without scheme", withAuth(testAPIToken)},
		{"basic scheme", withAuth("Basic " + testAPIToken)},
		{"token with a suffix", withAuth("Bearer " + testAPIToken + "x")},
		{"token prefix", withAuth("Bearer " + testAPIToken[:31])},
		{"empty token", withAuth("Bearer ")},
		{"scheme only", withAuth("Bearer")},
		{"two tokens", withAuth("Bearer " + testAPIToken + " " + testAPIToken)},
	}

	for _, route := range routes {
		for _, credential := range credentials {
			t.Run(route.name+"/"+credential.name, func(t *testing.T) {
				resp := s.send(
					t, route.method, route.path, route.body,
					withHeader("Accept", mcpAccept), credential.mod,
				)

				requireError(
					t, resp, http.StatusUnauthorized, "UNAUTHORIZED",
					"Unauthorized",
				)
			})
		}
	}

	assert.Zero(t, s.pool.OpenCount(), "a rejected request never ran")
	assert.Zero(t, s.wallets.Called("create_wallet"))
}

func TestTheTokenInTheQueryStringIsNotACredential(t *testing.T) {
	s := newStack(t, stackOptions{})

	for _, name := range []string{"access_token", "token", "api_key"} {
		resp := s.get(
			t, "/v1/node/info?"+name+"="+testAPIToken, withoutAuth(),
		)

		requireError(
			t, resp, http.StatusUnauthorized, "UNAUTHORIZED",
			"Unauthorized",
		)
	}
}

func TestTheBearerSchemeIsCaseInsensitive(t *testing.T) {
	s := newStack(t, stackOptions{})

	for _, scheme := range []string{"bearer", "BEARER", "BeArEr"} {
		resp := s.get(
			t, "/v1/node/info", withAuth(scheme+" "+testAPIToken),
		)

		requireStatus(t, resp, http.StatusOK)
	}
}

func TestAnEmptyAPITokenRejectsEveryRequest(t *testing.T) {
	router := NewRouter(RouterConfig{
		APIToken:   "",
		Server:     New(nil, nil),
		MCPHandler: http.NotFoundHandler(),
		Ready:      func(context.Context) bool { return true },
	})
	s := &stack{baseURL: serveRouter(t, router.Serbewr()), client: newClient()}

	for _, mod := range []requestMod{
		withoutAuth(),
		withAuth("Bearer "),
		withAuth("Bearer x"),
		withAuth("Bearer"),
	} {
		resp := s.get(t, "/v1/node/info", mod)

		requireError(
			t, resp, http.StatusUnauthorized, "UNAUTHORIZED",
			"Unauthorized",
		)
	}

	requireStatus(t, s.get(t, "/healthz", withoutAuth()), http.StatusOK)
}

func TestEveryResponseCarriesRequestIDAndSecurityHeaders(t *testing.T) {
	s := newStack(t, stackOptions{})

	requests := map[string]response{
		"probe":        s.get(t, "/healthz", withoutAuth()),
		"api":          s.get(t, "/v1/node/info"),
		"api error":    s.get(t, "/v1/node/blocks/height/-1"),
		"unauthorized": s.get(t, "/v1/node/info", withoutAuth()),
	}

	for name, resp := range requests {
		t.Run(name, func(t *testing.T) {
			assert.NotEmpty(t, resp.header.Get("X-Request-Id"))
			assert.Equal(
				t, "nosniff", resp.header.Get("X-Content-Type-Options"),
			)
			assert.Equal(t, "DENY", resp.header.Get("X-Frame-Options"))
		})
	}
}

func TestRequestIDIsEchoedOrReplaced(t *testing.T) {
	s := newStack(t, stackOptions{})

	echoed := s.get(
		t, "/v1/node/info", withHeader("X-Request-Id", "req-123.a_b"),
	)
	assert.Equal(t, "req-123.a_b", echoed.header.Get("X-Request-Id"))

	for name, supplied := range map[string]string{
		"spaces":   "bad id",
		"newline":  "bad\tid",
		"quote":    `bad"id`,
		"too long": strings.Repeat("a", 129),
	} {
		t.Run(name, func(t *testing.T) {
			resp := s.get(
				t, "/v1/node/info", withHeader("X-Request-Id", supplied),
			)

			got := resp.header.Get("X-Request-Id")
			assert.NotEqual(t, supplied, got)
			assert.Len(t, got, 36, "a generated uuid")
		})
	}

	generated := s.get(t, "/v1/node/info")
	assert.Len(t, generated.header.Get("X-Request-Id"), 36)
}

func TestUnroutedRequests(t *testing.T) {
	s := newStack(t, stackOptions{})

	testCases := []struct {
		name       string
		method     string
		path       string
		wantStatus int
	}{
		{
			"unknown top-level path", http.MethodGet, "/nope",
			http.StatusNotFound,
		},
		{"unknown api path", http.MethodGet, "/v1/nope", http.StatusNotFound},
		{
			"unknown nested api path", http.MethodGet,
			"/v1/node/nope/deeper", http.StatusNotFound,
		},
		{
			"wrong method on a read endpoint", http.MethodPost,
			"/v1/node/info", http.StatusMethodNotAllowed,
		},
		{
			"wrong method on a write endpoint", http.MethodGet,
			"/v1/node/regtest/generate-blocks", http.StatusMethodNotAllowed,
		},
		{
			"delete on the wallet collection", http.MethodDelete,
			"/v1/wallets", http.StatusMethodNotAllowed,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resp := s.send(t, tc.method, tc.path, "")

			requireStatus(t, resp, tc.wantStatus)

			if strings.HasPrefix(tc.path, APIBasePath+"/") {
				assert.Equal(
					t, "application/json", resp.header.Get("Content-Type"),
				)
				envelope := decodeAs[aichteeteapee.ErrorResponse](t, resp)
				assert.Equal(t,
					aichteeteapee.ErrorCodeFromHTTPStatus(tc.wantStatus),
					envelope.Code,
				)
			}

			if tc.wantStatus == http.StatusMethodNotAllowed {
				assert.NotEmpty(t, resp.header.Get("Allow"))
			}
		})
	}
}

func TestUnmatchedPathRedirectsArePreserved(t *testing.T) {
	s := newStack(t, stackOptions{})
	resp := s.get(t, "/v1/nope//deeper")

	requireStatus(t, resp, http.StatusTemporaryRedirect)
	assert.Equal(t, "/v1/nope/deeper", resp.header.Get("Location"))
	assert.Contains(t, resp.text(), "/v1/nope/deeper")
}

func bodyOfSize(t *testing.T, size int) string {
	t.Helper()

	const frame = `{"name":"a","password":""}`

	require.GreaterOrEqual(t, size, len(frame))

	return `{"name":"a","password":"` +
		strings.Repeat("p", size-len(frame)) + `"}`
}

func TestRequestBodyLimit(t *testing.T) {
	const limit = 80

	t.Run("a body of exactly the limit is accepted", func(t *testing.T) {
		s := newStack(t, stackOptions{maxRequestBytes: limit})

		body := bodyOfSize(t, limit)
		require.Len(t, body, limit)

		requireStatus(t, s.post(t, "/v1/wallets", body), http.StatusCreated)
	})

	t.Run("one byte over is refused", func(t *testing.T) {
		s := newStack(t, stackOptions{maxRequestBytes: limit})

		body := bodyOfSize(t, limit+1)
		require.Len(t, body, limit+1)

		resp := s.post(t, "/v1/wallets", body)
		requireStatus(t, resp, http.StatusRequestEntityTooLarge)

		envelope := decodeAs[aichteeteapee.ErrorResponse](t, resp)
		assert.Equal(t, "the request body is too large", envelope.Message)
		assert.Equal(t, "PAYLOAD_TOO_LARGE", envelope.Code)
		assert.Zero(t, s.pool.OpenCount(), "nothing was created")
		assert.Zero(t, s.wallets.Called("create_wallet"))
	})

	t.Run("a huge body is refused", func(t *testing.T) {
		s := newStack(t, stackOptions{maxRequestBytes: limit})

		resp := s.post(t, "/v1/node/regtest/generate-blocks",
			`{"count":1,"address":"`+strings.Repeat("a", 1<<16)+`"}`)

		requireStatus(t, resp, http.StatusRequestEntityTooLarge)
		assert.Empty(t, s.daemon.paramsFor("generateblocks"))
	})

	t.Run("the mcp endpoint shares the limit", func(t *testing.T) {
		s := newStack(t, stackOptions{maxRequestBytes: limit})

		resp := s.post(
			t, "/mcp",
			`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"pad":"`+
				strings.Repeat("a", limit)+`"}}`,
			withHeader("Accept", mcpAccept),
		)

		requireStatus(t, resp, http.StatusRequestEntityTooLarge)
	})
}

func TestMalformedRequestsAreAnsweredWithoutEchoingTheCause(t *testing.T) {
	s := newStack(t, stackOptions{})

	resp := s.post(t, "/v1/wallets", `{"name": nope}`)

	requireError(t, resp, http.StatusBadRequest, codeBadRequest, messageDecode)
	assert.NotContains(t, resp.text(), "invalid character")
	assert.NotContains(t, resp.text(), "nope")

	resp = s.get(t, "/v1/node/blocks/height/abc")

	requireError(t, resp, http.StatusBadRequest, codeBadRequest, messageDecode)
	assert.NotContains(t, resp.text(), "abc")
}

// counterValue returns the HTTP request count for matching labels.
func counterValue(
	t *testing.T,
	s *stack,
	labels map[string]string,
) float64 {
	t.Helper()

	const family = "monero_api_http_requests_total"

	for _, metric := range gatherFamily(t, s, family).GetMetric() {
		if sameLabels(metric, labels) {
			return metric.GetCounter().GetValue()
		}
	}

	return 0
}

func gatherFamily(t *testing.T, s *stack, name string) *dto.MetricFamily {
	t.Helper()

	families, err := s.metrics.Registry().Gather()
	require.NoError(t, err)

	for _, family := range families {
		if family.GetName() == name {
			return family
		}
	}

	return &dto.MetricFamily{}
}

func sameLabels(metric *dto.Metric, want map[string]string) bool {
	got := make(map[string]string, len(metric.GetLabel()))
	for _, pair := range metric.GetLabel() {
		got[pair.GetName()] = pair.GetValue()
	}

	return assert.ObjectsAreEqual(want, got)
}

func httpLabels(method, route, status string) map[string]string {
	return map[string]string{"method": method, "route": route, "status": status}
}

func TestRequestsAreCountedAgainstTheRouteTemplate(t *testing.T) {
	s := newStack(t, stackOptions{})
	s.createWallet(t, "alice")

	requireStatus(t, s.get(t, "/v1/node/info"), http.StatusOK)
	requireStatus(t, s.get(t, "/v1/node/info"), http.StatusOK)
	requireStatus(t, s.get(t, "/v1/node/blocks/height/7"), http.StatusOK)
	requireStatus(t, s.get(t, "/v1/node/blocks/height/9"), http.StatusOK)
	requireStatus(
		t, s.get(t, "/v1/node/blocks/height/-1"), http.StatusBadRequest,
	)

	requireStatus(t, s.post(t, "/v1/wallets/alice/close", ""), http.StatusOK)

	for _, name := range []string{"ghost", "other"} {
		requireStatus(
			t, s.post(t, "/v1/wallets/"+name+"/close", ""), http.StatusConflict,
		)
	}

	const requests = "monero_api_http_requests_total"

	want := map[string]float64{
		"GET /v1/node/info:200":                   2,
		"GET /v1/node/blocks/height/{height}:200": 2,
		"GET /v1/node/blocks/height/{height}:400": 1,
		"POST /v1/wallets:201":                    1,
		"POST /v1/wallets/{walletName}/close:200": 1,
		"POST /v1/wallets/{walletName}/close:409": 2,
	}

	for key, count := range want {
		route, status, _ := strings.Cut(key, ":")
		method, _, _ := strings.Cut(route, " ")

		assert.InDelta(
			t, count,
			counterValue(
				t, s, httpLabels(method, route, status),
			), 0, key,
		)
	}

	for _, metric := range gatherFamily(t, s, requests).GetMetric() {
		for _, pair := range metric.GetLabel() {
			assert.NotContains(t, pair.GetValue(), "alice")
			assert.NotContains(t, pair.GetValue(), "ghost")
			assert.NotContains(t, pair.GetValue(), "other")
		}
	}
}

func TestRequestsRejectedBeforeAnOperationAreCounted(t *testing.T) {
	s := newStack(t, stackOptions{})

	testCases := []struct {
		method string
		path   string
		status int
		route  string
		mod    requestMod
	}{
		{
			http.MethodGet, "/v1/node/info", http.StatusUnauthorized,
			apiMountPattern, withoutAuth(),
		},
		{
			http.MethodGet, "/v1/unknown-unique-path", http.StatusNotFound,
			routeUnmatched, withAuth("Bearer " + testAPIToken),
		},
		{
			http.MethodPost, "/v1/node/info", http.StatusMethodNotAllowed,
			routeUnmatched, withAuth("Bearer " + testAPIToken),
		},
	}

	for _, tc := range testCases {
		requireStatus(t, s.send(t, tc.method, tc.path, "", tc.mod), tc.status)
		assert.InDelta(t, 1, counterValue(
			t, s,
			httpLabels(tc.method, tc.route, fmt.Sprint(tc.status)),
		), 0)
	}
}

func TestRequestDurationsAreObservedPerRoute(t *testing.T) {
	s := newStack(t, stackOptions{})

	for range 3 {
		requireStatus(t, s.get(t, "/v1/node/fee-estimate"), http.StatusOK)
	}

	family := gatherFamily(t, s, "monero_api_http_request_duration_seconds")

	for _, metric := range family.GetMetric() {
		labels := map[string]string{}
		for _, pair := range metric.GetLabel() {
			labels[pair.GetName()] = pair.GetValue()
		}

		if labels["route"] != "GET /v1/node/fee-estimate" {
			continue
		}

		assert.Equal(t, "GET", labels["method"])
		assert.EqualValues(t, 3, metric.GetHistogram().GetSampleCount())

		return
	}

	assert.Fail(t, "no duration series for the fee-estimate route")
}

func TestInFlightGaugeReturnsToZero(t *testing.T) {
	s := newStack(t, stackOptions{})

	const requests = 40

	statuses := make([]int, requests)

	var wg sync.WaitGroup

	for i := range requests {
		wg.Go(func() {
			statuses[i] = s.statusOf(t, http.MethodGet, "/v1/node/info", "")
		})
	}

	wg.Wait()

	for _, status := range statuses {
		assert.Equal(t, http.StatusOK, status)
	}

	assert.InDelta(
		t, requests,
		counterValue(
			t, s,
			httpLabels(http.MethodGet, "GET /v1/node/info", "200"),
		), 0,
	)

	family := gatherFamily(t, s, "monero_api_http_requests_in_flight")
	require.Len(t, family.GetMetric(), 1)
	assert.InDelta(t, 0, family.GetMetric()[0].GetGauge().GetValue(), 0)
}

func TestRouterServesWithoutMetrics(t *testing.T) {
	s := newStack(t, stackOptions{withoutMetrics: true})

	requireStatus(t, s.get(t, "/v1/node/info"), http.StatusOK)

	resp := s.post(
		t, "/mcp", mcpToolsList, withHeader("Accept", mcpAccept),
	)
	requireStatus(t, resp, http.StatusOK)
}

const (
	mcpAccept    = "application/json, text/event-stream"
	mcpToolsList = `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
)

type mcpEnvelope struct {
	Result struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
		IsError           bool            `json:"isError"`
		StructuredContent json.RawMessage `json:"structuredContent"`
		Content           []struct {
			Text string `json:"text"`
		} `json:"content"`
	} `json:"result"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func TestMCPEndpointServesBothPathForms(t *testing.T) {
	s := newStack(t, stackOptions{})

	for _, path := range []string{"/mcp", "/mcp/"} {
		t.Run(path, func(t *testing.T) {
			resp := s.post(
				t, path, mcpToolsList, withHeader("Accept", mcpAccept),
			)

			requireStatus(t, resp, http.StatusOK)

			envelope := decodeAs[mcpEnvelope](t, resp)
			names := make([]string, 0, len(envelope.Result.Tools))

			for _, tool := range envelope.Result.Tools {
				names = append(names, tool.Name)
			}

			assert.Contains(t, names, "monero_node_info")
			assert.Contains(t, names, "monero_transfer")
			assert.Contains(t, names, "monero_generate_blocks")
		})
	}
}

func TestMCPToolCallThroughTheRouter(t *testing.T) {
	s := newStack(t, stackOptions{})

	resp := s.post(
		t, "/mcp",
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":`+
			`{"name":"monero_node_info","arguments":{}}}`,
		withHeader("Accept", mcpAccept),
	)
	requireStatus(t, resp, http.StatusOK)

	envelope := decodeAs[mcpEnvelope](t, resp)
	require.False(t, envelope.Result.IsError, resp.text())

	var info struct {
		Height  int64  `json:"height"`
		Network string `json:"network"`
	}

	require.NoError(t, json.Unmarshal(envelope.Result.StructuredContent, &info))
	assert.EqualValues(t, 1000, info.Height)
	assert.Equal(t, "regtest", info.Network)
}

func TestMCPCallsAreObservedUnderTheMCPRoute(t *testing.T) {
	s := newStack(t, stackOptions{})

	for _, path := range []string{"/mcp", "/mcp/"} {
		requireStatus(t, s.post(
			t, path, mcpToolsList, withHeader("Accept", mcpAccept),
		), http.StatusOK)
	}

	assert.InDelta(
		t, 2,
		counterValue(
			t, s,
			httpLabels(http.MethodPost, "/mcp", "200"),
		), 0,
	)
}

func TestMCPToolErrorsStayInsideTheProtocol(t *testing.T) {
	s := newStack(t, stackOptions{})

	resp := s.post(
		t, "/mcp",
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":`+
			`{"name":"monero_get_block","arguments":{"height":-1}}}`,
		withHeader("Accept", mcpAccept),
	)
	requireStatus(t, resp, http.StatusOK)

	envelope := decodeAs[mcpEnvelope](t, resp)
	require.True(t, envelope.Result.IsError, resp.text())
	require.Len(t, envelope.Result.Content, 1)
	assert.Equal(
		t, "BAD_REQUEST: height -1 is negative",
		envelope.Result.Content[0].Text,
	)
}

func TestMCPEndpointRefusesWhatTheTransportDoesNot(t *testing.T) {
	s := newStack(t, stackOptions{})

	t.Run("GET is not a stateless operation", func(t *testing.T) {
		resp := s.get(t, "/mcp", withHeader("Accept", "text/event-stream"))

		requireStatus(t, resp, http.StatusMethodNotAllowed)
	})

	t.Run("Accept must list both media types", func(t *testing.T) {
		resp := s.post(
			t, "/mcp", mcpToolsList, withHeader("Accept", "application/json"),
		)

		requireStatus(t, resp, http.StatusBadRequest)
	})

	t.Run("a body that is not json rpc", func(t *testing.T) {
		resp := s.post(
			t, "/mcp", `{"hello":"world"}`, withHeader("Accept", mcpAccept),
		)

		assert.GreaterOrEqual(t, resp.status, http.StatusBadRequest)
		assert.NotEqual(t, http.StatusInternalServerError, resp.status)
	})
}

func TestWriteRequestError(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		err         error
		wantStatus  int
		wantMessage string
	}{
		{
			name:        "body over the limit",
			err:         &http.MaxBytesError{Limit: 10},
			wantStatus:  http.StatusRequestEntityTooLarge,
			wantMessage: "the request body is too large",
		},
		{
			name: "body over the limit, wrapped by the decoder",
			err: fmt.Errorf(
				"can't decode JSON body: %w", &http.MaxBytesError{Limit: 10},
			),
			wantStatus:  http.StatusRequestEntityTooLarge,
			wantMessage: "the request body is too large",
		},
		{
			name:        "anything else the binder refused",
			err:         errors.New("invalid character 'x' looking for value"),
			wantStatus:  http.StatusBadRequest,
			wantMessage: messageDecode,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequestWithContext(
				context.Background(), http.MethodPost, "/v1/wallets", nil,
			)
			recorder := httptest.NewRecorder()

			writeRequestError(recorder, req, tc.err)

			assert.Equal(t, tc.wantStatus, recorder.Code)
			assert.Equal(
				t, "application/json", recorder.Header().Get("Content-Type"),
			)

			var envelope aichteeteapee.ErrorResponse

			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
			assert.Equal(t, tc.wantMessage, envelope.Message)
			assert.NotContains(t, recorder.Body.String(), "invalid character")
		})
	}
}

func TestWriteResponseErrorNeverEchoesTheCause(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequestWithContext(
		context.Background(), http.MethodGet, "/v1/node/info", nil,
	)
	recorder := httptest.NewRecorder()

	writeResponseError(recorder, req, errors.New("secret detail at db.go:9"))

	assert.Equal(t, http.StatusInternalServerError, recorder.Code)
	assert.JSONEq(
		t, `{"code":"INTERNAL_SERVER_ERROR","message":"Internal server error"}`,
		recorder.Body.String(),
	)
	assert.NotContains(t, recorder.Body.String(), "secret")
}

func TestBodyLimitMiddleware(t *testing.T) {
	t.Parallel()

	readAll := func(readErr *error) http.Handler {
		return http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
			if req.Body == nil {
				return
			}

			_, *readErr = io.ReadAll(req.Body)
		})
	}

	t.Run("under the limit", func(t *testing.T) {
		t.Parallel()

		var readErr error

		req := httptest.NewRequestWithContext(
			context.Background(), http.MethodPost, "/",
			strings.NewReader("12345"),
		)

		BodyLimit(5)(readAll(&readErr)).ServeHTTP(httptest.NewRecorder(), req)

		require.NoError(t, readErr)
	})

	t.Run("over the limit", func(t *testing.T) {
		t.Parallel()

		var readErr error

		req := httptest.NewRequestWithContext(
			context.Background(), http.MethodPost, "/",
			strings.NewReader("123456"),
		)

		BodyLimit(5)(readAll(&readErr)).ServeHTTP(httptest.NewRecorder(), req)

		var tooLarge *http.MaxBytesError

		require.ErrorAs(t, readErr, &tooLarge)
		assert.EqualValues(t, 5, tooLarge.Limit)
	})

	t.Run("a request without a body passes through", func(t *testing.T) {
		t.Parallel()

		var called bool

		req := httptest.NewRequestWithContext(
			context.Background(), http.MethodGet, "/", nil,
		)
		req.Body = nil

		handle := func(_ http.ResponseWriter, got *http.Request) {
			called = true

			assert.Nil(t, got.Body)
		}

		BodyLimit(5)(http.HandlerFunc(handle)).
			ServeHTTP(httptest.NewRecorder(), req)

		assert.True(t, called)
	})
}

func TestRouteTemplate(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequestWithContext(
		context.Background(), http.MethodGet, "/", nil,
	)
	assert.Equal(t, "unmatched", routeTemplate(req))

	req.Pattern = "GET /v1/wallets/{walletName}/status"
	assert.Equal(t, "GET /v1/wallets/{walletName}/status", routeTemplate(req))
}

// failingWriter fails every body write.
type failingWriter struct {
	http.ResponseWriter
}

var errWriteRefused = errors.New("write refused")

func (failingWriter) Write([]byte) (int, error) { return 0, errWriteRefused }

func TestStatusRecorder(t *testing.T) {
	t.Parallel()

	t.Run("no write means 200", func(t *testing.T) {
		t.Parallel()

		recorder := &statusRecorder{ResponseWriter: httptest.NewRecorder()}

		assert.Equal(t, http.StatusOK, recorder.statusOrOK())
	})

	t.Run("an implicit header is 200", func(t *testing.T) {
		t.Parallel()

		inner := httptest.NewRecorder()
		recorder := &statusRecorder{ResponseWriter: inner}

		written, err := recorder.Write([]byte("hello"))
		require.NoError(t, err)

		assert.Equal(t, 5, written)
		assert.Equal(t, http.StatusOK, recorder.statusOrOK())
		assert.Equal(t, "hello", inner.Body.String())
	})

	t.Run("an explicit header is kept across writes", func(t *testing.T) {
		t.Parallel()

		inner := httptest.NewRecorder()
		recorder := &statusRecorder{ResponseWriter: inner}

		recorder.WriteHeader(http.StatusTeapot)

		_, err := recorder.Write([]byte("short and stout"))
		require.NoError(t, err)

		assert.Equal(t, http.StatusTeapot, recorder.statusOrOK())
		assert.Equal(t, http.StatusTeapot, inner.Code)
	})

	t.Run("a write error reaches the caller unchanged", func(t *testing.T) {
		t.Parallel()

		recorder := &statusRecorder{
			ResponseWriter: failingWriter{httptest.NewRecorder()},
		}

		written, err := recorder.Write([]byte("x"))

		require.ErrorIs(t, err, errWriteRefused)
		assert.Zero(t, written)
	})

	t.Run("flush reaches a flushing writer", func(t *testing.T) {
		t.Parallel()

		inner := httptest.NewRecorder()
		recorder := &statusRecorder{ResponseWriter: inner}

		recorder.Flush()

		assert.True(t, inner.Flushed)
	})

	t.Run("flush on a writer that cannot flush is a no-op", func(t *testing.T) {
		t.Parallel()

		recorder := &statusRecorder{
			ResponseWriter: failingWriter{httptest.NewRecorder()},
		}

		assert.NotPanics(t, recorder.Flush)
	})
}

package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	dto "github.com/prometheus/client_model/go"
	"github.com/psyb0t/docker-monero/internal/pkg/config"
	corenode "github.com/psyb0t/docker-monero/internal/pkg/core/node"
	"github.com/psyb0t/docker-monero/internal/pkg/http/api"
	"github.com/psyb0t/docker-monero/internal/pkg/monero/daemonrpc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServerIdentityAndInstructions(t *testing.T) {
	t.Parallel()

	f := newFixture(t, fixtureOptions{})
	init := connect(t, f.newServer(t, allTools)).InitializeResult()

	require.NotNil(t, init.ServerInfo)

	assert.Equal(t, "docker-monero", init.ServerInfo.Name)
	assert.Equal(t, "Monero node and wallets", init.ServerInfo.Title)
	assert.Equal(t, api.SpecVersion, init.ServerInfo.Version)
	assert.Contains(t, init.Instructions, "atomic units")
	assert.Contains(t, init.Instructions, "monero_wallet_status")
	require.NotNil(t, init.Capabilities)
	assert.NotNil(t, init.Capabilities.Tools)
}

func TestNewServerWorksWithoutARecorder(t *testing.T) {
	t.Parallel()

	f := newFixture(t, fixtureOptions{})

	server, err := NewServer(f.node, f.service, nil, allTools)
	require.NoError(t, err)

	session := connect(t, server)

	assert.False(t, call(t, session, "monero_node_info", nil).IsError)
	requireToolError(
		t, call(t, session, "monero_get_block", map[string]any{"height": -1}),
		"BAD_REQUEST: height -1 is negative",
	)
}

type chanOutput = chan int

func TestAddReportsAnOutputTypeItCannotDescribe(t *testing.T) {
	t.Parallel()

	f := newFixture(t, fixtureOptions{})
	server := mcpsdk.NewServer(
		&mcpsdk.Implementation{Name: "x", Version: "1"}, nil,
	)

	err := add(
		server, f.metrics, "broken", "a tool that cannot be described",
		func(context.Context, Empty) (chanOutput, error) {
			return make(chanOutput), nil
		},
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "tool broken")
	assert.Contains(t, err.Error(), "derive the tool output schema")
}

func toolCalls(
	t *testing.T,
	f *fixture,
	tool, outcome string,
) float64 {
	t.Helper()

	families, err := f.metrics.Registry().Gather()
	require.NoError(t, err)

	for _, family := range families {
		if family.GetName() != "monero_api_mcp_tool_calls_total" {
			continue
		}

		for _, metric := range family.GetMetric() {
			if hasLabels(metric, map[string]string{
				"tool": tool, "outcome": outcome,
			}) {
				return metric.GetCounter().GetValue()
			}
		}
	}

	return 0
}

func hasLabels(metric *dto.Metric, want map[string]string) bool {
	got := make(map[string]string, len(metric.GetLabel()))
	for _, pair := range metric.GetLabel() {
		got[pair.GetName()] = pair.GetValue()
	}

	return assert.ObjectsAreEqual(want, got)
}

func TestToolCallsAreCountedByOutcome(t *testing.T) {
	t.Parallel()

	f := newFixture(t, fixtureOptions{})
	session := f.session(t)

	for range 3 {
		call(t, session, "monero_node_info", nil)
	}

	call(t, session, "monero_get_block", map[string]any{"height": -1})
	call(t, session, "monero_get_block", map[string]any{"hash": "short"})
	call(t, session, "monero_get_block", nil)

	assert.InDelta(t, 3, toolCalls(t, f, "monero_node_info", "success"), 0)
	assert.InDelta(t, 0, toolCalls(t, f, "monero_node_info", "error"), 0)
	assert.InDelta(t, 1, toolCalls(t, f, "monero_get_block", "success"), 0)
	assert.InDelta(t, 2, toolCalls(t, f, "monero_get_block", "error"), 0)
	assert.InDelta(t, 0, toolCalls(t, f, "monero_fee_estimate", "success"), 0)
}

func TestToolDurationsAreObserved(t *testing.T) {
	t.Parallel()

	f := newFixture(t, fixtureOptions{})
	session := f.session(t)

	call(t, session, "monero_fee_estimate", nil)
	call(t, session, "monero_fee_estimate", nil)

	families, err := f.metrics.Registry().Gather()
	require.NoError(t, err)

	for _, family := range families {
		if family.GetName() != "monero_api_mcp_tool_call_duration_seconds" {
			continue
		}

		require.Len(t, family.GetMetric(), 1)
		assert.True(t, hasLabels(family.GetMetric()[0], map[string]string{
			"tool": "monero_fee_estimate",
		}))
		assert.EqualValues(
			t, 2, family.GetMetric()[0].GetHistogram().GetSampleCount(),
		)

		return
	}

	assert.Fail(t, "no duration series")
}

const (
	mcpAccept    = "application/json, text/event-stream"
	mcpToolsList = `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	mcpNodeInfo  = `{"jsonrpc":"2.0","id":2,"method":"tools/call",` +
		`"params":{"name":"monero_node_info","arguments":{}}}`
)

type httpReply struct {
	status int
	header http.Header
	body   []byte
}

func (r httpReply) text() string { return string(r.body) }

type httpMod func(req *http.Request)

func serveHandler(t *testing.T, handler http.Handler) string {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	return server.URL
}

func send(
	t *testing.T,
	method, url, body string,
	mods ...httpMod,
) httpReply {
	t.Helper()

	req, err := http.NewRequestWithContext(
		context.Background(), method, url, strings.NewReader(body),
	)
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", mcpAccept)

	for _, mod := range mods {
		mod(req)
	}

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	defer func() { assert.NoError(t, resp.Body.Close()) }()

	payload, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return httpReply{
		status: resp.StatusCode, header: resp.Header, body: payload,
	}
}

func withHeader(name, value string) httpMod {
	return func(req *http.Request) { req.Header.Set(name, value) }
}

type rpcReply struct {
	Result struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
		IsError           bool            `json:"isError"`
		StructuredContent json.RawMessage `json:"structuredContent"`
	} `json:"result"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func decodeRPC(t *testing.T, reply httpReply) rpcReply {
	t.Helper()

	var decoded rpcReply

	require.NoError(t, json.Unmarshal(reply.body, &decoded), reply.text())

	return decoded
}

func TestHandlerAnswersStatelessJSONRPC(t *testing.T) {
	t.Parallel()

	f := newFixture(t, fixtureOptions{})

	handler, err := Handler(f.node, f.service, f.metrics, allTools)
	require.NoError(t, err)

	url := serveHandler(t, handler)

	listed := send(t, http.MethodPost, url, mcpToolsList)
	require.Equal(t, http.StatusOK, listed.status, listed.text())
	assert.Equal(t, "application/json", listed.header.Get("Content-Type"))
	assert.Empty(t, listed.header.Get("Mcp-Session-Id"), "stateless")
	assert.Len(t, decodeRPC(t, listed).Result.Tools, 15)

	called := send(t, http.MethodPost, url, mcpNodeInfo)
	require.Equal(t, http.StatusOK, called.status, called.text())

	reply := decodeRPC(t, called)
	require.False(t, reply.Result.IsError, called.text())

	var info struct {
		Height  int64  `json:"height"`
		Network string `json:"network"`
	}

	require.NoError(t, json.Unmarshal(reply.Result.StructuredContent, &info))
	assert.EqualValues(t, nodeHeight, info.Height)
	assert.Equal(t, "regtest", info.Network)
}

func TestHandlerHonoursTheDeploymentOptions(t *testing.T) {
	t.Parallel()

	f := newFixture(t, fixtureOptions{readOnly: true})

	handler, err := Handler(
		f.node, f.service, f.metrics, Options{ReadOnly: true},
	)
	require.NoError(t, err)

	listed := send(t, http.MethodPost, serveHandler(t, handler), mcpToolsList)
	require.Equal(t, http.StatusOK, listed.status, listed.text())

	tools := decodeRPC(t, listed).Result.Tools

	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}

	assert.ElementsMatch(t, readOnlyTools, names)
}

func TestHandlerRefusesWhatTheTransportDoesNot(t *testing.T) {
	t.Parallel()

	f := newFixture(t, fixtureOptions{})

	handler, err := Handler(f.node, f.service, f.metrics, allTools)
	require.NoError(t, err)

	url := serveHandler(t, handler)

	testCases := []struct {
		name       string
		method     string
		body       string
		mods       []httpMod
		wantStatus int
	}{
		{
			name:       "accept lists only json",
			method:     http.MethodPost,
			body:       mcpToolsList,
			mods:       []httpMod{withHeader("Accept", "application/json")},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "accept lists only event streams",
			method:     http.MethodPost,
			body:       mcpToolsList,
			mods:       []httpMod{withHeader("Accept", "text/event-stream")},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "no accept header",
			method:     http.MethodPost,
			body:       mcpToolsList,
			mods:       []httpMod{withHeader("Accept", "")},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "content type is not json",
			method:     http.MethodPost,
			body:       mcpToolsList,
			mods:       []httpMod{withHeader("Content-Type", "text/plain")},
			wantStatus: http.StatusUnsupportedMediaType,
		},
		{
			name:       "get has no stream to open",
			method:     http.MethodGet,
			wantStatus: http.StatusMethodNotAllowed,
		},
		{
			name:       "delete has no session to end",
			method:     http.MethodDelete,
			wantStatus: http.StatusMethodNotAllowed,
		},
		{
			name:   "unsupported protocol version",
			method: http.MethodPost,
			body:   mcpToolsList,
			mods: []httpMod{
				withHeader("MCP-Protocol-Version", "1999-01-01"),
			},
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			reply := send(t, tc.method, url, tc.body, tc.mods...)

			assert.Equal(t, tc.wantStatus, reply.status, reply.text())
		})
	}
}

func TestHandlerBoundsTheRequestBody(t *testing.T) {
	t.Parallel()

	padded := func(size int) string {
		return `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":` +
			`{"pad":"` + strings.Repeat("a", size) + `"}}`
	}

	t.Run("over the configured limit", func(t *testing.T) {
		t.Parallel()

		f := newFixture(t, fixtureOptions{})

		handler, err := Handler(
			f.node, f.service, f.metrics,
			Options{MaxRequestBytes: 200},
		)
		require.NoError(t, err)

		url := serveHandler(t, handler)

		assert.Equal(
			t, http.StatusOK, send(t, http.MethodPost, url, padded(10)).status,
		)
		assert.Equal(
			t, http.StatusRequestEntityTooLarge,
			send(t, http.MethodPost, url, padded(500)).status,
		)
	})

	t.Run("an unset limit falls back to the SDK default", func(t *testing.T) {
		t.Parallel()

		f := newFixture(t, fixtureOptions{})

		handler, err := Handler(f.node, f.service, f.metrics, Options{})
		require.NoError(t, err)

		reply := send(
			t, http.MethodPost, serveHandler(t, handler), padded(100_000),
		)

		assert.Equal(t, http.StatusOK, reply.status, reply.text())
	})
}

type requestKey struct{}

// contextDaemon records what the request context carried into the node
// service.
type contextDaemon struct {
	*daemonrpc.Client

	seen atomic.Value
}

func (d *contextDaemon) GetInfo(ctx context.Context) (daemonrpc.Info, error) {
	value, _ := ctx.Value(requestKey{}).(string)
	d.seen.Store(value)

	return d.Client.GetInfo(ctx)
}

func TestHandlerCarriesTheRequestContextIntoTools(t *testing.T) {
	t.Parallel()

	f := newFixture(t, fixtureOptions{})

	recording := &contextDaemon{
		Client: daemonrpc.New(f.daemon.server.URL, defaultRPCTimeout),
	}
	node := corenode.New(recording, corenode.Options{
		Network:  config.NetworkRegtest,
		Restarts: func() uint64 { return 0 },
	})

	handler, err := Handler(node, f.service, f.metrics, allTools)
	require.NoError(t, err)

	scoped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(
			r.Context(), requestKey{}, r.Header.Get("X-Marker"),
		)

		handler.ServeHTTP(w, r.WithContext(ctx))
	})

	url := serveHandler(t, scoped)

	for _, marker := range []string{"first-request", "second-request"} {
		reply := send(
			t, http.MethodPost, url, mcpNodeInfo,
			withHeader("X-Marker", marker),
		)

		require.Equal(t, http.StatusOK, reply.status, reply.text())
		assert.Equal(t, marker, recording.seen.Load())
	}
}

func TestHandlerServesARealMCPClient(t *testing.T) {
	t.Parallel()

	f := newFixture(t, fixtureOptions{})

	handler, err := Handler(f.node, f.service, f.metrics, allTools)
	require.NoError(t, err)

	client := mcpsdk.NewClient(
		&mcpsdk.Implementation{Name: "http-client", Version: "1"}, nil,
	)

	session, err := client.Connect(
		context.Background(),
		&mcpsdk.StreamableClientTransport{Endpoint: serveHandler(t, handler)},
		nil,
	)
	require.NoError(t, err)

	t.Cleanup(func() { assert.NoError(t, session.Close()) })

	assert.Len(t, toolNames(t, session), 15)

	info := callAs[api.NodeInfo](t, session, "monero_node_info", nil)
	assert.EqualValues(t, nodeHeight, info.Height)

	requireToolError(
		t, call(t, session, "monero_get_block", map[string]any{"height": -1}),
		"BAD_REQUEST: height -1 is negative",
	)
}

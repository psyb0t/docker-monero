//go:build integration

package integration

import (
	"net/http"
	"sort"
	"testing"

	"github.com/psyb0t/docker-monero/tests/testinfra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mcpCall sends one stateless JSON-RPC request to /mcp.
func mcpCall(
	t *testing.T,
	app *testinfra.App,
	path, method string,
	params map[string]any,
) map[string]any {
	t.Helper()

	request := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
	if params != nil {
		request["params"] = params
	}

	status, body := rawRequest(t, app, http.MethodPost, path, request, true)
	require.Equal(t, http.StatusOK, status, body)

	return body
}

// toolNames lists the tools /mcp advertises, sorted.
func toolNames(t *testing.T, app *testinfra.App, path string) []string {
	t.Helper()

	body := mcpCall(t, app, path, "tools/list", nil)

	result, ok := body["result"].(map[string]any)
	require.True(t, ok, body)

	tools, ok := result["tools"].([]any)
	require.True(t, ok, body)

	names := make([]string, 0, len(tools))

	for _, tool := range tools {
		entry, _ := tool.(map[string]any)
		name, _ := entry["name"].(string)
		names = append(names, name)
	}

	sort.Strings(names)

	return names
}

func TestMCPToolList(t *testing.T) {
	names := toolNames(t, sharedApp, "/mcp")

	assert.Equal(t, []string{
		"monero_close_wallet",
		"monero_create_address",
		"monero_create_wallet",
		"monero_fee_estimate",
		"monero_generate_blocks",
		"monero_get_block",
		"monero_get_transaction",
		"monero_list_wallets",
		"monero_node_info",
		"monero_open_wallet",
		"monero_wallet_addresses",
		"monero_wallet_balance",
		"monero_wallet_status",
		"monero_wallet_transfers",
	}, names, "transfers stay off MCP unless the deployment opts in")

	// The trailing-slash form must answer directly; a redirected POST
	// would lose its body.
	assert.Equal(t, names, toolNames(t, sharedApp, "/mcp/"))
}

func TestMCPToolCalls(t *testing.T) {
	body := mcpCall(t, sharedApp, "/mcp", "tools/call", map[string]any{
		"name":      "monero_create_wallet",
		"arguments": map[string]any{"name": "mcp-made", "password": "pw"},
	})

	result, _ := body["result"].(map[string]any)
	require.NotEqual(t, true, result["isError"], body)

	structured, _ := result["structuredContent"].(map[string]any)
	assert.Equal(t, "mcp-made", structured["name"])
	assert.Equal(t, true, structured["open"])

	body = mcpCall(t, sharedApp, "/mcp", "tools/call", map[string]any{
		"name":      "monero_wallet_balance",
		"arguments": map[string]any{"name": "never-created"},
	})

	result, _ = body["result"].(map[string]any)
	assert.Equal(t, true, result["isError"], body)

	content, _ := result["content"].([]any)
	require.NotEmpty(t, content)

	first, _ := content[0].(map[string]any)
	assert.Equal(t, "WALLET_NOT_OPEN: the wallet is not open", first["text"],
		"tool errors carry the mapped code, never internal error text")

	body = mcpCall(t, sharedApp, "/mcp", "tools/call", map[string]any{
		"name":      "monero_node_info",
		"arguments": map[string]any{},
	})

	result, _ = body["result"].(map[string]any)
	structured, _ = result["structuredContent"].(map[string]any)
	assert.Equal(t, "regtest", structured["network"])
}

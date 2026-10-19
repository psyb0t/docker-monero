// Package mcp exposes the node and wallet services over the Model Context
// Protocol.
//
// It is a transport, nothing more. Every tool converts its typed input into
// the same core service call the REST handlers make and returns the same
// generated API types, so an agent and a REST client see the same behavior.
//
// Deliberate gaps: wallet restore is not offered (a seed or spend key should
// not pass through an agent's context), the transfer tool is registered only
// when the deployment opts in, and read-only mode registers no write tool.
package mcp

import (
	"context"
	"net/http"
	"reflect"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/docker-monero/internal/pkg/apierrors"
	corenode "github.com/psyb0t/docker-monero/internal/pkg/core/node"
	corewallets "github.com/psyb0t/docker-monero/internal/pkg/core/wallets"
	"github.com/psyb0t/docker-monero/internal/pkg/http/api"
	"github.com/psyb0t/docker-monero/internal/pkg/metrics"
)

// Server identity reported during initialize.
const (
	ServerName  = "docker-monero"
	ServerTitle = "Monero node and wallets"
)

// Tool names. They are the protocol's public surface.
const (
	ToolNodeInfo        = "monero_node_info"
	ToolGetBlock        = "monero_get_block"
	ToolGetTransaction  = "monero_get_transaction"
	ToolFeeEstimate     = "monero_fee_estimate"
	ToolGenerateBlocks  = "monero_generate_blocks"
	ToolListWallets     = "monero_list_wallets"
	ToolCreateWallet    = "monero_create_wallet"
	ToolOpenWallet      = "monero_open_wallet"
	ToolCloseWallet     = "monero_close_wallet"
	ToolWalletStatus    = "monero_wallet_status"
	ToolWalletBalance   = "monero_wallet_balance"
	ToolWalletAddresses = "monero_wallet_addresses"
	ToolCreateAddress   = "monero_create_address"
	ToolWalletTransfers = "monero_wallet_transfers"
	ToolTransfer        = "monero_transfer"
)

const instructions = "Read a Monero node's state and manage wallets on it. " +
	"Amounts are strings of atomic units (1 XMR = 10^12). A wallet must be " +
	"open before its balance, addresses, or transfers can be read; open it " +
	"with its password and close it when done. Check monero_wallet_status " +
	"before sending: a wallet that is still syncing refuses to send."

// Options controls which optional tools are registered.
type Options struct {
	// ReadOnly registers only the tools that read. It wins over the rest.
	ReadOnly bool

	// AllowTransfers registers monero_transfer.
	AllowTransfers bool

	// Regtest registers monero_generate_blocks.
	Regtest bool

	// MaxRequestBytes bounds one JSON-RPC request body.
	MaxRequestBytes int64
}

// Handler builds the Streamable HTTP handler to mount at /mcp.
//
// The transport runs stateless: each POST is a complete exchange, so the
// request-scoped context the HTTP middleware sets reaches every tool call.
func Handler(
	node *corenode.Service,
	wallets *corewallets.Service,
	recorder *metrics.Metrics,
	options Options,
) (http.Handler, error) {
	server, err := NewServer(node, wallets, recorder, options)
	if err != nil {
		return nil, err
	}

	return mcpsdk.NewStreamableHTTPHandler(
		func(*http.Request) *mcpsdk.Server { return server },
		&mcpsdk.StreamableHTTPOptions{
			Stateless:                    true,
			JSONResponse:                 true,
			MaxRequestBodyBytes:          options.MaxRequestBytes,
			PropagateRequestCancellation: true,
		},
	), nil
}

// NewServer builds the MCP server and registers every tool. It fails when an
// output schema cannot be derived, which depends only on the generated types
// and so surfaces at startup rather than on a call.
func NewServer(
	node *corenode.Service,
	wallets *corewallets.Service,
	recorder *metrics.Metrics,
	options Options,
) (*mcpsdk.Server, error) {
	server := mcpsdk.NewServer(
		&mcpsdk.Implementation{
			Name:    ServerName,
			Title:   ServerTitle,
			Version: api.SpecVersion,
		},
		&mcpsdk.ServerOptions{Instructions: instructions},
	)

	tools := &toolset{node: node, wallets: wallets, metrics: recorder}
	if err := tools.register(server, options); err != nil {
		return nil, err
	}

	return server, nil
}

// toolset holds what every tool needs.
type toolset struct {
	node    *corenode.Service
	wallets *corewallets.Service
	metrics *metrics.Metrics
}

// schemaFor derives an output schema with time.Time described as a string.
// The generated types carry timestamps as time.Time, which marshals to an
// RFC 3339 string; inferring "object" would make every result fail the
// SDK's own output validation.
func schemaFor[T any]() (*jsonschema.Schema, error) {
	schema, err := jsonschema.For[T](&jsonschema.ForOptions{
		TypeSchemas: map[reflect.Type]*jsonschema.Schema{
			reflect.TypeFor[time.Time](): {Type: "string", Format: "date-time"},
		},
	})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "derive the tool output schema")
	}

	return schema, nil
}

// add registers one tool with a derived output schema and instrumentation.
func add[In, Out any](
	server *mcpsdk.Server,
	recorder *metrics.Metrics,
	name, description string,
	handler func(context.Context, In) (Out, error),
) error {
	schema, err := schemaFor[Out]()
	if err != nil {
		return ctxerrors.Wrapf(err, "tool %s", name)
	}

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:         name,
		Description:  description,
		OutputSchema: schema,
	}, instrument(recorder, name, handler))

	return nil
}

// instrument wraps a tool with metrics and turns its error into the same
// client-safe text REST would return.
func instrument[In, Out any](
	recorder *metrics.Metrics,
	name string,
	handler func(context.Context, In) (Out, error),
) mcpsdk.ToolHandlerFor[In, Out] {
	return func(
		ctx context.Context,
		_ *mcpsdk.CallToolRequest,
		input In,
	) (*mcpsdk.CallToolResult, Out, error) {
		startedAt := time.Now()

		output, err := handler(ctx, input)

		outcome := metrics.OutcomeSuccess
		if err != nil {
			outcome = metrics.OutcomeError
		}

		if recorder != nil {
			recorder.ObserveMCPTool(
				name,
				outcome,
				time.Since(startedAt).Seconds(),
			)
		}

		if err != nil {
			mapped := apierrors.Classify(err)
			ctxscope.GetLogger(ctx).Warn(
				"mcp tool failed",
				"tool", name,
				"code", mapped.Body.Code,
				"err", err,
			)

			var zero Out

			return nil, zero, &toolError{message: mapped.Text()}
		}

		return nil, output, nil
	}
}

// toolError is the client-facing failure of a tool call. Its text is the
// mapped error code and message, never the internal error chain.
type toolError struct {
	message string
}

func (e *toolError) Error() string {
	return e.message
}

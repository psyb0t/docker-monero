// Package jsonrpc is the HTTP transport shared by the monerod and
// monero-wallet-rpc clients.
//
// Both daemons speak JSON-RPC 2.0 on /json_rpc, and monerod also serves
// plain JSON endpoints such as /get_transactions. A plain endpoint reports
// failure through a "status" field rather than an error object, so both call
// styles end in the same status check.
package jsonrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
)

// Wire constants.
const (
	pathJSONRPC   = "/json_rpc"
	versionJSON   = "2.0"
	requestID     = "0"
	contentType   = "application/json"
	statusOK      = "OK"
	statusBusy    = "BUSY"
	headerContent = "Content-Type"
)

// DefaultMaxResponseBytes caps a response body. A full block or a large
// transfer history is well under this; anything bigger is a fault.
const DefaultMaxResponseBytes = 64 << 20

// ErrRPC marks an error object returned by the remote daemon.
var ErrRPC = errors.New("rpc error")

// ErrStatus marks a plain-endpoint response whose status is not OK.
var ErrStatus = errors.New("rpc status not ok")

// Error is an error object returned by the daemon. It wraps ErrRPC so callers
// can test for "the daemon answered with an error" without inspecting codes.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error {
	return ErrRPC
}

// HasMessage reports whether the daemon's message contains text, ignoring
// case. Some wallet-rpc failures share a generic code and differ only in
// their message.
func (e *Error) HasMessage(text string) bool {
	return strings.Contains(strings.ToLower(e.Message), strings.ToLower(text))
}

// Client talks to one daemon base URL.
type Client struct {
	baseURL          string
	httpClient       *http.Client
	maxResponseBytes int64
}

// New builds a client for baseURL (scheme, host, and port, no path) with a
// per-request timeout.
func New(baseURL string, timeout time.Duration) *Client {
	return &Client{
		baseURL:          strings.TrimRight(baseURL, "/"),
		httpClient:       &http.Client{Timeout: timeout},
		maxResponseBytes: DefaultMaxResponseBytes,
	}
}

// BaseURL returns the daemon URL the client was built for.
func (c *Client) BaseURL() string {
	return c.baseURL
}

type request struct {
	JSONRPC string `json:"jsonrpc"`
	ID      string `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type response struct {
	Result json.RawMessage `json:"result"`
	Error  *Error          `json:"error"`
}

// Call invokes a JSON-RPC method and decodes its result into result. A nil
// result discards the payload.
func (c *Client) Call(
	ctx context.Context,
	method string,
	params any,
	result any,
) error {
	body := request{
		JSONRPC: versionJSON,
		ID:      requestID,
		Method:  method,
		Params:  params,
	}

	raw, err := c.post(ctx, pathJSONRPC, body)
	if err != nil {
		return ctxerrors.Wrapf(err, "call %s", method)
	}

	var envelope response
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return ctxerrors.Wrapf(
			commerr.ErrUnmarshalFailed, "decode %s envelope: %v", method, err,
		)
	}

	if envelope.Error != nil {
		return ctxerrors.Wrapf(envelope.Error, "call %s", method)
	}

	return decodeResult(method, envelope.Result, result)
}

// Post sends body to a plain endpoint such as /get_transactions and decodes
// the reply into result.
func (c *Client) Post(
	ctx context.Context,
	path string,
	body any,
	result any,
) error {
	raw, err := c.post(ctx, path, body)
	if err != nil {
		return ctxerrors.Wrapf(err, "post %s", path)
	}

	return decodeResult(path, raw, result)
}

// decodeResult checks the optional status field and decodes the payload.
func decodeResult(label string, raw json.RawMessage, result any) error {
	if len(raw) == 0 {
		return nil
	}

	var status struct {
		Status string `json:"status"`
	}

	// A result that is not an object (a bare string or number) has no
	// status to check, so a decode failure here is not an error.
	_ = json.Unmarshal(raw, &status)

	switch status.Status {
	case "", statusOK:
	case statusBusy:
		return ctxerrors.Wrapf(
			commerr.ErrNotReady, "%s: daemon is busy syncing", label,
		)
	default:
		return ctxerrors.Wrapf(ErrStatus, "%s: status %q", label, status.Status)
	}

	if result == nil {
		return nil
	}

	if err := json.Unmarshal(raw, result); err != nil {
		return ctxerrors.Wrapf(
			commerr.ErrUnmarshalFailed, "decode %s result: %v", label, err,
		)
	}

	return nil
}

func (c *Client) post(
	ctx context.Context,
	path string,
	body any,
) (json.RawMessage, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, ctxerrors.Wrapf(
			commerr.ErrMarshalFailed, "encode request: %v", err,
		)
	}

	httpRequest, err := http.NewRequestWithContext(
		ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload),
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "build request")
	}

	httpRequest.Header.Set(headerContent, contentType)

	httpResponse, err := c.httpClient.Do(httpRequest)
	if err != nil {
		return nil, classifyTransportError(ctx, err)
	}
	defer httpResponse.Body.Close() //nolint:errcheck // read-only body

	reader := io.LimitReader(httpResponse.Body, c.maxResponseBytes+1)

	raw, err := io.ReadAll(reader)
	if err != nil {
		return nil, ctxerrors.Wrapf(
			commerr.ErrReadFailed, "read response: %v", err,
		)
	}

	if int64(len(raw)) > c.maxResponseBytes {
		return nil, ctxerrors.Wrapf(
			commerr.ErrOutOfRange,
			"response exceeds %d bytes", c.maxResponseBytes,
		)
	}

	if httpResponse.StatusCode != http.StatusOK {
		return nil, ctxerrors.Wrapf(
			commerr.ErrUnavailable,
			"unexpected http status %d", httpResponse.StatusCode,
		)
	}

	return raw, nil
}

// classifyTransportError keeps cancellation and timeouts distinguishable
// from a daemon that is simply not listening.
func classifyTransportError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxerrors.Wrap(ctxErr, "request aborted")
	}

	if isTimeout(err) {
		return ctxerrors.Wrapf(commerr.ErrTimeout, "request timed out: %v", err)
	}

	return ctxerrors.Wrapf(commerr.ErrConnectFailed, "request failed: %v", err)
}

func isTimeout(err error) bool {
	var timeout interface{ Timeout() bool }

	return errors.As(err, &timeout) && timeout.Timeout()
}

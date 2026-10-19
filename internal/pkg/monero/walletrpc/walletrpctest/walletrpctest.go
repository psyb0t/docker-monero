// Package walletrpctest is an in-memory monero-wallet-rpc for tests.
//
// It speaks the same JSON-RPC methods the walletrpc client calls, keeps one
// open wallet per server like the real process, and writes a <name>.keys
// file so the pool's on-disk checks behave as they do in production. It is
// a protocol fake, not a wallet: amounts and heights are whatever the test
// sets.
package walletrpctest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/psyb0t/docker-monero/internal/pkg/monero/walletrpc"
)

// FakeAddress is the primary address every fake wallet reports.
const FakeAddress = "4FakePrimaryAddress"

// FakeTxHash is the hash every fake transfer returns.
const FakeTxHash = "f00dfacef00dfacef00dfacef00dface" +
	"f00dfacef00dfacef00dfacef00dface"

const (
	keysSuffix  = ".keys"
	fileMode    = 0o600
	readTimeout = 5 * time.Second
	walletRPCV  = 65562
)

var errNoWallet = errors.New("no wallet open")

// State is shared by every fake server a test starts, standing in for the
// wallet files and the chain they all see.
type State struct {
	mu sync.Mutex

	WalletDir       string
	Height          uint64
	Balance         uint64
	UnlockedBalance uint64
	Transfers       walletrpc.Transfers
	Subaddresses    []walletrpc.AddressInfo
	LastTransfer    map[string]any
	FailMethod      map[string]rpcError
	Calls           []string
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// NewState returns state backed by walletDir.
func NewState(walletDir string) *State {
	return &State{
		WalletDir:  walletDir,
		FailMethod: map[string]rpcError{},
	}
}

// Fail makes every call to method return the given wallet-rpc error.
func (s *State) Fail(method string, code int, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.FailMethod[method] = rpcError{Code: code, Message: message}
}

// Set runs fn with the state locked, for adjusting fields mid-test.
func (s *State) Set(fn func(state *State)) {
	s.mu.Lock()
	defer s.mu.Unlock()

	fn(s)
}

// Called reports how many times method was called across every server.
func (s *State) Called(method string) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	count := 0

	for _, call := range s.Calls {
		if call == method {
			count++
		}
	}

	return count
}

// Server is one fake wallet-rpc process.
type Server struct {
	state  *State
	server *http.Server
	mu     sync.Mutex
	open   string
}

// Start serves a fake on 127.0.0.1:port until Stop.
func Start(state *State, port int) (*Server, error) {
	listener, err := net.Listen(
		"tcp",
		net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
	)
	if err != nil {
		return nil, fmt.Errorf("listen on %d: %w", port, err)
	}

	fake := &Server{state: state}
	fake.server = &http.Server{
		Handler:           http.HandlerFunc(fake.serve),
		ReadHeaderTimeout: readTimeout,
	}

	go func() { _ = fake.server.Serve(listener) }()

	return fake, nil
}

// Stop shuts the fake down.
func (f *Server) Stop() {
	_ = f.server.Shutdown(context.Background())
}

// OpenWallet is the name of the wallet this fake holds, or "".
func (f *Server) OpenWallet() string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.open
}

type request struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func (f *Server) serve(w http.ResponseWriter, r *http.Request) {
	var req request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)

		return
	}

	params := map[string]any{}
	if len(req.Params) > 0 {
		_ = json.Unmarshal(req.Params, &params)
	}

	f.state.mu.Lock()
	f.state.Calls = append(f.state.Calls, req.Method)
	failure, failing := f.state.FailMethod[req.Method]
	f.state.mu.Unlock()

	if failing {
		writeJSON(w, map[string]any{"error": failure})

		return
	}

	result, rpcErr := f.dispatch(req.Method, params)
	if rpcErr != nil {
		writeJSON(w, map[string]any{"error": rpcErr})

		return
	}

	writeJSON(w, map[string]any{"result": result})
}

func (f *Server) dispatch(
	method string,
	params map[string]any,
) (any, *rpcError) {
	switch method {
	case "get_version":
		return map[string]any{"version": walletRPCV, "release": true}, nil
	case "create_wallet":
		return f.create(params)
	case "restore_deterministic_wallet", "generate_from_keys":
		result, err := f.create(params)
		if err != nil {
			return nil, err
		}

		_ = result

		return map[string]any{"address": FakeAddress}, nil
	case "open_wallet":
		return f.openWallet(params)
	case "close_wallet":
		f.mu.Lock()
		f.open = ""
		f.mu.Unlock()

		return map[string]any{}, nil
	}

	if f.OpenWallet() == "" {
		return nil, &rpcError{
			Code:    walletrpc.CodeNotOpen,
			Message: errNoWallet.Error(),
		}
	}

	return f.walletMethod(method, params)
}

func (f *Server) walletMethod(
	method string,
	params map[string]any,
) (any, *rpcError) {
	f.state.mu.Lock()
	defer f.state.mu.Unlock()

	switch method {
	case "auto_refresh", "store":
		return map[string]any{}, nil
	case "get_height":
		return map[string]any{"height": f.state.Height}, nil
	case "get_balance":
		return map[string]any{
			"balance":          f.state.Balance,
			"unlocked_balance": f.state.UnlockedBalance,
			"blocks_to_unlock": 0,
			"per_subaddress":   []any{},
		}, nil
	case "get_address":
		addresses := append([]walletrpc.AddressInfo{{
			Address: FakeAddress, Label: "Primary account", AddressIndex: 0,
		}}, f.state.Subaddresses...)

		return map[string]any{
			"address":   FakeAddress,
			"addresses": addresses,
		}, nil
	case "create_address":
		count := len(f.state.Subaddresses) + 1
		index := uint32(count) //nolint:gosec // tiny test sizes
		label, _ := params["label"].(string)
		address := fmt.Sprintf("8FakeSubaddress%d", index)
		f.state.Subaddresses = append(
			f.state.Subaddresses,
			walletrpc.AddressInfo{
				Address: address, Label: label, AddressIndex: index,
			},
		)

		return map[string]any{"address": address, "address_index": index}, nil
	case "get_transfers":
		return f.state.Transfers, nil
	case "transfer":
		return f.transfer(params)
	default:
		return nil, &rpcError{Code: -32601, Message: "Method not found"}
	}
}

// transfer runs with state locked.
func (f *Server) transfer(params map[string]any) (any, *rpcError) {
	f.state.LastTransfer = params

	destinations, _ := params["destinations"].([]any)

	var total uint64

	for _, raw := range destinations {
		destination, _ := raw.(map[string]any)
		amount, _ := destination["amount"].(float64)
		total += uint64(amount)
	}

	const fee = 30_000_000

	if total+fee > f.state.UnlockedBalance {
		return nil, &rpcError{
			Code:    walletrpc.CodeNotEnoughUnlockedMoney,
			Message: "not enough unlocked money",
		}
	}

	return map[string]any{
		"tx_hash": FakeTxHash,
		"tx_key":  "fakekey",
		"amount":  total,
		"fee":     fee,
		"weight":  1500,
	}, nil
}

func (f *Server) create(params map[string]any) (any, *rpcError) {
	name, _ := params["filename"].(string)
	password, _ := params["password"].(string)
	path := filepath.Join(f.state.WalletDir, name+keysSuffix)

	if _, err := os.Stat(path); err == nil {
		return nil, &rpcError{
			Code:    walletrpc.CodeWalletAlreadyExists,
			Message: "Cannot create wallet. Already exists.",
		}
	}

	if err := os.WriteFile(path, []byte(password), fileMode); err != nil {
		return nil, &rpcError{
			Code:    walletrpc.CodeUnknownError,
			Message: err.Error(),
		}
	}

	f.mu.Lock()
	f.open = name
	f.mu.Unlock()

	return map[string]any{}, nil
}

func (f *Server) openWallet(params map[string]any) (any, *rpcError) {
	name, _ := params["filename"].(string)
	password, _ := params["password"].(string)

	stored, err := os.ReadFile(
		filepath.Join(f.state.WalletDir, name+keysSuffix),
	)
	if err != nil {
		return nil, &rpcError{
			Code:    walletrpc.CodeUnknownError,
			Message: "Failed to open wallet : file not found",
		}
	}

	if string(stored) != password {
		return nil, &rpcError{
			Code:    walletrpc.CodeUnknownError,
			Message: "Failed to open wallet : Invalid password.",
		}
	}

	f.mu.Lock()
	f.open = name
	f.mu.Unlock()

	return map[string]any{}, nil
}

func writeJSON(w http.ResponseWriter, body any) {
	encoded, err := json.Marshal(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(encoded)
}

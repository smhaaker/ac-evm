// Package chain is a stdlib-only Ethereum JSON-RPC client; signing lives in
// internal/wallet.
package chain

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Avalanche Fuji (C-Chain) defaults.
const (
	FujiRPC     = "https://api.avax-test.network/ext/bc/C/rpc"
	FujiChainID = 43113
)

// Client is a JSON-RPC client, optionally scoped to one contract for reads.
type Client struct {
	endpoint string
	contract string // 0x-prefixed, lowercased; empty for a Dial-only client
	http     *http.Client
	id       atomic.Uint64
}

// Dial returns a client bound to no contract (deploy, nonce, gas, send).
func Dial(endpoint string) *Client {
	return &Client{endpoint: endpoint, http: &http.Client{Timeout: 30 * time.Second}}
}

// New returns a client for the contract at contractAddr.
func New(endpoint, contractAddr string) (*Client, error) {
	addr := strings.ToLower(strings.TrimSpace(contractAddr))
	if !strings.HasPrefix(addr, "0x") || len(addr) != 42 {
		return nil, fmt.Errorf("chain: contract address %q is not a 20-byte 0x address", contractAddr)
	}
	if _, err := hex.DecodeString(addr[2:]); err != nil {
		return nil, fmt.Errorf("chain: contract address is not hex: %w", err)
	}
	return &Client{
		endpoint: endpoint,
		contract: addr,
		http:     &http.Client{Timeout: 30 * time.Second},
	}, nil
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      uint64 `json:"id"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"` // revert data, if any
}

func (e *rpcError) Error() string {
	if len(e.Data) > 0 {
		return fmt.Sprintf("json-rpc error %d: %s %s", e.Code, e.Message, e.Data)
	}
	return fmt.Sprintf("json-rpc error %d: %s", e.Code, e.Message)
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

func (c *Client) call(ctx context.Context, method string, params ...any) (json.RawMessage, error) {
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: c.id.Add(1), Method: method, Params: params})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("chain: %s: %w", method, err)
	}
	defer resp.Body.Close()
	var out rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("chain: %s: decoding response: %w", method, err)
	}
	if out.Error != nil {
		return nil, fmt.Errorf("chain: %s: %w", method, out.Error)
	}
	return out.Result, nil
}

// abiDecodeBytes decodes one ABI `bytes`/`string` return, bounds-checked.
func abiDecodeBytes(data []byte) ([]byte, error) {
	if len(data) < 64 {
		return nil, fmt.Errorf("truncated: %d bytes", len(data))
	}
	off := new(big.Int).SetBytes(data[0:32])
	if !off.IsUint64() {
		return nil, fmt.Errorf("offset out of range")
	}
	o := off.Uint64()
	if o+32 < o || o+32 > uint64(len(data)) {
		return nil, fmt.Errorf("offset %d past data length %d", o, len(data))
	}
	length := new(big.Int).SetBytes(data[o : o+32])
	if !length.IsUint64() {
		return nil, fmt.Errorf("length out of range")
	}
	n := length.Uint64()
	start := o + 32
	if start+n < start || start+n > uint64(len(data)) {
		return nil, fmt.Errorf("length %d past data length %d", n, len(data))
	}
	out := make([]byte, n)
	copy(out, data[start:start+n])
	return out, nil
}

func parseHexUint(s string) (uint64, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "0x")
	if s == "" {
		return 0, fmt.Errorf("empty hex quantity")
	}
	return strconv.ParseUint(s, 16, 64)
}

func decodeHex(s string) ([]byte, error) {
	return hex.DecodeString(strings.TrimPrefix(strings.TrimSpace(s), "0x"))
}

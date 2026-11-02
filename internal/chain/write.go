package chain

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// Write side: nonce, gas, broadcast, receipt. Signing lives in internal/wallet.

// ChainID returns the endpoint's EIP-155 chain id (43113 for Fuji).
func (c *Client) ChainID(ctx context.Context) (uint64, error) {
	raw, err := c.call(ctx, "eth_chainId")
	if err != nil {
		return 0, err
	}
	return unmarshalHexUint(raw, "eth_chainId")
}

// Nonce counts pending txs so back-to-back sends don't collide.
func (c *Client) Nonce(ctx context.Context, addr string) (uint64, error) {
	raw, err := c.call(ctx, "eth_getTransactionCount", addr, "pending")
	if err != nil {
		return 0, err
	}
	return unmarshalHexUint(raw, "eth_getTransactionCount")
}

// GasPrice returns the endpoint's suggested legacy gas price in wei.
func (c *Client) GasPrice(ctx context.Context) (*big.Int, error) {
	raw, err := c.call(ctx, "eth_gasPrice")
	if err != nil {
		return nil, err
	}
	return unmarshalHexBig(raw, "eth_gasPrice")
}

// EstimateGas: to == "" for a creation; value may be nil.
func (c *Client) EstimateGas(ctx context.Context, from, to string, data []byte, value *big.Int) (uint64, error) {
	arg := map[string]any{
		"from":  from,
		"data":  "0x" + hex.EncodeToString(data),
		"value": toHexBig(value),
	}
	if to != "" {
		arg["to"] = to
	}
	raw, err := c.call(ctx, "eth_estimateGas", arg)
	if err != nil {
		return 0, err
	}
	return unmarshalHexUint(raw, "eth_estimateGas")
}

// SendRawTransaction broadcasts a signed transaction and returns its hash.
func (c *Client) SendRawTransaction(ctx context.Context, rawTx []byte) (string, error) {
	raw, err := c.call(ctx, "eth_sendRawTransaction", "0x"+hex.EncodeToString(rawTx))
	if err != nil {
		return "", err
	}
	var h string
	if err := json.Unmarshal(raw, &h); err != nil {
		return "", fmt.Errorf("chain: eth_sendRawTransaction: %w", err)
	}
	return h, nil
}

type Receipt struct {
	Status          uint64 // 1 success, 0 revert
	ContractAddress string // 0x address for a creation, "" otherwise
	BlockNumber     uint64
	GasUsed         uint64
}

// TransactionReceipt returns (nil, nil) while txHash is pending.
func (c *Client) TransactionReceipt(ctx context.Context, txHash string) (*Receipt, error) {
	raw, err := c.call(ctx, "eth_getTransactionReceipt", txHash)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil // still pending
	}
	var r struct {
		Status          string `json:"status"`
		ContractAddress string `json:"contractAddress"`
		BlockNumber     string `json:"blockNumber"`
		GasUsed         string `json:"gasUsed"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("chain: eth_getTransactionReceipt: %w", err)
	}
	status, _ := parseHexUint(r.Status)
	blk, _ := parseHexUint(r.BlockNumber)
	gas, _ := parseHexUint(r.GasUsed)
	return &Receipt{
		Status:          status,
		ContractAddress: strings.ToLower(r.ContractAddress),
		BlockNumber:     blk,
		GasUsed:         gas,
	}, nil
}

// WaitReceipt polls until mined or ctx ends; zero interval = 2s.
func (c *Client) WaitReceipt(ctx context.Context, txHash string, interval time.Duration) (*Receipt, error) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		rcpt, err := c.TransactionReceipt(ctx, txHash)
		if err != nil {
			return nil, err
		}
		if rcpt != nil {
			return rcpt, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-t.C:
		}
	}
}

func unmarshalHexUint(raw json.RawMessage, method string) (uint64, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return 0, fmt.Errorf("chain: %s: %w", method, err)
	}
	return parseHexUint(s)
}

func unmarshalHexBig(raw json.RawMessage, method string) (*big.Int, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("chain: %s: %w", method, err)
	}
	n, ok := new(big.Int).SetString(strings.TrimPrefix(strings.TrimSpace(s), "0x"), 16)
	if !ok {
		return nil, fmt.Errorf("chain: %s: %q is not a hex quantity", method, s)
	}
	return n, nil
}

func toHexBig(n *big.Int) string {
	if n == nil || n.Sign() == 0 {
		return "0x0"
	}
	return "0x" + n.Text(16)
}

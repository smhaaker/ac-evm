package chain

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	"github.com/smhaaker/ac-evm/internal/keccak"
)

// Calldata and view calls for ACToken (contracts/ACToken.sol).

func selector(sig string) []byte {
	h := keccak.Sum256([]byte(sig))
	return h[:4]
}

func leftPad32(n uint64) []byte {
	w := make([]byte, 32)
	for i := 0; i < 8; i++ {
		w[31-i] = byte(n >> (8 * i))
	}
	return w
}

var (
	mintSelector         = selector("mint(bytes)")
	mintWithSigSelector  = selector("mintWithSig(address,bytes,uint256,bytes)")
	noncesSelector       = selector("nonces(address)")
	totalSupplySelector  = selector("totalSupply()")
	ownerOfSelector      = selector("ownerOf(uint256)")
	readSelector         = selector("read(uint256)")
	compressedOfSelector = selector("compressedOf(uint256)")
	tokenURISelector     = selector("tokenURI(uint256)")
)

// MintCalldata builds calldata for mint(bytes blob).
func MintCalldata(compressed []byte) []byte {
	out := append([]byte{}, mintSelector...)
	out = append(out, leftPad32(0x20)...)
	return append(out, bytesTail(compressed)...)
}

// MintWithSigCalldata builds calldata for mintWithSig(author, blob, deadline, sig).
func MintWithSigCalldata(author string, blob []byte, deadline uint64, sig []byte) []byte {
	tail := bytesTail(blob)
	out := append([]byte{}, mintWithSigSelector...)
	out = append(out, addrWord(author)...)
	out = append(out, leftPad32(4*32)...)
	out = append(out, leftPad32(deadline)...)
	out = append(out, leftPad32(uint64(4*32+len(tail)))...)
	out = append(out, tail...)
	return append(out, bytesTail(sig)...)
}

// bytesTail is the ABI length word + payload, padded to 32 bytes.
func bytesTail(b []byte) []byte {
	out := append(leftPad32(uint64(len(b))), b...)
	if rem := len(b) % 32; rem != 0 {
		out = append(out, make([]byte, 32-rem)...)
	}
	return out
}

func addrWord(a string) []byte {
	b, _ := hex.DecodeString(strings.TrimPrefix(strings.TrimSpace(a), "0x"))
	w := make([]byte, 32)
	if len(b) == 20 {
		copy(w[12:], b)
	}
	return w
}

// Call is eth_call against `to` at the latest block; returns raw ABI bytes.
func (c *Client) Call(ctx context.Context, to string, data []byte) ([]byte, error) {
	arg := map[string]any{"to": to, "data": "0x" + hex.EncodeToString(data)}
	raw, err := c.call(ctx, "eth_call", arg, "latest")
	if err != nil {
		return nil, err
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("chain: eth_call: %w", err)
	}
	return decodeHex(s)
}

func (c *Client) callContract(ctx context.Context, data []byte) ([]byte, error) {
	if c.contract == "" {
		return nil, fmt.Errorf("chain: client has no contract address (use New, not Dial)")
	}
	return c.Call(ctx, c.contract, data)
}

func withID(selector []byte, id uint64) []byte {
	return append(append([]byte{}, selector...), leftPad32(id)...)
}

// TotalSupply is also the id of the last mint.
func (c *Client) TotalSupply(ctx context.Context) (uint64, error) {
	ret, err := c.callContract(ctx, totalSupplySelector)
	if err != nil {
		return 0, err
	}
	if len(ret) < 32 {
		return 0, fmt.Errorf("chain: totalSupply: short return")
	}
	return new(big.Int).SetBytes(ret[:32]).Uint64(), nil
}

func (c *Client) OwnerOf(ctx context.Context, id uint64) (string, error) {
	ret, err := c.callContract(ctx, withID(ownerOfSelector, id))
	if err != nil {
		return "", err
	}
	if len(ret) < 32 {
		return "", fmt.Errorf("chain: ownerOf: short return")
	}
	return "0x" + hex.EncodeToString(ret[12:32]), nil
}

// CompressedOf returns the raw packed bytes stored for token id.
func (c *Client) CompressedOf(ctx context.Context, id uint64) ([]byte, error) {
	ret, err := c.callContract(ctx, withID(compressedOfSelector, id))
	if err != nil {
		return nil, err
	}
	b, err := abiDecodeBytes(ret)
	if err != nil {
		return nil, fmt.Errorf("chain: compressedOf: %w", err)
	}
	return b, nil
}

// ReadMessage decodes token id on-chain via read(uint256).
func (c *Client) ReadMessage(ctx context.Context, id uint64) (string, error) {
	ret, err := c.callContract(ctx, withID(readSelector, id))
	if err != nil {
		return "", err
	}
	b, err := abiDecodeBytes(ret) // a `string` return is ABI-encoded like `bytes`
	if err != nil {
		return "", fmt.Errorf("chain: read: %w", err)
	}
	return string(b), nil
}

// Nonces is the next EIP-712 mint nonce for author.
func (c *Client) Nonces(ctx context.Context, author string) (uint64, error) {
	ret, err := c.callContract(ctx, append(append([]byte{}, noncesSelector...), addrWord(author)...))
	if err != nil {
		return 0, err
	}
	if len(ret) < 32 {
		return 0, fmt.Errorf("chain: nonces: short return")
	}
	return new(big.Int).SetBytes(ret[:32]).Uint64(), nil
}

func (c *Client) TokenURI(ctx context.Context, id uint64) (string, error) {
	ret, err := c.callContract(ctx, withID(tokenURISelector, id))
	if err != nil {
		return "", err
	}
	b, err := abiDecodeBytes(ret)
	if err != nil {
		return "", fmt.Errorf("chain: tokenURI: %w", err)
	}
	return string(b), nil
}

// TokenInitCode appends ACToken's constructor arg (the decoder) to its bytecode.
func TokenInitCode(code []byte, decoder string) []byte {
	return append(append([]byte{}, code...), addrWord(decoder)...)
}

// ExplainRevert names ACToken/decoder custom errors found in a revert.
func ExplainRevert(err error) error {
	if err == nil {
		return nil
	}
	for _, name := range []string{"BadSignature", "Expired", "EmptyBlob", "Soulbound", "Nonexistent", "TooLong", "Corrupt"} {
		if strings.Contains(err.Error(), hex.EncodeToString(selector(name+"()"))) {
			return fmt.Errorf("contract rejected it: %s", name)
		}
	}
	return err
}

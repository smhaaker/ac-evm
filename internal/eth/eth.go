// Package eth holds Ethereum encodings (addresses, RLP, EIP-1559, EIP-712) on
// stdlib + internal/keccak only.
package eth

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"github.com/smhaaker/ac-evm/internal/keccak"
)

type Address [20]byte

func ParseAddress(s string) (Address, error) {
	var a Address
	b, err := hex.DecodeString(strings.TrimPrefix(s, "0x"))
	if err != nil || len(b) != 20 {
		return a, fmt.Errorf("eth: bad address %q", s)
	}
	copy(a[:], b)
	return a, nil
}

// Hex is the EIP-55 checksummed form.
func (a Address) Hex() string {
	lower := hex.EncodeToString(a[:])
	h := keccak.Sum256([]byte(lower))
	out := []byte(lower)
	for i, c := range out {
		if c >= 'a' && (h[i/2]>>(4*(1-uint(i%2))))&0xf >= 8 {
			out[i] = c - 32
		}
	}
	return "0x" + string(out)
}

// Tx is an EIP-1559 (type 2) transaction. To == nil deploys a contract.
type Tx struct {
	ChainID, Nonce uint64
	Tip, FeeCap    *big.Int
	Gas            uint64
	To             *Address
	Value          *big.Int
	Data           []byte
}

func (tx Tx) fields() []any {
	var to []byte
	if tx.To != nil {
		to = tx.To[:]
	}
	return []any{tx.ChainID, tx.Nonce, tx.Tip, tx.FeeCap, tx.Gas, to, tx.Value, tx.Data, []any{}}
}

func (tx Tx) SigningHash() [32]byte {
	return keccak.Sum256(append([]byte{2}, rlp(tx.fields())...))
}

// Encode returns the raw signed tx for eth_sendRawTransaction; sig is r||s||v (v = 27/28).
func (tx Tx) Encode(sig [65]byte) []byte {
	f := append(tx.fields(), uint64(sig[64]-27), new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:64]))
	return append([]byte{2}, rlp(f)...)
}

func rlp(v any) []byte {
	switch x := v.(type) {
	case uint64:
		return rlp(new(big.Int).SetUint64(x))
	case *big.Int:
		if x == nil {
			return rlp([]byte{})
		}
		return rlp(x.Bytes())
	case []byte:
		if len(x) == 1 && x[0] < 0x80 {
			return x
		}
		return append(rlpHeader(0x80, len(x)), x...)
	case []any:
		var body []byte
		for _, e := range x {
			body = append(body, rlp(e)...)
		}
		return append(rlpHeader(0xc0, len(body)), body...)
	}
	panic(fmt.Sprintf("rlp: unsupported %T", v))
}

func rlpHeader(base byte, n int) []byte {
	if n < 56 {
		return []byte{base + byte(n)}
	}
	l := big.NewInt(int64(n)).Bytes()
	return append([]byte{base + 55 + byte(len(l))}, l...)
}

// MintDigest is the EIP-712 digest ACToken.mintWithSig verifies.
func MintDigest(chainID uint64, token, author Address, blob []byte, nonce, deadline uint64) [32]byte {
	word := func(v uint64) []byte { return new(big.Int).SetUint64(v).FillBytes(make([]byte, 32)) }
	addr := func(a Address) []byte { return append(make([]byte, 12), a[:]...) }
	hash := func(parts ...[]byte) []byte {
		h := keccak.Sum256(bytes.Join(parts, nil))
		return h[:]
	}
	domain := hash(
		hash([]byte("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)")),
		hash([]byte("ac-evm")), hash([]byte("1")), word(chainID), addr(token),
	)
	mint := hash(
		hash([]byte("Mint(address author,bytes blob,uint256 nonce,uint256 deadline)")),
		addr(author), hash(blob), word(nonce), word(deadline),
	)
	var d [32]byte
	copy(d[:], hash([]byte{0x19, 0x01}, domain, mint))
	return d
}

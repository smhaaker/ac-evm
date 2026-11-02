//go:build evm

// Go↔EVM checks against anvil: decoder parity, and EIP-712 mint → tokenURI.
package contracts

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/smhaaker/ac-evm/internal/ac"
	"github.com/smhaaker/ac-evm/internal/chain"
	"github.com/smhaaker/ac-evm/internal/eth"
	"github.com/smhaaker/ac-evm/internal/keccak"
	"github.com/smhaaker/ac-evm/internal/txn"
	"github.com/smhaaker/ac-evm/internal/wallet"
)

// Foundry's published anvil dev keys (accounts 0 and 1).
const anvilKey0, anvilKey1 = "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80",
	"59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d"

var ctx = context.Background()

func endpoint() string {
	if u := os.Getenv("RPC_URL"); u != "" {
		return u
	}
	return "http://127.0.0.1:8545"
}

func key(t *testing.T, hexKey string) *wallet.Key {
	k, err := wallet.FromHex(hexKey)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// sendTx goes through txn exactly as the TUI will; to == "" deploys.
func sendTx(t *testing.T, k *wallet.Key, to string, data []byte) *chain.Receipt {
	t.Helper()
	c := chain.Dial(endpoint())
	p, err := txn.Prepare(ctx, c, k.Address().Hex(), to, data)
	if err != nil {
		t.Fatal(err)
	}
	rc, h, err := txn.Send(ctx, c, k, p)
	if err != nil || rc.Status != 1 {
		t.Fatalf("tx %s: %+v %v", h, rc, err)
	}
	return rc
}

func deploy(t *testing.T, name string, ctor []byte) string {
	t.Helper()
	bin, err := os.ReadFile("../out/" + name + ".bin")
	if err != nil {
		t.Fatalf("run make compile first: %v", err)
	}
	code, _ := hex.DecodeString(strings.TrimSpace(string(bin)))
	return sendTx(t, key(t, anvilKey0), "", append(code, ctor...)).ContractAddress
}

func word(v any) []byte {
	w := make([]byte, 32)
	switch x := v.(type) {
	case int:
		big.NewInt(int64(x)).FillBytes(w)
	case string: // address
		b, _ := hex.DecodeString(strings.TrimPrefix(x, "0x"))
		copy(w[32-len(b):], b)
	}
	return w
}

// abi encodes a call for signatures chain has no helper for.
func abi(sig string, args ...any) []byte {
	sel := keccak.Sum256([]byte(sig))
	var head, tail []byte
	for _, a := range args {
		b, ok := a.([]byte)
		if !ok {
			head = append(head, word(a)...)
			continue
		}
		head = append(head, word(32*len(args)+len(tail))...)
		tail = append(tail, word(len(b))...)
		tail = append(tail, b...)
		tail = append(tail, make([]byte, (32-len(b)%32)%32)...)
	}
	return append(append(sel[:4:4], head...), tail...)
}

func TestDecodeMatchesGo(t *testing.T) {
	all := make([]byte, 256)
	for i := range all {
		all[i] = byte(i)
	}
	msgs := map[string][]byte{
		"single":   []byte("a"),
		"short":    []byte("hello"),
		"phrase":   []byte("the loose brick is third from the left"),
		"repeat":   bytes.Repeat([]byte("a"), 100),
		"rescale":  bytes.Repeat([]byte("ab"), 1200),
		"allbytes": all,
		"newlines": []byte("line one\nline two\nline three"),
		"unicode":  []byte("café — naïve — 🜁"),
		"json":     []byte(`{"to":"0xabc","msg":"gm"}`),
		"long":     []byte("the quick brown fox jumps over the lazy dog, then does it again and again to build up some real statistics for the model to chew on"),
		"binary":   {0x00, 0x01, 0x02, 0xfe, 0xff, 0x00, 0x80, 0x7f},
	}
	c := chain.Dial(endpoint())
	dec := deploy(t, "ArithmeticDecoder", nil)
	for name, msg := range msgs {
		for _, id := range []byte{ac.PriorCold, ac.PriorEnglish} {
			blob, _ := ac.Pack(msg, id)
			t.Run(fmt.Sprintf("%s-%s", name, ac.PriorName(id)), func(t *testing.T) {
				ret, err := c.Call(ctx, dec, abi("decompressPacked(bytes,uint256)", blob, 1<<16))
				if err != nil {
					t.Fatal(err)
				}
				if got := ret[64 : 64+new(big.Int).SetBytes(ret[32:64]).Int64()]; !bytes.Equal(got, msg) {
					t.Fatalf("EVM decoded %q, want %q", got, msg)
				}
			})
		}
	}
}

func TestMintWithSig(t *testing.T) {
	relayerKey, authorKey := key(t, anvilKey0), key(t, anvilKey1)
	relayer, author := relayerKey.Address().Hex(), authorKey.Address().Hex()
	tok := deploy(t, "ACToken", word(deploy(t, "ArithmeticDecoder", nil)))
	c, err := chain.New(endpoint(), tok)
	if err != nil {
		t.Fatal(err)
	}

	msg := `Thank you for "everything".` + "\nSee you soon."
	blob := ac.PackBest([]byte(msg))
	const deadline = 1 << 40

	tokAddr, _ := eth.ParseAddress(tok)
	goSig, err := authorKey.Sign(eth.MintDigest(31337, tokAddr, authorKey.Address(), blob, 0, deadline))
	if err != nil {
		t.Fatal(err)
	}
	sig := goSig[:]

	// RFC 6979 is deterministic, so anvil's signature must equal Go's.
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "eth_signTypedData_v4", "params": []any{author, map[string]any{
		"types": map[string]any{
			"EIP712Domain": []map[string]string{{"name": "name", "type": "string"}, {"name": "version", "type": "string"}, {"name": "chainId", "type": "uint256"}, {"name": "verifyingContract", "type": "address"}},
			"Mint":         []map[string]string{{"name": "author", "type": "address"}, {"name": "blob", "type": "bytes"}, {"name": "nonce", "type": "uint256"}, {"name": "deadline", "type": "uint256"}},
		},
		"primaryType": "Mint",
		"domain":      map[string]any{"name": "ac-evm", "version": "1", "chainId": 31337, "verifyingContract": tok},
		"message":     map[string]any{"author": author, "blob": "0x" + hex.EncodeToString(blob), "nonce": "0", "deadline": fmt.Sprint(deadline)},
	}}})
	resp, err := http.Post(endpoint(), "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var anvil struct{ Result string }
	json.NewDecoder(resp.Body).Decode(&anvil)
	resp.Body.Close()
	if anvil.Result != "0x"+hex.EncodeToString(sig) {
		t.Fatalf("Go sig %x != anvil sig %s", sig, anvil.Result)
	}

	const badSig, soulbound = "5cd5d233", "a4420a95" // error selectors
	reverts := func(data []byte, sel string) bool {
		_, err := c.Call(ctx, tok, data)
		return err != nil && strings.Contains(err.Error(), sel)
	}
	tampered := append([]byte{}, blob...)
	tampered[len(tampered)-1] ^= 1
	if !reverts(chain.MintWithSigCalldata(author, tampered, deadline, sig), badSig) {
		t.Fatal("tampered blob not rejected")
	}
	sendTx(t, relayerKey, tok, chain.MintWithSigCalldata(author, blob, deadline, sig))
	if !reverts(chain.MintWithSigCalldata(author, blob, deadline, sig), badSig) {
		t.Fatal("replayed signature not rejected")
	}
	if n, err := c.Nonces(ctx, author); err != nil || n != 1 {
		t.Fatalf("nonce = %d, %v", n, err)
	}

	if owner, err := c.OwnerOf(ctx, 1); err != nil || !strings.EqualFold(owner, author) {
		t.Fatalf("owner %s, want author %s (%v)", owner, author, err)
	}
	if !reverts(abi("transferFrom(address,address,uint256)", author, relayer, 1), soulbound) {
		t.Fatal("transfer not rejected")
	}

	uri, err := c.TokenURI(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(uri, "data:application/json;base64,"))
	if err != nil {
		t.Fatalf("tokenURI %q: %v", uri, err)
	}
	var meta struct{ Name, Description string }
	if err := json.Unmarshal(raw, &meta); err != nil || meta.Description != msg {
		t.Fatalf("tokenURI json %s: %v", raw, err)
	}
	if got, err := c.ReadMessage(ctx, 1); err != nil || got != msg {
		t.Fatalf("read = %q, %v", got, err)
	}
}

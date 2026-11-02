// Package wallet holds secp256k1 keys. It is the only package that imports decred.
package wallet

import (
	"encoding/hex"
	"errors"
	"strings"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"

	"github.com/smhaaker/ac-evm/internal/eth"
	"github.com/smhaaker/ac-evm/internal/keccak"
)

type Key struct{ priv *secp256k1.PrivateKey }

func Generate() (*Key, error) {
	p, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		return nil, err
	}
	return &Key{p}, nil
}

func FromHex(s string) (*Key, error) {
	b, err := hex.DecodeString(strings.TrimPrefix(strings.TrimSpace(s), "0x"))
	if err != nil || len(b) != 32 {
		return nil, errors.New("wallet: private key must be 32 bytes hex")
	}
	var n secp256k1.ModNScalar
	if overflow := n.SetByteSlice(b); overflow || n.IsZero() {
		return nil, errors.New("wallet: not a valid secp256k1 key")
	}
	return &Key{secp256k1.NewPrivateKey(&n)}, nil
}

func (k *Key) Address() eth.Address {
	h := keccak.Sum256(k.priv.PubKey().SerializeUncompressed()[1:])
	var a eth.Address
	copy(a[:], h[12:])
	return a
}

// Sign returns r||s||v with v = 27/28 and low s.
func (k *Key) Sign(hash [32]byte) ([65]byte, error) {
	var out [65]byte
	c := ecdsa.SignCompact(k.priv, hash[:], false)
	if c[0] > 28 {
		return out, errors.New("wallet: unsupported recovery id")
	}
	copy(out[:64], c[1:])
	out[64] = c[0]
	return out, nil
}

func (k *Key) SignTx(tx eth.Tx) ([]byte, error) {
	sig, err := k.Sign(tx.SigningHash())
	if err != nil {
		return nil, err
	}
	return tx.Encode(sig), nil
}

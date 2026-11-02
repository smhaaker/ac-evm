package keccak

import (
	"encoding/hex"
	"strings"
	"testing"
)

// Ethereum Keccak-256 (0x01 padding, not SHA3-256), incl. rate-boundary lengths.
func TestVectors(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "c5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470"},
		{"abc", "4e03657aea45a94fc7d47ba826c8d667c0d1e6e33a64a036ec44f58fa12d6c45"},
		{strings.Repeat("a", 135), "34367dc248bbd832f4e3e69dfaac2f92638bd0bbd18f2912ba4ef454919cf446"},
		{strings.Repeat("a", 136), "a6c4d403279fe3e0af03729caada8374b5ca54d8065329a3ebcaeb4b60aa386e"},
		{strings.Repeat("a", 272), "cf7fcd4f705ee749930d19ca84561a9bf62516bd90a471545fa2f49fdc7e63c8"},
	}
	for _, c := range cases {
		d := Sum256([]byte(c.in))
		if got := hex.EncodeToString(d[:]); got != c.want {
			t.Errorf("Sum256(%d bytes) = %s, want %s", len(c.in), got, c.want)
		}
	}
}

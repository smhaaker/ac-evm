package ac

import (
	"bytes"
	"math/rand"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	all := make([]byte, 256)
	for i := range all {
		all[i] = byte(i)
	}
	rnd := make([]byte, 5000)
	rand.New(rand.NewSource(1)).Read(rnd)
	inputs := [][]byte{{}, []byte("hello from the EVM"), all, rnd, bytes.Repeat([]byte("ab"), 40000)}
	for _, prior := range [][]byte{nil, DefaultPrior} {
		for _, src := range inputs {
			got, err := Decompress(Compress(src, prior), prior, 0)
			if err != nil || !bytes.Equal(got, src) {
				t.Fatalf("round trip failed for %d bytes: %v", len(src), err)
			}
		}
	}
}

func TestPackBest(t *testing.T) {
	cases := map[string]byte{
		"the quick brown fox jumps over the lazy dog":      PriorEnglish,
		string(bytes.Repeat([]byte{0, 1, 0xfe, 0xff}, 12)): PriorCold,
	}
	for msg, want := range cases {
		blob := PackBest([]byte(msg))
		id, out, err := Unpack(blob, 1<<16)
		if err != nil || id != want || string(out) != msg {
			t.Fatalf("PackBest(%q): id=%d want %d, err=%v", msg, id, want, err)
		}
	}
}

func FuzzRoundTrip(f *testing.F) {
	f.Add([]byte("hello"))
	f.Fuzz(func(t *testing.T, src []byte) {
		got, err := Decompress(Compress(src, DefaultPrior), DefaultPrior, 0)
		if err != nil || !bytes.Equal(got, src) {
			t.Fatalf("round trip failed: %v", err)
		}
	})
}

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

// Untrusted blobs (vouchers, pasted hex) must never panic or exceed the limit.
func FuzzUnpackHostile(f *testing.F) {
	f.Add([]byte{0x01, 0xff, 0xff, 0xff, 0xff})
	f.Fuzz(func(t *testing.T, blob []byte) {
		if _, out, err := Unpack(blob, 1000); err == nil && len(out) > 1000 {
			t.Fatalf("limit not enforced: %d", len(out))
		}
	})
}

func TestPrintable(t *testing.T) {
	cases := map[string]string{
		"gm\n\tok":           "gm\n\tok",
		"café 🜁":             "café 🜁",
		"\x1b[2Jhidden":      `\u001b[2Jhidden`,
		"a\x07\x7fb":         `a\u0007\u007fb`,
		"\u009b31m":          `\u009b31m`,
		"abc\u202edcba":      `abc\u202edcba`,
		string([]byte{0xff}): `\xff`,
	}
	for in, want := range cases {
		if got := Printable(in); got != want {
			t.Errorf("Printable(%q) = %q, want %q", in, got, want)
		}
	}
}

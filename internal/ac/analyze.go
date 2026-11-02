package ac

import (
	"fmt"
	"math"
)

// SymbolCost is a symbol's slice [Lo,Hi) of the interval and its cost in bits.
type SymbolCost struct {
	Sym  byte
	Bits float64
	Lo   float64
	Hi   float64
}

// Analyze reports the modeled cost of each byte of src, without coding.
func Analyze(src, prior []byte) []SymbolCost {
	m := NewModel(prior)
	out := make([]SymbolCost, len(src))
	for i, b := range src {
		lo, hi, tot := m.Cum(int(b))
		out[i] = cost(b, lo, hi, tot)
		m.Update(int(b))
	}
	return out
}

func cost(sym byte, lo, hi, tot uint64) SymbolCost {
	loF, hiF := float64(lo)/float64(tot), float64(hi)/float64(tot)
	return SymbolCost{Sym: sym, Bits: -math.Log2(hiF - loF), Lo: loF, Hi: hiF}
}

// AnalyzeDecode is Analyze driven by the real decoder over compressed bytes.
func AnalyzeDecode(compressed, prior []byte, limit int) ([]SymbolCost, error) {
	if limit <= 0 {
		limit = 1 << 20
	}
	m := NewModel(prior)
	d := newDecoder(compressed)
	var out []SymbolCost
	for {
		tot := m.Total()
		sym, lo, hi, err := m.Find(d.target(tot))
		if err != nil {
			return nil, err
		}
		d.consume(lo, hi, tot)
		if sym == symEOF {
			return out, nil
		}
		if len(out) >= limit {
			return nil, fmt.Errorf("ac: decode exceeds limit of %d symbols", limit)
		}
		out = append(out, cost(byte(sym), lo, hi, tot))
		m.Update(sym)
	}
}

func TotalBits(costs []SymbolCost) float64 {
	var t float64
	for _, c := range costs {
		t += c.Bits
	}
	return t
}

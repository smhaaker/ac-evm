// Package ac is a Witten-Neal-Cleary arithmetic coder (32-bit register, E3
// underflow) with an adaptive order-1 model.
package ac

import (
	"errors"
	"fmt"
	"io"
)

const (
	// 32-bit register carried in uint64 so products don't overflow.
	top     = uint64(1) << 32
	half    = top >> 1
	quarter = top >> 2
	threeQ  = 3 * quarter

	// range (<= 2^32) * total (< 2^16) stays exact in uint64.
	maxTotal = uint64(1) << 16
)

var ErrCorrupt = errors.New("ac: corrupt or model-mismatched stream")

type bitWriter struct {
	buf  []byte
	cur  byte
	nbit uint8
}

func (w *bitWriter) writeBit(b uint64) {
	w.cur <<= 1
	if b != 0 {
		w.cur |= 1
	}
	w.nbit++
	if w.nbit == 8 {
		w.buf = append(w.buf, w.cur)
		w.cur, w.nbit = 0, 0
	}
}

// flush pads the final partial byte with zero bits.
func (w *bitWriter) flush() {
	for w.nbit != 0 {
		w.writeBit(0)
	}
}

type bitReader struct {
	buf []byte
	pos int
	bit uint8
}

// readBit returns 0 past the end; the coder relies on zero padding.
func (r *bitReader) readBit() uint64 {
	if r.pos >= len(r.buf) {
		return 0
	}
	b := (r.buf[r.pos] >> (7 - r.bit)) & 1
	r.bit++
	if r.bit == 8 {
		r.bit = 0
		r.pos++
	}
	return uint64(b)
}

type encoder struct {
	low, high uint64
	pending   int
	w         bitWriter
}

func newEncoder() *encoder {
	return &encoder{low: 0, high: top - 1}
}

// emit writes b, then the pending underflow bits (always ^b).
func (e *encoder) emit(b uint64) {
	e.w.writeBit(b)
	for ; e.pending > 0; e.pending-- {
		e.w.writeBit(1 - b)
	}
}

func (e *encoder) encode(cumLow, cumHigh, total uint64) {
	rng := e.high - e.low + 1
	e.high = e.low + (rng*cumHigh)/total - 1
	e.low = e.low + (rng*cumLow)/total

	for {
		switch {
		case e.high < half:
			e.emit(0)
		case e.low >= half:
			e.emit(1)
			e.low -= half
			e.high -= half
		case e.low >= quarter && e.high < threeQ:
			e.pending++
			e.low -= quarter
			e.high -= quarter
		default:
			return
		}
		e.low = (e.low << 1) & (top - 1)
		e.high = ((e.high << 1) | 1) & (top - 1)
	}
}

func (e *encoder) finish() []byte {
	e.pending++
	if e.low < quarter {
		e.emit(0)
	} else {
		e.emit(1)
	}
	e.w.flush()
	return e.w.buf
}

type decoder struct {
	low, high, value uint64
	r                bitReader
}

func newDecoder(buf []byte) *decoder {
	d := &decoder{low: 0, high: top - 1, r: bitReader{buf: buf}}
	for i := 0; i < 32; i++ {
		d.value = (d.value << 1) | d.r.readBit()
	}
	return d
}

func (d *decoder) target(total uint64) uint64 {
	rng := d.high - d.low + 1
	return ((d.value-d.low+1)*total - 1) / rng
}

func (d *decoder) consume(cumLow, cumHigh, total uint64) {
	rng := d.high - d.low + 1
	d.high = d.low + (rng*cumHigh)/total - 1
	d.low = d.low + (rng*cumLow)/total

	for {
		switch {
		case d.high < half:
			// nothing to subtract
		case d.low >= half:
			d.low -= half
			d.high -= half
			d.value -= half
		case d.low >= quarter && d.high < threeQ:
			d.low -= quarter
			d.high -= quarter
			d.value -= quarter
		default:
			return
		}
		d.low = (d.low << 1) & (top - 1)
		d.high = ((d.high << 1) | 1) & (top - 1)
		d.value = ((d.value << 1) | d.r.readBit()) & (top - 1)
	}
}

// Compress codes src; prior warm-starts the model (nil = cold).
func Compress(src, prior []byte) []byte {
	m := NewModel(prior)
	e := newEncoder()
	for _, b := range src {
		lo, hi, tot := m.Cum(int(b))
		e.encode(lo, hi, tot)
		m.Update(int(b))
	}
	lo, hi, tot := m.Cum(symEOF)
	e.encode(lo, hi, tot)
	return e.finish()
}

// Decompress reverses Compress. limit caps output (0 = 1 MiB).
func Decompress(src, prior []byte, limit int) ([]byte, error) {
	if limit <= 0 {
		limit = 1 << 20
	}
	m := NewModel(prior)
	d := newDecoder(src)
	out := make([]byte, 0, len(src)*4)
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
			return nil, fmt.Errorf("ac: output exceeds limit of %d bytes: %w", limit, io.ErrShortBuffer)
		}
		out = append(out, byte(sym))
		m.Update(sym)
	}
}

package ac

const (
	nSyms  = 257 // 0..255 = byte values, 256 = terminator
	symEOF = 256

	increment = 32
	rescaleAt = 1 << 15 // halve counts well before maxTotal
)

// Model is one frequency row per previous-byte context (EOF = start).
type Model struct {
	ctx   int
	count [nSyms][nSyms]uint32
	total [nSyms]uint32
}

// NewModel starts uniform, then replays prior through Update.
func NewModel(prior []byte) *Model {
	m := &Model{}
	for c := 0; c < nSyms; c++ {
		for s := 0; s < nSyms; s++ {
			m.count[c][s] = 1
		}
		m.total[c] = nSyms
	}
	for _, b := range prior {
		m.Update(int(b))
	}
	m.ctx = symEOF
	return m
}

func (m *Model) Total() uint64 { return uint64(m.total[m.ctx]) }

func (m *Model) Cum(sym int) (lo, hi, total uint64) {
	row := &m.count[m.ctx]
	var acc uint32
	for s := 0; s < sym; s++ {
		acc += row[s]
	}
	return uint64(acc), uint64(acc + row[sym]), uint64(m.total[m.ctx])
}

func (m *Model) Find(target uint64) (sym int, lo, hi uint64, err error) {
	row := &m.count[m.ctx]
	var acc uint32
	for s := 0; s < nSyms; s++ {
		next := acc + row[s]
		if uint64(next) > target {
			return s, uint64(acc), uint64(next), nil
		}
		acc = next
	}
	return 0, 0, 0, ErrCorrupt
}

func (m *Model) Update(sym int) {
	row := &m.count[m.ctx]
	row[sym] += increment
	m.total[m.ctx] += increment
	if m.total[m.ctx] >= rescaleAt {
		var t uint32
		for s := 0; s < nSyms; s++ {
			row[s] = (row[s] + 1) / 2 // never zero
			t += row[s]
		}
		m.total[m.ctx] = t
	}
	m.ctx = sym
}

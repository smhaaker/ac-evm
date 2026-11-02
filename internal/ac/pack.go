package ac

import "fmt"

// blob = priorID(1) | coded bytes. Never renumber ids; add new ones.
const (
	PriorCold    byte = 0
	PriorEnglish byte = 1 // DefaultPrior
)

// packIDs is the order PackBest tries priors in; ties prefer the earlier id.
var packIDs = []byte{PriorCold, PriorEnglish}

// PriorByID returns the corpus for an id (nil for cold) and whether it is known.
func PriorByID(id byte) (prior []byte, ok bool) {
	switch id {
	case PriorCold:
		return nil, true
	case PriorEnglish:
		return DefaultPrior, true
	}
	return nil, false
}

// Pack compresses src with prior `id` and prepends the id byte.
func Pack(src []byte, id byte) ([]byte, error) {
	prior, ok := PriorByID(id)
	if !ok {
		return nil, fmt.Errorf("ac: unknown prior id %d", id)
	}
	coded := Compress(src, prior)
	out := make([]byte, 0, 1+len(coded))
	out = append(out, id)
	return append(out, coded...), nil
}

// PackBest returns the smallest Pack over all priors; ties prefer cold.
func PackBest(src []byte) []byte {
	var best []byte
	for _, id := range packIDs {
		b, err := Pack(src, id)
		if err != nil {
			continue
		}
		if best == nil || len(b) < len(best) {
			best = b
		}
	}
	return best
}

// Unpack decodes a packed blob and returns its prior id.
func Unpack(blob []byte, limit int) (id byte, out []byte, err error) {
	if len(blob) == 0 {
		return 0, nil, fmt.Errorf("ac: empty blob")
	}
	id = blob[0]
	prior, ok := PriorByID(id)
	if !ok {
		return id, nil, fmt.Errorf("ac: unknown prior id %d", id)
	}
	out, err = Decompress(blob[1:], prior, limit)
	return id, out, err
}

// PriorName is a short human label for a prior id.
func PriorName(id byte) string {
	switch id {
	case PriorCold:
		return "cold"
	case PriorEnglish:
		return "english"
	}
	return fmt.Sprintf("prior#%d", id)
}

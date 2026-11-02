package eth

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"
)

const voucherPrefix = "acevm:"

// Voucher is a signed mint anyone can submit via mintWithSig.
type Voucher struct {
	Author   Address
	Deadline uint64
	Sig      [65]byte
	Blob     []byte
}

// String is "acevm:" + hex(author | deadline | sig | blob).
func (v Voucher) String() string {
	b := append(v.Author[:], binary.BigEndian.AppendUint64(nil, v.Deadline)...)
	b = append(append(b, v.Sig[:]...), v.Blob...)
	return voucherPrefix + hex.EncodeToString(b)
}

func ParseVoucher(s string) (Voucher, error) {
	var v Voucher
	s = strings.Join(strings.Fields(s), "")
	if !strings.HasPrefix(s, voucherPrefix) {
		return v, errors.New("voucher must start with " + voucherPrefix)
	}
	b, err := hex.DecodeString(s[len(voucherPrefix):])
	if err != nil || len(b) < 20+8+65+1 {
		return v, errors.New("voucher is malformed")
	}
	copy(v.Author[:], b[:20])
	v.Deadline = binary.BigEndian.Uint64(b[20:28])
	copy(v.Sig[:], b[28:93])
	v.Blob = b[93:]
	return v, nil
}

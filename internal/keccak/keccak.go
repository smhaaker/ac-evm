// Package keccak is Ethereum's Keccak-256 (0x01 padding, not NIST SHA3).
package keccak

import "encoding/binary"

const (
	rate      = 136 // 1088-bit rate for Keccak-256
	rounds    = 24
	digestLen = 32
)

var roundConstants = [rounds]uint64{
	0x0000000000000001, 0x0000000000008082, 0x800000000000808a, 0x8000000080008000,
	0x000000000000808b, 0x0000000080000001, 0x8000000080008081, 0x8000000000008009,
	0x000000000000008a, 0x0000000000000088, 0x0000000080008009, 0x000000008000000a,
	0x000000008000808b, 0x800000000000008b, 0x8000000000008089, 0x8000000000008003,
	0x8000000000008002, 0x8000000000000080, 0x000000000000800a, 0x800000008000000a,
	0x8000000080008081, 0x8000000000008080, 0x0000000080000001, 0x8000000080008008,
}

// rotationOffsets[x][y] is the rho step's rotation for lane (x, y).
var rotationOffsets = [5][5]uint{
	{0, 36, 3, 41, 18},
	{1, 44, 10, 45, 2},
	{62, 6, 43, 15, 61},
	{28, 55, 25, 21, 56},
	{27, 20, 39, 8, 14},
}

func rotl(x uint64, n uint) uint64 {
	n &= 63
	if n == 0 {
		return x
	}
	return (x << n) | (x >> (64 - n))
}

// keccakF is the Keccak-f[1600] permutation over a 5x5 lane state.
func keccakF(a *[5][5]uint64) {
	var c [5]uint64
	var d [5]uint64
	var b [5][5]uint64

	for r := 0; r < rounds; r++ {
		// theta
		for x := 0; x < 5; x++ {
			c[x] = a[x][0] ^ a[x][1] ^ a[x][2] ^ a[x][3] ^ a[x][4]
		}
		for x := 0; x < 5; x++ {
			d[x] = c[(x+4)%5] ^ rotl(c[(x+1)%5], 1)
		}
		for x := 0; x < 5; x++ {
			for y := 0; y < 5; y++ {
				a[x][y] ^= d[x]
			}
		}
		// rho + pi
		for x := 0; x < 5; x++ {
			for y := 0; y < 5; y++ {
				b[y][(2*x+3*y)%5] = rotl(a[x][y], rotationOffsets[x][y])
			}
		}
		// chi
		for x := 0; x < 5; x++ {
			for y := 0; y < 5; y++ {
				a[x][y] = b[x][y] ^ (^b[(x+1)%5][y] & b[(x+2)%5][y])
			}
		}
		// iota
		a[0][0] ^= roundConstants[r]
	}
}

// Sum256 returns the Keccak-256 digest of data.
func Sum256(data []byte) [digestLen]byte {
	var a [5][5]uint64

	absorb := func(block []byte) {
		for i := 0; i < rate/8; i++ {
			lane := binary.LittleEndian.Uint64(block[i*8:])
			a[i%5][i/5] ^= lane
		}
		keccakF(&a)
	}

	for len(data) >= rate {
		absorb(data[:rate])
		data = data[rate:]
	}

	// Pad10*1 with the original Keccak domain byte.
	var block [rate]byte
	copy(block[:], data)
	block[len(data)] = 0x01
	block[rate-1] |= 0x80
	absorb(block[:])

	var out [digestLen]byte
	for i := 0; i < digestLen/8; i++ {
		binary.LittleEndian.PutUint64(out[i*8:], a[i%5][i/5])
	}
	return out
}

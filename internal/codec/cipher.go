// Package codec implements HBR's on-the-wire body codec: a chained-XOR LCG
// stream cipher wrapped around gzip+JSON. The cipher constants are verified
// from the client (request build + response decode paths) and confirmed in
// both directions, so they are reliable rather than inferred.
package codec

import "encoding/hex"

const (
	seed = 1156       // 0x00000484
	mult = 0x015A4E35 // 22695477
	inc  = 1
)

// Scramble applies the chained-XOR LCG cipher to p, returning a new slice.
// Each output byte is XORed with a per-byte LCG keystream byte and the
// previous *ciphertext* byte; the LCG state advances after every byte.
func Scramble(p []byte) []byte {
	out := make([]byte, len(p))
	k := uint32(seed)
	var prev byte
	for i := range p {
		c := p[i] ^ prev ^ byte(k)
		out[i] = c
		k = k*mult + inc
		prev = c
	}
	return out
}

// Unscramble inverts Scramble. Because the chain references the previous
// ciphertext byte (available on both sides), encode and decode share the same
// keystream and are proper inverses.
func Unscramble(c []byte) []byte {
	out := make([]byte, len(c))
	k := uint32(seed)
	var prev byte
	for i := range c {
		out[i] = c[i] ^ prev ^ byte(k)
		k = k*mult + inc
		prev = c[i]
	}
	return out
}

func hexs(b []byte) string { return hex.EncodeToString(b) }

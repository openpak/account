package social

import "crypto/rand"

// newUUID returns a random RFC 4122 v4 UUID string.
func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	const hexDigits = "0123456789abcdef"
	out := make([]byte, 36)
	pos := map[int]bool{8: true, 13: true, 18: true, 23: true}
	j := 0
	for i := 0; i < 36; i++ {
		if pos[i] {
			out[i] = '-'
			continue
		}
		out[i] = hexDigits[b[j>>1]>>(4-(j&1)*4)&0x0f]
		j++
	}
	return string(out)
}

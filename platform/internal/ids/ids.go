// Package ids mints the one kind of identifier the state contract accepts: UUID v7, assigned by
// the producer (SP-K01-2).
//
// The schema has no sequence, no identity column and no database-side default on any `id` —
// acceptance/k01-schema.sh probes all three. That is not a stylistic preference: a central counter
// would put a synchronization point in front of every insert, and the queue lives in the same
// database (SP-E02-2). So whoever creates an object names it, and the name is sortable by the time
// it was created, which is what makes `ORDER BY created_at` and the primary key agree without a
// second index.
package ids

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// New returns a UUID version 7 (RFC 9562 §5.7) as its canonical 36-character text: 48 bits of
// Unix milliseconds, then 74 bits of randomness, with the version and variant bits set.
func New() string {
	return NewAt(time.Now())
}

// NewAt is New with the timestamp given, so a test can state the ordering it expects instead of
// racing the clock for it.
func NewAt(t time.Time) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand on Linux does not fail; if it ever did, minting an identifier from a
		// degraded source would be worse than stopping.
		panic(fmt.Sprintf("no randomness for a UUID v7: %v", err))
	}

	ms := uint64(t.UnixMilli())
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)

	b[6] = (b[6] & 0x0f) | 0x70 // version 7
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10

	var out [36]byte
	hex.Encode(out[0:8], b[0:4])
	out[8] = '-'
	hex.Encode(out[9:13], b[4:6])
	out[13] = '-'
	hex.Encode(out[14:18], b[6:8])
	out[18] = '-'
	hex.Encode(out[19:23], b[8:10])
	out[23] = '-'
	hex.Encode(out[24:36], b[10:16])
	return string(out[:])
}

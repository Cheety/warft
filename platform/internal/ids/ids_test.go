package ids

import (
	"testing"
	"time"
)

func TestNewIsAUUIDv7(t *testing.T) {
	id := New()
	if len(id) != 36 {
		t.Fatalf("%q is %d characters, not 36", id, len(id))
	}
	for _, i := range []int{8, 13, 18, 23} {
		if id[i] != '-' {
			t.Fatalf("%q is not in canonical form", id)
		}
	}
	if id[14] != '7' {
		t.Errorf("version nibble is %q, not 7 — the state contract takes UUID v7 (SP-K01-2)", id[14])
	}
	switch id[19] {
	case '8', '9', 'a', 'b':
	default:
		t.Errorf("variant nibble is %q, not one of 8, 9, a, b", id[19])
	}
}

// Sortable by creation time is the property that lets the primary key and `ORDER BY created_at`
// agree without a second index.
func TestNewAtSortsByTime(t *testing.T) {
	base := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	early := NewAt(base)
	late := NewAt(base.Add(time.Second))
	if !(early < late) {
		t.Errorf("%s does not sort before %s", early, late)
	}
}

func TestNewIsNotTheSameTwice(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := New()
		if seen[id] {
			t.Fatalf("%s was minted twice", id)
		}
		seen[id] = true
	}
}

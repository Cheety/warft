package budget

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"
)

// HaltFile is SP-E08-3's second path: a file on the control node, read at every admission decision.
// The first path is the `halt` row in the state database, written over the API. Both are read; either
// one halts the cell.
//
// The file exists for the case in which you need it — the API no longer answers. It is therefore
// deliberately something a person can write with an editor over a serial console, and something the
// admission decision reads without a database.
const HaltFile = "/var/lib/workpod/halt"

// HaltFilePath is HaltFile, unless a probe moved it. WORKPOD_HALT_FILE is the seam
// acceptance/v04-budget.sh uses and nothing else: on a control node the path is a constant of the
// program, the way the state database's socket is (SP-A04-4).
func HaltFilePath() string {
	if p := os.Getenv("WORKPOD_HALT_FILE"); p != "" {
		return p
	}
	return HaltFile
}

// HaltExpiry is SP-E08-4, and it is mandatory rather than a convenience: a state that needs
// attention in order to persist disappears by itself. `halt.renew` over the API rewrites the row;
// on the file path, touching the file renews it — see decisions/halt-file.md.
const HaltExpiry = 60 * time.Minute

// Halt is the halt as one of the two paths states it.
type Halt struct {
	InForce   bool
	Reason    string
	SetBy     string
	SetAt     time.Time
	ExpiresAt time.Time
	// Source is which path this came from: "file" for HaltFile, "api" for the state database's
	// `halt` row, "" for a halt that is not in force.
	Source string
}

// Active reports whether this halt still stops admission at the given moment. An expired halt is no
// halt: the expiry is the rule, not a hint (SP-E08-4).
func (h Halt) Active(now time.Time) bool {
	return h.InForce && now.Before(h.ExpiresAt)
}

// Refusal turns an active halt into the answer a sender gets.
func (h Halt) Refusal() Halted {
	return Halted{Reason: h.Reason, SetBy: h.SetBy, Source: h.Source}
}

// ReadHaltFile reads SP-E08-3's file. An absent file is not an error — it is the ordinary state of
// a cell that is running — and any other read error is, because a halt file that cannot be read
// must never be mistaken for a cell that is not halted.
//
// The format is one `key: value` per line, so it stays writable by hand:
//
//	reason: the model provider is answering nonsense
//	set_by: the duty officer's name
//	set_at: 2026-07-28T09:12:00Z
//
// `set_at` may be omitted; the file's modification time then says when it was set, which is what
// makes `touch` a renewal (decisions/halt-file.md).
func ReadHaltFile(path string) (Halt, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return Halt{}, nil
	}
	if err != nil {
		return Halt{}, fmt.Errorf("the halt file at %s cannot be read, and an unreadable halt is not an absent one: %w", path, err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return Halt{}, fmt.Errorf("the halt file at %s cannot be read, and an unreadable halt is not an absent one: %w", path, err)
	}

	h := Halt{InForce: true, Source: "file", SetAt: info.ModTime()}
	sc := bufio.NewScanner(strings.NewReader(string(body)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch key {
		case "reason":
			h.Reason = value
		case "set_by":
			h.SetBy = value
		case "set_at":
			t, err := time.Parse(time.RFC3339, value)
			if err != nil {
				return Halt{}, fmt.Errorf("the halt file states set_at %q, which is not RFC 3339 — a halt whose age is unreadable cannot expire (SP-E08-4)", value)
			}
			// The later of the two: a file that was touched to renew it is renewed, whatever the
			// line inside it still says.
			if t.After(h.SetAt) {
				h.SetAt = t
			}
		}
	}
	if err := sc.Err(); err != nil {
		return Halt{}, err
	}
	if h.Reason == "" {
		// SP-E08-2: a rationale is mandatory for halt.set. The halt still stands — refusing to
		// honour a halt because its note is missing would be the wrong direction of failure.
		h.Reason = "no rationale in " + path + " — a halt states one (SP-E08-2)"
	}
	if h.SetBy == "" {
		h.SetBy = "unnamed"
	}
	h.ExpiresAt = h.SetAt.Add(HaltExpiry)
	return h, nil
}

// ReadHalt reads both of SP-E08-3's paths and returns the one that stops admission. The file is read
// first and on its own: the whole reason it exists is that the other path may be unreachable, so a
// database error must not stop the file from being honoured.
//
// api is the halt as the state database holds it, or the zero Halt when the database could not be
// asked; dbErr says why it could not. A halt in force on either path wins; two halts in force answer
// with the file's, because that is the one somebody wrote when the other path was not working.
func ReadHalt(path string, api Halt, dbErr error, now time.Time) (Halt, error) {
	file, err := ReadHaltFile(path)
	if err != nil {
		return Halt{}, err
	}
	if file.Active(now) {
		return file, nil
	}
	if api.Active(now) {
		return api, nil
	}
	if dbErr != nil {
		// Not halted, but not known either: the file said nothing and the row could not be read.
		// Admission decides on the file alone in that case, which is exactly what the second path
		// is for — the caller is told so rather than being handed a confident "no halt".
		return Halt{}, dbErr
	}
	return Halt{}, nil
}

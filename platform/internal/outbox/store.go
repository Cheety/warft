package outbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// DefaultDir is where a work node's outbox lies: on /var, beside the rest of what must survive a
// worker restart (SP-K03-6, SP-A05-1). Not /run — that dies with the boot — and not /data/work,
// which a reinstall wipes (SP-A05-1, AB-A05-1).
const DefaultDir = "/var/lib/workpod/outbox"

// Store is the outbox: a directory of files, one per domain key.
//
// There is no index, no database and no lock. The file name *is* the key (Key.ID), and recording is
// a create with O_EXCL — so two workers that both decided to push the same patch race on one
// `open(2)` and exactly one of them wins, whatever either of them believes about the other. That is
// the property V-02 needs at this one place, and the reason it is a filesystem primitive rather
// than a leader election is that it holds while the control plane is down.
type Store struct{ Dir string }

// New opens a store at a directory. The directory is created on the first write, not here: a
// process that only reads an outbox (a probe, a report) must not create one by looking.
func New(dir string) *Store {
	if dir == "" {
		dir = DefaultDir
	}
	return &Store{Dir: dir}
}

func (s *Store) path(k Key) string { return filepath.Join(s.Dir, k.ID()+".json") }

// Record is the first link of SP-K03-1's chain: the pod produced an intent, and this writes it
// down. It never executes anything.
//
// The bool answers "was this fresh?" — false means the domain key was already in the outbox and the
// entry returned is the one that was there. A caller that ignores it and executes anyway is the
// double push this package exists to make impossible, which is why the existing entry comes back
// rather than an error: the second attempt learns the first one's state.
func (s *Store) Record(e Entry) (Entry, bool, error) {
	k := e.Key()
	if err := k.Valid(); err != nil {
		return Entry{}, false, err
	}
	if err := os.MkdirAll(s.Dir, 0o750); err != nil {
		return Entry{}, false, err
	}
	now := time.Now().UTC()
	e.State, e.Recorded, e.Changed = Recorded, now, now

	body, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return Entry{}, false, err
	}
	f, err := os.OpenFile(s.path(k), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			// The domain key decided, and it decided against a second effect. Whoever holds the
			// entry now is the one execution there will be (SP-K03-2).
			have, err := s.Get(k)
			return have, false, err
		}
		return Entry{}, false, err
	}
	defer f.Close()
	if _, err := f.Write(append(body, '\n')); err != nil {
		return Entry{}, false, err
	}
	return e, true, f.Sync()
}

// Get reads one entry.
func (s *Store) Get(k Key) (Entry, error) {
	body, err := os.ReadFile(s.path(k))
	if err != nil {
		if os.IsNotExist(err) {
			return Entry{}, fmt.Errorf("%w: %s", ErrNotFound, k)
		}
		return Entry{}, err
	}
	var e Entry
	return e, json.Unmarshal(body, &e)
}

// Begin is the register's first half (SP-K03-4): the entry is moved to Executing and written to
// disk *before* the gate is called, so a crash between the two leaves evidence that the effect may
// have happened.
//
// It is also the one place in the system where retrying is forbidden. An entry found already in
// Executing has been handed to a gate once before by a process that then died:
//
//   - requires_register — the target is not idempotent, so a second call may send a second email.
//     The entry moves to Asking, which is terminal, and ErrAskDoNotRetry comes back.
//   - otherwise — the domain key makes the second call the same call, and the gate's ledger
//     recognizes it. Executing again is allowed, and it is not a retry in the harmful sense.
func (s *Store) Begin(k Key) (Entry, error) {
	e, err := s.Get(k)
	if err != nil {
		return Entry{}, err
	}
	switch e.State {
	case Acknowledged, Denied:
		return e, fmt.Errorf("entry %s is already %s — nothing more happens to it", k, e.State)
	case Asking:
		return e, ErrAskDoNotRetry
	case Executing:
		if e.RequiresRegister {
			e.State, e.Cause = Asking, "the gate was called and never acknowledged; a non-idempotent target may not be called twice (SP-K03-4)"
			if err := s.write(e); err != nil {
				return e, err
			}
			return e, ErrAskDoNotRetry
		}
	}
	e.State, e.Executing = Executing, time.Now().UTC()
	e.Attempt++
	return e, s.write(e)
}

// Acknowledge is the register's third half and the last link of SP-K03-1's chain: the receipt back
// into the job.
func (s *Store) Acknowledge(k Key, r Receipt) (Entry, error) {
	e, err := s.Get(k)
	if err != nil {
		return Entry{}, err
	}
	if r.At.IsZero() {
		r.At = time.Now().UTC()
	}
	e.State, e.Receipt = Acknowledged, &r
	return e, s.write(e)
}

// Deny records a gate's refusal. A terminal state, and never without a cause — SP-B02-5 wants a
// rejected target in the display and not only in the log, and a state without a reason is not
// something a display can show.
func (s *Store) Deny(k Key, cause string) (Entry, error) {
	e, err := s.Get(k)
	if err != nil {
		return Entry{}, err
	}
	if cause == "" {
		return e, errors.New("a denial without a cause is not a terminal state (SP-K02-4)")
	}
	e.State, e.Cause = Denied, cause
	return e, s.write(e)
}

// Ask moves an entry to Asking without a gate call ever having been made — the operator's half of
// SP-K03-4, for the case where the answer is known to be missing before Begin is tried again.
func (s *Store) Ask(k Key, cause string) (Entry, error) {
	e, err := s.Get(k)
	if err != nil {
		return Entry{}, err
	}
	e.State, e.Cause = Asking, cause
	return e, s.write(e)
}

// write replaces an entry atomically. A rename over the same directory, so a reader never sees half
// a state: the entry either says Executing or it does not, and a crash mid-write cannot invent a
// third answer.
func (s *Store) write(e Entry) error {
	e.Changed = time.Now().UTC()
	body, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	final := s.path(e.Key())
	tmp, err := os.CreateTemp(s.Dir, ".entry-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(body, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o640); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), final)
}

// List is every entry in the outbox, oldest first. Reading the directory rather than an index is
// the point: the files are the state, so there is nothing that can disagree with them.
func (s *Store) List() ([]Entry, error) {
	names, err := os.ReadDir(s.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Entry
	for _, n := range names {
		if n.IsDir() || !strings.HasSuffix(n.Name(), ".json") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(s.Dir, n.Name()))
		if err != nil {
			return nil, err
		}
		var e Entry
		if err := json.Unmarshal(body, &e); err != nil {
			return nil, fmt.Errorf("%s: %w", n.Name(), err)
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Recorded.Before(out[j].Recorded) })
	return out, nil
}

// Pending is what a drain has work to do about: recorded, and — for idempotent targets only —
// executing entries left behind by a worker that died. Asking is not pending; that is the state
// whose whole meaning is that the machine stops and a human starts.
func (s *Store) Pending() ([]Entry, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, e := range all {
		if e.State == Recorded || (e.State == Executing && !e.RequiresRegister) {
			out = append(out, e)
		}
	}
	return out, nil
}

// Unanswered is what a human is asked about: entries the gate was called for and never answered,
// on a target that may not be called twice. The list SP-K03-4's second sentence produces.
func (s *Store) Unanswered() ([]Entry, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, e := range all {
		if e.State == Asking || (e.State == Executing && e.RequiresRegister) {
			out = append(out, e)
		}
	}
	return out, nil
}

// -------------------------------------------------------------------------------------------------
// The gate's side of the same mechanism.
// -------------------------------------------------------------------------------------------------

// Ledger is what a gate keeps, and it is the outbox's mechanism turned around: the outbox
// deduplicates the *intent*, the ledger deduplicates the *execution*.
//
// Both are needed, and they are not the same claim. A worker that crashed between calling the gate
// and writing the receipt calls again on the next drain; the outbox — on another machine — has no
// way to know the push already happened, and the gate does, because it is the one that did it.
// decisions/gates-and-the-outbox.md §2.
type Ledger struct{ store *Store }

// OpenLedger opens a gate's ledger at a directory.
func OpenLedger(dir string) *Ledger { return &Ledger{store: New(dir)} }

// Once executes an effect at most once per domain key, ever.
//
// The bool is "did this run execute it?" — false means the receipt came out of the ledger and
// nothing was done. That is the sentence AB-K03-2 checks: two attempts, one push.
//
// The order is SP-K03-4's, on the gate's side too: the ledger records the attempt before `exec` is
// called, so an execution that crashes the gate leaves a mark. On a non-idempotent target that mark
// is what stops the second call.
func (l *Ledger) Once(e Entry, exec func() (Receipt, error)) (Receipt, bool, error) {
	k := e.Key()
	existing, _, err := l.store.Record(e)
	if err != nil {
		return Receipt{}, false, err
	}
	if existing.State == Acknowledged && existing.Receipt != nil {
		return *existing.Receipt, false, nil
	}
	if existing.State == Denied {
		return Receipt{}, false, fmt.Errorf("%s was refused before and the refusal stands: %s", k, existing.Cause)
	}
	if _, err := l.store.Begin(k); err != nil {
		return Receipt{}, false, err
	}
	r, err := exec()
	if err != nil {
		if _, dErr := l.store.Deny(k, err.Error()); dErr != nil {
			return Receipt{}, false, dErr
		}
		return Receipt{}, false, err
	}
	if _, err := l.store.Acknowledge(k, r); err != nil {
		return Receipt{}, false, err
	}
	return r, true, nil
}

// Seen reports whether an id has been through this ledger before, and records it if not.
//
// This is SP-K03-5's half: replies into channels are events, and the adapter deduplicates via the
// *event id* rather than a domain key, because an event has no content hash and no target — it has
// an identity given to it by whoever produced it. A control plane that restarts and republishes
// finds `true` here and the channel sees no second message (AB-K03-5).
func (l *Ledger) Seen(id string) (bool, error) {
	if id == "" {
		return false, errors.New("an event without an id cannot be deduplicated (SP-K03-5)")
	}
	// An event is not an effect: it is recorded under a key of its own shape, and the store's
	// scheme check would refuse it as a target. `event:` is that shape, and the entry is written
	// straight rather than through Record.
	k := Key{Order: "event", Target: "event:" + id, ContentHash: id}
	if _, err := l.store.Get(k); err == nil {
		return true, nil
	} else if !errors.Is(err, ErrNotFound) {
		return false, err
	}
	if err := os.MkdirAll(l.store.Dir, 0o750); err != nil {
		return false, err
	}
	now := time.Now().UTC()
	e := Entry{Order: k.Order, Target: k.Target, ContentHash: k.ContentHash,
		State: Acknowledged, Recorded: now, Changed: now, Receipt: &Receipt{Executed: true, ExternalID: id, At: now}}
	body, err := json.Marshal(e)
	if err != nil {
		return false, err
	}
	f, err := os.OpenFile(l.store.path(k), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return true, nil // another process got there first; the message is already out
		}
		return false, err
	}
	defer f.Close()
	if _, err := f.Write(append(body, '\n')); err != nil {
		return false, err
	}
	return false, f.Sync()
}

// Tokens per phase (SP-RB-1), the pool sizes of decisions/phase-tokens.md, and the exclusive
// operation of SP-RB-5 and SP-RB-6.

package scheduling

import (
	"fmt"
	"sort"
	"sync"
)

// Sizes is how many tokens of each class a node offers.
type Sizes struct {
	Net    int
	IO     int
	CPURAM int
}

// SizesFor derives the three pools from the node's allocation, by the ratios of
// decisions/phase-tokens.md. `cores` is what the work layer was allocated — never what the machine
// happens to have and never os.cpus(), which in a container reports the host (SP-RC-5).
func SizesFor(cores int) Sizes {
	if cores < 1 {
		cores = 1
	}
	io := cores / 4
	if io < 1 {
		io = 1
	}
	return Sizes{Net: 8 * cores, IO: io, CPURAM: cores}
}

// Of reads one pool's size by class.
func (s Sizes) Of(c Class) int {
	switch c {
	case ClassNet:
		return s.Net
	case ClassIO:
		return s.IO
	case ClassCPURAM:
		return s.CPURAM
	}
	return 0
}

// Grant is the answer to one request for a token: which class was asked for, and whether the pod
// may proceed. A refusal names what is in the way, because a pod that is not admitted to a phase is
// a pod somebody will ask about.
type Grant struct {
	Pod       string
	Class     Class
	Granted   bool
	Exclusive bool
	Reason    string
}

// Pool is the three token pools of one node, and the only place a pod's right to run a phase comes
// from.
//
// It is a runtime object over a ruled table: the sizes come from SizesFor, the phase-to-class join
// comes from the embedded ruling, and nothing here decides either. What it does own is who holds
// what right now — which is state, and therefore has a mutex rather than a file.
type Pool struct {
	mu     sync.Mutex
	sizes  Sizes
	held   map[Class]int
	holder map[string]Class
	tokens PhaseTokens

	// exclusive is the pod holding all cpu·ram tokens under SP-RB-5, or empty. It is one pod at
	// most, ever: SP-RB-6 says two large runs are never time-sliced, and a second exclusive holder
	// is exactly that.
	exclusive string

	// blocked is SP-RC-3's second rung. Under it the pool grants nothing new; what is held stays
	// held, because blocking admission is not the same as taking a token back (that is the third
	// rung).
	blocked bool
}

// NewPool is a node's three pools at their ruled sizes.
func NewPool(s Sizes) *Pool {
	return &Pool{
		sizes:  s,
		held:   map[Class]int{},
		holder: map[string]Class{},
		tokens: RuledTokens(),
	}
}

// Sizes is what the pool was built with. SetIO moves the io row and nothing else.
func (p *Pool) Sizes() Sizes {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sizes
}

// Enter is a pod asking for the token of the phase it is about to run. It holds that one token and
// no other: entering a new phase returns the token of the old one first, which is SP-RB-1's "a pod
// holds only the token of its current phase" as a single operation rather than as two the caller
// could get wrong.
func (p *Pool) Enter(pod string, phase Phase) (Grant, error) {
	class, err := p.tokens.Of(phase)
	if err != nil {
		return Grant{Pod: pod}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	// Returned before the new one is asked for, so a pod moving from `check` to `deliver` never
	// needs two tokens at once to keep the one it had.
	p.release(pod)

	if p.blocked {
		return Grant{Pod: pod, Class: class, Reason: "admission is blocked: the pressure ladder stands on `block` (SP-RC-3)"}, nil
	}
	if p.exclusive != "" && p.exclusive != pod && class == ClassCPURAM {
		return Grant{Pod: pod, Class: class,
			Reason: fmt.Sprintf("pod %s holds every cpu·ram token in exclusive operation (SP-RB-5)", p.exclusive)}, nil
	}
	if p.held[class] >= p.sizes.Of(class) {
		return Grant{Pod: pod, Class: class,
			Reason: fmt.Sprintf("no %s token free: %d of %d held", class, p.held[class], p.sizes.Of(class))}, nil
	}
	p.held[class]++
	p.holder[pod] = class
	return Grant{Pod: pod, Class: class, Granted: true}, nil
}

// EnterExclusive is SP-RB-5: a job whose predicted peak RSS is above ~60 % of the available RAM
// holds *all* cpu·ram tokens for the phase it is entering, and no second such job runs beside it.
//
// It succeeds only when every cpu·ram token is free. That is the difference between exclusive
// operation and a priority: an exclusive run does not evict, it waits — and the pods that are
// running are frozen at the next phase boundary by the caller, which is where "at the next phase
// boundary" can be observed at all.
func (p *Pool) EnterExclusive(pod string) (Grant, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.blocked {
		return Grant{Pod: pod, Class: ClassCPURAM, Exclusive: true,
			Reason: "admission is blocked: the pressure ladder stands on `block` (SP-RC-3)"}, nil
	}
	if p.exclusive != "" && p.exclusive != pod {
		// SP-RB-6, in one line: never two large runs at once, not even a little of each.
		return Grant{Pod: pod, Class: ClassCPURAM, Exclusive: true,
			Reason: fmt.Sprintf("pod %s is already the exclusive run; two large runs are never time-sliced (SP-RB-6)", p.exclusive)}, nil
	}
	p.release(pod)
	if p.held[ClassCPURAM] > 0 {
		return Grant{Pod: pod, Class: ClassCPURAM, Exclusive: true,
			Reason: fmt.Sprintf("%d cpu·ram token(s) still held; an exclusive run waits for all of them (SP-RB-5)", p.held[ClassCPURAM])}, nil
	}
	p.held[ClassCPURAM] = p.sizes.CPURAM
	p.holder[pod] = ClassCPURAM
	p.exclusive = pod
	return Grant{Pod: pod, Class: ClassCPURAM, Exclusive: true, Granted: true}, nil
}

// Wait is SP-RB-1's closing sentence: a pod that begins waiting for a model response returns its
// token beforehand. It keeps its state, its snapshot and its place — it returns a right to compute
// it is not using.
func (p *Pool) Wait(pod string) Class {
	p.mu.Lock()
	defer p.mu.Unlock()
	class := p.holder[pod]
	p.release(pod)
	return class
}

// Leave returns whatever a pod holds. A pod that holds nothing may leave; the reaper calls this for
// pods it collected, and a second call is not an error (SP-T04-5 sweeps what is already gone).
func (p *Pool) Leave(pod string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.release(pod)
}

// release is the unlocked half. Returning the exclusive run's token returns every cpu·ram token,
// because that is what it took.
func (p *Pool) release(pod string) {
	class, ok := p.holder[pod]
	if !ok {
		return
	}
	delete(p.holder, pod)
	if p.exclusive == pod {
		p.exclusive = ""
		p.held[ClassCPURAM] = 0
		return
	}
	p.held[class]--
	if p.held[class] < 0 {
		p.held[class] = 0
	}
}

// Holds reports the class a pod currently holds, and whether it holds anything at all. AB-RB-1 is
// this function answering `false` for a pod that is waiting for a model.
func (p *Pool) Holds(pod string) (Class, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	c, ok := p.holder[pod]
	return c, ok
}

// Held is how many tokens of one class are out.
func (p *Pool) Held(c Class) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.held[c]
}

// Free is how many are left. Under exclusive operation the cpu·ram row is zero for everyone but the
// exclusive holder, which is the point of it.
func (p *Pool) Free(c Class) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	free := p.sizes.Of(c) - p.held[c]
	if free < 0 {
		return 0
	}
	return free
}

// Exclusive names the pod in exclusive operation, or "".
func (p *Pool) Exclusive() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exclusive
}

// SetIO is SP-RC-2's reaction to `io full avg10 > 20 %`: I/O tokens to 1, installations serialized.
// It lowers the ceiling and never takes a token back — a pod already preparing keeps its token and
// finishes; what the reaction stops is the next one starting.
func (p *Pool) SetIO(n int) {
	if n < 1 {
		n = 1
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sizes.IO = n
}

// Block is SP-RC-3's second rung: no admission. Nothing held is returned, which is what makes it
// the rung *before* freeze rather than a gentler version of it.
func (p *Pool) Block(on bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.blocked = on
}

// Blocked reports whether the pool is on the `block` rung.
func (p *Pool) Blocked() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.blocked
}

// Snapshot is the pool as a report reads it: what each class offers, what is out, and who is
// holding it.
type Snapshot struct {
	Sizes     Sizes             `json:"sizes"`
	Held      map[string]int    `json:"held"`
	Holders   map[string]string `json:"holders"`
	Exclusive string            `json:"exclusive,omitempty"`
	Blocked   bool              `json:"blocked"`
}

// Snapshot reads the pool out whole.
func (p *Pool) Snapshot() Snapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := Snapshot{Sizes: p.sizes, Held: map[string]int{}, Holders: map[string]string{},
		Exclusive: p.exclusive, Blocked: p.blocked}
	for _, c := range Classes() {
		s.Held[string(c)] = p.held[c]
	}
	for pod, c := range p.holder {
		s.Holders[pod] = string(c)
	}
	return s
}

// Pods lists who holds a token, sorted, so a report is the same twice.
func (p *Pool) Pods() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	pods := make([]string, 0, len(p.holder))
	for pod := range p.holder {
		pods = append(pods, pod)
	}
	sort.Strings(pods)
	return pods
}

// ExclusiveShare is SP-RB-5's "~60 % of the available RAM". A job whose predicted peak RSS is above
// this share of what the node has runs alone.
const ExclusiveShare = 0.60

// NeedsExclusive answers SP-RB-5 for one job: does this predicted peak take enough of the node that
// nothing else may compute beside it?
//
// It is asked of a *predicted* peak — a number from three measured runs (SP-RC-6) — and never of
// E-05's planning constants, which SP-RD-3 keeps out of admission and preemption.
func NeedsExclusive(peakRSSBytes, availableBytes int64) bool {
	if peakRSSBytes <= 0 || availableBytes <= 0 {
		return false
	}
	return float64(peakRSSBytes) > ExclusiveShare*float64(availableBytes)
}

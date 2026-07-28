package scheduling

import (
	"testing"
	"time"
)

func TestRuledTokensCoversTheSpineOnce(t *testing.T) {
	tokens := RuledTokens()
	rows := tokens.Rows()
	if len(rows) != 7 {
		t.Fatalf("T-05's spine has seven phases, the ruling has %d rows", len(rows))
	}
	// The one row the work package opens with: a pod that is waiting for a model must not be
	// holding the bottleneck.
	if c, err := tokens.Of("edit"); err != nil || c != ClassNet {
		t.Fatalf("edit holds %q (%v); decisions/phase-tokens.md puts it on net", c, err)
	}
	if c, err := tokens.Of("check"); err != nil || c != ClassCPURAM {
		t.Fatalf("check holds %q (%v); SP-RB-1 says checking is the bottleneck", c, err)
	}
	if _, err := tokens.Of("compile"); err == nil {
		t.Fatal("an unruled phase got a token; a guessed token is a phase nobody ruled")
	}
}

func TestSizesFollowTheRuledRatios(t *testing.T) {
	for _, c := range []struct {
		cores           int
		net, io, cpuram int
	}{
		{1, 8, 1, 1},
		{4, 32, 1, 4},
		{12, 96, 3, 12},
		{0, 8, 1, 1}, // a node that reports no cores still runs one thing at a time
	} {
		got := SizesFor(c.cores)
		if got.Net != c.net || got.IO != c.io || got.CPURAM != c.cpuram {
			t.Errorf("SizesFor(%d) = %+v, ruled %d/%d/%d", c.cores, got, c.net, c.io, c.cpuram)
		}
	}
}

// AB-RB-1: a waiting pod holds no CPU token.
func TestWaitingPodHoldsNoToken(t *testing.T) {
	p := NewPool(SizesFor(2))
	if g, err := p.Enter("pod-a", "check"); err != nil || !g.Granted || g.Class != ClassCPURAM {
		t.Fatalf("entering check: %+v %v", g, err)
	}
	if p.Held(ClassCPURAM) != 1 {
		t.Fatalf("one pod in check holds %d cpu·ram tokens", p.Held(ClassCPURAM))
	}
	if left := p.Wait("pod-a"); left != ClassCPURAM {
		t.Fatalf("waiting returned %q", left)
	}
	if _, holds := p.Holds("pod-a"); holds {
		t.Fatal("a pod waiting for a model response still holds a token (SP-RB-1)")
	}
	if p.Held(ClassCPURAM) != 0 {
		t.Fatalf("%d cpu·ram tokens still out after the holder began waiting", p.Held(ClassCPURAM))
	}
}

func TestOnePodHoldsOneToken(t *testing.T) {
	p := NewPool(SizesFor(4))
	if _, err := p.Enter("pod-a", "prepare"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Enter("pod-a", "check"); err != nil {
		t.Fatal(err)
	}
	if p.Held(ClassIO) != 0 {
		t.Fatalf("the io token was not returned when the pod left prepare: %d out", p.Held(ClassIO))
	}
	if p.Held(ClassCPURAM) != 1 {
		t.Fatalf("cpu·ram held %d after entering check", p.Held(ClassCPURAM))
	}
}

func TestPoolRefusesWhenTheBottleneckIsFull(t *testing.T) {
	p := NewPool(Sizes{Net: 8, IO: 1, CPURAM: 1})
	if g, _ := p.Enter("pod-a", "check"); !g.Granted {
		t.Fatal("the first check was refused")
	}
	g, err := p.Enter("pod-b", "check")
	if err != nil {
		t.Fatal(err)
	}
	if g.Granted {
		t.Fatal("two pods computing on a one-token pool")
	}
	if g.Reason == "" {
		t.Fatal("a refusal without a reason is a pod nobody can explain")
	}
	// The same pod may plan while it waits for a cpu·ram token: net is where waiting belongs.
	if g, _ := p.Enter("pod-b", "plan"); !g.Granted {
		t.Fatal("planning was refused while only the bottleneck was full")
	}
}

// AB-RB-5: a job above ~60 % RAM holds all CPU tokens.
func TestExclusiveOperationHoldsEveryCPUToken(t *testing.T) {
	if !NeedsExclusive(7<<30, 10<<30) {
		t.Fatal("7 GB of 10 GB is above 60 % and must run alone (SP-RB-5)")
	}
	if NeedsExclusive(5<<30, 10<<30) {
		t.Fatal("5 GB of 10 GB is below 60 % and must not take the node")
	}

	p := NewPool(SizesFor(8))
	g, err := p.EnterExclusive("pod-large")
	if err != nil || !g.Granted {
		t.Fatalf("exclusive operation refused on an idle node: %+v %v", g, err)
	}
	if p.Free(ClassCPURAM) != 0 {
		t.Fatalf("%d cpu·ram tokens are still free beside an exclusive run", p.Free(ClassCPURAM))
	}
	if g, _ := p.Enter("pod-b", "check"); g.Granted {
		t.Fatal("a second pod got a cpu·ram token beside an exclusive run")
	}
	// SP-RB-6: two large runs are never time-sliced.
	if g, _ := p.EnterExclusive("pod-large-2"); g.Granted {
		t.Fatal("two large runs at once")
	}
	// Everything that is not the bottleneck keeps running: exclusive operation is about CPU and
	// RAM, not about stopping the node.
	if g, _ := p.Enter("pod-c", "plan"); !g.Granted {
		t.Fatal("planning was refused beside an exclusive run")
	}
	p.Leave("pod-large")
	if p.Free(ClassCPURAM) != 8 {
		t.Fatalf("after the exclusive run left, %d of 8 cpu·ram tokens are free", p.Free(ClassCPURAM))
	}
	if g, _ := p.EnterExclusive("pod-large-2"); !g.Granted {
		t.Fatal("the second large run did not get the node after the first left")
	}
}

func TestExclusiveWaitsForTheTokensItNeeds(t *testing.T) {
	p := NewPool(SizesFor(2))
	if g, _ := p.Enter("pod-a", "check"); !g.Granted {
		t.Fatal("the first check was refused")
	}
	g, _ := p.EnterExclusive("pod-large")
	if g.Granted {
		t.Fatal("an exclusive run started while another pod was computing; it waits, it does not evict")
	}
}

func TestBlockRungStopsAdmissionAndKeepsWhatIsHeld(t *testing.T) {
	p := NewPool(SizesFor(4))
	if g, _ := p.Enter("pod-a", "check"); !g.Granted {
		t.Fatal("setup")
	}
	p.Block(true)
	if g, _ := p.Enter("pod-b", "plan"); g.Granted {
		t.Fatal("a pod was admitted while the ladder stood on `block`")
	}
	if _, holds := p.Holds("pod-a"); !holds {
		t.Fatal("blocking admission took a token back; that is the freeze rung, not the block rung")
	}
}

func TestSetIOIsSPRC2sReaction(t *testing.T) {
	p := NewPool(SizesFor(12)) // io = 3
	if p.Sizes().IO != 3 {
		t.Fatalf("io pool is %d on twelve cores", p.Sizes().IO)
	}
	p.SetIO(1)
	if g, _ := p.Enter("pod-a", "prepare"); !g.Granted {
		t.Fatal("the first prepare was refused after io went to 1")
	}
	if g, _ := p.Enter("pod-b", "reap"); g.Granted {
		t.Fatal("two io phases at once after SP-RC-2 cut the pool to 1")
	}
}

func TestPhaseTokensParseRejectsAnUnknownClass(t *testing.T) {
	if _, err := parsePhaseTokens("phase\ttoken\nplan\tdisk\n"); err == nil {
		t.Fatal("a fourth token class was accepted; SP-RB-1 names three")
	}
	if _, err := parsePhaseTokens("phase\ttoken\nplan\tnet\nplan\tio\n"); err == nil {
		t.Fatal("a phase with two tokens was accepted")
	}
}

func at(sec int) time.Time { return time.Unix(1750000000+int64(sec), 0).UTC() }

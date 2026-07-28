package observation

import (
	"bufio"
	_ "embed"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/Cheety/warft/platform/internal/scheduling"
)

//go:embed e05-constants.tsv
var constantSource string

// R-D, the occupancy table, with the two sources SP-RD-2 puts under the same six places.
//
// Planning is an arithmetic over the five constants of E-05 and R-D's own sliders; operation is the
// same six places read from the cell and from the kernel. "It does not estimate" is the sentence
// that decides everything below: in operation a place either carries a number somebody measured, or
// it carries no number and says why. The one place that is arithmetic all the way down — how many
// active places a node has — therefore stays empty in operation, because what a node admits *now*
// is decided from pressure and not from a plan (SP-RC-1, SP-RD-3).

// Place is one field of the table: a value, and where it came from. The source travels with the
// value so that a display cannot silently mix the two — which is the failure SP-RD-2 is written
// against.
type Place struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Source string `json:"source"` // "planned" · "measured" · "not measured"
	Why    string `json:"why,omitempty"`
}

// Table is R-D's six places, in the order the panel shows them.
type Table struct {
	Mode   string  `json:"mode"` // "planning" or "operation"
	Places []Place `json:"places"`
}

// Sliders are R-D's own, left of the table: one node, and the cell around it.
type Sliders struct {
	RAMGigabytes int
	Cores        int
	WorkNodes    int
	Fleet        int
	RushPercent  int
}

// Constant is one row of e05-constants.tsv: what E-05 gave, what AP-1.3 measured, and which of the
// two the table computes with.
type Constant struct {
	Key      string  `json:"key"`
	Given    float64 `json:"given"`
	Measured float64 `json:"measured"`
	Unit     string  `json:"unit"`
	Adopted  string  `json:"adopted"`
	Evidence string  `json:"evidence"`
}

// Value is what R-D computes with: the measured number where E-05 adopted it, the given one where
// it did not. `adopted` in the file is prose, and the rule is its first word.
func (c Constant) Value() float64 {
	if strings.HasPrefix(c.Adopted, "measured") {
		return c.Measured
	}
	return c.Given
}

// Constants is the file the binary carries, which is a copy of acceptance/e05-constants.tsv —
// decisions/E-05.md's machine-readable half. acceptance/b03-observation.sh holds the two against
// each other, the way every other ruled table in this program is held against its ruling.
func Constants() []Constant { return append([]Constant(nil), constants...) }

// ConstantOf is one row by key.
func ConstantOf(key string) (Constant, error) {
	for _, c := range constants {
		if c.Key == key {
			return c, nil
		}
	}
	return Constant{}, fmt.Errorf("e05-constants.tsv has no %q — E-05 rules five constants over seven rows", key)
}

var constants = mustParseConstants(constantSource)

func mustParseConstants(src string) []Constant {
	out, err := parseConstants(src)
	if err != nil {
		panic(err)
	}
	return out
}

func parseConstants(src string) ([]Constant, error) {
	var out []Constant
	sc := bufio.NewScanner(strings.NewReader(src))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if f[0] == "key" {
			continue
		}
		if len(f) != 6 {
			return nil, fmt.Errorf("e05-constants.tsv line %d: six columns, not %d", n, len(f))
		}
		given, err := strconv.ParseFloat(f[1], 64)
		if err != nil {
			return nil, fmt.Errorf("e05-constants.tsv line %d: %w", n, err)
		}
		measured, err := strconv.ParseFloat(f[2], 64)
		if err != nil {
			return nil, fmt.Errorf("e05-constants.tsv line %d: %w", n, err)
		}
		out = append(out, Constant{Key: f[0], Given: given, Measured: measured,
			Unit: f[3], Adopted: f[4], Evidence: f[5]})
	}
	return out, sc.Err()
}

// Plan is the table as a design calculation: how many active places a node of this shape offers,
// how many of the fleet are active at this rush, how many wait, and what runs out first.
//
// The arithmetic is R-D's own, and the one thing worth stating in prose is why the zram factor
// divides only the frozen pod: frozen pods lie compressed and active pods lie hot, so compression
// is a property of the base load and not of the computing load.
func Plan(s Sliders) (Table, error) {
	get := func(key string) (float64, error) {
		c, err := ConstantOf(key)
		if err != nil {
			return 0, err
		}
		return c.Value(), nil
	}
	hostMB, err := get("host_runtime_work")
	if err != nil {
		return Table{}, err
	}
	cacheMB, err := get("page_cache_base")
	if err != nil {
		return Table{}, err
	}
	frozenMB, err := get("frozen_pod")
	if err != nil {
		return Table{}, err
	}
	zram, err := get("zram_factor")
	if err != nil {
		return Table{}, err
	}
	podMB, err := get("active_pod_ram")
	if err != nil {
		return Table{}, err
	}
	coresPerPod, err := get("active_pod_cores")
	if err != nil {
		return Table{}, err
	}
	if s.WorkNodes < 1 || zram <= 0 {
		return Table{}, fmt.Errorf("a table needs at least one node and a zram factor above zero")
	}

	frostMB := frozenMB / zram
	perNode := float64(s.Fleet) / float64(s.WorkNodes)
	available := (float64(s.RAMGigabytes) - hostMB/1024 - cacheMB/1024) * 1024
	if available < 0 {
		available = 0
	}
	rest := available - perNode*frostMB
	surcharge := math.Max(podMB-frostMB, 1)

	ramSlots := 0
	if rest > 0 {
		ramSlots = int(rest / surcharge)
	}
	cpuSlots := 0
	if coresPerPod > 0 {
		cpuSlots = int(float64(s.Cores) / coresPerPod)
	}
	slots := min(ramSlots, cpuSlots, int(math.Ceil(perNode)))
	total := min(slots*s.WorkNodes, s.Fleet)
	want := int(float64(s.Fleet)*float64(s.RushPercent)/100 + 0.5)
	active := min(want, total)

	bottleneck := "balanced"
	switch {
	case rest <= 0:
		bottleneck = "fleet"
	case ramSlots < cpuSlots:
		bottleneck = "memory"
	case cpuSlots < ramSlots:
		bottleneck = "cores"
	}

	return Table{Mode: "planning", Places: []Place{
		{Name: "slots", Value: strconv.Itoa(total), Source: "planned"},
		{Name: "active", Value: strconv.Itoa(active), Source: "planned"},
		{Name: "queued", Value: strconv.Itoa(want - active), Source: "planned"},
		{Name: "frozen", Value: strconv.Itoa(s.Fleet - want), Source: "planned"},
		{Name: "per_node", Value: strconv.Itoa(slots), Source: "planned"},
		{Name: "bottleneck", Value: bottleneck, Source: "planned"},
	}}, nil
}

// Reading is what operation puts in front of the same six places: counts out of the state contract,
// and one PSI sample out of the pods slice. Nothing here is computed from a constant.
type Reading struct {
	Active    int `json:"active"`
	Queued    int `json:"queued"`
	Frozen    int `json:"frozen"`
	WorkNodes int `json:"work_nodes"`

	Sample    scheduling.Sample `json:"sample"`
	SampleOf  string            `json:"sample_of"` // the cgroup the pressure files were read from
	HavePSI   bool              `json:"have_psi"`
	HaveCells bool              `json:"have_cells"`
}

// Measure is SP-RD-2: the same display, a different source.
//
// Five of the six places carry a measurement. The sixth — how many active places there are — is
// left empty on purpose: it is the one number that only the design calculation has, and filling it
// in operation would be the table estimating, which SP-RD-2 forbids in as many words.
func Measure(r Reading) Table {
	place := func(name string, n int) Place {
		if !r.HaveCells {
			return Place{Name: name, Source: "not measured", Why: "no cell in front of this table"}
		}
		return Place{Name: name, Value: strconv.Itoa(n), Source: "measured"}
	}
	perNode := Place{Name: "per_node", Source: "not measured", Why: "no work node is registered in this cell"}
	if r.HaveCells && r.WorkNodes > 0 {
		perNode = Place{Name: "per_node", Value: strconv.Itoa(r.Active / r.WorkNodes), Source: "measured"}
	}

	return Table{Mode: "operation", Places: []Place{
		{Name: "slots", Source: "not measured",
			Why: "the number of places is a planning value; what a node admits now is decided from pressure (SP-RC-1, SP-RD-3)"},
		place("active", r.Active),
		place("queued", r.Queued),
		place("frozen", r.Frozen),
		perNode,
		Bottleneck(r),
	}}
}

// Bottleneck is the place SP-RD-2 names by file: in operation it stands for real values from
// `cpu.pressure` and `memory.pressure`, read from the pods slice, and it is decided by SP-RC-2's
// own thresholds rather than by a second opinion about them.
func Bottleneck(r Reading) Place {
	if !r.HavePSI {
		return Place{Name: "bottleneck", Source: "not measured",
			Why: "no pressure files were read — R-D measures, it does not estimate (SP-RD-2)"}
	}
	value := map[scheduling.Signal]float64{
		scheduling.MemoryStalled:     r.Sample.MemoryFullAvg10,
		scheduling.MemoryTight:       r.Sample.MemorySomeAvg10,
		scheduling.IOBottleneck:      r.Sample.IOFullAvg10,
		scheduling.CPUBesideThePoint: r.Sample.CPUSomeAvg60,
	}
	// The order is SP-RC-2's severity, not the file order: everything stalled outranks getting
	// tight, and both outrank a disk or a busy core.
	for _, s := range []scheduling.Signal{scheduling.MemoryStalled, scheduling.MemoryTight,
		scheduling.IOBottleneck, scheduling.CPUBesideThePoint} {
		t, err := scheduling.ThresholdOf(s)
		if err != nil {
			continue
		}
		if value[s] >= t.Enter {
			return Place{Name: "bottleneck", Value: string(s), Source: "measured",
				Why: fmt.Sprintf("%s at %.2f %%, over SP-RC-2's %.0f %% (%s)", s, value[s], t.Enter, r.SampleOf)}
		}
	}
	return Place{Name: "bottleneck", Value: "none", Source: "measured",
		Why: fmt.Sprintf("memory some %.2f %%, memory full %.2f %%, io full %.2f %%, cpu some %.2f %% (%s)",
			r.Sample.MemorySomeAvg10, r.Sample.MemoryFullAvg10, r.Sample.IOFullAvg10,
			r.Sample.CPUSomeAvg60, r.SampleOf)}
}

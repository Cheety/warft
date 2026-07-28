package observation

import (
	"bufio"
	_ "embed"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

//go:embed alerts.tsv
var alertSource string

// WakingSlots is SP-B03-3's number, and the only place in this program it is written as a digit.
// The state contract enforces it too (`alert.waking_slot` is `1..4`, unique); a program that
// carried a different number than the database would make the requirement a matter of which one
// answered first.
const WakingSlots = 4

// Alert is one entry of the catalog: what it is called, whether it wakes a human, which of the four
// slots it holds if it does, what it is measured from and the ruled condition in words.
type Alert struct {
	Name      string `json:"name"`
	Wakes     bool   `json:"wakes"`
	Slot      int    `json:"slot,omitempty"`
	Signal    string `json:"signal"`
	Condition string `json:"condition"`
}

// Catalog is decisions/alerts.md as the binary carries it: the four waking alerts in slot order,
// then the displays.
func Catalog() []Alert { return append([]Alert(nil), ruled...) }

// Waking is the four, in slot order. It is a function rather than a constant list because the
// answer has to come from the same file the ruling generates — a hard-coded four here would be the
// second copy this package exists to avoid.
func Waking() []Alert {
	var out []Alert
	for _, a := range ruled {
		if a.Wakes {
			out = append(out, a)
		}
	}
	return out
}

// Displays is everything else. SP-B03-3's "everything else is a display" is not a leftover: it is
// where the disk lands (SP-A05-5) and where a cluster of rejected targets lands (SP-B02-5).
func Displays() []Alert {
	var out []Alert
	for _, a := range ruled {
		if !a.Wakes {
			out = append(out, a)
		}
	}
	return out
}

// ByName looks one up. An alert nobody ruled has no name here, which is the point.
func ByName(name string) (Alert, error) {
	for _, a := range ruled {
		if a.Name == name {
			return a, nil
		}
	}
	return Alert{}, fmt.Errorf("no alert %q — the catalog is decisions/alerts.md, and it is closed", name)
}

var ruled = mustParseAlerts(alertSource)

func mustParseAlerts(src string) []Alert {
	out, err := parseAlerts(src)
	if err != nil {
		// The file is embedded: a malformed catalog is a broken build, not a runtime condition.
		panic(err)
	}
	return out
}

// parseAlerts reads alerts.tsv and holds it to the two rules that make it a catalog rather than a
// list: the slots are exactly `1..WakingSlots`, each once, and a slot exists exactly where the
// alert wakes somebody. The same two rules stand in the state contract as constraints — this is
// the half that fails the build rather than the insert.
func parseAlerts(src string) ([]Alert, error) {
	var out []Alert
	seen := map[int]string{}
	sc := bufio.NewScanner(strings.NewReader(src))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if f[0] == "name" {
			continue // the header
		}
		if len(f) != 5 {
			return nil, fmt.Errorf("alerts.tsv line %d: five columns, not %d", n, len(f))
		}
		a := Alert{Name: f[0], Signal: f[3], Condition: f[4]}
		switch f[1] {
		case "true":
			a.Wakes = true
		case "false":
		default:
			return nil, fmt.Errorf("alerts.tsv line %d: wakes is true or false, not %q", n, f[1])
		}
		if a.Wakes {
			slot, err := strconv.Atoi(f[2])
			if err != nil || slot < 1 || slot > WakingSlots {
				return nil, fmt.Errorf("alerts.tsv line %d: %s wakes, so it holds one of the %d slots, not %q",
					n, a.Name, WakingSlots, f[2])
			}
			if other, taken := seen[slot]; taken {
				return nil, fmt.Errorf("alerts.tsv line %d: slot %d is %s's already", n, slot, other)
			}
			seen[slot] = a.Name
			a.Slot = slot
		} else if f[2] != "-" {
			return nil, fmt.Errorf("alerts.tsv line %d: %s is a display, so it holds no slot (%q)", n, a.Name, f[2])
		}
		out = append(out, a)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(seen) != WakingSlots {
		return nil, fmt.Errorf("alerts.tsv: %d of the %d waking alerts are named — SP-B03-3 has exactly four",
			len(seen), WakingSlots)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Wakes != out[j].Wakes {
			return out[i].Wakes
		}
		return out[i].Slot < out[j].Slot
	})
	return out, nil
}

// Threshold reads a number out of a ruled condition, so the evaluation and the ruling cannot drift.
//
// The conditions are prose because a duty officer reads them; the numbers in them are what the
// evaluation compares against. `unit` is the text that follows the number — " samples",
// "x the mean", " % of a cap" — and the number is what stands immediately in front of it. Reading
// the prose keeps decisions/alerts.md the single source of both halves: a threshold that moved in
// the ruling and not here fails the lookup rather than quietly leaving the old number in force,
// because there is only one number.
func Threshold(alert, unit string) (float64, error) {
	a, err := ByName(alert)
	if err != nil {
		return 0, err
	}
	i := strings.Index(a.Condition, unit)
	if i < 0 {
		return 0, fmt.Errorf("%s: the ruled condition does not carry %q: %s", alert, unit, a.Condition)
	}
	start := i
	for start > 0 && (a.Condition[start-1] == '.' || (a.Condition[start-1] >= '0' && a.Condition[start-1] <= '9')) {
		start--
	}
	if start == i {
		return 0, fmt.Errorf("%s: no number in front of %q in %q", alert, unit, a.Condition)
	}
	return strconv.ParseFloat(a.Condition[start:i], 64)
}

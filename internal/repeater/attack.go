package repeater

// AttackMode selects how payload lists are paired with insertion points, mirror-
// ing Burp Intruder's modes.
type AttackMode int

const (
	// Single is one request with fixed values (plain Repeater, not an attack).
	Single AttackMode = iota
	// Sniper walks one position at a time over its payload list, leaving the
	// others at their baseline. Requests = Σ len(list_i).
	Sniper
	// BatteringRam puts the same payload in every position at once, iterating one
	// shared list (Lists[0]). Requests = len(Lists[0]).
	BatteringRam
	// Pitchfork advances every position's list in lockstep. Requests = min length.
	Pitchfork
	// ClusterBomb takes the cartesian product of every position's list.
	// Requests = Π len(list_i).
	ClusterBomb
)

func (m AttackMode) String() string {
	switch m {
	case Sniper:
		return "sniper"
	case BatteringRam:
		return "battering-ram"
	case Pitchfork:
		return "pitchfork"
	case ClusterBomb:
		return "cluster-bomb"
	default:
		return "single"
	}
}

// Modes is the cycle order for a mode selector.
var Modes = []AttackMode{Single, Sniper, BatteringRam, Pitchfork, ClusterBomb}

// Attack describes an intruder run: which variables are insertion points, the
// payload list for each (aligned with Positions), and a baseline value map for
// positions not being fuzzed on a given request.
type Attack struct {
	Mode      AttackMode
	Positions []string
	Lists     [][]string
	Base      map[string]string
}

// Jobs computes the ordered sequence of variable maps to send — one per request.
// Callers feed each map to Send; Jobs itself does no I/O, so the combinatorics
// are fully testable.
func (a Attack) Jobs() []map[string]string {
	switch a.Mode {
	case Sniper:
		return a.sniper()
	case BatteringRam:
		return a.batteringRam()
	case Pitchfork:
		return a.pitchfork()
	case ClusterBomb:
		return a.clusterBomb()
	default:
		return []map[string]string{a.base()}
	}
}

func (a Attack) sniper() []map[string]string {
	var jobs []map[string]string
	for i, pos := range a.Positions {
		if i >= len(a.Lists) {
			break
		}
		for _, payload := range a.Lists[i] {
			j := a.base()
			j[pos] = payload
			jobs = append(jobs, j)
		}
	}
	return jobs
}

func (a Attack) batteringRam() []map[string]string {
	if len(a.Lists) == 0 {
		return nil
	}
	var jobs []map[string]string
	for _, payload := range a.Lists[0] {
		j := a.base()
		for _, pos := range a.Positions {
			j[pos] = payload
		}
		jobs = append(jobs, j)
	}
	return jobs
}

func (a Attack) pitchfork() []map[string]string {
	n := -1
	for i := range a.Positions {
		if i >= len(a.Lists) {
			n = 0
			break
		}
		if l := len(a.Lists[i]); n < 0 || l < n {
			n = l
		}
	}
	var jobs []map[string]string
	for k := 0; k < n; k++ {
		j := a.base()
		for i, pos := range a.Positions {
			j[pos] = a.Lists[i][k]
		}
		jobs = append(jobs, j)
	}
	return jobs
}

func (a Attack) clusterBomb() []map[string]string {
	combos := product(a.Lists[:min(len(a.Lists), len(a.Positions))])
	var jobs []map[string]string
	for _, combo := range combos {
		j := a.base()
		for i, val := range combo {
			j[a.Positions[i]] = val
		}
		jobs = append(jobs, j)
	}
	return jobs
}

func (a Attack) base() map[string]string {
	out := make(map[string]string, len(a.Base))
	for k, v := range a.Base {
		out[k] = v
	}
	return out
}

// product returns the cartesian product of the given lists. An empty input (or a
// list with zero items) yields no combinations.
func product(lists [][]string) [][]string {
	result := [][]string{{}}
	for _, list := range lists {
		if len(list) == 0 {
			return nil
		}
		var next [][]string
		for _, combo := range result {
			for _, item := range list {
				c := append(append([]string{}, combo...), item)
				next = append(next, c)
			}
		}
		result = next
	}
	return result
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

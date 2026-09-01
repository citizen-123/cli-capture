package repeater

import "iter"

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

// Jobs returns the ordered sequence of variable maps to send, one per request.
// The map is reused between yields to keep large attacks bounded; callers that
// retain a job after the next yield must clone it.
func (a Attack) Jobs() iter.Seq[map[string]string] {
	return func(yield func(map[string]string) bool) {
		job := a.base()

		switch a.Mode {
		case Sniper:
			for i, pos := range a.Positions {
				if i >= len(a.Lists) {
					return
				}
				baseline, hasBaseline := a.Base[pos]
				for _, payload := range a.Lists[i] {
					job[pos] = payload
					if !yield(job) {
						return
					}
				}
				if hasBaseline {
					job[pos] = baseline
				} else {
					delete(job, pos)
				}
			}

		case BatteringRam:
			if len(a.Lists) == 0 {
				return
			}
			for _, payload := range a.Lists[0] {
				for _, pos := range a.Positions {
					job[pos] = payload
				}
				if !yield(job) {
					return
				}
			}

		case Pitchfork:
			n := -1
			for i := range a.Positions {
				if i >= len(a.Lists) {
					return
				}
				if l := len(a.Lists[i]); n < 0 || l < n {
					n = l
				}
			}
			for k := 0; k < n; k++ {
				for i, pos := range a.Positions {
					job[pos] = a.Lists[i][k]
				}
				if !yield(job) {
					return
				}
			}

		case ClusterBomb:
			a.clusterBomb(job, yield)

		default:
			yield(job)
		}
	}
}

func (a Attack) clusterBomb(job map[string]string, yield func(map[string]string) bool) {
	n := min(len(a.Lists), len(a.Positions))
	if n == 0 {
		yield(job)
		return
	}
	for _, list := range a.Lists[:n] {
		if len(list) == 0 {
			return
		}
	}

	indices := make([]int, n)
	for {
		for i := range n {
			job[a.Positions[i]] = a.Lists[i][indices[i]]
		}
		if !yield(job) {
			return
		}

		for i := n - 1; i >= 0; i-- {
			indices[i]++
			if indices[i] < len(a.Lists[i]) {
				break
			}
			indices[i] = 0
			if i == 0 {
				return
			}
		}
	}
}

func (a Attack) base() map[string]string {
	out := make(map[string]string, len(a.Base))
	for k, v := range a.Base {
		out[k] = v
	}
	return out
}

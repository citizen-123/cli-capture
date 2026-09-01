package repeater

import (
	"fmt"
	"maps"
	"reflect"
	"testing"
)

func TestSniperJobs(t *testing.T) {
	a := Attack{
		Mode:      Sniper,
		Positions: []string{"a", "b"},
		Lists:     [][]string{{"1", "2"}, {"9"}},
		Base:      map[string]string{"a": "A", "b": "B"},
	}
	got := collectJobs(a)
	want := []map[string]string{
		{"a": "1", "b": "B"},
		{"a": "2", "b": "B"},
		{"a": "A", "b": "9"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sniper jobs = %v, want %v", got, want)
	}
}

func TestBatteringRamJobs(t *testing.T) {
	a := Attack{
		Mode:      BatteringRam,
		Positions: []string{"a", "b"},
		Lists:     [][]string{{"x", "y"}},
	}
	got := collectJobs(a)
	want := []map[string]string{
		{"a": "x", "b": "x"},
		{"a": "y", "b": "y"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("battering-ram jobs = %v, want %v", got, want)
	}
}

func TestPitchforkJobs(t *testing.T) {
	a := Attack{
		Mode:      Pitchfork,
		Positions: []string{"a", "b"},
		Lists:     [][]string{{"1", "2", "3"}, {"9", "8"}}, // min length 2
	}
	got := collectJobs(a)
	want := []map[string]string{
		{"a": "1", "b": "9"},
		{"a": "2", "b": "8"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("pitchfork jobs = %v, want %v", got, want)
	}
}

func TestClusterBombJobs(t *testing.T) {
	a := Attack{
		Mode:      ClusterBomb,
		Positions: []string{"a", "b"},
		Lists:     [][]string{{"1", "2"}, {"9", "8"}},
	}
	got := collectJobs(a)
	want := []map[string]string{
		{"a": "1", "b": "9"},
		{"a": "1", "b": "8"},
		{"a": "2", "b": "9"},
		{"a": "2", "b": "8"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("cluster-bomb jobs = %v (%d), want %v", got, len(got), want)
	}
}

func TestSingleJob(t *testing.T) {
	a := Attack{Mode: Single, Base: map[string]string{"a": "A"}}
	got := collectJobs(a)
	if len(got) != 1 || got[0]["a"] != "A" {
		t.Errorf("single jobs = %v", got)
	}
}

func TestClusterBombYieldsBeforeEnumeratingHugeProduct(t *testing.T) {
	const dimensions = 64
	positions := make([]string, dimensions)
	lists := make([][]string, dimensions)
	for i := range dimensions {
		positions[i] = fmt.Sprintf("p%d", i)
		lists[i] = []string{"0", "1"}
	}

	seen := 0
	for job := range (Attack{Mode: ClusterBomb, Positions: positions, Lists: lists}).Jobs() {
		seen++
		for _, pos := range positions {
			if job[pos] != "0" {
				t.Fatalf("first cluster-bomb job[%q] = %q, want 0", pos, job[pos])
			}
		}
		break
	}
	if seen != 1 {
		t.Fatalf("jobs yielded before break = %d, want 1", seen)
	}
}

func TestEmptyAttackInputsPreserveJobCounts(t *testing.T) {
	tests := []struct {
		name string
		a    Attack
		want int
	}{
		{name: "single", a: Attack{Mode: Single}, want: 1},
		{name: "sniper", a: Attack{Mode: Sniper}, want: 0},
		{name: "battering ram", a: Attack{Mode: BatteringRam}, want: 0},
		{name: "pitchfork", a: Attack{Mode: Pitchfork}, want: 0},
		{name: "cluster bomb no positions", a: Attack{Mode: ClusterBomb}, want: 1},
		{
			name: "cluster bomb empty list",
			a:    Attack{Mode: ClusterBomb, Positions: []string{"a"}, Lists: [][]string{{}}},
			want: 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := len(collectJobs(tc.a)); got != tc.want {
				t.Errorf("job count = %d, want %d", got, tc.want)
			}
		})
	}
}

func collectJobs(a Attack) []map[string]string {
	var jobs []map[string]string
	for job := range a.Jobs() {
		jobs = append(jobs, maps.Clone(job))
	}
	return jobs
}

func TestModeString(t *testing.T) {
	for m, want := range map[AttackMode]string{
		Single: "single", Sniper: "sniper", BatteringRam: "battering-ram",
		Pitchfork: "pitchfork", ClusterBomb: "cluster-bomb",
	} {
		if m.String() != want {
			t.Errorf("%d.String() = %q, want %q", m, m.String(), want)
		}
	}
}

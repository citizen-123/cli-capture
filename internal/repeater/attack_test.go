package repeater

import (
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
	got := a.Jobs()
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
	got := a.Jobs()
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
	got := a.Jobs()
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
	got := a.Jobs()
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
	got := a.Jobs()
	if len(got) != 1 || got[0]["a"] != "A" {
		t.Errorf("single jobs = %v", got)
	}
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

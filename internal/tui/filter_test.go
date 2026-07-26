package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/citizen-123/cli-capture/internal/capture"
)

func TestRenderTrafficScrollsToSelection(t *testing.T) {
	var flows []*capture.Flow
	for i := 0; i < 30; i++ {
		f := capture.NewFlow("c", fmt.Sprintf("host%02d:443", i))
		f.Request = &capture.Message{Summary: fmt.Sprintf("GET /path%02d HTTP/1.1", i), Meta: map[string]string{}}
		flows = append(flows, f)
	}
	m := Model{flows: flows, fi: newFilter(), selected: 25}

	out := m.renderTraffic(80, 10) // only ~9 rows visible
	if !strings.Contains(out, "/path25") {
		t.Errorf("selected row (25) must be visible after scrolling:\n%s", out)
	}
	if strings.Contains(out, "/path00") {
		t.Error("early rows should scroll off when the selection is near the end")
	}
}

func TestFlaggedOnlyView(t *testing.T) {
	a := capture.NewFlow("c", "a:443")
	a.Request = &capture.Message{Meta: map[string]string{}}
	b := capture.NewFlow("c", "b:443")
	b.Request = &capture.Message{Meta: map[string]string{}}
	b.Flagged = true

	m := Model{flows: []*capture.Flow{a, b}, fi: newFilter()}
	if len(m.visible()) != 2 {
		t.Fatalf("without flagged-only, both flows visible; got %d", len(m.visible()))
	}
	m.flaggedOnly = true
	if v := m.visible(); len(v) != 1 || v[0] != b {
		t.Errorf("flagged-only should show just the flagged flow, got %v", v)
	}
}

func TestVisibleFilter(t *testing.T) {
	gh := capture.NewFlow("c", "api.github.com:443")
	gh.Protocol = capture.ProtoHTTP1
	gh.SNI = "api.github.com"
	gh.Request = &capture.Message{Meta: map[string]string{"method": "GET", "path": "/user"}}

	ex := capture.NewFlow("c", "example.com:80")
	ex.Protocol = capture.ProtoHTTP1
	ex.Request = &capture.Message{Meta: map[string]string{"method": "POST", "path": "/login"}}

	m := Model{flows: []*capture.Flow{gh, ex}, fi: newFilter()}

	if len(m.visible()) != 2 {
		t.Fatalf("empty filter should show all, got %d", len(m.visible()))
	}

	m.fi.SetValue("github")
	if v := m.visible(); len(v) != 1 || v[0] != gh {
		t.Errorf("host filter failed: %v", v)
	}

	m.fi.SetValue("post") // case-insensitive, matches method
	if v := m.visible(); len(v) != 1 || v[0] != ex {
		t.Errorf("method filter failed: %v", v)
	}

	m.fi.SetValue("/login") // matches path
	if v := m.visible(); len(v) != 1 || v[0] != ex {
		t.Errorf("path filter failed: %v", v)
	}

	m.fi.SetValue("nothingmatches")
	if v := m.visible(); len(v) != 0 {
		t.Errorf("non-matching filter should be empty, got %d", len(v))
	}
}

func TestSelectedFlowRespectsFilter(t *testing.T) {
	gh := capture.NewFlow("c", "api.github.com:443")
	gh.Request = &capture.Message{Meta: map[string]string{}}
	ex := capture.NewFlow("c", "example.com:80")
	ex.Request = &capture.Message{Meta: map[string]string{}}

	m := Model{flows: []*capture.Flow{gh, ex}, fi: newFilter(), selected: 0}
	m.fi.SetValue("example")
	if got := m.selectedFlow(); got != ex {
		t.Errorf("selectedFlow should index into the filtered list, got %v", got)
	}
}

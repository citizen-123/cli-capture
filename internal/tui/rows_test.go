package tui

import (
	"net/http"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/x/ansi"
	"github.com/citizen-123/cli-capture/internal/capture"
)

func TestFlagMarkStaysOneCell(t *testing.T) {
	// The flag glyph is themeable, including to an empty string ("remove it").
	// The marker column must stay one cell wide either way, or flagged rows
	// misalign against unflagged ones.
	saved := glyphFlag
	t.Cleanup(func() { glyphFlag = saved })

	if w := ansi.StringWidth(flagMark(false)); w != 1 {
		t.Errorf("unflagged mark width = %d, want 1", w)
	}
	for _, g := range []string{"⚑", "", "*"} {
		glyphFlag = g
		if w := ansi.StringWidth(flagMark(true)); w != 1 {
			t.Errorf("flag glyph %q: mark width = %d, want 1", g, w)
		}
	}
	// An emptied glyph makes flagged and unflagged rows share the same marker.
	glyphFlag = ""
	if flagMark(true) != flagMark(false) {
		t.Error("empty flag glyph should collapse to the same one-cell blank")
	}
}

func flowWithResp(status string, bodyLen int) *capture.Flow {
	f := capture.NewFlow("c", "api.example.com:443")
	f.Protocol = capture.ProtoHTTP1
	f.Request = &capture.Message{Summary: "GET /x HTTP/1.1", Meta: map[string]string{"method": "GET", "path": "/x"}}
	f.Response = &capture.Message{
		Headers: http.Header{},
		Body:    make([]byte, bodyLen),
		Meta:    map[string]string{"status": status},
	}
	return f
}

func TestRenderFlowRowShowsStatusAndSize(t *testing.T) {
	f := flowWithResp("200 OK", 2048)
	row := ansi.Strip((Model{}).renderFlowRow(f, false, 80))
	if !strings.Contains(row, "200") {
		t.Errorf("row should show HTTP status code: %q", row)
	}
	if !strings.Contains(row, "2.0 KB") {
		t.Errorf("row should show response size: %q", row)
	}
	if !strings.Contains(row, "/x") {
		t.Errorf("row should show the request title: %q", row)
	}
}

func TestRenderFlowRowLifecycleWhenNoResponse(t *testing.T) {
	f := capture.NewFlow("c", "h:443")
	f.Status = capture.StatusActive
	f.Request = &capture.Message{Summary: "GET / HTTP/1.1", Meta: map[string]string{}}
	row := ansi.Strip((Model{}).renderFlowRow(f, false, 80))
	if !strings.Contains(row, "···") {
		t.Errorf("in-flight flow should show a lifecycle marker, not a code: %q", row)
	}
}

func TestVisibleSortBySize(t *testing.T) {
	small := flowWithResp("200 OK", 100)
	big := flowWithResp("200 OK", 100000)
	mid := flowWithResp("200 OK", 5000)
	m := Model{flows: []*capture.Flow{small, big, mid}, fi: newFilter(), sort: sortSize}
	vis := m.visible()
	if vis[0] != big || vis[2] != small {
		t.Errorf("size sort should be largest-first: got %d, %d, %d",
			respSize(vis[0]), respSize(vis[1]), respSize(vis[2]))
	}
}

func TestVisibleSortByStatus(t *testing.T) {
	ok := flowWithResp("200 OK", 10)
	err500 := flowWithResp("500 Internal Server Error", 10)
	notfound := flowWithResp("404 Not Found", 10)
	m := Model{flows: []*capture.Flow{err500, ok, notfound}, fi: newFilter(), sort: sortStatus}
	vis := m.visible()
	if rowCode(vis[0]) != 200 || rowCode(vis[2]) != 500 {
		t.Errorf("status sort should be ascending: got %d, %d, %d",
			rowCode(vis[0]), rowCode(vis[1]), rowCode(vis[2]))
	}
}

func TestJobLabel(t *testing.T) {
	got := jobLabel(map[string]string{"user": "admin", "pass": "x"}, []string{"user", "pass"})
	if got != "user=admin, pass=x" {
		t.Errorf("jobLabel = %q", got)
	}
}

func TestCapturedTitleIsEscapedAtEveryTrafficSink(t *testing.T) {
	unsafe := "\x1b]8;;https://attacker.example\x07"
	f := capture.NewFlow("c", "server:443")
	f.Request = &capture.Message{Summary: "GET " + unsafe, Meta: map[string]string{}}
	m := Model{
		flows:    []*capture.Flow{f},
		paused:   f,
		viewFlow: f,
		fi:       newFilter(),
		vp:       viewport.New(80, 4),
	}

	sinks := map[string]string{
		"row":          m.renderFlowRow(f, false, 80),
		"pending":      m.renderTraffic(80, 12),
		"detail title": m.renderDetail(),
		"editor title": m.renderEditorTitle(),
		"status":       statusBar(80, f.Title(), false, false),
	}
	for name, out := range sinks {
		if strings.Contains(out, unsafe) {
			t.Errorf("%s emitted unsafe captured title %q", name, out)
		}
		if !strings.Contains(out, `\x1B]8;;https://attacker.example\x07`) {
			t.Errorf("%s did not render the escaped captured title: %q", name, out)
		}
	}
}

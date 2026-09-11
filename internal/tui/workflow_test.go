package tui

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/citizen-123/cli-capture/internal/capture"
	"github.com/citizen-123/cli-capture/internal/intercept"
	"github.com/citizen-123/cli-capture/internal/runner"
)

func workflowModel(t *testing.T) Model {
	t.Helper()
	store := capture.NewStore()
	f := capture.NewFlow("client", "example.test:80")
	f.Protocol = capture.ProtoHTTP1
	f.Status = capture.StatusComplete
	f.Flagged = true
	f.Request = &capture.Message{Direction: capture.ClientToServer, Summary: "GET /example HTTP/1.1", Raw: []byte("GET /example HTTP/1.1\r\nHost: example.test\r\n\r\n"), Meta: map[string]string{"method": "GET", "path": "/example", "proto": "HTTP/1.1"}, Headers: map[string][]string{"Host": {"example.test"}}}
	f.Response = &capture.Message{Direction: capture.ServerToClient, Summary: "HTTP/1.1 500 Internal Server Error", Body: []byte("response"), Meta: map[string]string{"status": "500"}}
	store.Add(f)
	m := New(store, intercept.NewEngine(), nil, nil, Feeds{}, filepath.Join(t.TempDir(), "session.json"))
	m.width, m.height = 120, 36
	m.focus = focusTraffic
	m.sizeOverlays()
	return m
}

func leaderKey(t *testing.T, m Model, command string) Model {
	t.Helper()
	m = pauseKey(t, m, tea.KeyMsg{Type: m.km().Leader})
	return pauseKey(t, m, key(command))
}

func TestDefaultNULLeaderAndExplicitOverrideReachTerminalExactly(t *testing.T) {
	for _, leader := range []string{"", "ctrl+a"} {
		t.Run(leader, func(t *testing.T) {
			readEnd, writeEnd, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer readEnd.Close()
			defer writeEnd.Close()
			km, err := NewKeyMap(leader, nil)
			if err != nil {
				t.Fatal(err)
			}
			m := workflowModel(t).WithKeys(km)
			m.focus = focusTerminal
			m.target = &runner.Target{Pty: writeEnd}
			ordinary := tea.KeyCtrlA
			if leader != "" {
				ordinary = tea.KeyCtrlAt
			}
			m = pauseKey(t, m, tea.KeyMsg{Type: ordinary})
			m = pauseKey(t, m, tea.KeyMsg{Type: km.Leader})
			if !m.pendingLeader {
				t.Fatal("prefix was forwarded to the target")
			}
			m = pauseKey(t, m, tea.KeyMsg{Type: km.Leader})
			m = leaderKey(t, m, "Z")
			m = pauseKey(t, m, tea.KeyMsg{Type: km.Leader})
			m = pauseKey(t, m, tea.KeyMsg{Type: tea.KeyEsc})
			writeEnd.Close()
			got, err := io.ReadAll(readEnd)
			if err != nil {
				t.Fatal(err)
			}
			want := []byte{byte(ordinary), byte(km.Leader)}
			if !bytes.Equal(got, want) || m.pendingLeader {
				t.Fatalf("terminal bytes %q want %q, pending=%v", got, want, m.pendingLeader)
			}
		})
	}
}

func TestGlobalLeaderPreservesEachModalDraft(t *testing.T) {
	for _, mode := range []string{"editor", "inject", "filter", "command", "detail", "repeater", "help"} {
		t.Run(mode, func(t *testing.T) {
			m := workflowModel(t)
			switch mode {
			case "editor", "inject":
				m.editing = true
				m.injecting = mode == "inject"
				m.ta.SetValue("draft abc")
				m.ta.Focus()
			case "filter":
				m.filtering = true
				m.fi.SetValue("draft")
				m.fi.Focus()
			case "command":
				m.cmdline = true
				m.ci.SetValue("draft")
				m.ci.Focus()
			case "detail":
				m.openDetail()
			case "repeater":
				m.openRepeater()
			case "help":
				m.toggleHelp()
			}
			owner := m.keyboardOwner()
			before := []string{m.ta.Value(), m.fi.Value(), m.ci.Value(), m.rep.req.Value(), m.rep.payload.Value()}
			for _, command := range []string{"s", "h", "f", "i", "r", "<", ">", "m", "c", "Z"} {
				m = leaderKey(t, m, command)
			}
			if !m.engine.Enabled() || !m.engine.InterceptResponses() {
				t.Fatal("modal swallowed interception commands")
			}
			for _, file := range []string{"session.json", "capture.har", "flagged.txt"} {
				if _, err := os.Stat(filepath.Join(filepath.Dir(m.sessionPath), file)); err != nil {
					t.Fatal(err)
				}
			}
			if mode != "help" {
				m = leaderKey(t, m, "?")
				if m.keyboardOwner() != "help" {
					t.Fatal("help did not cover modal")
				}
				m = pauseKey(t, m, tea.KeyMsg{Type: tea.KeyEsc})
			}
			m = leaderKey(t, m, "w")
			if m.keyboardOwner() != "terminal" {
				t.Fatal("pane switch did not suspend modal")
			}
			m = pauseKey(t, m, key("no modal insertion"))
			m = leaderKey(t, m, "w")
			if m.keyboardOwner() != owner {
				t.Fatalf("returned to %s, want %s", m.keyboardOwner(), owner)
			}
			after := []string{m.ta.Value(), m.fi.Value(), m.ci.Value(), m.rep.req.Value(), m.rep.payload.Value()}
			for i := range before {
				if before[i] != after[i] {
					t.Fatalf("draft %d changed: %q -> %q", i, before[i], after[i])
				}
			}
		})
	}
}

func TestHelpReturnsToSuspendedModalWithoutTakingTerminalFocus(t *testing.T) {
	m := workflowModel(t)
	m.filtering = true
	m.fi.SetValue("saved")
	m.fi.Focus()
	m = leaderKey(t, m, "w")
	m = leaderKey(t, m, "?")
	m = pauseKey(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.keyboardOwner() != "terminal" || !m.modalSuspended {
		t.Fatal("help resumed the covered filter")
	}
	m = leaderKey(t, m, "w")
	m = pauseKey(t, m, key("X"))
	if !strings.Contains(m.fi.Value(), "X") {
		t.Fatal("restored filter does not own text input")
	}
}

func TestPauseInspectionRestoresFilterAndPausedEditorDraft(t *testing.T) {
	h := newPauseModelHarness(t)
	h.model.filtering = true
	h.model.fi.SetValue("original filter")
	h.model.fi.Focus()
	a, result := h.startPause(t, "held", "original body")
	h.notify(a)
	if h.model.keyboardOwner() != "filter" || h.model.fi.Value() != "original filter" {
		t.Fatal("pause stole filter input")
	}
	h.model = leaderKey(t, h.model, "p")
	h.model = pauseKey(t, h.model, key("e"))
	h.model.ta.SetValue("edited body")
	h.model = leaderKey(t, h.model, "p")
	if h.model.keyboardOwner() != "filter" || h.model.fi.Value() != "original filter" {
		t.Fatal("inspection lost the original filter")
	}
	h.model = leaderKey(t, h.model, "p")
	h.model = pauseKey(t, h.model, key("e"))
	if h.model.ta.Value() != "edited body" {
		t.Fatal("inspection return discarded the held draft")
	}
	h.model = pauseKey(t, h.model, tea.KeyMsg{Type: tea.KeyCtrlS})
	if got := awaitPauseOutcome(t, "held", result); string(got.body) != "edited body" || got.drop {
		t.Fatalf("outcome %#v", got)
	}
	if h.model.keyboardOwner() != "filter" || h.model.fi.Value() != "original filter" {
		t.Fatal("resolution did not restore the filter")
	}
}

func TestInterceptedEditorSendsUnchangedGETToOrigin(t *testing.T) {
	received := make(chan string, 1)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		received <- fmt.Sprintf("%s|%s|%s", r.Method, r.Header.Get("X-Probe"), body)
		w.WriteHeader(204)
	}))
	defer origin.Close()
	h := newPauseModelHarness(t)
	raw := "GET /probe HTTP/1.1\r\nHost: " + origin.Listener.Addr().String() + "\r\nX-Probe: retained\r\n\r\n"
	item, result := h.startPause(t, "editor", raw)
	item.Flow.Protocol = capture.ProtoHTTP1
	h.notify(item)
	h.model = pauseKey(t, h.model, key("e"))
	h.model = pauseKey(t, h.model, tea.KeyMsg{Type: tea.KeyCtrlS})
	out := awaitPauseOutcome(t, "editor", result)
	conn, err := net.Dial("tcp", origin.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err = conn.Write(out.body); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := <-received; got != "GET|retained|" {
		t.Fatalf("origin received %q", got)
	}
}

func TestPreloadedSessionNavigatesAndExportsBeforeEvents(t *testing.T) {
	original := workflowModel(t)
	if err := capture.SaveFile(original.sessionPath, original.store.List()); err != nil {
		t.Fatal(err)
	}
	loaded, err := capture.LoadFile(original.sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	store := capture.NewStore()
	for _, f := range loaded {
		store.Add(f)
	}
	m := New(store, intercept.NewEngine(), nil, nil, Feeds{}, original.sessionPath)
	m.width, m.height = 80, 24
	m.focus = focusTraffic
	if len(m.visible()) != 1 || !m.selectedFlow().Flagged {
		t.Fatal("loaded flow is invisible without a new event")
	}
	m = pauseKey(t, m, key("enter"))
	if !strings.Contains(ansi.Strip(m.View()), "500") {
		t.Fatal("loaded response metadata missing")
	}
	m = pauseKey(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	m.fi.SetValue("absent")
	if len(m.visible()) != 0 {
		t.Fatal("filter ignored")
	}
	m = leaderKey(t, m, "s")
	m = leaderKey(t, m, "h")
	m = leaderKey(t, m, "f")
	all, err := capture.LoadFile(m.sessionPath)
	if err != nil || len(all) != 1 {
		t.Fatalf("session export followed view filter: %v %d", err, len(all))
	}
	for _, name := range []string{"capture.har", "flagged.txt"} {
		data, err := os.ReadFile(filepath.Join(filepath.Dir(m.sessionPath), name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(data, []byte("example")) {
			t.Fatalf("%s omitted filtered flow", name)
		}
	}
	f := capture.NewFlow("client", "new.example:80")
	store.Add(f)
	next, _ := m.Update(eventMsg{})
	m = next.(Model)
	if len(m.flows) != 2 {
		t.Fatalf("preload + event has %d flows", len(m.flows))
	}
	m.sessionPath = filepath.Join(t.TempDir(), "missing", "session.json")
	if status := m.saveSession(); !strings.Contains(status, "failed") {
		t.Fatalf("unwritable export silently succeeded: %s", status)
	}
}

func TestCompleteModalViewsFitAndReflow(t *testing.T) {
	m := workflowModel(t)
	var body strings.Builder
	// Stay below the explicit 8 KiB preview limit; truncation has separate tests.
	for i := range 100 {
		fmt.Fprintf(&body, "line%03d long response with words that wrap when the terminal narrows\n", i)
	}
	body.WriteString("ENDTOKEN")
	m.flows[0].Response.Body = []byte(body.String())
	m.openDetail()
	m.vp.GotoBottom()
	for _, size := range [][2]int{{160, 48}, {120, 36}, {80, 24}, {40, 10}, {80, 24}} {
		next, _ := m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m = next.(Model)
		assertViewBounds(t, m)
		if size[0] >= 60 && !strings.Contains(ansi.Strip(m.vp.View()), "ENDTOKEN") {
			t.Fatalf("resize %v lost bottom at offset %d/%d", size, m.vp.YOffset, m.vp.TotalLineCount())
		}
	}
	m = pauseKey(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	m.openRepeater()
	for _, size := range [][2]int{{160, 48}, {120, 36}, {80, 24}} {
		next, _ := m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m = next.(Model)
		m.rep.result = strings.Repeat("long result ", 80)
		assertViewBounds(t, m)
	}
}

func assertViewBounds(t *testing.T, m Model) {
	t.Helper()
	view := m.View()
	lines := strings.Split(view, "\n")
	if len(lines) > m.height {
		t.Fatalf("view %dx%d is %d rows", m.width, m.height, len(lines))
	}
	for i, line := range lines {
		if w := ansi.StringWidth(line); w > m.width {
			t.Fatalf("view %dx%d row %d is %d cells: %q", m.width, m.height, i, w, line)
		}
	}
}

func TestClosedInjectionRetainsEditableDraft(t *testing.T) {
	m := workflowModel(t)
	m.editing, m.injecting = true, true
	m.injectID = "session-closed-during-composition"
	m.ta.SetValue("retain frame")
	m.ta.Focus()
	m = pauseKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlS})
	if !m.editing || !m.injecting || m.ta.Value() != "retain frame" {
		t.Fatal("closed-session error discarded the injection draft")
	}
	if !strings.Contains(m.status, "closed") {
		t.Fatalf("closed session not reported: %s", m.status)
	}
	m = pauseKey(t, m, key("X"))
	if !strings.Contains(m.ta.Value(), "X") {
		t.Fatal("failed injection draft no longer accepts edits")
	}
}

func TestCustomAndUnboundModalControlsMatchDispatch(t *testing.T) {
	km, err := NewKeyMap("ctrl+g", map[string]map[string]string{
		ctxLeader: {"?": string(Unbind), "H": string(ActHelpToggle)},
		ctxEditor: {"ctrl+s": string(Unbind), "f2": string(ActEditorSend), "ctrl+l": string(Unbind)},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := workflowModel(t).WithKeys(km)
	m.editing, m.injecting = true, true
	m.ta.SetValue("not sent")
	m.ta.Focus()
	view := strings.ToLower(ansi.Strip(m.View()))
	for _, want := range []string{"ctrl+g h", "f2"} {
		if !strings.Contains(view, want) {
			t.Fatalf("effective control %s missing", want)
		}
	}
	if strings.Contains(view, "ctrl+a") || strings.Contains(view, "ctrl+space") || strings.Contains(view, "ctrl+s") {
		t.Fatal("modal advertises overridden default controls")
	}
	m = pauseKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlS})
	if strings.Contains(m.status, "closed") {
		t.Fatal("unbound send action still dispatched")
	}
	m = pauseKey(t, m, tea.KeyMsg{Type: tea.KeyF2})
	if !strings.Contains(m.status, "closed") {
		t.Fatal("rebound send action did not dispatch")
	}
}

func TestMouseSelectionMatchesRenderedRowsWithChrome(t *testing.T) {
	for _, held := range []bool{false, true} {
		m := workflowModel(t)
		m.width, m.height = 120, 24
		m.flows = nil
		for i := range 30 {
			f := capture.NewFlow("client", fmt.Sprintf("row%02d.test:80", i))
			f.Protocol = capture.ProtoHTTP1
			f.Request = &capture.Message{Summary: fmt.Sprintf("ROW%02d", i)}
			m.flows = append(m.flows, f)
		}
		m.selected = 25
		if held {
			m.paused = m.flows[0]
		}
		_, box := m.paneRects()
		w, h := contentGrid(box)
		lines := strings.Split(ansi.Strip(m.renderTraffic(w, h)), "\n")
		hits := 0
		for row, line := range lines {
			for _, f := range m.flows {
				if !strings.Contains(line, f.Request.Summary) || strings.Contains(line, "host") {
					continue
				}
				if strings.Contains(line, "Held") {
					continue
				}
				next, _ := m.Update(tea.MouseMsg{X: box.X + 2, Y: box.Y + 1 + row, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
				got := next.(Model).selectedFlow()
				if got == nil || got.ID != f.ID {
					t.Fatalf("row %d displayed %s but click selected %v", row, f.ID, got)
				}
				hits++
			}
		}
		if hits < 2 {
			t.Fatal("scroll fixture did not expose multiple selectable rows")
		}
	}
}

func TestHTTP2AndGRPCEditorsKeepBodyOnlyFraming(t *testing.T) {
	for _, protocol := range []capture.Protocol{capture.ProtoHTTP2, capture.ProtoGRPC} {
		t.Run(string(protocol), func(t *testing.T) {
			h := newPauseModelHarness(t)
			item, result := h.startPause(t, "body-only", "first\nsecond")
			item.Flow.Protocol = protocol
			h.notify(item)
			h.model = pauseKey(t, h.model, key("e"))
			h.model.ta.SetValue("first\nchanged")
			h.model = pauseKey(t, h.model, tea.KeyMsg{Type: tea.KeyCtrlS})
			if got := awaitPauseOutcome(t, "body-only", result); got.drop || string(got.body) != "first\nchanged" {
				t.Fatalf("body-only edit changed framing: %#v", got)
			}
		})
	}
}

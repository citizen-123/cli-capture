package tui

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/citizen-123/cli-capture/internal/capture"
	"github.com/citizen-123/cli-capture/internal/repeater"
)

func openRepeaterTestModel(t *testing.T, target, path string) Model {
	t.Helper()
	flow := requestJobTestFlow(target)
	flow.Request.Meta["path"] = path
	model := requestJobTestModel(flow)
	model.openRepeater()
	if !model.repeating || model.rep.previewErr != nil {
		t.Fatalf("could not open Repeater: %s, %v", model.status, model.rep.previewErr)
	}
	return model
}

func TestRepeaterPreviewMatchesAllAttackModesAtOrigin(t *testing.T) {
	cases := []struct {
		mode repeater.AttackMode
		want []string
	}{
		{repeater.Single, []string{"1/x"}},
		{repeater.Sniper, []string{"1/x", "2/x", "1/x", "1/y", "1/z"}},
		{repeater.BatteringRam, []string{"1/1", "2/2"}},
		{repeater.Pitchfork, []string{"1/x", "2/y"}},
		{repeater.ClusterBomb, []string{"1/x", "1/y", "1/z", "2/x", "2/y", "2/z"}},
	}
	for _, tc := range cases {
		t.Run(tc.mode.String(), func(t *testing.T) {
			receipts := make(chan string, 6)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				receipts <- r.URL.Query().Get("a") + "/" + r.URL.Query().Get("b")
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()
			model := openRepeaterTestModel(t, server.URL, "/?a={{a}}&b={{b}}")
			defer func() { model.Close() }()
			model.rep.mode = tc.mode
			model.rep.payload.SetValue("a = 1, 2\nb = x, y, z\nunreferenced = ignored, extra\n")
			model.refreshRepeater()
			if model.rep.previewErr != nil || model.rep.planned != len(tc.want) {
				t.Fatalf("preview = %d, %v; want %d", model.rep.planned, model.rep.previewErr, len(tc.want))
			}
			progress := finishRequestJob(t, &model, model.runRepeater())
			if progress.planned != len(tc.want) || progress.attempted != len(tc.want) || progress.succeeded != len(tc.want) || progress.failed != 0 {
				t.Fatalf("progress = %+v", progress)
			}
			var got []string
			for range tc.want {
				select {
				case value := <-receipts:
					got = append(got, value)
				default:
					t.Fatal("planned exchange missing at origin")
				}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("origin combinations = %v, want %v", got, tc.want)
			}
			stored := model.store.List()
			if len(stored) != len(tc.want) {
				t.Fatalf("stored %d exchanges, want %d", len(stored), len(tc.want))
			}
			for i, values := range tc.want {
				a, b, _ := strings.Cut(values, "/")
				if label := stored[i].Request.Meta["payload"]; label != "a="+a+", b="+b {
					t.Errorf("exchange %d label = %q, want selected payloads %s", i, label, values)
				}
			}
		})
	}
}

func TestRepeaterZeroPositionPreviewsAndExecution(t *testing.T) {
	for _, mode := range repeater.Modes {
		t.Run(mode.String(), func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
			defer server.Close()
			model := openRepeaterTestModel(t, server.URL, "/?a={{a}}")
			model.rep.mode = mode
			model.rep.payload.SetValue("a = \nunused = 1, 2\n")
			model.refreshRepeater()
			want := 0
			if mode == repeater.Single || mode == repeater.ClusterBomb {
				want = 1
			}
			if model.rep.previewErr != nil || model.rep.planned != want {
				t.Fatalf("zero-position preview = %d, %v; want %d", model.rep.planned, model.rep.previewErr, want)
			}
			progress := finishRequestJob(t, &model, model.runRepeater())
			model.Close()
			if int(requests.Load()) != want || progress.attempted != want || progress.succeeded != want {
				t.Fatalf("zero-position execution = %d, progress = %+v", requests.Load(), progress)
			}
		})
	}
}

func TestRepeaterDisablesInvalidAndOverflowPreviews(t *testing.T) {
	model := openRepeaterTestModel(t, "http://example.test", "/")
	// An invalid method token must be rejected by request construction.
	model.rep.req.SetValue("G@T / HTTP/1.1\n\n")
	model.refreshRepeater()
	if model.rep.previewErr == nil || model.runRepeater() != nil || model.job != nil {
		t.Fatal("invalid request started a manual job")
	}
	var path, payload strings.Builder
	path.WriteString("/?")
	for i := range strconv.IntSize {
		fmt.Fprintf(&path, "p%d={{p%d}}&", i, i)
		fmt.Fprintf(&payload, "p%d = 0, 1\n", i)
	}
	model = openRepeaterTestModel(t, "http://example.test", path.String())
	model.rep.mode = repeater.ClusterBomb
	model.rep.payload.SetValue(payload.String())
	model.refreshRepeater()
	if model.rep.previewErr == nil || model.runRepeater() != nil || model.job != nil {
		t.Fatal("unrepresentable attack product started a manual job")
	}
}

func TestRepeaterSeedAndEditedTargetReachOriginWithoutPhantomBody(t *testing.T) {
	type receipt struct {
		path, header string
		body         []byte
	}
	receipts := make(chan receipt, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return
		}
		receipts <- receipt{r.URL.Path, r.Header.Get("X-Seed"), body}
		_, _ = io.WriteString(w, "current-response")
	}))
	defer server.Close()
	flow := requestJobTestFlow(server.URL)
	flow.Request.Meta["path"] = "/captured"
	flow.Request.Headers = http.Header{"X-Seed": {"kept"}}
	model := requestJobTestModel(flow)
	model.openRepeater()
	model.rep.req.SetValue(strings.Replace(model.rep.req.Value(), "/captured", "/edited", 1))
	model.refreshRepeater()
	if !strings.Contains(ansi.Strip(model.repeaterView()), "/edited") || strings.Contains(ansi.Strip(model.repeaterView()), "/captured") {
		t.Fatal("Repeater title did not reflect parsed edited target")
	}
	cmd := model.runRepeater()
	model.rep.req.SetValue(strings.Replace(model.rep.req.Value(), "/edited", "/later", 1))
	model.refreshRepeater()
	finishRequestJob(t, &model, cmd)
	got := <-receipts
	if got.path != "/edited" || got.header != "kept" || len(got.body) != 0 {
		t.Fatalf("origin saw path=%q header=%q body=%q", got.path, got.header, got.body)
	}
	if model.rep.resp == nil || model.rep.resp.Response == nil || string(model.rep.resp.Response.Body) != "current-response" {
		t.Fatal("current response was not displayed")
	}
	model.rep.mode = repeater.Sniper
	model.refreshRepeater()
	cmd = model.runRepeater()
	if model.rep.resp != nil || strings.Contains(ansi.Strip(model.rep.respVP.View()), "current-response") {
		t.Fatal("new attack retained the previous response")
	}
	progress := finishRequestJob(t, &model, cmd)
	if progress.planned != 0 || progress.attempted != 0 {
		t.Fatalf("empty sniper sent work: %+v", progress)
	}
	model.Close()
}

func TestRepeaterClosedSessionCannotReplaceReopenedResponse(t *testing.T) {
	started, released := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(released) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-released:
			_, _ = io.WriteString(w, "first-session-response")
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer release()
	model := openRepeaterTestModel(t, server.URL, "/first")
	cmd := model.runRepeater()
	defer model.job.cancel()
	first := nextRequestJobEvent(t, cmd)
	cmd = model.handleJobEvent(first)
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not start")
	}
	originalSession := model.rep.sessionID
	closed, _ := model.onRepeaterKey(tea.KeyMsg{Type: tea.KeyEsc})
	model = closed.(Model)
	if model.repeating || !model.job.active() || model.jobStatus() == "" {
		t.Fatal("closing Repeater hid or canceled the active job")
	}
	other := requestJobTestFlow(server.URL)
	other.Request.Meta["path"] = "/second"
	model.flows = []*capture.Flow{other}
	model.openRepeater()
	if model.rep.sessionID == originalSession || model.runRepeater() != nil {
		t.Fatal("reopened session reused identity or accepted a duplicate send")
	}
	release()
	terminal := finishRequestJob(t, &model, cmd)
	if model.rep.resp != nil || strings.Contains(ansi.Strip(model.repeaterView()), "first-session-response") {
		t.Fatal("old job response contaminated the reopened session")
	}
	if stored := model.store.List(); len(stored) != 1 || stored[0].Request.Meta["path"] != "/first" {
		t.Fatalf("originating exchange was not retained: %+v", stored)
	}
	// Even a duplicate delayed terminal cannot overwrite a later job's state.
	second := model.runRepeater()
	if second == nil {
		t.Fatal("new session could not send after previous completion")
	}
	if next := model.handleJobEvent(terminal); next != nil || !model.job.active() || model.rep.resp != nil {
		t.Fatal("stale job event affected a new active job")
	}
	model.Close()
}

func TestRepeaterViewBoundsLongResultsAtEightyColumns(t *testing.T) {
	model := openRepeaterTestModel(t, "http://example.test", "/target")
	model.width, model.height = 80, 24
	model.rep.result = strings.Repeat("long failure ", 100) + "\nextra"
	model.sizeRepeater()
	view := model.repeaterView()
	lines := strings.Split(view, "\n")
	if len(lines) > model.height {
		t.Fatalf("Repeater occupies %d rows at height %d", len(lines), model.height)
	}
	for i, line := range lines {
		if width := ansi.StringWidth(line); width > model.width {
			t.Errorf("row %d occupies %d cells at width %d", i, width, model.width)
		}
	}
	if !strings.Contains(ansi.Strip(view), model.keyHint(ctxRepeater, ActRepeaterSend)) {
		t.Fatal("bounded result displaced send controls")
	}
}

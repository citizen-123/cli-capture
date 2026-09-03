package tui

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/citizen-123/cli-capture/internal/capture"
	"github.com/citizen-123/cli-capture/internal/repeater"
)

func TestRunRepeaterDefersAttackWorkUntilCommandRuns(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()

	const dimensions = 12 // 4,096 jobs if the Cartesian product is materialized.
	var query, payloadText strings.Builder
	for i := range dimensions {
		if i > 0 {
			query.WriteByte('&')
		}
		fmt.Fprintf(&query, "p%d={{p%d}}", i, i)
		fmt.Fprintf(&payloadText, "p%d = 0, 1\n", i)
	}

	payload := newEditor()
	payload.SetValue(payloadText.String())
	model := Model{rep: repeaterState{mode: repeater.ClusterBomb, payload: payload}}
	tmpl := &repeater.Template{
		Method: http.MethodGet,
		URL:    server.URL + "/?" + query.String(),
	}

	allocs := testing.AllocsPerRun(1, func() {
		if cmd := model.runRepeater(tmpl); cmd == nil {
			panic("runRepeater returned a nil command")
		}
	})
	if allocs >= 1000 {
		t.Fatalf("runRepeater allocated %.0f objects before command invocation; attack generation must be deferred", allocs)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("requests sent before command invocation = %d, want 0", got)
	}
}

func TestRepeaterEscapesCapturedTerminalControls(t *testing.T) {
	unsafe := "\x1b]8;;https://attacker.example\x07"
	req, payload := newEditor(), newEditor()
	m := Model{
		width: 80,
		rep: repeaterState{
			base:    &repeater.Template{Method: "GET" + unsafe, URL: "https://api.example/" + unsafe},
			req:     req,
			payload: payload,
			respVP:  viewport.New(80, 3),
			mode:    repeater.Single,
			result:  "sent " + unsafe,
		},
	}
	f := capture.NewFlow("c", "api.example:443")
	f.Response = &capture.Message{Summary: "200 " + unsafe}

	for name, out := range map[string]string{
		"repeater title and result": m.repeaterView(),
		"repeater response":         renderRepeaterResponse(f, 80),
	} {
		if strings.Contains(out, unsafe) {
			t.Errorf("%s emitted unsafe capture text %q", name, out)
		}
		if !strings.Contains(out, `\x1B]8;;https://attacker.example\x07`) {
			t.Errorf("%s did not render escaped capture text: %q", name, out)
		}
	}
}

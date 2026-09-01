package tui

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

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

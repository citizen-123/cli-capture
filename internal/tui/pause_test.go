package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/citizen-123/cli-capture/internal/capture"
	"github.com/citizen-123/cli-capture/internal/intercept"
	"github.com/citizen-123/cli-capture/internal/scope"
)

type pauseOutcome struct {
	body []byte
	drop bool
}

type pauseModelHarness struct {
	model  Model
	paused chan Paused
}

func newPauseModelHarness(t *testing.T) *pauseModelHarness {
	t.Helper()
	engine := intercept.NewEngine()
	set, err := scope.Build(nil, nil, true)
	if err != nil {
		t.Fatalf("build intercept scope: %v", err)
	}
	engine.SetScope(set)
	engine.SetEnabled(true)
	engine.SetInterceptResponses(true)

	paused := make(chan Paused, 8)
	engine.OnPause(func(token intercept.PauseToken, flow *capture.Flow, msg *capture.Message) {
		paused <- Paused{Token: token, Flow: flow, Msg: msg}
	})
	model := New(capture.NewStore(), engine, nil, nil, Feeds{}, "")
	model.width, model.height = 100, 40
	model.focus = focusTraffic
	return &pauseModelHarness{model: model, paused: paused}
}

func (h *pauseModelHarness) startPause(t *testing.T, name, raw string) (Paused, <-chan pauseOutcome) {
	t.Helper()
	flow := capture.NewFlow("client", name+".example:443")
	flow.SNI = name + ".example"
	msg := &capture.Message{Raw: []byte(raw)}
	result := make(chan pauseOutcome, 1)
	go func() {
		body, drop := h.model.engine.BeforeForward(flow, msg)
		result <- pauseOutcome{body: body, drop: drop}
	}()

	select {
	case item := <-h.paused:
		return item, result
	case <-time.After(time.Second):
		t.Fatalf("%s did not pause", name)
		return Paused{}, nil
	}
}

func (h *pauseModelHarness) notify(item Paused) {
	next, _ := h.model.Update(pauseMsg{item: item})
	h.model = next.(Model)
}

func pauseKey(t *testing.T, m Model, k tea.KeyMsg) Model {
	t.Helper()
	next, _ := m.Update(k)
	return next.(Model)
}

func awaitPauseOutcome(t *testing.T, name string, result <-chan pauseOutcome) pauseOutcome {
	t.Helper()
	select {
	case got := <-result:
		return got
	case <-time.After(time.Second):
		t.Fatalf("%s was not resolved", name)
		return pauseOutcome{}
	}
}

func TestPauseNotificationQueuesBehindActiveEditor(t *testing.T) {
	h := newPauseModelHarness(t)
	a, aResult := h.startPause(t, "a", "original-a")
	h.notify(a)
	h.model = pauseKey(t, h.model, key("e"))
	h.model.ta.SetValue("edited-a")

	b, bResult := h.startPause(t, "b", "original-b")
	h.notify(b)

	if h.model.paused != a.Flow || h.model.pausedMsg != a.Msg || h.model.pausedToken != a.Token {
		t.Fatal("second notification replaced the active pause")
	}
	if len(h.model.pauseQueue) != 1 ||
		h.model.pauseQueue[0].Token != b.Token ||
		h.model.pauseQueue[0].Flow != b.Flow ||
		h.model.pauseQueue[0].Msg != b.Msg {
		t.Fatalf("queued pause = %#v, want pause B with its token and message", h.model.pauseQueue)
	}
	if !h.model.editing || h.model.ta.Value() != "edited-a" {
		t.Fatalf("A editor changed after B arrived: editing=%v body=%q", h.model.editing, h.model.ta.Value())
	}

	h.model = pauseKey(t, h.model, tea.KeyMsg{Type: tea.KeyCtrlS})
	if got := awaitPauseOutcome(t, "A", aResult); got.drop || string(got.body) != "edited-a" {
		t.Fatalf("A resolution = {drop:%v body:%q}, want edited forward", got.drop, got.body)
	}
	if h.model.paused != b.Flow || h.model.pausedMsg != b.Msg || h.model.pausedToken != b.Token {
		t.Fatal("B was not promoted with its matching token and message")
	}
	if h.model.editing || h.model.ta.Value() != "" {
		t.Fatalf("A editor leaked into B: editing=%v body=%q", h.model.editing, h.model.ta.Value())
	}
	if len(h.model.pauseQueue) != 0 {
		t.Fatalf("queue length after promoting B = %d, want 0", len(h.model.pauseQueue))
	}

	h.model = pauseKey(t, h.model, key("e"))
	if got := h.model.ta.Value(); got != "original-b" {
		t.Fatalf("B editor seed = %q, want its untouched payload", got)
	}
	h.model = pauseKey(t, h.model, tea.KeyMsg{Type: tea.KeyEsc})
	h.model = pauseKey(t, h.model, key("f"))
	if got := awaitPauseOutcome(t, "B", bResult); got.drop || got.body != nil {
		t.Fatalf("B resolution = {drop:%v body:%q}, want unchanged forward", got.drop, got.body)
	}
}

func TestPauseQueuePromotesFIFOAndResolvesMatchingFlows(t *testing.T) {
	h := newPauseModelHarness(t)
	a, aResult := h.startPause(t, "a", "body-a")
	b, bResult := h.startPause(t, "b", "body-b")
	c, cResult := h.startPause(t, "c", "body-c")
	h.notify(a)
	h.notify(b)
	h.notify(c)

	if h.model.paused != a.Flow || len(h.model.pauseQueue) != 2 {
		t.Fatalf("initial pause order lost: active=%v queued=%d", h.model.paused, len(h.model.pauseQueue))
	}

	h.model = pauseKey(t, h.model, key("f"))
	if got := awaitPauseOutcome(t, "A", aResult); got.drop || got.body != nil {
		t.Fatalf("A resolution = {drop:%v body:%q}, want unchanged forward", got.drop, got.body)
	}
	if h.model.paused != b.Flow || h.model.pausedMsg != b.Msg || h.model.pausedToken != b.Token {
		t.Fatal("B was not the first promoted pause")
	}

	h.model = pauseKey(t, h.model, key("d"))
	if got := awaitPauseOutcome(t, "B", bResult); !got.drop || got.body != nil {
		t.Fatalf("B resolution = {drop:%v body:%q}, want drop", got.drop, got.body)
	}
	if h.model.paused != c.Flow || h.model.pausedMsg != c.Msg || h.model.pausedToken != c.Token {
		t.Fatal("C was not the second promoted pause")
	}

	h.model = pauseKey(t, h.model, key("e"))
	h.model.ta.SetValue("edited-c")
	h.model = pauseKey(t, h.model, tea.KeyMsg{Type: tea.KeyCtrlS})
	if got := awaitPauseOutcome(t, "C", cResult); got.drop || string(got.body) != "edited-c" {
		t.Fatalf("C resolution = {drop:%v body:%q}, want edited forward", got.drop, got.body)
	}
	if h.model.paused != nil || h.model.pausedMsg != nil || h.model.pausedToken != 0 || len(h.model.pauseQueue) != 0 {
		t.Fatal("pause state was not empty after resolving the queue")
	}
	if h.model.status != "forwarded (edited)" {
		t.Fatalf("final status = %q, want edited-forward completion", h.model.status)
	}
}

func TestPauseQueueResolvesConcurrentSameFlowPausesOutOfOrder(t *testing.T) {
	h := newPauseModelHarness(t)
	flow := capture.NewFlow("client", "shared.example:443")
	flow.SNI = "shared.example"
	request := &capture.Message{Raw: []byte("request")}
	response := &capture.Message{Raw: []byte("response")}
	requestResult := make(chan pauseOutcome, 1)
	responseResult := make(chan pauseOutcome, 1)

	go func() {
		body, drop := h.model.engine.BeforeForward(flow, request)
		requestResult <- pauseOutcome{body: body, drop: drop}
	}()
	var requestPause Paused
	select {
	case requestPause = <-h.paused:
	case <-time.After(time.Second):
		t.Fatal("request did not pause")
	}

	go func() {
		body, drop := h.model.engine.BeforeDeliver(flow, response)
		responseResult <- pauseOutcome{body: body, drop: drop}
	}()
	var responsePause Paused
	select {
	case responsePause = <-h.paused:
	case <-time.After(time.Second):
		t.Fatal("response did not pause")
	}

	if requestPause.Flow != flow || responsePause.Flow != flow {
		t.Fatal("same-flow pause notifications did not retain the shared flow")
	}
	if requestPause.Token == responsePause.Token {
		t.Fatalf("same-flow pauses shared token %d", requestPause.Token)
	}

	// Concurrent callbacks can be delivered in either order. Queue the response
	// first even though the request hook blocked first, then resolve FIFO.
	h.notify(responsePause)
	h.notify(requestPause)
	if h.model.paused != flow || h.model.pausedMsg != response || h.model.pausedToken != responsePause.Token {
		t.Fatal("response pause was not active with its token")
	}
	if len(h.model.pauseQueue) != 1 ||
		h.model.pauseQueue[0].Token != requestPause.Token ||
		h.model.pauseQueue[0].Flow != flow ||
		h.model.pauseQueue[0].Msg != request {
		t.Fatalf("queued same-flow request = %#v, want its own token and message", h.model.pauseQueue)
	}

	h.model = pauseKey(t, h.model, key("d"))
	if got := awaitPauseOutcome(t, "response", responseResult); !got.drop || got.body != nil {
		t.Fatalf("response resolution = {drop:%v body:%q}, want drop", got.drop, got.body)
	}
	select {
	case <-requestResult:
		t.Fatal("resolving the response token also released the request")
	case <-time.After(20 * time.Millisecond):
	}
	if flow.Status != capture.StatusPending {
		t.Fatalf("shared flow status with queued request = %s, want PAUSED", flow.Status)
	}
	if h.model.paused != flow || h.model.pausedMsg != request || h.model.pausedToken != requestPause.Token {
		t.Fatal("request pause was not promoted with its own token")
	}

	h.model = pauseKey(t, h.model, key("e"))
	h.model.ta.SetValue("edited request")
	h.model = pauseKey(t, h.model, tea.KeyMsg{Type: tea.KeyCtrlS})
	if got := awaitPauseOutcome(t, "request", requestResult); got.drop || string(got.body) != "edited request" {
		t.Fatalf("request resolution = {drop:%v body:%q}, want edited forward", got.drop, got.body)
	}
	if h.model.paused != nil || h.model.pausedMsg != nil || h.model.pausedToken != 0 || len(h.model.pauseQueue) != 0 {
		t.Fatal("same-flow pause queue was not empty after both resolutions")
	}
	if flow.Status != capture.StatusActive {
		t.Fatalf("shared flow status after both resolutions = %s, want active", flow.Status)
	}
}

func TestPauseActivationClearsPendingLeaderBeforeShortcut(t *testing.T) {
	h := newPauseModelHarness(t)
	item, result := h.startPause(t, "leader", "body")

	h.model = pauseKey(t, h.model, tea.KeyMsg{Type: h.model.km().Leader})
	if !h.model.pendingLeader {
		t.Fatal("test setup did not enter the leader-key state")
	}
	h.notify(item)
	if h.model.pendingLeader {
		t.Fatal("pause activation left a stale leader key pending")
	}

	// Exercise the production Update path: the prompt's plain f must resolve the
	// pause, not dispatch leader-f (export flagged).
	h.model = pauseKey(t, h.model, key("f"))
	if got := awaitPauseOutcome(t, "leader", result); got.drop || got.body != nil {
		t.Fatalf("resolution = {drop:%v body:%q}, want unchanged forward", got.drop, got.body)
	}
	if h.model.paused != nil || h.model.status != "forwarded" {
		t.Fatalf("pause shortcut did not complete: active=%v status=%q", h.model.paused, h.model.status)
	}
}

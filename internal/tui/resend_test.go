package tui

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/citizen-123/cli-capture/internal/capture"
	"github.com/citizen-123/cli-capture/internal/intercept"
	"github.com/citizen-123/cli-capture/internal/repeater"
)

func requestJobTestModel(flow *capture.Flow) Model {
	return Model{
		store: capture.NewStore(), engine: intercept.NewEngine(),
		flows: []*capture.Flow{flow}, fi: newFilter(), ci: newCommandLine(),
		focus: focusTraffic, width: 100, height: 36,
	}
}

func requestJobTestFlow(target string) *capture.Flow {
	flow := capture.NewFlow("c", strings.TrimPrefix(target, "http://"))
	flow.Protocol = capture.ProtoHTTP1
	flow.Request = &capture.Message{Meta: map[string]string{"method": http.MethodGet, "path": "/"}}
	return flow
}

func nextRequestJobEvent(t *testing.T, cmd tea.Cmd) jobEventMsg {
	t.Helper()
	if cmd == nil {
		t.Fatal("missing request-job command")
	}
	messages := make(chan tea.Msg, 1)
	go func() { messages <- cmd() }()
	select {
	case message := <-messages:
		event, ok := message.(jobEventMsg)
		if !ok {
			t.Fatalf("request-job command returned %T", message)
		}
		return event
	case <-time.After(3 * time.Second):
		t.Fatal("request-job command did not return")
		return jobEventMsg{}
	}
}

func finishRequestJob(t *testing.T, model *Model, cmd tea.Cmd) jobEventMsg {
	t.Helper()
	var last jobEventMsg
	for cmd != nil {
		last = nextRequestJobEvent(t, cmd)
		cmd = model.handleJobEvent(last)
	}
	if model.job.active() {
		t.Fatal("job lost its waiter before reaching a terminal outcome")
	}
	return last
}

func TestResendSelectedStoresFailedExchange(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "10")
		_, _ = io.WriteString(w, "short")
	}))
	defer server.Close()
	model := requestJobTestModel(requestJobTestFlow(server.URL))
	progress := finishRequestJob(t, &model, model.startResend())
	if progress.attempted != 1 || progress.failed != 1 || progress.succeeded != 0 || progress.outcome != jobComplete {
		t.Fatalf("failed resend progress = %+v", progress)
	}
	stored := model.store.List()
	if len(stored) != 1 {
		t.Fatalf("stored flows = %d, want 1", len(stored))
	}
	if stored[0].Status != capture.StatusError || !errors.Is(stored[0].Err, io.ErrUnexpectedEOF) {
		t.Errorf("stored flow status = %v, err = %v", stored[0].Status, stored[0].Err)
	}
	if stored[0].Response != nil {
		t.Errorf("stored flow retained a partial response: %+v", stored[0].Response)
	}
}

func TestResendSnapshotsRequestAndKeepsInputResponsiveUntilCancellation(t *testing.T) {
	type receipt struct{ path, header, body string }
	received := make(chan receipt, 1)
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return
		}
		received <- receipt{r.URL.Path, r.Header.Get("X-Snapshot"), string(body)}
		<-r.Context().Done()
	}))
	defer server.Close()
	original := requestJobTestFlow(server.URL)
	original.Request.Meta["method"] = http.MethodPost
	original.Request.Meta["path"] = "/original"
	original.Request.Headers = http.Header{"X-Snapshot": {"original"}}
	original.Request.Body = []byte("original")
	model := requestJobTestModel(original)
	cmd := model.startResend()
	defer model.job.cancel()
	original.Request.Meta["path"] = "/changed"
	original.Request.Headers["X-Snapshot"][0] = "changed"
	copy(original.Request.Body, "mutated!")
	if duplicate := model.startResend(); duplicate != nil {
		t.Fatal("duplicate job was accepted before command dispatch")
	}
	first := nextRequestJobEvent(t, cmd)
	cmd = model.handleJobEvent(first)
	select {
	case got := <-received:
		if got != (receipt{"/original", "original", "original"}) {
			t.Fatalf("origin saw mutable request: %+v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("request did not reach held endpoint")
	}
	updated, _ := model.Update(tea.KeyMsg{Type: model.km().Leader})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	model = updated.(Model)
	if !model.showHelp || !model.job.active() {
		t.Fatal("help did not open while the request was held")
	}
	model.cancelJob()
	progress := finishRequestJob(t, &model, cmd)
	if progress.outcome != jobCanceled || progress.attempted != 1 || progress.canceled != 1 || progress.succeeded != 0 || progress.failed != 0 {
		t.Fatalf("canceled resend progress = %+v", progress)
	}
	if stored := model.store.List(); len(stored) != 1 || !errors.Is(stored[0].Err, context.Canceled) {
		t.Fatalf("canceled exchange not retained: %+v", stored)
	}
}

func TestRequestJobCoalescesProgressWithoutLosingResults(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := capture.NewStore()
	events := make(chan jobEventMsg, 1)
	attack := repeater.Attack{Mode: repeater.Sniper, Positions: []string{"value"}, Lists: [][]string{{"good", "bad", "good-again"}}}
	finished := make(chan struct{})
	go func() {
		runRequestJob(ctx, cancel, events, jobEventMsg{jobID: 1, planned: 3, outcome: jobRunning}, attack, store,
			func(_ context.Context, vars map[string]string) (*capture.Flow, error) {
				flow := requestJobTestFlow("http://example.test")
				if vars["value"] == "bad" {
					flow.Status, flow.Err = capture.StatusError, io.ErrUnexpectedEOF
					return flow, flow.Err
				}
				flow.Status = capture.StatusComplete
				return flow, nil
			})
		close(finished)
	}()
	// No progress consumer runs until the worker has finished.
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("worker blocked on an unconsumed progress event")
	}
	terminal := <-events
	if terminal.outcome != jobComplete || terminal.attempted != 3 || terminal.succeeded != 2 || terminal.failed != 1 {
		t.Fatalf("coalesced terminal outcome = %+v", terminal)
	}
	stored := store.List()
	if len(stored) != 3 {
		t.Fatalf("retained %d exchanges, want all 3", len(stored))
	}
	for i, want := range []string{"value=good", "value=bad", "value=good-again"} {
		if got := stored[i].Request.Meta["payload"]; got != want {
			t.Errorf("exchange %d payload = %q, want %q", i, got, want)
		}
	}
}

func TestRequestJobCancellationStopsFutureIterationsAndRetainsSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := capture.NewStore()
	events := make(chan jobEventMsg, 1)
	attack := repeater.Attack{Mode: repeater.Sniper, Positions: []string{"id"}, Lists: [][]string{{"1", "2", "3"}}}
	attempts := 0
	runRequestJob(ctx, cancel, events, jobEventMsg{planned: 3, outcome: jobRunning}, attack, store,
		func(context.Context, map[string]string) (*capture.Flow, error) {
			attempts++
			flow := requestJobTestFlow("http://example.test")
			flow.Status = capture.StatusComplete
			cancel()
			return flow, nil
		})
	terminal := <-events
	if attempts != 1 || terminal.attempted != 1 || terminal.succeeded != 1 || terminal.failed != 0 || terminal.outcome != jobCanceled {
		t.Fatalf("cancel-between-requests progress = %+v, calls = %d", terminal, attempts)
	}
	if stored := store.List(); len(stored) != 1 || stored[0].Status != capture.StatusComplete {
		t.Fatalf("completed exchange lost after cancellation: %+v", stored)
	}
}

func TestRequestJobTimeoutIsFailureNotOperatorCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan jobEventMsg, 1)
	runRequestJob(ctx, cancel, events, jobEventMsg{planned: 1, outcome: jobRunning}, repeater.Attack{Mode: repeater.Single}, capture.NewStore(),
		func(context.Context, map[string]string) (*capture.Flow, error) { return nil, context.DeadlineExceeded })
	terminal := <-events
	if terminal.outcome != jobComplete || terminal.failed != 1 || terminal.canceled != 0 {
		t.Fatalf("network timeout treated as cancellation: %+v", terminal)
	}
}

func TestCloseJoinsCanceledJobBeforeCommandDispatch(t *testing.T) {
	model := requestJobTestModel(requestJobTestFlow("http://example.test"))
	calls := 0
	cmd := model.startRequestJob("resend", 0, repeater.Attack{Mode: repeater.Single}, 1,
		func(context.Context, map[string]string) (*capture.Flow, error) {
			calls++
			return nil, errors.New("must not send after shutdown")
		})
	closed := make(chan struct{})
	go func() {
		model.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("Close blocked on an undispatched command")
	}
	terminal := nextRequestJobEvent(t, cmd)
	if calls != 0 || terminal.attempted != 0 || terminal.outcome != jobCanceled {
		t.Fatalf("undispatched job sent after shutdown: calls=%d, progress=%+v", calls, terminal)
	}
}

func TestCloseCancelsHeldIOAndJoinsWorker(t *testing.T) {
	started, disconnected := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(disconnected)
	}))
	defer server.Close()
	model := requestJobTestModel(requestJobTestFlow(server.URL))
	cmd := model.startResend()
	defer model.job.cancel()
	first := nextRequestJobEvent(t, cmd)
	cmd = model.handleJobEvent(first)
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("held request did not start")
	}
	closed := make(chan struct{})
	go func() {
		model.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not join the canceled worker")
	}
	select {
	case <-disconnected:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown left the origin request active")
	}
	progress := finishRequestJob(t, &model, cmd)
	if progress.outcome != jobCanceled || progress.canceled != 1 || progress.succeeded != 0 {
		t.Fatalf("shutdown outcome = %+v", progress)
	}
}

func TestCompletedExchangeWinsCancellationAfterFinalResponse(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan jobEventMsg, 1)
	runRequestJob(ctx, cancel, events, jobEventMsg{planned: 1, outcome: jobRunning}, repeater.Attack{Mode: repeater.Single}, capture.NewStore(),
		func(context.Context, map[string]string) (*capture.Flow, error) {
			flow := requestJobTestFlow("http://example.test")
			flow.Status = capture.StatusComplete
			cancel()
			return flow, nil
		})
	terminal := <-events
	if terminal.outcome != jobComplete || terminal.succeeded != 1 || terminal.canceled != 0 {
		t.Fatalf("completed response misreported by late cancellation: %+v", terminal)
	}
}

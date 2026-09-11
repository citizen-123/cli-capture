package tui

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"sync"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/citizen-123/cli-capture/internal/capture"
	"github.com/citizen-123/cli-capture/internal/repeater"
	"github.com/citizen-123/cli-capture/internal/replay"
)

type jobOutcome string

const (
	jobRunning  jobOutcome = "running"
	jobComplete jobOutcome = "complete"
	jobCanceled jobOutcome = "canceled"
)

// requestJob is mutated only by the model. The worker owns its event snapshots
// and the single-slot channel, never this state or a live request draft.
type requestJob struct {
	id              uint64
	sessionID       uint64
	kind            string
	cancel          context.CancelFunc
	events          <-chan jobEventMsg
	start           func()
	done            <-chan struct{}
	cancelRequested bool
	progress        jobEventMsg
}

type jobEventMsg struct {
	jobID     uint64
	sessionID uint64
	planned   int
	attempted int
	succeeded int
	failed    int
	canceled  int
	outcome   jobOutcome
	flow      *capture.Flow
	err       error
}

func (j *requestJob) active() bool {
	return j != nil && j.progress.outcome == jobRunning
}

func (m *Model) startResend() tea.Cmd {
	if m.job.active() {
		m.status = "manual job already active"
		return nil
	}
	flow := m.selectedFlow()
	if flow == nil {
		m.status = "nothing selected to resend"
		return nil
	}
	snapshot := snapshotRequestFlow(flow)
	return m.startRequestJob("resend", 0, repeater.Attack{Mode: repeater.Single}, 1,
		func(ctx context.Context, _ map[string]string) (*capture.Flow, error) {
			return replay.Resend(ctx, snapshot)
		})
}

func (m *Model) startRequestJob(kind string, sessionID uint64, attack repeater.Attack, planned int,
	send func(context.Context, map[string]string) (*capture.Flow, error),
) tea.Cmd {
	if m.job.active() {
		m.status = "manual job already active"
		return nil
	}
	m.nextJobID++
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan jobEventMsg, 1)
	done := make(chan struct{})
	store := m.store
	var once sync.Once
	progress := jobEventMsg{jobID: m.nextJobID, sessionID: sessionID, planned: planned, outcome: jobRunning}
	start := func() {
		once.Do(func() {
			go func() {
				defer close(done)
				runRequestJob(ctx, cancel, events, progress, attack, store, send)
			}()
		})
	}
	m.job = &requestJob{
		id: progress.jobID, sessionID: sessionID, kind: kind,
		cancel: cancel, events: events, start: start, done: done, progress: progress,
	}
	if sessionID != 0 && sessionID == m.rep.sessionID {
		m.rep.jobID = progress.jobID
		m.rep.resp = nil
		m.rep.result = ""
		m.rep.respVP.SetContent("")
		m.rep.respVP.GotoTop()
	}
	m.status = "started " + kind
	m.sizeRepeater()
	return func() tea.Msg {
		start()
		return waitJobEvent(events)()
	}
}

// publishJobEvent coalesces progress without delaying the network worker. There
// is one producer: replacing the queued event also reliably retains its terminal
// outcome, even when shutdown stops the model from receiving more messages.
func publishJobEvent(events chan jobEventMsg, event jobEventMsg) {
	select {
	case events <- event:
		return
	default:
	}
	select {
	case <-events:
	default:
	}
	events <- event
}

func runRequestJob(ctx context.Context, cancel context.CancelFunc, events chan jobEventMsg,
	progress jobEventMsg, attack repeater.Attack, store *capture.Store,
	send func(context.Context, map[string]string) (*capture.Flow, error),
) {
	defer cancel()
	defer close(events)
	for vars := range attack.Jobs() {
		if ctx.Err() != nil {
			progress.outcome = jobCanceled
			break
		}
		progress.attempted++
		publishJobEvent(events, progress)
		flow, err := send(ctx, vars)
		progress.flow, progress.err = flow, err
		if flow != nil {
			if flow.Request != nil && len(attack.Positions) != 0 {
				if flow.Request.Meta == nil {
					flow.Request.Meta = make(map[string]string)
				}
				flow.Request.Meta["payload"] = jobLabel(vars, attack.Positions)
			}
			store.Add(flow)
		}
		switch {
		case ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)):
			progress.canceled++
			progress.outcome = jobCanceled
		case err != nil:
			progress.failed++
		case flow != nil && flow.Status == capture.StatusComplete:
			progress.succeeded++
		default:
			progress.failed++
			if flow != nil && flow.Err != nil {
				progress.err = flow.Err
			} else {
				progress.err = fmt.Errorf("request produced no completed exchange")
			}
		}
		publishJobEvent(events, progress)
		if progress.outcome == jobCanceled {
			break
		}
	}
	if progress.outcome == jobRunning {
		progress.outcome = jobComplete
	}
	publishJobEvent(events, progress)
}

func waitJobEvent(events <-chan jobEventMsg) tea.Cmd {
	return func() tea.Msg {
		event, ok := <-events
		if !ok {
			return nil
		}
		return event
	}
}

func (m *Model) handleJobEvent(event jobEventMsg) tea.Cmd {
	if m.job == nil || event.jobID != m.job.id || !m.job.active() {
		return nil
	}
	m.job.progress = event
	if event.sessionID != 0 && event.sessionID == m.rep.sessionID && event.jobID == m.rep.jobID {
		if m.rep.resp != event.flow {
			m.rep.resp = event.flow
			m.rep.respVP.SetContent(renderRepeaterResponse(event.flow, m.rep.respVP.Width))
			m.rep.respVP.GotoTop()
		}
		m.rep.result = ""
		if event.err != nil {
			m.rep.result = "error: " + event.err.Error()
		}
	}
	m.sizeRepeater()
	if !m.job.active() {
		m.job.cancel()
		m.status = m.job.kind + " " + string(event.outcome)
		return nil
	}
	return waitJobEvent(m.job.events)
}

func (m *Model) cancelJob() {
	if !m.job.active() {
		m.status = "no active manual job"
		return
	}
	m.job.cancelRequested = true
	m.job.cancel()
	m.status = "canceling " + m.job.kind
	m.sizeRepeater()
}

// Close cancels and joins the manual worker, including a command that Bubble Tea
// has not dispatched yet. Starting an already-canceled job sends no requests.
func (m Model) Close() {
	if m.job == nil {
		return
	}
	m.job.cancel()
	m.job.start()
	<-m.job.done
}

func (m Model) jobStatus() string {
	if m.job == nil {
		return ""
	}
	p := m.job.progress
	state := string(p.outcome)
	if m.job.active() && m.job.cancelRequested {
		state = "canceling"
	}
	status := fmt.Sprintf("%s %s · plan%d try%d ok%d fail%d cancel%d",
		m.job.kind, state, p.planned, p.attempted, p.succeeded, p.failed, p.canceled)
	if m.job.active() {
		if key := m.leaderHint(ActJobCancel); key != "" {
			status = key + " cancel · " + status
		} else {
			status = ":cancel · " + status
		}
	}
	return status
}

func snapshotRequestFlow(flow *capture.Flow) *capture.Flow {
	out := &capture.Flow{
		Protocol:   flow.Protocol,
		ServerAddr: flow.ServerAddr,
		SNI:        flow.SNI,
		Secure:     flow.Secure,
		Request:    snapshotRequestMessage(flow.Request),
	}
	for _, message := range flow.Messages {
		if message != nil && message.Direction == capture.ClientToServer {
			out.Messages = append(out.Messages, snapshotRequestMessage(message))
		}
	}
	return out
}

func snapshotRequestMessage(message *capture.Message) *capture.Message {
	if message == nil {
		return nil
	}
	out := *message
	out.Headers = http.Header(message.Headers).Clone()
	out.Meta = maps.Clone(message.Meta)
	out.Body = append([]byte(nil), message.Body...)
	out.Raw = append([]byte(nil), message.Raw...)
	return &out
}

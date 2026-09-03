// Package intercept decides which flows to pause for manual tampering and
// carries the pause/resume handshake between the proxy goroutines and the UI.
// It implements protocol.Tamperer, so protocol parsers depend only on the
// narrow interface, not on this package. The "what is in scope" policy is
// delegated wholesale to a scope.Set.
package intercept

import (
	"context"
	"net"
	"sync"

	"github.com/citizen-123/cli-capture/internal/capture"
	"github.com/citizen-123/cli-capture/internal/scope"
)

// Decision is the user's verdict on a paused message.
type Decision int

const (
	Forward Decision = iota // send (possibly edited) upstream
	Drop                    // abort this message
)

// PauseToken uniquely identifies one blocking pause occurrence. It is
// independent of the flow because a flow can have multiple messages paused at
// the same time. The zero value is never issued.
type PauseToken uint64

// Resolution is what the UI sends back to release a paused message.
type Resolution struct {
	Decision Decision
	// EditedBody, when non-nil, replaces the outgoing bytes. nil means
	// "forward the original unchanged".
	EditedBody []byte
}

// Engine implements protocol.Tamperer.
type Engine struct {
	mu             sync.Mutex
	reqEnabled     bool
	respEnabled    bool
	scope          *scope.Set
	nextPauseToken PauseToken
	pending        map[PauseToken]chan Resolution
	pendingFlow    map[PauseToken]*capture.Flow
	pausedByFlow   map[*capture.Flow]int
	sessions       map[string]capture.Injector // live injectable sessions, by flow ID
	store          *capture.Store

	// onPause is called (off the UI goroutine) when a message becomes paused, so
	// the TUI can surface it. It carries the token needed to resolve this exact
	// pause and the outgoing message being tampered (msg.Raw seeds the editor).
	// Set by cmd/ wiring.
	onPause func(PauseToken, *capture.Flow, *capture.Message)
}

func NewEngine() *Engine {
	return &Engine{
		pending:      make(map[PauseToken]chan Resolution),
		pendingFlow:  make(map[PauseToken]*capture.Flow),
		pausedByFlow: make(map[*capture.Flow]int),
		sessions:     make(map[string]capture.Injector),
	}
}

// SetStore installs the snapshot store to update when an interception changes
// a flow's pause status.
func (e *Engine) SetStore(store *capture.Store) {
	e.mu.Lock()
	e.store = store
	e.mu.Unlock()
}

// RegisterSession records a live injectable session (implements
// capture.SessionRegistrar). Called by streaming protocol handlers on start.
func (e *Engine) RegisterSession(flowID string, inj capture.Injector) {
	e.mu.Lock()
	e.sessions[flowID] = inj
	e.mu.Unlock()
}

// UnregisterSession removes a session when its connection closes.
func (e *Engine) UnregisterSession(flowID string) {
	e.mu.Lock()
	delete(e.sessions, flowID)
	e.mu.Unlock()
}

// Session returns the live injector for a flow, if one is registered.
func (e *Engine) Session(flowID string) (capture.Injector, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	inj, ok := e.sessions[flowID]
	return inj, ok
}

// SetScope installs the scoping policy that decides which flows to pause.
func (e *Engine) SetScope(s *scope.Set) {
	e.mu.Lock()
	e.scope = s
	e.mu.Unlock()
}

// SetEnabled toggles request interception (client→server). When off, requests
// forward untouched regardless of scope.
func (e *Engine) SetEnabled(on bool) {
	e.mu.Lock()
	e.reqEnabled = on
	e.mu.Unlock()
}

// Enabled reports whether request interception is on.
func (e *Engine) Enabled() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.reqEnabled
}

// SetInterceptResponses toggles response interception (server→client). Off by
// default — pausing every response is noisier than pausing requests, so it is
// opt-in, matching how tools like Burp ship it.
func (e *Engine) SetInterceptResponses(on bool) {
	e.mu.Lock()
	e.respEnabled = on
	e.mu.Unlock()
}

// InterceptResponses reports whether response interception is on.
func (e *Engine) InterceptResponses() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.respEnabled
}

// OnPause installs the UI notification callback. Its flow and message arguments
// are independent snapshots; callers must resolve using the token rather than
// retaining or mutating live capture state.
func (e *Engine) OnPause(fn func(PauseToken, *capture.Flow, *capture.Message)) {
	e.mu.Lock()
	e.onPause = fn
	e.mu.Unlock()
}

// BeforeForward is the request-side Tamperer hook. See pause for behavior.
func (e *Engine) BeforeForward(f *capture.Flow, msg *capture.Message) (out []byte, drop bool) {
	return e.pause(context.Background(), f, msg, e.Enabled())
}

// BeforeForwardContext is the context-aware request-side hook. HTTP/2 uses it
// so a canceled stream releases a pending manual pause.
func (e *Engine) BeforeForwardContext(ctx context.Context, f *capture.Flow, msg *capture.Message) (out []byte, drop bool) {
	return e.pause(ctx, f, msg, e.Enabled())
}

// BeforeDeliver is the response-side Tamperer hook; gated by the separate
// response-interception toggle.
func (e *Engine) BeforeDeliver(f *capture.Flow, msg *capture.Message) (out []byte, drop bool) {
	return e.pause(context.Background(), f, msg, e.InterceptResponses())
}

// BeforeDeliverContext is the context-aware response-side hook. It shares the
// request context for an HTTP/2 stream.
func (e *Engine) BeforeDeliverContext(ctx context.Context, f *capture.Flow, msg *capture.Message) (out []byte, drop bool) {
	return e.pause(ctx, f, msg, e.InterceptResponses())
}

// ShouldInterceptRequest reports whether a request would be paused (enabled and
// in scope) — used by streaming transports to decide whether to buffer.
func (e *Engine) ShouldInterceptRequest(f *capture.Flow) bool {
	return e.inScopeAnd(e.Enabled(), f)
}

// ShouldInterceptResponse is the response-direction counterpart.
func (e *Engine) ShouldInterceptResponse(f *capture.Flow) bool {
	return e.inScopeAnd(e.InterceptResponses(), f)
}

func (e *Engine) inScopeAnd(directionOn bool, f *capture.Flow) bool {
	if !directionOn {
		return false
	}
	e.mu.Lock()
	sc := e.scope
	e.mu.Unlock()
	return sc != nil && sc.InScope(TargetFromFlow(f))
}

// pause marks an in-scope flow pending, notifies the UI with immutable
// snapshots, then waits for either a resolution or transport cancellation.
func (e *Engine) pause(ctx context.Context, f *capture.Flow, msg *capture.Message, directionOn bool) (out []byte, drop bool) {
	if !e.inScopeAnd(directionOn, f) {
		return nil, false // pass through original, unchanged
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return nil, true
	}

	ch := make(chan Resolution, 1)
	publishPending := false
	e.mu.Lock()
	if ctx.Err() != nil {
		e.mu.Unlock()
		return nil, true
	}
	var token PauseToken
	for {
		e.nextPauseToken++
		token = e.nextPauseToken
		if token == 0 {
			continue
		}
		if _, exists := e.pending[token]; !exists {
			break
		}
	}
	e.pending[token] = ch
	e.pendingFlow[token] = f
	e.pausedByFlow[f]++
	f.Mutate(func() {
		if f.Status != capture.StatusPending {
			f.Status = capture.StatusPending
			publishPending = true
		}
	})
	store := e.store
	onPause := e.onPause
	e.mu.Unlock()

	if publishPending && store != nil {
		store.Touch(f)
	}

	// Claim-based Resolve normally removes the entry. The conditional deferred
	// cleanup also covers a panicking callback without deleting a token that may
	// have been reused after counter wrap.
	defer func() {
		publishCleanup := false
		e.mu.Lock()
		if e.pending[token] == ch {
			delete(e.pending, token)
			delete(e.pendingFlow, token)
		}
		remaining := e.pausedByFlow[f] - 1
		if remaining == 0 {
			delete(e.pausedByFlow, f)
			// A concurrent terminal path (for example a completed response on
			// a bidi stream) must win over this pause cleanup. Only restore
			// Active when this pause still owns the Pending transition.
			f.Mutate(func() {
				if f.Status == capture.StatusPending {
					f.Status = capture.StatusActive
				}
			})
			publishCleanup = true
		} else {
			e.pausedByFlow[f] = remaining
		}
		store := e.store
		e.mu.Unlock()
		if publishCleanup && store != nil {
			store.Touch(f)
		}
	}()

	if onPause != nil {
		onPause(token, f.Snapshot(), msg.Snapshot())
	}

	select {
	case res := <-ch:
		if ctx.Err() != nil || res.Decision == Drop {
			return nil, true
		}
		return res.EditedBody, false
	case <-ctx.Done():
		return nil, true
	}
}

// Resolve releases one paused message. It returns false without blocking when
// token is unknown or was already resolved.
func (e *Engine) Resolve(token PauseToken, res Resolution) bool {
	e.mu.Lock()
	ch, ok := e.pending[token]
	if ok {
		// Claim this pause while holding the lock so concurrent or duplicate
		// resolutions cannot both send to its channel.
		delete(e.pending, token)
		delete(e.pendingFlow, token)
	}
	e.mu.Unlock()
	if !ok {
		return false
	}
	ch <- res
	return true
}

// CancelFlow releases every pause belonging to a terminating connection.
// Cancellation is a drop because there is no peer left to receive a resumed
// message. It is idempotent and races safely with Resolve.
func (e *Engine) CancelFlow(f *capture.Flow) {
	if f == nil {
		return
	}
	e.mu.Lock()
	var pending []chan Resolution
	for token, flow := range e.pendingFlow {
		if flow != f {
			continue
		}
		ch := e.pending[token]
		delete(e.pending, token)
		delete(e.pendingFlow, token)
		pending = append(pending, ch)
	}
	e.mu.Unlock()
	for _, ch := range pending {
		ch <- Resolution{Decision: Drop}
	}
}

// TargetFromFlow projects a capture.Flow onto the neutral scope.Target the
func TargetFromFlow(f *capture.Flow) scope.Target {
	snapshot := f.Snapshot()
	if snapshot == nil {
		return scope.Target{}
	}
	host := snapshot.SNI
	if host == "" {
		host = hostOnly(snapshot.ServerAddr)
	}
	t := scope.Target{Host: host, Protocol: string(snapshot.Protocol)}
	if snapshot.Request != nil {
		t.Method = snapshot.Request.Meta["method"]
		t.Path = snapshot.Request.Meta["path"]
		t.Body = snapshot.Request.Body
	}
	return t
}
func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return hostport
}

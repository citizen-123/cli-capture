// Package intercept decides which flows to pause for manual tampering and
// carries the pause/resume handshake between the proxy goroutines and the UI.
// It implements protocol.Tamperer, so protocol parsers depend only on the
// narrow interface, not on this package. The "what is in scope" policy is
// delegated wholesale to a scope.Set.
package intercept

import (
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

// Resolution is what the UI sends back to release a paused flow.
type Resolution struct {
	Decision Decision
	// EditedBody, when non-nil, replaces the outgoing bytes. nil means
	// "forward the original unchanged".
	EditedBody []byte
}

// Engine implements protocol.Tamperer.
type Engine struct {
	mu          sync.Mutex
	reqEnabled  bool
	respEnabled bool
	scope       *scope.Set
	pending     map[string]chan Resolution  // keyed by flow ID
	sessions    map[string]capture.Injector // live injectable sessions, by flow ID

	// onPause is called (off the UI goroutine) when a flow becomes paused, so
	// the TUI can surface it. It carries the specific outgoing message being
	// tampered (msg.Raw is what the editor seeds from). Set by cmd/ wiring.
	onPause func(*capture.Flow, *capture.Message)
}

func NewEngine() *Engine {
	return &Engine{
		pending:  make(map[string]chan Resolution),
		sessions: make(map[string]capture.Injector),
	}
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

func (e *Engine) OnPause(fn func(*capture.Flow, *capture.Message)) {
	e.mu.Lock()
	e.onPause = fn
	e.mu.Unlock()
}

// BeforeForward is the request-side Tamperer hook. See pause for behavior.
func (e *Engine) BeforeForward(f *capture.Flow, msg *capture.Message) (out []byte, drop bool) {
	return e.pause(f, msg, e.Enabled())
}

// BeforeDeliver is the response-side Tamperer hook; gated by the separate
// response-interception toggle.
func (e *Engine) BeforeDeliver(f *capture.Flow, msg *capture.Message) (out []byte, drop bool) {
	return e.pause(f, msg, e.InterceptResponses())
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

// pause is the shared intercept handshake. If this direction is enabled and the
// flow is in scope, it marks the flow pending, notifies the UI, and blocks the
// calling (per-connection) goroutine until the UI resolves it. It never blocks
// the UI goroutine — the proxy runs one goroutine per connection, so only that
// connection stalls. Request and response never pause concurrently for the same
// flow (they are sequential), so keying pending by flow ID is safe.
func (e *Engine) pause(f *capture.Flow, msg *capture.Message, directionOn bool) (out []byte, drop bool) {
	if !e.inScopeAnd(directionOn, f) {
		return nil, false // pass through original, unchanged
	}

	e.mu.Lock()
	onPause := e.onPause
	e.mu.Unlock()

	ch := make(chan Resolution, 1)
	e.mu.Lock()
	e.pending[f.ID] = ch
	e.mu.Unlock()

	f.Status = capture.StatusPending
	if onPause != nil {
		onPause(f, msg)
	}

	res := <-ch // block until the UI calls Resolve

	e.mu.Lock()
	delete(e.pending, f.ID)
	e.mu.Unlock()

	f.Status = capture.StatusActive
	if res.Decision == Drop {
		return nil, true
	}
	return res.EditedBody, false
}

// Resolve releases a paused flow. Called from the UI when the user forwards,
// drops, or forwards-with-edits. Safe to call for an unknown id (no-op).
func (e *Engine) Resolve(flowID string, res Resolution) {
	e.mu.Lock()
	ch := e.pending[flowID]
	e.mu.Unlock()
	if ch != nil {
		ch <- res
	}
}

// TargetFromFlow projects a capture.Flow onto the neutral scope.Target the
// matching engine understands. For non-HTTP flows the method/path/body fields
// are simply empty, so host/protocol rules still apply.
func TargetFromFlow(f *capture.Flow) scope.Target {
	host := f.SNI
	if host == "" {
		host = hostOnly(f.ServerAddr)
	}
	t := scope.Target{Host: host, Protocol: string(f.Protocol)}
	if f.Request != nil {
		t.Method = f.Request.Meta["method"]
		t.Path = f.Request.Meta["path"]
		t.Body = f.Request.Body
	}
	return t
}

func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return hostport
}

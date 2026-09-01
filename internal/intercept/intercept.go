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
	pausedByFlow   map[*capture.Flow]int
	sessions       map[string]capture.Injector // live injectable sessions, by flow ID

	// onPause is called (off the UI goroutine) when a message becomes paused, so
	// the TUI can surface it. It carries the token needed to resolve this exact
	// pause and the outgoing message being tampered (msg.Raw seeds the editor).
	// Set by cmd/ wiring.
	onPause func(PauseToken, *capture.Flow, *capture.Message)
}

func NewEngine() *Engine {
	return &Engine{
		pending:      make(map[PauseToken]chan Resolution),
		pausedByFlow: make(map[*capture.Flow]int),
		sessions:     make(map[string]capture.Injector),
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

func (e *Engine) OnPause(fn func(PauseToken, *capture.Flow, *capture.Message)) {
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
// flow is in scope, it marks the flow pending, registers a unique pause token,
// notifies the UI, and blocks the calling (per-connection) goroutine until the
// UI resolves that token. It never blocks the UI goroutine.
func (e *Engine) pause(f *capture.Flow, msg *capture.Message, directionOn bool) (out []byte, drop bool) {
	if !e.inScopeAnd(directionOn, f) {
		return nil, false // pass through original, unchanged
	}

	ch := make(chan Resolution, 1)
	e.mu.Lock()
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
	e.pausedByFlow[f]++
	f.Status = capture.StatusPending
	onPause := e.onPause
	e.mu.Unlock()

	// Claim-based Resolve normally removes the entry. The conditional deferred
	// cleanup also covers a panicking callback without deleting a token that may
	// have been reused after counter wrap.
	defer func() {
		e.mu.Lock()
		if e.pending[token] == ch {
			delete(e.pending, token)
		}
		remaining := e.pausedByFlow[f] - 1
		if remaining == 0 {
			delete(e.pausedByFlow, f)
			f.Status = capture.StatusActive
		} else {
			e.pausedByFlow[f] = remaining
		}
		e.mu.Unlock()
	}()

	if onPause != nil {
		onPause(token, f, msg)
	}

	res := <-ch // block until the UI calls Resolve
	if res.Decision == Drop {
		return nil, true
	}
	return res.EditedBody, false
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
	}
	e.mu.Unlock()
	if !ok {
		return false
	}
	ch <- res
	return true
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

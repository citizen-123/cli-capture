package intercept

import (
	"testing"
	"time"

	"github.com/citizen-123/cli-capture/internal/capture"
	"github.com/citizen-123/cli-capture/internal/scope"
)

type fakeInjector struct{ n int }

func (f *fakeInjector) Inject(capture.Direction, byte, []byte) error { f.n++; return nil }

func TestSessionRegistry(t *testing.T) {
	e := NewEngine()
	if _, ok := e.Session("f1"); ok {
		t.Fatal("no session should be registered yet")
	}
	fi := &fakeInjector{}
	e.RegisterSession("f1", fi)

	got, ok := e.Session("f1")
	if !ok || got != capture.Injector(fi) {
		t.Fatal("registered session not returned")
	}
	e.UnregisterSession("f1")
	if _, ok := e.Session("f1"); ok {
		t.Error("session should be gone after Unregister")
	}
}

func inScopeFlow(host string) *capture.Flow {
	f := capture.NewFlow("client", host+":443")
	f.SNI = host
	f.Protocol = capture.ProtoHTTP1
	f.Request = &capture.Message{Meta: map[string]string{"method": "GET", "path": "/"}}
	return f
}

// TestOutOfScopeForwardsImmediately: a flow not in scope must not pause.
func TestOutOfScopeForwardsImmediately(t *testing.T) {
	e := NewEngine()
	set, _ := scope.Build([]string{"*.github.com"}, nil, false) // allowlist
	e.SetScope(set)
	e.SetEnabled(true)

	out, drop := e.BeforeForward(inScopeFlow("example.com"), &capture.Message{})
	if out != nil || drop {
		t.Fatalf("out-of-scope flow should forward unchanged, got out=%v drop=%v", out, drop)
	}
}

// TestDisabledForwardsImmediately: interception off ⇒ never pause.
func TestDisabledForwardsImmediately(t *testing.T) {
	e := NewEngine()
	set, _ := scope.Build([]string{"*.github.com"}, nil, false)
	e.SetScope(set)
	e.SetEnabled(false)

	out, drop := e.BeforeForward(inScopeFlow("api.github.com"), &capture.Message{})
	if out != nil || drop {
		t.Fatalf("disabled engine should forward unchanged")
	}
}

// TestResponsesNotInterceptedByDefault: enabling request interception must not
// pause responses — response interception is a separate opt-in toggle.
func TestResponsesNotInterceptedByDefault(t *testing.T) {
	e := NewEngine()
	set, _ := scope.Build([]string{"*.github.com"}, nil, false)
	e.SetScope(set)
	e.SetEnabled(true) // requests on, responses left off

	out, drop := e.BeforeDeliver(inScopeFlow("api.github.com"), &capture.Message{})
	if out != nil || drop {
		t.Fatalf("response should pass through when response interception is off")
	}
}

// TestResponseInterceptEditsPayload: with response interception on, an in-scope
// response pauses and the edited bytes are delivered.
func TestResponseInterceptEditsPayload(t *testing.T) {
	e := NewEngine()
	set, _ := scope.Build([]string{"*.github.com"}, nil, false)
	e.SetScope(set)
	e.SetInterceptResponses(true)

	// pause() registers the pending channel before invoking onPause, so waiting
	// on this signal guarantees Resolve will find the flow.
	paused := make(chan struct{}, 1)
	e.OnPause(func(_ *capture.Flow, _ *capture.Message) { paused <- struct{}{} })

	flow := inScopeFlow("api.github.com")
	result := make(chan []byte, 1)
	go func() {
		out, _ := e.BeforeDeliver(flow, &capture.Message{})
		result <- out
	}()

	select {
	case <-paused:
	case <-time.After(time.Second):
		t.Fatal("response never became pending")
	}
	e.Resolve(flow.ID, Resolution{Decision: Forward, EditedBody: []byte("patched")})

	select {
	case out := <-result:
		if string(out) != "patched" {
			t.Fatalf("edited response not delivered, got %q", out)
		}
	case <-time.After(time.Second):
		t.Fatal("response not released after Resolve")
	}
}

// TestInScopePausesUntilResolved: an in-scope flow blocks until Resolve, and
// the UI's edited bytes come back out.
func TestInScopePausesUntilResolved(t *testing.T) {
	e := NewEngine()
	set, _ := scope.Build([]string{"*.github.com"}, nil, false)
	e.SetScope(set)
	e.SetEnabled(true)

	paused := make(chan *capture.Flow, 1)
	e.OnPause(func(f *capture.Flow, _ *capture.Message) { paused <- f })

	flow := inScopeFlow("api.github.com")
	result := make(chan []byte, 1)
	go func() {
		out, _ := e.BeforeForward(flow, &capture.Message{})
		result <- out
	}()

	select {
	case f := <-paused:
		if f.Status != capture.StatusPending {
			t.Errorf("paused flow status = %s, want PAUSED", f.Status)
		}
	case <-time.After(time.Second):
		t.Fatal("in-scope flow never paused")
	}

	// Should still be blocked.
	select {
	case <-result:
		t.Fatal("BeforeForward returned before Resolve")
	case <-time.After(20 * time.Millisecond):
	}

	e.Resolve(flow.ID, Resolution{Decision: Forward, EditedBody: []byte("edited")})

	select {
	case out := <-result:
		if string(out) != "edited" {
			t.Errorf("edited bytes not returned, got %q", out)
		}
	case <-time.After(time.Second):
		t.Fatal("Resolve did not release the flow")
	}
}

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

func TestTargetFromFlowHostPolicyUsesCanonicalDNSCase(t *testing.T) {
	policy, err := scope.Build([]string{
		"=login.bank.example",
		"=fallback.bank.example",
		"=192.0.2.10",
		"=2001:db8::a",
	}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	engine := NewEngine()
	engine.SetScope(policy)
	engine.SetEnabled(true)

	tests := []struct {
		name       string
		serverAddr string
		sni        string
		wantHost   string
	}{
		{
			name:       "SNI takes precedence",
			serverAddr: "ignored.example:443",
			sni:        "LOGIN.BANK.EXAMPLE",
			wantHost:   "LOGIN.BANK.EXAMPLE",
		},
		{
			name:       "DNS authority port is removed",
			serverAddr: "FALLBACK.BANK.EXAMPLE:8443",
			wantHost:   "FALLBACK.BANK.EXAMPLE",
		},
		{
			name:       "IPv4 authority port is removed",
			serverAddr: "192.0.2.10:9443",
			wantHost:   "192.0.2.10",
		},
		{
			name:       "IPv6 brackets and port are removed",
			serverAddr: "[2001:DB8::A]:443",
			wantHost:   "2001:DB8::A",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flow := capture.NewFlow("client", tt.serverAddr)
			flow.SNI = tt.sni
			target := TargetFromFlow(flow)
			if target.Host != tt.wantHost {
				t.Fatalf("TargetFromFlow host = %q, want %q", target.Host, tt.wantHost)
			}
			if !policy.InScope(target) {
				t.Errorf("host %q should match its canonical host rule", target.Host)
			}
			if !engine.ShouldInterceptRequest(flow) {
				t.Errorf("host %q should be intercepted by its canonical host rule", target.Host)
			}
		})
	}
}

func TestTargetFromFlowPreservesNonHostCase(t *testing.T) {
	flow := capture.NewFlow("client", "example.com:443")
	flow.Request = &capture.Message{
		Meta: map[string]string{"method": "POST", "path": "/Case/Sensitive"},
		Body: []byte("TOKEN=SECRET"),
	}
	target := TargetFromFlow(flow)
	for _, spec := range []string{"method:=post", "path:=/case/sensitive", "body:secret"} {
		condition := mustScopeCondition(t, spec)
		if condition.Match(target) {
			t.Errorf("%q unexpectedly matched differently-cased flow data", spec)
		}
	}
}

func mustScopeCondition(t *testing.T, spec string) scope.Condition {
	t.Helper()
	condition, err := scope.ParseCondition(spec)
	if err != nil {
		t.Fatalf("ParseCondition(%q): %v", spec, err)
	}
	return condition
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
	// on this signal guarantees Resolve will find the token.
	paused := make(chan PauseToken, 1)
	e.OnPause(func(token PauseToken, _ *capture.Flow, _ *capture.Message) { paused <- token })

	flow := inScopeFlow("api.github.com")
	result := make(chan []byte, 1)
	go func() {
		out, _ := e.BeforeDeliver(flow, &capture.Message{})
		result <- out
	}()

	var token PauseToken
	select {
	case token = <-paused:
	case <-time.After(time.Second):
		t.Fatal("response never became pending")
	}
	e.Resolve(token, Resolution{Decision: Forward, EditedBody: []byte("patched")})

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

	paused := make(chan PauseToken, 1)
	e.OnPause(func(token PauseToken, _ *capture.Flow, _ *capture.Message) { paused <- token })

	flow := inScopeFlow("api.github.com")
	result := make(chan []byte, 1)
	go func() {
		out, _ := e.BeforeForward(flow, &capture.Message{})
		result <- out
	}()

	var token PauseToken
	select {
	case token = <-paused:
		if flow.Status != capture.StatusPending {
			t.Errorf("paused flow status = %s, want PAUSED", flow.Status)
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

	e.Resolve(token, Resolution{Decision: Forward, EditedBody: []byte("edited")})

	select {
	case out := <-result:
		if string(out) != "edited" {
			t.Errorf("edited bytes not returned, got %q", out)
		}
	case <-time.After(time.Second):
		t.Fatal("Resolve did not release the flow")
	}
}

func TestConcurrentSameFlowPausesResolveByUniqueToken(t *testing.T) {
	e := NewEngine()
	set, err := scope.Build(nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	e.SetScope(set)
	e.SetEnabled(true)
	e.SetInterceptResponses(true)

	type notice struct {
		token PauseToken
		msg   *capture.Message
	}
	paused := make(chan notice, 2)
	e.OnPause(func(token PauseToken, _ *capture.Flow, msg *capture.Message) {
		paused <- notice{token: token, msg: msg}
	})
	awaitPause := func(name string) notice {
		select {
		case item := <-paused:
			return item
		case <-time.After(time.Second):
			t.Fatalf("%s did not pause", name)
			return notice{}
		}
	}

	type outcome struct {
		body []byte
		drop bool
	}
	flow := inScopeFlow("api.github.com")
	request := &capture.Message{Raw: []byte("request")}
	response := &capture.Message{Raw: []byte("response")}
	requestDone := make(chan outcome, 1)
	responseDone := make(chan outcome, 1)

	go func() {
		body, drop := e.BeforeForward(flow, request)
		requestDone <- outcome{body: body, drop: drop}
	}()
	requestPause := awaitPause("request")

	go func() {
		body, drop := e.BeforeDeliver(flow, response)
		responseDone <- outcome{body: body, drop: drop}
	}()
	responsePause := awaitPause("response")

	if requestPause.msg != request || responsePause.msg != response {
		t.Fatal("pause notifications did not retain their matching messages")
	}
	if requestPause.token == 0 || responsePause.token == 0 {
		t.Fatal("engine issued the reserved zero pause token")
	}
	if requestPause.token == responsePause.token {
		t.Fatalf("concurrent pauses shared token %d", requestPause.token)
	}

	// Resolve the second pause first. It must not release or delete the first.
	if !e.Resolve(responsePause.token, Resolution{Decision: Drop}) {
		t.Fatal("response pause token was rejected")
	}
	if e.Resolve(responsePause.token, Resolution{Decision: Forward}) {
		t.Fatal("double resolution was accepted")
	}
	select {
	case got := <-responseDone:
		if !got.drop || got.body != nil {
			t.Fatalf("response resolution = {drop:%v body:%q}, want drop", got.drop, got.body)
		}
	case <-time.After(time.Second):
		t.Fatal("response pause was not released")
	}
	if flow.Status != capture.StatusPending {
		t.Fatalf("flow status after one of two resolutions = %s, want PAUSED", flow.Status)
	}
	select {
	case <-requestDone:
		t.Fatal("resolving the response token released the request pause")
	case <-time.After(20 * time.Millisecond):
	}

	if e.Resolve(PauseToken(1<<63), Resolution{Decision: Drop}) {
		t.Fatal("unknown pause token was accepted")
	}
	if !e.Resolve(requestPause.token, Resolution{
		Decision:   Forward,
		EditedBody: []byte("edited request"),
	}) {
		t.Fatal("request pause token was orphaned")
	}
	select {
	case got := <-requestDone:
		if got.drop || string(got.body) != "edited request" {
			t.Fatalf("request resolution = {drop:%v body:%q}, want edited forward", got.drop, got.body)
		}
	case <-time.After(time.Second):
		t.Fatal("request pause was not released")
	}
	if flow.Status != capture.StatusActive {
		t.Fatalf("flow status after both resolutions = %s, want active", flow.Status)
	}
}

func TestPauseTokenWrapSkipsOccupiedToken(t *testing.T) {
	e := NewEngine()
	set, err := scope.Build(nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	e.SetScope(set)
	e.SetEnabled(true)

	occupied := make(chan Resolution, 1)
	const occupiedToken PauseToken = 1
	e.mu.Lock()
	e.pending[occupiedToken] = occupied
	e.nextPauseToken = ^PauseToken(0)
	e.mu.Unlock()

	var issued PauseToken
	e.OnPause(func(token PauseToken, _ *capture.Flow, _ *capture.Message) {
		issued = token
		if !e.Resolve(token, Resolution{Decision: Forward}) {
			t.Fatal("newly issued token was rejected")
		}
	})
	out, drop := e.BeforeForward(inScopeFlow("api.github.com"), &capture.Message{})
	if out != nil || drop {
		t.Fatalf("resolved pause = {out:%q drop:%v}, want unchanged forward", out, drop)
	}
	if issued == 0 || issued == occupiedToken {
		t.Fatalf("wrapped allocator issued occupied token %d", issued)
	}

	if !e.Resolve(occupiedToken, Resolution{Decision: Drop}) {
		t.Fatal("wrapped allocation orphaned the occupied token")
	}
	select {
	case got := <-occupied:
		if got.Decision != Drop {
			t.Fatalf("occupied token resolution = %v, want drop", got.Decision)
		}
	default:
		t.Fatal("occupied token channel did not receive its resolution")
	}
}

func TestPauseCallbackPanicCleansItsPendingEntry(t *testing.T) {
	e := NewEngine()
	set, err := scope.Build(nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	e.SetScope(set)
	e.SetEnabled(true)

	var token PauseToken
	e.OnPause(func(got PauseToken, _ *capture.Flow, _ *capture.Message) {
		token = got
		panic("pause callback failed")
	})
	flow := inScopeFlow("api.github.com")
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		e.BeforeForward(flow, &capture.Message{})
	}()
	if recovered == nil {
		t.Fatal("pause callback panic did not propagate")
	}
	if token == 0 {
		t.Fatal("pause callback did not receive a token")
	}
	if e.Resolve(token, Resolution{Decision: Forward}) {
		t.Fatal("panicking callback left its token resolvable")
	}

	e.mu.Lock()
	pending := len(e.pending)
	paused := e.pausedByFlow[flow]
	e.mu.Unlock()
	if pending != 0 || paused != 0 {
		t.Fatalf("panic cleanup left pending=%d paused-count=%d", pending, paused)
	}
	if flow.Status != capture.StatusActive {
		t.Fatalf("flow status after callback panic = %s, want active", flow.Status)
	}
}

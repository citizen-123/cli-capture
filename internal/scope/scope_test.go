package scope

import "testing"

func mustCond(t *testing.T, spec string) Condition {
	t.Helper()
	c, err := ParseCondition(spec)
	if err != nil {
		t.Fatalf("ParseCondition(%q): %v", spec, err)
	}
	return c
}

// TestStrategies exercises every match strategy and field, inferred from spec.
func TestStrategies(t *testing.T) {
	tgt := Target{Host: "api.github.com", Method: "POST", Path: "/v1/users", Protocol: "http/1.1", Body: []byte("token=secret")}
	cases := []struct {
		spec string
		want bool
	}{
		{"github", true},               // substring on host
		{"gitlab", false},              // substring miss
		{"*.github.com", true},         // glob
		{"*.gitlab.com", false},        // glob miss
		{"host:~^api\\.", true},        // regex
		{"host:~^www\\.", false},       // regex miss
		{"host:=api.github.com", true}, // exact
		{"host:=github.com", false},    // exact miss (not substring)
		{"method:=POST", true},         // exact method
		{"method:GET", false},          // substring method miss
		{"path:/v1/*", true},           // glob path
		{"path:/v2/*", false},          // glob path miss
		{"proto:http/1.1", true},       // protocol substring
		{"body:secret", true},          // body substring
		{"any:secret", true},           // any-field search
		{"!github", false},             // negation of a match
		{"!gitlab", true},              // negation of a miss
	}
	for _, tc := range cases {
		c := mustCond(t, tc.spec)
		if got := c.Match(tgt); got != tc.want {
			t.Errorf("spec %q: Match = %v, want %v", tc.spec, got, tc.want)
		}
	}
}

// TestAllowlist: default Exclude, only included hosts are in scope.
func TestAllowlist(t *testing.T) {
	s, err := Build([]string{"*.github.com"}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if !s.InScope(Target{Host: "api.github.com"}) {
		t.Error("github should be in scope")
	}
	if s.InScope(Target{Host: "example.com"}) {
		t.Error("example.com should be out of scope under an allowlist")
	}
}

// TestDenylist: default Include, excluded hosts drop out.
func TestDenylist(t *testing.T) {
	s, err := Build(nil, []string{"*.telemetry.com", "path:/health"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !s.InScope(Target{Host: "api.example.com"}) {
		t.Error("arbitrary host should be in scope under a denylist")
	}
	if s.InScope(Target{Host: "x.telemetry.com"}) {
		t.Error("telemetry should be excluded")
	}
	if s.InScope(Target{Host: "api.example.com", Path: "/health"}) {
		t.Error("/health should be excluded by path rule")
	}
}

// TestExcludeWinsOverInclude: an allowlist with a carve-out.
func TestExcludeWinsOverInclude(t *testing.T) {
	// Include all of github, but exclude the noisy events endpoint.
	s, err := Build([]string{"*.github.com"}, []string{"path:/events"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !s.InScope(Target{Host: "api.github.com", Path: "/user"}) {
		t.Error("/user on github should be in scope")
	}
	if s.InScope(Target{Host: "api.github.com", Path: "/events"}) {
		t.Error("/events must be excluded even though host is included")
	}
}

// TestFirstVsLastMatch shows the ordering knob changes the verdict.
func TestFirstVsLastMatch(t *testing.T) {
	inc, _ := Rule1(Include, "*.github.com")
	exc, _ := Rule1(Exclude, "api.github.com")
	tgt := Target{Host: "api.github.com"}

	first := &Set{Default: Exclude, Rules: []*Rule{inc, exc}} // first-match-wins ⇒ include
	if !first.InScope(tgt) {
		t.Error("first-match-wins: include rule (listed first) should win")
	}
	last := &Set{Default: Exclude, Rules: []*Rule{inc, exc}, LastMatchWins: true} // last ⇒ exclude
	if last.InScope(tgt) {
		t.Error("last-match-wins: exclude rule (listed last) should win")
	}
}

// TestMITMTargetUsesHostOnly documents that TLS policy decisions rely on host
// (path/method are unknown before decryption) and still work.
func TestMITMTargetUsesHostOnly(t *testing.T) {
	// MITM everything except the bank.
	s, err := Build(nil, []string{"*.bank.example"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !s.InScope(Target{Host: "api.github.com", Protocol: "tls"}) {
		t.Error("non-bank host should be MITM'd")
	}
	if s.InScope(Target{Host: "login.bank.example", Protocol: "tls"}) {
		t.Error("bank host should be passed through")
	}
}

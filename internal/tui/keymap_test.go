package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestDefaultKeyMapMatchesTheStockBindings(t *testing.T) {
	km := DefaultKeyMap()
	cases := []struct {
		ctx, key string
		want     Action
	}{
		{ctxLeader, "w", ActPaneSwitch},
		{ctxLeader, "q", ActQuit},
		{ctxLeader, "<", ActSplitShrink},
		{ctxLeader, ",", ActSplitShrink},
		{ctxTraffic, "j", ActFlowNext},
		{ctxTraffic, "down", ActFlowNext},
		{ctxTraffic, " ", ActFlowFlag},
		{ctxTraffic, "enter", ActDetailOpen},
		{ctxTraffic, "N", ActInjectIn},
		{ctxDetail, "esc", ActDetailClose},
		{ctxEditor, "ctrl+l", ActEditorFixLen},
		{ctxRepeater, "tab", ActRepeaterCycle},
		{ctxTraffic, "Z", ""}, // unbound
	}
	for _, c := range cases {
		if got := km.Action(c.ctx, c.key); got != c.want {
			t.Errorf("Action(%q, %q) = %q, want %q", c.ctx, c.key, got, c.want)
		}
	}
	if km.Leader != tea.KeyCtrlA || km.LeaderName != "ctrl+a" {
		t.Errorf("default leader = %v/%q, want ctrl+a", km.Leader, km.LeaderName)
	}
}

func TestZeroKeyMapFallsBackToDefaults(t *testing.T) {
	// A zero-value Model (used throughout the tests) must still dispatch.
	if got := (KeyMap{}).Action(ctxTraffic, "j"); got != ActFlowNext {
		t.Errorf("zero KeyMap Action = %q, want %q", got, ActFlowNext)
	}
	if got := (Model{}).km().LeaderName; got != "ctrl+a" {
		t.Errorf("zero Model leader = %q, want ctrl+a", got)
	}
}

func TestOverridesRebindAndUnbind(t *testing.T) {
	km, err := NewKeyMap("ctrl+b", map[string]map[string]string{
		ctxTraffic: {
			"g":   string(ActFlowPrev), // add
			"j":   string(Unbind),      // remove a default
			"tab": string(ActDetailOpen),
		},
	})
	if err != nil {
		t.Fatalf("NewKeyMap: %v", err)
	}
	if got := km.Action(ctxTraffic, "g"); got != ActFlowPrev {
		t.Errorf("g = %q, want %q", got, ActFlowPrev)
	}
	if got := km.Action(ctxTraffic, "j"); got != "" {
		t.Errorf("j should be unbound, got %q", got)
	}
	// Unbinding one key of an action leaves its other keys alone.
	if got := km.Action(ctxTraffic, "down"); got != ActFlowNext {
		t.Errorf("down = %q, want %q", got, ActFlowNext)
	}
	if km.Leader != tea.KeyCtrlB || km.LeaderName != "ctrl+b" {
		t.Errorf("leader = %v/%q, want ctrl+b", km.Leader, km.LeaderName)
	}
}

func TestNewKeyMapRejectsBadConfig(t *testing.T) {
	cases := []struct {
		name      string
		leader    string
		overrides map[string]map[string]string
		wantIn    string
	}{
		{"unknown context", "", map[string]map[string]string{"trafic": {"j": "flow.next"}}, "unknown context"},
		{"unknown action", "", map[string]map[string]string{ctxTraffic: {"j": "flow.nxt"}}, "not available in this context"},
		// A real action, but in a context whose dispatch never handles it: the
		// traffic pane has no session.save, so binding it there can't fire.
		{"action wrong context", "", map[string]map[string]string{ctxTraffic: {"z": "session.save"}}, "not available in this context"},
		{"leader not a ctrl key", "x", nil, "must be a ctrl key"},
		{"leader is tab", "tab", nil, "must be a ctrl key"},
		// Binding the leader inside another context can never fire, because the
		// leader is consumed before the context is consulted.
		{"shadowed by leader", "ctrl+a", map[string]map[string]string{ctxTraffic: {"ctrl+a": "flow.next"}}, "never reaches"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := NewKeyMap(c.leader, c.overrides)
			if err == nil {
				t.Fatalf("NewKeyMap(%q, %v) succeeded, want an error", c.leader, c.overrides)
			}
			if !strings.Contains(err.Error(), c.wantIn) {
				t.Errorf("error %q should mention %q", err, c.wantIn)
			}
		})
	}
}

func TestEveryDefaultActionIsAKnownAction(t *testing.T) {
	known := map[string]bool{}
	for _, a := range knownActions() {
		known[a] = true
	}
	for ctx, binds := range defaults {
		for _, b := range binds {
			if !known[string(b.action)] {
				t.Errorf("%s: action %q is not in knownActions()", ctx, b.action)
			}
		}
	}
	// Each advertised action must be bindable in the context that dispatches it
	// (validation is per context, so it's only accepted where it can fire).
	for ctx, binds := range defaults {
		for _, b := range binds {
			if _, err := NewKeyMap("", map[string]map[string]string{ctx: {"Q": string(b.action)}}); err != nil {
				t.Errorf("%s: advertised action %q rejected in its own context: %v", ctx, b.action, err)
			}
		}
	}
}

func TestHelpFollowsTheKeymap(t *testing.T) {
	km, err := NewKeyMap("ctrl+b", map[string]map[string]string{
		ctxTraffic: {
			"g": string(ActFlowPrev),
			"k": string(Unbind),
			"x": string(Unbind), // the only key for resend
		},
	})
	if err != nil {
		t.Fatalf("NewKeyMap: %v", err)
	}
	out := (Model{}).WithKeys(km).helpView()

	if !strings.Contains(out, "ctrl+b") {
		t.Error("help should name the configured leader")
	}
	if strings.Contains(out, "ctrl+a") {
		t.Error("help should not mention the default leader once it is rebound")
	}
	if !strings.Contains(out, "g") {
		t.Error("help should list a newly bound key")
	}
	// An action with no keys left drops out of the key sections entirely. Only
	// those sections are keymap-derived: ':resend' stays listed below, because
	// the command line is reachable whether or not the action still has a key.
	keyHelp, _, _ := strings.Cut(out, vimHelpHeading)
	if strings.Contains(keyHelp, "resend the selected flow") {
		t.Error("fully unbound action should not appear in help")
	}
	// The space key needs a readable name or the row looks blank.
	if !strings.Contains(out, "space") {
		t.Error("help should render the space binding as \"space\"")
	}
}

func TestHelpCoversEveryBoundAction(t *testing.T) {
	// The old help was a second hand-written list and had drifted from the
	// dispatch; this asserts the two can't disagree again.
	out := (Model{}).helpView()
	for ctx, binds := range defaults {
		for _, b := range binds {
			if !strings.Contains(out, b.desc) {
				t.Errorf("%s: help is missing %q (%s)", ctx, b.desc, b.action)
			}
		}
	}
}

func TestKeysForIsStable(t *testing.T) {
	km := DefaultKeyMap()
	for range 20 {
		if got := km.keysFor(ctxTraffic, ActFlowPrev); len(got) != 2 || got[0] != "k" || got[1] != "up" {
			t.Fatalf("keysFor = %v, want [k up] every time", got)
		}
	}
}

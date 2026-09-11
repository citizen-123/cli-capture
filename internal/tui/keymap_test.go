package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestZeroKeyMapFallsBackToDefaults(t *testing.T) {
	// A zero-value Model (used throughout the tests) must still dispatch.
	if got := (KeyMap{}).Action(ctxTraffic, "j"); got != ActFlowNext {
		t.Errorf("zero KeyMap Action = %q, want %q", got, ActFlowNext)
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

// Ctrl+Space is the leader people reach for when a target app claims the ctrl
// letters. Terminals send NUL for it, which bubbletea names "ctrl+@", so the
// name a user types has to resolve to that key type.
func TestNewKeyMapAcceptsCtrlSpace(t *testing.T) {
	km, err := NewKeyMap("ctrl+space", nil)
	if err != nil {
		t.Fatalf("NewKeyMap(\"ctrl+space\", nil) = %v, want no error", err)
	}
	if km.Leader != tea.KeyCtrlAt {
		t.Errorf("leader key type = %v, want %v (NUL)", km.Leader, tea.KeyCtrlAt)
	}
	if km.LeaderName != "ctrl+space" {
		t.Errorf("leader name = %q, want %q — the name is what the help overlay shows", km.LeaderName, "ctrl+space")
	}
	out := strings.ToLower((Model{}).WithKeys(km).helpView())
	if !strings.Contains(out, "ctrl+space") || strings.Contains(out, "ctrl+@") {
		t.Errorf("help should preserve configured leader spelling ctrl+space; got:\n%s", out)
	}
}

func TestNewKeyMapRejectsBindingsShadowedByLeaderRuntimeKey(t *testing.T) {
	contextActions := map[string]Action{
		ctxLeader:   ActPaneSwitch,
		ctxTraffic:  ActFlowNext,
		ctxDetail:   ActDetailClose,
		ctxEditor:   ActEditorCancel,
		ctxRepeater: ActRepeaterClose,
	}
	for _, leader := range []string{"ctrl+space", "ctrl+@"} {
		for _, key := range []string{"ctrl+space", "ctrl+@"} {
			for ctx := range contextActions {
				name := leader + "/" + key + "/" + ctx
				t.Run(name, func(t *testing.T) {
					_, err := NewKeyMap(leader, map[string]map[string]string{
						ctx: {key: string(contextActions[ctx])},
					})
					if err == nil {
						t.Fatalf("NewKeyMap(%q, binding %q in %s) succeeded, want an error", leader, key, ctx)
					}
					for _, want := range []string{
						"keys: " + ctx,
						"key \"" + key + "\"",
						"leader \"" + leader + "\"",
					} {
						if !strings.Contains(err.Error(), want) {
							t.Errorf("error %q should mention %q", err, want)
						}
					}
				})
			}
		}
	}
}

func TestNewKeyMapAllowsBindingWithDifferentControlKeyType(t *testing.T) {
	km, err := NewKeyMap("ctrl+a", map[string]map[string]string{
		ctxLeader:  {"ctrl+b": string(ActPaneSwitch)},
		ctxTraffic: {"ctrl+b": string(ActFlowNext)},
	})
	if err != nil {
		t.Fatalf("NewKeyMap with leader ctrl+a and binding ctrl+b: %v", err)
	}
	if got := km.Action(ctxLeader, "ctrl+b"); got != ActPaneSwitch {
		t.Errorf("leader ctrl+b binding = %q, want %q", got, ActPaneSwitch)
	}
	if got := km.Action(ctxTraffic, "ctrl+b"); got != ActFlowNext {
		t.Errorf("traffic ctrl+b binding = %q, want %q", got, ActFlowNext)
	}
}

func TestNewKeyMapAllowsAltLeaderBinding(t *testing.T) {
	km, err := NewKeyMap("ctrl+a", map[string]map[string]string{
		ctxLeader:  {"alt+ctrl+a": string(ActPaneSwitch)},
		ctxTraffic: {"alt+ctrl+a": string(ActFlowNext)},
	})
	if err != nil {
		t.Fatalf("NewKeyMap with alt+ctrl+a bindings: %v", err)
	}
	if got := km.Action(ctxLeader, "alt+ctrl+a"); got != ActPaneSwitch {
		t.Errorf("leader alt+ctrl+a binding = %q, want %q", got, ActPaneSwitch)
	}
	if got := km.Action(ctxTraffic, "alt+ctrl+a"); got != ActFlowNext {
		t.Errorf("traffic alt+ctrl+a binding = %q, want %q", got, ActFlowNext)
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

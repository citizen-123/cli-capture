package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// Action names a thing the UI can do. Keys are bound to actions rather than to
// code, so a binding can move without touching the dispatch, and so the help
// overlay can be rendered from the same table the dispatcher reads — the two
// used to be written out separately and had already drifted apart.
type Action string

// Unbind removes a default binding: `"keys": {"traffic": {"x": "none"}}`.
const Unbind Action = "none"

const (
	ActPaneSwitch         Action = "pane.switch"
	ActSplitShrink        Action = "split.shrink"
	ActSplitGrow          Action = "split.grow"
	ActInterceptRequests  Action = "intercept.requests"
	ActInterceptResponses Action = "intercept.responses"
	ActSessionSave        Action = "session.save"
	ActExportHAR          Action = "export.har"
	ActExportFlagged      Action = "export.flagged"
	ActExportCurl         Action = "export.curl"
	ActHelpToggle         Action = "help.toggle"
	ActQuit               Action = "app.quit"

	ActFlowNext    Action = "flow.next"
	ActFlowPrev    Action = "flow.prev"
	ActFlowFlag    Action = "flow.flag"
	ActFlaggedOnly Action = "flow.flagged-only"
	ActSortCycle   Action = "flow.sort"
	ActFilterOpen  Action = "flow.filter"
	ActFlowResend  Action = "flow.resend"
	ActDetailOpen  Action = "flow.detail"
	ActRepeaterNew Action = "repeater.open"
	ActPausedEdit  Action = "paused.edit"
	ActPausedFwd   Action = "paused.forward"
	ActPausedDrop  Action = "paused.drop"
	ActInjectOut   Action = "ws.inject.client"
	ActInjectIn    Action = "ws.inject.server"

	ActDetailSave  Action = "detail.save"
	ActDetailClose Action = "detail.close"

	ActEditorSend   Action = "editor.send"
	ActEditorFixLen Action = "editor.fix-length"
	ActEditorCancel Action = "editor.cancel"

	ActRepeaterCycle Action = "repeater.cycle-focus"
	ActRepeaterMode  Action = "repeater.cycle-mode"
	ActRepeaterSend  Action = "repeater.send"
	ActRepeaterClose Action = "repeater.close"
)

// Key contexts. Which one is consulted depends on what's open, so the same key
// can mean different things in the list and in the editor.
const (
	ctxLeader   = "leader"
	ctxTraffic  = "traffic"
	ctxDetail   = "detail"
	ctxEditor   = "editor"
	ctxRepeater = "repeater"
)

type binding struct {
	action Action
	keys   []string
	desc   string
}

// contexts is the display order of the help overlay, and the set of context
// names a config file may use.
var contexts = []string{ctxLeader, ctxTraffic, ctxDetail, ctxEditor, ctxRepeater}

var contextTitle = map[string]string{
	ctxLeader:   "Global — press the leader, then:",
	ctxTraffic:  "Traffic pane (right)",
	ctxDetail:   "Detail view (enter)",
	ctxEditor:   "Editor (while intercepting)",
	ctxRepeater: "Repeater (R)",
}

// defaults are the stock bindings. The order here is the order the help
// overlay lists them in.
var defaults = map[string][]binding{
	ctxLeader: {
		{ActPaneSwitch, []string{"w"}, "switch pane (terminal ⇄ traffic)"},
		{ActSplitShrink, []string{"<", ","}, "resize split: shrink the left pane"},
		{ActSplitGrow, []string{">", "."}, "resize split: grow the left pane"},
		{ActInterceptRequests, []string{"i"}, "toggle intercept: requests"},
		{ActInterceptResponses, []string{"r"}, "toggle intercept: responses"},
		{ActSessionSave, []string{"s"}, "save session to JSON"},
		{ActExportHAR, []string{"h"}, "export session as HAR"},
		{ActExportFlagged, []string{"f"}, "export flagged flows → flagged.txt"},
		{ActHelpToggle, []string{"?"}, "toggle this help"},
		{ActQuit, []string{"q"}, "quit"},
	},
	ctxTraffic: {
		{ActFlowPrev, []string{"k", "up"}, "move selection up"},
		{ActFlowNext, []string{"j", "down"}, "move selection down"},
		{ActFlowFlag, []string{" "}, "flag / unflag the selected flow"},
		{ActFlaggedOnly, []string{"F"}, "show flagged only"},
		{ActSortCycle, []string{"o"}, "cycle sort: none / status / size"},
		{ActFilterOpen, []string{"/"}, "filter by host / method / path / status"},
		{ActDetailOpen, []string{"enter"}, "open detail view"},
		{ActInterceptRequests, []string{"i"}, "toggle intercept: requests"},
		{ActInterceptResponses, []string{"r"}, "toggle intercept: responses"},
		{ActPausedEdit, []string{"e"}, "PAUSED flow: edit"},
		{ActPausedFwd, []string{"f"}, "PAUSED flow: forward"},
		{ActPausedDrop, []string{"d"}, "PAUSED flow: drop"},
		{ActFlowResend, []string{"x"}, "resend the selected flow to its origin"},
		{ActRepeaterNew, []string{"R"}, "open in Repeater — edit, set {{variables}}, resend or attack"},
		{ActExportCurl, []string{"c"}, "export the selected flow as a curl command"},
		{ActInjectOut, []string{"n"}, "inject a WebSocket frame (client→server)"},
		{ActInjectIn, []string{"N"}, "inject a WebSocket frame (server→client)"},
		{ActHelpToggle, []string{"?"}, "toggle this help"},
	},
	ctxDetail: {
		{ActDetailSave, []string{"s"}, "save this flow to a .txt file"},
		{ActDetailClose, []string{"esc", "q"}, "back to the list"},
	},
	ctxEditor: {
		{ActEditorSend, []string{"ctrl+s"}, "forward / send the edited bytes"},
		{ActEditorFixLen, []string{"ctrl+l"}, "fix Content-Length to match the body"},
		{ActEditorCancel, []string{"esc"}, "cancel"},
		{ActQuit, []string{"ctrl+q"}, "quit"},
	},
	ctxRepeater: {
		{ActRepeaterCycle, []string{"tab"}, "cycle focus: request → payloads → response"},
		{ActRepeaterMode, []string{"ctrl+o"}, "cycle attack mode: single / sniper / battering-ram / pitchfork / cluster-bomb"},
		{ActRepeaterSend, []string{"ctrl+s"}, "send (single) or run the attack; results stream into the traffic list"},
		{ActRepeaterClose, []string{"esc"}, "close"},
		{ActQuit, []string{"ctrl+q"}, "quit"},
	},
}

// knownActions is every action a config file may name, derived from the
// defaults so the two can't disagree.
func knownActions() []string {
	seen := map[Action]bool{Unbind: true}
	for _, binds := range defaults {
		for _, b := range binds {
			seen[b.action] = true
		}
	}
	out := make([]string, 0, len(seen))
	for a := range seen {
		out = append(out, string(a))
	}
	sort.Strings(out)
	return out
}

// DefaultLeader is the tmux-style prefix: press it, then a command key. Using a
// leader means only this one key is taken from the target — everything else in
// the terminal pane passes through untouched.
const DefaultLeader = "ctrl+a"

// KeyMap resolves (context, key) to an action.
type KeyMap struct {
	Leader     tea.KeyType
	LeaderName string
	binds      map[string]map[string]Action
}

// leaderTypes maps the key names usable as a leader to their KeyType. Only
// control keys qualify: the leader has to be a key the target app is unlikely
// to need, and its literal byte (KeyType 1-26 is the ASCII control code) is
// what we send when it's pressed twice.
func leaderTypes() map[string]tea.KeyType {
	out := map[string]tea.KeyType{}
	for i := 1; i <= 26; i++ {
		kt := tea.KeyType(i)
		name := tea.Key{Type: kt}.String()
		if strings.HasPrefix(name, "ctrl+") {
			out[name] = kt
		}
	}
	return out
}

// DefaultKeyMap is the stock keymap.
func DefaultKeyMap() KeyMap {
	km, err := NewKeyMap("", nil)
	if err != nil {
		panic("tui: default keymap is invalid: " + err.Error()) // unreachable: built from defaults
	}
	return km
}

// NewKeyMap builds a keymap from the defaults plus user overrides. An empty
// leader keeps the default. Overrides are context → key → action, where the
// action "none" unbinds the key.
func NewKeyMap(leader string, overrides map[string]map[string]string) (KeyMap, error) {
	if leader == "" {
		leader = DefaultLeader
	}
	kt, ok := leaderTypes()[leader]
	if !ok {
		return KeyMap{}, fmt.Errorf("keys: leader %q must be a ctrl key (ctrl+a … ctrl+z, excluding ctrl+i and ctrl+m)", leader)
	}

	km := KeyMap{Leader: kt, LeaderName: leader, binds: map[string]map[string]Action{}}
	for ctx, binds := range defaults {
		km.binds[ctx] = map[string]Action{}
		for _, b := range binds {
			for _, key := range b.keys {
				km.binds[ctx][key] = b.action
			}
		}
	}

	valid := map[Action]bool{Unbind: true}
	for _, a := range knownActions() {
		valid[Action(a)] = true
	}

	for _, ctx := range sortedMapKeys(overrides) {
		if _, ok := km.binds[ctx]; !ok {
			return KeyMap{}, fmt.Errorf("keys: unknown context %q (have %v)", ctx, contexts)
		}
		binds := overrides[ctx]
		for _, key := range sortedMapKeys(binds) {
			action := Action(binds[key])
			if !valid[action] {
				return KeyMap{}, fmt.Errorf("keys: %s: unknown action %q (have %v)", ctx, action, knownActions())
			}
			// A key that the leader swallows would never reach this context,
			// so binding it there is a silent no-op — say so instead.
			if ctx != ctxLeader && key == leader {
				return KeyMap{}, fmt.Errorf("keys: %s: %q is the leader, so it never reaches this context", ctx, key)
			}
			if action == Unbind {
				delete(km.binds[ctx], key)
				continue
			}
			km.binds[ctx][key] = action
		}
	}
	return km, nil
}

func sortedMapKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Action returns the action bound to key in ctx, or "" if the key is unbound.
func (k KeyMap) Action(ctx, key string) Action {
	if k.binds == nil {
		return DefaultKeyMap().Action(ctx, key)
	}
	return k.binds[ctx][key]
}

// keysFor lists the keys bound to an action, defaults first (in their declared
// order) so the help overlay reads the way it always has, then any keys the
// user added, sorted.
func (k KeyMap) keysFor(ctx string, a Action) []string {
	bound := k.binds[ctx]
	if bound == nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, b := range defaults[ctx] {
		if b.action != a {
			continue
		}
		for _, key := range b.keys {
			if bound[key] == a && !seen[key] {
				out = append(out, key)
				seen[key] = true
			}
		}
	}
	var extra []string
	for key, act := range bound {
		if act == a && !seen[key] {
			extra = append(extra, key)
		}
	}
	sort.Strings(extra)
	return append(out, extra...)
}

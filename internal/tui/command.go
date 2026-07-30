package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/citizen-123/cli-capture/internal/capture"
)

// The ':' command line — a spike for #9 (explore a vim-style modal UI). It is a
// discoverable path to actions, some of which have no keybinding at all
// (:sort size, :export flagged). It deliberately reuses the existing model
// methods so a command and its key do the exact same thing. Only the traffic
// pane opens it, so ':' never reaches a target that might itself be vim.

// command is one ':' command: how it dispatches and how it documents itself.
// Keeping the handler and the help text on the same record is deliberate — the
// help overlay renders from this table, so a command cannot be added without
// documenting it, and the two cannot drift the way a parallel list would.
type command struct {
	name    string
	aliases []string
	args    string
	desc    string
	run     func(Model, string) (Model, tea.Cmd)
}

// commands is the ':' command table, in help-overlay order.
var commands = []command{
	{name: "filter", aliases: []string{"f"}, args: "<query>", desc: "set the flow filter; no argument clears it",
		run: func(m Model, arg string) (Model, tea.Cmd) {
			m.fi.SetValue(arg)
			// Match the '/' key exactly (see onFilterKey): a selection that the
			// new query pushes out of range goes to the FIRST match, not the
			// last, or ':filter foo' and '/foo' would land on different rows.
			if m.selected >= len(m.visible()) {
				m.selected = 0
			}
			if arg == "" {
				m.status = "filter cleared"
			} else {
				m.status = "filter: " + arg
			}
			return m, nil
		}},
	{name: "sort", args: "none|status|size", desc: "sort the list",
		run: func(m Model, arg string) (Model, tea.Cmd) {
			switch arg {
			case "none", "":
				m.sort = sortNone
			case "status":
				m.sort = sortStatus
			case "size":
				m.sort = sortSize
			default:
				m.status = "sort: want none | status | size"
				return m, nil
			}
			m.selected = clampIndex(m.selected, len(m.visible()))
			m.status = "sort: " + m.sort.String()
			return m, nil
		}},
	{name: "export", args: "har|flagged", desc: "WRITE flows to disk: capture.har or flagged.txt",
		run: func(m Model, arg string) (Model, tea.Cmd) {
			switch arg {
			case "har":
				m.status = m.exportHAR()
			case "flagged":
				m.status = m.exportFlagged()
			default:
				m.status = "export: want har | flagged"
			}
			return m, nil
		}},
	{name: "har", desc: "export every flow as HAR (same as :export har)",
		run: func(m Model, _ string) (Model, tea.Cmd) { m.status = m.exportHAR(); return m, nil }},
	// ':flagged' reads as the 'F' view toggle, not as a file write, so that is
	// what it does. Writing flagged bodies — credentials and all — to disk is
	// spelled out in full as ':export flagged'; it is too destructive to sit
	// behind the more guessable of two near-identical names.
	{name: "flagged", aliases: []string{"only"}, desc: "show only flagged flows (same as F)",
		run: func(m Model, _ string) (Model, tea.Cmd) {
			m.flaggedOnly = !m.flaggedOnly
			m.selected = clampIndex(m.selected, len(m.visible()))
			if m.flaggedOnly {
				m.status = "showing flagged only"
			} else {
				m.status = "showing all flows"
			}
			return m, nil
		}},
	{name: "curl", aliases: []string{"y"}, desc: "write the selected flow to a .curl file",
		run: func(m Model, _ string) (Model, tea.Cmd) { m.status = m.exportCurlSelected(); return m, nil }},
	{name: "resend", aliases: []string{"x"}, desc: "resend the selected flow",
		run: func(m Model, _ string) (Model, tea.Cmd) { m.status = m.resendSelected(); return m, nil }},
	{name: "flag", desc: "toggle the flag on the selected flow",
		run: func(m Model, _ string) (Model, tea.Cmd) {
			m.status = m.toggleSelectedFlag()
			return m, nil
		}},
	{name: "w", aliases: []string{"write", "save"}, desc: "save the session",
		run: func(m Model, _ string) (Model, tea.Cmd) { m.status = m.saveSession(); return m, nil }},
	{name: "help", aliases: []string{"h"}, desc: "this overlay",
		run: func(m Model, _ string) (Model, tea.Cmd) { m.showHelp, m.helpScroll = true, 0; return m, nil }},
	{name: "q", aliases: []string{"quit"}, desc: "quit",
		run: func(m Model, _ string) (Model, tea.Cmd) { return m, tea.Quit }},
}

// lookupCommand resolves a typed name against the table's names and aliases.
func lookupCommand(name string) (command, bool) {
	for _, c := range commands {
		if c.name == name {
			return c, true
		}
		for _, a := range c.aliases {
			if a == name {
				return c, true
			}
		}
	}
	return command{}, false
}

// newCommandLine builds the single-line ':' input. The placeholder stays short
// and ASCII on purpose: the pane truncates it to the pane width, and a long
// styled placeholder gets cut mid-escape, bleeding its colour down the list.
// The full list lives in the help overlay, which is where :help points.
func newCommandLine() textinput.Model {
	ti := textinput.New()
	ti.Prompt = ":"
	ti.Placeholder = "command - :help for the list"
	return ti
}

// onCmdKey edits the ':' command line. Enter runs it, Esc cancels.
func (m Model) onCmdKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.Type {
	case tea.KeyCtrlQ:
		// Every other modal honours ctrl+q; a text input that swallowed it would
		// be the one place in the app where the quit key does nothing.
		return m, tea.Quit
	case tea.KeyEnter:
		line := strings.TrimSpace(m.ci.Value())
		m.cmdline = false
		m.ci.Blur()
		m.ci.SetValue("")
		return m.runCommand(line)
	case tea.KeyEsc:
		m.cmdline = false
		m.ci.Blur()
		m.ci.SetValue("")
		return m, nil
	}
	var cmd tea.Cmd
	m.ci, cmd = m.ci.Update(k)
	return m, cmd
}

// runCommand dispatches one ':' command through the table above. Names mirror
// the keybindings and add a few that have no key. An unknown command reports
// rather than silently doing nothing.
func (m Model) runCommand(line string) (tea.Model, tea.Cmd) {
	if line == "" {
		return m, nil
	}
	name, arg, _ := strings.Cut(line, " ")
	arg = strings.TrimSpace(arg)

	c, ok := lookupCommand(name)
	if !ok {
		m.status = "unknown command: " + name + " (try :help)"
		return m, nil
	}
	return c.run(m, arg)
}

// hostJump moves across host boundaries in the flow list the way } and { move
// across paragraphs in vim: a run of consecutive flows sharing a host is the
// "paragraph". Forward lands on the first flow of the next host; backward lands
// on the first flow of the current host, or of the previous one when the cursor
// is already there. Neither direction wraps — running out of list stops the
// motion, so a held } settles on the last host instead of cycling.
func hostJump(vis []*capture.Flow, from, dir, count int) int {
	if len(vis) == 0 {
		return 0
	}
	i := clampIndex(from, len(vis))
	for ; count > 0; count-- {
		j := i
		if dir > 0 {
			for j < len(vis)-1 && vis[j+1].ServerAddr == vis[i].ServerAddr {
				j++
			}
			if j < len(vis)-1 {
				j++ // first flow of the next host
			}
		} else {
			for j > 0 && vis[j-1].ServerAddr == vis[i].ServerAddr {
				j-- // first flow of the current host
			}
			if j == i && j > 0 { // already there, so step into the previous host
				j--
				for j > 0 && vis[j-1].ServerAddr == vis[j].ServerAddr {
					j--
				}
			}
		}
		if j == i {
			break // hit the end of the list
		}
		i = j
	}
	return i
}

// clearMotion discards a half-typed motion — a numeric prefix or a lone 'g'
// waiting for its pair. Anything that takes focus away from the traffic list
// calls this, so a stale count can never attach itself to a later keystroke.
func (m Model) clearMotion() Model {
	m.count, m.pendingG = 0, false
	return m
}

// digit reports the value of a single 0-9 keystroke, for the vim-style numeric
// prefix in the traffic list.
func digit(s string) (int, bool) {
	if len(s) == 1 && s[0] >= '0' && s[0] <= '9' {
		return int(s[0] - '0'), true
	}
	return 0, false
}

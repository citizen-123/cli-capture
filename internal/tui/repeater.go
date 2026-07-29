package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/citizen-123/cli-capture/internal/capture"
	"github.com/citizen-123/cli-capture/internal/repeater"
)

// repeaterFocus enumerates the three focusable areas of the Repeater modal.
const (
	repFocusReq = iota
	repFocusPayload
	repFocusResp
)

// repeaterState holds the Repeater modal: an editable raw request (with
// {{markers}}), a payloads editor, the selected attack mode, and the last
// response (shown inline so you don't have to leave to see the result).
type repeaterState struct {
	base    *repeater.Template // origin scheme/host/secure to send against
	req     textarea.Model     // raw request, editable, may contain {{markers}}
	payload textarea.Model     // "name = a, b, c" lines
	respVP  viewport.Model     // last response, scrollable
	resp    *capture.Flow      // last sent flow
	mode    repeater.AttackMode
	focus   int // repFocusReq | repFocusPayload | repFocusResp
	result  string
}

// --- messages ---

type repeaterResultMsg struct {
	flow *capture.Flow
	err  error
}
type attackDoneMsg struct{ count int }

// openRepeater loads the selected flow into the Repeater as an editable template.
func (m *Model) openRepeater() {
	f := m.selectedFlow()
	if f == nil {
		return
	}
	tmpl, err := repeater.FromFlow(f)
	if err != nil {
		m.status = "repeater: " + err.Error()
		return
	}
	req := newEditor()
	req.SetValue(tmpl.Raw())
	req.Focus()
	pay := newEditor()
	pay.SetValue(prefillPayloads(tmpl.Variables()))
	pay.Blur()

	m.rep = repeaterState{base: tmpl, req: req, payload: pay, respVP: viewport.New(0, 0), mode: repeater.Single, focus: repFocusReq}
	m.sizeRepeater()
	m.repeating = true
	m.status = "Repeater: Tab switch · Ctrl+O mode · Ctrl+S send · Esc close"
}

// onRepeaterKey drives the modal. Ctrl+S runs it, Ctrl+O cycles the attack mode,
// Tab switches editors, Esc closes; other keys type into the focused editor.
func (m Model) onRepeaterKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.km().Action(ctxRepeater, k.String()) {
	case ActQuit:
		return m, tea.Quit
	case ActRepeaterClose:
		m.repeating = false
		return m, nil
	case ActRepeaterCycle:
		m.rep.focus = (m.rep.focus + 1) % 3
		m.rep.req.Blur()
		m.rep.payload.Blur()
		switch m.rep.focus {
		case repFocusReq:
			m.rep.req.Focus()
		case repFocusPayload:
			m.rep.payload.Focus()
		}
		return m, nil
	case ActRepeaterMode:
		m.rep.mode = nextMode(m.rep.mode)
		return m, nil
	case ActRepeaterSend:
		tmpl, err := repeater.ParseRaw(m.rep.req.Value(), m.rep.base)
		if err != nil {
			m.rep.result = "parse error: " + err.Error()
			return m, nil
		}
		m.rep.result = "sending…"
		return m, m.runRepeater(tmpl)
	}

	var cmd tea.Cmd
	switch m.rep.focus {
	case repFocusReq:
		m.rep.req, cmd = m.rep.req.Update(k)
	case repFocusPayload:
		m.rep.payload, cmd = m.rep.payload.Update(k)
	case repFocusResp:
		m.rep.respVP, cmd = m.rep.respVP.Update(k)
	}
	return m, cmd
}

// runRepeater returns a command that sends the request(s). Single mode sends
// once; attack modes expand the payload lists via repeater.Attack.Jobs and send
// each. Every result is added to the store, so they stream into the traffic list
// (which doubles as the results table). Runs off the UI goroutine so a big
// attack doesn't freeze the interface.
func (m Model) runRepeater(tmpl *repeater.Template) tea.Cmd {
	payloads := repeater.ParsePayloads(m.rep.payload.Value())
	store := m.store

	if m.rep.mode == repeater.Single {
		vars := map[string]string{}
		for name, list := range payloads {
			if len(list) > 0 {
				vars[name] = list[0]
			}
		}
		return func() tea.Msg {
			flow, err := repeater.Send(tmpl, vars)
			if flow != nil {
				store.Add(flow)
			}
			return repeaterResultMsg{flow: flow, err: err}
		}
	}

	// Attack: only variables that have a payload list are insertion points; the
	// rest keep their (first) value as a baseline.
	var positions []string
	var lists [][]string
	base := map[string]string{}
	for _, v := range tmpl.Variables() {
		list := payloads[v]
		if len(list) == 0 || (len(list) == 1 && list[0] == "") {
			continue
		}
		positions = append(positions, v)
		lists = append(lists, list)
		base[v] = list[0]
	}
	jobs := repeater.Attack{Mode: m.rep.mode, Positions: positions, Lists: lists, Base: base}.Jobs()
	return func() tea.Msg {
		n := 0
		for _, job := range jobs {
			if flow, _ := repeater.Send(tmpl, job); flow != nil {
				if flow.Request != nil {
					flow.Request.Meta["payload"] = jobLabel(job, positions)
				}
				store.Add(flow)
				n++
			}
		}
		return attackDoneMsg{count: n}
	}
}

// jobLabel renders the fuzzed positions of an attack job as "name=value, …" so
// the traffic-list row for each result shows which payload produced it.
func jobLabel(job map[string]string, positions []string) string {
	parts := make([]string, 0, len(positions))
	for _, p := range positions {
		parts = append(parts, p+"="+job[p])
	}
	return strings.Join(parts, ", ")
}

func (m *Model) sizeRepeater() {
	w := m.width - 4
	if w < 10 {
		w = 10
	}
	// Fixed chrome: title + 3 section labels + footer ≈ 5 lines.
	avail := m.height - 6
	if avail < 9 {
		avail = 9
	}
	reqH := avail * 45 / 100
	payH := avail * 20 / 100
	if payH < 2 {
		payH = 2
	}
	respH := avail - reqH - payH
	if respH < 3 {
		respH = 3
	}
	m.rep.req.SetWidth(w)
	m.rep.req.SetHeight(reqH)
	m.rep.payload.SetWidth(w)
	m.rep.payload.SetHeight(payH)
	m.rep.respVP.Width = w
	m.rep.respVP.Height = respH
	if m.rep.resp != nil {
		m.rep.respVP.SetContent(renderRepeaterResponse(m.rep.resp, w))
	}
}

func (m Model) repeaterView() string {
	title := "Repeater " + glyphArrow + " " + m.rep.base.Method + " " + m.rep.base.URL
	reqLabel := sectionStyle.Render("Request") + focusMark(m.rep.focus == repFocusReq)
	payLabel := sectionStyle.Render("Payloads") +
		dimStyle.Render("  ["+m.rep.mode.String()+"]  name = a, b, c") + focusMark(m.rep.focus == repFocusPayload)
	respLabel := sectionStyle.Render("Response") + focusMark(m.rep.focus == repFocusResp)

	footer := dimStyle.Render("Tab switch · Ctrl+O mode · Ctrl+S send · Esc close")
	if m.rep.result != "" {
		footer = pendingStyle.Render(m.rep.result) + "   " + footer
	}

	return lipgloss.JoinVertical(lipgloss.Left,
		titleStyle.Render(truncate(title, m.width-1)),
		reqLabel,
		m.rep.req.View(),
		payLabel,
		m.rep.payload.View(),
		respLabel,
		m.rep.respVP.View(),
		footer,
	)
}

// renderRepeaterResponse renders the last response (status, headers, decoded &
// colored body) for the inline response pane.
func renderRepeaterResponse(f *capture.Flow, width int) string {
	if f == nil || f.Response == nil {
		return dimStyle.Render("(no response yet — Ctrl+S to send)")
	}
	body := codeStyle(f).Render(f.Response.Summary) + "\n\n" + renderMessage(f.Response, width)
	return ansi.Hardwrap(body, width, false)
}

func focusMark(active bool) string {
	if active {
		return keyCapStyle.Render(" ◀")
	}
	return ""
}

func nextMode(cur repeater.AttackMode) repeater.AttackMode {
	for i, mode := range repeater.Modes {
		if mode == cur {
			return repeater.Modes[(i+1)%len(repeater.Modes)]
		}
	}
	return repeater.Single
}

func prefillPayloads(vars []string) string {
	if len(vars) == 0 {
		return "# no {{variables}} in this request — add some, e.g. {{id}}, then\n# list payloads here:  id = 1, 2, 3\n"
	}
	var b strings.Builder
	b.WriteString("# one line per variable; comma-separated payloads for attacks\n")
	for _, v := range vars {
		fmt.Fprintf(&b, "%s = \n", v)
	}
	return b.String()
}

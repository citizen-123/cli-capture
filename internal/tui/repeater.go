package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/net/http/httpguts"

	"github.com/citizen-123/cli-capture/internal/capture"
	"github.com/citizen-123/cli-capture/internal/repeater"
)

const (
	repFocusReq = iota
	repFocusPayload
	repFocusResp
)

// The base supplies transport/body reconstruction metadata. The cached preview
// and attack describe exactly the current draft, not the captured request.
type repeaterState struct {
	sessionID  uint64
	jobID      uint64
	base       *repeater.Template
	editBuffer messageEditBuffer
	req        textarea.Model
	payload    textarea.Model
	respVP     viewport.Model
	resp       *capture.Flow
	mode       repeater.AttackMode
	focus      int
	result     string
	preview    *repeater.Template
	attack     repeater.Attack
	planned    int
	previewErr error
}

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
	buffer := newMessageEditBuffer([]byte(tmpl.Raw()), true)
	req := newEditor()
	req.SetValue(buffer.Text())
	req.Focus()
	pay := newEditor()
	pay.SetValue(prefillPayloads(tmpl.Variables()))
	pay.Blur()
	m.nextRepeaterID++
	m.rep = repeaterState{
		sessionID: m.nextRepeaterID, base: tmpl, editBuffer: buffer,
		req: req, payload: pay, respVP: viewport.New(0, 0), mode: repeater.Single, focus: repFocusReq,
	}
	m.refreshRepeater()
	m.repeating = true
	m.sizeRepeater()
	m.status = "Repeater"
}

func (m Model) onRepeaterKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.km().Action(ctxRepeater, k.String()) {
	case ActQuit:
		m.cancelJob()
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
		m.refreshRepeater()
		return m, nil
	case ActEditorReplaceBody:
		if m.rep.focus == repFocusReq {
			text, err := m.rep.editBuffer.ReplaceBody(m.rep.req.Value())
			if err != nil {
				m.rep.result = err.Error()
			} else {
				m.rep.req.SetValue(text)
				m.rep.result = "body replacement enabled"
				m.refreshRepeater()
			}
		}
		return m, nil
	case ActRepeaterSend:
		cmd := m.runRepeater()
		return m, cmd
	}

	var cmd tea.Cmd
	switch m.rep.focus {
	case repFocusReq:
		before := m.rep.req.Value()
		m.rep.req, cmd = m.rep.req.Update(k)
		if m.rep.req.Value() != before {
			m.refreshRepeater()
		}
	case repFocusPayload:
		before := m.rep.payload.Value()
		m.rep.payload, cmd = m.rep.payload.Update(k)
		if m.rep.payload.Value() != before {
			m.refreshRepeater()
		}
	case repFocusResp:
		m.rep.respVP, cmd = m.rep.respVP.Update(k)
	}
	return m, cmd
}

func (m *Model) refreshRepeater() {
	m.rep.preview = nil
	m.rep.planned = 0
	raw, err := m.rep.editBuffer.Bytes(m.rep.req.Value())
	if err != nil {
		m.rep.previewErr = err
		return
	}
	tmpl, err := repeater.ParseRaw(string(raw), m.rep.base)
	if err != nil {
		m.rep.previewErr = err
		return
	}
	attack := planRepeaterAttack(tmpl, m.rep.mode, repeater.ParsePayloads(m.rep.payload.Value()))
	count, err := attack.Count()
	if err == nil {
		req, renderErr := tmpl.Render(attack.Base)
		err = renderErr
		if err == nil && (req.URL.Host == "" || (req.URL.Scheme != "http" && req.URL.Scheme != "https")) {
			err = fmt.Errorf("request needs an HTTP or HTTPS target")
		}
		if err == nil {
			for name, values := range req.Header {
				if !httpguts.ValidHeaderFieldName(name) {
					err = fmt.Errorf("invalid request header name %q", name)
					break
				}
				for _, value := range values {
					if !httpguts.ValidHeaderFieldValue(value) {
						err = fmt.Errorf("invalid value for request header %q", name)
						break
					}
				}
				if err != nil {
					break
				}
			}
		}
	}
	m.rep.preview, m.rep.attack, m.rep.planned, m.rep.previewErr = tmpl, attack, count, err
}

// Payload selection is shared by the preview and the worker. Single mode uses
// the first supplied value, while attack insertion points exclude empty lists.
func planRepeaterAttack(tmpl *repeater.Template, mode repeater.AttackMode, payloads map[string][]string) repeater.Attack {
	attack := repeater.Attack{Mode: mode, Base: make(map[string]string)}
	if mode == repeater.Single {
		for name, list := range payloads {
			if len(list) > 0 {
				attack.Base[name] = list[0]
			}
		}
		for _, name := range tmpl.Variables() {
			if len(payloads[name]) > 0 {
				attack.Positions = append(attack.Positions, name)
			}
		}
		return attack
	}
	for _, name := range tmpl.Variables() {
		list := payloads[name]
		if len(list) == 0 || (len(list) == 1 && list[0] == "") {
			continue
		}
		attack.Positions = append(attack.Positions, name)
		attack.Lists = append(attack.Lists, list)
		attack.Base[name] = list[0]
	}
	return attack
}

func (m *Model) runRepeater() tea.Cmd {
	if m.job.active() {
		m.status = "manual job already active"
		return nil
	}
	if m.rep.previewErr != nil {
		m.rep.result = "cannot send: " + m.rep.previewErr.Error()
		return nil
	}
	if m.rep.preview == nil {
		m.rep.result = "cannot send: no request template"
		return nil
	}
	// refreshRepeater builds fresh owned templates/maps; later edits replace
	// these values instead of mutating anything held by the worker.
	tmpl := m.rep.preview
	kind := m.rep.mode.String()
	if m.rep.mode == repeater.Single {
		kind = "repeater"
	}
	return m.startRequestJob(kind, m.rep.sessionID, m.rep.attack, m.rep.planned,
		func(ctx context.Context, vars map[string]string) (*capture.Flow, error) {
			return repeater.Send(ctx, tmpl, vars)
		})
}

func jobLabel(job map[string]string, positions []string) string {
	parts := make([]string, 0, len(positions))
	for _, p := range positions {
		parts = append(parts, p+"="+job[p])
	}
	return strings.Join(parts, ", ")
}

func (m *Model) sizeRepeater() {
	if m.rep.base == nil {
		return
	}
	w := max(1, m.width)
	// Title, three labels, bounded result, and footer are six single rows.
	avail := max(3, m.height-m.chromeHeight()-6)
	reqH := max(1, avail*45/100)
	payH := max(1, avail*20/100)
	respH := max(1, avail-reqH-payH)
	m.rep.req.SetWidth(w)
	m.rep.req.SetHeight(reqH)
	m.rep.payload.SetWidth(w)
	m.rep.payload.SetHeight(payH)
	m.rep.respVP.Width = w
	m.rep.respVP.Height = respH
	if m.rep.resp != nil {
		offset, atBottom := m.rep.respVP.YOffset, m.rep.respVP.AtBottom()
		m.rep.respVP.SetContent(renderRepeaterResponse(m.rep.resp, w))
		if atBottom {
			m.rep.respVP.GotoBottom()
		} else {
			m.rep.respVP.SetYOffset(offset)
		}
	}
}

func (m Model) repeaterView() string {
	w := max(1, m.width)
	height := max(0, m.height-m.chromeHeight())
	if height < 9 || w < 30 {
		return fitRepeaterRows("Repeater: enlarge terminal to edit", w, height) + "\n" + m.globalChrome()
	}
	title := "Repeater"
	if m.rep.preview != nil {
		title += " " + glyphArrow + " " + m.rep.preview.Method + " " + m.rep.preview.URL
	} else {
		title += " — invalid request"
	}
	requestLabel := "Request"
	if notice := m.rep.editBuffer.Notice(); notice != "" {
		requestLabel += " · " + notice
		if key := m.keyHint(ctxRepeater, ActEditorReplaceBody); key != "" {
			requestLabel += " · " + key + " replace body"
		}
	}
	planned := fmt.Sprintf("%d planned", m.rep.planned)
	if m.rep.previewErr != nil {
		planned = "send disabled"
	}
	payloadLabel := "Payloads [" + m.rep.mode.String() + "] · " + planned
	result := m.rep.result
	if m.rep.previewErr != nil {
		result = "preview: " + m.rep.previewErr.Error()
	}
	var hints []string
	for _, hint := range []struct {
		action Action
		label  string
	}{
		{ActRepeaterSend, "send"}, {ActRepeaterClose, "close"},
		{ActRepeaterCycle, "focus"}, {ActRepeaterMode, "mode"},
	} {
		if key := m.keyHint(ctxRepeater, hint.action); key != "" {
			hints = append(hints, key+" "+hint.label)
		}
	}
	body := lipgloss.JoinVertical(lipgloss.Left,
		titleStyle.Render(repeaterLine(title, w)),
		repeaterLine(sectionStyle.Render(requestLabel)+focusMark(m.rep.focus == repFocusReq), w),
		m.rep.req.View(),
		repeaterLine(sectionStyle.Render(payloadLabel)+focusMark(m.rep.focus == repFocusPayload), w),
		m.rep.payload.View(),
		repeaterLine(sectionStyle.Render("Response")+focusMark(m.rep.focus == repFocusResp), w),
		m.rep.respVP.View(),
		pendingStyle.Render(repeaterLine(result, w)),
		dimStyle.Render(repeaterLine(strings.Join(hints, " · "), w)),
	)
	return fitRepeaterRows(body, w, height) + "\n" + m.globalChrome()
}

func repeaterLine(text string, width int) string {
	return ansi.Truncate(strings.NewReplacer("\r", " ", "\n", " ", "\t", " ").Replace(text), max(1, width), "…")
}

func fitRepeaterRows(text string, width, height int) string {
	if height <= 0 {
		return ""
	}
	lines := strings.Split(text, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], max(1, width), "…")
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func renderRepeaterResponse(f *capture.Flow, width int) string {
	if f == nil {
		return ""
	}
	if f.Err != nil {
		return ansi.Hardwrap("error: "+f.Err.Error(), max(1, width), false)
	}
	if f.Response == nil {
		return dimStyle.Render("(no response)")
	}
	body := codeStyle(f).Render(f.Response.Summary) + "\n\n" + renderMessage(f.Response, max(1, width), true)
	return ansi.Hardwrap(body, max(1, width), false)
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

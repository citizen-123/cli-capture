package tui

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/x/ansi"

	"github.com/citizen-123/cli-capture/internal/capture"
	"github.com/citizen-123/cli-capture/internal/intercept"
)

// Foreground snapshots contain input state, never capture, pause, or job state.
// Inspection can replace a textarea without sharing its backing storage with
// the suspended draft. Help and pane switches only suspend dispatch.
type foregroundState struct {
	focus                               focus
	modalSuspended, helpReturnSuspended bool
	suspendedFocus                      focus
	showHelp                            bool
	helpScroll                          int
	editing, injecting                  bool
	injectID                            string
	injectDir                           capture.Direction
	ta                                  textarea.Model
	editBuffer                          messageEditBuffer
	viewing                             bool
	viewFlow                            *capture.Flow
	vp                                  viewport.Model
	detailExpanded                      bool
	detailContent                       string
	filtering, cmdline                  bool
	fi, ci                              textinput.Model
	repeating                           bool
	rep                                 repeaterState
	selected                            int
	count                               int
	pendingG                            bool
}

type pausedEditorDraft struct {
	token  intercept.PauseToken
	ta     textarea.Model
	buffer messageEditBuffer
}

func (m Model) keyboardOwner() string {
	if m.modalSuspended {
		return "terminal"
	}
	switch {
	case m.showHelp:
		return "help"
	case m.repeating:
		return "repeater"
	case m.editing:
		if m.injecting {
			return "inject"
		}
		return "editor"
	case m.filtering:
		return "filter"
	case m.cmdline:
		return "command"
	case m.viewing:
		return "detail"
	case m.focus == focusTerminal:
		return "terminal"
	default:
		return "traffic"
	}
}

func (m *Model) switchPane() {
	if m.modalSuspended {
		m.modalSuspended = false
		m.focus = m.suspendedFocus
		return
	}
	owner := m.keyboardOwner()
	if owner != "terminal" && owner != "traffic" {
		m.suspendedFocus = m.focus
		m.modalSuspended = true
		m.focus = focusTerminal
		return
	}
	if m.focus == focusTerminal {
		m.focus = focusTraffic
	} else {
		m.focus = focusTerminal
	}
}

func (m *Model) toggleHelp() {
	if m.showHelp && !m.modalSuspended {
		m.closeHelp()
		return
	}
	m.helpReturnSuspended = m.modalSuspended
	m.modalSuspended = false
	m.showHelp = true
	m.helpScroll = 0
}

func (m *Model) closeHelp() {
	m.showHelp = false
	m.modalSuspended = m.helpReturnSuspended
	m.helpReturnSuspended = false
}

func (m *Model) inspectPause() {
	if m.pauseReturn != nil {
		m.restoreForeground()
		return
	}
	if m.paused == nil {
		m.status = "No held message"
		return
	}
	if m.editing && !m.injecting {
		m.modalSuspended = false
		return
	}
	m.pauseReturn = &foregroundState{
		focus: m.focus, modalSuspended: m.modalSuspended,
		helpReturnSuspended: m.helpReturnSuspended, suspendedFocus: m.suspendedFocus,
		showHelp: m.showHelp, helpScroll: m.helpScroll,
		editing: m.editing, injecting: m.injecting, injectID: m.injectID, injectDir: m.injectDir,
		ta: m.ta, editBuffer: m.editBuffer,
		viewing: m.viewing, viewFlow: m.viewFlow, vp: m.vp,
		detailExpanded: m.detailExpanded, detailContent: m.detailContent,
		filtering: m.filtering, cmdline: m.cmdline, fi: m.fi, ci: m.ci,
		repeating: m.repeating, rep: m.rep, selected: m.selected, count: m.count, pendingG: m.pendingG,
	}
	m.modalSuspended, m.showHelp = false, false
	m.editing, m.injecting, m.viewing = false, false, false
	m.filtering, m.cmdline, m.repeating = false, false, false
	m.ta, m.fi, m.ci = newEditor(), newFilter(), newCommandLine()
	m.vp = viewport.New(1, 1)
	m.viewFlow, m.detailContent = nil, ""
	m.editBuffer = messageEditBuffer{}
	m.focus = focusTraffic
	m.count, m.pendingG = 0, false
	for i, f := range m.visible() {
		if f.ID == m.paused.ID {
			m.selected = i
			break
		}
	}
	m.status = "Inspecting held message; Esc returns"
}

func (m *Model) restoreForeground() {
	s := m.pauseReturn
	if s == nil {
		return
	}
	if m.editing && !m.injecting && m.paused != nil {
		m.pauseEditDraft = &pausedEditorDraft{m.pausedToken, m.ta, m.editBuffer}
	}
	m.focus, m.modalSuspended = s.focus, s.modalSuspended
	m.helpReturnSuspended, m.suspendedFocus = s.helpReturnSuspended, s.suspendedFocus
	m.showHelp, m.helpScroll = s.showHelp, s.helpScroll
	m.editing, m.injecting = s.editing, s.injecting
	m.injectID, m.injectDir, m.ta, m.editBuffer = s.injectID, s.injectDir, s.ta, s.editBuffer
	m.viewing, m.viewFlow, m.vp = s.viewing, s.viewFlow, s.vp
	m.detailExpanded, m.detailContent = s.detailExpanded, s.detailContent
	m.filtering, m.cmdline, m.fi, m.ci = s.filtering, s.cmdline, s.fi, s.ci
	m.repeating = s.repeating
	if m.rep.sessionID != s.rep.sessionID {
		m.rep = s.rep
	}
	m.selected, m.count, m.pendingG = s.selected, s.count, s.pendingG
	m.pauseReturn = nil
	m.sizeOverlays()
}

func (m Model) chromeHeight() int {
	h := 1
	if m.paused != nil {
		h++
	}
	if m.job != nil {
		h++
	}
	return h
}

func (m Model) paneContentHeight() int {
	return max(1, m.height-m.chromeHeight()-2)
}

func (m Model) globalChrome() string {
	msg := "focus:" + m.keyboardOwner() + " · "
	if m.pendingLeader {
		msg += displayKey(m.km().LeaderName) + " pending · Esc cancel"
	} else if m.status != "" {
		msg += oneLine(m.status)
	} else {
		msg += m.leaderHint(ActPaneSwitch) + " pane"
	}
	var reqOn, respOn bool
	if m.engine != nil {
		reqOn, respOn = m.engine.Enabled(), m.engine.InterceptResponses()
	}
	help := m.leaderHint(ActHelpToggle) + " help"
	mode := "monitor"
	if reqOn || respOn {
		mode = "req:" + onOff(reqOn) + " resp:" + onOff(respOn)
	}
	available := max(1, m.width-ansi.StringWidth(mode)-3)
	msg = truncate(msg, max(1, available-ansi.StringWidth(help)-3)) + " · " + help
	lines := []string{statusBar(m.width, msg, reqOn, respOn)}
	if m.paused != nil {
		lines = append(lines, truncate(pendingStyle.Render(m.pauseSummary()), m.width))
	}
	if m.job != nil {
		lines = append(lines, truncate(oneLine(m.jobStatus()), m.width))
	}
	return strings.Join(lines, "\n")
}

func (m Model) pauseSummary() string {
	if m.paused == nil {
		return ""
	}
	direction := "request/outbound"
	if m.pausedMsg != nil && m.pausedMsg.Direction == capture.ServerToClient {
		direction = "response/inbound"
	}
	return fmt.Sprintf("HELD %s #%s/%d · %d queued · %s inspect · %s", direction, m.paused.ID, m.pausedToken, len(m.pauseQueue), m.leaderHint(ActPauseInspect), oneLine(m.paused.Title()))
}

func (m *Model) sizeOverlays() {
	m.sizeEditor()
	m.sizeDetail()
	if m.repeating {
		m.sizeRepeater()
	}
	m.resizeChild()
}

// Reflow searches near the old rendered source fragment instead of resetting
// scroll. Bottom-follow remains stable as streaming messages arrive.
func (m *Model) rewrapDetail() {
	if m.viewFlow == nil {
		return
	}
	bottom := m.detailContent != "" && m.vp.AtBottom()
	oldLines := strings.Split(m.detailContent, "\n")
	offset := m.vp.YOffset
	anchor := ""
	if offset < len(oldLines) {
		anchor = strings.TrimSpace(ansi.Strip(oldLines[offset]))
		r := []rune(anchor)
		if limit := max(1, m.vp.Width/2); len(r) > limit {
			anchor = string(r[:limit])
		}
	}
	m.detailContent = wrapDetail(m.viewFlow, max(1, m.vp.Width), m.detailExpanded)
	m.vp.SetContent(m.detailContent)
	if bottom {
		m.vp.GotoBottom()
		return
	}
	best, distance := -1, int(^uint(0)>>1)
	if anchor != "" {
		for i, line := range strings.Split(m.detailContent, "\n") {
			if !strings.Contains(ansi.Strip(line), anchor) {
				continue
			}
			d := i - offset
			if d < 0 {
				d = -d
			}
			if d < distance {
				best, distance = i, d
			}
		}
	}
	if best >= 0 {
		m.vp.SetYOffset(best)
	} else {
		m.vp.SetYOffset(offset)
	}
}

func oneLine(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, ansi.Strip(s))
}

// fitBlock clips both axes after measuring cells, then pads the exact height.
// lipgloss Height alone is a minimum and otherwise lets modal chrome overflow.
func fitBlock(s string, w, h int) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > h {
		lines = lines[:h]
	}
	for i, line := range lines {
		lines[i] = truncate(line, w)
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func displayKey(key string) string {
	if key == " " {
		return "Space"
	}
	if strings.HasPrefix(key, "ctrl+") {
		suffix := strings.TrimPrefix(key, "ctrl+")
		if suffix == "space" || suffix == "@" {
			return "Ctrl+Space"
		}
		return "Ctrl+" + strings.ToUpper(suffix)
	}
	return key
}

func (m Model) keyHint(ctx string, act Action) string {
	if hint := m.km().hints[ctx][act]; hint != "" {
		return hint
	}
	return "unbound"
}

func (m Model) leaderHint(act Action) string {
	if hint := m.km().globalHints[act]; hint != "" {
		return hint
	}
	return "unbound"
}

// Package tui is the split-pane front end: left pane hosts the target's
// terminal, right pane shows captured flows and the intercept controls. It is a
// standard bubbletea Elm loop — the single writer of UI state — and it never
// touches proxy internals directly, only the capture.Store and intercept.Engine.
package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/citizen-123/cli-capture/internal/capture"
	"github.com/citizen-123/cli-capture/internal/export"
	"github.com/citizen-123/cli-capture/internal/intercept"
	"github.com/citizen-123/cli-capture/internal/protocol"
	"github.com/citizen-123/cli-capture/internal/replay"
	"github.com/citizen-123/cli-capture/internal/runner"
	"github.com/citizen-123/cli-capture/internal/terminal"
)

type focus int

const (
	focusTerminal focus = iota
	focusTraffic
)

// Paused is the notification that a flow is held awaiting a decision, carrying
// the exact outgoing message being tampered (Msg.Raw seeds the editor).
type Paused struct {
	Flow *capture.Flow
	Msg  *capture.Message
}

// Channels the model watches. main wires these to the proxy/PTY plumbing.
type Feeds struct {
	Events <-chan capture.Event // store change events
	Pty    <-chan struct{}      // "PTY produced output, re-render"
	Pause  <-chan Paused        // a flow just became paused
}

type Model struct {
	store  *capture.Store
	engine *intercept.Engine
	target *runner.Target
	screen terminal.Emulator
	feeds  Feeds

	width, height int
	focus         focus
	flows         []*capture.Flow
	selected      int
	paused        *capture.Flow    // flow awaiting a forward/drop decision
	pausedMsg     *capture.Message // the specific outgoing message being tampered
	editing       bool             // true while the raw editor is open
	pendingLeader bool             // true after the leader key, awaiting a command
	ta            textarea.Model
	viewing       bool           // true while the detail view is open
	viewFlow      *capture.Flow  // the flow shown in the detail view
	vp            viewport.Model // scrollable detail body
	injecting     bool           // true while composing a frame to inject
	injectID      string         // flow id of the session being injected into
	injectDir     capture.Direction
	filtering     bool // true while editing the flow-list filter
	fi            textinput.Model
	flaggedOnly   bool // show only flagged flows
	sort          sortMode
	showHelp      bool // floating help overlay
	repeating     bool // repeater modal open
	rep           repeaterState
	splitRatio    float64 // fraction of the width given to the left (terminal) pane
	sessionPath   string  // where "save session" writes
	status        string
	keys          KeyMap // key → action bindings; zero value means the defaults
}

// WithKeys returns the model using km for dispatch and for the help overlay.
func (m Model) WithKeys(km KeyMap) Model {
	m.keys = km
	return m
}

// km is the model's keymap, falling back to the defaults so a zero-value Model
// (as used in tests) still dispatches and renders help.
func (m Model) km() KeyMap {
	if m.keys.binds == nil {
		return defaultKeyMap
	}
	return m.keys
}

// defaultKeyMap is built once: dispatch consults it on every keystroke.
var defaultKeyMap = DefaultKeyMap()

// Split resize bounds. splitStep is the per-keystroke adjustment; the ratio is
// clamped to [minSplit, maxSplit] so neither pane can be squeezed to nothing.
const (
	splitStep = 0.05
	minSplit  = 0.2
	maxSplit  = 0.8
)

func New(store *capture.Store, engine *intercept.Engine, target *runner.Target, screen terminal.Emulator, feeds Feeds, sessionPath string) Model {
	return Model{
		store:       store,
		engine:      engine,
		target:      target,
		screen:      screen,
		feeds:       feeds,
		focus:       focusTerminal,
		ta:          newEditor(),
		vp:          viewport.New(0, 0),
		fi:          newFilter(),
		splitRatio:  0.5,
		sessionPath: sessionPath,
		status:      "? for help · Ctrl+A w switch pane · Ctrl+A < > resize · Ctrl+A i arm intercept · Ctrl+A q quit",
	}
}

// saveSession writes every captured flow to the session file and returns a
// status line reporting the outcome.
func (m Model) saveSession() string {
	flows := m.store.List()
	if err := capture.SaveFile(m.sessionPath, flows); err != nil {
		return "save failed: " + err.Error()
	}
	return fmt.Sprintf("saved %d flows to %s", len(flows), m.sessionPath)
}

// visible returns the flows matching the current filter (or all of them when
// the filter is empty). The match is a case-insensitive substring over the
// flow's title, protocol, status, and server address.
func (m Model) visible() []*capture.Flow {
	q := strings.ToLower(strings.TrimSpace(m.fi.Value()))
	if q == "" && !m.flaggedOnly && m.sort == sortNone {
		return m.flows
	}
	out := make([]*capture.Flow, 0, len(m.flows))
	for _, f := range m.flows {
		if m.flaggedOnly && !f.Flagged {
			continue
		}
		if q != "" {
			hay := f.Title() + " " + string(f.Protocol) + " " + f.Status.String() + " " + f.ServerAddr
			if f.Request != nil {
				hay += " " + f.Request.Meta["method"] + " " + f.Request.Meta["path"]
			}
			if !strings.Contains(strings.ToLower(hay), q) {
				continue
			}
		}
		out = append(out, f)
	}
	switch m.sort {
	case sortStatus:
		sort.SliceStable(out, func(i, j int) bool { return rowCode(out[i]) < rowCode(out[j]) })
	case sortSize:
		sort.SliceStable(out, func(i, j int) bool { return respSize(out[i]) > respSize(out[j]) })
	}
	return out
}

// selectedFlow returns the currently highlighted flow in the filtered list.
func (m Model) selectedFlow() *capture.Flow {
	vis := m.visible()
	if m.selected < 0 || m.selected >= len(vis) {
		return nil
	}
	return vis[m.selected]
}

// resendSelected re-issues the selected flow's request to its origin and adds
// the new exchange to the store.
func (m Model) resendSelected() string {
	f := m.selectedFlow()
	if f == nil {
		return "nothing selected to resend"
	}
	nf, err := replay.Resend(f)
	if err != nil {
		return "resend failed: " + err.Error()
	}
	m.store.Add(nf)
	return "resent " + f.Title()
}

// exportHAR writes all captured flows to a HAR file next to the session file.
func (m Model) exportHAR() string {
	data, err := export.HAR(m.store.List())
	if err != nil {
		return "har: " + err.Error()
	}
	path := filepath.Join(filepath.Dir(m.sessionPath), "capture.har")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "har: " + err.Error()
	}
	return "exported HAR to " + path
}

// exportViewedFlow writes the flow currently open in the detail view to a text
// file.
func (m Model) exportViewedFlow() string {
	if m.viewFlow == nil {
		return "no flow in view"
	}
	path := filepath.Join(filepath.Dir(m.sessionPath), "flow-"+m.viewFlow.ID+".txt")
	if err := os.WriteFile(path, []byte(export.FlowText(m.viewFlow)), 0o644); err != nil {
		return "export: " + err.Error()
	}
	return "wrote " + path
}

// exportFlagged writes every flagged flow for the run to one text file.
func (m Model) exportFlagged() string {
	var flagged []*capture.Flow
	for _, f := range m.store.List() {
		if f.Flagged {
			flagged = append(flagged, f)
		}
	}
	if len(flagged) == 0 {
		return "no flagged flows to export"
	}
	path := filepath.Join(filepath.Dir(m.sessionPath), "flagged.txt")
	if err := os.WriteFile(path, []byte(export.FlowsText(flagged)), 0o644); err != nil {
		return "export: " + err.Error()
	}
	return fmt.Sprintf("wrote %d flagged flows to %s", len(flagged), path)
}

// exportCurlSelected writes the selected flow's request as a curl command file.
func (m Model) exportCurlSelected() string {
	f := m.selectedFlow()
	if f == nil {
		return "nothing selected to export"
	}
	cmd, err := export.Curl(f)
	if err != nil {
		return "curl: " + err.Error()
	}
	path := filepath.Join(filepath.Dir(m.sessionPath), "flow-"+f.ID+".curl")
	if err := os.WriteFile(path, []byte(cmd+"\n"), 0o644); err != nil {
		return "curl: " + err.Error()
	}
	return "wrote curl to " + path
}

// --- messages ---

type eventMsg capture.Event
type ptyMsg struct{}
type pauseMsg struct{ item Paused }

func (m Model) Init() tea.Cmd {
	return tea.Batch(waitEvent(m.feeds.Events), waitPty(m.feeds.Pty), waitPause(m.feeds.Pause))
}

func waitEvent(ch <-chan capture.Event) tea.Cmd {
	return func() tea.Msg { return eventMsg(<-ch) }
}
func waitPty(ch <-chan struct{}) tea.Cmd {
	return func() tea.Msg { <-ch; return ptyMsg{} }
}
func waitPause(ch <-chan Paused) tea.Cmd {
	return func() tea.Msg { return pauseMsg{item: <-ch} }
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resizeChild()
		m.sizeEditor()
		m.sizeDetail()
		if m.repeating {
			m.sizeRepeater()
		}
		return m, nil

	case ptyMsg:
		return m, waitPty(m.feeds.Pty) // just re-render; screen already updated

	case eventMsg:
		m.flows = m.store.List()
		if m.viewing && m.viewFlow != nil {
			// Keep the detail view live as a streaming flow accrues messages.
			m.vp.SetContent(wrapDetail(m.viewFlow, m.vp.Width))
		}
		return m, waitEvent(m.feeds.Events)

	case pauseMsg:
		m.paused = msg.item.Flow
		m.pausedMsg = msg.item.Msg
		m.focus = focusTraffic
		m.status = "PAUSED: [e]dit  [f]orward  [d]rop"
		return m, waitPause(m.feeds.Pause)

	case repeaterResultMsg:
		m.rep.resp = msg.flow
		if msg.err != nil {
			m.rep.result = "error: " + msg.err.Error()
		} else if msg.flow != nil && msg.flow.Response != nil {
			m.rep.result = "sent · " + msg.flow.Response.Summary
		}
		m.rep.respVP.SetContent(renderRepeaterResponse(msg.flow, m.rep.respVP.Width))
		m.rep.respVP.GotoTop()
		return m, nil

	case attackDoneMsg:
		m.rep.result = fmt.Sprintf("attack complete · %d requests sent (see traffic list)", msg.count)
		return m, nil

	case tea.KeyMsg:
		if m.showHelp {
			return m.onHelpKey(msg)
		}
		if m.repeating {
			return m.onRepeaterKey(msg)
		}
		if m.editing {
			return m.onEditKey(msg)
		}
		if m.filtering {
			return m.onFilterKey(msg)
		}
		if m.viewing {
			return m.onViewKey(msg)
		}
		return m.onKey(msg)
	}
	return m, nil
}

// onHelpKey handles keys while the help overlay is open: it is modal, so keys
// don't fall through — esc/q/? close it, Ctrl+Q still quits.
func (m Model) onHelpKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if k.Type == tea.KeyCtrlQ {
		return m, tea.Quit
	}
	if k.Type == tea.KeyEsc || k.String() == "q" || k.String() == "?" {
		m.showHelp = false
	}
	return m, nil
}

// onFilterKey handles keys while the flow-list filter is being edited. Enter
// applies and keeps the filter; Esc clears it; other keys edit the query live.
func (m Model) onFilterKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.Type {
	case tea.KeyEnter:
		m.filtering = false
		m.fi.Blur()
		return m, nil
	case tea.KeyEsc:
		m.filtering = false
		m.fi.Blur()
		m.fi.SetValue("")
		return m, nil
	}
	var cmd tea.Cmd
	m.fi, cmd = m.fi.Update(k)
	if m.selected >= len(m.visible()) {
		m.selected = 0 // keep selection valid as the filter narrows the list
	}
	return m, cmd
}

// onViewKey handles keys while the detail view is open: Esc/q closes it, and
// everything else scrolls the viewport.
func (m Model) onViewKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.km().Action(ctxDetail, k.String()) {
	case ActDetailClose:
		m.viewing = false
		m.viewFlow = nil
		return m, nil
	case ActDetailSave:
		m.status = m.exportViewedFlow()
		return m, nil
	}
	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(k)
	return m, cmd
}

func (m *Model) sizeDetail() {
	m.vp.Width = m.rightPaneWidth() - 3
	m.vp.Height = m.height - 5
}

func (m *Model) openDetail() {
	f := m.selectedFlow()
	if f == nil {
		return
	}
	m.viewFlow = f
	m.sizeDetail()
	m.vp.SetContent(wrapDetail(m.viewFlow, m.vp.Width))
	m.vp.GotoTop()
	m.viewing = true
}

func (m Model) onKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Leader dispatch: if the previous key was the leader, this key is a command.
	if m.pendingLeader {
		return m.leaderCommand(k)
	}
	if k.Type == m.km().Leader {
		m.pendingLeader = true
		return m, nil
	}

	if m.focus == focusTerminal {
		// Forward the keystroke to the child PTY verbatim.
		if b := keyBytes(k); b != nil && m.target != nil {
			m.target.Pty.Write(b)
		}
		return m, nil
	}

	// Traffic pane. Interception toggles also live here as plain letters because
	// this pane never forwards keystrokes to the target.
	switch m.km().Action(ctxTraffic, k.String()) {
	case ActInterceptRequests:
		m.engine.SetEnabled(!m.engine.Enabled())
	case ActInterceptResponses:
		m.engine.SetInterceptResponses(!m.engine.InterceptResponses())
	case ActFlowPrev:
		if m.selected > 0 {
			m.selected--
		}
	case ActFlowNext:
		if m.selected < len(m.visible())-1 {
			m.selected++
		}
	case ActFilterOpen:
		m.filtering = true
		m.fi.Focus()
	case ActFlowFlag:
		if f := m.selectedFlow(); f != nil {
			f.Flagged = !f.Flagged
		}
	case ActFlaggedOnly:
		m.flaggedOnly = !m.flaggedOnly
		m.selected = clampIndex(m.selected, len(m.visible()))
	case ActSortCycle:
		m.sort = (m.sort + 1) % 3
		m.selected = clampIndex(m.selected, len(m.visible()))
	case ActExportCurl:
		m.status = m.exportCurlSelected()
	case ActPausedEdit:
		if m.paused != nil {
			m.enterEdit()
		}
	case ActPausedFwd:
		if m.paused != nil {
			m.engine.Resolve(m.paused.ID, intercept.Resolution{Decision: intercept.Forward})
			m.clearPause()
			m.status = "forwarded"
		}
	case ActPausedDrop:
		if m.paused != nil {
			m.engine.Resolve(m.paused.ID, intercept.Resolution{Decision: intercept.Drop})
			m.clearPause()
			m.status = "dropped"
		}
	case ActHelpToggle:
		m.showHelp = true
	case ActRepeaterNew:
		m.openRepeater()
	case ActFlowResend:
		m.status = m.resendSelected()
	case ActDetailOpen:
		m.openDetail()
	case ActInjectOut:
		m.enterInject(capture.ClientToServer)
	case ActInjectIn:
		m.enterInject(capture.ServerToClient)
	}
	return m, nil
}

// enterInject opens the editor to compose a WebSocket frame to inject into a
// live session, in the given direction.
func (m *Model) enterInject(dir capture.Direction) {
	f := m.selectedFlow()
	if f == nil {
		return
	}
	if f.Protocol != capture.ProtoWebSocket || f.Status != capture.StatusActive {
		m.status = "inject: select a live WebSocket flow"
		return
	}
	if _, ok := m.engine.Session(f.ID); !ok {
		m.status = "inject: no live session for that flow"
		return
	}
	m.injecting = true
	m.injectID = f.ID
	m.injectDir = dir
	m.ta.SetValue("")
	m.sizeEditor()
	m.ta.Focus()
	m.editing = true // reuse the editor modal for composing
	m.status = "INJECT " + dir.String() + ": Ctrl+S send · Esc cancel"
}

// doInject sends the composed frame into the live session.
func (m *Model) doInject() string {
	inj, ok := m.engine.Session(m.injectID)
	if !ok {
		return "inject: session closed"
	}
	if err := inj.Inject(m.injectDir, protocol.WSText, []byte(m.ta.Value())); err != nil {
		return "inject failed: " + err.Error()
	}
	return "injected " + m.injectDir.String() + " frame"
}

// leaderCommand interprets the key pressed after the leader. Pressing the leader
// twice sends a literal leader byte to the target (tmux behavior), so the child
// app can still receive Ctrl+A.
func (m Model) leaderCommand(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.pendingLeader = false

	if k.Type == m.km().Leader {
		if m.focus == focusTerminal && m.target != nil {
			// KeyType 1-26 is the ASCII control code, so this is the literal
			// leader byte whichever ctrl key is configured.
			m.target.Pty.Write([]byte{byte(m.km().Leader)})
		}
		return m, nil
	}

	switch m.km().Action(ctxLeader, k.String()) {
	case ActPaneSwitch:
		if m.focus == focusTerminal {
			m.focus = focusTraffic
		} else {
			m.focus = focusTerminal
		}
	case ActQuit:
		return m, tea.Quit
	case ActInterceptRequests:
		m.engine.SetEnabled(!m.engine.Enabled())
	case ActInterceptResponses:
		m.engine.SetInterceptResponses(!m.engine.InterceptResponses())
	case ActSessionSave:
		m.status = m.saveSession()
	case ActExportHAR:
		m.status = m.exportHAR()
	case ActExportFlagged:
		m.status = m.exportFlagged()
	case ActSplitShrink:
		m.adjustSplit(-splitStep)
	case ActSplitGrow:
		m.adjustSplit(splitStep)
	case ActHelpToggle:
		m.showHelp = !m.showHelp
	}
	return m, nil
}

// onEditKey handles keys while the raw editor is open. Ctrl+S forwards the
// edited bytes, Ctrl+L fixes the HTTP Content-Length, Esc cancels back to the
// forward/drop prompt, and everything else is typing into the textarea.
func (m Model) onEditKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.km().Action(ctxEditor, k.String()) {
	case ActQuit:
		return m, tea.Quit
	case ActEditorSend:
		if m.injecting {
			status := m.doInject()
			m.editing = false
			m.injecting = false
			m.ta.Blur()
			m.status = status
			return m, nil
		}
		if m.paused != nil {
			m.engine.Resolve(m.paused.ID, intercept.Resolution{
				Decision:   intercept.Forward,
				EditedBody: []byte(m.ta.Value()),
			})
			m.clearPause()
			m.status = "forwarded (edited)"
		}
		return m, nil
	case ActEditorFixLen:
		if !m.injecting {
			m.ta.SetValue(fixContentLength(m.ta.Value()))
			m.status = "fixed Content-Length"
		}
		return m, nil
	case ActEditorCancel:
		m.editing = false
		m.injecting = false
		m.ta.Blur()
		if m.paused != nil {
			m.status = "PAUSED: [e]dit  [f]orward  [d]rop"
		} else {
			m.status = "cancelled"
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(k)
	return m, cmd
}

// enterEdit opens the raw editor seeded with the paused message's bytes.
func (m *Model) enterEdit() {
	seed := ""
	if m.pausedMsg != nil {
		seed = string(m.pausedMsg.Raw)
	}
	m.ta.SetValue(seed)
	m.sizeEditor()
	m.ta.Focus()
	m.editing = true
	m.status = "EDIT: Ctrl+S forward · Ctrl+L fix len · Esc cancel"
}

func (m *Model) clearPause() {
	m.paused = nil
	m.pausedMsg = nil
	m.editing = false
	m.ta.Blur()
}

func (m *Model) sizeEditor() {
	m.ta.SetWidth(m.rightPaneWidth() - 3)
	m.ta.SetHeight(m.height - 6)
}

// leftPaneWidth is the outer width of the left pane — the value handed to
// lipgloss .Width(); the rounded border adds one column on each side. It is a
// fraction of the terminal width set by splitRatio, and equals the old
// hardcoded m.width/2 - 1 when the ratio is 0.5.
func (m Model) leftPaneWidth() int {
	return int(float64(m.width)*m.splitRatio) - 1
}

// rightPaneWidth is the outer width of the right pane: whatever is left after the
// left pane and both borders. leftPaneWidth + rightPaneWidth is always m.width-2,
// so the two panes fit side by side exactly as the even 50/50 split did.
func (m Model) rightPaneWidth() int {
	return m.width - 2 - m.leftPaneWidth()
}

// leftSize is the terminal grid size for the left pane's content area. The PTY,
// the emulator, and the render call must all use it so the target draws for the
// exact grid we display.
func (m Model) leftSize() (cols, rows int) {
	paneW := m.leftPaneWidth()
	paneH := m.height - 3
	return paneW - 2, paneH - 1
}

// adjustSplit nudges the split ratio by delta, clamped to [minSplit, maxSplit],
// and re-sizes the target PTY/emulator and the right-pane overlays so everything
// redraws at the new widths.
func (m *Model) adjustSplit(delta float64) {
	r := m.splitRatio + delta
	if r < minSplit {
		r = minSplit
	}
	if r > maxSplit {
		r = maxSplit
	}
	if r == m.splitRatio {
		return
	}
	m.splitRatio = r
	m.resizeChild()
	m.sizeEditor()
	m.sizeDetail()
}

func (m *Model) resizeChild() {
	cols, rows := m.leftSize()
	if cols < 1 || rows < 1 {
		return
	}
	if m.target != nil {
		_ = m.target.Resize(uint16(rows), uint16(cols))
	}
	if m.screen != nil {
		m.screen.Resize(cols, rows)
	}
}

func (m Model) View() string {
	if m.width == 0 {
		return "starting…"
	}
	if m.showHelp {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, m.helpView())
	}
	if m.repeating {
		return m.repeaterView()
	}
	leftW := m.leftPaneWidth()
	rightW := m.rightPaneWidth()
	paneH := m.height - 3

	lw, lh := m.leftSize()
	left := paneStyle(m.focus == focusTerminal).Width(leftW).Height(paneH).
		Render(m.screen.Render(lw, lh))

	rightContent := m.renderTraffic(rightW-2, paneH-1)
	switch {
	case m.editing:
		rightContent = m.renderEditor()
	case m.viewing:
		rightContent = m.renderDetail()
	}
	right := paneStyle(m.focus == focusTraffic).Width(rightW).Height(paneH).
		Render(rightContent)

	body := lipgloss.JoinHorizontal(lipgloss.Top, left, right)
	bar := statusBar(m.width, m.status, m.engine.Enabled(), m.engine.InterceptResponses())
	return body + "\n" + bar
}

func (m Model) renderTraffic(w, h int) string {
	var b strings.Builder
	vis := m.visible()

	header := fmt.Sprintf("Traffic (%d)", len(vis))
	if len(vis) != len(m.flows) {
		header = fmt.Sprintf("Traffic (%d/%d)", len(vis), len(m.flows))
	}
	if m.flaggedOnly {
		header += " " + glyphFlag + "only"
	}
	if m.sort != sortNone {
		header += " ↕" + m.sort.String()
	}
	b.WriteString(titleStyle.Render(header) + "\n")

	// Reserve lines for the header, the optional filter line, and the paused
	// prompt, so the scrollable list gets the remaining rows.
	reserved := 1
	if m.filtering || m.fi.Value() != "" {
		b.WriteString(truncate(m.fi.View(), w) + "\n")
		reserved++
	}
	if m.paused != nil {
		reserved += 2
	}
	listRows := h - reserved
	if listRows < 1 {
		listRows = 1
	}

	// Slide the window so the selection stays visible (the missing scroll).
	sel := clampIndex(m.selected, len(vis))
	start := 0
	if sel >= listRows {
		start = sel - listRows + 1
	}
	for i := start; i < len(vis) && i < start+listRows; i++ {
		b.WriteString(m.renderFlowRow(vis[i], i == sel, w) + "\n")
	}

	if m.paused != nil {
		b.WriteString("\n" + pendingStyle.Render(glyphPointer+" "+m.paused.Title()))
		b.WriteString("\n[e]dit  [f]orward  [d]rop")
	}
	return b.String()
}

// clampIndex bounds i to a valid index in a slice of length n (0 if empty).
func clampIndex(i, n int) int {
	if n == 0 {
		return 0
	}
	if i < 0 {
		return 0
	}
	if i >= n {
		return n - 1
	}
	return i
}

func (m Model) renderDetail() string {
	title := "Detail"
	if m.viewFlow != nil {
		title = "Detail " + glyphArrow + " " + m.viewFlow.Title()
	}
	return titleStyle.Render(truncate(title, m.vp.Width)) + "\n" + m.vp.View() + "\n" + "[esc] back · j/k scroll · s save to txt"
}

func (m Model) renderEditorTitle() string {
	if m.injecting {
		return "Inject WS frame (" + m.injectDir.String() + ")"
	}
	if m.paused != nil {
		return "Edit " + glyphPointer + " " + m.paused.Title()
	}
	return "Edit request"
}

func (m Model) renderEditor() string {
	return titleStyle.Render(m.renderEditorTitle()) + "\n" + m.ta.View()
}

func truncate(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

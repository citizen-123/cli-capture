// Package tui is the split-pane front end: left pane hosts the target's
// terminal, right pane shows captured flows and the intercept controls. It is a
// standard bubbletea Elm loop — the single writer of UI state — and it never
// touches proxy internals directly, only the capture.Store and intercept.Engine.
package tui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/citizen-123/cli-capture/internal/capture"
	"github.com/citizen-123/cli-capture/internal/export"
	"github.com/citizen-123/cli-capture/internal/intercept"
	"github.com/citizen-123/cli-capture/internal/ownerfile"
	"github.com/citizen-123/cli-capture/internal/protocol"
	"github.com/citizen-123/cli-capture/internal/runner"
	"github.com/citizen-123/cli-capture/internal/terminal"
)

type focus int

const (
	focusTerminal focus = iota
	focusTraffic
)

// Paused is the notification that one message is held awaiting a decision. Its
// token resolves this exact pause even when the flow has another paused message;
// Msg.Raw seeds the editor.
type Paused struct {
	Token intercept.PauseToken
	Flow  *capture.Flow
	Msg   *capture.Message
}

// Channels the model watches. main wires these to the proxy/PTY plumbing.
type Feeds struct {
	Events <-chan capture.Event // store change events
	Pty    <-chan struct{}      // "PTY produced output, re-render"
	Pause  <-chan Paused        // a message just became paused
}

type Model struct {
	store  *capture.Store
	engine *intercept.Engine
	target *runner.Target
	screen terminal.Emulator
	feeds  Feeds

	width, height       int
	focus               focus
	flows               []*capture.Flow
	selected            int
	paused              *capture.Flow        // flow awaiting a forward/drop decision
	pausedMsg           *capture.Message     // the specific outgoing message being tampered
	pausedToken         intercept.PauseToken // identity of the active pause occurrence
	pauseQueue          []Paused             // paused messages waiting behind the active one, in arrival order
	editing             bool                 // true while the raw editor is open
	pendingLeader       bool                 // true after the leader key, awaiting a command
	ta                  textarea.Model
	editBuffer          messageEditBuffer
	viewing             bool           // true while the detail view is open
	viewFlow            *capture.Flow  // the flow shown in the detail view
	vp                  viewport.Model // scrollable detail body
	detailExpanded      bool
	detailContent       string
	injecting           bool   // true while composing a frame to inject
	injectID            string // flow id of the session being injected into
	injectDir           capture.Direction
	filtering           bool // true while editing the flow-list filter
	fi                  textinput.Model
	cmdline             bool // true while editing the ':' command line (vim spike, #9)
	ci                  textinput.Model
	count               int  // pending numeric prefix for list motions (5j, 3k)
	pendingG            bool // saw one 'g'; a second makes gg (jump to top)
	flaggedOnly         bool // show only flagged flows
	sort                sortMode
	showHelp            bool // floating help overlay
	helpScroll          int  // first visible line of the help overlay
	repeating           bool // repeater modal open
	rep                 repeaterState
	splitRatio          float64 // fraction of the width given to the left (terminal) pane
	modalSuspended      bool
	pauseReturn         *foregroundState
	suspendedFocus      focus
	helpReturnSuspended bool
	pauseEditDraft      *pausedEditorDraft
	job                 *requestJob
	nextJobID           uint64
	nextRepeaterID      uint64
	sessionPath         string // where "save session" writes
	status              string
	// mouseCapture tracks whether cli-capture is asking bubbletea to report
	// mouse events. Capturing the mouse steals native terminal-emulator text
	// selection/copy-paste, so the operator needs a way to hand it back
	// without restarting — see the leader mouse-capture command.
	mouseCapture bool
	keys         KeyMap // key → action bindings; zero value means the defaults
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
		flows:       store.List(),
		engine:      engine,
		target:      target,
		screen:      screen,
		feeds:       feeds,
		focus:       focusTerminal,
		ta:          newEditor(),
		vp:          viewport.New(0, 0),
		fi:          newFilter(),
		ci:          newCommandLine(),
		splitRatio:  0.5,
		sessionPath: sessionPath,
		// main.go starts the tea.Program with tea.WithMouseCellMotion(); this
		// mirrors that so the mouse-capture toggle's first press turns capture off.
		mouseCapture: true,
		status:       "",
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

func (m *Model) toggleSelectedFlag() string {
	f := m.selectedFlow()
	if f == nil {
		return "nothing selected to flag"
	}
	f.Flagged = !f.Flagged
	m.selected = clampIndex(m.selected, len(m.visible()))
	if f.Flagged {
		return "flagged " + f.Title()
	}
	return "unflagged " + f.Title()
}

// exportHAR writes all captured flows to a HAR file next to the session file.
func (m Model) exportHAR() string {
	data, err := export.HAR(m.store.List())
	if err != nil {
		return "har: " + err.Error()
	}
	path := filepath.Join(filepath.Dir(m.sessionPath), "capture.har")
	if err := ownerfile.Write(path, data); err != nil {
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
	if err := ownerfile.Write(path, []byte(export.FlowText(m.viewFlow))); err != nil {
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
	if err := ownerfile.Write(path, []byte(export.FlowsText(flagged))); err != nil {
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
	if err := ownerfile.Write(path, []byte(cmd+"\n")); err != nil {
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
		m.width, m.height = max(0, msg.Width), max(0, msg.Height)
		m.resizeChild()
		m.sizeOverlays()
		return m, nil

	case ptyMsg:
		return m, waitPty(m.feeds.Pty) // just re-render; screen already updated

	case eventMsg:
		m.flows = m.store.List()
		m.rewrapDetail()
		return m, waitEvent(m.feeds.Events)

	case pauseMsg:
		m.enqueuePause(msg.item)
		m.sizeOverlays()
		return m, waitPause(m.feeds.Pause)

	case tea.MouseMsg:
		return m.onMouse(msg)

	case jobEventMsg:
		cmd := m.handleJobEvent(msg)
		m.sizeOverlays()
		return m, cmd

	case tea.KeyMsg:
		if m.pendingLeader {
			return m.clearMotion().leaderCommand(msg)
		}
		if !msg.Alt && msg.Type == m.km().Leader {
			m = m.clearMotion()
			m.pendingLeader = true
			return m, nil
		}
		if m.modalSuspended {
			return m.onKey(msg)
		}
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
		if m.cmdline {
			return m.onCmdKey(msg)
		}
		if m.viewing {
			return m.onViewKey(msg)
		}
		return m.onKey(msg)
	}
	return m, nil
}

// onHelpKey handles keys while the help overlay is open: it is modal, so keys
// don't fall through — esc/q/? close it, Ctrl+Q still quits. The body is far
// taller than a terminal, so j/k/G/gg scroll it; without that the ':' command
// section near the bottom would be unreachable.
func (m Model) onHelpKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if k.Type == tea.KeyCtrlQ {
		m.cancelJob()
		return m, tea.Quit
	}
	max := m.maxHelpScroll(m.height - m.chromeHeight())
	switch {
	case k.Type == tea.KeyEsc, k.String() == "q", k.String() == "?":
		m.closeHelp()
		return m, nil
	case k.String() == "j", k.Type == tea.KeyDown:
		m.helpScroll++
	case k.String() == "k", k.Type == tea.KeyUp:
		m.helpScroll--
	case k.Type == tea.KeyPgDown, k.String() == "ctrl+f":
		m.helpScroll += max/4 + 1
	case k.Type == tea.KeyPgUp, k.String() == "ctrl+b":
		m.helpScroll -= max/4 + 1
	case k.String() == "G":
		m.helpScroll = max
	case k.String() == "g":
		m.helpScroll = 0
	}
	return m.clampHelpScroll(), nil
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
	case ActDetailFormat:
		m.detailExpanded = !m.detailExpanded
		m.rewrapDetail()
		return m, nil
	}
	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(k)
	return m, cmd
}

func (m *Model) sizeDetail() {
	atBottom := m.detailContent != "" && m.vp.AtBottom()
	m.vp.Width = max(1, m.rightPaneWidth()-2)
	m.vp.Height = max(1, m.paneContentHeight()-4)
	m.rewrapDetail()
	if atBottom {
		m.vp.GotoBottom()
	}
}

func (m *Model) openDetail() {
	f := m.selectedFlow()
	if f == nil {
		return
	}
	m.viewFlow = f
	m.detailExpanded = true
	m.detailContent = ""
	m.sizeDetail()
	m.vp.GotoTop()
	m.viewing = true
}

func (m Model) forwardTerminalKey(k tea.KeyMsg) {
	if m.target == nil {
		return
	}
	var b []byte
	stateful := false
	if m.screen != nil {
		if k.Type == tea.KeyRunes && k.Paste {
			stateful = true
			b = m.screen.PasteBytes(string(k.Runes))
		} else if !k.Alt {
			switch k.Type {
			case tea.KeyUp:
				stateful = true
				b = m.screen.CursorKeyBytes(terminal.CursorUp)
			case tea.KeyDown:
				stateful = true
				b = m.screen.CursorKeyBytes(terminal.CursorDown)
			case tea.KeyRight:
				stateful = true
				b = m.screen.CursorKeyBytes(terminal.CursorRight)
			case tea.KeyLeft:
				stateful = true
				b = m.screen.CursorKeyBytes(terminal.CursorLeft)
			}
		}
	}
	if !stateful {
		b = keyBytes(k)
	}
	for len(b) > 0 {
		n, err := m.target.Pty.Write(b)
		if err != nil || n == 0 {
			return
		}
		b = b[n:]
	}
}

func (m Model) onKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.focus == focusTerminal {
		m = m.clearMotion()
		m.forwardTerminalKey(k)
		return m, nil
	}

	// Traffic pane. Interception toggles also live here as plain letters because
	// this pane never forwards keystrokes to the target.
	key := k.String()
	if k.Type == tea.KeyEsc && m.pauseReturn != nil {
		m.restoreForeground()
		return m, nil
	}
	act := m.km().Action(ctxTraffic, key)

	// vim-style numeric prefix and gg/G motions (spike, #9): only for keys the
	// keymap doesn't claim (user bindings always win) and only in the traffic
	// pane, so counts/motions never reach a target that might itself be vim.
	if act == "" && !m.km().claims(ctxTraffic, key) {
		if n, ok := digit(key); ok && (n > 0 || m.count > 0) {
			m.count = m.count*10 + n
			if m.count > 100000 {
				m.count = 100000
			}
			return m, nil
		}
		switch key {
		case "g": // gg → first flow, or row {count} — vim's 5gg is line 5
			if !m.pendingG {
				m.pendingG = true
				return m, nil
			}
			n := len(m.visible())
			target := 0
			if m.count > 0 {
				target = clampIndex(m.count-1, n)
			}
			m.selected = target
			return m.clearMotion(), nil
		case "G": // jump to the last flow, or to row {count}
			n := len(m.visible())
			target := n - 1
			if m.count > 0 {
				target = clampIndex(m.count-1, n)
			}
			if target < 0 {
				target = 0
			}
			m.selected = target
			return m.clearMotion(), nil
		}
	}
	m.pendingG = false
	count := m.count
	if count < 1 {
		count = 1
	}
	m.count = 0

	switch act {
	case ActInterceptRequests:
		m.engine.SetEnabled(!m.engine.Enabled())
	case ActInterceptResponses:
		m.engine.SetInterceptResponses(!m.engine.InterceptResponses())
	case ActFlowPrev:
		m.selected = clampIndex(m.selected-count, len(m.visible()))
	case ActFlowNext:
		m.selected = clampIndex(m.selected+count, len(m.visible()))
	case ActHostNext:
		m.selected = hostJump(m.visible(), m.selected, +1, count)
	case ActHostPrev:
		m.selected = hostJump(m.visible(), m.selected, -1, count)
	case ActCommand:
		m.cmdline = true
		m.ci.Focus()
	case ActFilterOpen:
		m.filtering = true
		m.fi.Focus()
	case ActFlowFlag:
		m.status = m.toggleSelectedFlag()
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
			m.engine.Resolve(m.pausedToken, intercept.Resolution{Decision: intercept.Forward})
			m.completePause("forwarded")
		}
	case ActPausedDrop:
		if m.paused != nil {
			m.engine.Resolve(m.pausedToken, intercept.Resolution{Decision: intercept.Drop})
			m.completePause("dropped")
		}
	case ActHelpToggle:
		m.toggleHelp()
	case ActRepeaterNew:
		m.openRepeater()
	case ActFlowResend:
		cmd := m.startResend()
		m.sizeOverlays()
		return m, cmd
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
	m.ta = newEditor()
	m.editBuffer = newMessageEditBuffer(nil, false)
	m.sizeEditor()
	m.ta.Focus()
	m.editing = true // reuse the editor modal for composing
	m.status = "Inject " + dir.String()
}

// doInject retains the draft until the live writer accepts the complete frame.
func (m *Model) doInject() error {
	inj, ok := m.engine.Session(m.injectID)
	if !ok {
		return fmt.Errorf("inject: session closed")
	}
	if err := inj.Inject(m.injectDir, protocol.WSText, []byte(m.ta.Value())); err != nil {
		return fmt.Errorf("inject failed: %w", err)
	}
	m.status = "injected " + m.injectDir.String() + " frame"
	return nil
}

// leaderCommand interprets the key pressed after the leader. Pressing the leader
// twice sends a literal leader byte to the target (tmux behavior), so the child
// app can still receive the leader chord itself.
func (m Model) leaderCommand(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.pendingLeader = false

	if k.Type == tea.KeyEsc {
		return m, nil
	}
	if !k.Alt && k.Type == m.km().Leader {
		if m.keyboardOwner() == "terminal" {
			m.forwardTerminalKey(k)
		} else {
			m.status = "Literal leader needs terminal focus: " + m.leaderHint(ActPaneSwitch)
		}
		return m, nil
	}

	switch m.km().Action(ctxLeader, k.String()) {
	case ActPaneSwitch:
		m.switchPane()
	case ActQuit:
		m.cancelJob()
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
	case ActJobCancel:
		m.cancelJob()
		m.sizeOverlays()
	case ActPauseInspect:
		m.inspectPause()
		m.sizeOverlays()
	case ActMouseCapture:
		return m.toggleMouseCapture()
	case ActHelpToggle:
		m.toggleHelp()
	default:
		m.status = "Unknown leader command; help: " + m.leaderHint(ActHelpToggle)
	}
	return m, nil
}

// toggleMouseCapture flips whether cli-capture asks bubbletea to report mouse
// events. Capturing steals the terminal emulator's own text selection and
// copy-paste, so this is the re-entrant escape hatch: toggle off to select
// text natively, then toggle back on — no restart needed either way, since
// EnableMouseCellMotion/DisableMouse just re-issue the DECSET/DECRST
// sequences bubbletea already knows how to send.
func (m Model) toggleMouseCapture() (tea.Model, tea.Cmd) {
	m.mouseCapture = !m.mouseCapture
	if m.mouseCapture {
		m.status = "Mouse capture ON; " + m.leaderHint(ActMouseCapture) + " for native selection"
		return m, tea.EnableMouseCellMotion
	}
	m.status = "Mouse capture OFF; " + m.leaderHint(ActMouseCapture) + " to enable"
	return m, tea.DisableMouse
}

// onEditKey reconstructs original message bytes before resolving the exact pause.
func (m Model) onEditKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.km().Action(ctxEditor, k.String()) {
	case ActQuit:
		m.cancelJob()
		return m, tea.Quit
	case ActEditorSend:
		if m.injecting {
			if err := m.doInject(); err != nil {
				m.status = err.Error()
				return m, nil
			}
			m.editing = false
			m.injecting = false
			m.ta.Blur()
			return m, nil
		}
		if m.paused != nil {
			body, err := m.editBuffer.Bytes(m.ta.Value())
			if err != nil {
				m.status = "edit: " + err.Error()
				return m, nil
			}
			m.engine.Resolve(m.pausedToken, intercept.Resolution{
				Decision:   intercept.Forward,
				EditedBody: body,
			})
			m.completePause("forwarded (edited)")
		}
		return m, nil
	case ActEditorFixLen:
		if !m.injecting {
			text, err := m.editBuffer.FixLength(m.ta.Value())
			if err != nil {
				m.status = "Content-Length: " + err.Error()
			} else {
				m.ta.SetValue(text)
				m.status = "fixed Content-Length"
			}
		}
		return m, nil
	case ActEditorReplaceBody:
		if !m.injecting {
			text, err := m.editBuffer.ReplaceBody(m.ta.Value())
			if err != nil {
				m.status = "replace body: " + err.Error()
			} else {
				m.ta.SetValue(text)
				m.ta.CursorEnd()
				m.status = "Enter the replacement body"
			}
		}
		return m, nil
	case ActEditorCancel:
		m.pauseEditDraft = nil
		m.editing = false
		m.injecting = false
		m.ta.Blur()
		if m.paused != nil {
			m.status = "Editor closed; message remains held"
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
	if draft := m.pauseEditDraft; draft != nil && draft.token == m.pausedToken {
		m.ta, m.editBuffer = draft.ta, draft.buffer
		m.pauseEditDraft = nil
		m.editing = true
		m.sizeEditor()
		return
	}
	var raw []byte
	if m.pausedMsg != nil {
		raw = m.pausedMsg.Raw
	}
	httpMessage := m.paused != nil && m.paused.Protocol == capture.ProtoHTTP1
	m.editBuffer = newMessageEditBuffer(raw, httpMessage)
	m.ta = newEditor()
	m.ta.SetValue(m.editBuffer.Text())
	m.sizeEditor()
	m.ta.Focus()
	m.editing = true
	m.status = "Editing held message"
}

// enqueuePause preserves the active pause until the operator resolves it.
// Later notifications wait in arrival order and cannot replace an open editor.
func (m *Model) enqueuePause(item Paused) {
	if m.paused != nil {
		m.pauseQueue = append(m.pauseQueue, item)
		return
	}
	m.activatePause(item)
}

func (m *Model) activatePause(item Paused) {
	m.paused = item.Flow
	m.pausedMsg = item.Msg
	m.pausedToken = item.Token
	// Notifications never take ownership of input or discard pending text.
}

// completePause clears editor state before promoting the oldest queued pause.
// Clearing the textarea is part of the invariant: edits belong only to the
// pause that seeded them and never become the next pause's payload.
func (m *Model) completePause(doneStatus string) {
	m.paused = nil
	m.pausedMsg = nil
	m.pausedToken = 0
	m.pauseEditDraft = nil
	m.editing = false
	m.ta.Blur()
	m.ta.SetValue("")
	m.editBuffer = messageEditBuffer{}
	if m.pauseReturn != nil {
		m.restoreForeground()
	}

	if len(m.pauseQueue) == 0 {
		m.status = doneStatus
		m.sizeOverlays()
		return
	}
	next := m.pauseQueue[0]
	m.pauseQueue[0] = Paused{}
	m.pauseQueue = m.pauseQueue[1:]
	m.activatePause(next)
	m.status = doneStatus
	m.sizeOverlays()
}

func (m *Model) sizeEditor() {
	m.ta.SetWidth(max(1, m.rightPaneWidth()-2))
	m.ta.SetHeight(max(1, m.paneContentHeight()-4))
	m.fi.Width = max(1, m.rightPaneWidth()-4)
	m.ci.Width = max(1, m.rightPaneWidth()-4)
}

// paneBoxWidths returns the on-screen widths of the two pane boxes, border
// included, split by splitRatio and summing to EXACTLY m.width. lipgloss draws
// a border column on both sides of both panes — four columns in all — so the
// two boxes, not their content, are what must fit m.width. Charging only two
// border columns (the old leftPaneWidth + rightPaneWidth == m.width-2) made the
// joined row m.width+2 wide, wrapping every pane line and sliding the layout —
// and every mouse row — down the screen. mouse hit-testing (paneRects) reads
// the same split, so clicks land on the row actually drawn.
func (m Model) paneBoxWidths() (left, right int) {
	left = int(float64(m.width) * m.splitRatio)
	// Keep a border plus at least one content column in each pane at any
	// ratio/width, so neither box collapses into its own borders.
	if left < 3 {
		left = 3
	}
	if left > m.width-3 {
		left = m.width - 3
	}
	return left, m.width - left
}

// leftPaneWidth is the content width handed to lipgloss .Width() for the left
// pane; the border adds one column on each side, so the box is leftPaneWidth+2.
// At a 0.5 ratio on an even width this is m.width/2 - 2.
func (m Model) leftPaneWidth() int {
	l, _ := m.paneBoxWidths()
	return l - 2
}

// rightPaneWidth is the content width for the right pane. leftPaneWidth and
// rightPaneWidth each map to a box of width value+2, and the two boxes sum to
// exactly m.width, so the joined row never overflows the terminal.
func (m Model) rightPaneWidth() int {
	_, r := m.paneBoxWidths()
	return r - 2
}

// leftSize is the terminal grid size for the left pane's content area. The PTY,
// the emulator, and the render call must all use it so the target draws for the
// exact grid we display.
func (m Model) leftSize() (cols, rows int) {
	paneW := m.leftPaneWidth()
	paneH := m.paneContentHeight()
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
	m.sizeOverlays()
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
	if m.width < 60 || m.height < 16 {
		return fitBlock("Resize terminal to at least 60×16\nQuit: "+m.leaderHint(ActQuit), m.width, m.height)
	}
	if m.showHelp && !m.modalSuspended {
		h := m.height - m.chromeHeight()
		return fitBlock(lipgloss.Place(m.width, h, lipgloss.Center, lipgloss.Center,
			m.helpViewAt(h, m.helpScroll)), m.width, h) + "\n" + m.globalChrome()
	}
	if m.repeating && !m.modalSuspended {
		return m.repeaterView()
	}
	leftW := m.leftPaneWidth()
	rightW := m.rightPaneWidth()
	paneH := m.paneContentHeight()

	lw, lh := m.leftSize()
	terminalContent := ""
	if m.screen != nil {
		terminalContent = m.screen.Render(lw, lh)
	}
	left := paneStyle(m.keyboardOwner() == "terminal").Width(leftW).Height(paneH).
		Render(fitBlock(terminalContent, leftW, paneH))

	rightContent := m.renderTraffic(rightW-2, paneH-1)
	switch {
	case m.editing:
		rightContent = m.renderEditor()
	case m.viewing:
		rightContent = m.renderDetail()
	}
	if rightW < 20 {
		rightContent = "Pane too narrow\n" + m.leaderHint(ActSplitShrink) + " / " + m.leaderHint(ActSplitGrow)
	}
	right := paneStyle(m.keyboardOwner() != "terminal").Width(rightW).Height(paneH).
		Render(fitBlock(rightContent, rightW, paneH))

	body := lipgloss.JoinHorizontal(lipgloss.Top, left, right)
	bar := m.globalChrome()
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
	// Pending vim motion (a count like "12" or a lone "g" awaiting the second).
	if m.count > 0 {
		header += fmt.Sprintf("  %d", m.count)
	} else if m.pendingG {
		header += "  g"
	}
	b.WriteString(truncate(titleStyle.Render(header), w) + "\n")

	// Reserve lines for the header, the optional filter line, and the paused
	// prompt, so the scrollable list gets the remaining rows. trafficRowLayout
	// is the same accounting clickedFlowIndex uses to map a mouse click back
	// onto a row, so the two can't drift apart.
	filterLineShown := m.filtering || m.fi.Value() != ""
	if filterLineShown {
		b.WriteString(truncate(m.fi.View(), w) + "\n")
	}
	if m.cmdline {
		b.WriteString(truncate(m.ci.View(), w) + "\n")
	}
	_, listRows := trafficRowLayout(h, filterLineShown, m.cmdline, m.paused != nil)

	// Slide the window so the selection stays visible (the missing scroll).
	sel := clampIndex(m.selected, len(vis))
	start := 0
	if sel >= listRows {
		start = sel - listRows + 1
	}
	if len(vis) == 0 {
		b.WriteString(fitBlock(m.emptyTraffic(), w, listRows))
		if listRows > 0 {
			b.WriteByte('\n')
		}
	} else {
		for row := range listRows {
			if i := start + row; i < len(vis) {
				b.WriteString(m.renderFlowRow(vis[i], i == sel, w))
			}
			b.WriteByte('\n')
		}
	}
	if len(vis) > 0 {
		b.WriteString(truncate(dimStyle.Render(selectedFlowSummary(vis[sel])), w))
	}
	b.WriteByte('\n')

	if m.paused != nil {
		b.WriteString(truncate(pendingStyle.Render("Held #"+m.paused.ID+" "+oneLine(m.paused.Title())), w))
		b.WriteString("\n" + truncate(m.keyHint(ctxTraffic, ActPausedEdit)+" edit · "+m.keyHint(ctxTraffic, ActPausedFwd)+" forward · "+m.keyHint(ctxTraffic, ActPausedDrop)+" drop", w))
	}
	return b.String()
}
func (m Model) emptyTraffic() string {
	if len(m.flows) == 0 {
		return "No captured traffic (0 flows)\n" + m.leaderHint(ActPaneSwitch) + " use terminal"
	}
	if m.flaggedOnly {
		hasFlags := false
		for _, f := range m.flows {
			if f.Flagged {
				hasFlags = true
				break
			}
		}
		if !hasFlags {
			return fmt.Sprintf("No flagged flows (0/%d)\n%s all flows\n%s flag selected", len(m.flows), m.keyHint(ctxTraffic, ActFlaggedOnly), m.keyHint(ctxTraffic, ActFlowFlag))
		}
	}
	return fmt.Sprintf("No filter matches (0/%d)\nFilter: %s\n%s edit filter; Esc clears it", len(m.flows), oneLine(m.fi.Value()), m.keyHint(ctxTraffic, ActFilterOpen))
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
		title += " " + oneLine(m.viewFlow.Title())
	}
	mode := "expanded"
	if !m.detailExpanded {
		mode = "raw"
	}
	title = "Detail [" + mode + "]"
	if m.viewFlow != nil {
		title += " " + oneLine(m.viewFlow.Title())
	}
	position := fmt.Sprintf("%d%% %d/%d · %s full", int(m.vp.ScrollPercent()*100), min(m.vp.YOffset+1, m.vp.TotalLineCount()), m.vp.TotalLineCount(), m.leaderHint(ActSessionSave))
	controls := m.keyHint(ctxDetail, ActDetailClose) + " back · " + m.keyHint(ctxDetail, ActDetailSave) + " txt · " + m.keyHint(ctxDetail, ActDetailFormat) + " mode"
	return truncate(titleStyle.Render(title), m.vp.Width) + "\n" + m.vp.View() + "\n" +
		truncate(position, m.vp.Width) + "\n" + truncate(controls, m.vp.Width)
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
	w := max(1, m.rightPaneWidth()-2)
	notice := m.editBuffer.Notice()
	if notice != "" {
		notice = m.keyHint(ctxEditor, ActEditorReplaceBody) + " replace body · " + notice
	}
	if m.injecting {
		notice = "Text frame · " + m.injectDir.String()
	}
	controls := m.keyHint(ctxEditor, ActEditorSend) + " send · " + m.keyHint(ctxEditor, ActEditorCancel) + " back"
	if !m.injecting {
		controls += " · " + m.keyHint(ctxEditor, ActEditorFixLen) + " len"
	}
	return truncate(titleStyle.Render(oneLine(m.renderEditorTitle())), w) + "\n" +
		m.ta.View() + "\n" + truncate(notice, w) + "\n" + truncate(controls, w)
}

// truncate measures rendered terminal cells and preserves complete ANSI sequences.
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	return ansi.Truncate(s, n, "…")
}

// Package tui renders a gateway's live event stream and captures the operator's
// input, driving it through the narrow Backend interface. It is the terminal
// gateway's frontend: the daemon hub treats that gateway like any other; the TUI
// is what makes it a human-usable pane. Depending on Backend (not the concrete
// gateway) keeps this package importable by the gateway without a cycle.
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

// RoutedEvent is a turn event tagged with the conversation (session channel) it
// belongs to, so the TUI can route it to the right tab.
type RoutedEvent struct {
	Conv  contracts.Conversation
	Event contracts.Event
}

// Backend is the narrow view of the terminal gateway the TUI drives: it reads
// routed outbound events to render, submits the lines the operator types into a
// specific channel, enumerates the hub's sessions for tab labels, and dispatches
// operator slash-commands to the hub. Taking an interface keeps this package
// free of any dependency on the terminal plugin.
type Backend interface {
	Frontend() <-chan RoutedEvent
	Submit(channel, text string)
	Sessions() []contracts.SessionInfo
	Dispatch(args []string) (string, error)
	// Close tears a session down by name through the typed control seam, so the
	// UI's close action never assembles "session close" flag argv.
	Close(name string, force bool) (string, error)
}

var (
	humanStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	statusStyle = lipgloss.NewStyle().Faint(true)
	replyStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	costStyle   = lipgloss.NewStyle().Faint(true)
)

// tab is one session's pane: its transcript, unread flag, busy state, last cost,
// and a disconnected flag set when the last event was an "abandoned" turn.
type tab struct {
	channel      string
	label        string
	lines        []string
	unread       bool
	busy         bool
	lastCost     float64
	disconnected bool
}

// maxTabLines bounds a tab's transcript so a long-lived or chatty session
// (streaming chunk events token-by-token) cannot grow memory without limit.
const maxTabLines = 5000

// appendLine adds a rendered line to the tab, dropping the oldest lines once the
// transcript exceeds maxTabLines. Trimming reallocates so the backing array does
// not pin the whole history in memory.
func (tb *tab) appendLine(s string) {
	tb.lines = append(tb.lines, s)
	if len(tb.lines) > maxTabLines {
		tb.lines = append([]string(nil), tb.lines[len(tb.lines)-maxTabLines:]...)
	}
}

// dispatchResultMsg carries the outcome of a slash-command run off the Update
// goroutine back into it, so a slow command never freezes the TUI.
type dispatchResultMsg struct {
	origin string
	out    string
	err    error
}

type eventMsg RoutedEvent

// tickMsg fires on a periodic timer so the TUI refreshes tabs from the hub.
type tickMsg struct{}

// tickCmd returns a command that fires tickMsg after ~1 second.
func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return tickMsg{} })
}

type model struct {
	tm           Backend
	vp           viewport.Model
	input        textinput.Model
	tabs         map[string]*tab
	order        []string
	active       string
	ready        bool
	showHelp     bool
	pendingClose bool
	width        int
	height       int
	flash        string // transient status shown when there is no active tab
	tabSig       string // last reconciled session signature, to skip idle work
}

// chromeHeight is the number of non-viewport rows View renders (tab bar, footer,
// input, spacers) plus the help block when it is shown.
func (m *model) chromeHeight() int {
	h := 5
	if m.showHelp {
		h += 3
	}
	return h
}

// applySize fits the viewport to the current window minus the chrome.
func (m *model) applySize() {
	if !m.ready {
		return
	}
	m.vp.Width = m.width
	m.vp.Height = m.height - m.chromeHeight()
}

func newModel(tm Backend) *model {
	in := textinput.New()
	in.Placeholder = "type a message…"
	in.Focus()
	return &model{tm: tm, input: in, tabs: map[string]*tab{}}
}

// ensureTab creates a tab for channel if missing, making the first tab active.
func (m *model) ensureTab(channel string) *tab {
	if tb, ok := m.tabs[channel]; ok {
		return tb
	}
	tb := &tab{channel: channel, label: channel}
	m.tabs[channel] = tb
	m.order = append(m.order, channel)
	if m.active == "" {
		m.active = channel
	}
	return tb
}

// removeTab drops a tab and fixes the active selection.
func (m *model) removeTab(channel string) {
	if _, ok := m.tabs[channel]; !ok {
		return
	}
	delete(m.tabs, channel)
	out := m.order[:0]
	for _, ch := range m.order {
		if ch != channel {
			out = append(out, ch)
		}
	}
	m.order = out
	if m.active == channel {
		m.active = ""
		if len(m.order) > 0 {
			m.active = m.order[0]
		}
		m.syncViewport()
	}
}

// route delivers a routed event to its tab, marking inactive tabs unread.
func (m *model) route(re RoutedEvent) {
	if re.Event.T == "closed" {
		m.removeTab(re.Conv.ID)
		return
	}
	tb := m.ensureTab(re.Conv.ID)
	before := len(tb.lines)
	m.renderInto(tb, re.Event)
	if len(tb.lines) != before && tb.channel != m.active {
		tb.unread = true
	}
	if tb.channel == m.active {
		m.syncViewport()
	}
}

// syncTabs reconciles tabs against the hub's session list: it creates tabs for
// new sessions, labels them by name, and drops tabs whose session is gone.
func (m *model) syncTabs() {
	infos := m.tm.Sessions()
	if infos == nil {
		return
	}
	var sb strings.Builder
	for _, s := range infos {
		sb.WriteString(s.ChannelID)
		sb.WriteByte('\x1f')
		sb.WriteString(s.Name)
		sb.WriteByte('\n')
	}
	sig := sb.String()
	if sig == m.tabSig {
		return // session set unchanged since last reconcile
	}
	m.tabSig = sig
	live := map[string]bool{}
	for _, s := range infos {
		live[s.ChannelID] = true
		tb := m.ensureTab(s.ChannelID)
		if s.Name != "" {
			tb.label = s.Name
		}
	}
	for _, ch := range append([]string(nil), m.order...) {
		if !live[ch] {
			m.removeTab(ch)
		}
	}
}

// handleEnter dispatches a /command or submits a prompt to the active tab. A
// slash command runs in a returned tea.Cmd (off the Update goroutine) so a slow
// dispatch — e.g. a clone against an unreachable host — never freezes the TUI;
// its result is delivered back as a dispatchResultMsg.
func (m *model) handleEnter() tea.Cmd {
	text := strings.TrimSpace(m.input.Value())
	if text == "" {
		return nil
	}
	m.input.Reset()
	if strings.HasPrefix(text, "/") {
		args := strings.Fields(strings.TrimPrefix(text, "/"))
		return m.dispatchCmd(m.active, args)
	}
	if m.active == "" {
		return nil
	}
	m.tm.Submit(m.active, text)
	m.tabs[m.active].appendLine(humanStyle.Render("you ") + text)
	m.syncViewport()
	return nil
}

// dispatchCmd runs an operator argv against the backend off the Update goroutine,
// tagging the result with the tab it was issued from.
func (m *model) dispatchCmd(origin string, args []string) tea.Cmd {
	tm := m.tm
	return func() tea.Msg {
		out, err := tm.Dispatch(args)
		return dispatchResultMsg{origin: origin, out: out, err: err}
	}
}

// toggleHelp flips the help overlay on/off, resizing the viewport so the help
// block does not push the chrome off-screen.
func (m *model) toggleHelp() {
	m.showHelp = !m.showHelp
	m.applySize()
	m.syncViewport()
}

// sessionName resolves a channel id to its logical session name via the hub,
// returning "" when the session is not (yet) known.
func (m *model) sessionName(channel string) string {
	for _, s := range m.tm.Sessions() {
		if s.ChannelID == channel && s.Name != "" {
			return s.Name
		}
	}
	return ""
}

// confirmClose dispatches a close for the active tab's session, resolving the
// real session name (the tab label can still be the channel id before the first
// reconcile) and surfacing any error through dispatchResultMsg.
func (m *model) confirmClose() tea.Cmd {
	tb := m.tabs[m.active]
	if tb == nil {
		return nil
	}
	name := m.sessionName(tb.channel)
	if name == "" {
		name = tb.label
	}
	origin, tm := m.active, m.tm
	return func() tea.Msg {
		out, err := tm.Close(name, false)
		return dispatchResultMsg{origin: origin, out: out, err: err}
	}
}

func (m *model) switchTab(delta int) {
	if len(m.order) == 0 {
		return
	}
	idx := 0
	for i, ch := range m.order {
		if ch == m.active {
			idx = i
			break
		}
	}
	idx = (idx + delta + len(m.order)) % len(m.order)
	m.active = m.order[idx]
	m.tabs[m.active].unread = false
	m.syncViewport()
	m.vp.GotoBottom() // a freshly switched-to tab starts at its latest output
}

func (m *model) renderInto(tb *tab, e contracts.Event) {
	// Any non-abandoned event clears the disconnected marker.
	if e.T != "abandoned" {
		tb.disconnected = false
	}
	switch e.T {
	case "chunk":
		tb.busy = true
		tb.appendLine(e.Text)
	case "status":
		tb.busy = true
		tb.appendLine(statusStyle.Render("· " + e.Text))
	case "reply":
		if e.Done {
			tb.busy = false
			if e.Cost > 0 {
				tb.lastCost = e.Cost
			}
		}
		if e.Text != "" {
			tb.appendLine(replyStyle.Render(e.Text))
		}
		if e.Cost > 0 {
			tb.appendLine(costStyle.Render(formatCost(e.Cost)))
		}
	case "reset":
		tb.appendLine(statusStyle.Render("· (turn reset)"))
	case "abandoned":
		tb.disconnected = true
		tb.appendLine(statusStyle.Render("· (turn abandoned)"))
	}
}

func (m *model) syncViewport() {
	if !m.ready {
		return
	}
	tb := m.tabs[m.active]
	if tb == nil {
		m.vp.SetContent("")
		return
	}
	// Preserve the operator's scroll position: only snap to the bottom when the
	// view is already there, so streaming output into the active tab does not
	// defeat PgUp/PgDn scrollback mid-turn.
	atBottom := m.vp.AtBottom()
	m.vp.SetContent(strings.Join(tb.lines, "\n"))
	if atBottom {
		m.vp.GotoBottom()
	}
}

// tabBar renders the tab strip: active tab highlighted, unread marked with •, busy with ⟳.
func (m *model) tabBar() string {
	var b strings.Builder
	for _, ch := range m.order {
		tb := m.tabs[ch]
		name := tb.label
		if tb.unread {
			name = "•" + name
		}
		if tb.busy {
			name = "⟳" + name
		}
		if ch == m.active {
			b.WriteString(humanStyle.Render("[" + name + "] "))
		} else {
			b.WriteString(statusStyle.Render(" " + name + " "))
		}
	}
	return b.String()
}

// footer renders the status/cost line for the active tab.
func (m *model) footer() string {
	tb := m.tabs[m.active]
	if tb == nil {
		return ""
	}
	if tb.disconnected {
		return statusStyle.Render("· disconnected")
	}
	state := statusStyle.Render("· idle")
	if tb.busy {
		state = humanStyle.Render("⟳ working")
	}
	cost := ""
	if tb.lastCost > 0 {
		cost = "  " + costStyle.Render("last "+formatCost(tb.lastCost))
	}
	return state + cost
}

// formatCost renders a turn's USD cost, matching the host progress summary:
// sub-cent costs get four decimals, larger ones two.
func formatCost(c float64) string {
	if c < 0.01 {
		return fmt.Sprintf("$%.4f", c)
	}
	return fmt.Sprintf("$%.2f", c)
}

// Run starts the TUI bound to the given gateway backend, blocking until the user
// quits; quitting cancels ctx (wired by the caller) so the daemon shuts down
// cleanly.
func Run(ctx context.Context, cancel context.CancelFunc, tm Backend) error {
	m := newModel(tm)
	p := tea.NewProgram(m, tea.WithAltScreen())
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case e, ok := <-tm.Frontend():
				if !ok {
					return
				}
				p.Send(eventMsg(e))
			}
		}
	}()
	_, err := p.Run()
	cancel()
	return err
}

func (m *model) Init() tea.Cmd { return tea.Batch(textinput.Blink, tickCmd()) }

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if !m.ready {
			m.vp = viewport.New(msg.Width, msg.Height-m.chromeHeight())
			m.ready = true
		} else {
			m.applySize()
		}
		m.input.Width = msg.Width - 2
		m.syncViewport()
	case tea.KeyMsg:
		m.flash = "" // any keypress clears a transient status
		// Two-step close confirm: if waiting for confirmation, next key decides.
		if m.pendingClose {
			if msg.String() == "y" {
				cmd := m.confirmClose()
				m.pendingClose = false
				return m, cmd
			}
			// Quit keys always quit, even during pending close.
			if msg.Type == tea.KeyCtrlC || msg.Type == tea.KeyEsc {
				m.pendingClose = false
				return m, tea.Quit
			}
			// Any other key cancels the close.
			m.pendingClose = false
			return m, nil
		}
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			return m, tea.Quit
		case tea.KeyTab:
			m.switchTab(1)
			return m, nil
		case tea.KeyShiftTab:
			m.switchTab(-1)
			return m, nil
		case tea.KeyEnter:
			return m, m.handleEnter()
		case tea.KeyCtrlW:
			m.pendingClose = true
			return m, nil
		case tea.KeyRunes:
			if msg.String() == "?" && m.input.Value() == "" {
				m.toggleHelp()
				return m, nil
			}
		}
		// PgUp/PgDn reach m.vp.Update(msg) below — not intercepted here.
	case eventMsg:
		m.route(RoutedEvent(msg))
	case dispatchResultMsg:
		m.syncTabs()
		line := msg.out
		if msg.err != nil {
			line = "error: " + msg.err.Error()
		}
		if line != "" {
			tb := m.tabs[msg.origin]
			if tb == nil {
				tb = m.tabs[m.active]
			}
			if tb != nil {
				tb.appendLine(statusStyle.Render("· " + line))
				m.syncViewport()
			} else {
				m.flash = line // no tab to render into — surface it standalone
			}
		}
		return m, nil
	case tickMsg:
		m.syncTabs()
		return m, tickCmd()
	}
	var cmds []tea.Cmd
	var c tea.Cmd
	m.input, c = m.input.Update(msg)
	cmds = append(cmds, c)
	m.vp, c = m.vp.Update(msg)
	cmds = append(cmds, c)
	return m, tea.Batch(cmds...)
}

// helpView returns the key-binding cheat-sheet rendered as a faint status block.
func (m *model) helpView() string {
	lines := []string{
		"Keys:  Tab / Shift+Tab  switch tab        Ctrl+W  close tab (y to confirm)",
		"       PgUp / PgDn      scroll             ?       toggle this help",
		"       Enter            submit / /cmd      Ctrl+C / Esc  quit",
	}
	return statusStyle.Render(strings.Join(lines, "\n"))
}

func (m *model) View() string {
	if !m.ready {
		return "starting…"
	}
	var parts []string
	if m.showHelp {
		parts = append(parts, m.helpView())
	}
	footer := m.footer()
	if m.flash != "" {
		footer = statusStyle.Render("· " + m.flash)
	}
	parts = append(parts, m.tabBar(), m.vp.View(), footer, m.input.View())
	return strings.Join(parts, "\n")
}

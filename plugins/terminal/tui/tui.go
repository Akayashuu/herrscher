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

// handleEnter dispatches a /command or submits a prompt to the active tab.
func (m *model) handleEnter() {
	text := strings.TrimSpace(m.input.Value())
	if text == "" {
		return
	}
	m.input.Reset()
	if strings.HasPrefix(text, "/") {
		args := strings.Fields(strings.TrimPrefix(text, "/"))
		out, err := m.tm.Dispatch(args)
		m.syncTabs()
		tb := m.tabs[m.active]
		if tb != nil {
			line := out
			if err != nil {
				line = "error: " + err.Error()
			}
			tb.lines = append(tb.lines, statusStyle.Render("· "+line))
			m.syncViewport()
		}
		return
	}
	if m.active == "" {
		return
	}
	m.tm.Submit(m.active, text)
	tb := m.tabs[m.active]
	tb.lines = append(tb.lines, humanStyle.Render("you ")+text)
	m.syncViewport()
}

// toggleHelp flips the help overlay on/off.
func (m *model) toggleHelp() {
	m.showHelp = !m.showHelp
}

// confirmClose dispatches a close for the active tab's session by label (name).
func (m *model) confirmClose() {
	tb := m.tabs[m.active]
	if tb == nil || tb.label == "" {
		return
	}
	_, _ = m.tm.Dispatch([]string{"session", "close", "--name", tb.label})
	m.syncTabs()
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
}

func (m *model) renderInto(tb *tab, e contracts.Event) {
	// Any non-abandoned event clears the disconnected marker.
	if e.T != "abandoned" {
		tb.disconnected = false
	}
	switch e.T {
	case "chunk":
		tb.busy = true
		tb.lines = append(tb.lines, e.Text)
	case "status":
		tb.busy = true
		tb.lines = append(tb.lines, statusStyle.Render("· "+e.Text))
	case "reply":
		if e.Done {
			tb.busy = false
			if e.Cost > 0 {
				tb.lastCost = e.Cost
			}
		}
		if e.Text != "" {
			tb.lines = append(tb.lines, replyStyle.Render(e.Text))
		}
		if e.Cost > 0 {
			tb.lines = append(tb.lines, costStyle.Render(formatCost(e.Cost)))
		}
	case "reset":
		tb.lines = append(tb.lines, statusStyle.Render("· (turn reset)"))
	case "abandoned":
		tb.disconnected = true
		tb.lines = append(tb.lines, statusStyle.Render("· (turn abandoned)"))
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
	m.vp.SetContent(strings.Join(tb.lines, "\n"))
	m.vp.GotoBottom()
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
		if !m.ready {
			m.vp = viewport.New(msg.Width, msg.Height-5)
			m.ready = true
			m.syncViewport()
		} else {
			m.vp.Width = msg.Width
			m.vp.Height = msg.Height - 5
			m.syncViewport()
		}
		m.input.Width = msg.Width - 2
	case tea.KeyMsg:
		// Two-step close confirm: if waiting for confirmation, next key decides.
		if m.pendingClose {
			if msg.String() == "y" {
				m.confirmClose()
			}
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
			m.handleEnter()
		case tea.KeyCtrlW:
			m.pendingClose = true
			return m, nil
		case tea.KeyRunes:
			if msg.String() == "?" {
				m.toggleHelp()
				return m, nil
			}
		}
		// PgUp/PgDn reach m.vp.Update(msg) below — not intercepted here.
	case eventMsg:
		m.route(RoutedEvent(msg))
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
	parts = append(parts, m.tabBar(), m.vp.View(), m.footer(), m.input.View())
	return strings.Join(parts, "\n")
}

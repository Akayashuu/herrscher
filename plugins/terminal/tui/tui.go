// Package tui renders the terminal gateway's live event stream and captures the
// operator's input, driving the active terminal gateway. It is the gateway's
// frontend: the daemon hub treats the terminal gateway like any other; the TUI
// is what makes that gateway a human-usable pane.
package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher/plugins/terminal"
)

var (
	humanStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	statusStyle = lipgloss.NewStyle().Faint(true)
	replyStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
)

// eventMsg wraps a gateway event for the Bubbletea update loop.
type eventMsg contracts.Event

type model struct {
	tm    *terminal.Terminal
	vp    viewport.Model
	input textinput.Model
	lines []string
	ready bool
}

// Run starts the TUI bound to the active terminal gateway, blocking until the
// user quits; quitting cancels ctx (wired by the caller) so the daemon shuts
// down cleanly. Returns nil if no terminal gateway was instantiated.
func Run(ctx context.Context, cancel context.CancelFunc) error {
	tm := terminal.Active()
	if tm == nil {
		return nil
	}
	in := textinput.New()
	in.Placeholder = "type a message…"
	in.Focus()
	m := model{tm: tm, input: in}
	p := tea.NewProgram(m, tea.WithAltScreen())

	// Forward gateway events into the program.
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
	cancel() // quitting the TUI tears the daemon down
	return err
}

func (m model) Init() tea.Cmd { return textinput.Blink }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if !m.ready {
			m.vp = viewport.New(msg.Width, msg.Height-3)
			m.ready = true
		} else {
			m.vp.Width = msg.Width
			m.vp.Height = msg.Height - 3
		}
		m.input.Width = msg.Width - 2
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			return m, tea.Quit
		case tea.KeyEnter:
			text := strings.TrimSpace(m.input.Value())
			if text != "" {
				m.tm.Submit(text)
				m.append(humanStyle.Render("you ") + text)
				m.input.Reset()
			}
		}
	case eventMsg:
		m.renderEvent(contracts.Event(msg))
	}
	var cmds []tea.Cmd
	var c tea.Cmd
	m.input, c = m.input.Update(msg)
	cmds = append(cmds, c)
	m.vp, c = m.vp.Update(msg)
	cmds = append(cmds, c)
	return m, tea.Batch(cmds...)
}

func (m *model) renderEvent(e contracts.Event) {
	switch e.T {
	case "chunk":
		m.append(e.Text)
	case "status":
		m.append(statusStyle.Render("· " + e.Text))
	case "reply":
		if e.Text != "" {
			m.append(replyStyle.Render(e.Text))
		}
	case "reset":
		m.append(statusStyle.Render("· (turn reset)"))
	}
}

func (m *model) append(line string) {
	m.lines = append(m.lines, line)
	if m.ready {
		m.vp.SetContent(strings.Join(m.lines, "\n"))
		m.vp.GotoBottom()
	}
}

func (m model) View() string {
	if !m.ready {
		return "starting…"
	}
	return fmt.Sprintf("%s\n%s", m.vp.View(), m.input.View())
}

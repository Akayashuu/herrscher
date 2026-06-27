// Package terminal is the terminal gateway plugin: a chat gateway whose
// "channel" is the local TUI. It self-registers like any gateway (init →
// contracts.Register) and implements Gateway + ChannelReader + EventSink so the
// daemon hub drives it exactly like Discord — polling Read for typed lines and
// fanning the live event stream to it via Emit. The TUI binds to the active
// instance through Active().
package terminal

import (
	"context"
	"strconv"
	"sync"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher/plugins/terminal/tui"
)

// ChannelID is the fixed conversation id the terminal gateway uses (single
// local channel).
const ChannelID = "terminal"

func init() {
	contracts.Register(contracts.Plugin{
		Manifest: contracts.Manifest{
			Kind:         "terminal",
			Category:     contracts.CategoryGateway,
			Capabilities: contracts.Capabilities{Replies: true},
		},
		Gateway: newGatewaySet,
	})
}

func newGatewaySet(ctx context.Context, cfg contracts.PluginConfig) (contracts.GatewaySet, error) {
	tm := New()
	return contracts.GatewaySet{Gateway: tm, Reader: tm}, nil
}

// Terminal is the in-process terminal gateway. Typed lines arrive via Submit
// (tagged with the active channel) and are drained per-channel by the hub
// through Read; outbound events (EmitTo/Emit/Post/Reply) are forwarded to the
// TUI as RoutedEvents on Frontend.
type Terminal struct {
	mu      sync.Mutex
	pending map[string][]contracts.Message // channel id -> queued inbound lines
	nextID  int
	out     chan tui.RoutedEvent

	ctrlMu sync.Mutex
	ctrl   contracts.SessionControl // set by BindSessionControl (Task 6); nil-safe here
}

var (
	_ contracts.Gateway         = (*Terminal)(nil)
	_ contracts.ChannelReader   = (*Terminal)(nil)
	_ contracts.EventSink       = (*Terminal)(nil)
	_ contracts.RoutedEventSink = (*Terminal)(nil)
	_ contracts.Foreground      = (*Terminal)(nil)
)

// RunForeground satisfies contracts.Foreground: the terminal gateway owns the
// process's main thread by running its Bubbletea TUI, blocking until the user
// quits (which calls cancel to tear the daemon down) or ctx is cancelled. The
// composition root runs this for the one bound gateway that implements
// Foreground, on an interactive TTY only.
func (t *Terminal) RunForeground(ctx context.Context, cancel context.CancelFunc) error {
	return tui.Run(ctx, cancel, t)
}

// New builds an unbound terminal gateway.
func New() *Terminal {
	return &Terminal{pending: map[string][]contracts.Message{}, out: make(chan tui.RoutedEvent, 256)}
}

// Submit enqueues a line the user typed in the TUI as an inbound message on the
// given channel (the active tab's session channel).
func (t *Terminal) Submit(channel, text string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.nextID++
	t.pending[channel] = append(t.pending[channel], contracts.Message{
		ID:         "t" + strconv.Itoa(t.nextID),
		ChannelID:  channel,
		Content:    text,
		AuthorID:   "local",
		AuthorName: "you",
	})
}

// Frontend yields routed outbound events for the TUI to render.
func (t *Terminal) Frontend() <-chan tui.RoutedEvent { return t.out }

// --- contracts.ChannelReader ---

func (t *Terminal) Enabled() bool          { return true }
func (t *Terminal) DefaultChannel() string { return ChannelID }

func (t *Terminal) EnsureChannel(context.Context, string, string) (contracts.Channel, error) {
	return contracts.Channel{ID: ChannelID, Name: ChannelID}, nil
}

// Read drains and returns the lines queued for channelID since the last Read.
// The hub polls this per-session with the session's own channel, so each
// session drains only its own input.
func (t *Terminal) Read(_ context.Context, channelID string, _ int, _ string) ([]contracts.Message, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := t.pending[channelID]
	if len(out) == 0 {
		return nil, nil
	}
	delete(t.pending, channelID)
	return out, nil
}

func (t *Terminal) Unreact(context.Context, string, string, string) error { return nil }

func (t *Terminal) UpsertStatusMessage(_ context.Context, channelID, _, content string) (string, error) {
	t.EmitTo(contracts.Conversation{Gateway: "terminal", ID: channelID}, contracts.Event{T: "status", Text: content})
	return "", nil
}

// --- contracts.Gateway ---

func (t *Terminal) Manifest() contracts.Manifest {
	return contracts.Manifest{Kind: "terminal", Category: contracts.CategoryGateway}
}

func (t *Terminal) Post(_ context.Context, conv contracts.Conversation, text string) (contracts.MessageID, error) {
	t.EmitTo(conv, contracts.Event{T: "reply", Text: text, Done: true})
	return "", nil
}

func (t *Terminal) Reply(_ context.Context, conv contracts.Conversation, _ contracts.MessageID, text string) (contracts.MessageID, error) {
	t.EmitTo(conv, contracts.Event{T: "reply", Text: text, Done: true})
	return "", nil
}

func (t *Terminal) React(context.Context, contracts.Conversation, contracts.MessageID, string) error {
	return nil
}

func (t *Terminal) Menu(_ context.Context, conv contracts.Conversation, _ contracts.MessageID, prompt string, opts []contracts.Choice) error {
	t.EmitTo(conv, contracts.Event{T: "status", Text: prompt})
	return nil
}

// --- contracts.RoutedEventSink ---

// EmitTo routes a turn event to the conversation's tab in the TUI.
func (t *Terminal) EmitTo(conv contracts.Conversation, e contracts.Event) {
	t.emit(tui.RoutedEvent{Conv: conv, Event: e})
}

// --- contracts.EventSink ---

// Emit (legacy EventSink) routes to the default single channel. The hub calls
// this when it sees only EventSink; RoutedEventSink takes priority (Task 2).
func (t *Terminal) Emit(e contracts.Event) {
	t.emit(tui.RoutedEvent{Conv: contracts.Conversation{Gateway: "terminal", ID: ChannelID}, Event: e})
}

func (t *Terminal) emit(re tui.RoutedEvent) {
	select {
	case t.out <- re:
	default: // TUI not draining fast enough → drop rather than block the hub
	}
}

// Sessions returns the hub's sessions for tab labels (nil until SessionControl
// is bound — see BindSessionControl).
func (t *Terminal) Sessions() []contracts.SessionInfo {
	t.ctrlMu.Lock()
	c := t.ctrl
	t.ctrlMu.Unlock()
	if c == nil {
		return nil
	}
	return c.Sessions()
}

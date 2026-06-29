// Package terminal is the terminal gateway plugin: a chat gateway whose
// "channel" is the local TUI. It self-registers like any gateway (init →
// contracts.Register) and implements Gateway + ChannelReader + EventSink so the
// daemon hub drives it exactly like Discord — polling Read for typed lines and
// fanning the live event stream to it via Emit. The TUI binds to the active
// instance through Active().
package terminal

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

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
	return contracts.GatewaySet{Gateway: tm, Reader: tm, Admin: tm}, nil
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

	ctrlMu    sync.Mutex
	ctrl      contracts.SessionControl // set by BindSessionControl; nil-safe here
	ctrlReady chan struct{}            // closed once ctrl is bound
	bindOnce  sync.Once                // guards the close of ctrlReady

	baseCtx context.Context // the foreground lifetime; scopes operator dispatches
}

var (
	_ contracts.Gateway                = (*Terminal)(nil)
	_ contracts.ChannelReader          = (*Terminal)(nil)
	_ contracts.EventSink              = (*Terminal)(nil)
	_ contracts.RoutedEventSink        = (*Terminal)(nil)
	_ contracts.Foreground             = (*Terminal)(nil)
	_ contracts.ChannelAdmin           = (*Terminal)(nil)
	_ contracts.SessionControlReceiver = (*Terminal)(nil)
)

// ensureDefaultSession creates a default terminal-bound session when none is
// live yet, so a freshly launched TUI has a ready tab that replies immediately.
// It is a no-op when a session already bound to the terminal gateway exists.
func ensureDefaultSession(ctx context.Context, c contracts.SessionControl) error {
	for _, s := range c.Sessions() {
		for _, g := range s.Gateways {
			if g == "terminal" {
				return nil // a terminal session already exists
			}
		}
	}
	_, err := c.Create(ctx, contracts.CreateSession{Name: "main", TerminalOnly: true, Shared: true})
	return err
}

// bootstrapDefaultSession waits for the host to bind SessionControl (RunHub binds
// it from a background goroutine after the TUI may have started), then ensures a
// default session exists. It blocks on the ctrlReady signal rather than polling,
// so a slow bind is picked up the instant it lands. Never blocks forever: if the
// bind doesn't arrive within ~5 s, or ctx is cancelled, it returns silently so
// the TUI still launches. A failed bootstrap is best-effort and must not stop it.
func (t *Terminal) bootstrapDefaultSession(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(5 * time.Second):
		return
	case <-t.ctrlReady:
		if c := t.Control(); c != nil {
			_ = ensureDefaultSession(ctx, c) // best-effort; error silently ignored
		}
	}
}

// RunForeground satisfies contracts.Foreground: the terminal gateway owns the
// process's main thread by running its Bubbletea TUI, blocking until the user
// quits (which calls cancel to tear the daemon down) or ctx is cancelled. The
// composition root runs this for the one bound gateway that implements
// Foreground, on an interactive TTY only.
func (t *Terminal) RunForeground(ctx context.Context, cancel context.CancelFunc) error {
	t.ctrlMu.Lock()
	t.baseCtx = ctx
	t.ctrlMu.Unlock()
	t.bootstrapDefaultSession(ctx)
	return tui.Run(ctx, cancel, t)
}

// New builds an unbound terminal gateway.
func New() *Terminal {
	return &Terminal{
		pending:   map[string][]contracts.Message{},
		out:       make(chan tui.RoutedEvent, 256),
		ctrlReady: make(chan struct{}),
	}
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
	text := prompt
	for _, o := range opts {
		text += "\n  • " + o.Value + " — " + o.Label
	}
	t.EmitTo(conv, contracts.Event{T: "status", Text: text})
	return nil
}

// --- contracts.RoutedEventSink ---

// EmitTo routes a turn event to the conversation's tab in the TUI.
func (t *Terminal) EmitTo(conv contracts.Conversation, e contracts.Event) {
	t.emit(tui.RoutedEvent{Conv: conv, Event: e})
}

// --- contracts.EventSink ---

// Emit (legacy EventSink) routes to the default single channel. The hub calls
// this when it sees only EventSink; RoutedEventSink takes priority.
func (t *Terminal) Emit(e contracts.Event) {
	t.emit(tui.RoutedEvent{Conv: contracts.Conversation{Gateway: "terminal", ID: ChannelID}, Event: e})
}

func (t *Terminal) emit(re tui.RoutedEvent) {
	select {
	case t.out <- re:
		return
	default:
	}
	// High-volume chunk/status events are dropped rather than block the hub when
	// the TUI lags. A finished reply or a channel close carries terminal state
	// (clears the busy marker, removes the tab) the TUI must not miss, so wait
	// briefly for room before giving up.
	if re.Event.T == "closed" || (re.Event.T == "reply" && re.Event.Done) {
		select {
		case t.out <- re:
		case <-time.After(2 * time.Second):
		}
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

// BindSessionControl stores the hub controller so the TUI can drive the session
// lifecycle (create/close/list) and enumerate sessions for tab labels.
func (t *Terminal) BindSessionControl(c contracts.SessionControl) {
	t.ctrlMu.Lock()
	t.ctrl = c
	t.ctrlMu.Unlock()
	t.bindOnce.Do(func() { close(t.ctrlReady) })
}

// Control exposes the bound SessionControl to the TUI (nil before bind).
func (t *Terminal) Control() contracts.SessionControl {
	t.ctrlMu.Lock()
	defer t.ctrlMu.Unlock()
	return t.ctrl
}

// withTerminalDefault ensures that a "session create" command without an
// explicit gateway selector binds to the terminal, so TUI-created sessions
// always appear as tabs. An explicit --gateways or --terminal_only flag is
// respected and passed through unchanged.
func withTerminalDefault(args []string) []string {
	if len(args) < 2 || args[0] != "session" || args[1] != "create" {
		return args
	}
	for _, a := range args {
		if a == "--gateways" || strings.HasPrefix(a, "--gateways=") ||
			a == "--terminal_only" || strings.HasPrefix(a, "--terminal_only=") ||
			strings.HasPrefix(a, "terminal_only:") {
			return args
		}
	}
	out := make([]string, len(args), len(args)+1)
	copy(out, args)
	return append(out, "--terminal_only")
}

// terminalVerbs is the allow-list of command groups reachable from the TUI. The
// terminal drives session lifecycle and its companion agents; daemon-management
// verbs (service restart/update — which would tear down the host the TUI runs
// in — and set home/source, which rewrites its routing config) are deliberately
// out of reach, so a future destructive verb can never be typed into a tab.
var terminalVerbs = map[string]bool{"session": true, "agent": true}

// Dispatch forwards an operator argv to the bound SessionControl, prepending
// --terminal_only when creating a session without an explicit gateway selector
// so TUI-created sessions bind to this terminal and appear as tabs. The first
// token is gated against terminalVerbs before it ever reaches the registry.
func (t *Terminal) Dispatch(args []string) (string, error) {
	if len(args) == 0 || !terminalVerbs[args[0]] {
		verb := ""
		if len(args) > 0 {
			verb = args[0]
		}
		return "", fmt.Errorf("command %q is not available from the terminal (allowed: session, agent)", verb)
	}
	t.ctrlMu.Lock()
	c, ctx := t.ctrl, t.baseCtx
	t.ctrlMu.Unlock()
	if c == nil {
		return "", fmt.Errorf("session control not bound")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return c.Dispatch(ctx, withTerminalDefault(args))
}

// Close tears a session down through the typed SessionControl seam, so the TUI's
// close action never assembles "session close" flag argv.
func (t *Terminal) Close(name string, force bool) (string, error) {
	t.ctrlMu.Lock()
	c, ctx := t.ctrl, t.baseCtx
	t.ctrlMu.Unlock()
	if c == nil {
		return "", fmt.Errorf("session control not bound")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return c.Close(ctx, name, force)
}

// --- contracts.ChannelAdmin: synthetic, terminal-local channels ---

func (t *Terminal) Kind(_ context.Context, _ string) (string, error) { return "text", nil }

func (t *Terminal) CreateUnder(_ context.Context, _, name string) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.nextID++
	return "terminal/" + slug(name) + "-" + strconv.Itoa(t.nextID), nil
}

func (t *Terminal) ForumPost(ctx context.Context, parentID, name, _ string) (string, error) {
	return t.CreateUnder(ctx, parentID, name)
}

func (t *Terminal) Archive(_ context.Context, id string) error {
	t.EmitTo(contracts.Conversation{Gateway: "terminal", ID: id}, contracts.Event{T: "closed"})
	return nil
}

func (t *Terminal) Send(_ context.Context, channelID, content string) error {
	t.EmitTo(contracts.Conversation{Gateway: "terminal", ID: channelID}, contracts.Event{T: "status", Text: content})
	return nil
}

// ChannelRef renders a terminal channel id bare: a plain TUI has no mention
// markup, so the operator just sees the id.
func (t *Terminal) ChannelRef(id string) string { return id }

// slug lowercases and replaces unsafe runes so a channel id stays path-safe.
func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "session"
	}
	return out
}

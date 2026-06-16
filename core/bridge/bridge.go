// Package bridge implements the bridge loop: it watches a chat channel (via the
// injected platform port) for human messages and, for each, asks the injected
// backend port to answer, then posts the output back as a threaded reply. The
// loop is model-agnostic: it never knows which backend (Claude, …) responds.
package bridge

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher/core/internal/control"
	"github.com/Herrscherd/herrscher/core/internal/state"
)

// BackendFactory builds the model-edge backend for a resolved channel. It is
// injected so core stays free of any model-specific code: the binary supplies a
// factory closing over its chosen backend (e.g. claude.NewBackend). The channel
// id is passed because a backend may key its session/process on it, and the
// channel can be created inside Run.
type BackendFactory func(channelID string) (contracts.Backend, error)

// ErrDisabled is returned when the platform has no token / is not enabled.
var ErrDisabled = errors.New("dctl is disabled (no token)")

// discordMaxLen is the hard per-message character limit (Discord's 2000).
const discordMaxLen = 2000

// Reaction marks the bridge puts on a human message: ack on pickup, swapped for
// done/fail once the command finishes.
const (
	ackEmoji  = "👀"
	doneEmoji = "✅"
	failEmoji = "⚠️"
)

// Options configures one bridge run (parsed from CLI flags by the binary).
type Options struct {
	Channel       string
	Ensure        string
	Interval      int
	State         string
	After         string
	Participants  string // append-only journal of message authors (empty = disabled)
	AllowState    string // daemon state.json read per-message to enforce the allowlist (empty = no enforcement)
	Session       string // session name, used with AllowState to resolve the per-session allowlist
	Verbose       bool
	Progress      string // "off" | "actions" | "full" (default "full")
	ProgressKeep  bool   // keep the full running list instead of collapsing to a summary
	ControlSocket string // unix socket the daemon forwards select-menu clicks to (empty = numeric-reply fallback only)
}

// Run links the channel to the backend until ctx is cancelled. newBackend builds
// the model edge for the resolved channel, keeping core model-agnostic.
func Run(ctx context.Context, p contracts.ChannelReader, gw contracts.Gateway, newBackend BackendFactory, o Options) error {
	if !p.Enabled() {
		return ErrDisabled
	}

	switch o.Progress {
	case "", "off", "actions", "full":
	default:
		return fmt.Errorf("invalid --progress %q (want off|actions|full)", o.Progress)
	}
	if o.Progress == "" {
		o.Progress = "full"
	}

	// No channel configured anywhere → create (or reuse) a default one so the
	// bridge always has somewhere to talk.
	ch := o.Channel
	if ch == "" && p.DefaultChannel() == "" {
		created, err := p.EnsureChannel(ctx, "", o.Ensure)
		if err != nil {
			return fmt.Errorf("no channel set and could not create %q: %w", o.Ensure, err)
		}
		ch = created.ID
		logf(true, "no default channel — using #%s (%s)", created.Name, created.ID)
	}

	// The persisted state file is authoritative: a restart resumes exactly where
	// it left off, never replaying messages it already handled. --after only
	// seeds the very first run (before any state exists).
	var last string
	if o.State != "" {
		if b, err := os.ReadFile(o.State); err == nil {
			last = strings.TrimSpace(string(b))
		}
	}
	if last == "" {
		last = o.After
	}
	// No baseline yet → anchor on the latest message so we don't replay history.
	if last == "" {
		if msgs, err := p.Read(ctx, ch, 1, ""); err == nil && len(msgs) > 0 {
			last = msgs[len(msgs)-1].ID
		}
	}
	logf(o.Verbose, "bridge up: channel=%s interval=%ds last=%s", ch, o.Interval, last)

	// Outbound emission routes through the injected contracts.Gateway port (the
	// caller wraps the platform adapter in Degrade). gw/conv are stable for the
	// whole run: ch is fixed and gw does not depend on it.
	conv := contracts.Conversation{Gateway: "discord", ID: ch}

	resp, err := newBackend(ch)
	if err != nil {
		return fmt.Errorf("backend: %w", err)
	}
	defer resp.Close()

	// Control channel: when set (daemon mode), the daemon forwards select-menu
	// clicks here and the backend injects the picked value, posting whatever it
	// produces. Best-effort: a socket that fails to bind just disables the menu
	// path, leaving the numeric-reply fallback intact.
	if inj, ok := resp.(contracts.ChoiceInjector); ok && o.ControlSocket != "" {
		if srv, err := control.Listen(o.ControlSocket); err != nil {
			logf(true, "control socket %s: %v — select menus disabled", o.ControlSocket, err)
			o.ControlSocket = ""
		} else {
			defer srv.Close()
			go func() {
				for v := range srv.Values() {
					logf(o.Verbose, "choice pick %q", v)
					out, err := inj.InjectChoice(ctx, v)
					if err != nil {
						logf(true, "inject choice %q: %v", v, err)
					}
					if out = strings.TrimSpace(out); out != "" {
						postResult(ctx, p, gw, conv, "", out, resp, o)
					}
				}
			}()
		}
	}

	auth := &authorizer{o: o}
	// Authors already journaled this run; skip the dedup-read for repeats.
	seen := map[string]bool{}

	for {
		msgs, err := p.Read(ctx, ch, 100, last)
		if err != nil {
			logf(true, "read error: %v", err)
			time.Sleep(time.Duration(o.Interval) * time.Second)
			continue
		}
		for _, m := range msgs {
			last = m.ID
			persist(o.State, last)
			if m.AuthorBot {
				continue // never answer a bot (incl. ourselves) → no loops
			}
			if !seen[m.AuthorID] {
				seen[m.AuthorID] = true
				recordParticipant(o.Participants, m.AuthorID)
			}
			if !auth.allowed(m.AuthorID) {
				logf(o.Verbose, "skip <%s>: not on the allowlist for %q", m.AuthorName, o.Session)
				continue // unauthorized author → observed but never drives the session
			}
			logf(o.Verbose, "<%s> %s", m.AuthorName, oneline(m.Content))
			// Pull any image attachments down to local files so the backend can
			// reference them. Best-effort: a download failure never drops a turn.
			var atts []string
			if len(m.Attachments) > 0 {
				var derr error
				atts, derr = downloadImages(ctx, nil, m, attachmentDir(o.Session))
				if derr != nil {
					logf(o.Verbose, "attachment download error: %v", derr)
				}
			}
			// Acknowledge immediately so the human sees the message was picked
			// up while the (slow) command runs. Best-effort: ignore if the bot
			// lacks Add Reactions.
			_ = gw.React(ctx, conv, contracts.MessageID(m.ID), ackEmoji)

			var pv *progressView
			var onEvent func(contracts.BackendEvent)
			if o.Progress != "off" {
				post := func(id, content string) (string, error) {
					return p.UpsertStatusMessage(ctx, ch, id, content)
				}
				pv = newProgressView(post, o.Progress, o.ProgressKeep, time.Now())
				onEvent = pv.add
			}

			out, err := resp.Respond(ctx, contracts.Prompt{
				Content:     m.Content,
				Author:      m.AuthorName,
				MessageID:   m.ID,
				ChannelID:   m.ChannelID,
				Attachments: atts,
			}, onEvent)
			// The backend has read the files during the (now-finished) turn, so
			// they can go. Keeping them would slowly fill the temp dir.
			removeFiles(atts)
			if err != nil && out == "" {
				out = "⚠️ " + err.Error()
			}
			out = strings.TrimSpace(out)
			if out == "" {
				if pv != nil {
					pv.finish(true)
				}
				_ = p.Unreact(ctx, ch, m.ID, ackEmoji)
				_ = gw.React(ctx, conv, contracts.MessageID(m.ID), failEmoji)
				continue
			}
			postResult(ctx, p, gw, conv, m.ID, out, resp, o)
			if pv != nil {
				pv.finish(err != nil)
			}
			// Swap the "seen" mark for a "done" mark once the answer is posted.
			_ = p.Unreact(ctx, ch, m.ID, ackEmoji)
			_ = gw.React(ctx, conv, contracts.MessageID(m.ID), doneEmoji)
		}
		time.Sleep(time.Duration(o.Interval) * time.Second)
	}
}

// postResult delivers a turn's output to the channel. When the pane is left
// waiting on a choice prompt and a control socket is wired (daemon mode), it
// posts the rendered options as a native select menu whose clicks route back via
// ControlSocket; otherwise it chunks the text and replies (or sends, when there
// is no human message to thread under — e.g. output from an injected pick). A
// failed menu post degrades to the plain-text path so a turn is never dropped.
func postResult(ctx context.Context, p contracts.ChannelReader, gw contracts.Gateway, conv contracts.Conversation, replyTo, out string, resp contracts.Backend, o Options) {
	// Choice-menu emission goes through the MenuRouter capability: the plugin owns
	// the wire encoding and routes a pick back to the SESSION (not the channel) so
	// the daemon delivers it to this session's control socket. Routing it through
	// gw.Menu would key the menu on the channel id and break pick routing. A plugin
	// without MenuRouter (or a failed post) degrades to the plain-text path.
	if o.ControlSocket != "" {
		if mr, ok := p.(contracts.MenuRouter); ok {
			if ca, ok := resp.(contracts.ChoiceAware); ok {
				if pc, has := ca.PendingChoice(); has {
					choices := make([]contracts.Choice, 0, len(pc.Options))
					for _, it := range pc.Options {
						choices = append(choices, contracts.Choice{Label: it.Label, Value: it.Value})
					}
					if _, err := mr.RouteMenu(ctx, conv.ID, replyTo, out, o.Session, choices); err != nil {
						logf(true, "choice menu post error: %v — falling back to text", err)
					} else {
						return
					}
				}
			}
		}
	}
	postResultGW(ctx, gw, conv, replyTo, out)
}

// postResultGW emits the plain-text branch through the contracts.Gateway port: a
// reply when threading under a human message, or a post otherwise. The
// choice-menu branch is the caller's concern and is NOT handled here.
func postResultGW(ctx context.Context, gw contracts.Gateway, conv contracts.Conversation, replyTo, out string) {
	for _, part := range chunk(out, discordMaxLen) {
		var err error
		if replyTo != "" {
			_, err = gw.Reply(ctx, conv, contracts.MessageID(replyTo), part)
		} else {
			_, err = gw.Post(ctx, conv, part)
		}
		if err != nil {
			logf(true, "reply error: %v", err)
		}
	}
}

// removeFiles best-effort deletes downloaded attachment files once a turn is
// done, so the per-session temp dir doesn't grow without bound.
func removeFiles(paths []string) {
	for _, p := range paths {
		_ = os.Remove(p)
	}
}

func persist(path, id string) {
	if path == "" || id == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.WriteFile(path, []byte(id+"\n"), 0o644)
}

// recordParticipant best-effort appends a human author id to the journal so the
// daemon can answer /session who. Errors are swallowed: observability must never
// break the bridge loop.
func recordParticipant(path, userID string) {
	_, _ = state.AppendParticipant(path, userID)
}

// authorizer enforces the per-session allowlist (semantics B) for the bridge
// loop, caching the parsed daemon state and reloading only when state.json
// changes (mtime or size), so a busy channel doesn't re-read+parse the file on
// every message. When AllowState is empty the bridge runs unguarded (standalone
// use). Otherwise it is an access-control gate and fails CLOSED: an unreadable/
// corrupt/missing state file denies rather than silently dropping enforcement.
// Because saveLocked writes atomically (temp + rename), a changed allowlist
// always lands a new mtime/size, so /session allow changes apply without a
// restart. A missing session is not special-cased — SessionAllowed checks the
// global allowlist first, so a globally-allowed admin still passes even if the
// per-session entry is absent.
type authorizer struct {
	o       Options
	loaded  bool
	mod     time.Time
	size    int64
	st      *state.State
	loadErr error
}

func (a *authorizer) allowed(userID string) bool {
	if a.o.AllowState == "" {
		return true
	}
	fi, err := os.Stat(a.o.AllowState)
	if err != nil {
		logf(a.o.Verbose, "allowlist: cannot stat %s (%v) — denying", a.o.AllowState, err)
		return false
	}
	if !a.loaded || !fi.ModTime().Equal(a.mod) || fi.Size() != a.size {
		a.st, a.loadErr = state.LoadState(a.o.AllowState)
		a.mod, a.size, a.loaded = fi.ModTime(), fi.Size(), true
	}
	if a.loadErr != nil {
		logf(a.o.Verbose, "allowlist: cannot read %s (%v) — denying", a.o.AllowState, a.loadErr)
		return false
	}
	return a.st.SessionAllowed(a.o.Session, userID)
}

// authorized is the uncached single-shot form used in tests; the Run loop holds
// a long-lived *authorizer so reads are cached across messages.
func authorized(o Options, userID string) bool {
	return (&authorizer{o: o}).allowed(userID)
}

// chunk splits s into pieces no longer than max, preferring to break on a
// newline boundary so multi-line command output stays readable.
func chunk(s string, max int) []string {
	var out []string
	for len(s) > max {
		cut := max
		if nl := strings.LastIndexByte(s[:max], '\n'); nl > max/2 {
			cut = nl
		}
		out = append(out, s[:cut])
		s = strings.TrimPrefix(s[cut:], "\n")
	}
	if s != "" {
		out = append(out, s)
	}
	return out
}

func oneline(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 80 {
		s = s[:80] + "…"
	}
	return s
}

func logf(on bool, format string, a ...any) {
	if !on {
		return
	}
	w := bufio.NewWriter(os.Stderr)
	fmt.Fprintf(w, "dctl bridge: "+format+"\n", a...)
	w.Flush()
}

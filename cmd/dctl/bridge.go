package main

import (
	"context"
	"flag"
	"strings"
	"time"

	"github.com/Akayashuu/dctl"
	"github.com/Akayashuu/herrscher/internal/bridge"
)

// stringList is a repeatable string flag: each occurrence appends one value, so
// `--tmux-init a --tmux-init b` yields ["a","b"] in order.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ", ") }
func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// runBridge links a channel to an external command: it watches for new human
// messages and, for each, runs `--cmd` with the message text, then posts the
// command's stdout back as a threaded reply. The canonical use is binding a
// persistent Claude session to a channel:
//
//	dctl bridge --cmd 'claude -p --continue'
//
// The message text is passed to the command three ways (use whichever fits):
// appended as the final argument, piped on stdin, and via env vars
// (DCTL_MSG, DCTL_AUTHOR, DCTL_MESSAGE_ID, DCTL_CHANNEL).
func runBridge(ctx context.Context, c *dctl.Client, args []string) error {
	fs := flag.NewFlagSet("bridge", flag.ExitOnError)
	ch := channelFlag(fs)
	cmdStr := fs.String("cmd", "", "base command (default 'claude' in stream mode; the per-message program in one-shot mode)")
	stream := fs.Bool("stream", true, "legacy: only consulted when --backend is unset; --stream=false selects the one-shot backend")
	model := fs.String("model", "", "model for the persistent claude session (e.g. claude-haiku-4-5-20251001)")
	ensure := fs.String("ensure", "prospector", "if no channel is set, create/reuse a channel with this name")
	interval := fs.Int("i", 5, "poll interval in seconds")
	state := fs.String("state", "", "file to persist the last-seen message id across restarts")
	participants := fs.String("participants", "", "append-only journal of message authors for /session who")
	allowState := fs.String("allow-state", "", "daemon state.json read per-message to enforce the session allowlist (empty = no enforcement)")
	allowSession := fs.String("allow-session", "", "session name used with --allow-state to resolve the per-session allowlist")
	after := fs.String("after", "", "seed start id for the first run (state file wins once it exists)")
	verbose := fs.Bool("v", false, "log activity to stderr")
	progress := fs.String("progress", "full", "live activity feedback level: off | actions | full")
	progressKeep := fs.Bool("progress-keep", false, "keep the full progress list instead of collapsing to a one-line summary")
	backend := fs.String("backend", "", "responder backend: stream (default) | tmux | oneshot")
	tmuxTimeout := fs.Duration("tmux-timeout", 5*time.Minute, "tmux backend: max wait for a turn to settle")
	var initPrompts stringList
	fs.Var(&initPrompts, "tmux-init", "tmux backend: priming message typed once after the pane settles (repeatable)")
	controlSocket := fs.String("control-socket", "", "tmux backend: unix socket the daemon forwards select-menu clicks to (set by the daemon)")
	fs.Parse(args)

	return bridge.Run(ctx, c, bridge.Options{
		Channel:       *ch,
		Cmd:           *cmdStr,
		Stream:        *stream,
		Model:         *model,
		Ensure:        *ensure,
		Interval:      *interval,
		State:         *state,
		Participants:  *participants,
		AllowState:    *allowState,
		Session:       *allowSession,
		After:         *after,
		Verbose:       *verbose,
		Progress:      *progress,
		ProgressKeep:  *progressKeep,
		Backend:       *backend,
		TmuxTimeout:   *tmuxTimeout,
		InitPrompts:   initPrompts,
		ControlSocket: *controlSocket,
	})
}

// bridgeOptionsHasParticipants exists so a compile-time test can assert the
// --participants journal is wired into bridge.Options.
var bridgeOptionsHasParticipants = bridge.Options{}.Participants

var bridgeOptionsHasBackend = bridge.Options{}.Backend

var bridgeOptionsHasInitPrompts = bridge.Options{}.InitPrompts

var bridgeOptionsHasControlSocket = bridge.Options{}.ControlSocket

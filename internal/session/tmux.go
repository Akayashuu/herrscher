package session

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// newLines returns the lines of after that follow the longest common line
// prefix it shares with before. tmux scrollback is append-only above the input
// box, so the shared prefix is everything from prior turns and the remainder is
// what this turn added (echoed input + Claude's output + the redrawn input box).
func newLines(before, after string) []string {
	b := strings.Split(strings.TrimRight(before, "\n"), "\n")
	a := strings.Split(strings.TrimRight(after, "\n"), "\n")
	i := 0
	for i < len(b) && i < len(a) && b[i] == a[i] {
		i++
	}
	return a[i:]
}

// chromeLine reports whether a captured line is TUI furniture (borders, the
// input box, status/hint lines, spinner) rather than Claude's prose.
func chromeLine(line string) bool {
	t := strings.TrimSpace(line)
	if t == "" {
		return false // blank handled by the blank-collapser, not dropped here
	}
	// Border / input-box frame: corner glyphs, or a line made up entirely of
	// box-drawing/space runes (a bare horizontal rule has no corners).
	if strings.ContainsAny(t, "╭╮╰╯┌┐└┘") {
		return true
	}
	if strings.TrimLeft(t, "─│╭╮╰╯┌┐└┘ ") == "" {
		return true
	}
	if strings.HasPrefix(t, "│") {
		return true // input box content line
	}
	// A bare ">" (optionally with a cursor/placeholder) is the empty input
	// prompt. Don't strip "> text": Claude prose can be a markdown blockquote,
	// and the echoed user input is already removed by the newLines prefix diff.
	if t == ">" || strings.TrimSpace(strings.TrimPrefix(t, ">")) == "" {
		return true
	}
	// Status / hint / spinner lines.
	for _, p := range []string{"⏵⏵", "? for shortcuts", "(esc to interrupt)", "esc to interrupt"} {
		if strings.Contains(t, p) {
			return true
		}
	}
	return false
}

// extractTurn turns a before/after scrollback pair into the clean text Claude
// added this turn. Empty result means nothing new survived stripping.
func extractTurn(before, after string) string {
	lines := stripToolBlocks(stripChrome(newLines(before, after)))
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// stripToolBlocks drops tool-call bullet lines and their ⎿ continuation lines,
// leaving only Claude's prose — the tools are already shown in the live progress
// message, so the final reply need not repeat them. A tool line is recognized by
// isToolLine, the same predicate the live feed uses, so a prose line that merely
// starts with a bullet glyph (e.g. "● note: …") is never mistaken for a tool and
// dropped. Blank lines inside a tool block (between the bullet and its ⎿ results)
// don't end the block, so a result is never leaked back into the reply.
func stripToolBlocks(lines []string) []string {
	var out []string
	inTool := false
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if isToolLine(t) {
			inTool = true
			continue
		}
		if inTool && (t == "" || strings.HasPrefix(t, "⎿")) {
			continue // blank or continuation/result line within the tool block
		}
		inTool = false
		out = append(out, l)
	}
	return out
}

// stripChrome drops chrome lines and collapses runs of blank lines, returning
// the cleaned prose lines with no leading/trailing blanks.
func stripChrome(lines []string) []string {
	var out []string
	blank := false
	for _, l := range lines {
		if chromeLine(l) {
			continue
		}
		if strings.TrimSpace(l) == "" {
			blank = true
			continue
		}
		if blank && len(out) > 0 {
			out = append(out, "")
		}
		blank = false
		out = append(out, strings.TrimRight(l, " "))
	}
	return out
}

// quiesceCfg tunes the quiescence poll. stable = number of consecutive equal
// captures that mark "done"; poll = delay between captures; timeout = hard cap;
// busy = optional predicate that, while it reports true for the current capture,
// forbids settling (the program is still working even if the frame looks static,
// e.g. Claude is mid-tool-call with the spinner repainted to an identical pixel).
type quiesceCfg struct {
	stable  int
	poll    time.Duration
	timeout time.Duration
	busy    func(string) bool
	// onFrame, when non-nil, is called once per changed capture with the current
	// pane text, so a caller can stream intermediate state (e.g. tool progress).
	onFrame func(string)
}

// paneBusy reports whether a capture shows Claude still actively working — its
// interrupt hint is on screen — so a brief static frame must not be mistaken for
// a finished turn.
func paneBusy(capture string) bool {
	return strings.Contains(capture, "esc to interrupt")
}

// awaitQuiescence polls capture until the pane text is unchanged for cfg.stable
// consecutive reads, then returns that text. It errors on timeout or a capture
// error, or if ctx is cancelled. An empty capture never counts as settled: a
// freshly created pane returns "" until the program paints its first frame, so
// settling on it would baseline against a blank screen and leak the whole first
// paint into the next turn's diff. While cfg.busy reports true the pane is never
// considered settled, so a static mid-turn frame can't be mistaken for "done".
//
// On timeout it returns the last capture seen alongside the error, so a caller
// can salvage a partial turn (and re-baseline) instead of losing it entirely.
func awaitQuiescence(ctx context.Context, capture func() (string, error), cfg quiesceCfg) (string, error) {
	deadline := time.Now().Add(cfg.timeout)
	var last string
	same := 0
	for {
		if ctx.Err() != nil {
			return last, ctx.Err()
		}
		cur, err := capture()
		if err != nil {
			return last, err
		}
		if cur == last {
			same++
		} else {
			same, last = 0, cur
			if cfg.onFrame != nil && cur != "" {
				cfg.onFrame(cur)
			}
		}
		busy := cfg.busy != nil && cfg.busy(cur)
		if same >= cfg.stable && cur != "" && !busy {
			return cur, nil
		}
		if time.Now().After(deadline) {
			return last, fmt.Errorf("tmux pane did not settle within %s", cfg.timeout)
		}
		if cfg.poll > 0 {
			time.Sleep(cfg.poll)
		}
	}
}

// tmuxResponder drives an interactive `claude` TUI inside a persistent tmux
// session: one session per bridge, lazily started on the first message and
// reused for every later message (Claude keeps its context hot). Each turn
// types the message with send-keys, waits for the pane to settle, and returns
// the cleaned text Claude added.
type tmuxResponder struct {
	sessName string
	dir      string
	cmd      []string // program launched in the pane (e.g. ["claude","--dangerously-skip-permissions"])
	timeout  time.Duration
	init     []string // priming messages sent once after the pane settles, before any human turn

	mu       sync.Mutex
	started  bool
	baseline string        // cleaned-up: full capture after the previous turn settled
	pending  *choicePrompt // set when the last turn ended on an interactive choice prompt
}

// newTmuxResponder builds a responder. base is the pane command (defaults to
// claude --dangerously-skip-permissions); model is appended when set. init holds
// priming messages typed once after the first prompt settles (best-effort).
func newTmuxResponder(sessName, dir string, base []string, model string, timeout time.Duration, init []string) *tmuxResponder {
	cmd := append([]string{}, base...)
	if len(cmd) == 0 {
		cmd = []string{"claude", "--dangerously-skip-permissions"}
	}
	if model != "" {
		cmd = append(cmd, "--model", model)
	}
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	return &tmuxResponder{sessName: sessName, dir: dir, cmd: cmd, timeout: timeout, init: init}
}

// tmuxRun runs a tmux command without cancellation — used for best-effort
// teardown (Close) where there is no caller context to honor.
func tmuxRun(args ...string) (string, error) {
	out, err := exec.Command("tmux", args...).CombinedOutput()
	return string(out), err
}

// tmuxRunCtx runs a tmux command bound to ctx so a cancelled turn doesn't leave
// a send-keys/capture blocking past the caller's deadline.
func tmuxRunCtx(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "tmux", args...).CombinedOutput()
	return string(out), err
}

// tmuxRunCtxStdin runs a tmux command bound to ctx, piping stdin to the process.
// Used by load-buffer to feed arbitrary message text (newlines, leading dashes)
// without it ever reaching argv — no quoting/option-injection surface.
func tmuxRunCtxStdin(ctx context.Context, stdin string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "tmux", args...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (t *tmuxResponder) capture(ctx context.Context) (string, error) {
	return tmuxRunCtx(ctx, "capture-pane", "-p", "-S", "-", "-t", t.sessName)
}

func (t *tmuxResponder) capturePoll(ctx context.Context, onFrame func(string)) (string, error) {
	return awaitQuiescence(ctx, func() (string, error) { return t.capture(ctx) }, quiesceCfg{
		stable:  3,
		poll:    300 * time.Millisecond,
		timeout: t.timeout,
		busy:    paneBusy,
		onFrame: onFrame,
	})
}

func (t *tmuxResponder) start(ctx context.Context) error {
	if _, err := exec.LookPath("tmux"); err != nil {
		return fmt.Errorf("tmux not found on PATH: %w", err)
	}
	// Reattach to a live namesake instead of recreating it. An in-place dctl
	// update restarts the bridge process, but the tmux session — and Claude's
	// hot context inside it — keeps running independently. has-session succeeds
	// only when such a pane is still alive (a crashed Claude exits its pane,
	// which ends the session, so has-session then fails and we create afresh
	// below). Adopt the survivor by baselining on its current pane and skip
	// priming so the conversation continues uninterrupted.
	if _, err := tmuxRun("has-session", "-t", t.sessName); err == nil {
		settled, err := t.capturePoll(ctx, nil)
		if err != nil {
			return err
		}
		t.baseline = settled
		t.started = true
		return nil
	}

	// Pin the working directory explicitly. new-session without -c inherits the
	// tmux *server* cwd (a long-lived daemon that may already run elsewhere), not
	// this process's, so an empty dir is resolved to our own cwd here.
	dir := t.dir
	if dir == "" {
		if wd, err := os.Getwd(); err == nil {
			dir = wd
		}
	}
	args := []string{"new-session", "-d", "-s", t.sessName, "-x", "200", "-y", "50"}
	if dir != "" {
		args = append(args, "-c", dir)
	}
	args = append(args, strings.Join(t.cmd, " "))
	if out, err := tmuxRunCtx(ctx, args...); err != nil {
		return fmt.Errorf("tmux new-session: %v: %s", err, out)
	}
	// Wait for the TUI to finish drawing its first prompt before we type. On
	// failure the pane is left for Close (or the next start's stale-kill) to reap;
	// started stays false so we never type against an unsettled baseline.
	settled, err := t.capturePoll(ctx, nil)
	if err != nil {
		return err
	}
	t.baseline = settled
	t.started = true
	// Priming: type the configured init messages once, before any human turn.
	// Best-effort — a failed/timed-out prompt is logged and the rest (plus the
	// first human message) still go through; amorçage must never block a session.
	// Each turn advances the baseline, so the human's first reply diffs against
	// the post-priming pane and never echoes the priming output back.
	for i, p := range t.init {
		p = normalizeNewlines(p)
		if strings.TrimSpace(p) == "" {
			continue
		}
		if _, err := t.turn(ctx, p, nil); err != nil {
			fmt.Fprintf(os.Stderr, "dctl tmux: init prompt %d failed: %v\n", i+1, err)
		}
	}
	return nil
}

// sendLiteralArgs builds the tmux send-keys argv that types text verbatim into a
// pane. The `--` terminates option parsing so a message beginning with `-` (e.g.
// "-h") is typed literally instead of being mistaken for a send-keys flag.
func sendLiteralArgs(sess, text string) []string {
	return []string{"send-keys", "-t", sess, "-l", "--", text}
}

// Choice is an interactive prompt the pane is waiting on, surfaced to the bridge
// so it can render a native select menu. Each item's Value is the digit typed
// into the pane to pick it; Label is the human text.
type Choice struct {
	Question string
	Options  []ChoiceItem
}

// ChoiceItem is one selectable option: Value is the keystroke that picks it.
type ChoiceItem struct {
	Value string
	Label string
}

// ChoiceAware is implemented by responders that can leave the pane waiting on an
// interactive prompt. After Respond, the bridge asks PendingChoice to decide
// whether to attach a select menu to its reply.
type ChoiceAware interface {
	PendingChoice() (Choice, bool)
}

// ChoiceInjector is implemented by responders that can answer a pending choice
// out-of-band (a daemon-routed select-menu click) by typing the value into the
// pane, serialized with normal turns.
type ChoiceInjector interface {
	InjectChoice(ctx context.Context, value string) (string, error)
}

// PendingChoice reports the choice the pane is waiting on after the last turn,
// if any. Guarded by the same mutex as Respond, so it reflects a completed turn.
func (t *tmuxResponder) PendingChoice() (Choice, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.pending == nil {
		return Choice{}, false
	}
	c := Choice{Question: t.pending.Question}
	for _, o := range t.pending.Options {
		c.Options = append(c.Options, ChoiceItem{Value: o.Key, Label: o.Label})
	}
	return c, true
}

// InjectChoice types value into the pane as one turn, answering a pending choice
// from a select-menu click. It takes the same lock as Respond, so an injected
// pick can never interleave keystrokes with a concurrent human turn.
func (t *tmuxResponder) InjectChoice(ctx context.Context, value string) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.started {
		// Nothing has been typed yet, so there is no prompt to answer.
		return "", fmt.Errorf("tmux: no session to inject choice into")
	}
	return t.turn(ctx, normalizeNewlines(value), nil)
}

func (t *tmuxResponder) Respond(ctx context.Context, m DctlMessage, onEvent func(Event)) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.started {
		if err := t.start(ctx); err != nil {
			return "", err
		}
	}
	// Preserve the message's line structure; typeText bracket-pastes multi-line
	// input so embedded newlines stay literal instead of submitting early.
	return t.turn(ctx, normalizeNewlines(withAttachments(m.Content, m.Attachments)), onEvent)
}

// turn types one already-sanitized line into the pane, submits it, waits for the
// pane to settle, and returns the cleaned text Claude added this turn. It assumes
// t.mu is held and the pane is started; it is shared by Respond and the priming
// loop in start().
func (t *tmuxResponder) turn(ctx context.Context, text string, onEvent func(Event)) (string, error) {
	before := t.baseline
	t.pending = nil // cleared up front; a new choice prompt re-sets it below
	if err := t.typeText(ctx, text); err != nil {
		return "", err
	}
	if out, err := tmuxRunCtx(ctx, "send-keys", "-t", t.sessName, "Enter"); err != nil {
		return "", fmt.Errorf("tmux send-keys Enter: %v: %s", err, out)
	}
	// emitted counts the tools already streamed this turn. Dedup is positional:
	// the TUI scrollback is append-only above the input box, so a tool already
	// seen keeps its index across repaints and is never emitted twice. lastLines
	// skips the re-parse on spinner-only repaints (frame changed but no line was
	// added), so a verbose turn stays roughly O(lines) instead of O(lines×frames).
	emitted := 0
	lastLines := -1
	var onFrame func(string)
	if onEvent != nil {
		onFrame = func(frame string) {
			nl := newLines(before, frame)
			if len(nl) == lastLines {
				return // no line added since the last parse → no new tool possible
			}
			lastLines = len(nl)
			tools := parseToolEvents(strings.Join(nl, "\n"))
			for ; emitted < len(tools); emitted++ {
				onEvent(Event{Kind: "tool", Tool: tools[emitted].Tool, Detail: tools[emitted].Detail})
			}
		}
	}
	after, err := t.capturePoll(ctx, onFrame)
	// Advance the baseline to whatever was on screen even on timeout, so the next
	// turn diffs against the current pane instead of replaying this turn's output.
	if after != "" {
		t.baseline = after
	}
	// A turn that ends on an interactive choice prompt (e.g. a tool-permission
	// "Do you want to proceed?") is rendered as clean numbered options rather than
	// run through extractTurn, whose stripChrome would eat the box and leave an
	// empty or garbled reply. The human picks by replying with a number (typed into
	// the pane next turn) or, in daemon mode, via a native select menu.
	// Detect a choice prompt only among the lines THIS turn drew, not the whole
	// scrollback — an already-answered prompt lingering above would otherwise
	// re-match every turn and keep re-posting a menu.
	if cp, ok := parseChoicePrompt(strings.Join(newLines(before, after), "\n")); ok {
		cp := cp
		t.pending = &cp // surfaced to the bridge (PendingChoice) for the select menu
		return renderChoice(cp), nil
	}
	if err != nil {
		// Salvage whatever Claude produced before the deadline rather than losing
		// the turn; surface the error only when there's nothing to show.
		if partial := extractTurn(before, after); partial != "" {
			return partial, nil
		}
		return "", err
	}
	reply := extractTurn(before, after)
	if reply == "" {
		// A turn that was all tool calls and no prose strips to nothing. Never
		// silently lose it, but don't dump raw ⏺/⎿ markup either — fall back to the
		// chrome-stripped diff (tool blocks kept, TUI furniture removed) so the
		// reply is at least readable when there's no prose to show.
		reply = strings.TrimSpace(strings.Join(stripChrome(newLines(before, after)), "\n"))
	}
	return reply, nil
}

// normalizeNewlines canonicalizes line endings to "\n" without flattening, so a
// multi-line message keeps its structure. A bare "\n" sent via send-keys would
// read as Enter (early submit), so typeText routes multi-line text through a
// bracketed paste instead — see typeText.
func normalizeNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// pasteBufferName derives a per-session tmux buffer name for the paste path, so
// two sessions pasting concurrently never clash on a shared buffer.
func pasteBufferName(sess string) string { return "dctlbuf-" + sess }

// pasteBufferArgs builds the paste-buffer argv: -p enables bracketed paste so the
// TUI treats embedded newlines as literal input (not Enter), and -d drops the
// buffer afterwards so it never lingers in tmux's paste ring.
func pasteBufferArgs(sess, buf string) []string {
	return []string{"paste-buffer", "-d", "-p", "-b", buf, "-t", sess}
}

// typeText types text into the pane without submitting it. A single line goes
// through send-keys -l (fast, the "--" guards a leading dash). Multi-line text is
// loaded into a tmux buffer from stdin (no argv exposure) and bracket-pasted, so
// the embedded newlines land as literal input instead of submitting each line.
func (t *tmuxResponder) typeText(ctx context.Context, text string) error {
	if !strings.Contains(text, "\n") {
		if out, err := tmuxRunCtx(ctx, sendLiteralArgs(t.sessName, text)...); err != nil {
			return fmt.Errorf("tmux send-keys: %v: %s", err, out)
		}
		return nil
	}
	buf := pasteBufferName(t.sessName)
	if out, err := tmuxRunCtxStdin(ctx, text, "load-buffer", "-b", buf, "-"); err != nil {
		return fmt.Errorf("tmux load-buffer: %v: %s", err, out)
	}
	if out, err := tmuxRunCtx(ctx, pasteBufferArgs(t.sessName, buf)...); err != nil {
		return fmt.Errorf("tmux paste-buffer: %v: %s", err, out)
	}
	return nil
}

func (t *tmuxResponder) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	// Deliberately does NOT kill the tmux session: it must outlive this bridge
	// process so an in-place dctl update (which restarts the bridge) keeps
	// Claude's context hot — the next start() reattaches via has-session.
	// Genuine teardown is explicit, via KillTmuxSession from the session-close
	// handler.
	t.started = false
	return nil
}

// tmuxSessionName derives a collision-safe tmux session name from the channel,
// namespaced by DCTL_INSTANCE_ID (same scheme as session worktrees). tmux forbids
// "." and ":" in session names (and trims whitespace), so they are folded to "-".
func tmuxSessionName(channel string) string {
	name := "dctl-" + channel
	if inst := os.Getenv("DCTL_INSTANCE_ID"); inst != "" {
		name = "dctl-" + inst + "-" + channel
	}
	return sanitizeSessionName(name)
}

// KillTmuxSession terminates the persistent pane for channel. It exists because
// Close() now deliberately leaves the session running across bridge restarts, so
// genuine teardown (a /session close) must reap it explicitly. Best-effort: a
// no-op when no such session exists. Safe to call for non-tmux backends.
func KillTmuxSession(channel string) {
	_, _ = tmuxRun("kill-session", "-t", tmuxSessionName(channel))
}

// TmuxSessionExists reports whether the persistent pane for channel is still
// alive. Symmetric to KillTmuxSession: same name derivation, best-effort. A
// missing tmux binary or absent session both yield false.
func TmuxSessionExists(channel string) bool {
	_, err := tmuxRun("has-session", "-t", tmuxSessionName(channel))
	return err == nil
}

// sanitizeSessionName folds characters tmux rejects in a session name ("." and
// ":") and any whitespace into "-", keeping the name addressable by -t.
func sanitizeSessionName(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '.', ':', ' ', '\t', '\n':
			return '-'
		}
		return r
	}, s)
}

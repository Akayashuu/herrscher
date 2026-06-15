package session

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// NewResponder selects the response strategy by backend:
//
//	"stream"  — one persistent claude stream-json process (default)
//	"oneshot" — run cmd fresh per message
//	"tmux"    — interactive claude TUI inside a tmux session, text relayed
//
// dir is the working directory (tmux/stream); channel seeds the tmux session name.
// tmuxTimeout caps how long the tmux backend waits for a turn to settle (0 =
// default); it is ignored by the other backends. initPrompts are priming
// messages the tmux backend types once after the pane settles (also tmux-only).
func NewResponder(ctx context.Context, backend, cmdStr, model, dir, channel string, tmuxTimeout time.Duration, initPrompts []string, oneShot func(context.Context, DctlMessage) (string, error)) Responder {
	switch backend {
	case "oneshot":
		return &oneShotResponder{run: oneShot}
	case "tmux":
		return newTmuxResponder(tmuxSessionName(channel), dir, strings.Fields(cmdStr), model, tmuxTimeout, initPrompts)
	default: // "stream"
		r := &streamResponder{ctx: ctx, base: streamBase(strings.Fields(cmdStr)), model: model}
		r.dir = dir
		return r
	}
}

// streamBase normalizes a base command for persistent stream-json mode by
// dropping flags that collide with it. A session persisted before stream mode
// carries the old default ("claude -p --continue"); streamArgv adds its own -p
// and the stream-format flags, so the legacy -p/--print/--continue must go or
// the process launches with a duplicate -p and a conflicting --continue.
func streamBase(fields []string) []string {
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		switch f {
		case "-p", "--print", "--continue":
			continue
		}
		out = append(out, f)
	}
	if len(out) == 0 {
		return []string{"claude"}
	}
	return out
}

// withAttachments appends image attachment paths to a message body as plain
// references so the local Claude can open them with its Read tool. Shared by
// every backend (stream, tmux, one-shot) for consistent phrasing.
func withAttachments(text string, paths []string) string {
	if len(paths) == 0 {
		return text
	}
	var b strings.Builder
	b.WriteString(text)
	if text != "" {
		b.WriteString("\n\n")
	}
	for i, p := range paths {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("[Image jointe : ")
		b.WriteString(p)
		b.WriteByte(']')
	}
	return b.String()
}

// userLine marshals one Claude Code stream-json user message, newline-terminated
// for writing to the process stdin.
func userLine(text string) ([]byte, error) {
	msg := map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": text,
		},
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// turnResult is the outcome of one assistant turn, parsed from the stream's
// terminal `result` event.
type turnResult struct {
	Text      string
	CostUSD   float64
	SessionID string
	IsError   bool
	ErrMsg    string
}

// Event is one intermediate stream occurrence surfaced to a progress consumer.
type Event struct {
	Kind    string  // "tool" | "text" | "result" | "reset"
	Tool    string  // tool name (Kind == "tool")
	Detail  string  // tool: salient input field; text: the assistant text
	Cost    float64 // Kind == "result": total_cost_usd
	IsError bool    // Kind == "result"
}

// contentBlock is one block of an assistant message's content array.
type contentBlock struct {
	Type  string          `json:"type"` // "text" | "tool_use" | "thinking" | ...
	Text  string          `json:"text"`
	Name  string          `json:"name"`  // tool name (tool_use)
	Input json.RawMessage `json:"input"` // tool input (tool_use)
}

// toolDetail extracts the most informative single field from a tool's input
// (command for Bash, file_path for Read/Edit, etc.) for a one-line summary.
func toolDetail(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(input, &m) != nil {
		return ""
	}
	for _, k := range []string{"command", "file_path", "path", "pattern", "query", "url", "description", "prompt"} {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// streamEvent is the subset of a stream-json line we care about. The `result`
// event terminates a turn and carries the final assembled text + cost.
type streamEvent struct {
	Type         string  `json:"type"`
	SessionID    string  `json:"session_id"`
	IsError      bool    `json:"is_error"`
	Result       string  `json:"result"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	Message      struct {
		Content []contentBlock `json:"content"`
	} `json:"message"`
}

// readTurn consumes stream-json events until the terminal `result` event. When
// onEvent is non-nil it emits an Event per intermediate assistant block (tool
// uses and text) and a terminal "result" event carrying cost. It uses ReadBytes
// (not bufio.Scanner) because the system/init event can exceed Scanner's 64 KB cap.
func readTurn(r *bufio.Reader, onEvent func(Event)) (turnResult, error) {
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			var ev streamEvent
			if json.Unmarshal(line, &ev) == nil {
				switch ev.Type {
				case "assistant":
					if onEvent != nil {
						for _, b := range ev.Message.Content {
							switch b.Type {
							case "text":
								if t := strings.TrimSpace(b.Text); t != "" {
									onEvent(Event{Kind: "text", Detail: t})
								}
							case "tool_use":
								onEvent(Event{Kind: "tool", Tool: b.Name, Detail: toolDetail(b.Input)})
							}
						}
					}
				case "result":
					if onEvent != nil {
						onEvent(Event{Kind: "result", Cost: ev.TotalCostUSD, IsError: ev.IsError})
					}
					tr := turnResult{
						Text:      ev.Result,
						CostUSD:   ev.TotalCostUSD,
						SessionID: ev.SessionID,
						IsError:   ev.IsError,
					}
					if ev.IsError {
						tr.ErrMsg = ev.Result
					}
					return tr, nil
				}
			}
		}
		if err != nil {
			return turnResult{}, err
		}
	}
}

// streamSession wraps a live `claude` stream-json process: one turn at a time
// (serialized by mu), writing user messages to stdin and reading turns off
// stdout. In tests the io pair is injected directly; in production Start wires
// it to a real subprocess.
type streamSession struct {
	mu     sync.Mutex
	stdin  io.WriteCloser
	out    *bufio.Reader
	cmd    *exec.Cmd // nil when the io pair is injected (tests)
	sessID string    // last session id seen, for --resume on restart
}

// newStreamSession builds a session over an arbitrary io pair (used by tests and
// by Start once it has the process pipes).
func newStreamSession(stdin io.WriteCloser, out io.Reader) *streamSession {
	return &streamSession{stdin: stdin, out: bufio.NewReader(out)}
}

// streamArgv builds the claude argv for persistent stream-json mode: the base
// command (e.g. ["claude"], possibly with extra user args) followed by the
// stream flags, plus --model / --resume when provided.
func streamArgv(base []string, model, resumeID string) []string {
	argv := append([]string{}, base...)
	argv = append(argv, "-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose")
	if model != "" {
		argv = append(argv, "--model", model)
	}
	if resumeID != "" {
		argv = append(argv, "--resume", resumeID)
	}
	return argv
}

// startStreamSession launches a real `claude` stream-json process in dir and
// returns a session wired to its stdin/stdout.
func startStreamSession(ctx context.Context, base []string, model, resumeID, dir string) (*streamSession, error) {
	argv := streamArgv(base, model, resumeID)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	s := newStreamSession(stdin, stdout)
	s.cmd = cmd
	s.sessID = resumeID
	return s, nil
}

// Send writes one user message and reads back the full assistant turn, emitting
// intermediate events to onEvent (nil = none). An error means the stream closed
// (process died) — the caller should restart.
func (s *streamSession) Send(text string, onEvent func(Event)) (turnResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	line, err := userLine(text)
	if err != nil {
		return turnResult{}, err
	}
	if _, err := s.stdin.Write(line); err != nil {
		return turnResult{}, err
	}
	tr, err := readTurn(s.out, onEvent)
	if err != nil {
		return tr, err
	}
	if tr.SessionID != "" {
		s.sessID = tr.SessionID
	}
	return tr, nil
}

// Responder turns one Discord message into a reply string, optionally emitting
// intermediate progress events. Two implementations: a persistent stream-json
// claude session, or a per-message one-shot command.
type Responder interface {
	Respond(ctx context.Context, m DctlMessage, onEvent func(Event)) (string, error)
	Close() error
}

// DctlMessage is the minimal message shape the responders need (decoupled from
// the discord package so stream.go stays import-light; runBridge adapts).
type DctlMessage struct {
	Content   string
	Author    string
	MessageID string
	ChannelID string
	// Attachments holds local filesystem paths to image files downloaded from
	// the Discord message, ready for the session backend to reference.
	Attachments []string
}

// oneShotResponder runs cmdStr fresh for every message (legacy behavior, used
// for arbitrary non-claude commands when --stream=false).
type oneShotResponder struct {
	run func(ctx context.Context, m DctlMessage) (string, error)
}

func (o *oneShotResponder) Respond(ctx context.Context, m DctlMessage, _ func(Event)) (string, error) {
	return o.run(ctx, m)
}
func (o *oneShotResponder) Close() error { return nil }

// streamResponder keeps one persistent claude stream-json process alive across
// messages. On process death it restarts with --resume and retries once.
type streamResponder struct {
	ctx   context.Context
	base  []string
	model string
	dir   string
	mu    sync.Mutex
	sess  *streamSession
}

func (r *streamResponder) Respond(ctx context.Context, m DctlMessage, onEvent func(Event)) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sess == nil {
		s, err := startStreamSession(r.ctx, r.base, r.model, "", r.dir)
		if err != nil {
			return "", err
		}
		r.sess = s
	}
	content := withAttachments(m.Content, m.Attachments)
	tr, err := r.sess.Send(content, onEvent)
	if err != nil {
		// Process likely died: restart with the last session id and retry once.
		// Tell the consumer to discard any partial-turn events emitted before the
		// crash so the retried turn isn't double-counted.
		if onEvent != nil {
			onEvent(Event{Kind: "reset"})
		}
		resume := r.sess.sessID
		_ = r.sess.Close()
		s, startErr := startStreamSession(r.ctx, r.base, r.model, resume, r.dir)
		if startErr != nil {
			return "", startErr
		}
		r.sess = s
		if tr, err = r.sess.Send(content, onEvent); err != nil {
			return "", err
		}
	}
	if tr.IsError {
		return tr.Text, errFromTurn(tr)
	}
	return tr.Text, nil
}

func (r *streamResponder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sess != nil {
		return r.sess.Close()
	}
	return nil
}

func errFromTurn(tr turnResult) error {
	if tr.ErrMsg != "" {
		return &turnError{tr.ErrMsg}
	}
	return &turnError{"claude reported an error"}
}

type turnError struct{ msg string }

func (e *turnError) Error() string { return e.msg }

// Close stops the session: closes stdin and kills the process if any.
func (s *streamSession) Close() error {
	if s.stdin != nil {
		_ = s.stdin.Close()
	}
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		_ = s.cmd.Wait() // reap so a killed session doesn't leak a zombie on restart
	}
	return nil
}

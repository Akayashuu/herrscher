package session

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestResponderSelection(t *testing.T) {
	noop := func(ctx context.Context, m DctlMessage) (string, error) { return "x", nil }
	if _, ok := NewResponder(context.Background(), "oneshot", "foo", "", "", "chan", 0, nil, noop).(*oneShotResponder); !ok {
		t.Fatal("oneshot backend should yield oneShotResponder")
	}
	if _, ok := NewResponder(context.Background(), "stream", "claude", "", "", "chan", 0, nil, noop).(*streamResponder); !ok {
		t.Fatal("stream backend should yield streamResponder")
	}
	if _, ok := NewResponder(context.Background(), "tmux", "claude", "", "/tmp", "chan", 0, nil, noop).(*tmuxResponder); !ok {
		t.Fatal("tmux backend should yield tmuxResponder")
	}
}

func TestStreamArgv(t *testing.T) {
	got := streamArgv([]string{"claude", "--permission-mode", "acceptEdits"}, "claude-haiku", "sess-9")
	want := []string{
		"claude", "--permission-mode", "acceptEdits",
		"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
		"--model", "claude-haiku",
		"--resume", "sess-9",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("argv =\n  %v\nwant\n  %v", got, want)
	}

	// No model / no resume → those flags are omitted.
	bare := streamArgv([]string{"claude"}, "", "")
	for _, f := range bare {
		if f == "--model" || f == "--resume" {
			t.Fatalf("did not expect %q in %v", f, bare)
		}
	}
}

func TestStreamBaseStripsLegacyFlags(t *testing.T) {
	// A session persisted before stream mode carries the old default
	// "claude -p --continue"; the stream flags must not collide with it.
	got := streamBase([]string{"claude", "-p", "--continue"})
	if strings.Join(got, " ") != "claude" {
		t.Fatalf("base = %v, want [claude]", got)
	}
	// Empty / no command → claude.
	if strings.Join(streamBase(nil), " ") != "claude" {
		t.Fatal("empty base should default to claude")
	}
	// Legitimate extra args survive.
	keep := streamBase([]string{"claude", "--permission-mode", "acceptEdits"})
	if strings.Join(keep, " ") != "claude --permission-mode acceptEdits" {
		t.Fatalf("base = %v, want extra args preserved", keep)
	}
	// And the full argv built from a legacy command has exactly one -p.
	argv := streamArgv(streamBase([]string{"claude", "-p", "--continue"}), "", "")
	n := 0
	for _, f := range argv {
		if f == "-p" {
			n++
		}
		if f == "--continue" {
			t.Fatalf("--continue leaked into argv: %v", argv)
		}
	}
	if n != 1 {
		t.Fatalf("expected exactly one -p, got %d in %v", n, argv)
	}
}

func TestStreamSessionSend(t *testing.T) {
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	go func() {
		br := bufio.NewReader(stdinR)
		if _, err := br.ReadBytes('\n'); err != nil {
			return
		}
		io.WriteString(stdoutW,
			`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]}}`+"\n"+
				`{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}`+"\n"+
				`{"type":"result","subtype":"success","is_error":false,"result":"hello back","total_cost_usd":0.002,"session_id":"abc"}`+"\n")
		stdoutW.Close()
	}()

	s := newStreamSession(stdinW, stdoutR)
	var got []Event
	tr, err := s.Send("hello", func(e Event) { got = append(got, e) })
	if err != nil {
		t.Fatal(err)
	}
	if tr.Text != "hello back" {
		t.Fatalf("text = %q, want 'hello back'", tr.Text)
	}
	if s.sessID != "abc" {
		t.Fatalf("session id not recorded: %q", s.sessID)
	}
	if len(got) < 1 || got[0].Kind != "tool" || got[0].Tool != "Bash" {
		t.Fatalf("expected a Bash tool event, got %+v", got)
	}
}

func TestUserLineShape(t *testing.T) {
	line, err := userLine("hi there")
	if err != nil {
		t.Fatal(err)
	}
	if len(line) == 0 || line[len(line)-1] != '\n' {
		t.Fatalf("expected newline-terminated line, got %q", line)
	}
	var v struct {
		Type    string `json:"type"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(line, &v); err != nil {
		t.Fatalf("not valid json: %v", err)
	}
	if v.Type != "user" || v.Message.Role != "user" || v.Message.Content != "hi there" {
		t.Fatalf("wrong shape: %+v", v)
	}
}

func TestReadTurnSuccess(t *testing.T) {
	canned := strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"sess-1","model":"claude-haiku"}`,
		`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"hmm"}]},"session_id":"sess-1"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"PONG"}]},"session_id":"sess-1"}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"PONG","total_cost_usd":0.0136,"session_id":"sess-1"}`,
	}, "\n") + "\n"

	tr, err := readTurn(bufio.NewReader(strings.NewReader(canned)), nil)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Text != "PONG" {
		t.Fatalf("text = %q, want PONG", tr.Text)
	}
	if tr.CostUSD <= 0 {
		t.Fatalf("cost = %v, want > 0", tr.CostUSD)
	}
	if tr.SessionID != "sess-1" {
		t.Fatalf("session id = %q, want sess-1", tr.SessionID)
	}
	if tr.IsError {
		t.Fatal("did not expect error")
	}
}

func TestReadTurnError(t *testing.T) {
	canned := `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"boom","session_id":"s"}` + "\n"
	tr, err := readTurn(bufio.NewReader(strings.NewReader(canned)), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !tr.IsError {
		t.Fatal("expected IsError")
	}
	if tr.ErrMsg == "" {
		t.Fatal("expected ErrMsg populated")
	}
}

func TestReadTurnHandlesHugeLine(t *testing.T) {
	huge := strings.Repeat("x", 200_000)
	canned := `{"type":"system","subtype":"init","session_id":"s","blob":"` + huge + `"}` + "\n" +
		`{"type":"result","subtype":"success","is_error":false,"result":"ok","session_id":"s"}` + "\n"
	tr, err := readTurn(bufio.NewReader(strings.NewReader(canned)), nil)
	if err != nil {
		t.Fatalf("huge line should not error: %v", err)
	}
	if tr.Text != "ok" {
		t.Fatalf("text = %q, want ok", tr.Text)
	}
}

func TestReadTurnEmitsEvents(t *testing.T) {
	canned := strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"s"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"je regarde"}]},"session_id":"s"}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"git status"}}]},"session_id":"s"}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"stream.go"}}]},"session_id":"s"}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"done","total_cost_usd":0.04,"session_id":"s"}`,
	}, "\n") + "\n"

	var got []Event
	tr, err := readTurn(bufio.NewReader(strings.NewReader(canned)), func(e Event) { got = append(got, e) })
	if err != nil {
		t.Fatal(err)
	}
	if tr.Text != "done" {
		t.Fatalf("text = %q, want done", tr.Text)
	}
	want := []Event{
		{Kind: "text", Detail: "je regarde"},
		{Kind: "tool", Tool: "Bash", Detail: "git status"},
		{Kind: "tool", Tool: "Read", Detail: "stream.go"},
		{Kind: "result", Cost: 0.04, IsError: false},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestReadTurnNilCallback(t *testing.T) {
	canned := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"x"}}]}}` + "\n" +
		`{"type":"result","is_error":false,"result":"ok","session_id":"s"}` + "\n"
	tr, err := readTurn(bufio.NewReader(strings.NewReader(canned)), nil)
	if err != nil {
		t.Fatalf("nil callback must not panic: %v", err)
	}
	if tr.Text != "ok" {
		t.Fatalf("text = %q, want ok", tr.Text)
	}
}

func TestWithAttachments(t *testing.T) {
	if got := withAttachments("hello", nil); got != "hello" {
		t.Errorf("no attachments should pass text through, got %q", got)
	}
	got := withAttachments("look", []string{"/tmp/a.png", "/tmp/b.png"})
	want := "look\n\n[Image jointe : /tmp/a.png]\n[Image jointe : /tmp/b.png]"
	if got != want {
		t.Errorf("withAttachments = %q, want %q", got, want)
	}
	if got := withAttachments("", []string{"/tmp/a.png"}); got != "[Image jointe : /tmp/a.png]" {
		t.Errorf("empty text should not add leading newlines, got %q", got)
	}
}

package terminal

import (
	"context"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

func TestReadDrainsSubmittedLines(t *testing.T) {
	tm := New()
	tm.Submit("hello")
	tm.Submit("world")

	msgs, err := tm.Read(context.Background(), "terminal", 100, "")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(msgs) != 2 || msgs[0].Content != "hello" || msgs[1].Content != "world" {
		t.Fatalf("Read = %+v, want hello/world", msgs)
	}
	// A second Read drains nothing (already consumed).
	msgs, _ = tm.Read(context.Background(), "terminal", 100, "")
	if len(msgs) != 0 {
		t.Fatalf("second Read = %+v, want empty", msgs)
	}
}

func TestEmitForwardsToFrontend(t *testing.T) {
	tm := New()
	got := make([]contracts.Event, 0, 2)
	done := make(chan struct{})
	go func() {
		for e := range tm.Frontend() {
			got = append(got, e)
			if len(got) == 2 {
				close(done)
				return
			}
		}
	}()
	tm.Emit(contracts.Event{T: "chunk", Text: "a"})
	tm.Emit(contracts.Event{T: "reply", Text: "b", Done: true})
	<-done
	if got[0].Text != "a" || got[1].Text != "b" {
		t.Fatalf("frontend got %+v, want a/b", got)
	}
}

func TestPostEmitsReplyEvent(t *testing.T) {
	tm := New()
	got := make(chan contracts.Event, 1)
	go func() { got <- <-tm.Frontend() }()
	if _, err := tm.Post(context.Background(), contracts.Conversation{Gateway: "terminal", ID: "terminal"}, "hi"); err != nil {
		t.Fatalf("Post: %v", err)
	}
	if e := <-got; e.T != "reply" || e.Text != "hi" {
		t.Fatalf("Post emitted %+v, want reply/hi", e)
	}
}

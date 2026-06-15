package contracts

import (
	"context"
	"strings"
	"testing"
)

func full() Capabilities { return Capabilities{Reactions: true, SelectMenus: true, Replies: true} }

func TestDegradePassThroughWhenCapable(t *testing.T) {
	rec := &recGateway{manifest: Manifest{Capabilities: full()}}
	d := Degrade(rec)
	ctx, conv := context.Background(), Conversation{Gateway: "discord", ID: "c"}

	_, _ = d.Reply(ctx, conv, "m", "hello")
	_ = d.React(ctx, conv, "m", "👀")
	_ = d.Menu(ctx, conv, "m", "pick", []Choice{{Label: "A", Value: "a"}})

	if len(rec.replies) != 1 || len(rec.reacts) != 1 || len(rec.menus) != 1 {
		t.Fatalf("capable gateway should receive rich calls: %+v", rec)
	}
	if len(rec.posts) != 0 {
		t.Fatalf("no fallback Post expected when capable")
	}
}

func TestDegradeReplyToPost(t *testing.T) {
	rec := &recGateway{manifest: Manifest{Capabilities: Capabilities{Reactions: true, SelectMenus: true}}}
	d := Degrade(rec)
	_, _ = d.Reply(context.Background(), Conversation{}, "m", "hello")
	if len(rec.replies) != 0 || len(rec.posts) != 1 || rec.posts[0] != "hello" {
		t.Fatalf("reply should degrade to post: %+v", rec)
	}
}

func TestDegradeReactNoOp(t *testing.T) {
	rec := &recGateway{manifest: Manifest{Capabilities: Capabilities{SelectMenus: true, Replies: true}}}
	d := Degrade(rec)
	if err := d.React(context.Background(), Conversation{}, "m", "👀"); err != nil {
		t.Fatalf("degraded react should be a no-op, got %v", err)
	}
	if len(rec.reacts) != 0 {
		t.Fatalf("react must not reach a gateway without Reactions")
	}
}

func TestDegradeMenuToNumberedList(t *testing.T) {
	rec := &recGateway{manifest: Manifest{Capabilities: Capabilities{Reactions: true, Replies: true}}}
	d := Degrade(rec)
	_ = d.Menu(context.Background(), Conversation{}, "m", "Choose:", []Choice{
		{Label: "Alpha", Value: "a"}, {Label: "Beta", Value: "b"},
	})
	if len(rec.menus) != 0 || len(rec.posts) != 1 {
		t.Fatalf("menu should degrade to a post: %+v", rec)
	}
	got := rec.posts[0]
	if !strings.Contains(got, "Choose:") || !strings.Contains(got, "1. Alpha") || !strings.Contains(got, "2. Beta") {
		t.Fatalf("numbered list malformed: %q", got)
	}
}

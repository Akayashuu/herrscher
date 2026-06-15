package discord

import (
	"context"
	"testing"

	"github.com/Akayashuu/dctl"
	"github.com/Akayashuu/herrscher/contracts"
)

type fakeClient struct {
	sent    []string
	replied []string
	reacted []string
	menus   []string
}

func (f *fakeClient) Send(_ context.Context, _, content string) (*dctl.Message, error) {
	f.sent = append(f.sent, content)
	return &dctl.Message{ID: "m1"}, nil
}
func (f *fakeClient) Reply(_ context.Context, _, _, content string) (*dctl.Message, error) {
	f.replied = append(f.replied, content)
	return &dctl.Message{ID: "m2"}, nil
}
func (f *fakeClient) React(_ context.Context, _, _, emoji string) error {
	f.reacted = append(f.reacted, emoji)
	return nil
}
func (f *fakeClient) SendSelectMenu(_ context.Context, _, _, content, _ string, _ []dctl.SelectOption) (*dctl.Message, error) {
	f.menus = append(f.menus, content)
	return &dctl.Message{ID: "m3"}, nil
}

var _ contracts.Gateway = (*Gateway)(nil)

func TestGatewayManifest(t *testing.T) {
	g := NewGateway(&fakeClient{})
	m := g.Manifest()
	if m.Kind != "discord" || m.Category != contracts.CategoryGateway {
		t.Fatalf("bad manifest %+v", m)
	}
	if !m.Capabilities.Reactions || !m.Capabilities.SelectMenus || !m.Capabilities.Replies {
		t.Fatalf("discord should announce all capabilities: %+v", m.Capabilities)
	}
}

func TestGatewayTranslatesActions(t *testing.T) {
	fc := &fakeClient{}
	g := NewGateway(fc)
	ctx := context.Background()
	conv := contracts.Conversation{Gateway: "discord", ID: "chan"}

	_, _ = g.Post(ctx, conv, "hello")
	_, _ = g.Reply(ctx, conv, "mid", "answer")
	_ = g.React(ctx, conv, "mid", "👀")
	_ = g.Menu(ctx, conv, "mid", "pick", []contracts.Choice{{Label: "A", Value: "a"}})

	if len(fc.sent) != 1 || len(fc.replied) != 1 || len(fc.reacted) != 1 || len(fc.menus) != 1 {
		t.Fatalf("translation incomplete: %+v", fc)
	}
}

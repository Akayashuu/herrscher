package contracts

import (
	"context"
	"testing"
)

// recGateway records calls; used across contracts tests.
type recGateway struct {
	manifest Manifest
	posts    []string
	replies  []string
	reacts   []string
	menus    []string
}

func (g *recGateway) Manifest() Manifest { return g.manifest }
func (g *recGateway) Post(_ context.Context, _ Conversation, text string) (MessageID, error) {
	g.posts = append(g.posts, text)
	return "mid", nil
}
func (g *recGateway) Reply(_ context.Context, _ Conversation, _ MessageID, text string) (MessageID, error) {
	g.replies = append(g.replies, text)
	return "mid", nil
}
func (g *recGateway) React(_ context.Context, _ Conversation, _ MessageID, emoji string) error {
	g.reacts = append(g.reacts, emoji)
	return nil
}
func (g *recGateway) Menu(_ context.Context, _ Conversation, _ MessageID, prompt string, _ []Choice) error {
	g.menus = append(g.menus, prompt)
	return nil
}

// Compile-time proof the recorder satisfies the port.
var _ Gateway = (*recGateway)(nil)

func TestInboundCarriesConversation(t *testing.T) {
	in := Inbound{Conversation: Conversation{Gateway: "discord", ID: "c1"}, Author: "leo", Text: "hi"}
	if in.Conversation.ID != "c1" || in.Author != "leo" {
		t.Fatalf("unexpected inbound %+v", in)
	}
}

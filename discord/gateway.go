package discord

import (
	"context"

	"github.com/Akayashuu/dctl"
	"github.com/Akayashuu/herrscher/contracts"
)

// client is the subset of *dctl.Client the adapter needs (injected for tests).
type client interface {
	Send(ctx context.Context, channelID, content string) (*dctl.Message, error)
	Reply(ctx context.Context, channelID, replyTo, content string) (*dctl.Message, error)
	React(ctx context.Context, channelID, messageID, emoji string) error
	SendSelectMenu(ctx context.Context, channelID, replyTo, content, customID string, options []dctl.SelectOption) (*dctl.Message, error)
}

// Gateway adapts the Discord REST client to contracts.Gateway.
type Gateway struct{ c client }

func NewGateway(c client) *Gateway { return &Gateway{c: c} }

func (g *Gateway) Manifest() contracts.Manifest {
	return contracts.Manifest{
		Kind:         "discord",
		Category:     contracts.CategoryGateway,
		Capabilities: contracts.Capabilities{Reactions: true, SelectMenus: true, Replies: true},
	}
}

func (g *Gateway) Post(ctx context.Context, conv contracts.Conversation, text string) (contracts.MessageID, error) {
	m, err := g.c.Send(ctx, conv.ID, text)
	return msgID(m), err
}

func (g *Gateway) Reply(ctx context.Context, conv contracts.Conversation, replyTo contracts.MessageID, text string) (contracts.MessageID, error) {
	m, err := g.c.Reply(ctx, conv.ID, string(replyTo), text)
	return msgID(m), err
}

func (g *Gateway) React(ctx context.Context, conv contracts.Conversation, msg contracts.MessageID, emoji string) error {
	return g.c.React(ctx, conv.ID, string(msg), emoji)
}

func (g *Gateway) Menu(ctx context.Context, conv contracts.Conversation, replyTo contracts.MessageID, prompt string, opts []contracts.Choice) error {
	out := make([]dctl.SelectOption, 0, len(opts))
	for _, o := range opts {
		out = append(out, dctl.SelectOption{Label: o.Label, Value: o.Value})
	}
	// customID carries the session so a click routes back to its bridge. The
	// bridge wires the real session in Task 13; conv.ID is the default here.
	_, err := g.c.SendSelectMenu(ctx, conv.ID, string(replyTo), prompt, ChoiceCustomID(conv.ID), out)
	return err
}

func msgID(m *dctl.Message) contracts.MessageID {
	if m == nil {
		return ""
	}
	return contracts.MessageID(m.ID)
}

package kernel

import (
	"context"
	"fmt"
	"strings"
)

// Degrade wraps a Gateway and rabats actions the plugin does not announce. The
// manager always calls the rich method; degradation lives here, never in the
// domain.
func Degrade(g Gateway) Gateway { return degrading{g} }

type degrading struct{ g Gateway }

func (d degrading) Manifest() Manifest { return d.g.Manifest() }

func (d degrading) Post(ctx context.Context, conv Conversation, text string) (MessageID, error) {
	return d.g.Post(ctx, conv, text)
}

func (d degrading) Reply(ctx context.Context, conv Conversation, replyTo MessageID, text string) (MessageID, error) {
	if !d.g.Manifest().Capabilities.Replies {
		return d.g.Post(ctx, conv, text)
	}
	return d.g.Reply(ctx, conv, replyTo, text)
}

func (d degrading) React(ctx context.Context, conv Conversation, msg MessageID, emoji string) error {
	if !d.g.Manifest().Capabilities.Reactions {
		return nil
	}
	return d.g.React(ctx, conv, msg, emoji)
}

func (d degrading) Menu(ctx context.Context, conv Conversation, replyTo MessageID, prompt string, opts []Choice) error {
	if !d.g.Manifest().Capabilities.SelectMenus {
		var b strings.Builder
		b.WriteString(prompt)
		for i, o := range opts {
			fmt.Fprintf(&b, "\n%d. %s", i+1, o.Label)
		}
		_, err := d.g.Post(ctx, conv, b.String())
		return err
	}
	return d.g.Menu(ctx, conv, replyTo, prompt, opts)
}

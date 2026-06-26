package host

import (
	"context"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

// gatewayMaxLen is the hard per-message chunk limit for gateways that do not
// render themselves. Rich, gateway-specific rendering (progress, emojis, edit
// throttling) lives in the gateway behind contracts.EventSink.
const gatewayMaxLen = 2000

// gatewayRenderer is the minimal fallback for a gateway that does NOT implement
// EventSink: it posts only the final reply through the Gateway port, chunked.
type gatewayRenderer struct {
	gw   contracts.Gateway
	conv contracts.Conversation
}

func newGatewayRenderer(gw contracts.Gateway, ch string) *gatewayRenderer {
	return &gatewayRenderer{
		gw:   gw,
		conv: contracts.Conversation{Gateway: gw.Manifest().Kind, ID: ch},
	}
}

// handle posts the final reply; all other event kinds are ignored.
func (r *gatewayRenderer) handle(ctx context.Context, e contracts.Event) {
	if e.T != "reply" || !e.Done || e.Text == "" {
		return
	}
	for _, part := range chunk(e.Text, gatewayMaxLen) {
		_, _ = r.gw.Post(ctx, r.conv, part)
	}
}

// chunk splits s into pieces of at most max runes, preferring a newline break
// within the limit so multi-line output stays readable. It counts and slices in
// rune space (a single []rune pass, not a per-iteration byte scan) so multibyte
// UTF-8 (e.g. accented French text) is never split into invalid bytes and the
// limit is honoured in characters. This mirrors the gateway sink's chunker.
func chunk(s string, max int) []string {
	var out []string
	r := []rune(s)
	for len(r) > max {
		cut := max
		// Prefer the last newline within the limit, but only past the halfway
		// point so a stray early newline does not yield tiny chunks.
		for i := max - 1; i > max/2; i-- {
			if r[i] == '\n' {
				cut = i
				break
			}
		}
		out = append(out, string(r[:cut]))
		r = r[cut:]
		if len(r) > 0 && r[0] == '\n' {
			r = r[1:]
		}
	}
	if len(r) > 0 {
		out = append(out, string(r))
	}
	return out
}

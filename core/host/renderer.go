package host

import (
	"context"
	"strings"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

// nowFunc lets tests pin the progress view's clock.
var nowFunc = time.Now

// gatewayMaxLen is the hard per-message limit for chunking (Discord's 2000).
const gatewayMaxLen = 2000

// gatewayRenderer reproduces, on the daemon side, the rich gateway rendering the
// bridge used to do inline: a live progress view fed by status/chunk events and a
// threaded final reply. It uses only the contracts Gateway/ChannelReader ports,
// so the hub wraps any gateway that does NOT implement EventSink in one of these.
type gatewayRenderer struct {
	gw       contracts.Gateway
	reader   contracts.ChannelReader
	ch       string
	progress string
	conv     contracts.Conversation
	pv       *progressView
}

func newGatewayRenderer(gw contracts.Gateway, reader contracts.ChannelReader, ch, progress string) *gatewayRenderer {
	if progress == "" {
		progress = "full"
	}
	return &gatewayRenderer{
		gw:       gw,
		reader:   reader,
		ch:       ch,
		progress: progress,
		conv:     contracts.Conversation{Gateway: gw.Manifest().Kind, ID: ch},
	}
}

// handle renders one turn event onto the gateway. human opens a progress view;
// status/chunk feed it; reply finishes it and posts the result.
func (r *gatewayRenderer) handle(ctx context.Context, e contracts.Event) {
	switch e.T {
	case "human":
		if r.progress != "off" && r.reader != nil {
			post := func(id, content string) (string, error) {
				return r.reader.UpsertStatusMessage(ctx, r.ch, id, content)
			}
			r.pv = newProgressView(post, r.progress, false, nowFunc())
		}
	case "status":
		if r.pv != nil {
			tool, detail := splitTool(e.Text)
			r.pv.add(contracts.BackendEvent{Kind: "tool", Tool: tool, Detail: detail})
		}
	case "chunk":
		if r.pv != nil {
			r.pv.add(contracts.BackendEvent{Kind: "text", Detail: e.Text})
		}
	case "reset":
		if r.pv != nil {
			r.pv.finish(true)
			r.pv = nil
		}
	case "reply":
		if e.Done {
			if e.Text != "" {
				for _, part := range chunk(e.Text, gatewayMaxLen) {
					_, _ = r.gw.Post(ctx, r.conv, part)
				}
			}
			if r.pv != nil {
				r.pv.finish(false)
				r.pv = nil
			}
		}
	}
}

// splitTool recovers the tool name and detail from a status line the bridge
// emitted as "Tool Detail" (see bridge.emitBackendEvent), so the progress view
// can group and icon by tool name rather than collapsing everything under "".
func splitTool(s string) (tool, detail string) {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, ' '); i >= 0 {
		return s[:i], strings.TrimSpace(s[i+1:])
	}
	return s, ""
}

// chunk splits s into pieces no longer than max, preferring to break on a
// newline boundary so multi-line command output stays readable.
func chunk(s string, max int) []string {
	var out []string
	for len(s) > max {
		cut := max
		if nl := strings.LastIndexByte(s[:max], '\n'); nl > max/2 {
			cut = nl
		}
		out = append(out, s[:cut])
		s = strings.TrimPrefix(s[cut:], "\n")
	}
	if s != "" {
		out = append(out, s)
	}
	return out
}

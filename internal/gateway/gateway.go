package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/coder/websocket"
	"github.com/Akayashuu/dctl"
	"github.com/Akayashuu/herrscher/internal/health"
)

const gatewayURL = "wss://gateway.discord.gg/?v=10&encoding=json"

// intentGuilds is the only intent we need (interactions don't require message intents).
const intentGuilds = 1 << 0

type gwPayload struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d"`
	S  int             `json:"s,omitempty"`
	T  string          `json:"t,omitempty"`
}

// Gateway maintains the bot's websocket connection (its online presence) and
// surfaces INTERACTION_CREATE events on Interactions. It records heartbeat ACKs
// into Health (when non-nil) so liveness reflects pure transport state.
type Gateway struct {
	c            *dctl.Client
	token        string
	Interactions chan dctl.Interaction
	Health       *health.Health
}

// NewGateway builds a Gateway for client c, authenticating the websocket
// IDENTIFY with token (the same bot token c was built with). h may be nil.
func NewGateway(c *dctl.Client, token string, h *health.Health) *Gateway {
	return &Gateway{c: c, token: token, Interactions: make(chan dctl.Interaction, 16), Health: h}
}

// Run connects and processes events until ctx is cancelled or the connection
// drops. On connection loss it returns an error; the caller reconnects.
func (g *Gateway) Run(ctx context.Context) error {
	if !g.c.Enabled() {
		return dctl.ErrDisabled
	}
	conn, _, err := websocket.Dial(ctx, gatewayURL, nil)
	if err != nil {
		return err
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	conn.SetReadLimit(1 << 20)

	// First frame: Hello (op 10) with heartbeat_interval.
	var hello struct {
		HeartbeatInterval int `json:"heartbeat_interval"`
	}
	first, err := readPayload(ctx, conn)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(first.D, &hello); err != nil {
		return err
	}

	// Identify (op 2).
	identify := map[string]any{
		"op": 2,
		"d": map[string]any{
			"token":      g.token,
			"intents":    intentGuilds,
			"properties": map[string]any{"os": "linux", "browser": "dctl", "device": "dctl"},
		},
	}
	if err := writeJSON(ctx, conn, identify); err != nil {
		return err
	}

	// Heartbeat loop.
	hbCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		t := time.NewTicker(time.Duration(hello.HeartbeatInterval) * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-t.C:
				_ = writeJSON(hbCtx, conn, map[string]any{"op": 1, "d": nil})
			}
		}
	}()

	// Event loop.
	for {
		p, err := readPayload(ctx, conn)
		if err != nil {
			return err
		}
		if p.Op == 11 && g.Health != nil { // Heartbeat ACK
			g.Health.HeartbeatAck(time.Now())
		}
		if p.Op == 0 && p.T == "INTERACTION_CREATE" {
			var in dctl.Interaction
			if err := json.Unmarshal(p.D, &in); err == nil {
				select {
				case g.Interactions <- in:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
	}
}

func readPayload(ctx context.Context, conn *websocket.Conn) (gwPayload, error) {
	_, data, err := conn.Read(ctx)
	if err != nil {
		return gwPayload{}, err
	}
	var p gwPayload
	if err := json.Unmarshal(data, &p); err != nil {
		return gwPayload{}, fmt.Errorf("gateway decode: %w", err)
	}
	return p, nil
}

func writeJSON(ctx context.Context, conn *websocket.Conn, v any) error {
	buf, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, buf)
}

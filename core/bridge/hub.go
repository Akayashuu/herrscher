package bridge

import (
	"context"
	"fmt"
	"strings"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher/core/internal/control"
)

// runHub is the pure-runner loop: it dials the hub control socket, reads input
// frames, and drives the backend one turn at a time, emitting events back over
// the same connection. ctx cancellation returns its error.
func runHub(ctx context.Context, newBackend BackendFactory, orch contracts.Orchestrator, o Options) error {
	resp, err := newBackend(o.Channel)
	if err != nil {
		return fmt.Errorf("backend: %w", err)
	}
	defer resp.Close()

	conn, err := control.Dial(o.HubSocket)
	if err != nil {
		return fmt.Errorf("dial hub socket %s: %w", o.HubSocket, err)
	}
	defer conn.Close()

	// The hub frames inputs as JSON-line Events; surface them on a channel the
	// turn driver consumes. Scan returns when the hub closes the conn (daemon
	// gone or session closed) → the bridge exits and the supervisor decides.
	in := make(chan contracts.Event)
	go func() {
		defer close(in)
		_ = conn.Scan(func(e contracts.Event) error {
			select {
			case in <- e:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()

	runHubTurns(ctx, in, conn, resp, orch)
	return ctx.Err()
}

// runHubTurns serially drains input frames, running one backend turn per
// input/pick. It is split from runHub so it can be unit-tested over an
// in-memory channel + sink without a real socket. FIFO is inherent: the hub
// sends the next input only after it sees this turn's reply{done}, and this
// loop processes one frame at a time anyway.
func runHubTurns(ctx context.Context, in <-chan contracts.Event, sink contracts.EventSink, resp contracts.Backend, orch contracts.Orchestrator) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-in:
			if !ok {
				return
			}
			switch ev.T {
			case "pick":
				runPick(ctx, sink, resp, ev.Value)
			default: // "input" (and any human-origin frame)
				runOneTurn(ctx, sink, resp, orch, ev)
			}
		}
	}
}

// runOneTurn runs a single backend turn for an input frame, streaming chunk/
// status events and a terminal reply{done}. An empty output still emits
// reply{done} so the hub's FIFO can advance.
func runOneTurn(ctx context.Context, sink contracts.EventSink, resp contracts.Backend, orch contracts.Orchestrator, ev contracts.Event) {
	var memCtx string
	if orch != nil {
		memCtx = orch.Context(ctx)
	}
	prompt := contracts.Prompt{Content: ev.Text, Context: memCtx, Author: ev.Who}
	var cost float64
	onEvent := func(be contracts.BackendEvent) {
		if be.Kind == "result" {
			cost = be.Cost
		}
		emitBackendEvent(sink, be)
	}
	out, err := resp.Respond(ctx, prompt, onEvent)
	if err != nil && out == "" {
		out = "⚠️ " + err.Error()
	}
	out = strings.TrimSpace(out)
	sink.Emit(contracts.Event{T: "reply", Text: out, Done: true, Cost: cost})
	if orch != nil {
		_ = orch.Observe(ctx, prompt, out)
	}
}

// runPick answers a routed select-menu pick out-of-band (serialized with turns
// by runHubTurns), emitting whatever the backend produces as a reply{done}.
func runPick(ctx context.Context, sink contracts.EventSink, resp contracts.Backend, value string) {
	inj, ok := resp.(contracts.ChoiceInjector)
	if !ok {
		return
	}
	out, err := inj.InjectChoice(ctx, value)
	if err != nil {
		out = "⚠️ " + err.Error()
	}
	sink.Emit(contracts.Event{T: "reply", Text: strings.TrimSpace(out), Done: true})
}

// emitBackendEvent maps a backend progress event onto the bus vocabulary: text
// → chunk, tool → status (dropped when empty), reset → reset; others (result)
// carry no transcript and are dropped. Mirrors the relocated runner.emitBackend.
func emitBackendEvent(sink contracts.EventSink, be contracts.BackendEvent) {
	switch be.Kind {
	case "text":
		sink.Emit(contracts.Event{T: "chunk", Text: be.Detail})
	case "tool":
		if text := strings.TrimSpace(be.Tool + " " + be.Detail); text != "" {
			sink.Emit(contracts.Event{T: "status", Text: text})
		}
	case "reset":
		sink.Emit(contracts.Event{T: "reset"})
	}
}

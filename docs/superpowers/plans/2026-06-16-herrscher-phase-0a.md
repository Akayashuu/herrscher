# Herrscher Phase 0a Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Poser les frontières plugin in-process dans le monolithe dctl — un `kernel/` pur (entities + ports + contrat de plugin), un adaptateur `discord/` implémentant le port `Gateway`, un `Registry` de plugins, une identité de session stable, et l'émission sortante routée via le port — sans changer le comportement utilisateur.

**Architecture:** Hexagonal. `kernel/` est le centre pur (aucun import I/O) : `Conversation`, `Session*` identités, port `Gateway`, `Manifest`, `Registry`, `Capabilities`, décorateur de dégradation. `discord/` est l'adaptateur (enveloppe le client `dctl` racine) qui implémente `kernel.Gateway` et porte le domaine sorti de la racine (catalogue de commandes, convention de custom_id). Le package racine `dctl` redevient un client Discord pur. Renommages `handler→manager` / `serve→host` reportés à 0b.

**Tech Stack:** Go 1.x, module `github.com/vskstudio/dctl`, lib standard uniquement. Tests `go test`. Le build doit rester vert et les 296 tests existants verts à chaque commit.

---

## File Structure

- **Create** `kernel/conversation.go` — `Conversation`, `SessionID`, `MessageID`, `Choice`, `Attachment`.
- **Create** `kernel/manifest.go` — `Category` + constantes, `Capabilities`, `Manifest`.
- **Create** `kernel/gateway.go` — port `Gateway`, `Inbound`.
- **Create** `kernel/registry.go` — `Registry` (RegisterGateway / Gateways).
- **Create** `kernel/degrade.go` — `Degrade()` + `degrading`.
- **Create** `kernel/*_test.go` — tests de chaque unité.
- **Create** `discord/commands.go` — `Commands()` (ex-`dctlCommands`), `RegisterCommands(ctx, client)`.
- **Create** `discord/choice.go` — `ChoiceCustomID` / `ParseChoiceCustomID`.
- **Create** `discord/gateway.go` — adaptateur `kernel.Gateway` sur un sous-ensemble du client `dctl`.
- **Create** `discord/*_test.go` — tests adaptateur + catalogue.
- **Create** `purity_test.go` (package `dctl`, racine) — garde-fou d'imports.
- **Modify** `components.go` — renommer `SendChoiceMenu` → `SendSelectMenu` ; retirer `ChoiceCustomID`/`ParseChoiceCustomID`.
- **Modify** `interactions.go` — retirer `dctlCommands` + le wiring `RegisterCommands` spécifique.
- **Modify** `internal/state/state.go` — champ `Session.ID` + génération + migration au chargement.
- **Modify** `internal/serve/serve.go` — construire le `Registry`, enregistrer le plugin gateway Discord, router `handleComponent` via `discord.ParseChoiceCustomID`, `RegisterCommands` via `discord`.
- **Modify** `internal/bridge/bridge.go` — émettre via `kernel.Gateway` (adaptateur Discord + `Degrade`).

---

## Task 1: kernel — identités & types de base

**Files:**
- Create: `kernel/conversation.go`
- Test: `kernel/conversation_test.go`

- [ ] **Step 1: Write the failing test**

```go
package kernel

import "testing"

func TestConversationValue(t *testing.T) {
	a := Conversation{Gateway: "discord", ID: "123"}
	b := Conversation{Gateway: "discord", ID: "123"}
	if a != b {
		t.Fatalf("equal conversations should compare equal")
	}
	if (Conversation{Gateway: "discord", ID: "123"}) == (Conversation{Gateway: "telegram", ID: "123"}) {
		t.Fatalf("different gateways must not be equal")
	}
}

func TestChoiceFields(t *testing.T) {
	c := Choice{Label: "Yes", Value: "y"}
	if c.Label != "Yes" || c.Value != "y" {
		t.Fatalf("unexpected choice %+v", c)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./kernel/`
Expected: FAIL — `undefined: Conversation` (package doesn't compile).

- [ ] **Step 3: Write minimal implementation**

```go
package kernel

// Conversation is an opaque address into a chat platform. Comparable so it can
// key a map (Conversation -> SessionID).
type Conversation struct {
	Gateway string
	ID      string
}

type (
	SessionID string
	MessageID string
)

type Choice struct {
	Label string
	Value string
}

type Attachment struct {
	Filename string
	URL      string
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./kernel/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add kernel/conversation.go kernel/conversation_test.go
git commit -m "feat(kernel): conversation, session/message ids, choice, attachment"
```

---

## Task 2: kernel — Manifest, Capabilities, Category

**Files:**
- Create: `kernel/manifest.go`
- Test: `kernel/manifest_test.go`

- [ ] **Step 1: Write the failing test**

```go
package kernel

import "testing"

func TestManifestCarriesCapabilities(t *testing.T) {
	m := Manifest{
		Kind:         "discord",
		Category:     CategoryGateway,
		Capabilities: Capabilities{Reactions: true, SelectMenus: true, Replies: true},
	}
	if m.Category != "gateway" {
		t.Fatalf("CategoryGateway should be %q, got %q", "gateway", m.Category)
	}
	if !m.Capabilities.SelectMenus {
		t.Fatalf("capabilities not carried by manifest")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./kernel/ -run TestManifest`
Expected: FAIL — `undefined: Manifest`.

- [ ] **Step 3: Write minimal implementation**

```go
package kernel

type Category string

const (
	CategoryGateway      Category = "gateway"
	CategoryBackend      Category = "backend"
	CategoryMemory       Category = "memory"
	CategoryOrchestrator Category = "orchestrator"
)

// Capabilities are announced by a plugin. The degrading decorator reads them to
// rabat unsupported actions. This is the single source of truth (no separate
// Capabilities() method on the port).
type Capabilities struct {
	Reactions   bool
	SelectMenus bool
	Replies     bool
}

// Manifest is what a plugin announces about itself. In Phase 1 this becomes the
// payload of the NATS self-registration; the shape stays identical.
type Manifest struct {
	Kind         string
	Category     Category
	Capabilities Capabilities
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./kernel/ -run TestManifest`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add kernel/manifest.go kernel/manifest_test.go
git commit -m "feat(kernel): manifest, capabilities, plugin categories"
```

---

## Task 3: kernel — port Gateway + Inbound

**Files:**
- Create: `kernel/gateway.go`
- Test: `kernel/gateway_test.go`

- [ ] **Step 1: Write the failing test**

```go
package kernel

import (
	"context"
	"testing"
)

// recGateway records calls; used across kernel tests.
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./kernel/ -run TestInbound`
Expected: FAIL — `undefined: Gateway` / `undefined: Inbound`.

- [ ] **Step 3: Write minimal implementation**

```go
package kernel

import "context"

// Gateway is the port a chat-platform plugin implements. Method-based: the
// manager calls the rich method and the degrading decorator rabats when a
// capability is missing.
type Gateway interface {
	Manifest() Manifest
	Post(ctx context.Context, conv Conversation, text string) (MessageID, error)
	Reply(ctx context.Context, conv Conversation, replyTo MessageID, text string) (MessageID, error)
	React(ctx context.Context, conv Conversation, msg MessageID, emoji string) error
	Menu(ctx context.Context, conv Conversation, replyTo MessageID, prompt string, opts []Choice) error
}

// Inbound is a message arriving from a gateway into the manager.
type Inbound struct {
	Conversation Conversation
	Author       string
	Text         string
	Attachments  []Attachment
	MessageID    MessageID
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./kernel/`
Expected: PASS (recGateway + Inbound now compile).

- [ ] **Step 5: Commit**

```bash
git add kernel/gateway.go kernel/gateway_test.go
git commit -m "feat(kernel): Gateway port and Inbound message"
```

---

## Task 4: kernel — Registry

**Files:**
- Create: `kernel/registry.go`
- Test: `kernel/registry_test.go`

- [ ] **Step 1: Write the failing test**

```go
package kernel

import "testing"

func TestRegistryCollectsGateways(t *testing.T) {
	var r Registry
	if len(r.Gateways()) != 0 {
		t.Fatalf("fresh registry should be empty")
	}
	g := &recGateway{manifest: Manifest{Kind: "discord", Category: CategoryGateway}}
	r.RegisterGateway(g)
	got := r.Gateways()
	if len(got) != 1 || got[0].Manifest().Kind != "discord" {
		t.Fatalf("registry did not return the registered gateway: %+v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./kernel/ -run TestRegistry`
Expected: FAIL — `undefined: Registry`.

- [ ] **Step 3: Write minimal implementation**

```go
package kernel

// Registry is held by the host. Plugins register into it; the host queries by
// category. In Phase 1 the in-process registration becomes NATS
// self-registration with the same Manifest and the same query surface.
type Registry struct {
	gateways []Gateway
}

func (r *Registry) RegisterGateway(g Gateway) { r.gateways = append(r.gateways, g) }
func (r *Registry) Gateways() []Gateway       { return r.gateways }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./kernel/ -run TestRegistry`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add kernel/registry.go kernel/registry_test.go
git commit -m "feat(kernel): plugin registry (gateways)"
```

---

## Task 5: kernel — décorateur de dégradation

**Files:**
- Create: `kernel/degrade.go`
- Test: `kernel/degrade_test.go`

- [ ] **Step 1: Write the failing test**

```go
package kernel

import (
	"context"
	"strings"
	"testing"
)

func full() Capabilities  { return Capabilities{Reactions: true, SelectMenus: true, Replies: true} }
func none() Capabilities  { return Capabilities{} }

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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./kernel/ -run TestDegrade`
Expected: FAIL — `undefined: Degrade`.

- [ ] **Step 3: Write minimal implementation**

```go
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./kernel/`
Expected: PASS (all kernel tests).

- [ ] **Step 5: Commit**

```bash
git add kernel/degrade.go kernel/degrade_test.go
git commit -m "feat(kernel): capability-degrading gateway decorator"
```

---

## Task 6: racine — renommer SendChoiceMenu → SendSelectMenu

The select menu is generic Discord; only its name leaked the domain. Rename it (keep `SelectOption`), update the sole caller.

**Files:**
- Modify: `components.go:55-70`
- Modify: `internal/bridge/bridge.go:269`

- [ ] **Step 1: Rename the method**

In `components.go`, change the signature and doc:

```go
// SendSelectMenu posts content with a single-select dropdown. When replyTo is set
// the message threads under it; customID routes the click back to a session.
func (c *Client) SendSelectMenu(ctx context.Context, channelID, replyTo, content, customID string, options []SelectOption) (*Message, error) {
```

(body unchanged)

- [ ] **Step 2: Update the caller**

In `internal/bridge/bridge.go:269`, change `c.SendChoiceMenu(` to `c.SendSelectMenu(`:

```go
				if _, err := c.SendSelectMenu(ctx, ch, replyTo, out, dctl.ChoiceCustomID(o.Session), opts); err != nil {
```

- [ ] **Step 3: Build & test**

Run: `go build ./... && go test ./...`
Expected: PASS — pure rename, 296 tests still green.

- [ ] **Step 4: Commit**

```bash
git add components.go internal/bridge/bridge.go
git commit -m "refactor(dctl): rename SendChoiceMenu to generic SendSelectMenu"
```

---

## Task 7: discord/ — déplacer le catalogue de commandes hors de la racine

Move `dctlCommands()` and the dctl-specific `RegisterCommands` wiring into the new `discord/` package. The root keeps only a *generic* command registration helper.

**Files:**
- Create: `discord/commands.go`
- Modify: `interactions.go:144-246` (remove `dctlCommands`; generalise `RegisterCommands`)
- Modify: `internal/serve/serve.go:219`
- Test: `discord/commands_test.go`

- [ ] **Step 1: Write the failing test**

```go
package discord

import "testing"

func TestCommandsCatalogHasSession(t *testing.T) {
	cmds := Commands()
	var names []string
	for _, c := range cmds {
		names = append(names, c["name"].(string))
	}
	want := map[string]bool{"set": true, "session": true, "workspace": true, "service": true, "allow": true}
	for n := range want {
		found := false
		for _, got := range names {
			if got == n {
				found = true
			}
		}
		if !found {
			t.Fatalf("command catalog missing %q (got %v)", n, names)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./discord/`
Expected: FAIL — package `discord` / `Commands` undefined.

- [ ] **Step 3: Move the catalog**

Create `discord/commands.go` with the catalog (moved verbatim from `interactions.go:164-246`, renamed exported `Commands`):

```go
package discord

import (
	"context"

	"github.com/vskstudio/dctl"
)

// Commands is the declarative slash-command set for the Discord gateway.
func Commands() []map[string]any {
	const (
		typeSub   = 1
		typeGroup = 2
		typeStr   = 3
		typeBool  = 5
		typeUser  = 6
		typeChan  = 7
	)
	return []map[string]any{
		// ... (move the full slice body from interactions.go dctlCommands verbatim) ...
	}
}

// RegisterCommands registers the Discord gateway's slash commands for the sole guild.
func RegisterCommands(ctx context.Context, c *dctl.Client) error {
	return c.RegisterCommands(ctx, Commands())
}
```

- [ ] **Step 4: Generalise the root helper**

In `interactions.go`, delete `dctlCommands()` and change `RegisterCommands` to take the catalogue as a parameter:

```go
// RegisterCommands (re)registers the given guild-scoped slash command set for the
// sole guild (guild-scoped commands appear instantly, unlike global ones).
func (c *Client) RegisterCommands(ctx context.Context, commands []map[string]any) error {
	appID, err := c.AppID(ctx)
	if err != nil {
		return err
	}
	g, err := c.SoleGuild(ctx)
	if err != nil {
		return err
	}
	req, err := c.newRequest(ctx, http.MethodPut,
		"/applications/"+appID+"/guilds/"+g.ID+"/commands", commands)
	if err != nil {
		return err
	}
	return c.do(req, nil)
}
```

- [ ] **Step 5: Update the daemon wiring**

In `internal/serve/serve.go`, add the import `"github.com/vskstudio/dctl/discord"` and change line 219:

```go
	if err := discord.RegisterCommands(ctx, c); err != nil {
		return fmt.Errorf("register commands: %w", err)
	}
```

- [ ] **Step 6: Build & test**

Run: `go build ./... && go test ./...`
Expected: PASS — catalog moved, wiring updated, behavior unchanged.

- [ ] **Step 7: Commit**

```bash
git add discord/commands.go discord/commands_test.go interactions.go internal/serve/serve.go
git commit -m "refactor: move slash-command catalog into discord gateway package"
```

---

## Task 8: discord/ — déplacer la convention de custom_id

**Files:**
- Create: `discord/choice.go`
- Modify: `components.go:9-22` (remove `choiceCustomIDPrefix`, `ChoiceCustomID`, `ParseChoiceCustomID`)
- Modify: `internal/serve/serve.go:139` (`dctl.ParseChoiceCustomID` → `discord.ParseChoiceCustomID`)
- Modify: `internal/bridge/bridge.go:269` (`dctl.ChoiceCustomID` → `discord.ChoiceCustomID`)
- Test: `discord/choice_test.go`

- [ ] **Step 1: Write the failing test**

```go
package discord

import "testing"

func TestChoiceCustomIDRoundTrip(t *testing.T) {
	id := ChoiceCustomID("mysession")
	name, ok := ParseChoiceCustomID(id)
	if !ok || name != "mysession" {
		t.Fatalf("round trip failed: id=%q name=%q ok=%v", id, name, ok)
	}
	if _, ok := ParseChoiceCustomID("not-a-choice"); ok {
		t.Fatalf("non-choice custom_id must report ok=false")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./discord/ -run TestChoiceCustomID`
Expected: FAIL — `undefined: ChoiceCustomID`.

- [ ] **Step 3: Move the convention**

Create `discord/choice.go` (moved verbatim from `components.go:9-22`):

```go
package discord

import "strings"

const choiceCustomIDPrefix = "dctlchoice:"

// ChoiceCustomID builds the custom_id carried by a session's choice select menu.
func ChoiceCustomID(session string) string { return choiceCustomIDPrefix + session }

// ParseChoiceCustomID extracts the session name from a choice-menu custom_id and
// reports whether the id is a choice menu at all.
func ParseChoiceCustomID(id string) (string, bool) {
	return strings.CutPrefix(id, choiceCustomIDPrefix)
}
```

Delete those three declarations from `components.go` (the `strings` import there is still used by `clamp`? check: `clamp` uses `[]rune`, not `strings`. Remove the now-unused `strings` import from `components.go` if nothing else uses it).

- [ ] **Step 4: Update call sites**

`internal/serve/serve.go:139`:

```go
	sess, ok := discord.ParseChoiceCustomID(in.Data.CustomID)
```

`internal/bridge/bridge.go:269`:

```go
				if _, err := c.SendSelectMenu(ctx, ch, replyTo, out, discord.ChoiceCustomID(o.Session), opts); err != nil {
```

Add `"github.com/vskstudio/dctl/discord"` to `internal/bridge/bridge.go` imports (serve.go already imported it in Task 7).

- [ ] **Step 5: Build & test**

Run: `go build ./... && go test ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add discord/choice.go discord/choice_test.go components.go internal/serve/serve.go internal/bridge/bridge.go
git commit -m "refactor: move choice custom_id convention into discord package"
```

---

## Task 9: garde-fou — la racine n'importe aucun domaine

Lock the purification with a test that fails if the root package re-acquires a domain dependency.

**Files:**
- Create: `purity_test.go` (package `dctl`, repo root)

- [ ] **Step 1: Write the test**

```go
package dctl

import (
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// The root dctl package must stay a pure Discord client: it may not import any
// project package (kernel, discord, internal/...). This guards the invariant
// "dctl is usable without the core".
func TestRootImportsNoDomain(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, e.Name(), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			if strings.HasPrefix(p, "github.com/vskstudio/dctl/") {
				t.Errorf("%s imports domain package %q — root must stay a pure client", e.Name(), p)
			}
		}
	}
}
```

- [ ] **Step 2: Run test to verify it passes**

Run: `go test . -run TestRootImportsNoDomain`
Expected: PASS (Tasks 6–8 already removed the leaks).

- [ ] **Step 3: Commit**

```bash
git add purity_test.go
git commit -m "test(dctl): guard root package against domain imports"
```

---

## Task 10: discord/ — adaptateur kernel.Gateway

Implement `kernel.Gateway` over a minimal subset of `*dctl.Client`, so it is testable with a fake.

**Files:**
- Create: `discord/gateway.go`
- Test: `discord/gateway_test.go`

- [ ] **Step 1: Write the failing test**

```go
package discord

import (
	"context"
	"testing"

	"github.com/vskstudio/dctl"
	"github.com/vskstudio/dctl/kernel"
)

type fakeClient struct {
	sent     []string
	replied  []string
	reacted  []string
	menus    []string
}

func (f *fakeClient) Send(_ context.Context, _ , content string) (*dctl.Message, error) {
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

var _ kernel.Gateway = (*Gateway)(nil)

func TestGatewayManifest(t *testing.T) {
	g := NewGateway(&fakeClient{})
	m := g.Manifest()
	if m.Kind != "discord" || m.Category != kernel.CategoryGateway {
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
	conv := kernel.Conversation{Gateway: "discord", ID: "chan"}

	_, _ = g.Post(ctx, conv, "hello")
	_, _ = g.Reply(ctx, conv, "mid", "answer")
	_ = g.React(ctx, conv, "mid", "👀")
	_ = g.Menu(ctx, conv, "mid", "pick", []kernel.Choice{{Label: "A", Value: "a"}})

	if len(fc.sent) != 1 || len(fc.replied) != 1 || len(fc.reacted) != 1 || len(fc.menus) != 1 {
		t.Fatalf("translation incomplete: %+v", fc)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./discord/ -run TestGateway`
Expected: FAIL — `undefined: Gateway` / `NewGateway`.

- [ ] **Step 3: Write the adapter**

```go
package discord

import (
	"context"

	"github.com/vskstudio/dctl"
	"github.com/vskstudio/dctl/kernel"
)

// client is the subset of *dctl.Client the adapter needs (injected for tests).
type client interface {
	Send(ctx context.Context, channelID, content string) (*dctl.Message, error)
	Reply(ctx context.Context, channelID, replyTo, content string) (*dctl.Message, error)
	React(ctx context.Context, channelID, messageID, emoji string) error
	SendSelectMenu(ctx context.Context, channelID, replyTo, content, customID string, options []dctl.SelectOption) (*dctl.Message, error)
}

// Gateway adapts the Discord REST client to kernel.Gateway.
type Gateway struct{ c client }

func NewGateway(c client) *Gateway { return &Gateway{c: c} }

func (g *Gateway) Manifest() kernel.Manifest {
	return kernel.Manifest{
		Kind:         "discord",
		Category:     kernel.CategoryGateway,
		Capabilities: kernel.Capabilities{Reactions: true, SelectMenus: true, Replies: true},
	}
}

func (g *Gateway) Post(ctx context.Context, conv kernel.Conversation, text string) (kernel.MessageID, error) {
	m, err := g.c.Send(ctx, conv.ID, text)
	return msgID(m), err
}

func (g *Gateway) Reply(ctx context.Context, conv kernel.Conversation, replyTo kernel.MessageID, text string) (kernel.MessageID, error) {
	m, err := g.c.Reply(ctx, conv.ID, string(replyTo), text)
	return msgID(m), err
}

func (g *Gateway) React(ctx context.Context, conv kernel.Conversation, msg kernel.MessageID, emoji string) error {
	return g.c.React(ctx, conv.ID, string(msg), emoji)
}

func (g *Gateway) Menu(ctx context.Context, conv kernel.Conversation, replyTo kernel.MessageID, prompt string, opts []kernel.Choice) error {
	out := make([]dctl.SelectOption, 0, len(opts))
	for _, o := range opts {
		out = append(out, dctl.SelectOption{Label: o.Label, Value: o.Value})
	}
	// customID carries the session so a click routes back to its bridge. The
	// session is resolved by the caller in 0a (see bridge wiring, Task 13).
	_, err := g.c.SendSelectMenu(ctx, conv.ID, string(replyTo), prompt, ChoiceCustomID(conv.ID), out)
	return err
}

func msgID(m *dctl.Message) kernel.MessageID {
	if m == nil {
		return ""
	}
	return kernel.MessageID(m.ID)
}
```

> NOTE on `Menu` custom_id: in 0a the bridge still owns the session→customID mapping (Task 13 passes the session through). The adapter's `ChoiceCustomID(conv.ID)` is a default; Task 13 wires the real session name. If `dctl.Client` method signatures differ from the `client` interface above (param order/names), align the interface to the real methods — do not change `dctl`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./discord/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add discord/gateway.go discord/gateway_test.go
git commit -m "feat(discord): kernel.Gateway adapter over the dctl client"
```

---

## Task 11: state — identité de session stable

**Files:**
- Modify: `internal/state/state.go:18-31` (add `ID`)
- Modify: state load path (where sessions are read from disk) — add migration
- Test: `internal/state/state_test.go` (add cases)

- [ ] **Step 1: Write the failing test**

Add to `internal/state/state_test.go`:

```go
func TestLoadStateBackfillsSessionID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	// A legacy state.json with a session that has no "id".
	legacy := `{"sessions":[{"name":"alpha","channelID":"123","type":"text","cmd":"claude"}]}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	s, ok := st.FindSession("alpha")
	if !ok {
		t.Fatal("session alpha missing after load")
	}
	if s.ID == "" {
		t.Fatalf("legacy session should get a generated ID")
	}
}
```

(Use the imports already present in the test file: `os`, `path/filepath`, `testing`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/state/ -run TestLoadStateBackfills`
Expected: FAIL — `s.ID` undefined (field missing).

- [ ] **Step 3: Add the field**

In `internal/state/state.go`, add `ID` to `Session`:

```go
type Session struct {
	ID        string `json:"id,omitempty"` // stable logical id, decoupled from Name and ChannelID
	Name      string `json:"name"`
	ChannelID string `json:"channelID"`
	Type      string `json:"type"`
	Cmd       string `json:"cmd"`
	Backend   string `json:"backend,omitempty"`
	Worktree  string `json:"worktree,omitempty"`
	Project   string `json:"project,omitempty"`

	InitPrompts []string `json:"initPrompts,omitempty"`

	Allow        []string `json:"allow,omitempty"`
	Participants []string `json:"participants,omitempty"`
}
```

- [ ] **Step 4: Generate on load (migration) and on create**

Add a small generator (slug-based, deterministic-free is fine; use a counter+name to stay dependency-light):

```go
// newSessionID returns a stable id for a session. Name is already unique and
// git-safe, so it seeds a readable id; a short disambiguator keeps ids stable if
// a name is later reused.
func newSessionID(name string, existing map[string]bool) string {
	base := "s_" + name
	id := base
	for n := 1; existing[id]; n++ {
		id = fmt.Sprintf("%s_%d", base, n)
	}
	return id
}
```

In `LoadState`, after unmarshalling, backfill any session whose `ID == ""`:

```go
	seen := map[string]bool{}
	for i := range st.Sessions {
		if st.Sessions[i].ID != "" {
			seen[st.Sessions[i].ID] = true
		}
	}
	for i := range st.Sessions {
		if st.Sessions[i].ID == "" {
			st.Sessions[i].ID = newSessionID(st.Sessions[i].Name, seen)
			seen[st.Sessions[i].ID] = true
		}
	}
```

(Place this right after the existing sessions are loaded into `st`, before returning. Persist is not required here — the id is recomputed deterministically; a later `save` will write it.)

In `internal/handler/handler.go` session create (`state.Session{...}` literals at lines 427 and 436), set `ID: ""` — `AddSession` should backfill. Update `AddSession` to generate the id if empty:

```go
// In AddSession, before appending, if s.ID == "" assign one:
	if s.ID == "" {
		seen := map[string]bool{}
		for _, e := range s2.Sessions {
			seen[e.ID] = true
		}
		s.ID = newSessionID(s.Name, seen)
	}
```

(Adapt receiver/variable names to the real `AddSession` body.)

- [ ] **Step 5: Run tests**

Run: `go test ./internal/state/ ./internal/handler/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/state/state.go internal/state/state_test.go internal/handler/handler.go
git commit -m "feat(state): stable Session.ID with load-time backfill"
```

---

## Task 12: host (serve) — construire le Registry et enregistrer le plugin gateway

Wire the plugin registry into the daemon so Discord is a *registered* gateway, not a hard-wired one. The behavior is unchanged; this lands the discovery seam.

**Files:**
- Modify: `internal/serve/serve.go:174-235` (build registry, register discord gateway)
- Test: `internal/serve/serve_test.go` (add a registration test if the package has tests; otherwise assert via a small helper)

- [ ] **Step 1: Write the failing test**

Add `internal/serve/registry_test.go`:

```go
package serve

import (
	"testing"

	"github.com/vskstudio/dctl/kernel"
)

func TestBuildRegistryHasDiscordGateway(t *testing.T) {
	var r kernel.Registry
	registerPlugins(&r, nil) // nil client is fine: we only inspect manifests
	gws := r.Gateways()
	if len(gws) != 1 {
		t.Fatalf("expected 1 gateway, got %d", len(gws))
	}
	if gws[0].Manifest().Kind != "discord" {
		t.Fatalf("expected discord gateway, got %q", gws[0].Manifest().Kind)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/serve/ -run TestBuildRegistry`
Expected: FAIL — `undefined: registerPlugins`.

- [ ] **Step 3: Add the registration helper and call it**

In `internal/serve/serve.go`, add:

```go
// registerPlugins wires the in-process plugins into the registry. In Phase 1 this
// is replaced by NATS self-registration with the same Manifest.
func registerPlugins(r *kernel.Registry, c *dctl.Client) {
	r.RegisterGateway(discord.NewGateway(c))
}
```

Add imports `"github.com/vskstudio/dctl/discord"` (already added in Task 7) and `"github.com/vskstudio/dctl/kernel"`. In `Run`, after building the client and before the gateway loop, build the registry:

```go
	var registry kernel.Registry
	registerPlugins(&registry, c)
```

(0a only *registers* the gateway; the websocket connection still uses the concrete client directly. Using `registry.Gateways()` to drive emission lands incrementally in Task 13 / 0b.)

- [ ] **Step 4: Build & test**

Run: `go build ./... && go test ./internal/serve/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/serve/serve.go internal/serve/registry_test.go
git commit -m "feat(host): register discord as a plugin gateway in the registry"
```

---

## Task 13: bridge — émettre via kernel.Gateway

Route the bridge's outbound message emission (reply/send/menu + the ack/done/fail reactions) through `kernel.Gateway` built from the bridge's own client and wrapped in `Degrade`. Status-message upserts and ack-emoji removal (`Unreact`) stay direct (not modelled as outbound actions in 0a).

**Files:**
- Modify: `internal/bridge/bridge.go` (construct gateway; rewrite emission sites at lines ~209, ~240, ~249, and `postResult`)
- Test: `internal/bridge/bridge_test.go` (assert emission goes through a fake kernel.Gateway)

- [ ] **Step 1: Write the failing test**

Add to `internal/bridge/bridge_test.go` a test that drives `postResult` through a fake `kernel.Gateway` and asserts the reply path:

```go
func TestPostResultEmitsViaGateway(t *testing.T) {
	rec := &recGW{} // implements kernel.Gateway, records calls
	conv := kernel.Conversation{Gateway: "discord", ID: "chan"}
	postResultGW(context.Background(), rec, conv, "mid", "hello world", nil, Options{})
	if len(rec.replies) != 1 || rec.replies[0] != "hello world" {
		t.Fatalf("postResult should reply via the gateway: %+v", rec)
	}
}
```

Define `recGW` in the test file mirroring the `recGateway` shape from Task 3 (records `replies`, `posts`, `menus`, `reacts`; `Manifest()` returns full capabilities).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/bridge/ -run TestPostResultEmitsViaGateway`
Expected: FAIL — `undefined: postResultGW`.

- [ ] **Step 3: Introduce a gateway-based postResult**

Add `postResultGW` alongside the existing `postResult`, taking a `kernel.Gateway` and a `kernel.Conversation` instead of `*dctl.Client` + channel string:

```go
func postResultGW(ctx context.Context, gw kernel.Gateway, conv kernel.Conversation, replyTo, out string, resp session.Responder, o Options) {
	if o.ControlSocket != "" {
		if ca, ok := resp.(session.ChoiceAware); ok {
			if pc, has := ca.PendingChoice(); has {
				opts := make([]kernel.Choice, 0, len(pc.Options))
				for _, it := range pc.Options {
					opts = append(opts, kernel.Choice{Label: it.Label, Value: it.Value})
				}
				if err := gw.Menu(ctx, conv, kernel.MessageID(replyTo), out, opts); err != nil {
					logf(true, "choice menu post error: %v — falling back to text", err)
				} else {
					return
				}
			}
		}
	}
	for _, part := range chunk(out, discordMaxLen) {
		var err error
		if replyTo != "" {
			_, err = gw.Reply(ctx, conv, kernel.MessageID(replyTo), part)
		} else {
			_, err = gw.Post(ctx, conv, part)
		}
		if err != nil {
			logf(true, "reply error: %v", err)
		}
	}
}
```

- [ ] **Step 4: Build the gateway in the run loop and call the new path**

In the bridge run loop, construct the gateway once from the client and use it for emission. Near where `c` and `ch` are in scope:

```go
	gw := kernel.Degrade(discord.NewGateway(c))
	conv := kernel.Conversation{Gateway: "discord", ID: ch}
```

Replace the ack/done/fail reactions and the `postResult` call:

```go
	_ = gw.React(ctx, conv, kernel.MessageID(m.ID), ackEmoji)         // was c.React(...)
	...
	_ = c.Unreact(ctx, ch, m.ID, ackEmoji)                            // unchanged (direct)
	_ = gw.React(ctx, conv, kernel.MessageID(m.ID), failEmoji)        // was c.React(...)
	...
	postResultGW(ctx, gw, conv, m.ID, out, resp, o)                   // was postResult(ctx, c, ch, ...)
	...
	_ = c.Unreact(ctx, ch, m.ID, ackEmoji)                           // unchanged (direct)
	_ = gw.React(ctx, conv, kernel.MessageID(m.ID), doneEmoji)        // was c.React(...)
```

Add imports `"github.com/vskstudio/dctl/kernel"` and `"github.com/vskstudio/dctl/discord"` (discord already added in Task 8). Once `postResultGW` is the only caller, delete the old `postResult`.

> NOTE on the menu customID: `discord.Gateway.Menu` uses `ChoiceCustomID(conv.ID)`, i.e. the channel id. The control-socket routing keys on the *session name* (`o.Session`). To preserve behavior, set `conv.ID` resolution so the click still routes — simplest in 0a: keep the menu's customID keyed on the session by having the adapter accept the session via the conversation, OR keep the single existing menu call using the session name. If aligning is non-trivial, leave the menu path on the existing `c.SendSelectMenu(..., discord.ChoiceCustomID(o.Session), ...)` direct call for 0a and route only reply/post/react through the gateway; record this in the commit message. Do not break choice routing.

- [ ] **Step 5: Run tests**

Run: `go test ./internal/bridge/ && go test ./...`
Expected: PASS — 296 existing tests + the new one.

- [ ] **Step 6: Commit**

```bash
git add internal/bridge/bridge.go internal/bridge/bridge_test.go
git commit -m "feat(bridge): emit outbound via kernel.Gateway (degraded discord adapter)"
```

---

## Task 14: vérification finale

- [ ] **Step 1: Full build, vet, tests**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: build + vet clean, all tests green (296 + new kernel/discord/serve/bridge tests).

- [ ] **Step 2: Confirm the invariants**

- `go test . -run TestRootImportsNoDomain` passes (root is pure).
- The daemon still: registers commands, creates sessions, replies, reacts, and renders choice menus identically.

- [ ] **Step 3: Commit any final cleanup**

```bash
git add -A
git commit -m "chore: phase 0a cleanup"
```

---

## Self-Review notes

- **Spec coverage:** Conversation (T1), capabilities + degradation (T2/T5), Gateway port (T3), manifest + registry (T2/T4/T12), discord adapter (T10), dctl purification (T6/T7/T8/T9), stable session id (T11), emission via port (T13). Memory/Backend/Orchestrator explicitly out of 0a per spec.
- **Deferred (0b), stated deliberately:** package renames `handler→manager`, `serve→host`; full migration of emission out of `internal/bridge`; `Unreact` + status-upsert through a richer port.
- **Risk hot-spots flagged inline:** the menu custom_id routing (Task 10/13 NOTE) and the exact `dctl.Client` method signatures (Task 10 NOTE) — align the small `client` interface to the real methods, never change `dctl`.

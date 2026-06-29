package manager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher/core/internal/state"
)

// Forge/git clone runs inline on the dispatch path, so bound it: an unreachable
// host or a hung CLI must not wedge the daemon. Clone gets a generous ceiling
// for large repos.
const cloneTimeout = 10 * time.Minute

// maxSessions caps how many sessions can be live at once. Each session mints a
// worktree and a supervised child process, so an unbounded create loop (e.g. a
// script driving Dispatch) could exhaust process/disk resources.
const maxSessions = 64

// sessionBanner renders the shared context body posted on session create.
// worktree=="" means no isolated worktree was made; shared distinguishes an
// explicit shared:true run (main checkout) from a non-git fallback. branch is
// the real (possibly instanceID-namespaced) branch produced by the worktreer.
func sessionBanner(repo, name, worktree, branch, cmd string, shared bool) string {
	b := fmt.Sprintf("🚀 Session **%s** ready.\n", name)
	if repo == "" {
		b += "• Project: **(cwd)**\n"
	} else {
		b += fmt.Sprintf("• Project: **%s** (`%s`)\n", filepath.Base(repo), repo)
	}
	switch {
	case worktree != "":
		b += "• Mode: isolated worktree\n"
		b += fmt.Sprintf("• Worktree: `%s`\n", worktree)
		b += fmt.Sprintf("• Branch: `%s`\n", branch)
	case shared:
		b += "• Mode: shared (main checkout)\n"
		b += "• Branch: — (runs on current branch)\n"
	default:
		b += "• Mode: shared (not a git repo)\n"
	}
	b += fmt.Sprintf("• Command: `%s`", cmd)
	return b
}

func (h *Handler) sessionCreateRun(ctx context.Context, in contracts.Input) (string, error) {
	raw, ok := in.Lookup("name")
	if !ok {
		return "", fmt.Errorf("missing name")
	}
	name := slugify(raw)
	if name == "" || !sessionNameRe.MatchString(name) {
		return "", fmt.Errorf("invalid name %q — use letters, digits, - or _ (max 64, no /, spaces or ..)", raw)
	}
	if _, exists := h.st.FindSession(name); exists {
		return "", fmt.Errorf("session %q already exists", name)
	}
	if n := len(h.st.SnapshotSessions()); n >= maxSessions {
		return "", fmt.Errorf("session limit reached (%d) — close a session before creating another", maxSessions)
	}
	home := h.st.Home
	if home.ID == "" {
		return "", fmt.Errorf("no home set — run `set home` first")
	}
	cmd := h.defaultCmd
	if c, ok := in.Lookup("cmd"); ok && c != "" {
		cmd = c
	}
	backend, _ := in.Lookup("backend")
	if backend == "" {
		backend = "stream" // default backend: persistent claude stream-json
	}
	agentName, _ := in.Lookup("agent")
	// P1 learning (opt-in): extractor names a registered curation extractor; the
	// journal/cadence feed its Consolidate. Persisted on the session and threaded
	// to the bridge by the supervisor, like project/agent scope.
	extractor, _ := in.Lookup("extractor")
	journal, _ := in.Lookup("journal")
	consolidateEvery := 0
	if v, ok := in.Lookup("consolidate_every"); ok && v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return "", fmt.Errorf("invalid consolidate_every %q — use a non-negative integer", v)
		}
		consolidateEvery = n
	}
	gwList, _ := in.Lookup("gateways")
	gateways := ParseGateways(gwList, in.Bool("terminal_only"))
	ws := h.st.WorkspaceRoot()
	project := ""
	if ws != "" {
		if spec, ok := in.Lookup("clone"); ok && spec != "" {
			cctx, cancel := context.WithTimeout(ctx, cloneTimeout)
			dir, err := h.fg.Clone(cctx, spec, ws)
			cancel()
			if err != nil {
				return "", fmt.Errorf("clone: %v", err)
			}
			project = filepath.Base(dir)
		} else {
			project, _ = in.Lookup("project")
		}
		if project == "" {
			return "", fmt.Errorf("specify project: (see `workspace list`) or clone:")
		}
		if !projectRe.MatchString(project) {
			return "", fmt.Errorf("invalid project %q — use a single name (no /, spaces, or ..)", project)
		}
	}
	repo := repoFor(ws, project)
	// Worktree isolation by default; shared:true runs in the main checkout.
	shared := in.Bool("shared")
	var worktree string
	if !shared {
		path, err := h.wt.Create(repo, name)
		if err != nil {
			return "", fmt.Errorf("worktree: %v", err)
		}
		worktree = path // "" means non-git fallback
	}
	// rollbackWorktree removes the worktree we just made when a later step fails;
	// the removal error is logged but never masks the original failure.
	rollbackWorktree := func() {
		if rmErr := h.wt.Remove(repo, name, true); rmErr != nil {
			fmt.Fprintf(os.Stderr, "herrscher: worktree rollback for %q failed: %v\n", name, rmErr)
		}
	}
	// Agent provisioning: an agent companion needs a disposable, isolated worktree
	// (session close removes it), so reject shared/non-git, then materialize the
	// agent's persona + MCP + settings into it before anything outward (channel)
	// is created.
	if agentName != "" {
		if shared || worktree == "" {
			return "", fmt.Errorf("session create with agent %q needs an isolated git worktree (use a git repo and drop shared:true)", agentName)
		}
		a, found := h.agents.Get(agentName)
		if !found {
			rollbackWorktree()
			return "", fmt.Errorf("unknown agent %q — create it with `agent create %s`", agentName, agentName)
		}
		if err := a.Materialize(worktree); err != nil {
			rollbackWorktree()
			return "", fmt.Errorf("provision agent %q: %v", agentName, err)
		}
	}
	// Logical name stays the state/worktree key; the qualified name namespaces
	// the Discord title so daemons sharing a home stay distinguishable.
	title := h.st.QualifiedName(name)
	var sess state.Session
	switch home.Type {
	case "category", "terminal":
		chID, err := h.d.CreateUnder(ctx, home.ID, title)
		if err != nil {
			rollbackWorktree()
			return "", fmt.Errorf("create channel: %v", err)
		}
		sess = state.Session{Name: name, ChannelID: chID, Type: "text", Cmd: cmd, Backend: backend, Worktree: worktree, Project: project, Agent: agentName, Gateways: gateways, Extractor: extractor, Journal: journal, ConsolidateEvery: consolidateEvery}
	case "forum":
		chID, err := h.d.ForumPost(ctx, home.ID, title, "Session **"+title+"** started.")
		if err != nil {
			rollbackWorktree()
			return "", fmt.Errorf("create forum post: %v", err)
		}
		sess = state.Session{Name: name, ChannelID: chID, Type: "forum", Cmd: cmd, Backend: backend, Worktree: worktree, Project: project, Agent: agentName, Gateways: gateways, Extractor: extractor, Journal: journal, ConsolidateEvery: consolidateEvery}
	default:
		return "", fmt.Errorf("home type %q unsupported", home.Type)
	}
	if err := h.st.AddSession(sess); err != nil {
		return "", fmt.Errorf("persist: %v", err)
	}
	if err := h.sup.Start(sess); err != nil {
		return "", fmt.Errorf("start bridge: %v", err)
	}
	banner := sessionBanner(repo, name, worktree, h.wt.Branch(name), cmd, shared)
	_ = h.d.Send(ctx, sess.ChannelID, banner) // best-effort; reply is source of truth
	return fmt.Sprintf("✅ Running on %s.\n\n%s", h.d.ChannelRef(sess.ChannelID), banner), nil
}

func (h *Handler) sessionCloseRun(ctx context.Context, in contracts.Input) (string, error) {
	name, ok := in.Lookup("name")
	if !ok {
		return "", fmt.Errorf("missing name")
	}
	sess, exists := h.st.FindSession(name)
	if !exists {
		return "", fmt.Errorf("no session %q", name)
	}
	_ = h.sup.Stop(name)
	if sess.Worktree != "" {
		force := in.Bool("force")
		repo := repoFor(h.st.WorkspaceRoot(), sess.Project)
		if err := h.wt.Remove(repo, name, force); err != nil {
			return "", fmt.Errorf("%v — commit, or close with force:true to discard (branch session/%s is kept)", err, name)
		}
	}
	if err := h.d.Archive(ctx, sess.ChannelID); err != nil {
		return "", fmt.Errorf("archive: %v", err)
	}
	if err := h.st.RemoveSession(name); err != nil {
		return "", fmt.Errorf("persist: %v", err)
	}
	_ = state.RemoveParticipantJournal(state.ParticipantsPath(h.partDir, name))
	return fmt.Sprintf("🗄️ Session **%s** closed.", name), nil
}

func (h *Handler) sessionListRun(_ context.Context, _ contracts.Input) (string, error) {
	sessions := h.st.SnapshotSessions()
	if len(sessions) == 0 {
		return "No active sessions.", nil
	}
	out := "Active sessions:\n"
	for _, s := range sessions {
		out += fmt.Sprintf("• **%s** (%s) %s\n", s.Name, s.Type, h.d.ChannelRef(s.ChannelID))
	}
	return out, nil
}

func (h *Handler) sessionWhoRun(_ context.Context, in contracts.Input) (string, error) {
	name, ok := in.Lookup("name")
	if !ok {
		return "", fmt.Errorf("missing name")
	}
	if _, exists := h.st.FindSession(name); !exists {
		return "", fmt.Errorf("no session %q", name)
	}
	ids := state.ReadParticipants(state.ParticipantsPath(h.partDir, name))
	if len(ids) == 0 {
		return "Personne n'a encore écrit dans cette session.", nil
	}
	out := fmt.Sprintf("Participants observed in **%s**:\n", name)
	for _, id := range ids {
		out += fmt.Sprintf("• <@%s>\n", id)
	}
	return out, nil
}

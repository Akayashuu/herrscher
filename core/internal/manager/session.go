package manager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher/core/internal/state"
)

// Forge/git clone runs inline on the gateway dispatch loop, so bound it: an
// unreachable host or a hung CLI must not wedge the daemon. Clone gets a
// generous ceiling for large repos.
const cloneTimeout = 10 * time.Minute

// sessionBanner renders the shared context body posted on /session create.
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

func (h *Handler) handleSession(ctx context.Context, in contracts.Command) contracts.CommandResponse {
	// The "allow" sub-command group is type 2, which Subcommand() (type-1 only)
	// does not surface; detect it explicitly.
	if allowAction(in.Data.Options) != "" {
		return h.sessionAllow(in)
	}
	sub, _ := in.Data.Subcommand()
	switch sub {
	case "create":
		return h.sessionCreate(ctx, in)
	case "close":
		return h.sessionClose(ctx, in)
	case "list":
		return h.sessionList()
	case "who":
		return h.sessionWho(in)
	default:
		return errf("unknown /session subcommand")
	}
}

func (h *Handler) sessionCreate(ctx context.Context, in contracts.Command) contracts.CommandResponse {
	raw, ok := in.Data.Opt("name")
	if !ok {
		return errf("missing name")
	}
	name := slugify(raw)
	if name == "" || !sessionNameRe.MatchString(name) {
		return errf("invalid name %q — use letters, digits, - or _ (max 64, no /, spaces or ..)", raw)
	}
	if _, exists := h.st.FindSession(name); exists {
		return errf("session %q already exists", name)
	}
	home := h.st.Home
	if home.ID == "" {
		return errf("no home set — run /set home first")
	}
	cmd := h.defaultCmd
	if c, ok := in.Data.Opt("cmd"); ok && c != "" {
		cmd = c
	}
	backend, _ := in.Data.Opt("backend")
	if backend == "" {
		backend = "stream" // default backend: persistent claude stream-json
	}
	ws := h.st.WorkspaceRoot()
	project := ""
	if ws != "" {
		if spec, ok := in.Data.Opt("clone"); ok && spec != "" {
			cctx, cancel := context.WithTimeout(ctx, cloneTimeout)
			dir, err := h.fg.Clone(cctx, spec, ws)
			cancel()
			if err != nil {
				return errf("clone: %v", err)
			}
			project = filepath.Base(dir)
		} else {
			project, _ = in.Data.Opt("project")
		}
		if project == "" {
			return errf("specify project: (see `/workspace list`) or clone:")
		}
		if !projectRe.MatchString(project) {
			return errf("invalid project %q — use a single name (no /, spaces, or ..)", project)
		}
	}
	repo := repoFor(ws, project)
	// Worktree isolation by default; shared:true runs in the main checkout.
	shared := in.Data.OptBool("shared")
	var worktree string
	if !shared {
		path, err := h.wt.Create(repo, name)
		if err != nil {
			return errf("worktree: %v", err)
		}
		worktree = path // "" means non-git fallback
	}
	// Logical name stays the state/worktree key; the qualified name namespaces
	// the Discord title so daemons sharing a home stay distinguishable (Spec §3).
	title := h.st.QualifiedName(name)
	var sess state.Session
	switch home.Type {
	case "category":
		chID, err := h.d.CreateUnder(ctx, home.ID, title)
		if err != nil {
			if rmErr := h.wt.Remove(repo, name, true); rmErr != nil { // roll back the worktree we just made
				fmt.Fprintf(os.Stderr, "dctl: worktree rollback for %q failed: %v\n", name, rmErr)
			}
			return errf("create channel: %v", err)
		}
		sess = state.Session{Name: name, ChannelID: chID, Type: "text", Cmd: cmd, Backend: backend, Worktree: worktree, Project: project}
	case "forum":
		chID, err := h.d.ForumPost(ctx, home.ID, title, "Session **"+title+"** started.")
		if err != nil {
			if rmErr := h.wt.Remove(repo, name, true); rmErr != nil {
				fmt.Fprintf(os.Stderr, "dctl: worktree rollback for %q failed: %v\n", name, rmErr)
			}
			return errf("create forum post: %v", err)
		}
		sess = state.Session{Name: name, ChannelID: chID, Type: "forum", Cmd: cmd, Backend: backend, Worktree: worktree, Project: project}
	default:
		return errf("home type %q unsupported", home.Type)
	}
	if err := h.st.AddSession(sess); err != nil {
		return errf("persist: %v", err)
	}
	if err := h.sup.Start(sess); err != nil {
		return errf("start bridge: %v", err)
	}
	banner := sessionBanner(repo, name, worktree, h.wt.Branch(name), cmd, shared)
	_ = h.d.Send(ctx, sess.ChannelID, banner) // best-effort; reply is source of truth
	reply := fmt.Sprintf("✅ Running on <#%s>.\n\n%s", sess.ChannelID, banner)
	return contracts.CommandResponse{Content: reply, Private: true}
}

func (h *Handler) sessionClose(ctx context.Context, in contracts.Command) contracts.CommandResponse {
	name, ok := in.Data.Opt("name")
	if !ok {
		return errf("missing name")
	}
	sess, exists := h.st.FindSession(name)
	if !exists {
		return errf("no session %q", name)
	}
	_ = h.sup.Stop(name)
	if sess.Worktree != "" {
		force := in.Data.OptBool("force")
		repo := repoFor(h.st.WorkspaceRoot(), sess.Project)
		if err := h.wt.Remove(repo, name, force); err != nil {
			return errf("%v — commit, or close with force:true to discard (branch session/%s is kept)", err, name)
		}
	}
	if err := h.d.Archive(ctx, sess.ChannelID); err != nil {
		return errf("archive: %v", err)
	}
	if err := h.st.RemoveSession(name); err != nil {
		return errf("persist: %v", err)
	}
	_ = state.RemoveParticipantJournal(state.ParticipantsPath(h.partDir, name))
	return contracts.CommandResponse{Content: fmt.Sprintf("🗄️ Session **%s** closed.", name), Private: true}
}

func (h *Handler) sessionList() contracts.CommandResponse {
	sessions := h.st.SnapshotSessions()
	if len(sessions) == 0 {
		return contracts.CommandResponse{Content: "No active sessions.", Private: true}
	}
	out := "Active sessions:\n"
	for _, s := range sessions {
		out += fmt.Sprintf("• **%s** (%s) <#%s>\n", s.Name, s.Type, s.ChannelID)
	}
	return contracts.CommandResponse{Content: out, Private: true}
}

// sessionAllow routes /session allow add|remove|list. The option group is the
// SUB_COMMAND_GROUP "allow"; its single child SUB_COMMAND is the action.
func (h *Handler) sessionAllow(in contracts.Command) contracts.CommandResponse {
	action := allowAction(in.Data.Options)
	name, ok := in.Data.Opt("name")
	if !ok {
		return errf("missing name")
	}
	if _, exists := h.st.FindSession(name); !exists {
		return errf("no session %q", name)
	}
	switch action {
	case "add":
		id, ok := in.Data.Opt("user")
		if !ok {
			return errf("missing user")
		}
		id = normalizeUserID(id)
		added, err := h.st.AddSessionAllow(name, id)
		if err != nil {
			return errf("%v", err)
		}
		if !added {
			return contracts.CommandResponse{Content: fmt.Sprintf("<@%s> already allowed on **%s**.", id, name), Private: true}
		}
		return contracts.CommandResponse{Content: fmt.Sprintf("✅ <@%s> allowed on **%s**.", id, name), Private: true}
	case "remove":
		id, ok := in.Data.Opt("user")
		if !ok {
			return errf("missing user")
		}
		id = normalizeUserID(id)
		removed, err := h.st.RemoveSessionAllow(name, id)
		if err != nil {
			return errf("%v", err)
		}
		if !removed {
			return contracts.CommandResponse{Content: fmt.Sprintf("<@%s> was not in **%s**'s allowlist.", id, name), Private: true}
		}
		return contracts.CommandResponse{Content: fmt.Sprintf("✅ <@%s> removed from **%s**.", id, name), Private: true}
	case "list":
		ids := h.st.SessionAllowlist(name)
		if len(ids) == 0 {
			return contracts.CommandResponse{Content: fmt.Sprintf("**%s** has no per-session allowlist (the global allowlist still applies).", name), Private: true}
		}
		out := fmt.Sprintf("Per-session allowlist for **%s** (plus the global allowlist):\n", name)
		for _, id := range ids {
			out += fmt.Sprintf("• <@%s>\n", id)
		}
		return contracts.CommandResponse{Content: out, Private: true}
	default:
		return errf("unknown /session allow action")
	}
}

// sessionWho lists observed participants (journal) for the session.
func (h *Handler) sessionWho(in contracts.Command) contracts.CommandResponse {
	name, ok := in.Data.Opt("name")
	if !ok {
		return errf("missing name")
	}
	if _, exists := h.st.FindSession(name); !exists {
		return errf("no session %q", name)
	}
	ids := state.ReadParticipants(state.ParticipantsPath(h.partDir, name))
	if len(ids) == 0 {
		return contracts.CommandResponse{Content: "Personne n'a encore écrit dans cette session.", Private: true}
	}
	out := fmt.Sprintf("Participants observed in **%s**:\n", name)
	for _, id := range ids {
		out += fmt.Sprintf("• <@%s>\n", id)
	}
	return contracts.CommandResponse{Content: out, Private: true}
}

// allowAction returns the SUB_COMMAND name nested in the "allow" group.
func allowAction(opts []contracts.Option) string {
	for _, o := range opts {
		if o.Name == "allow" && o.Type == contracts.OptSubcommandGroup {
			for _, c := range o.Options {
				if c.Type == contracts.OptSubcommand {
					return c.Name
				}
			}
		}
	}
	return ""
}

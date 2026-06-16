package worktree

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// Worktreer manages git worktrees. It is repo-stateless: the repo root is passed
// to each method, so one Worktreer serves every project in the workspace.
// With a non-empty instanceID, worktrees live under
// <repo>/.dctl-sessions/<instanceID>/<name> on branch session/<instanceID>/<name>;
// with an empty instanceID (legacy) under <repo>/.dctl-sessions/<name> on
// branch session/<name>, so multiple daemons sharing a repo never collide.
type Worktreer struct {
	ctx        context.Context
	instanceID string
}

// NewWorktreer builds a repo-stateless Worktreer, namespaced by instanceID
// ("" means legacy, non-namespaced layout).
func NewWorktreer(ctx context.Context, instanceID string) *Worktreer {
	return &Worktreer{ctx: ctx, instanceID: instanceID}
}

func (w *Worktreer) isGitRepo(repo string) bool {
	return exec.CommandContext(w.ctx, "git", "-C", repo, "rev-parse", "--git-dir").Run() == nil
}

// Path returns the on-disk worktree directory for a logical session name inside
// repo, namespaced by instanceID when set.
func (w *Worktreer) Path(repo, name string) string {
	if w.instanceID == "" {
		return filepath.Join(repo, ".dctl-sessions", name)
	}
	return filepath.Join(repo, ".dctl-sessions", w.instanceID, name)
}

// Branch returns the git branch backing a logical session name, namespaced by
// instanceID when set.
func (w *Worktreer) Branch(name string) string {
	if w.instanceID == "" {
		return "session/" + name
	}
	return "session/" + w.instanceID + "/" + name
}

// Create adds a worktree on branch session/<name> inside repo. Returns ("", nil)
// when repo is not a git repo (caller falls back to a shared session).
func (w *Worktreer) Create(repo, name string) (string, error) {
	if !w.isGitRepo(repo) {
		return "", nil
	}
	p := w.Path(repo, name)
	out, err := exec.CommandContext(w.ctx, "git", "-C", repo,
		"worktree", "add", p, "-b", w.Branch(name)).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("worktree add: %s", strings.TrimSpace(string(out)))
	}
	return p, nil
}

// Remove removes the worktree. If it has uncommitted changes and !force, it
// refuses with the status. The branch session/<name> is always left intact.
func (w *Worktreer) Remove(repo, name string, force bool) error {
	p := w.Path(repo, name)
	if !force {
		out, err := exec.CommandContext(w.ctx, "git", "-C", p, "status", "--porcelain").Output()
		if err != nil {
			// Can't verify the tree is clean (missing dir, not a git repo, git
			// error): refuse rather than risk discarding work. force: bypasses.
			return fmt.Errorf("worktree %q: cannot verify clean state (%v) — close with force:true to remove anyway", name, err)
		}
		if strings.TrimSpace(string(out)) != "" {
			return fmt.Errorf("worktree %q has uncommitted changes", name)
		}
	}
	args := []string{"-C", repo, "worktree", "remove", p}
	if force {
		args = append(args, "--force")
	}
	out, err := exec.CommandContext(w.ctx, "git", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("worktree remove: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

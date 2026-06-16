package manager

import (
	"context"

	"github.com/Herrscherd/herrscher/core/internal/forge"
	"github.com/Herrscherd/herrscher/core/internal/state"
)

// discord is the subset of Client the Handler needs (injected so routing is testable).
type discord interface {
	Kind(ctx context.Context, id string) (string, error)
	CreateUnder(ctx context.Context, parentID, name string) (channelID string, err error)
	ForumPost(ctx context.Context, forumID, name, content string) (channelID string, err error)
	Archive(ctx context.Context, id string) error
	Send(ctx context.Context, channelID, content string) error
}

// supervisor starts/stops the bridge process backing a session.
type supervisor interface {
	Start(s state.Session) error
	Stop(name string) error
}

// worktrees owns per-session git worktree lifecycle. Create returns the worktree
// path ("" + nil error means "fall back to shared", e.g. not a git repo). The
// repo root is passed per call so one Worktreer serves every project.
type worktrees interface {
	Create(repo, name string) (path string, err error)
	Branch(name string) string
	Remove(repo, name string, force bool) error
}

// forges lists/clones remote repos via gh/glab (see internal/forge).
type forges interface {
	Available() (github, gitlab bool)
	List(ctx context.Context) ([]forge.Repo, error)
	Clone(ctx context.Context, spec, workspace string) (projectDir string, err error)
}

// updater rebuilds the daemon from source and restarts its service. Build pulls
// (when pull is true) and recompiles, returning the new short version; Restart
// restarts the running service out-of-band so it survives the daemon being
// killed mid-restart. Both are injected so routing stays testable.
type updater interface {
	Build(ctx context.Context, pull bool) (version string, err error)
	Restart(ctx context.Context) error
}

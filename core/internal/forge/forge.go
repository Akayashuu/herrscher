// Package forge wraps the gh / glab CLIs to list and clone remote repos.
// All process execution goes through the runner seam so tests can fake exec.
package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// Repo is one remote repository discovered via a forge CLI.
type Repo struct {
	FullName string // owner/name
	CloneURL string
	Desc     string
	Forge    string // "github" | "gitlab"
}

// runner abstracts exec.LookPath + exec.CommandContext so it can be faked.
type runner interface {
	look(name string) error
	run(ctx context.Context, dir, name string, args ...string) ([]byte, error)
}

type execRunner struct{}

func (execRunner) look(name string) error { _, err := exec.LookPath(name); return err }

func (execRunner) run(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	return cmd.CombinedOutput()
}

// Client is the forge facade injected into the handler.
type Client struct {
	r runner
}

// New returns a Client backed by real exec.
func New() *Client { return &Client{r: execRunner{}} }

// Available reports which forge CLIs are installed.
func (c *Client) Available() (github, gitlab bool) {
	return c.r.look("gh") == nil, c.r.look("glab") == nil
}

// List returns repos from every installed forge, labeled by Forge.
func (c *Client) List(ctx context.Context) ([]Repo, error) {
	gh, gl := c.Available()
	var out []Repo
	if gh {
		repos, err := c.listGitHub(ctx)
		if err != nil {
			return nil, err
		}
		out = append(out, repos...)
	}
	if gl {
		repos, err := c.listGitLab(ctx)
		if err != nil {
			return nil, err
		}
		out = append(out, repos...)
	}
	return out, nil
}

func (c *Client) listGitHub(ctx context.Context) ([]Repo, error) {
	// Personal repos first; this query is the source of truth for auth errors.
	out, err := c.ghRepoList(ctx)
	if err != nil {
		return nil, err
	}
	// Then each org the user belongs to. Org discovery/listing is best-effort:
	// a missing org scope or a single failing org must not drop personal repos.
	seen := make(map[string]struct{}, len(out))
	for _, r := range out {
		seen[r.FullName] = struct{}{}
	}
	for _, org := range c.ghOrgs(ctx) {
		repos, err := c.ghRepoList(ctx, org)
		if err != nil {
			continue
		}
		for _, r := range repos {
			if _, dup := seen[r.FullName]; dup {
				continue
			}
			seen[r.FullName] = struct{}{}
			out = append(out, r)
		}
	}
	return out, nil
}

// ghRepoList runs `gh repo list [owner]` and parses the result. With no owner it
// lists the authenticated user's repos; with one it lists that org/user's.
func (c *Client) ghRepoList(ctx context.Context, owner ...string) ([]Repo, error) {
	args := []string{"repo", "list"}
	args = append(args, owner...)
	args = append(args, "--json", "nameWithOwner,sshUrl,description", "--limit", "100")
	raw, err := c.r.run(ctx, "", "gh", args...)
	if err != nil {
		return nil, fmt.Errorf("gh repo list: %s", strings.TrimSpace(string(raw)))
	}
	var items []struct {
		NameWithOwner string `json:"nameWithOwner"`
		SSHURL        string `json:"sshUrl"`
		Description   string `json:"description"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("gh repo list: parse: %w", err)
	}
	out := make([]Repo, 0, len(items))
	for _, it := range items {
		out = append(out, Repo{FullName: it.NameWithOwner, CloneURL: it.SSHURL, Desc: it.Description, Forge: "github"})
	}
	return out, nil
}

// ghOrgs returns the logins of the orgs the authenticated user belongs to, or
// nil on any failure (the caller treats orgs as best-effort).
func (c *Client) ghOrgs(ctx context.Context) []string {
	raw, err := c.r.run(ctx, "", "gh", "api", "--paginate", "user/orgs", "--jq", ".[].login")
	if err != nil {
		return nil
	}
	var orgs []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if l := strings.TrimSpace(line); l != "" {
			orgs = append(orgs, l)
		}
	}
	return orgs
}

func (c *Client) listGitLab(ctx context.Context) ([]Repo, error) {
	raw, err := c.r.run(ctx, "", "glab", "repo", "list", "--output", "json")
	if err != nil {
		return nil, fmt.Errorf("glab repo list: %s", strings.TrimSpace(string(raw)))
	}
	var items []struct {
		PathWithNamespace string `json:"path_with_namespace"`
		SSHURLToRepo      string `json:"ssh_url_to_repo"`
		Description       string `json:"description"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("glab repo list: parse: %w", err)
	}
	out := make([]Repo, 0, len(items))
	for _, it := range items {
		out = append(out, Repo{FullName: it.PathWithNamespace, CloneURL: it.SSHURLToRepo, Desc: it.Description, Forge: "gitlab"})
	}
	return out, nil
}

// specRe permits owner/name style specs (no shell metacharacters, no traversal).
var specRe = regexp.MustCompile(`^[\w.\-]+/[\w.\-]+$`)

// Clone clones spec into workspace and returns the project dir. It refuses
// traversal / shell-unsafe specs. If the target dir already exists it is
// returned as-is (idempotent "already cloned").
func (c *Client) Clone(ctx context.Context, spec, workspace string) (string, error) {
	base, isURL, err := parseSpec(spec)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(workspace, base)

	gh, gl := c.Available()
	var name string
	var args []string
	switch {
	case isURL:
		// `--` stops git from treating a (validated) spec as an option.
		name, args = "git", []string{"clone", "--", spec, dir}
	case gh:
		// gh parses `spec` as a positional before `--`; parseSpec already
		// rejects a leading dash, so it can't be read as a flag.
		name, args = "gh", []string{"repo", "clone", spec, "--", dir}
	case gl:
		// `--` stops glab (cobra) from treating spec as a flag.
		name, args = "glab", []string{"repo", "clone", "--", spec, dir}
	default:
		return "", fmt.Errorf("no gh/glab installed to clone %q; pass a full git URL instead", spec)
	}
	if out, err := c.r.run(ctx, workspace, name, args...); err != nil {
		msg := strings.TrimSpace(string(out))
		// A pre-existing checkout is success (idempotent).
		if strings.Contains(msg, "already exists") {
			return dir, nil
		}
		return "", fmt.Errorf("%s clone: %s", name, msg)
	}
	return dir, nil
}

// parseSpec validates spec and returns the project basename + whether it's a URL.
// It rejects shell/argument-injection vectors: control characters, path
// traversal, and any segment or derived name beginning with '-' (which a CLI
// could otherwise read as an option flag — the git-clone --upload-pack RCE
// class). Callers still pass specs after a `--` separator as defense in depth.
func parseSpec(spec string) (base string, isURL bool, err error) {
	if spec == "" {
		return "", false, fmt.Errorf("empty repo spec")
	}
	if strings.ContainsFunc(spec, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "", false, fmt.Errorf("invalid repo spec: control character")
	}
	if strings.HasPrefix(spec, "https://") || strings.HasPrefix(spec, "git@") || strings.HasPrefix(spec, "ssh://") {
		b := path.Base(strings.TrimSuffix(spec, ".git"))
		if b == "" || b == "." || b == "/" || strings.Contains(b, "..") || strings.HasPrefix(b, "-") {
			return "", false, fmt.Errorf("cannot derive project name from %q", spec)
		}
		return b, true, nil
	}
	if !specRe.MatchString(spec) {
		return "", false, fmt.Errorf("invalid repo spec %q — use owner/name or a full git URL", spec)
	}
	for _, seg := range strings.Split(spec, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.HasPrefix(seg, "-") {
			return "", false, fmt.Errorf("invalid repo spec %q", spec)
		}
	}
	return path.Base(spec), false, nil
}

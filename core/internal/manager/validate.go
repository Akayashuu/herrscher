package manager

import (
	"path/filepath"
	"regexp"
	"strings"
)

// sessionNameRe constrains a session name to a safe slug: it becomes both a
// filesystem path (<repo>/.dctl-sessions/<name>) and a git branch
// (session/<name>), so anything outside this set could traverse directories or
// forge odd refs even though the caller is allowlisted.
var sessionNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// slugInvalidRe matches any run of characters that are not allowed inside a
// session slug; slugify collapses each such run to a single '-'.
var slugInvalidRe = regexp.MustCompile(`[^a-z0-9_-]+`)

// slugify turns a free-form session name into a safe slug: lowercase, runs of
// invalid characters (spaces, punctuation, …) collapse to '-', and leading or
// trailing separators are trimmed. It returns "" when nothing usable remains
// (e.g. an all-emoji name), letting the caller emit a clear error. The result
// is always accepted by sessionNameRe, which stays as the final guard.
func slugify(name string) string {
	s := slugInvalidRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-")
	s = strings.Trim(s, "-_")
	if len(s) > 64 {
		s = strings.Trim(s[:64], "-_")
	}
	return s
}

// projectRe constrains a workspace project name to a single safe path segment
// (no "/", no "..", no spaces), so workspace+project cannot escape the root.
var projectRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// repoFor resolves the git repo root a session operates on: the workspace root
// when no project is set (legacy single-repo), else <workspace>/<project>.
func repoFor(workspace, project string) string {
	if project == "" {
		return workspace
	}
	return filepath.Join(workspace, project)
}

// normalizeUserID strips a Discord mention wrapper (<@id> / <@!id>) to the bare id.
func normalizeUserID(s string) string {
	s = strings.TrimPrefix(s, "<@")
	s = strings.TrimPrefix(s, "!")
	s = strings.TrimSuffix(s, ">")
	return s
}

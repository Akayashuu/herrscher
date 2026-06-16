package manager

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher/core/internal/state"
)

// maxAutocompleteChoices is Discord's hard cap on autocomplete suggestions.
const maxAutocompleteChoices = 25

// acTimeout bounds the work behind an autocomplete suggestion: Discord drops
// the response if it doesn't arrive within ~3s, so forge listing for clone:
// suggestions runs best-effort under a tight ceiling.
const acTimeout = 2500 * time.Millisecond

// Autocomplete answers a Discord autocomplete request (interaction type 4) for
// /session. On create it suggests the focused option (project, clone, cmd); on
// close it suggests live session names. An unknown option, a non-allowlisted
// user, or any failure yields no suggestions (Discord then shows free text), so
// project/session names never leak to non-allowlisted members.
func (h *Handler) Autocomplete(ctx context.Context, in contracts.Command) []contracts.Choice {
	// Same gate as Handle: autocomplete would otherwise leak workspace project
	// names / remote repos and spawn gh/glab for any guild member.
	if !h.st.Allowed(in.Invoker) {
		return nil
	}
	if in.Data.Name != "session" {
		return nil
	}
	sub, _ := in.Data.Subcommand()
	field, partial, ok := in.Data.Focused()
	if !ok {
		return nil
	}
	switch {
	case sub == "create" && field == "project":
		return filterChoices(h.localProjects(), partial)
	case sub == "create" && field == "clone":
		return h.cloneChoices(ctx, partial)
	case sub == "create" && field == "cmd":
		return h.cmdChoices(partial)
	case sub == "close" && field == "name":
		return filterSessionChoices(h.st.SnapshotSessions(), partial)
	default:
		return nil
	}
}

// localProjects lists the names of git projects in the workspace root.
func (h *Handler) localProjects() []string {
	ws := h.st.WorkspaceRoot()
	if ws == "" {
		return nil
	}
	entries, err := os.ReadDir(ws)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(ws, e.Name(), ".git")); err != nil {
			continue
		}
		names = append(names, e.Name())
	}
	return names
}

// cmdChoices offers ready-made bridged commands for the /session create cmd
// field: the configured default first, then the backend-supplied presets (e.g. a
// model × effort matrix) so a user can pick without typing flags. The presets are
// injected by the backend, keeping core free of any model-specific knowledge.
// Filtered case-insensitively against label and command, capped at Discord's
// 25-choice / 100-char limits.
func (h *Handler) cmdChoices(partial string) []contracts.Choice {
	p := strings.ToLower(partial)
	out := make([]contracts.Choice, 0, maxAutocompleteChoices)
	seen := map[string]bool{}
	add := func(label, cmd string) {
		if cmd == "" || seen[cmd] || len(cmd) > 100 || len(label) > 100 {
			return
		}
		if p != "" && !strings.Contains(strings.ToLower(label+" "+cmd), p) {
			return
		}
		seen[cmd] = true
		out = append(out, contracts.Choice{Label: label, Value: cmd})
	}
	add("Default (config.json)", h.defaultCmd)
	for _, c := range h.cmdPresets {
		if len(out) >= maxAutocompleteChoices {
			break
		}
		add(c.Label, c.Value)
	}
	return out
}

// cloneChoices lists remote repos (owner/name) under a tight timeout so the
// autocomplete response beats Discord's deadline; failures yield no suggestions.
func (h *Handler) cloneChoices(ctx context.Context, partial string) []contracts.Choice {
	cctx, cancel := context.WithTimeout(ctx, acTimeout)
	defer cancel()
	repos, err := h.fg.List(cctx)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(repos))
	for _, r := range repos {
		names = append(names, r.FullName)
	}
	return filterChoices(names, partial)
}

// filterChoices keeps values whose lowercased form contains the lowercased
// partial (case-insensitive substring), capped at the Discord 25-choice limit.
func filterChoices(values []string, partial string) []contracts.Choice {
	p := strings.ToLower(partial)
	out := make([]contracts.Choice, 0, len(values))
	for _, v := range values {
		// Discord rejects the whole response if any choice name/value exceeds 100
		// chars; the value is used verbatim (e.g. clone spec), so skip rather than
		// truncate into something invalid.
		if len(v) > 100 {
			continue
		}
		if p != "" && !strings.Contains(strings.ToLower(v), p) {
			continue
		}
		out = append(out, contracts.Choice{Label: v, Value: v})
		if len(out) == maxAutocompleteChoices {
			break
		}
	}
	return out
}

// filterSessionChoices builds the suggestion list: session names containing the
// typed text (case-insensitive), sorted, capped at Discord's limit.
func filterSessionChoices(sessions []state.Session, typed string) []contracts.Choice {
	q := strings.ToLower(strings.TrimSpace(typed))
	out := make([]contracts.Choice, 0, len(sessions))
	for _, s := range sessions {
		if q == "" || strings.Contains(strings.ToLower(s.Name), q) {
			out = append(out, contracts.Choice{Label: s.Name, Value: s.Name})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	if len(out) > maxAutocompleteChoices {
		out = out[:maxAutocompleteChoices]
	}
	return out
}

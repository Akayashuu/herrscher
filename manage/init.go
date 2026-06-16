package manage

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// The module catalog: one entry per category, mapping a short kind to its
// published module path. `herrscher init` composes a stack by picking one kind
// per category (or "none" to skip a category). New modules are added here.
var catalog = map[string]map[string]string{
	"gateway": {
		"discord": "github.com/Herrscherd/herrscher-discord-gateway",
	},
	"backend": {
		"claude": "github.com/Herrscherd/herrscher-claude-backend",
	},
	"memory": {
		"obsidian": "github.com/Herrscherd/herrscher-obsidian-memory",
	},
	"orchestrator": {
		"basic": "github.com/Herrscherd/herrscher-orchestrator",
	},
}

// categories is the fixed order a stack is composed and printed in.
var categories = []string{"gateway", "backend", "memory", "orchestrator"}

// defaultStack is the batteries-included composition: a Discord gateway, the
// Claude backend, Obsidian-backed memory and the basic multi-agent orchestrator.
var defaultStack = map[string]string{
	"gateway":      "discord",
	"backend":      "claude",
	"memory":       "obsidian",
	"orchestrator": "basic",
}

// InitCmd composes the host's plugin stack from scratch: it picks one module per
// category (defaulting to the batteries-included stack), rewrites plugins.go to
// exactly that set, seeds a .env from .env.example, then go-gets and rebuilds.
// Pass --gateway/--backend/--memory/--orchestrator to swap a category, the kind
// "none" to drop one, or --with MODULE to pin an extra module verbatim.
func InitCmd(args []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	hostDir := fs.String("host", "", "path to the host module")
	noBuild := fs.Bool("no-build", false, "write plugins.go but skip go get/build")
	list := fs.Bool("list", false, "list the module catalog and exit")
	gateway := fs.String("gateway", defaultStack["gateway"], "gateway module kind (or none)")
	backend := fs.String("backend", defaultStack["backend"], "backend module kind (or none)")
	memory := fs.String("memory", defaultStack["memory"], "memory module kind (or none)")
	orchestrator := fs.String("orchestrator", defaultStack["orchestrator"], "orchestrator module kind (or none)")
	var extras multiFlag
	fs.Var(&extras, "with", "pin an extra module path verbatim (repeatable)")
	if err := fs.Parse(args); err != nil {
		return parseExit(err)
	}

	if *list {
		printCatalog()
		return 0
	}

	choices := map[string]string{
		"gateway":      *gateway,
		"backend":      *backend,
		"memory":       *memory,
		"orchestrator": *orchestrator,
	}
	modules, err := resolveStack(choices, extras)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	dir, err := resolveHost(*hostDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	manifest := filepath.Join(dir, "plugins.go")
	src, err := os.ReadFile(manifest)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read %s: %v\n", manifest, err)
		return 1
	}
	out, err := setPlugins(string(src), modules)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := os.WriteFile(manifest, []byte(out), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write %s: %v\n", manifest, err)
		return 1
	}
	for _, m := range modules {
		fmt.Printf("stack: %s\n", m)
	}
	seedEnv(dir)

	if *noBuild {
		fmt.Println("wrote plugins.go (--no-build); run `go mod tidy && go build` in the host to apply")
		return 0
	}

	// If go get/tidy/build fails (e.g. an unresolvable module), restore the
	// original plugins.go so a botched init doesn't leave the host uncompilable.
	if code := buildStack(dir, modules); code != 0 {
		if werr := os.WriteFile(manifest, src, 0o644); werr != nil {
			fmt.Fprintf(os.Stderr, "restore %s: %v\n", manifest, werr)
		} else {
			fmt.Fprintf(os.Stderr, "build failed; restored %s\n", manifest)
		}
		return code
	}
	return 0
}

// buildStack go-gets each module then tidies and rebuilds the host.
func buildStack(dir string, modules []string) int {
	for _, m := range modules {
		if code := run(dir, "go", "get", "--", m); code != 0 {
			return code
		}
	}
	if code := run(dir, "go", "mod", "tidy"); code != 0 {
		return code
	}
	return run(dir, "go", "build", "./...")
}

// resolveStack turns per-category kind choices plus verbatim extras into an
// ordered, de-duplicated module list. A kind of "none" drops its category.
func resolveStack(choices map[string]string, extras []string) ([]string, error) {
	var modules []string
	seen := map[string]bool{}
	add := func(m string) {
		if !seen[m] {
			seen[m] = true
			modules = append(modules, m)
		}
	}
	for _, cat := range categories {
		kind := choices[cat]
		if kind == "" || kind == "none" {
			continue
		}
		module, ok := catalog[cat][kind]
		if !ok {
			return nil, fmt.Errorf("unknown %s kind %q (try `herrscher init --list`)", cat, kind)
		}
		add(module)
	}
	for _, m := range extras {
		// Reject paths that would be read as flags by `go get` (e.g. -insecure).
		if m == "" || m[0] == '-' {
			return nil, fmt.Errorf("invalid module path %q", m)
		}
		add(m)
	}
	if len(modules) == 0 {
		return nil, fmt.Errorf("empty stack: every category was set to none")
	}
	return modules, nil
}

// seedEnv copies .env.example to .env when no .env exists yet, so a fresh host
// has a config skeleton to fill in. A missing example or an existing .env is a
// no-op (never clobber real secrets).
func seedEnv(dir string) {
	dst := filepath.Join(dir, ".env")
	if _, err := os.Stat(dst); err == nil {
		return
	}
	src, err := os.ReadFile(filepath.Join(dir, ".env.example"))
	if err != nil {
		return
	}
	if err := os.WriteFile(dst, src, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "seed %s: %v\n", dst, err)
		return
	}
	fmt.Printf("seeded %s from .env.example — fill in your secrets\n", dst)
}

func printCatalog() {
	for _, cat := range categories {
		kinds := make([]string, 0, len(catalog[cat]))
		for k := range catalog[cat] {
			kinds = append(kinds, k)
		}
		sort.Strings(kinds)
		for _, k := range kinds {
			def := ""
			if defaultStack[cat] == k {
				def = " (default)"
			}
			fmt.Printf("%-13s %-10s %s%s\n", cat, k, catalog[cat][k], def)
		}
	}
}

// multiFlag collects a repeatable string flag into a slice.
type multiFlag []string

func (m *multiFlag) String() string { return fmt.Sprint([]string(*m)) }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

// Package manage is the host's self-management surface: it edits the managed
// plugins.go composition and rebuilds the binary. It is invoked by the herrscher
// dispatcher for the plugin/update/install verbs. Runtime lives in the host; this
// is install/update/plugin tooling only.
package manage

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// PluginCmd lists, adds or removes a compiled-in plugin in the managed plugins.go,
// rebuilding the binary on a change.
func PluginCmd(args []string) int {
	fs := flag.NewFlagSet("plugin", flag.ContinueOnError)
	hostDir := fs.String("host", "", "path to the host module")
	noBuild := fs.Bool("no-build", false, "edit plugins.go but skip go get/build")
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: herrscher plugin <list|add|remove> [module]")
		return 2
	}
	sub := args[0]
	// Allow flags and positionals in any order: flag.Parse stops at the first
	// non-flag, so loop, collecting positionals between flag runs.
	var positionals []string
	rem := args[1:]
	for len(rem) > 0 {
		if err := fs.Parse(rem); err != nil {
			return 2
		}
		rem = fs.Args()
		if len(rem) == 0 {
			break
		}
		positionals = append(positionals, rem[0])
		rem = rem[1:]
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

	switch sub {
	case "list":
		mods, err := listPlugins(string(src))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		for _, m := range mods {
			fmt.Println(m)
		}
		return 0

	case "add", "remove":
		if len(positionals) != 1 {
			fmt.Fprintf(os.Stderr, "usage: herrscher plugin %s <module>\n", sub)
			return 2
		}
		module := positionals[0]
		var out string
		var changed bool
		if sub == "add" {
			out, changed, err = addPlugin(string(src), module)
		} else {
			out, changed, err = removePlugin(string(src), module)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if !changed {
			fmt.Printf("no change: %s already %s\n", module, pastTense(sub))
			return 0
		}
		if err := os.WriteFile(manifest, []byte(out), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write %s: %v\n", manifest, err)
			return 1
		}
		fmt.Printf("%s %s in %s\n", pastTense(sub), module, manifest)
		if *noBuild {
			fmt.Println("skipped build (--no-build); run `go build` in the host to apply")
			return 0
		}
		return rebuild(dir, sub, module)

	default:
		fmt.Fprintf(os.Stderr, "unknown plugin subcommand %q\n", sub)
		return 2
	}
}

func pastTense(sub string) string {
	if sub == "add" {
		return "added"
	}
	return "removed"
}

// resolveHost finds the host module directory: an explicit --host, else the
// current directory (self-host), else a sibling ./herrscher.
func resolveHost(explicit string) (string, error) {
	candidates := []string{explicit, ".", "herrscher"}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(c, "plugins.go")); err == nil {
			return c, nil
		}
	}
	return "", fmt.Errorf("no host module found (looked for plugins.go); pass --host DIR")
}

// rebuild applies the manifest change: go get the module (on add), then build.
func rebuild(dir, sub, module string) int {
	if sub == "add" {
		if code := run(dir, "go", "get", module); code != 0 {
			return code
		}
	}
	if code := run(dir, "go", "mod", "tidy"); code != 0 {
		return code
	}
	return run(dir, "go", "build", "./...")
}

func run(dir string, name string, args ...string) int {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	fmt.Fprintf(os.Stderr, "+ %s %v\n", name, args)
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "%s failed: %v\n", name, err)
		return 1
	}
	return 0
}

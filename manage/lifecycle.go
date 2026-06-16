package manage

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

// updateCmd bumps every compiled-in plugin to its latest version, then rebuilds
// the host. This is the plugin-side counterpart to `herrscherd service update`
// (which pulls the host's own source): here we refresh the blank-imported plugin
// modules listed in plugins.go and re-tidy/rebuild the composition.
func UpdateCmd(args []string) int {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	hostDir := fs.String("host", "", "path to the host module")
	noBuild := fs.Bool("no-build", false, "go get -u the plugins but skip tidy/build")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	dir, err := resolveHost(*hostDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	src, err := os.ReadFile(filepath.Join(dir, "plugins.go"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "read plugins.go: %v\n", err)
		return 1
	}
	mods, err := listPlugins(string(src))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(mods) == 0 {
		fmt.Println("no plugins to update")
		return 0
	}

	for _, m := range mods {
		if code := run(dir, "go", "get", "-u", m); code != 0 {
			return code
		}
	}
	if *noBuild {
		fmt.Println("updated plugins (--no-build); run `go mod tidy && go build` in the host to apply")
		return 0
	}
	if code := run(dir, "go", "mod", "tidy"); code != 0 {
		return code
	}
	return run(dir, "go", "build", "./...")
}

// installCmd builds the host binary from its current plugin composition, then
// delegates to `herrscherd service install` for the OS-service glue (systemd /
// launchd / Task Scheduler). The CLI never reimplements that glue — it lives in
// core/service, exposed by the host. Anything after `--` is forwarded verbatim
// to the host's service install (e.g. --health-addr, --env-file, --cmd).
func InstallCmd(args []string) int {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	hostDir := fs.String("host", "", "path to the host module")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	passthrough := fs.Args()

	dir, err := resolveHost(*hostDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	if code := run(dir, "go", "build", "-o", "herrscher", "."); code != 0 {
		return code
	}

	// exec resolves a relative path-with-slash against the process cwd, not
	// cmd.Dir, so hand it the absolute path to the freshly built binary.
	abs, err := filepath.Abs(filepath.Join(dir, "herrscher"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	delegate := append([]string{"service", "install"}, passthrough...)
	return run(dir, abs, delegate...)
}

// Package service installs the `dctl serve` daemon as a native, boot-started
// background service on Linux (systemd user unit), macOS (launchd LaunchAgent),
// and Windows (Task Scheduler onlogon task).
//
// The design separates a pure planner (BuildPlan / BuildUninstall, testable on
// any OS) from the executor (Install / Uninstall, which writes files and runs
// the platform commands). Secrets never live in the generated unit: every
// platform sources an env file (mode 0600) that holds DISCORD_BOT_TOKEN et al.,
// and the planner only ever creates that file as an empty template — it never
// overwrites an existing one and never echoes a token.
package service

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Herrscherd/herrscher/core/config"
)

// label / on-disk names shared by the planner and the docs.
const (
	linuxUnitName = "dctl.service"
	macLabel      = "com.vskstudio.dctl"
	winTaskName   = "dctl"
)

// Config describes the service to install.
type Config struct {
	GOOS       string   // target OS; "" => runtime.GOOS
	BinPath    string   // absolute path to the dctl binary
	Home       string   // user home dir
	User       string   // username (for loginctl enable-linger)
	EnvFile    string   // path to the secrets env file (mode 0600)
	HealthAddr string   // --health-addr value; "" omits the flag
	ExtraArgs  []string // extra args appended to `dctl serve`
	SkipStart  bool     // configure boot-start but don't start now (e.g. token not set yet)

	ConfigPath string // path to the declarative config.json scaffold (template)
	DefaultCmd string // pre-fills the scaffold's "cmd" (from install --cmd)
}

// FileWrite is one file the plan writes. Template files are written only when
// missing (so an install never clobbers the user's secrets).
type FileWrite struct {
	Path     string
	Content  string
	Mode     os.FileMode
	Template bool
}

// Command is one shell command the plan runs. IgnoreErr commands are best-effort
// (e.g. unloading a service that isn't loaded yet).
type Command struct {
	Argv      []string
	IgnoreErr bool
}

// Plan is the full set of side effects for an install or uninstall.
type Plan struct {
	Files    []FileWrite
	Commands []Command
	Notes    []string // human-facing follow-ups (shown after a successful run)
}

func goos(c Config) string {
	if c.GOOS != "" {
		return c.GOOS
	}
	return runtime.GOOS
}

// serveArgs builds the `serve …` argv the service runs. The daemon loads its
// own secrets via --env-file (no shell sourcing), so the env file is passed as
// a plain argument on every platform. Tunable knobs (health-addr, cmd, …) are
// NOT baked here: they live in config.json so a user can edit them without
// reinstalling. Baking them as explicit flags would shadow config.json (an
// explicit flag outranks it), silently overriding the user's edits.
func serveArgs(c Config) []string {
	args := []string{"serve"}
	if c.EnvFile != "" {
		args = append(args, "--env-file", c.EnvFile)
	}
	return append(args, c.ExtraArgs...)
}

const envTemplate = `# dctl daemon secrets — keep private (chmod 600), never commit.
# Fill these in, then restart the service.
DISCORD_BOT_TOKEN=
DISCORD_CHANNEL_ID=
DCTL_OWNER_ID=
`

// envFileWrite is the (always template, never-overwrite) secrets file shared by
// every platform.
func envFileWrite(c Config) FileWrite {
	return FileWrite{Path: c.EnvFile, Content: envTemplate, Mode: 0o600, Template: true}
}

// BuildPlan returns the install plan for c's target OS. Every platform also
// scaffolds the declarative config.json (template, never clobbered).
func BuildPlan(c Config) (Plan, error) {
	var p Plan
	switch goos(c) {
	case "linux":
		p = linuxPlan(c)
	case "darwin":
		p = macPlan(c)
	case "windows":
		p = windowsPlan(c)
	default:
		return Plan{}, fmt.Errorf("unsupported OS %q", goos(c))
	}
	if c.ConfigPath != "" {
		p.Files = append(p.Files, configFileWrite(c))
	}
	return p, nil
}

// configFileWrite is the declarative config.json scaffold: a commented template
// written only when missing, so reinstalling never clobbers an edited file. It
// holds no secrets (those stay in the env file).
func configFileWrite(c Config) FileWrite {
	return FileWrite{Path: c.ConfigPath, Content: config.Template(c.DefaultCmd, c.HealthAddr), Mode: 0o644, Template: true}
}

// BuildUninstall returns the uninstall plan for c's target OS.
func BuildUninstall(c Config) (Plan, error) {
	switch goos(c) {
	case "linux":
		unit := filepath.Join(c.Home, ".config", "systemd", "user", linuxUnitName)
		return Plan{Commands: []Command{
			{Argv: []string{"systemctl", "--user", "disable", "--now", linuxUnitName}, IgnoreErr: true},
			{Argv: []string{"rm", "-f", unit}},
			{Argv: []string{"systemctl", "--user", "daemon-reload"}, IgnoreErr: true},
		}}, nil
	case "darwin":
		plist := filepath.Join(c.Home, "Library", "LaunchAgents", macLabel+".plist")
		return Plan{Commands: []Command{
			{Argv: []string{"launchctl", "unload", "-w", plist}, IgnoreErr: true},
			{Argv: []string{"rm", "-f", plist}},
		}}, nil
	case "windows":
		return Plan{Commands: []Command{
			{Argv: []string{"schtasks", "/delete", "/tn", winTaskName, "/f"}},
		}}, nil
	default:
		return Plan{}, fmt.Errorf("unsupported OS %q", goos(c))
	}
}

// StatusCommand returns the command that reports whether the service is active.
func StatusCommand(c Config) (Command, error) {
	// IgnoreErr: these report status by exit code (e.g. systemctl exits 3 when
	// the unit is inactive); we still want to print the output without turning
	// "stopped" into a CLI error.
	switch goos(c) {
	case "linux":
		return Command{Argv: []string{"systemctl", "--user", "status", linuxUnitName}, IgnoreErr: true}, nil
	case "darwin":
		return Command{Argv: []string{"launchctl", "list", macLabel}, IgnoreErr: true}, nil
	case "windows":
		return Command{Argv: []string{"schtasks", "/query", "/tn", winTaskName, "/v"}, IgnoreErr: true}, nil
	default:
		return Command{}, fmt.Errorf("unsupported OS %q", goos(c))
	}
}

// RestartCommands returns the platform commands that restart the running
// service. Some platforms need more than one (stop then start).
func RestartCommands(c Config) ([]Command, error) {
	switch goos(c) {
	case "linux":
		return []Command{{Argv: []string{"systemctl", "--user", "restart", linuxUnitName}}}, nil
	case "darwin":
		// kickstart -k stops (if running) and starts the agent in one call.
		target := fmt.Sprintf("gui/%d/%s", os.Getuid(), macLabel)
		return []Command{{Argv: []string{"launchctl", "kickstart", "-k", target}}}, nil
	case "windows":
		return []Command{
			{Argv: []string{"schtasks", "/end", "/tn", winTaskName}, IgnoreErr: true},
			{Argv: []string{"schtasks", "/run", "/tn", winTaskName}},
		}, nil
	default:
		return nil, fmt.Errorf("unsupported OS %q", goos(c))
	}
}

// Restart restarts the service inline (for a separate caller process, e.g. the
// `dctl service restart` CLI — never the daemon restarting itself).
func Restart(ctx context.Context, c Config) error {
	cmds, err := RestartCommands(c)
	if err != nil {
		return err
	}
	for _, cmd := range cmds {
		if err := runCommand(ctx, cmd); err != nil {
			return err
		}
	}
	return nil
}

// RestartDetached restarts the service out-of-band so it survives the caller
// being killed mid-restart — required when the daemon restarts *itself* (e.g.
// from /service restart). On Linux the daemon shares the unit's cgroup, which
// systemd kills on stop, so the restart is scheduled as a transient timer unit
// (systemd-run) that lives outside that cgroup. launchd/Task Scheduler manage
// the service from a separate process domain, so a normal restart already
// survives there.
func RestartDetached(ctx context.Context, c Config) error {
	if goos(c) == "linux" {
		// 3s, not 1s: the caller is mid-interaction and must finish editing its
		// deferred reply (an HTTP round-trip to Discord) before systemd kills the
		// unit's cgroup out from under it. 1s raced the reply; 3s clears it.
		return runCommand(ctx, Command{Argv: []string{
			"systemd-run", "--user", "--on-active=3",
			"--timer-property=AccuracySec=100ms",
			"systemctl", "--user", "restart", linuxUnitName,
		}})
	}
	return Restart(ctx, c)
}

// validateSource confirms src looks like a dctl checkout (has go.mod and
// cmd/dctl), so a build never runs `go build` in an unrelated directory.
func validateSource(src string) error {
	if src == "" {
		return errors.New("no source dir set")
	}
	if fi, err := os.Stat(filepath.Join(src, "go.mod")); err != nil || fi.IsDir() {
		return fmt.Errorf("not a dctl source checkout (no go.mod): %s", src)
	}
	if fi, err := os.Stat(filepath.Join(src, "cmd", "dctl")); err != nil || !fi.IsDir() {
		return fmt.Errorf("not a dctl source checkout (no cmd/dctl): %s", src)
	}
	return nil
}

// Pull fast-forwards the source checkout. --ff-only fails loudly rather than
// creating a merge commit when local and remote have diverged.
func Pull(ctx context.Context, src string) error {
	if err := validateSource(src); err != nil {
		return err
	}
	if out, err := runCapture(ctx, src, "git", "pull", "--ff-only"); err != nil {
		return fmt.Errorf("git pull: %s", out)
	}
	return nil
}

// Build compiles the dctl binary from src to binPath. `go build -o` writes a new
// file and renames it into place, so replacing the live binary is safe while the
// daemon runs (the restart then picks up the new file).
func Build(ctx context.Context, src, binPath string) error {
	if err := validateSource(src); err != nil {
		return err
	}
	if _, err := exec.LookPath("go"); err != nil {
		return fmt.Errorf("go toolchain not found in PATH")
	}
	if out, err := runCapture(ctx, src, "go", "build", "-o", binPath, "./cmd/dctl"); err != nil {
		return fmt.Errorf("go build: %s", out)
	}
	return nil
}

// SourceVersion returns the short commit of the source checkout, or "" if it
// can't be determined (best-effort, for user-facing messages only).
func SourceVersion(ctx context.Context, src string) string {
	out, err := runCapture(ctx, src, "git", "rev-parse", "--short", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Smoke runs the freshly built binary with `--help`, which prints usage and
// exits 0 without touching the network. A non-zero exit means the new binary is
// broken (won't even parse its CLI), so the caller must not restart into it. The
// daemon keeps running its old in-memory image; only the on-disk file changed.
func Smoke(ctx context.Context, binPath string) error {
	if binPath == "" {
		return errors.New("no binary path to smoke-test")
	}
	if out, err := runCapture(ctx, "", binPath, "--help"); err != nil {
		return fmt.Errorf("new binary failed smoke test (%s --help): %s", binPath, strings.TrimSpace(string(out)))
	}
	return nil
}

// InstalledBinPath returns the binary path baked into the installed service
// unit's ExecStart, so `service update` rebuilds the binary the service actually
// runs — not whatever binary happened to invoke the CLI (running it from a build
// dir would otherwise rebuild that throwaway binary and leave the daemon stale).
// Returns ("", false) when it can't be determined: unit absent, or an OS whose
// launcher this doesn't parse (only the linux systemd unit is read).
func InstalledBinPath(c Config) (string, bool) {
	if goos(c) != "linux" {
		return "", false
	}
	data, err := os.ReadFile(filepath.Join(c.Home, ".config", "systemd", "user", linuxUnitName))
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(data), "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "ExecStart=")
		if !ok {
			continue
		}
		if bin := firstToken(rest); bin != "" {
			return bin, true
		}
	}
	return "", false
}

// firstToken returns the first whitespace-separated token of s, honoring a
// leading double-quote (the unit builder quotes a space-bearing BinPath).
func firstToken(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if s[0] == '"' {
		if i := strings.IndexByte(s[1:], '"'); i >= 0 {
			return s[1 : 1+i]
		}
		return s[1:]
	}
	if i := strings.IndexByte(s, ' '); i >= 0 {
		return s[:i]
	}
	return s
}

// Update pulls (optional), rebuilds, smoke-tests, and restarts the service
// inline. Used by the `dctl service update` CLI (a separate process from the
// daemon).
func Update(ctx context.Context, c Config, src string, pull bool) error {
	if pull {
		if err := Pull(ctx, src); err != nil {
			return err
		}
	}
	if err := Build(ctx, src, c.BinPath); err != nil {
		return err
	}
	if err := Smoke(ctx, c.BinPath); err != nil {
		return err
	}
	return Restart(ctx, c)
}

func linuxPlan(c Config) Plan {
	unit := filepath.Join(c.Home, ".config", "systemd", "user", linuxUnitName)
	content := "[Unit]\n" +
		"Description=dctl Discord daemon\n" +
		"After=network-online.target\n" +
		"Wants=network-online.target\n" +
		// Don't retry forever if the daemon keeps failing (e.g. a bad token):
		// give up after 5 failures in 60s rather than spin indefinitely. These
		// live in [Unit] in modern systemd, not [Service].
		"StartLimitIntervalSec=60\n" +
		"StartLimitBurst=5\n\n" +
		"[Service]\n" +
		"Type=simple\n" +
		// The daemon loads the env file itself (serve --env-file), so the unit
		// needs no EnvironmentFile and the token never appears here.
		"ExecStart=" + joinQuoted(systemdQuote, c.BinPath, serveArgs(c)) + "\n" +
		"Restart=always\n" +
		"RestartSec=3\n\n" +
		"[Install]\n" +
		"WantedBy=default.target\n"
	cmds := []Command{
		{Argv: []string{"systemctl", "--user", "daemon-reload"}},
	}
	if c.SkipStart {
		// Enable at boot but don't start now: with no token the daemon exits
		// immediately and Restart=always would crash-loop until it's filled in.
		cmds = append(cmds, Command{Argv: []string{"systemctl", "--user", "enable", linuxUnitName}})
	} else {
		cmds = append(cmds, Command{Argv: []string{"systemctl", "--user", "enable", "--now", linuxUnitName}})
	}
	if c.User != "" {
		// Linger lets the user service keep running after logout / at boot.
		cmds = append(cmds, Command{Argv: []string{"loginctl", "enable-linger", c.User}, IgnoreErr: true})
	}
	return Plan{
		Files:    []FileWrite{{Path: unit, Content: content, Mode: 0o644}, envFileWrite(c)},
		Commands: cmds,
		Notes:    []string{startNote(c, "systemctl --user start "+linuxUnitName)},
	}
}

func macPlan(c Config) Plan {
	plist := filepath.Join(c.Home, "Library", "LaunchAgents", macLabel+".plist")
	logPath := filepath.Join(c.Home, ".local", "state", "dctl", "dctl.log")
	// No shell: launchd execs dctl directly and the daemon loads the env file
	// itself (serve --env-file). Each argument is its own array element, so
	// spaces never split a token — just XML-escape each one.
	argv := append([]string{c.BinPath}, serveArgs(c)...)
	var progArgs strings.Builder
	for _, a := range argv {
		progArgs.WriteString("    <string>" + xmlEscape(a) + "</string>\n")
	}
	content := xmlHeader +
		"<plist version=\"1.0\">\n<dict>\n" +
		"  <key>Label</key><string>" + macLabel + "</string>\n" +
		"  <key>ProgramArguments</key>\n  <array>\n" +
		progArgs.String() +
		"  </array>\n" +
		"  <key>RunAtLoad</key><true/>\n" +
		"  <key>KeepAlive</key><true/>\n" +
		"  <key>StandardOutPath</key><string>" + logPath + "</string>\n" +
		"  <key>StandardErrorPath</key><string>" + logPath + "</string>\n" +
		"</dict>\n</plist>\n"
	cmds := []Command{
		{Argv: []string{"launchctl", "unload", "-w", plist}, IgnoreErr: true},
	}
	if !c.SkipStart {
		// Skipped when no token yet: loading would start the agent, which exits
		// immediately and KeepAlive would respawn it. RunAtLoad still starts it
		// at the next login once the token is in place.
		cmds = append(cmds, Command{Argv: []string{"launchctl", "load", "-w", plist}})
	}
	return Plan{
		Files:    []FileWrite{{Path: plist, Content: content, Mode: 0o644}, envFileWrite(c)},
		Commands: cmds,
		Notes:    []string{startNote(c, "launchctl load -w "+plist)},
	}
}

func windowsPlan(c Config) Plan {
	launcher := filepath.Join(c.Home, "AppData", "Local", "dctl", "dctl-serve.cmd")
	// A tiny .cmd launcher just execs dctl; the daemon loads the env file itself
	// (serve --env-file), so there's no brittle `for /f` parsing and the task
	// never carries the token. (schtasks /tr can't quote argv reliably, hence
	// the wrapper file.)
	content := "@echo off\r\n" +
		joinQuoted(cmdQuote, c.BinPath, serveArgs(c)) + "\r\n"
	return Plan{
		Files: []FileWrite{{Path: launcher, Content: content, Mode: 0o644}, envFileWrite(c)},
		Commands: []Command{
			{Argv: []string{"schtasks", "/create", "/tn", winTaskName, "/tr", launcher, "/sc", "onlogon", "/rl", "limited", "/f"}},
		},
		// A logon-scheduled task only runs at the next sign-in, never on install.
		Notes: []string{"Edit " + c.EnvFile + " with your token; the task runs dctl at your next logon (or run it now: schtasks /run /tn " + winTaskName + ")."},
	}
}

const xmlHeader = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
`

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

// startNote returns the human-facing follow-up after an install: how to start
// the service once the token is in place (when start was skipped), or where its
// secrets live (when it's already running).
func startNote(c Config, startCmd string) string {
	if c.SkipStart {
		return "installed and enabled at boot, but NOT started — set DISCORD_BOT_TOKEN in " +
			c.EnvFile + ", then: " + startCmd
	}
	return "running. Secrets live in " + c.EnvFile + "; edit it and restart to change them."
}

// envFileHasToken reports whether the env file already carries a non-empty
// DISCORD_BOT_TOKEN, so install can start the service immediately instead of
// configuring a crash-loop with an empty template.
func envFileHasToken(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok &&
			strings.TrimSpace(k) == "DISCORD_BOT_TOKEN" && strings.TrimSpace(v) != "" {
			return true
		}
	}
	return false
}

// quoteArgv joins a binary path and its args for an ExecStart line, quoting the
// command line, applying quote to the binary and every argument so a path or
// value containing spaces or metacharacters survives the target's parser.
func joinQuoted(quote func(string) string, bin string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, quote(bin))
	for _, a := range args {
		parts = append(parts, quote(a))
	}
	return strings.Join(parts, " ")
}

// systemdQuote quotes a token for a systemd ExecStart line. systemd splits on
// whitespace unless double-quoted, and treats backslash as an escape inside the
// quotes, so spaces/quotes/backslashes must be wrapped and escaped.
func systemdQuote(s string) string {
	if s == "" {
		return `""`
	}
	if !strings.ContainsAny(s, " \t\n\"\\'") {
		return s
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}

// cmdQuote double-quotes a token for cmd.exe when it contains a space, tab or
// quote (doubling embedded quotes), so a "Program Files" path stays one token.
func cmdQuote(s string) string {
	if s == "" {
		return `""`
	}
	if !strings.ContainsAny(s, " \t\"") {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// DefaultConfig fills a Config from the current environment (binary path, home,
// user, default env-file location and health address).
func DefaultConfig() (Config, error) {
	bin, err := os.Executable()
	if err != nil {
		return Config{}, fmt.Errorf("locate dctl binary: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, fmt.Errorf("locate home dir: %w", err)
	}
	uname := ""
	if u, err := user.Current(); err == nil {
		uname = u.Username
	}
	return Config{
		BinPath:    bin,
		Home:       home,
		User:       uname,
		EnvFile:    filepath.Join(home, ".config", "dctl", "dctl.env"),
		ConfigPath: filepath.Join(home, ".config", "dctl", "config.json"),
		HealthAddr: "127.0.0.1:8787",
	}, nil
}

// Install runs the install plan for the current OS: it writes the unit/launcher
// and secrets template, then enables and starts the service.
func Install(ctx context.Context, c Config) error {
	// Without a token the daemon exits immediately; starting it now would just
	// crash-loop until the user edits the template. Configure boot-start only.
	if !envFileHasToken(c.EnvFile) {
		c.SkipStart = true
	}
	p, err := BuildPlan(c)
	if err != nil {
		return err
	}
	return runPlan(ctx, p)
}

// Uninstall stops and removes the service for the current OS.
func Uninstall(ctx context.Context, c Config) error {
	p, err := BuildUninstall(c)
	if err != nil {
		return err
	}
	return runPlan(ctx, p)
}

// Status prints the platform's service status to stdout/stderr.
func Status(ctx context.Context, c Config) error {
	cmd, err := StatusCommand(c)
	if err != nil {
		return err
	}
	return runCommand(ctx, cmd)
}

func runPlan(ctx context.Context, p Plan) error {
	for _, f := range p.Files {
		if err := writeFile(f); err != nil {
			return err
		}
	}
	for _, cmd := range p.Commands {
		if err := runCommand(ctx, cmd); err != nil {
			return err
		}
	}
	for _, n := range p.Notes {
		fmt.Fprintln(os.Stderr, "dctl service: "+n)
	}
	return nil
}

func writeFile(f FileWrite) error {
	if f.Template {
		// Never overwrite an existing secrets file. Only a definite "not found"
		// permits writing the template; any other stat error is fatal rather
		// than risk clobbering a file we simply couldn't read.
		switch _, err := os.Stat(f.Path); {
		case err == nil:
			return nil
		case !errors.Is(err, fs.ErrNotExist):
			return fmt.Errorf("stat %s: %w", f.Path, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(f.Path), err)
	}
	if err := os.WriteFile(f.Path, []byte(f.Content), f.Mode); err != nil {
		return fmt.Errorf("write %s: %w", f.Path, err)
	}
	return nil
}

// runCapture runs name in dir and returns its combined output, so a failing
// build/pull can surface the toolchain's own message to the caller. It forces
// GIT_TERMINAL_PROMPT=0 so an unattended `git pull` fails fast on a missing
// credential instead of blocking on a prompt that no one will answer.
func runCapture(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	c := exec.CommandContext(ctx, name, args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := c.CombinedOutput()
	return []byte(strings.TrimSpace(string(out))), err
}

func runCommand(ctx context.Context, cmd Command) error {
	if len(cmd.Argv) == 0 {
		return nil
	}
	c := exec.CommandContext(ctx, cmd.Argv[0], cmd.Argv[1:]...)
	c.Stdout, c.Stderr = os.Stderr, os.Stderr
	if err := c.Run(); err != nil && !cmd.IgnoreErr {
		return fmt.Errorf("%s: %w", strings.Join(cmd.Argv, " "), err)
	}
	return nil
}

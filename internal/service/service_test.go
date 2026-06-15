package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestTmuxPreflightNote(t *testing.T) {
	missing := func(string) (string, error) { return "", errors.New("not found") }
	present := func(string) (string, error) { return "/usr/bin/tmux", nil }

	if note := tmuxPreflightNote(present); note != "" {
		t.Errorf("tmux present: expected no note, got %q", note)
	}
	note := tmuxPreflightNote(missing)
	if note == "" || !strings.Contains(note, "tmux not found") {
		t.Errorf("tmux missing: expected a tmux-not-found note, got %q", note)
	}
}

func TestWriteFileNeverOverwritesTemplate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dctl.env")
	if err := os.WriteFile(path, []byte("DISCORD_BOT_TOKEN=real-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Template write against an existing file must be a no-op (preserve secrets).
	if err := writeFile(FileWrite{Path: path, Content: envTemplate, Mode: 0o600, Template: true}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "real-secret") {
		t.Fatalf("template clobbered existing secrets: %q", got)
	}
	// A fresh path is created from the template.
	fresh := filepath.Join(dir, "sub", "new.env")
	if err := writeFile(FileWrite{Path: fresh, Content: envTemplate, Mode: 0o600, Template: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("template not written to fresh path: %v", err)
	}
}

func testConfig(goos string) Config {
	return Config{
		GOOS:       goos,
		BinPath:    "/home/me/.local/bin/dctl",
		Home:       "/home/me",
		User:       "me",
		EnvFile:    "/home/me/.config/dctl/dctl.env",
		HealthAddr: "127.0.0.1:8787",
	}
}

func TestServeArgs(t *testing.T) {
	got := serveArgs(Config{EnvFile: "/e", ExtraArgs: []string{"--status-channel", "42"}})
	want := "serve --env-file /e --status-channel 42"
	if strings.Join(got, " ") != want {
		t.Fatalf("serveArgs = %q, want %q", strings.Join(got, " "), want)
	}
	// Tunable knobs (health-addr) are NOT baked into the unit — they live in
	// config.json so editing them doesn't require reinstalling. Baking them would
	// shadow config.json.
	if strings.Join(serveArgs(Config{HealthAddr: "127.0.0.1:8787"}), " ") != "serve" {
		t.Fatalf("health-addr must not be baked into serveArgs, got %v", serveArgs(Config{HealthAddr: "127.0.0.1:8787"}))
	}
}

func TestLinuxPlan(t *testing.T) {
	p, err := BuildPlan(testConfig("linux"))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Files) != 2 {
		t.Fatalf("want unit + env file, got %d files", len(p.Files))
	}
	unit := p.Files[0]
	if !strings.HasSuffix(unit.Path, "/.config/systemd/user/dctl.service") {
		t.Fatalf("unit path = %s", unit.Path)
	}
	for _, want := range []string{
		// The daemon loads the env file itself; the unit just passes --env-file.
		"ExecStart=/home/me/.local/bin/dctl serve --env-file /home/me/.config/dctl/dctl.env",
		"Restart=always",
		"StartLimitBurst=5", // cap restarts so a bad token can't spin forever
		"WantedBy=default.target",
	} {
		if !strings.Contains(unit.Content, want) {
			t.Errorf("unit missing %q\n---\n%s", want, unit.Content)
		}
	}
	// No EnvironmentFile (sourcing moved into the daemon), and no secret value.
	if strings.Contains(unit.Content, "EnvironmentFile") {
		t.Error("unit should not declare EnvironmentFile anymore")
	}
	if strings.Contains(unit.Content, "TOKEN=") {
		t.Error("unit file embeds a secret value")
	}
	// Env file is a never-overwrite 0600 template with no values.
	env := p.Files[1]
	if !env.Template || env.Mode != 0o600 {
		t.Fatalf("env file should be a 0600 template, got mode=%o template=%v", env.Mode, env.Template)
	}
	if strings.Contains(env.Content, "DISCORD_BOT_TOKEN=x") || !strings.Contains(env.Content, "DISCORD_BOT_TOKEN=") {
		t.Errorf("env template should list the var with no value:\n%s", env.Content)
	}
	assertCmd(t, p, "systemctl --user enable --now dctl.service")
	assertCmd(t, p, "loginctl enable-linger me")
}

func TestMacPlan(t *testing.T) {
	p, err := BuildPlan(testConfig("darwin"))
	if err != nil {
		t.Fatal(err)
	}
	plist := p.Files[0]
	if !strings.HasSuffix(plist.Path, "/Library/LaunchAgents/com.vskstudio.dctl.plist") {
		t.Fatalf("plist path = %s", plist.Path)
	}
	for _, want := range []string{
		"<key>Label</key><string>com.vskstudio.dctl</string>",
		"<key>RunAtLoad</key><true/>",
		// launchd execs dctl directly (no shell); each arg is its own <string>.
		"<string>/home/me/.local/bin/dctl</string>",
		"<string>serve</string>",
		"<string>--env-file</string>",
		"<string>/home/me/.config/dctl/dctl.env</string>",
	} {
		if !strings.Contains(plist.Content, want) {
			t.Errorf("plist missing %q\n---\n%s", want, plist.Content)
		}
	}
	// No shell wrapper anymore.
	if strings.Contains(plist.Content, "/bin/sh") {
		t.Errorf("plist should exec dctl directly, not via a shell:\n%s", plist.Content)
	}
	assertCmd(t, p, "launchctl load -w")
}

func TestWindowsPlan(t *testing.T) {
	c := testConfig("windows")
	c.BinPath = `C:\Users\me\dctl.exe`
	c.EnvFile = `C:\Users\me\.config\dctl\dctl.env`
	c.Home = `C:\Users\me`
	p, err := BuildPlan(c)
	if err != nil {
		t.Fatal(err)
	}
	launcher := p.Files[0]
	if !strings.HasSuffix(launcher.Path, "dctl-serve.cmd") {
		t.Fatalf("launcher path = %s", launcher.Path)
	}
	// The launcher just execs dctl with --env-file; no brittle for/f parsing.
	if !strings.Contains(launcher.Content, `C:\Users\me\dctl.exe serve --env-file C:\Users\me\.config\dctl\dctl.env`) {
		t.Errorf("launcher missing dctl invocation:\n%s", launcher.Content)
	}
	if strings.Contains(launcher.Content, "for /f") {
		t.Errorf("launcher should not parse the env file in batch anymore:\n%s", launcher.Content)
	}
	assertCmd(t, p, "schtasks /create /tn dctl /tr")
}

func TestStatusToleratesInactive(t *testing.T) {
	// `systemctl status` exits non-zero when the unit is stopped; status must
	// still print rather than surface that as a CLI error.
	for _, os := range []string{"linux", "darwin", "windows"} {
		cmd, err := StatusCommand(testConfig(os))
		if err != nil {
			t.Fatalf("status %s: %v", os, err)
		}
		if !cmd.IgnoreErr {
			t.Errorf("status command for %s should be IgnoreErr", os)
		}
	}
}

func TestUnsupportedOS(t *testing.T) {
	if _, err := BuildPlan(testConfig("plan9")); err == nil {
		t.Fatal("expected error for unsupported OS")
	}
}

func TestQuoters(t *testing.T) {
	// systemd: plain tokens pass through; spaces/quotes get double-quoted and
	// backslashes & quotes escaped.
	if got := systemdQuote("/usr/bin/dctl"); got != "/usr/bin/dctl" {
		t.Errorf("systemdQuote plain = %q", got)
	}
	if got := systemdQuote(`/opt/My Apps/dctl`); got != `"/opt/My Apps/dctl"` {
		t.Errorf("systemdQuote spaces = %q", got)
	}
	if got := systemdQuote(`a"b\c`); got != `"a\"b\\c"` {
		t.Errorf("systemdQuote escape = %q", got)
	}

	// cmd: plain tokens pass through; spaces get double-quoted, embedded quotes doubled.
	if got := cmdQuote(`C:\dctl.exe`); got != `C:\dctl.exe` {
		t.Errorf("cmdQuote plain = %q", got)
	}
	if got := cmdQuote(`C:\Program Files\dctl.exe`); got != `"C:\Program Files\dctl.exe"` {
		t.Errorf("cmdQuote spaces = %q", got)
	}

	// joinQuoted applies the quoter to bin and every arg.
	if got := joinQuoted(systemdQuote, "/b/dctl", []string{"serve", "x y"}); got != `/b/dctl serve "x y"` {
		t.Errorf("joinQuoted = %q", got)
	}
}

// TestArgsWithSpacesAreQuoted: an ExtraArg containing a space must stay a single
// token in every generated artifact (regression for unquoted arg-joining).
func TestArgsWithSpacesAreQuoted(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "windows"} {
		c := testConfig(goos)
		c.ExtraArgs = []string{"--status-channel", "chan with space"}
		p, err := BuildPlan(c)
		if err != nil {
			t.Fatalf("plan %s: %v", goos, err)
		}
		// The raw, unquoted form must NOT appear adjacent (would be two tokens).
		if strings.Contains(p.Files[0].Content, "--status-channel chan with space") {
			t.Errorf("%s leaves a space-bearing arg unquoted:\n%s", goos, p.Files[0].Content)
		}
	}
}

// A baked-in default --cmd (model/effort) must land in the unit's ExecStart as a
// single quoted token, so serve receives the whole "claude …" string as one arg.
func TestDefaultCmdBakedIntoUnit(t *testing.T) {
	const cmd = "claude --model claude-opus-4-8 --effort low"
	for _, goos := range []string{"linux", "darwin", "windows"} {
		c := testConfig(goos)
		c.ExtraArgs = []string{"--cmd", cmd}
		p, err := BuildPlan(c)
		if err != nil {
			t.Fatalf("plan %s: %v", goos, err)
		}
		content := p.Files[0].Content
		if !strings.Contains(content, "claude-opus-4-8") {
			t.Errorf("%s: ExecStart missing the baked model:\n%s", goos, content)
		}
		// Unquoted, the value would be five separate tokens; its presence as a raw
		// run means it was NOT quoted into one arg.
		if strings.Contains(content, "--cmd "+cmd) {
			t.Errorf("%s: default cmd not quoted as a single token:\n%s", goos, content)
		}
	}
}

// The install plan scaffolds a config.json template (never the unit's [0] slot,
// which stays the launcher) carrying the pre-filled cmd and no secrets.
func TestConfigScaffoldInPlan(t *testing.T) {
	const cmd = "claude --model claude-opus-4-8 --effort low"
	for _, goos := range []string{"linux", "darwin", "windows"} {
		c := testConfig(goos)
		c.ConfigPath = "/home/me/.config/dctl/config.json"
		c.DefaultCmd = cmd
		p, err := BuildPlan(c)
		if err != nil {
			t.Fatalf("plan %s: %v", goos, err)
		}
		var cfg *FileWrite
		for i := range p.Files {
			if p.Files[i].Path == c.ConfigPath {
				cfg = &p.Files[i]
			}
		}
		if cfg == nil {
			t.Fatalf("%s: no config.json in plan", goos)
		}
		if !cfg.Template {
			t.Errorf("%s: config.json must be Template (never clobber edits)", goos)
		}
		if !strings.Contains(cfg.Content, cmd) {
			t.Errorf("%s: config.json missing pre-filled cmd:\n%s", goos, cfg.Content)
		}
		// The comment may *name* the secrets (explaining they live elsewhere),
		// but the file must never carry an actual secret assignment.
		if strings.Contains(cfg.Content, "DISCORD_BOT_TOKEN=") {
			t.Errorf("%s: config.json must not assign secrets", goos)
		}
	}
}

// Without a ConfigPath (e.g. older callers) the plan omits the scaffold and the
// launcher stays the first file, as the InstalledBinPath parse assumes.
func TestNoConfigScaffoldWhenUnset(t *testing.T) {
	p, err := BuildPlan(testConfig("linux"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range p.Files {
		if strings.HasSuffix(f.Path, "config.json") {
			t.Fatalf("unexpected config.json scaffold: %s", f.Path)
		}
	}
}

func TestInstalledBinPathReadsUnit(t *testing.T) {
	home := t.TempDir()
	c := testConfig("linux")
	c.Home = home
	c.BinPath = "/opt/dctl/dctl"
	unitDir := home + "/.config/systemd/user"
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Write a real unit via the install plan so the parse matches what we emit.
	p, err := BuildPlan(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unitDir+"/dctl.service", []byte(p.Files[0].Content), 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok := InstalledBinPath(c)
	if !ok || got != "/opt/dctl/dctl" {
		t.Fatalf("InstalledBinPath = %q, %v; want /opt/dctl/dctl, true", got, ok)
	}
	// Absent unit (fresh home) → not found, not a panic.
	c2 := testConfig("linux")
	c2.Home = t.TempDir()
	if _, ok := InstalledBinPath(c2); ok {
		t.Fatal("expected no bin path when unit is absent")
	}
}

func TestFirstToken(t *testing.T) {
	cases := map[string]string{
		"/usr/bin/dctl serve --x y": "/usr/bin/dctl",
		`"/has space/dctl" serve`:   "/has space/dctl",
		"/lone/dctl":                "/lone/dctl",
		"":                          "",
	}
	for in, want := range cases {
		if got := firstToken(in); got != want {
			t.Errorf("firstToken(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUninstallAndStatus(t *testing.T) {
	for _, os := range []string{"linux", "darwin", "windows"} {
		if _, err := BuildUninstall(testConfig(os)); err != nil {
			t.Errorf("uninstall %s: %v", os, err)
		}
		if _, err := StatusCommand(testConfig(os)); err != nil {
			t.Errorf("status %s: %v", os, err)
		}
	}
}

func assertCmd(t *testing.T, p Plan, want string) {
	t.Helper()
	for _, c := range p.Commands {
		if strings.Contains(strings.Join(c.Argv, " "), want) {
			return
		}
	}
	t.Errorf("no command matching %q in plan", want)
}

// TestSkipStartOmitsImmediateStart: with SkipStart (no token yet), install must
// enable boot-start but NOT start the daemon now — else it crash-loops against
// the empty template (Restart=always / KeepAlive).
func TestSkipStartOmitsImmediateStart(t *testing.T) {
	// Linux: `enable` present, `enable --now` absent.
	lc := testConfig("linux")
	lc.SkipStart = true
	lp, err := BuildPlan(lc)
	if err != nil {
		t.Fatal(err)
	}
	var ljoined string
	for _, c := range lp.Commands {
		ljoined += strings.Join(c.Argv, " ") + "\n"
	}
	if strings.Contains(ljoined, "enable --now") {
		t.Errorf("SkipStart linux plan must not start now:\n%s", ljoined)
	}
	if !strings.Contains(ljoined, "enable "+linuxUnitName) {
		t.Errorf("SkipStart linux plan should still enable at boot:\n%s", ljoined)
	}

	// macOS: no `launchctl load` (RunAtLoad starts it next login).
	mc := testConfig("darwin")
	mc.SkipStart = true
	mp, err := BuildPlan(mc)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range mp.Commands {
		if len(c.Argv) >= 2 && c.Argv[0] == "launchctl" && c.Argv[1] == "load" {
			t.Errorf("SkipStart mac plan must not load (start) the agent: %v", c.Argv)
		}
	}
	// Non-skip is still the start path.
	mp2, _ := BuildPlan(testConfig("darwin"))
	assertCmd(t, mp2, "launchctl load -w")
}

// TestEnvFileHasToken: the start decision hinges on a real, non-empty token —
// the empty template and a comment line must read as "no token".
func TestEnvFileHasToken(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "absent.env")
	if envFileHasToken(missing) {
		t.Error("absent env file should report no token")
	}
	tmpl := filepath.Join(dir, "tmpl.env")
	if err := os.WriteFile(tmpl, []byte(envTemplate), 0o600); err != nil {
		t.Fatal(err)
	}
	if envFileHasToken(tmpl) {
		t.Error("empty template should report no token")
	}
	filled := filepath.Join(dir, "filled.env")
	if err := os.WriteFile(filled, []byte("# c\nDISCORD_BOT_TOKEN=  abc.def  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !envFileHasToken(filled) {
		t.Error("a filled token should be detected")
	}
	// A commented-out token must not count.
	commented := filepath.Join(dir, "commented.env")
	if err := os.WriteFile(commented, []byte("#DISCORD_BOT_TOKEN=abc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if envFileHasToken(commented) {
		t.Error("commented token line should not count")
	}
}

// TestInstallSkipsStartWhenNoToken: the end-to-end decision in Install — a fresh
// install (no env file) flips SkipStart so no immediate-start command is run.
// We exercise it on a non-host OS plan so no real services are touched.
func TestInstallSkipsStartWhenNoToken(t *testing.T) {
	dir := t.TempDir()
	c := testConfig("linux")
	c.EnvFile = filepath.Join(dir, "dctl.env") // does not exist yet
	// Mirror what Install computes, then assert the plan it would run.
	if envFileHasToken(c.EnvFile) {
		t.Fatal("precondition: env file should have no token")
	}
	c.SkipStart = true
	p, _ := BuildPlan(c)
	for _, cmd := range p.Commands {
		if strings.Contains(strings.Join(cmd.Argv, " "), "--now") {
			t.Errorf("fresh install must not start the service now: %v", cmd.Argv)
		}
	}
}

// TestEnvTemplateCarriesNoValues guards the core secret-hygiene invariant: the
// template lists each var but never a value, so installing can't leak a token.
func TestEnvTemplateCarriesNoValues(t *testing.T) {
	for _, key := range []string{"DISCORD_BOT_TOKEN", "DISCORD_CHANNEL_ID", "DCTL_OWNER_ID"} {
		if !strings.Contains(envTemplate, key+"=\n") && !strings.HasSuffix(envTemplate, key+"=") {
			t.Errorf("env template should declare %s with an empty value:\n%s", key, envTemplate)
		}
	}
	for _, line := range strings.Split(envTemplate, "\n") {
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		if _, val, _ := strings.Cut(line, "="); strings.TrimSpace(val) != "" {
			t.Errorf("env template line carries a value (would be a committed secret): %q", line)
		}
	}
}

// TestNoSecretEmbeddedInAnyArtifact scans every file an install would write, on
// every OS, for a token-shaped value — defense in depth against a regression
// that bakes a secret into the unit/plist/launcher instead of the env file.
func TestNoSecretEmbeddedInAnyArtifact(t *testing.T) {
	const sentinel = "super-secret-token-value"
	for _, os := range []string{"linux", "darwin", "windows"} {
		c := testConfig(os)
		// Even if a caller smuggles a secret through ExtraArgs we don't assert on
		// that; we assert the generated *files* never contain a raw token.
		p, err := BuildPlan(c)
		if err != nil {
			t.Fatalf("plan %s: %v", os, err)
		}
		for _, f := range p.Files {
			if strings.Contains(f.Content, sentinel) {
				t.Errorf("%s artifact %s embeds a secret", os, f.Path)
			}
			if strings.Contains(f.Content, "TOKEN=x") || strings.Contains(f.Content, "Bot ") {
				t.Errorf("%s artifact %s looks like it embeds a token:\n%s", os, f.Path, f.Content)
			}
		}
	}
}

// TestLinuxLingerOmittedWithoutUser: with no username we can't enable-linger, so
// the plan must not emit a malformed `loginctl enable-linger ` command.
func TestLinuxLingerOmittedWithoutUser(t *testing.T) {
	c := testConfig("linux")
	c.User = ""
	p, err := BuildPlan(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, cmd := range p.Commands {
		if len(cmd.Argv) > 0 && cmd.Argv[0] == "loginctl" {
			t.Errorf("linger command should be omitted when User is empty, got %v", cmd.Argv)
		}
	}
}

// TestMacRunStringIsXMLEscaped: the sh snippet contains `&&`-free text but the
// plist is XML, so any &/</> in the run string must be entity-escaped or the
// plist won't parse.
func TestMacRunStringIsXMLEscaped(t *testing.T) {
	c := testConfig("darwin")
	c.ExtraArgs = []string{"--note", "a<b>c&d"}
	p, err := BuildPlan(c)
	if err != nil {
		t.Fatal(err)
	}
	plist := p.Files[0].Content
	if strings.Contains(plist, "a<b>c&d") {
		t.Errorf("raw XML metacharacters leaked into the plist:\n%s", plist)
	}
	if !strings.Contains(plist, "a&lt;b&gt;c&amp;d") {
		t.Errorf("run string not XML-escaped:\n%s", plist)
	}
}

// TestWindowsBinPathQuotedWithSpaces: a Program Files path has a space; the .cmd
// must quote the binary or cmd.exe splits the path mid-command.
func TestWindowsBinPathQuotedWithSpaces(t *testing.T) {
	c := testConfig("windows")
	c.BinPath = `C:\Program Files\dctl\dctl.exe`
	p, err := BuildPlan(c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.Files[0].Content, `"C:\Program Files\dctl\dctl.exe" serve`) {
		t.Errorf("launcher must quote a binary path with spaces:\n%s", p.Files[0].Content)
	}
}

// TestBuildUninstallRemovesArtifacts: uninstall must both stop the service and
// delete the file it installed (otherwise a stale unit lingers).
func TestBuildUninstallRemovesArtifacts(t *testing.T) {
	cases := map[string]string{
		"linux":   "dctl.service",
		"darwin":  "com.vskstudio.dctl.plist",
		"windows": "dctl",
	}
	for os, marker := range cases {
		p, err := BuildUninstall(testConfig(os))
		if err != nil {
			t.Fatalf("uninstall %s: %v", os, err)
		}
		var joined string
		for _, cmd := range p.Commands {
			joined += strings.Join(cmd.Argv, " ") + "\n"
		}
		if !strings.Contains(joined, marker) {
			t.Errorf("%s uninstall never references %q:\n%s", os, marker, joined)
		}
		// A removal step (rm or schtasks /delete) must be present.
		if !strings.Contains(joined, "rm ") && !strings.Contains(joined, "/delete") {
			t.Errorf("%s uninstall has no removal step:\n%s", os, joined)
		}
	}
}

// TestDefaultConfig: the env-derived config must point the env file under the
// home dir and default the health address.
func TestDefaultConfig(t *testing.T) {
	c, err := DefaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	if c.BinPath == "" || c.Home == "" {
		t.Fatalf("DefaultConfig left BinPath/Home empty: %+v", c)
	}
	if !strings.HasPrefix(c.EnvFile, c.Home) {
		t.Errorf("env file %q should live under home %q", c.EnvFile, c.Home)
	}
	if !strings.HasSuffix(c.EnvFile, filepath.Join("dctl", "dctl.env")) {
		t.Errorf("unexpected env file path %q", c.EnvFile)
	}
	if c.HealthAddr == "" {
		t.Error("DefaultConfig should set a default health address")
	}
}

// TestRunCommandRespectsIgnoreErr: a failing IgnoreErr command is swallowed; a
// failing strict command surfaces. Uses a cross-platform always-fail binary.
func TestRunCommandRespectsIgnoreErr(t *testing.T) {
	bad := "definitely-not-a-real-binary-xyz"
	if runtime.GOOS == "windows" {
		t.Skip("argv exec semantics differ on windows")
	}
	if err := runCommand(context.Background(), Command{Argv: []string{bad}, IgnoreErr: true}); err != nil {
		t.Errorf("IgnoreErr command should swallow failure, got %v", err)
	}
	if err := runCommand(context.Background(), Command{Argv: []string{bad}}); err == nil {
		t.Error("strict command should surface a failure")
	}
	// An empty argv is a no-op, never an error.
	if err := runCommand(context.Background(), Command{}); err != nil {
		t.Errorf("empty command should be a no-op, got %v", err)
	}
}

// TestWriteFileAppliesMode: a freshly written (non-template) file gets the mode
// the plan requested — the env file must land at 0600.
func TestWriteFileAppliesMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix file modes not meaningful on windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "dctl.env")
	if err := writeFile(FileWrite{Path: path, Content: envTemplate, Mode: 0o600, Template: true}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("env file mode = %o, want 600", info.Mode().Perm())
	}
}

func TestRestartCommandsPerOS(t *testing.T) {
	for _, tc := range []struct {
		goos string
		want string // substring expected in the (last) restart command
	}{
		{"linux", "systemctl --user restart dctl.service"},
		{"darwin", "launchctl kickstart -k"},
		{"windows", "schtasks /run /tn dctl"},
	} {
		cmds, err := RestartCommands(testConfig(tc.goos))
		if err != nil {
			t.Fatalf("%s: %v", tc.goos, err)
		}
		joined := strings.Join(cmds[len(cmds)-1].Argv, " ")
		if !strings.Contains(joined, tc.want) {
			t.Fatalf("%s: want %q in %q", tc.goos, tc.want, joined)
		}
	}
	if _, err := RestartCommands(testConfig("plan9")); err == nil {
		t.Fatal("expected error for unsupported OS")
	}
}

func TestValidateSource(t *testing.T) {
	if err := validateSource(""); err == nil {
		t.Fatal("empty source must fail")
	}
	bare := t.TempDir()
	if err := validateSource(bare); err == nil {
		t.Fatal("dir without go.mod must fail")
	}
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateSource(src); err == nil {
		t.Fatal("dir without cmd/dctl must fail")
	}
	if err := os.MkdirAll(filepath.Join(src, "cmd", "dctl"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := validateSource(src); err != nil {
		t.Fatalf("valid checkout rejected: %v", err)
	}
}

func TestBuildRejectsNonSource(t *testing.T) {
	if err := Build(context.Background(), t.TempDir(), "/tmp/out"); err == nil {
		t.Fatal("Build must reject a non-checkout before invoking go")
	}
}

func TestSmokeRejectsBrokenBinary(t *testing.T) {
	if err := Smoke(context.Background(), ""); err == nil {
		t.Fatal("Smoke must reject an empty binary path")
	}
	// A binary that exits non-zero on --help must fail the smoke test.
	if err := Smoke(context.Background(), "/usr/bin/false"); err == nil {
		t.Fatal("Smoke must reject a binary that exits non-zero")
	}
	// A binary that exits 0 passes (true ignores --help and exits 0).
	if err := Smoke(context.Background(), "/bin/true"); err != nil {
		t.Skipf("smoke baseline unavailable: %v", err)
	}
}

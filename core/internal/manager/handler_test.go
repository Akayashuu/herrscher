package manager

import (
	"context"
	"errors"
	"strings"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher/core/internal/agent"
	"github.com/Herrscherd/herrscher/core/internal/state"
)

type sentMsg struct{ channelID, content string }

type fakeDiscord struct {
	created  []string
	archived []string
	homeType string
	sent     []sentMsg
	sendErr  error
}

func (f *fakeDiscord) Kind(ctx context.Context, id string) (string, error) {
	return f.homeType, nil
}
func (f *fakeDiscord) CreateUnder(ctx context.Context, parentID, name string) (string, error) {
	f.created = append(f.created, name)
	return "new-" + name, nil
}
func (f *fakeDiscord) ForumPost(ctx context.Context, forumID, name, content string) (string, error) {
	f.created = append(f.created, "forum:"+name)
	return "post-" + name, nil
}
func (f *fakeDiscord) Archive(ctx context.Context, id string) error {
	f.archived = append(f.archived, id)
	return nil
}
func (f *fakeDiscord) Send(ctx context.Context, channelID, content string) error {
	if f.sendErr != nil {
		return f.sendErr
	}
	f.sent = append(f.sent, sentMsg{channelID: channelID, content: content})
	return nil
}

func TestDiscordInterfaceHasSend(t *testing.T) {
	var _ discord = (*fakeDiscord)(nil)
}

type fakeSup struct{ started, stopped []string }

func (f *fakeSup) Start(s state.Session) error { f.started = append(f.started, s.Name); return nil }
func (f *fakeSup) Stop(name string) error      { f.stopped = append(f.stopped, name); return nil }

type fakeWT struct {
	createdRepos []string // repo arg captured per Create
	created      []string
	removed      []string
	path         string // "" → simulate shared fallback
	removeErr    error  // simulate dirty worktree
}

func (f *fakeWT) Create(repo, name string) (string, error) {
	f.createdRepos = append(f.createdRepos, repo)
	f.created = append(f.created, name)
	return f.path, nil
}
func (f *fakeWT) Branch(name string) string { return "session/" + name }
func (f *fakeWT) Remove(repo, name string, force bool) error {
	if f.removeErr != nil && !force {
		return f.removeErr
	}
	f.removed = append(f.removed, name)
	return nil
}

type fakeForge struct {
	cloneDir string
	cloneErr error
	cloned   []string // specs passed to Clone
}

func (f *fakeForge) Clone(ctx context.Context, spec, workspace string) (string, error) {
	f.cloned = append(f.cloned, spec)
	if f.cloneErr != nil {
		return "", f.cloneErr
	}
	return f.cloneDir, nil
}

type fakeUpdater struct {
	version    string
	buildErr   error
	restartErr error
	builds     []bool // pull flag per Build call
	restarts   int
}

func (f *fakeUpdater) Build(ctx context.Context, pull bool) (string, error) {
	f.builds = append(f.builds, pull)
	return f.version, f.buildErr
}
func (f *fakeUpdater) Restart(ctx context.Context) error { f.restarts++; return f.restartErr }

func newTestHandler(t *testing.T, homeType string) (*Handler, *fakeDiscord, *fakeSup, *fakeWT, *fakeForge, *state.State) {
	t.Helper()
	h, _, d, sup, wt, fg, st := newTestHandlerWithUpdater(t, homeType)
	return h, d, sup, wt, fg, st
}

func newTestHandlerWithUpdater(t *testing.T, homeType string) (*Handler, *fakeUpdater, *fakeDiscord, *fakeSup, *fakeWT, *fakeForge, *state.State) {
	t.Helper()
	d := &fakeDiscord{homeType: homeType}
	sup := &fakeSup{}
	wt := &fakeWT{path: "/wt/x"}
	fg := &fakeForge{}
	up := &fakeUpdater{version: "abc1234"}
	st := state.NewState(t.TempDir() + "/s.json")
	agents := agent.NewStore(t.TempDir())
	return NewHandler(d, sup, wt, fg, up, agents, st, "claude", t.TempDir()), up, d, sup, wt, fg, st
}

// args builds a command Input from name/value pairs (flags carry "true").
func args(kv ...string) contracts.Input {
	m := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return contracts.Input{Args: m}
}

func TestSessionCreateText(t *testing.T) {
	h, d, sup, wt, _, st := newTestHandler(t, "")
	st.SetHome(state.HomeRef{ID: "cat1", Type: "category"})
	if _, err := h.sessionCreateRun(context.Background(), args("name", "demo")); err != nil {
		t.Fatal(err)
	}
	if len(d.created) != 1 || d.created[0] != "demo" {
		t.Fatalf("expected channel created: %+v", d.created)
	}
	if len(wt.created) != 1 {
		t.Fatalf("expected worktree created: %+v", wt.created)
	}
	if len(sup.started) != 1 {
		t.Fatalf("expected bridge started: %+v", sup.started)
	}
	sess, ok := st.FindSession("demo")
	if !ok || sess.Worktree != "/wt/x" {
		t.Fatalf("session not persisted with worktree: %+v", sess)
	}
	// No backend option given → defaults to the persistent stream-json backend.
	if sess.Backend != "stream" {
		t.Fatalf("expected default backend stream, got %q", sess.Backend)
	}
}

func TestSessionCreateShared(t *testing.T) {
	h, _, _, wt, _, st := newTestHandler(t, "")
	st.SetHome(state.HomeRef{ID: "cat1", Type: "category"})
	if _, err := h.sessionCreateRun(context.Background(), args("name", "demo", "shared", "true")); err != nil {
		t.Fatal(err)
	}
	if len(wt.created) != 0 {
		t.Fatalf("shared session should not create a worktree: %+v", wt.created)
	}
	sess, _ := st.FindSession("demo")
	if sess.Worktree != "" {
		t.Fatalf("shared session should have empty worktree: %+v", sess)
	}
}

// Names that slugify to nothing usable (no letters/digits) are rejected outright.
func TestSessionCreateRejectsUnsafeName(t *testing.T) {
	for _, name := range []string{"..", "/", "---", "   ", "🙂", ""} {
		h, d, _, wt, _, st := newTestHandler(t, "")
		st.SetHome(state.HomeRef{ID: "cat1", Type: "category"})
		if _, err := h.sessionCreateRun(context.Background(), args("name", name)); err == nil {
			t.Fatalf("name %q: expected rejection", name)
		}
		if len(wt.created) != 0 || len(d.created) != 0 {
			t.Fatalf("name %q: nothing should be created on rejection (wt=%v ch=%v)", name, wt.created, d.created)
		}
		if _, ok := st.FindSession(name); ok {
			t.Fatalf("name %q: must not persist a session", name)
		}
	}
}

// Unsafe-looking but non-empty names are slugified into a safe slug.
func TestSessionCreateSlugifiesName(t *testing.T) {
	cases := map[string]string{
		"../escape":   "escape",
		"a/b":         "a-b",
		"with space":  "with-space",
		"bad;rm":      "bad-rm",
		"cmd improve": "cmd-improve",
		"Feat/Login":  "feat-login",
	}
	for raw, want := range cases {
		h, _, _, _, _, st := newTestHandler(t, "")
		st.SetHome(state.HomeRef{ID: "cat1", Type: "category"})
		if _, err := h.sessionCreateRun(context.Background(), args("name", raw)); err != nil {
			t.Fatalf("name %q: %v", raw, err)
		}
		if _, ok := st.FindSession(want); !ok {
			t.Fatalf("name %q: expected session under slug %q, sessions=%+v", raw, want, st.SnapshotSessions())
		}
	}
}

func TestSessionCreateAcceptsSafeName(t *testing.T) {
	h, d, _, _, _, st := newTestHandler(t, "")
	st.SetHome(state.HomeRef{ID: "cat1", Type: "category"})
	if _, err := h.sessionCreateRun(context.Background(), args("name", "feat_login-2")); err != nil {
		t.Fatalf("safe name should be accepted: %v", err)
	}
	if len(d.created) != 1 {
		t.Fatalf("safe name should be accepted: %+v", d.created)
	}
}

func TestSessionCreateRequiresHome(t *testing.T) {
	h, _, _, _, _, _ := newTestHandler(t, "")
	if _, err := h.sessionCreateRun(context.Background(), args("name", "demo")); err == nil {
		t.Fatal("expected error when home unset")
	}
}

func TestSessionCreateForum(t *testing.T) {
	h, d, sup, _, _, st := newTestHandler(t, "forum")
	st.SetHome(state.HomeRef{ID: "forum1", Type: "forum"})
	if _, err := h.sessionCreateRun(context.Background(), args("name", "topic")); err != nil {
		t.Fatal(err)
	}
	if len(d.created) != 1 || d.created[0] != "forum:topic" {
		t.Fatalf("expected forum post: %+v", d.created)
	}
	if len(sup.started) != 1 {
		t.Fatal("expected bridge started")
	}
}

func TestSessionBanner(t *testing.T) {
	const repo = "/home/me/proj"
	cases := []struct {
		name     string
		worktree string
		branch   string
		shared   bool
		want     []string
		absent   []string
	}{
		{
			name:     "isolated",
			worktree: "/home/me/proj/.dctl-sessions/demo",
			branch:   "session/demo",
			shared:   false,
			want: []string{
				"🚀 Session **demo** ready.",
				"Project: **proj** (`/home/me/proj`)",
				"Mode: isolated worktree",
				"Worktree: `/home/me/proj/.dctl-sessions/demo`",
				"Branch: `session/demo`",
				"Command: `claude`",
			},
			absent: []string{"main checkout", "not a git repo"},
		},
		{
			name:     "shared main checkout",
			worktree: "",
			branch:   "session/demo",
			shared:   true,
			want: []string{
				"🚀 Session **demo** ready.",
				"Project: **proj** (`/home/me/proj`)",
				"Mode: shared (main checkout)",
				"Branch: — (runs on current branch)",
				"Command: `claude`",
			},
			absent: []string{"Worktree:", "isolated worktree", "not a git repo"},
		},
		{
			name:     "non-git shared",
			worktree: "",
			branch:   "session/demo",
			shared:   false,
			want: []string{
				"🚀 Session **demo** ready.",
				"Project: **proj** (`/home/me/proj`)",
				"Mode: shared (not a git repo)",
				"Command: `claude`",
			},
			absent: []string{"Worktree:", "Branch:", "isolated worktree", "main checkout"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sessionBanner(repo, "demo", tc.worktree, tc.branch, "claude", tc.shared)
			for _, s := range tc.want {
				if !strings.Contains(got, s) {
					t.Errorf("banner missing %q\n--- got ---\n%s", s, got)
				}
			}
			for _, s := range tc.absent {
				if strings.Contains(got, s) {
					t.Errorf("banner unexpectedly contains %q\n--- got ---\n%s", s, got)
				}
			}
		})
	}
}

func TestSessionBannerEmptyRepo(t *testing.T) {
	got := sessionBanner("", "demo", "", "session/demo", "claude", true)
	if !strings.Contains(got, "Project: **(cwd)**") {
		t.Errorf("empty repo should render (cwd), got:\n%s", got)
	}
	if strings.Contains(got, "**.**") || strings.Contains(got, "(``)") {
		t.Errorf("empty repo must not render misleading path, got:\n%s", got)
	}
}

func TestSessionCreateBanner(t *testing.T) {
	cases := []struct {
		name      string
		homeType  string
		homeRef   state.HomeRef
		wtPath    string
		shared    bool
		wantReply []string
		wantSend  []string
	}{
		{
			name:     "category isolated",
			homeType: "",
			homeRef:  state.HomeRef{ID: "cat1", Type: "category"},
			wtPath:   "/wt/x",
			shared:   false,
			wantReply: []string{
				"✅ Running on <#new-demo>.",
				"Mode: isolated worktree",
				"Worktree: `/wt/x`",
				"Branch: `session/demo`",
				"Command: `claude`",
			},
			wantSend: []string{"Mode: isolated worktree", "Worktree: `/wt/x`"},
		},
		{
			name:     "category non-git shared",
			homeType: "",
			homeRef:  state.HomeRef{ID: "cat1", Type: "category"},
			wtPath:   "",
			shared:   false,
			wantReply: []string{
				"✅ Running on <#new-demo>.",
				"Mode: shared (not a git repo)",
			},
			wantSend: []string{"Mode: shared (not a git repo)"},
		},
		{
			name:     "category shared:true",
			homeType: "",
			homeRef:  state.HomeRef{ID: "cat1", Type: "category"},
			wtPath:   "/wt/x",
			shared:   true,
			wantReply: []string{
				"Mode: shared (main checkout)",
				"Branch: — (runs on current branch)",
			},
			wantSend: []string{"Mode: shared (main checkout)"},
		},
		{
			name:     "forum isolated",
			homeType: "forum",
			homeRef:  state.HomeRef{ID: "forum1", Type: "forum"},
			wtPath:   "/wt/x",
			shared:   false,
			wantReply: []string{
				"✅ Running on <#post-demo>.",
				"Mode: isolated worktree",
			},
			wantSend: []string{"Mode: isolated worktree"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, d, _, wt, _, st := newTestHandler(t, tc.homeType)
			wt.path = tc.wtPath
			st.SetHome(tc.homeRef)
			in := args("name", "demo")
			if tc.shared {
				in.Args["shared"] = "true"
			}
			reply, err := h.sessionCreateRun(context.Background(), in)
			if err != nil {
				t.Fatalf("create failed: %v", err)
			}
			for _, s := range tc.wantReply {
				if !strings.Contains(reply, s) {
					t.Errorf("reply missing %q\n--- got ---\n%s", s, reply)
				}
			}
			if len(d.sent) != 1 {
				t.Fatalf("expected exactly one in-channel Send, got %d: %+v", len(d.sent), d.sent)
			}
			sess, _ := st.FindSession("demo")
			if d.sent[0].channelID != sess.ChannelID {
				t.Errorf("Send went to %q, want session channel %q", d.sent[0].channelID, sess.ChannelID)
			}
			for _, s := range tc.wantSend {
				if !strings.Contains(d.sent[0].content, s) {
					t.Errorf("in-channel banner missing %q\n--- got ---\n%s", s, d.sent[0].content)
				}
			}
			if strings.Contains(d.sent[0].content, "Running on") {
				t.Errorf("in-channel banner must not carry the 'Running on' prefix:\n%s", d.sent[0].content)
			}
		})
	}
}

func TestSessionCreateSendFailureDoesNotFail(t *testing.T) {
	h, d, sup, _, _, st := newTestHandler(t, "")
	d.sendErr = errors.New("discord 500")
	st.SetHome(state.HomeRef{ID: "cat1", Type: "category"})
	reply, err := h.sessionCreateRun(context.Background(), args("name", "demo"))
	if err != nil {
		t.Fatalf("create must still succeed when Send fails: %v", err)
	}
	if !strings.Contains(reply, "✅ Running on") {
		t.Fatalf("unexpected reply: %q", reply)
	}
	if _, ok := st.FindSession("demo"); !ok {
		t.Fatal("session must remain persisted despite Send failure")
	}
	if len(sup.started) != 1 {
		t.Fatal("bridge must have started")
	}
	if len(d.sent) != 0 {
		t.Fatalf("no send should be recorded on error, got %+v", d.sent)
	}
}

func TestSessionCloseStopsAndArchives(t *testing.T) {
	h, d, sup, wt, _, st := newTestHandler(t, "")
	st.SetHome(state.HomeRef{ID: "cat1", Type: "category"})
	st.AddSession(state.Session{Name: "demo", ChannelID: "ch9", Type: "text", Worktree: "/wt/x"})
	if _, err := h.sessionCloseRun(context.Background(), args("name", "demo")); err != nil {
		t.Fatal(err)
	}
	if len(sup.stopped) != 1 || len(d.archived) != 1 || len(wt.removed) != 1 {
		t.Fatalf("expected stop+archive+wt-remove: %+v %+v %+v", sup.stopped, d.archived, wt.removed)
	}
	if _, ok := st.FindSession("demo"); ok {
		t.Fatal("session should be removed")
	}
}

func TestSessionCloseDirtyRefusedWithoutForce(t *testing.T) {
	h, d, _, wt, _, st := newTestHandler(t, "")
	wt.removeErr = errors.New(`worktree "demo" has uncommitted changes`)
	st.AddSession(state.Session{Name: "demo", ChannelID: "ch9", Type: "text", Worktree: "/wt/x"})
	if _, err := h.sessionCloseRun(context.Background(), args("name", "demo")); err == nil {
		t.Fatal("expected refusal")
	}
	if len(d.archived) != 0 {
		t.Fatal("must not archive when worktree removal refused")
	}
	if _, ok := st.FindSession("demo"); !ok {
		t.Fatal("session must survive a refused close")
	}
}

func TestSessionCloseDirtyForced(t *testing.T) {
	h, _, _, wt, _, st := newTestHandler(t, "")
	wt.removeErr = errors.New("dirty")
	st.AddSession(state.Session{Name: "demo", ChannelID: "ch9", Type: "text", Worktree: "/wt/x"})
	if _, err := h.sessionCloseRun(context.Background(), args("name", "demo", "force", "true")); err != nil {
		t.Fatal(err)
	}
	if len(wt.removed) != 1 {
		t.Fatalf("force should remove worktree: %+v", wt.removed)
	}
	if _, ok := st.FindSession("demo"); ok {
		t.Fatal("session should be removed after forced close")
	}
}

func TestSessionCreateUsesQualifiedTitle(t *testing.T) {
	tests := []struct {
		name       string
		instanceID string
		homeType   string
		setHome    state.HomeRef
		logical    string
		wantTitle  string
	}{
		{
			name:       "category-namespaced",
			instanceID: "alice",
			homeType:   "",
			setHome:    state.HomeRef{ID: "cat1", Type: "category"},
			logical:    "foo",
			wantTitle:  "alice__foo",
		},
		{
			name:       "forum-namespaced",
			instanceID: "bob",
			homeType:   "forum",
			setHome:    state.HomeRef{ID: "f1", Type: "forum"},
			logical:    "foo",
			wantTitle:  "forum:bob__foo",
		},
		{
			name:       "category-legacy",
			instanceID: "",
			homeType:   "",
			setHome:    state.HomeRef{ID: "cat1", Type: "category"},
			logical:    "foo",
			wantTitle:  "foo",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, d, _, _, _, st := newTestHandler(t, tt.homeType)
			st.InstanceID = tt.instanceID
			st.SetHome(tt.setHome)
			if _, err := h.sessionCreateRun(context.Background(), args("name", tt.logical)); err != nil {
				t.Fatal(err)
			}
			if len(d.created) != 1 || d.created[0] != tt.wantTitle {
				t.Fatalf("created titles = %+v, want [%q]", d.created, tt.wantTitle)
			}
			if _, ok := st.FindSession(tt.logical); !ok {
				t.Fatalf("session must be keyed by logical name %q", tt.logical)
			}
		})
	}
}

func TestSessionCloseOnlyTouchesOwnSession(t *testing.T) {
	h, d, sup, wt, _, st := newTestHandler(t, "")
	st.InstanceID = "bob"
	st.SetHome(state.HomeRef{ID: "cat1", Type: "category"})
	st.AddSession(state.Session{Name: "foo", ChannelID: "bob-foo-ch", Type: "text", Worktree: "/wt/bob"})

	if _, err := h.sessionCloseRun(context.Background(), args("name", "foo")); err != nil {
		t.Fatal(err)
	}

	if len(d.archived) != 1 || d.archived[0] != "bob-foo-ch" {
		t.Fatalf("close must archive only bob's channel, got %+v", d.archived)
	}
	if len(sup.stopped) != 1 || sup.stopped[0] != "foo" {
		t.Fatalf("close must stop only the local logical session, got %+v", sup.stopped)
	}
	if len(wt.removed) != 1 {
		t.Fatalf("close must remove exactly bob's worktree, got %+v", wt.removed)
	}
	if _, ok := st.FindSession("foo"); ok {
		t.Fatal("bob's foo should be gone after close")
	}
}

func TestSessionCloseUnknownNameIsNoop(t *testing.T) {
	h, d, sup, _, _, st := newTestHandler(t, "")
	st.InstanceID = "bob"
	if _, err := h.sessionCloseRun(context.Background(), args("name", "alice-only")); err == nil {
		t.Fatal("expected error for unknown session")
	}
	if len(d.archived) != 0 || len(sup.stopped) != 0 {
		t.Fatalf("unknown close must be a no-op, got archived=%+v stopped=%+v", d.archived, sup.stopped)
	}
}

func TestSessionCreateUsesWorkspaceProject(t *testing.T) {
	h, _, _, wt, _, st := newTestHandler(t, "")
	st.SetHome(state.HomeRef{ID: "cat1", Type: "category"})
	_ = st.SetWorkspace("/ws")
	if _, err := h.sessionCreateRun(context.Background(), args("name", "demo", "project", "myproj")); err != nil {
		t.Fatal(err)
	}
	if len(wt.createdRepos) != 1 || wt.createdRepos[0] != "/ws/myproj" {
		t.Fatalf("expected Create on /ws/myproj, got %+v", wt.createdRepos)
	}
	sess, _ := st.FindSession("demo")
	if sess.Project != "myproj" {
		t.Fatalf("session.Project not persisted: %+v", sess)
	}
}

func TestSessionCreateRequiresProjectWhenWorkspaceSet(t *testing.T) {
	h, d, _, wt, _, st := newTestHandler(t, "")
	st.SetHome(state.HomeRef{ID: "cat1", Type: "category"})
	_ = st.SetWorkspace("/ws")
	if _, err := h.sessionCreateRun(context.Background(), args("name", "demo")); err == nil {
		t.Fatal("expected error asking for project")
	}
	if len(wt.created) != 0 || len(d.created) != 0 {
		t.Fatalf("nothing should be created: wt=%v ch=%v", wt.created, d.created)
	}
}

func TestSessionCreateRejectsProjectTraversal(t *testing.T) {
	for _, p := range []string{"../escape", "a/b", "..", "with space"} {
		h, d, _, wt, _, st := newTestHandler(t, "")
		st.SetHome(state.HomeRef{ID: "cat1", Type: "category"})
		_ = st.SetWorkspace("/ws")
		if _, err := h.sessionCreateRun(context.Background(), args("name", "demo", "project", p)); err == nil {
			t.Fatalf("project %q: expected rejection", p)
		}
		if len(wt.created) != 0 || len(d.created) != 0 {
			t.Fatalf("project %q: nothing should be created", p)
		}
	}
}

func TestSessionCreateLegacyNoWorkspace(t *testing.T) {
	// No workspace set → legacy behaviour: repo is "" (WorkspaceRoot), still works.
	h, d, _, wt, _, st := newTestHandler(t, "")
	st.SetHome(state.HomeRef{ID: "cat1", Type: "category"})
	if _, err := h.sessionCreateRun(context.Background(), args("name", "demo")); err != nil {
		t.Fatal(err)
	}
	if len(d.created) != 1 || len(wt.created) != 1 {
		t.Fatalf("legacy create should still work: ch=%v wt=%v", d.created, wt.created)
	}
}

func TestSessionCloseUsesProjectRepo(t *testing.T) {
	h, _, _, wt, _, st := newTestHandler(t, "")
	_ = st.SetWorkspace("/ws")
	st.AddSession(state.Session{Name: "demo", ChannelID: "ch9", Type: "text", Worktree: "/ws/myproj/.dctl-sessions/demo", Project: "myproj"})
	if _, err := h.sessionCloseRun(context.Background(), args("name", "demo")); err != nil {
		t.Fatal(err)
	}
	if len(wt.removed) != 1 {
		t.Fatalf("expected worktree removed: %+v", wt.removed)
	}
}

func TestSessionCreateClonesThenUsesProject(t *testing.T) {
	h, _, _, wt, fg, st := newTestHandler(t, "")
	st.SetHome(state.HomeRef{ID: "cat1", Type: "category"})
	_ = st.SetWorkspace("/ws")
	fg.cloneDir = "/ws/app"
	if _, err := h.sessionCreateRun(context.Background(), args("name", "demo", "clone", "me/app")); err != nil {
		t.Fatal(err)
	}
	if len(fg.cloned) != 1 || fg.cloned[0] != "me/app" {
		t.Fatalf("expected clone of me/app, got %+v", fg.cloned)
	}
	if len(wt.createdRepos) != 1 || wt.createdRepos[0] != "/ws/app" {
		t.Fatalf("expected Create on /ws/app, got %+v", wt.createdRepos)
	}
	sess, _ := st.FindSession("demo")
	if sess.Project != "app" {
		t.Fatalf("project should be derived from clone: %+v", sess)
	}
}

func TestSessionCreateCloneErrorSurfaces(t *testing.T) {
	h, d, _, wt, fg, st := newTestHandler(t, "")
	st.SetHome(state.HomeRef{ID: "cat1", Type: "category"})
	_ = st.SetWorkspace("/ws")
	fg.cloneErr = errors.New("auth required")
	if _, err := h.sessionCreateRun(context.Background(), args("name", "demo", "clone", "me/app")); err == nil {
		t.Fatal("expected clone error")
	}
	if len(wt.created) != 0 || len(d.created) != 0 {
		t.Fatalf("nothing should be created after clone failure")
	}
}

func TestSessionListEmptyAndPopulated(t *testing.T) {
	h, _, _, _, _, st := newTestHandler(t, "")
	if out, err := h.sessionListRun(context.Background(), args()); err != nil || !strings.Contains(out, "No active sessions") {
		t.Fatalf("empty list: out=%q err=%v", out, err)
	}
	st.AddSession(state.Session{Name: "demo", ChannelID: "c1", Type: "text"})
	out, err := h.sessionListRun(context.Background(), args())
	if err != nil || !strings.Contains(out, "demo") {
		t.Fatalf("populated list should mention demo: out=%q err=%v", out, err)
	}
}

func TestSessionWhoListsParticipants(t *testing.T) {
	h, _, _, _, _, st := newTestHandler(t, "")
	st.AddSession(state.Session{Name: "demo", ChannelID: "c1", Type: "text"})
	jp := state.ParticipantsPath(h.PartDir(), "demo")
	state.AppendParticipant(jp, "h1")
	state.AppendParticipant(jp, "h2")

	out, err := h.sessionWhoRun(context.Background(), args("name", "demo"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "h1") || !strings.Contains(out, "h2") {
		t.Fatalf("who should list both participants: %q", out)
	}
}

func TestSessionWhoEmpty(t *testing.T) {
	h, _, _, _, _, st := newTestHandler(t, "")
	st.AddSession(state.Session{Name: "demo", ChannelID: "c1", Type: "text"})
	out, err := h.sessionWhoRun(context.Background(), args("name", "demo"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Personne") {
		t.Fatalf("empty who should say nobody wrote yet: %q", out)
	}
}

func TestSessionClosePurgesParticipants(t *testing.T) {
	h, _, _, _, _, st := newTestHandler(t, "")
	st.SetHome(state.HomeRef{ID: "cat1", Type: "category"})
	st.AddSession(state.Session{Name: "demo", ChannelID: "ch9", Type: "text", Worktree: "/wt/x"})
	jp := state.ParticipantsPath(h.PartDir(), "demo")
	state.AppendParticipant(jp, "h1")
	if _, err := h.sessionCloseRun(context.Background(), args("name", "demo")); err != nil {
		t.Fatal(err)
	}
	if got := state.ReadParticipants(jp); len(got) != 0 {
		t.Fatalf("close must purge the participants journal, got %+v", got)
	}
}

func TestServiceRestart(t *testing.T) {
	h, up, _, _, _, _, _ := newTestHandlerWithUpdater(t, "")
	out, err := h.serviceRestartRun(context.Background(), args())
	if err != nil {
		t.Fatal(err)
	}
	if up.restarts != 1 {
		t.Fatalf("expected 1 restart, got %d", up.restarts)
	}
	if len(up.builds) != 0 {
		t.Fatalf("restart must not build, got %+v", up.builds)
	}
	if !strings.Contains(out, "Restarting") {
		t.Fatalf("unexpected reply: %q", out)
	}
}

func TestServiceUpdatePullsBuildsRestarts(t *testing.T) {
	h, up, _, _, _, _, _ := newTestHandlerWithUpdater(t, "")
	out, err := h.serviceUpdateRun(context.Background(), args())
	if err != nil {
		t.Fatal(err)
	}
	if len(up.builds) != 1 || up.builds[0] != true {
		t.Fatalf("expected one build with pull=true, got %+v", up.builds)
	}
	if up.restarts != 1 {
		t.Fatalf("expected restart after build, got %d", up.restarts)
	}
	if !strings.Contains(out, "abc1234") {
		t.Fatalf("reply should mention version: %q", out)
	}
}

func TestServiceUpdateNoPull(t *testing.T) {
	h, up, _, _, _, _, _ := newTestHandlerWithUpdater(t, "")
	if _, err := h.serviceUpdateRun(context.Background(), args("no_pull", "true")); err != nil {
		t.Fatal(err)
	}
	if len(up.builds) != 1 || up.builds[0] != false {
		t.Fatalf("expected build with pull=false, got %+v", up.builds)
	}
}

func TestServiceUpdateBuildFailsNoRestart(t *testing.T) {
	h, up, _, _, _, _, _ := newTestHandlerWithUpdater(t, "")
	up.buildErr = errors.New("compile boom")
	if _, err := h.serviceUpdateRun(context.Background(), args()); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected build error surfaced, got %v", err)
	}
	if up.restarts != 0 {
		t.Fatalf("must not restart when build fails")
	}
}

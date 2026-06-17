package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// HomeRef points at the category or forum that holds session channels.
type HomeRef struct {
	ID   string `json:"id"`
	Type string `json:"type"` // "category" | "forum"
}

// Session is one bridged channel/post supervised by the daemon.
type Session struct {
	ID        string `json:"id,omitempty"` // stable logical id, decoupled from Name and ChannelID
	Name      string `json:"name"`
	ChannelID string `json:"channelID"`
	Type      string `json:"type"` // "text" | "forum"
	Cmd       string `json:"cmd"`
	Backend   string `json:"backend,omitempty"`  // bridge backend ("" or "stream" = stream-json default; "oneshot" = per-message cmd)
	Worktree  string `json:"worktree,omitempty"` // abs path; empty for a shared session
	Project   string `json:"project,omitempty"`  // workspace sub-dir the session started from

	// Gateways binds the session to a set of gateway kinds (e.g. "discord",
	// "terminal"). Empty means "legacy": a session with a ChannelID is Discord.
	Gateways []string `json:"gateways,omitempty"`

	Participants []string `json:"participants,omitempty"` // observed authors (cache; journal is source of truth)
}

// BoundGateways returns the gateway kinds this session is bound to. When the
// stored set is empty it falls back to the legacy rule: a session with a
// ChannelID is a Discord session; one without is bound to nothing.
func (s Session) BoundGateways() []string {
	if len(s.Gateways) > 0 {
		return append([]string(nil), s.Gateways...)
	}
	if s.ChannelID != "" {
		return []string{"discord"}
	}
	return nil
}

// State is the daemon's persisted configuration. All access is mutex-guarded.
type State struct {
	mu              sync.Mutex `json:"-"`
	path            string     `json:"-"`
	Home            HomeRef    `json:"home"`
	Repo            string     `json:"repo,omitempty"`      // legacy single-repo root; defaults to daemon cwd
	Workspace       string     `json:"workspace,omitempty"` // abs path to the workspace root; preferred over Repo
	Source          string     `json:"source,omitempty"`    // abs path to the dctl source checkout (for /service update)
	Sessions        []Session  `json:"sessions"`
	StatusMessageID string     `json:"statusMessageID,omitempty"` // cached id of the status embed
	InstanceID      string     `json:"instanceID,omitempty"`      // per-daemon namespace for global resources; "" = legacy
}

// NewState returns an empty state bound to path (not yet written).
func NewState(path string) *State { return &State{path: path} }

// ApplyDefaults seeds declarative config.json values into the in-memory state
// for any field not already set, WITHOUT persisting. This keeps config.json the
// source of truth for unset fields while a live /set (which persists to
// state.json) always wins: persisted state > config > empty. Because it never
// writes, removing a value from config.json takes effect on the next restart.
func (s *State) ApplyDefaults(home *HomeRef, workspace, source string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Home.ID == "" && home != nil && home.ID != "" {
		s.Home = *home
	}
	if s.Workspace == "" {
		s.Workspace = workspace
	}
	if s.Source == "" {
		s.Source = source
	}
}

// LoadState reads state from path; a missing file yields an empty state.
func LoadState(path string) (*State, error) {
	s := NewState(path)
	buf, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(buf, s); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for i := range s.Sessions {
		if s.Sessions[i].ID != "" {
			seen[s.Sessions[i].ID] = true
		}
	}
	for i := range s.Sessions {
		if s.Sessions[i].ID == "" {
			s.Sessions[i].ID = newSessionID(s.Sessions[i].Name, seen)
			seen[s.Sessions[i].ID] = true
		}
	}
	return s, nil
}

// newSessionID returns a stable id for a session. Name is already unique and
// git-safe, so it seeds a readable id; a numeric disambiguator keeps ids stable
// if a name is later reused.
func newSessionID(name string, existing map[string]bool) string {
	base := "s_" + name
	id := base
	for n := 1; existing[id]; n++ {
		id = fmt.Sprintf("%s_%d", base, n)
	}
	return id
}

// Save atomically writes state to its path.
func (s *State) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

func (s *State) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	buf, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, buf, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// FindSession returns the session with name (and whether it exists).
func (s *State) FindSession(name string) (Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ss := range s.Sessions {
		if ss.Name == name {
			return ss, true
		}
	}
	return Session{}, false
}

// AddSession adds a session, erroring if the name is taken, and persists.
func (s *State) AddSession(sess Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ss := range s.Sessions {
		if ss.Name == sess.Name {
			return fmt.Errorf("session %q already exists", sess.Name)
		}
	}
	if sess.ID == "" {
		seen := map[string]bool{}
		for _, e := range s.Sessions {
			seen[e.ID] = true
		}
		sess.ID = newSessionID(sess.Name, seen)
	}
	s.Sessions = append(s.Sessions, sess)
	return s.saveLocked()
}

// RemoveSession drops the session named name and persists.
func (s *State) RemoveSession(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.Sessions[:0]
	for _, ss := range s.Sessions {
		if ss.Name != name {
			out = append(out, ss)
		}
	}
	s.Sessions = out
	return s.saveLocked()
}

// SetHome records the home ref and persists.
func (s *State) SetHome(h HomeRef) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Home = h
	return s.saveLocked()
}

// SetWorkspace records the workspace root and persists.
func (s *State) SetWorkspace(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Workspace = path
	return s.saveLocked()
}

// WorkspaceRoot returns the configured workspace, else the legacy Repo, else "".
func (s *State) WorkspaceRoot() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Workspace != "" {
		return s.Workspace
	}
	return s.Repo
}

// SetSource records the dctl source-checkout path and persists.
func (s *State) SetSource(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Source = path
	return s.saveLocked()
}

// SourceDir returns the configured dctl source checkout, else "".
func (s *State) SourceDir() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Source
}

// SetStatusMessageID caches the status embed's message id and persists.
func (s *State) SetStatusMessageID(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.StatusMessageID = id
	return s.saveLocked()
}

// SnapshotSessions returns a copy of the current sessions.
func (s *State) SnapshotSessions() []Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Session(nil), s.Sessions...)
}

// QualifiedName maps a logical session name to the name used on global resources
// (Discord title): "<InstanceID>__<name>". In legacy mode (empty InstanceID) it
// returns the bare logical name, preserving pre-namespacing behavior.
func (s *State) QualifiedName(name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.InstanceID == "" {
		return name
	}
	return s.InstanceID + "__" + name
}

// SetInstanceID records the per-daemon instance id and persists. The id is meant
// to be frozen after first resolution; callers enforce that invariant.
func (s *State) SetInstanceID(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.InstanceID = id
	return s.saveLocked()
}

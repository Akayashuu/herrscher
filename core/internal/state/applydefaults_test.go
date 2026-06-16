package state

import (
	"path/filepath"
	"testing"
)

func TestApplyDefaultsSeedsEmptyFields(t *testing.T) {
	s := NewState(filepath.Join(t.TempDir(), "state.json"))
	s.ApplyDefaults(&HomeRef{ID: "h1", Type: "category"}, "/ws", "/src")
	if s.Home.ID != "h1" || s.Home.Type != "category" {
		t.Errorf("home = %+v", s.Home)
	}
	if s.Workspace != "/ws" {
		t.Errorf("workspace = %q", s.Workspace)
	}
	if s.Source != "/src" {
		t.Errorf("source = %q", s.Source)
	}
}

func TestApplyDefaultsDoesNotOverrideSetFields(t *testing.T) {
	s := NewState(filepath.Join(t.TempDir(), "state.json"))
	s.Home = HomeRef{ID: "live", Type: "forum"}
	s.Workspace = "/live/ws"
	s.Source = "/live/src"
	s.ApplyDefaults(&HomeRef{ID: "cfg"}, "/cfg/ws", "/cfg/src")
	if s.Home.ID != "live" || s.Workspace != "/live/ws" || s.Source != "/live/src" {
		t.Fatalf("config wrongly overrode live state: %+v ws=%q src=%q", s.Home, s.Workspace, s.Source)
	}
}

func TestApplyDefaultsDoesNotPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := NewState(path)
	s.ApplyDefaults(&HomeRef{ID: "h1"}, "/ws", "/src")
	// No file should have been written — config defaults live in-memory only so
	// that removing a value from config.json takes effect on the next restart.
	reloaded, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Home.ID != "" || reloaded.Workspace != "" || reloaded.Source != "" {
		t.Fatalf("ApplyDefaults must not persist; reloaded = %+v ws=%q src=%q",
			reloaded.Home, reloaded.Workspace, reloaded.Source)
	}
}

func TestApplyDefaultsIgnoresEmptyHome(t *testing.T) {
	s := NewState(filepath.Join(t.TempDir(), "state.json"))
	s.ApplyDefaults(nil, "", "")
	if s.Home.ID != "" {
		t.Errorf("nil home should leave Home empty, got %+v", s.Home)
	}
	s.ApplyDefaults(&HomeRef{ID: ""}, "", "")
	if s.Home.ID != "" {
		t.Errorf("empty-id home should be ignored, got %+v", s.Home)
	}
}

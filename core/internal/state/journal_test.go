package state

import (
	"path/filepath"
	"testing"
)

func TestParticipantJournalAppendAndRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "participants", "a.log")
	added, err := AppendParticipant(path, "u1")
	if err != nil || !added {
		t.Fatalf("first append should report new: added=%v err=%v", added, err)
	}
	if again, _ := AppendParticipant(path, "u1"); again {
		t.Fatal("duplicate append should report false (idempotent)")
	}
	AppendParticipant(path, "u2")
	got := ReadParticipants(path)
	if len(got) != 2 || got[0] != "u1" || got[1] != "u2" {
		t.Fatalf("expected [u1 u2] in order, got %+v", got)
	}
}

func TestReadParticipantsMissingFileIsEmpty(t *testing.T) {
	if got := ReadParticipants(filepath.Join(t.TempDir(), "nope.log")); len(got) != 0 {
		t.Fatalf("missing journal should read empty, got %+v", got)
	}
}

func TestRemoveParticipantJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p", "a.log")
	AppendParticipant(path, "u1")
	if err := RemoveParticipantJournal(path); err != nil {
		t.Fatal(err)
	}
	if got := ReadParticipants(path); len(got) != 0 {
		t.Fatalf("journal should be gone, got %+v", got)
	}
	if err := RemoveParticipantJournal(path); err != nil {
		t.Fatalf("removing missing journal must be a no-op, got %v", err)
	}
}

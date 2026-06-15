package state

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// AppendParticipant records userID in the append-only journal at path (one id
// per line). It is idempotent: if userID is already present, the file is left
// untouched and added is false. A missing parent directory is created.
//
// Append uses O_APPEND so concurrent appenders (the bridge child) never race
// with the daemon's reads; the daemon only reads this file, never writes it.
func AppendParticipant(path, userID string) (added bool, err error) {
	if path == "" || userID == "" {
		return false, nil
	}
	for _, id := range ReadParticipants(path) {
		if id == userID {
			return false, nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return false, err
	}
	defer f.Close()
	if _, err := f.WriteString(userID + "\n"); err != nil {
		return false, err
	}
	return true, nil
}

// ReadParticipants returns the de-duplicated user ids in the journal at path,
// in first-seen order. A missing file yields an empty slice (no error: the
// journal is best-effort observability).
func ReadParticipants(path string) []string {
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	seen := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		id := strings.TrimSpace(sc.Text())
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// RemoveParticipantJournal deletes the journal at path. A missing file is not
// an error (called on session close to avoid leaking participants/*.log).
func RemoveParticipantJournal(path string) error {
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ParticipantsPath returns the journal path for session name under dir
// (dir/participants/<name>.log). Both the supervisor (which tells the bridge
// where to append) and the handler (which reads it) call this so they agree.
func ParticipantsPath(dir, name string) string {
	return filepath.Join(dir, "participants", name+".log")
}

package control

import (
	"bytes"
	"strings"
	"testing"
)

func TestWriteThenScanRoundTrip(t *testing.T) {
	want := []Event{
		{T: "human", Who: "alice", Text: "refactor the env loader"},
		{T: "status", Text: "reading envfile.go"},
		{T: "chunk", Text: "proposing 3 changes"},
		{T: "reply", Text: "done", Done: true},
		{T: "pick", Value: "2"},
	}
	var buf bytes.Buffer
	for _, e := range want {
		if err := WriteEvent(&buf, e); err != nil {
			t.Fatalf("WriteEvent(%v): %v", e, err)
		}
	}
	var got []Event
	if err := ScanEvents(&buf, func(e Event) error { got = append(got, e); return nil }); err != nil {
		t.Fatalf("ScanEvents: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestWriteEventOneLinePerEvent(t *testing.T) {
	var buf bytes.Buffer
	_ = WriteEvent(&buf, Event{T: "chunk", Text: "a\nb"}) // text with an embedded newline
	if n := strings.Count(buf.String(), "\n"); n != 1 {
		t.Fatalf("encoded form has %d newlines, want exactly 1 (text newline must be escaped)", n)
	}
}

func TestScanLegacyBareValueBecomesPick(t *testing.T) {
	r := strings.NewReader("2\n")
	var got []Event
	if err := ScanEvents(r, func(e Event) error { got = append(got, e); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].T != "pick" || got[0].Value != "2" {
		t.Fatalf("legacy bare value decoded as %+v, want a single pick with value 2", got)
	}
}

func TestScanSkipsBlankLines(t *testing.T) {
	r := strings.NewReader("\n  \n{\"t\":\"pick\",\"value\":\"1\"}\n\n")
	count := 0
	if err := ScanEvents(r, func(Event) error { count++; return nil }); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("decoded %d events, want 1 (blank lines skipped)", count)
	}
}

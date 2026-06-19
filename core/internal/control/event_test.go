package control

import (
	"bytes"
	"strings"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

func TestWriteThenScanRoundTrip(t *testing.T) {
	want := []contracts.Event{
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
	var got []contracts.Event
	if err := ScanEvents(&buf, func(e contracts.Event) error { got = append(got, e); return nil }); err != nil {
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
	_ = WriteEvent(&buf, contracts.Event{T: "chunk", Text: "a\nb"}) // text with an embedded newline
	if n := strings.Count(buf.String(), "\n"); n != 1 {
		t.Fatalf("encoded form has %d newlines, want exactly 1 (text newline must be escaped)", n)
	}
}

func TestScanLegacyBareValueBecomesPick(t *testing.T) {
	r := strings.NewReader("2\n")
	var got []contracts.Event
	if err := ScanEvents(r, func(e contracts.Event) error { got = append(got, e); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].T != "pick" || got[0].Value != "2" {
		t.Fatalf("legacy bare value decoded as %+v, want a single pick with value 2", got)
	}
}

func TestScanRejectsMalformedJSONLine(t *testing.T) {
	r := strings.NewReader(`{"t":"chunk","text":` + "\n")
	called := false
	err := ScanEvents(r, func(contracts.Event) error { called = true; return nil })
	if err == nil {
		t.Fatal("ScanEvents returned nil for a malformed JSON line, want an error")
	}
	if called {
		t.Fatal("fn was called for a malformed JSON line, want it skipped as a protocol error")
	}
}

func TestScanSkipsBlankLines(t *testing.T) {
	r := strings.NewReader("\n  \n{\"t\":\"pick\",\"value\":\"1\"}\n\n")
	count := 0
	if err := ScanEvents(r, func(contracts.Event) error { count++; return nil }); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("decoded %d events, want 1 (blank lines skipped)", count)
	}
}

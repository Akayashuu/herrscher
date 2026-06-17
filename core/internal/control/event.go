package control

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
)

// Event is one message on the session bus. The bridge emits turn events
// (human/status/chunk/reply) for a consumer to render; a consumer injects
// input/pick down to the bridge. One Event encodes to exactly one JSON line.
//
//	{"t":"human","who":"alice","text":"refactor the env loader"}
//	{"t":"chunk","text":"reading envfile.go"}
//	{"t":"status","text":"proposing 3 changes"}
//	{"t":"reply","text":"done","done":true}
//	{"t":"input","who":"terminal","text":"apply them"}
//	{"t":"pick","value":"2"}
type Event struct {
	T     string `json:"t"`
	Who   string `json:"who,omitempty"`
	Text  string `json:"text,omitempty"`
	Value string `json:"value,omitempty"`
	Done  bool   `json:"done,omitempty"`
}

// WriteEvent encodes e as a single JSON line (newline-terminated). encoding/json
// escapes any newline inside a field, so one Event is always one line.
func WriteEvent(w io.Writer, e Event) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = w.Write(b)
	return err
}

// ScanEvents reads JSON-line events from r, calling fn for each. A blank line is
// skipped; a non-JSON line is treated as the legacy bare pick value (one digit
// per connection) and surfaced as Event{T:"pick", Value:line} for back-compat
// with the pre-bus daemon. It returns the first error from fn or a read error
// other than io.EOF.
func ScanEvents(r io.Reader, fn func(Event) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e Event
		if line[0] == '{' && json.Unmarshal([]byte(line), &e) == nil {
			if err := fn(e); err != nil {
				return err
			}
			continue
		}
		if err := fn(Event{T: "pick", Value: line}); err != nil {
			return err
		}
	}
	return sc.Err()
}

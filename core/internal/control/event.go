package control

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

// WriteEvent encodes e as a single JSON line (newline-terminated). encoding/json
// escapes any newline inside a field, so one Event is always one line.
func WriteEvent(w io.Writer, e contracts.Event) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = w.Write(b)
	return err
}

// ScanEvents reads JSON-line events from r, calling fn for each. A blank line is
// skipped; a line starting with '{' MUST parse as a JSON Event or it is a
// protocol error (returned, not surfaced as a pick); a non-'{' line is treated
// as the legacy bare pick value and surfaced as Event{T:"pick", Value:line} for
// back-compat. It returns the first fn error, a malformed-line error, or a read
// error other than io.EOF.
func ScanEvents(r io.Reader, fn func(contracts.Event) error) error {
	sc := bufio.NewScanner(r)
	// 1 MiB line cap: a longer line ends the scan with bufio.ErrTooLong. This is
	// a deliberate protocol limit.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if line[0] == '{' {
			var e contracts.Event
			if err := json.Unmarshal([]byte(line), &e); err != nil {
				return fmt.Errorf("control: malformed event line: %w", err)
			}
			if err := fn(e); err != nil {
				return err
			}
			continue
		}
		if err := fn(contracts.Event{T: "pick", Value: line}); err != nil {
			return err
		}
	}
	return sc.Err()
}

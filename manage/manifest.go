package manage

import (
	"fmt"
	"strings"
)

// The host's plugins.go carries blank imports between these markers. The CLI
// edits only that managed region, never the rest of the file.
const (
	beginMarker = "// herrscher:plugins"
	endMarker   = "// herrscher:end"
)

// importLine renders the blank import for a module inside the managed region.
func importLine(module string) string { return fmt.Sprintf("\t_ %q", module) }

// parseModule extracts the module path from a blank import line, reporting
// whether the line is a blank import at all.
func parseModule(line string) (string, bool) {
	s := strings.TrimSpace(line)
	if !strings.HasPrefix(s, "_ ") {
		return "", false
	}
	return strings.Trim(strings.TrimSpace(s[2:]), `"`), true
}

// listPlugins returns the module paths declared in the managed region, in order.
func listPlugins(src string) ([]string, error) {
	region, _, _, err := region(src)
	if err != nil {
		return nil, err
	}
	var mods []string
	for _, line := range region {
		if m, ok := parseModule(line); ok {
			mods = append(mods, m)
		}
	}
	return mods, nil
}

// addPlugin inserts a blank import for module before the end marker. It reports
// whether the source changed (false if the module was already present).
func addPlugin(src, module string) (string, bool, error) {
	mods, err := listPlugins(src)
	if err != nil {
		return src, false, err
	}
	for _, m := range mods {
		if m == module {
			return src, false, nil
		}
	}
	lines := strings.Split(src, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == endMarker {
			out := append([]string{}, lines[:i]...)
			out = append(out, importLine(module))
			out = append(out, lines[i:]...)
			return strings.Join(out, "\n"), true, nil
		}
	}
	return src, false, fmt.Errorf("end marker %q not found", endMarker)
}

// removePlugin drops the blank import for module from the managed region. It
// reports whether the source changed (false if the module was absent).
func removePlugin(src, module string) (string, bool, error) {
	if _, _, _, err := region(src); err != nil {
		return src, false, err
	}
	lines := strings.Split(src, "\n")
	var out []string
	changed := false
	inRegion := false
	for _, line := range lines {
		switch strings.TrimSpace(line) {
		case beginMarker:
			inRegion = true
		case endMarker:
			inRegion = false
		}
		if inRegion {
			if m, ok := parseModule(line); ok && m == module {
				changed = true
				continue
			}
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n"), changed, nil
}

// region returns the lines strictly between the markers, plus their indices.
func region(src string) (lines []string, begin, end int, err error) {
	all := strings.Split(src, "\n")
	begin, end = -1, -1
	for i, line := range all {
		switch strings.TrimSpace(line) {
		case beginMarker:
			begin = i
		case endMarker:
			end = i
		}
	}
	if begin < 0 || end < 0 {
		return nil, begin, end, fmt.Errorf("managed region markers not found")
	}
	if end < begin {
		return nil, begin, end, fmt.Errorf("end marker precedes begin marker")
	}
	return all[begin+1 : end], begin, end, nil
}

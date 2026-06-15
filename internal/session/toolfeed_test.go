package session

import "testing"

func TestParseToolEvents(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []toolEvent
	}{
		{
			name: "single tool with args",
			in:   "I'll run the tests.\n⏺ Bash(npm test)\n  ⎿ 12 passed",
			want: []toolEvent{{Tool: "Bash", Detail: "npm test"}},
		},
		{
			name: "multiple tools in order",
			in:   "⏺ Read(src/app.ts)\n  ⎿ 40 lines\n⏺ Edit(src/app.ts)",
			want: []toolEvent{{Tool: "Read", Detail: "src/app.ts"}, {Tool: "Edit", Detail: "src/app.ts"}},
		},
		{
			name: "filled-circle variant",
			in:   "● Grep(pattern)",
			want: []toolEvent{{Tool: "Grep", Detail: "pattern"}},
		},
		{
			name: "prose only, no tools",
			in:   "2 + 2 = 4.\nNothing to run here.",
			want: nil,
		},
		{
			name: "tool with no args",
			in:   "⏺ TodoWrite",
			want: []toolEvent{{Tool: "TodoWrite", Detail: ""}},
		},
		{
			name: "bare bullet, no name",
			in:   "⏺",
			want: nil,
		},
		{
			name: "indented tool line",
			in:   "  ⏺ Bash(ls)",
			want: []toolEvent{{Tool: "Bash", Detail: "ls"}},
		},
		{
			name: "trailing timing suffix excluded from detail",
			in:   "⏺ Bash(npm test) (2.3s)",
			want: []toolEvent{{Tool: "Bash", Detail: "npm test"}},
		},
		{
			name: "prose starting with bullet glyph is not a tool",
			in:   "● note: this is just prose, not a tool call",
			want: nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseToolEvents(c.in)
			if len(got) != len(c.want) {
				t.Fatalf("got %d events %v, want %d %v", len(got), got, len(c.want), c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("event %d: got %+v, want %+v", i, got[i], c.want[i])
				}
			}
		})
	}
}

package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseChoicePromptFromFixture(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "claude_choice.txt"))
	if err != nil {
		t.Fatal(err)
	}
	p, ok := parseChoicePrompt(string(b))
	if !ok {
		t.Fatal("expected a choice prompt to be detected")
	}
	if p.Question != "Do you want to proceed?" {
		t.Fatalf("question = %q, want %q", p.Question, "Do you want to proceed?")
	}
	want := []choiceOption{
		{Key: "1", Label: "Yes"},
		{Key: "2", Label: "Yes, and don't ask again for rm commands"},
		{Key: "3", Label: "No, and tell Claude what to do differently (esc)"},
	}
	if len(p.Options) != len(want) {
		t.Fatalf("got %d options, want %d: %+v", len(p.Options), len(want), p.Options)
	}
	for i, w := range want {
		if p.Options[i] != w {
			t.Errorf("option %d = %+v, want %+v", i, p.Options[i], w)
		}
	}
}

// Plan-mode / "Ready to code?" prompts survive --dangerously-skip-permissions
// (they are not tool-permission gates), so they are the realistic live trigger
// for a select menu. They render in the same boxed, glyph-marked numbered form
// and must parse just like a permission prompt.
func TestParseChoicePromptPlanMode(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "claude_plan_choice.txt"))
	if err != nil {
		t.Fatal(err)
	}
	p, ok := parseChoicePrompt(string(b))
	if !ok {
		t.Fatal("expected the plan-mode prompt to be detected")
	}
	if p.Question != "Ready to code?" {
		t.Fatalf("question = %q, want %q", p.Question, "Ready to code?")
	}
	want := []choiceOption{
		{Key: "1", Label: "Yes, and auto-accept edits"},
		{Key: "2", Label: "Yes, and manually approve edits"},
		{Key: "3", Label: "No, keep planning"},
	}
	if len(p.Options) != len(want) {
		t.Fatalf("got %d options, want %d: %+v", len(p.Options), len(want), p.Options)
	}
	for i, w := range want {
		if p.Options[i] != w {
			t.Errorf("option %d = %+v, want %+v", i, p.Options[i], w)
		}
	}
}

// A numbered list in ordinary prose has no selector glyph, so it must NOT be
// mistaken for an interactive prompt.
func TestParseChoicePromptIgnoresProseList(t *testing.T) {
	prose := "Here are the steps:\n1. Clone the repo\n2. Run make\n3. Profit\n"
	if _, ok := parseChoicePrompt(prose); ok {
		t.Fatal("a plain numbered list must not parse as a choice prompt")
	}
}

func TestParseChoicePromptRequiresTwoOptions(t *testing.T) {
	one := "Proceed?\n❯ 1. Yes\n"
	if _, ok := parseChoicePrompt(one); ok {
		t.Fatal("a single option must not parse as a choice prompt")
	}
}

// Options must form a run starting at 1; a stray "2." with no "1." is not a prompt.
func TestParseChoicePromptRequiresRunFromOne(t *testing.T) {
	stray := "❯ 2. second\n   3. third\n"
	if _, ok := parseChoicePrompt(stray); ok {
		t.Fatal("options not starting at 1 must not parse")
	}
}

func TestRenderChoice(t *testing.T) {
	p := choicePrompt{
		Question: "Do you want to proceed?",
		Options: []choiceOption{
			{Key: "1", Label: "Yes"},
			{Key: "2", Label: "No"},
		},
	}
	got := renderChoice(p)
	want := "Do you want to proceed?\n1. Yes\n2. No\n\n_Reply with a number to choose._"
	if got != want {
		t.Fatalf("renderChoice =\n%q\nwant\n%q", got, want)
	}
}

// The fixture capture round-trips through parse → render into a clean prompt the
// human can answer with a digit.
func TestParseThenRenderRoundTrip(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "claude_choice.txt"))
	if err != nil {
		t.Fatal(err)
	}
	p, ok := parseChoicePrompt(string(b))
	if !ok {
		t.Fatal("fixture should parse")
	}
	out := renderChoice(p)
	for _, want := range []string{"Do you want to proceed?", "1. Yes", "3. No, and tell Claude", "Reply with a number"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered prompt missing %q:\n%s", want, out)
		}
	}
}

func TestPendingChoiceSurfacesParsedPrompt(t *testing.T) {
	r := &tmuxResponder{}
	if _, ok := r.PendingChoice(); ok {
		t.Fatal("a fresh responder has no pending choice")
	}
	r.pending = &choicePrompt{
		Question: "Proceed?",
		Options:  []choiceOption{{Key: "1", Label: "Yes"}, {Key: "2", Label: "No"}},
	}
	c, ok := r.PendingChoice()
	if !ok {
		t.Fatal("expected a pending choice")
	}
	if c.Question != "Proceed?" || len(c.Options) != 2 {
		t.Fatalf("unexpected choice: %+v", c)
	}
	if c.Options[0] != (ChoiceItem{Value: "1", Label: "Yes"}) || c.Options[1].Value != "2" {
		t.Fatalf("options not mapped (Key->Value): %+v", c.Options)
	}
}

// The tmux responder satisfies the optional choice interfaces the bridge checks.
func TestTmuxResponderImplementsChoiceInterfaces(t *testing.T) {
	var _ ChoiceAware = (*tmuxResponder)(nil)
	var _ ChoiceInjector = (*tmuxResponder)(nil)
}

func TestParseChoicePromptUnboxed(t *testing.T) {
	// Same prompt without box chrome (some widths render unframed).
	in := strings.Join([]string{
		"Do you want to proceed?",
		"❯ 1. Yes",
		"  2. No",
	}, "\n")
	p, ok := parseChoicePrompt(in)
	if !ok || len(p.Options) != 2 || p.Options[1].Label != "No" {
		t.Fatalf("unboxed prompt not parsed: ok=%v %+v", ok, p)
	}
}

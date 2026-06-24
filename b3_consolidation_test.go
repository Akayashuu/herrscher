package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Herrscherd/herrscher-contracts"
	orchestrator "github.com/Herrscherd/herrscher-orchestrator"
	"github.com/Herrscherd/herrscher/core/host"
)

// B3 proves the P1 write loop end to end from the host: an enabled session
// (a registered extractor + a journal + a cadence, all threaded by
// buildOrchestrator) must consolidate the journal's tagged lines into exactly
// one shared fact under projects/<project> and one private skill under
// agents/<agent>, idempotently. The orchestrator owns the trigger/scope/dedup
// (herrscher-orchestrator); these tests verify B2's activation actually reaches
// those guarantees, rather than trusting the upstream unit tests alone.

// b3Extractor is a deterministic, dependency-free extractor that reads the
// journal it is handed and turns "fact:"/"skill:" lines into candidates. Reading
// the journal (not emitting fixed nodes) is deliberate: it proves the journal
// path is honored all the way from buildOrchestrator's config to the Learner.
type b3Extractor struct{}

func (b3Extractor) Extract(_ context.Context, journal, _ string) ([]orchestrator.Candidate, error) {
	var cands []orchestrator.Candidate
	for _, line := range strings.Split(journal, "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "fact:"); ok {
			v = strings.TrimSpace(v)
			cands = append(cands, orchestrator.Candidate{
				Node:    contracts.Node{Key: "fact/" + v, Kind: contracts.KindDecision, Title: v},
				Private: false,
			})
		}
		if v, ok := strings.CutPrefix(line, "skill:"); ok {
			v = strings.TrimSpace(v)
			cands = append(cands, orchestrator.Candidate{
				Node:    contracts.Node{Key: "skill/" + v, Kind: contracts.KindDecision, Title: v},
				Private: true,
			})
		}
	}
	return cands, nil
}

// linkCall captures one Memory.Links edge so a test can tell a shared write
// (linked under the project root) from a private one (under the agent root).
type linkCall struct{ from, to, rel string }

// recMemory is a fake Memory port that records every Record/Links call. Recall
// returns an empty rooted subgraph so the orchestrator's transcript read and
// scoped recall are clean no-ops.
type recMemory struct {
	records []contracts.Node
	links   []linkCall
}

func (m *recMemory) Recall(_ context.Context, key string, _ int) (contracts.Subgraph, error) {
	return contracts.Subgraph{Root: contracts.Node{Key: key}}, nil
}

func (m *recMemory) Record(_ context.Context, n contracts.Node) error {
	m.records = append(m.records, n)
	return nil
}

func (m *recMemory) Search(context.Context, contracts.Query) ([]contracts.Node, error) {
	return nil, nil
}

func (m *recMemory) Links(_ context.Context, from, to, rel string) error {
	m.links = append(m.links, linkCall{from, to, rel})
	return nil
}

func (m *recMemory) Close() error { return nil }

// linksFrom returns the edges hung under root — i.e. the scoped consolidation
// writes (RecordShared/RecordPrivate link the new node under a scope root). The
// session-transcript Observe writes a node but no link, so it never shows here.
func linksFrom(m *recMemory, root string) []linkCall {
	var out []linkCall
	for _, l := range m.links {
		if l.from == root {
			out = append(out, l)
		}
	}
	return out
}

func writeJournal(t *testing.T, lines string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "calls.log")
	if err := os.WriteFile(path, []byte(lines), 0o644); err != nil {
		t.Fatalf("write journal: %v", err)
	}
	return path
}

func TestB3ConsolidationWritesScopedFactAndSkill(t *testing.T) {
	orchestrator.RegisterExtractor("b3-fake", b3Extractor{})
	mem := &recMemory{}
	journal := writeJournal(t, "noise line\nfact: studio uses ECS\nmore noise\nskill: retry on 429\n")

	orch := buildOrchestrator(context.Background(), mem, "s1", "alpha", "bishop",
		learnConfig{extractor: "b3-fake", journal: journal, consolidateEvery: 2}, host.Logger(false))
	if _, ok := orch.(*orchestrator.Learner); !ok {
		t.Fatalf("want *orchestrator.Learner, got %T", orch)
	}

	// Two turns crosses the cadence (every 2), firing Consolidate once.
	p := contracts.Prompt{Author: "u", Content: "hi"}
	for i := 0; i < 2; i++ {
		if err := orch.Observe(context.Background(), p, "ok"); err != nil {
			t.Fatalf("Observe: %v", err)
		}
	}

	shared := linksFrom(mem, "projects/alpha")
	if len(shared) != 1 || shared[0].to != "fact/studio uses ECS" {
		t.Fatalf("want one shared fact under projects/alpha, got %+v", shared)
	}
	private := linksFrom(mem, "agents/bishop")
	if len(private) != 1 || private[0].to != "skill/retry on 429" {
		t.Fatalf("want one private skill under agents/bishop, got %+v", private)
	}
}

func TestB3ConsolidationIsIdempotent(t *testing.T) {
	orchestrator.RegisterExtractor("b3-fake", b3Extractor{})
	mem := &recMemory{}
	journal := writeJournal(t, "fact: studio uses ECS\nskill: retry on 429\n")

	orch := buildOrchestrator(context.Background(), mem, "s1", "alpha", "bishop",
		learnConfig{extractor: "b3-fake", journal: journal, consolidateEvery: 1}, host.Logger(false))

	// Five turns at cadence 1 runs Consolidate five times over the same journal;
	// the Learner's per-session dedup must keep each scoped write at exactly one.
	p := contracts.Prompt{Author: "u", Content: "hi"}
	for i := 0; i < 5; i++ {
		if err := orch.Observe(context.Background(), p, "ok"); err != nil {
			t.Fatalf("Observe: %v", err)
		}
	}

	if got := len(linksFrom(mem, "projects/alpha")); got != 1 {
		t.Fatalf("idempotence: want 1 shared write, got %d", got)
	}
	if got := len(linksFrom(mem, "agents/bishop")); got != 1 {
		t.Fatalf("idempotence: want 1 private write, got %d", got)
	}
}

func TestB3NoExtractorPerformsNoConsolidationWrites(t *testing.T) {
	mem := &recMemory{}
	orch := buildOrchestrator(context.Background(), mem, "s1", "alpha", "bishop",
		learnConfig{}, host.Logger(false))
	if _, ok := orch.(*orchestrator.Curator); !ok {
		t.Fatalf("want plain *orchestrator.Curator with no extractor, got %T", orch)
	}

	p := contracts.Prompt{Author: "u", Content: "hi"}
	for i := 0; i < 3; i++ {
		if err := orch.Observe(context.Background(), p, "ok"); err != nil {
			t.Fatalf("Observe: %v", err)
		}
	}

	// The default Curator keeps only the rolling transcript (Record, no Links);
	// it must perform zero scoped consolidation writes.
	if len(mem.links) != 0 {
		t.Fatalf("default session must not consolidate, got links %+v", mem.links)
	}
}

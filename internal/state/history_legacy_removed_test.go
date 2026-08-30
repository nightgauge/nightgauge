package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/intelligence/tokens"
)

// The legacy `executions.jsonl` history path (HistoryEntry, HistoryWriter.Write,
// HistoryWriter.ReadRecent) was deleted by #839. It had no production callers,
// but it was not merely inert: HistoryWriter.Write appended to `hw.dir`, which
// is the very directory the VSCode dashboard's JSONL scanners glob
// (DashboardState.getModelRoutingMetrics / getStageModelInfo read every
// *.jsonl there). HistoryEntry.Stages was a []StageResult — an ARRAY — whereas
// every record those readers accept carries `stages` as an object map (the
// V1/V2/V3 Zod schemas all use z.record, and Go's V2RunRecord uses
// map[string]V2StageDetail). #466 had to teach both scanners to skip
// array-shaped records precisely because of this shape.
//
// These two tests pin the reason the path was removed rather than the fact of
// its removal, so a reintroduction fails on the property that actually matters.

// TestHistoryDirRecordsUseObjectStages is the behavioural guard: everything
// HistoryWriter emits into its directory must carry `stages` as a JSON object,
// never an array. A reintroduced HistoryEntry-shaped writer fails here.
func TestHistoryDirRecordsUseObjectStages(t *testing.T) {
	root := t.TempDir()
	hw := NewHistoryWriter(root)
	// hw.dir — <root>/.nightgauge/pipeline/history — is the directory the
	// dashboard's JSONL scanners glob, and is where the deleted Write() aimed.
	dir := hw.dir

	rs := NewRuntimeState("nightgauge/nightgauge", 839, "item-839", testRunID())
	rs.BeginStage(StageIssuePickup)
	rs.CompleteStage(0, tokens.TokenCounts{Input: 1000, Output: 500}, "", "")

	if err := hw.WriteV2(rs, true, "", V2RunInput{Branch: "chore/839", BaseBranch: "main"}); err != nil {
		t.Fatalf("WriteV2: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	seen := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		for i, line := range splitLines(data) {
			if len(line) == 0 {
				continue
			}
			var probe struct {
				Stages json.RawMessage `json:"stages"`
			}
			if err := json.Unmarshal(line, &probe); err != nil {
				t.Fatalf("%s line %d: unmarshal: %v", e.Name(), i, err)
			}
			trimmed := strings.TrimSpace(string(probe.Stages))
			if trimmed == "" || trimmed == "null" {
				continue
			}
			seen++
			if trimmed[0] != '{' {
				t.Errorf("%s line %d: `stages` is %q-shaped, want a JSON object — the "+
					"dashboard's *.jsonl scanners in this directory reject array-shaped "+
					"records (#466, #839)", e.Name(), i, trimmed[0])
			}
		}
	}
	if seen == 0 {
		t.Fatal("no record with a `stages` field was written; the guard proved nothing")
	}
}

// TestLegacyExecutionsJSONLPathStaysDeleted is the source guard: the filename
// and the legacy symbols must not come back. A behavioural test cannot see a
// writer nobody calls, which is exactly the state #839 found the old path in.
func TestLegacyExecutionsJSONLPathStaysDeleted(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	banned := []string{"executions.jsonl", "HistoryEntry{", "type HistoryEntry ", ") ReadRecent("}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || e.Name() == "history_legacy_removed_test.go" {
			continue
		}
		data, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		src := string(data)
		for _, b := range banned {
			if strings.Contains(src, b) {
				t.Errorf("%s reintroduces the legacy history path (%q); #839 deleted it "+
					"because it wrote array-shaped `stages` into the directory the "+
					"dashboard scanners glob", e.Name(), b)
			}
		}
	}
}

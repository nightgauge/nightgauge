package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAppendAndReadUsage(t *testing.T) {
	root := t.TempDir()

	if err := AppendRecord(root, Record{TS: "2026-06-01T00:00:00Z", Skill: "nightgauge-security-audit", Session: "s1"}); err != nil {
		t.Fatalf("AppendRecord: %v", err)
	}
	if err := AppendRecord(root, Record{TS: "2026-06-02T00:00:00Z", Skill: "nightgauge-security-audit"}); err != nil {
		t.Fatalf("AppendRecord: %v", err)
	}

	recs, err := ReadUsage(root)
	if err != nil {
		t.Fatalf("ReadUsage: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("want 2 records, got %d", len(recs))
	}
	if recs[0].Skill != "nightgauge-security-audit" || recs[0].Session != "s1" {
		t.Errorf("unexpected first record: %+v", recs[0])
	}
}

func TestAppendRecordDefaultsTimestamp(t *testing.T) {
	root := t.TempDir()
	if err := AppendRecord(root, Record{Skill: "x"}); err != nil {
		t.Fatalf("AppendRecord: %v", err)
	}
	recs, _ := ReadUsage(root)
	if len(recs) != 1 || recs[0].TS == "" {
		t.Fatalf("expected a defaulted timestamp, got %+v", recs)
	}
}

func TestReadUsageMissingFileIsNotError(t *testing.T) {
	recs, err := ReadUsage(t.TempDir())
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if recs != nil {
		t.Fatalf("want nil, got %+v", recs)
	}
}

func TestReadUsageSkipsMalformedLines(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".nightgauge", "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "{\"ts\":\"t\",\"skill\":\"a\"}\nnot-json\n{\"skill\":\"\"}\n{\"ts\":\"t\",\"skill\":\"b\"}\n"
	if err := os.WriteFile(filepath.Join(dir, "usage.jsonl"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	recs, err := ReadUsage(root)
	if err != nil {
		t.Fatalf("ReadUsage: %v", err)
	}
	// malformed line skipped; empty-skill line skipped → 2 valid records
	if len(recs) != 2 {
		t.Fatalf("want 2 valid records, got %d (%+v)", len(recs), recs)
	}
}

func TestAggregateCountsAndOrdersByTriggers(t *testing.T) {
	recs := []Record{
		{TS: "2026-06-01T00:00:00Z", Skill: "a"},
		{TS: "2026-06-03T00:00:00Z", Skill: "a"},
		{TS: "2026-06-02T00:00:00Z", Skill: "b"},
	}
	stats := Aggregate(recs, nil)
	if len(stats) != 2 {
		t.Fatalf("want 2 stats, got %d", len(stats))
	}
	if stats[0].Skill != "a" || stats[0].TriggerCount != 2 {
		t.Errorf("expected 'a' first with 2 triggers, got %+v", stats[0])
	}
	if stats[0].FirstSeen != "2026-06-01T00:00:00Z" || stats[0].LastSeen != "2026-06-03T00:00:00Z" {
		t.Errorf("first/last seen wrong: %+v", stats[0])
	}
}

func TestAggregateFlagsNeverTriggeredFromCatalog(t *testing.T) {
	recs := []Record{{TS: "2026-06-01T00:00:00Z", Skill: "a"}}
	catalog := []string{"a", "b", "c"}
	stats := Aggregate(recs, catalog)

	never := map[string]bool{}
	for _, s := range stats {
		if s.NeverSeen {
			never[s.Skill] = true
		}
	}
	if !never["b"] || !never["c"] || never["a"] {
		t.Fatalf("expected b,c never-triggered and a triggered; got %+v", stats)
	}
}

func TestCatalogNames(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"nightgauge-queue", "smart-setup"} {
		d := filepath.Join(root, "skills", name)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte("# x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	names, err := CatalogNames(root)
	if err != nil {
		t.Fatalf("CatalogNames: %v", err)
	}
	if len(names) != 2 || names[0] != "nightgauge-queue" || names[1] != "smart-setup" {
		t.Fatalf("unexpected catalog: %+v", names)
	}
}

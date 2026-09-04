package telemetry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeEvents(t *testing.T, root string, events []Event) {
	t.Helper()
	p := Path(root)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, ev := range events {
		if err := enc.Encode(ev); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWriteLogMarkdown(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC()
	day1 := now.Add(-48 * time.Hour).Format(time.RFC3339)
	day2 := now.Add(-24 * time.Hour).Format(time.RFC3339)

	writeEvents(t, root, []Event{
		{Timestamp: day1, Type: EventScaffold, Path: ".nightgauge/knowledge/features/1-a/PRD.md"},
		{Timestamp: day1, Type: EventWrite, Path: ".nightgauge/knowledge/features/1-a/decisions.md"},
		{Timestamp: day2, Type: EventGraduate, Path: ".nightgauge/knowledge/features/2-b/decisions.md"},
		{Timestamp: day2, Type: EventPrune, Path: ".nightgauge/knowledge/features/3-c/PRD.md"},
		// A read is usage, not a change: log.md is a change history, and a log
		// dominated by "someone searched" tells a reader nothing about how the
		// knowledge got there.
		{Timestamp: day2, Type: EventRead, Path: ".nightgauge/knowledge/features/1-a/PRD.md"},
		{Timestamp: day2, Type: EventRecall, QuerySummary: "bm25"},
	})

	out := filepath.Join(root, "log.md")
	if err := WriteLogMarkdown(root, out); err != nil {
		t.Fatalf("WriteLogMarkdown: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)

	d1 := now.Add(-48 * time.Hour).UTC().Format("2006-01-02")
	d2 := now.Add(-24 * time.Hour).UTC().Format("2006-01-02")

	// Newest date first.
	i2, i1 := strings.Index(body, "## "+d2), strings.Index(body, "## "+d1)
	if i2 < 0 || i1 < 0 {
		t.Fatalf("missing date headings:\n%s", body)
	}
	if i2 > i1 {
		t.Errorf("dates are not newest-first:\n%s", body)
	}

	for _, want := range []string{
		"**Scaffolded**: [features/1-a/PRD.md](/features/1-a/PRD.md)",
		"**Written**: [features/1-a/decisions.md](/features/1-a/decisions.md)",
		"**Graduated**: [features/2-b/decisions.md](/features/2-b/decisions.md)",
		"**Pruned**: [features/3-c/PRD.md](/features/3-c/PRD.md)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing bullet %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Read") || strings.Contains(body, "bm25") {
		t.Errorf("usage events leaked into the change log:\n%s", body)
	}
	// Links are bundle-absolute — rooted at the knowledge root, not the
	// workspace, which is what an OKF consumer resolves.
	if strings.Contains(body, "(/.nightgauge/") {
		t.Errorf("links are workspace-relative, not bundle-absolute:\n%s", body)
	}
}

func TestWriteLogMarkdown_DeduplicatesWithinADay(t *testing.T) {
	root := t.TempDir()
	ts := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	writeEvents(t, root, []Event{
		{Timestamp: ts, Type: EventWrite, Path: ".nightgauge/knowledge/features/1-a/decisions.md"},
		{Timestamp: ts, Type: EventWrite, Path: ".nightgauge/knowledge/features/1-a/decisions.md"},
		{Timestamp: ts, Type: EventWrite, Path: ".nightgauge/knowledge/features/1-a/decisions.md"},
	})

	out := filepath.Join(root, "log.md")
	if err := WriteLogMarkdown(root, out); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(out)
	// A stage that rewrites the same entry three times in a day is one change
	// to a reader.
	if n := strings.Count(string(data), "**Written**"); n != 1 {
		t.Errorf("bullet count = %d, want 1:\n%s", n, data)
	}
}

func TestWriteLogMarkdown_NoEventStreamIsAnErrorNotAPanic(t *testing.T) {
	root := t.TempDir()
	// The index generator ignores this error on purpose: log.md renders data
	// that is already queryable elsewhere, so it must never fail the index the
	// pipeline depends on.
	if err := WriteLogMarkdown(root, filepath.Join(root, "log.md")); err == nil {
		t.Error("expected an error when the event stream is absent")
	}
}

func TestWriteLogMarkdown_EmptyEventStreamRendersAnEmptyLog(t *testing.T) {
	root := t.TempDir()
	writeEvents(t, root, nil)

	out := filepath.Join(root, "log.md")
	if err := WriteLogMarkdown(root, out); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(out)
	if !strings.Contains(string(data), "No recorded changes.") {
		t.Errorf("empty log body = %s", data)
	}
	if !strings.HasPrefix(string(data), "---\ntype: log") {
		t.Errorf("log.md is not a conformant entry:\n%s", data)
	}
}

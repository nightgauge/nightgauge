package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/cmd/finding"
)

func runFinding(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := findingCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestFindingCmd_RecordAndList(t *testing.T) {
	file := filepath.Join(t.TempDir(), "findings.jsonl")
	out, err := runFinding(t, "record", "--file", file, "--kind", "gotcha", "--scope", "project",
		"--summary", "Migrations must run before seed", "--remedy", "run make migrate; see db/seed.go",
		"--evidence", "seed failed: relation missing", "--path", "db")
	if err != nil {
		t.Fatalf("record: %v\n%s", err, out)
	}
	id := strings.TrimSpace(out)
	if id != finding.ID("gotcha", "project", "Migrations must run before seed") {
		t.Fatalf("printed id %q", id)
	}
	if _, err := runFinding(t, "record", "--file", file, "--kind", "environment", "--scope", "pipeline",
		"--summary", "Unrelated finding"); err != nil {
		t.Fatal(err)
	}

	out, err = runFinding(t, "list", "--file", file, "--match-paths", "db/seed.go,README.md", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var recs []finding.Record
	if err := json.Unmarshal([]byte(out), &recs); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(recs) != 1 || recs[0].ID != id || recs[0].Remedy == "" || len(recs[0].Evidence) != 1 {
		t.Fatalf("match-paths list: %+v", recs)
	}
	out, _ = runFinding(t, "list", "--file", file, "--scope", "pipeline")
	if !strings.Contains(out, "environment/pipeline  Unrelated finding") || strings.Contains(out, "Migrations") {
		t.Fatalf("text list: %q", out)
	}
	out, _ = runFinding(t, "record", "--file", file, "--kind", "gotcha", "--scope", "project",
		"--summary", "Migrations must run before seed")
	if !strings.Contains(out, "already recorded") {
		t.Fatalf("re-record: %q", out)
	}
}

func TestFindingCmd_RefusalsExitNonZero(t *testing.T) {
	file := filepath.Join(t.TempDir(), "findings.jsonl")
	_, err := runFinding(t, "record", "--file", file, "--kind", "bogus", "--scope", "project", "--summary", "x")
	if err == nil || !strings.HasPrefix(err.Error(), "kind:") {
		t.Fatalf("bogus kind: %v", err)
	}
	_, err = runFinding(t, "record", "--file", file, "--kind", "gotcha", "--scope", "project")
	if err == nil || !strings.HasPrefix(err.Error(), "summary:") {
		t.Fatalf("missing summary: %v", err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("a refused record created the file")
	}
}

func TestFindingCmd_AbsentFileListsEmpty(t *testing.T) {
	file := filepath.Join(t.TempDir(), "findings.jsonl")
	out, err := runFinding(t, "list", "--file", file, "--json")
	if err != nil || strings.TrimSpace(out) != "[]" {
		t.Fatalf("list --json: %v %q", err, out)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("list created the file")
	}
}

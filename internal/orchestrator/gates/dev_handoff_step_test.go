package gates

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// WorkTreeFingerprint (#1651) must move on a content change even when the set
// of changed paths does not, and must not move on pipeline bookkeeping.
func TestWorkTreeFingerprint_ContentNotPathSet(t *testing.T) {
	dir := gitRepo(t)
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fp := func() string {
		t.Helper()
		f, err := WorkTreeFingerprint(dir)
		if err != nil {
			t.Fatalf("WorkTreeFingerprint: %v", err)
		}
		return f
	}

	clean := fp()
	write("feature.go", "package x // v1\n")
	v1 := fp()
	if v1 == clean {
		t.Fatal("an untracked deliverable file did not move the fingerprint")
	}
	write("feature.go", "package x // v2\n")
	if fp() == v1 {
		t.Error("editing an already-changed file did not move the fingerprint")
	}
	v2 := fp()
	write(".nightgauge/pipeline/dev-1.json", "{}")
	if fp() != v2 {
		t.Error("pipeline bookkeeping moved the fingerprint")
	}
	if out, err := gitOutput(dir, "status", "--porcelain", "--", "feature.go"); err != nil || out == "" {
		t.Errorf("the real index was touched: status %q err %v", out, err)
	}
}

// DeriveStepHandoff (#1651) stamps the step, keeps the stage's narrative, and
// writes nothing over an empty tree.
func TestDeriveStepHandoff(t *testing.T) {
	dir := gitRepo(t)
	ctxPath := contextFilePath(dir, "dev", 7)

	if h, err := DeriveStepHandoff(dir, 7, ctxPath, 1, time.Now()); err != nil || h.Written {
		t.Fatalf("empty tree: written=%v err=%v, want nothing written", h.Written, err)
	}
	if _, err := os.Stat(ctxPath); !os.IsNotExist(err) {
		t.Fatalf("empty tree produced a handoff file (stat err %v)", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(ctxPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ctxPath, []byte(`{"knowledge_path":"kb/7","files_changed":{"created":["stale.go"]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	h, err := DeriveStepHandoff(dir, 7, ctxPath, 2, time.Now())
	if err != nil || !h.Written {
		t.Fatalf("written=%v err=%v", h.Written, err)
	}
	raw, _ := os.ReadFile(ctxPath)
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["handoff_source"] != HandoffSourceDerived || doc["step"] != float64(2) || doc["knowledge_path"] != "kb/7" {
		t.Errorf("doc = %v", doc)
	}
	created := doc["files_changed"].(map[string]any)["created"].([]any)
	if len(created) != 1 || created[0] != "a.go" {
		t.Errorf("created = %v, want git's [a.go], not the stage's stale list", created)
	}
}

// #1651 AC7 finding: when the last sub-session writes no handoff, the step
// loop's DeriveStepHandoff document is the one the gate judges. The gate must
// report that pass as derived, not as a handoff the stage authored.
func TestFeatureDevGate_StepLoopDerivedHandoffIsReportedDerived(t *testing.T) {
	ws := gitRepo(t)
	writeFile(t, filepath.Join(ws, "internal", "step", "step.go"), "package step\n")
	ctxPath := contextFilePath(ws, "dev", 1651)
	if h, err := DeriveStepHandoff(ws, 1651, ctxPath, 3, time.Now()); err != nil || !h.Written {
		t.Fatalf("DeriveStepHandoff: written=%v err=%v", h.Written, err)
	}

	gr := FeatureDevGate{}.Verify(context.Background(), 1651, ws)

	if !gr.Passed {
		t.Fatalf("expected pass over the derived step handoff; reason=%q evidence=%v", gr.Reason, gr.Evidence)
	}
	joined := strings.Join(gr.Evidence, "\n")
	if !strings.Contains(joined, "handoff_source=derived") || strings.Contains(joined, "handoff_source=authored") {
		t.Errorf("evidence does not report the loop's derivation as derived:\n%s", joined)
	}
}

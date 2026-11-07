package deliverable

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/gateledger"
)

func TestConfirmUnitFromLedger(t *testing.T) {
	pass := gateledger.Answer{Reusable: true, Pass: gateledger.Event{Tree: "abc"}}

	doc := map[string]any{"unit_tests": map[string]any{"ran": false, "log": "/tmp/gate.log"}}
	if !ConfirmUnitFromLedger(doc, pass) {
		t.Fatal("a block that did not run must take the ledger pass")
	}
	unit := doc["unit_tests"].(map[string]any)
	if unit["ran"] != true || unit["passed"] != true || unit["source"] != UnitSourceGateLedger ||
		unit["tree"] != "abc" || unit["log"] != "/tmp/gate.log" {
		t.Errorf("unit_tests = %v", unit)
	}

	failed := map[string]any{"unit_tests": map[string]any{"ran": true, "passed": false}}
	if ConfirmUnitFromLedger(failed, pass) || failed["unit_tests"].(map[string]any)["passed"] != false {
		t.Error("a recorded run keeps its own verdict")
	}
	if ConfirmUnitFromLedger(map[string]any{}, gateledger.Answer{}) {
		t.Error("no ledger pass confirms nothing")
	}
	absent := map[string]any{}
	if !ConfirmUnitFromLedger(absent, pass) || !ExecutionFromArtifact(absent).Unit {
		t.Error("an absent block is created from the ledger pass")
	}
}

func TestParseGoTestOutcomes(t *testing.T) {
	p := filepath.Join(t.TempDir(), "gate.log")
	log := "=== RUN   TestA\n--- PASS: TestA (0.35s)\n    --- PASS: TestA/sub (0.00s)\n--- FAIL: TestB (1.00s)\n" +
		"--- PASS: TestFlaky (0.01s)\n--- FAIL: TestFlaky (0.01s)\n  ✓ PASS: TestNotGo\nok  \tpkg\t1.0s\n"
	if err := os.WriteFile(p, []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ParseGoTestOutcomes(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"TestA", "TestA/sub"} {
		if !got.Passed[name] {
			t.Errorf("%s should pass", name)
		}
	}
	for _, name := range []string{"TestB", "TestFlaky"} {
		if !got.Failed[name] || got.Passed[name] {
			t.Errorf("%s should be failed only", name)
		}
	}
	if got.Passed["TestNotGo"] {
		t.Error("only Go's own result lines count")
	}
	if _, err := ParseGoTestOutcomes(filepath.Join(t.TempDir(), "missing.log")); err == nil {
		t.Error("a missing log is an error, not an empty pass")
	}
}

func TestFindingRecheck(t *testing.T) {
	f := Finding{
		Artifacts:        []TestArtifact{{Path: "a_test.go"}, {Path: "e2e/b.spec.ts"}, {Path: "c_test.go"}},
		Tiers:            []Tier{TierUnit, TierE2E},
		TierReasons:      map[Tier]string{TierUnit: "u", TierE2E: "e"},
		SupersededStatus: "passed",
	}
	added := map[string]bool{"a_test.go": true, "e2e/b.spec.ts": true}
	keep := func(p string) bool { return added[p] }

	if paths := f.Recheck(keep, Execution{}).Paths(); len(paths) != 2 || paths[0] != "a_test.go" {
		t.Errorf("paths = %v, want the two added files", paths)
	}
	got := f.Recheck(keep, Execution{Unit: true})
	if paths := got.Paths(); len(paths) != 1 || paths[0] != "e2e/b.spec.ts" || len(got.Tiers) != 1 || got.TierReasons[TierUnit] != "" {
		t.Errorf("a ran unit tier drops the unit file and its reason: %+v", got)
	}
	if got := f.Recheck(func(string) bool { return false }, Execution{}); got.Detected() {
		t.Errorf("nothing kept is no finding: %+v", got)
	}
}

func TestLogFromPass(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gate.log")
	if err := os.WriteFile(path, []byte("--- PASS: TestX (0.01s)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	pass := &gateledger.Event{TS: now.Format(time.RFC3339Nano), Seconds: 60}
	if !LogFromPass(path, pass) {
		t.Error("a log written during the pass was not tied to it")
	}
	if LogFromPass(path, nil) {
		t.Error("a log was tied to no pass")
	}
	stale := now.Add(-3 * time.Hour)
	if err := os.Chtimes(path, stale, stale); err != nil {
		t.Fatal(err)
	}
	if LogFromPass(path, pass) {
		t.Error("a stale log from an earlier run was tied to this pass")
	}
}

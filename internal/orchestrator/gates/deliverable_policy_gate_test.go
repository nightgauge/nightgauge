package gates

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The #1176 headline. feature-dev finished — lint clean, three files
// reconciled — and the gate discarded all of it because `files_changed` was a
// flat array while `files_created`/`files_modified` carried exactly the values
// the schema wanted. $6.12 and thirteen minutes, thrown away over a typo in a
// receipt.
//
// The repair is total (every path in the array is classified by a sibling key),
// so the gate must now let the work through.
func TestFeatureDevGate_RepairableManifestShapeProceeds(t *testing.T) {
	ws := gitRepo(t)
	// The whole point of #1176 is that the implementation was ON DISK and
	// correct. Put it there, so the only thing wrong with this run is the
	// shape of the receipt.
	writeFile(t, filepath.Join(ws, "docs", "PRODUCT_REQUIREMENTS.md"), "requirements\n")
	writeFile(t, filepath.Join(ws, "docs", "a.md"), "a\n")

	path := filepath.Join(ws, ".nightgauge", "pipeline", devContextName(210))
	writeFile(t, path, `{
  "schema_version": "1.5",
  "issue_number": 210,
  "files_changed": ["docs/PRODUCT_REQUIREMENTS.md", "docs/a.md"],
  "files_created": [],
  "files_modified": ["docs/PRODUCT_REQUIREMENTS.md", "docs/a.md"],
  "build_verification": {"ran": true, "status": "passed"}
}`)

	gate, ok := LookupByStageName("feature-dev")
	if !ok {
		t.Fatal("no feature-dev gate")
	}
	res := gate.Verify(context.Background(), 210, ws)

	if !res.Passed {
		t.Fatalf("gate rejected a totally repairable deliverable: %s %v", res.Reason, res.Evidence)
	}

	// The repair must be RECORDED, not silently papered over — a skill emitting
	// the wrong shape has to stay visible or nobody ever fixes the emitter.
	joined := strings.Join(res.Evidence, " ")
	if !strings.Contains(joined, "dev.files_changed.from_sibling_manifest") {
		t.Errorf("the repair is not in the gate evidence (run record), got %v", res.Evidence)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("repaired deliverable is not JSON: %v", err)
	}
	fc, isObj := doc["files_changed"].(map[string]any)
	if !isObj {
		t.Fatalf("files_changed was not rewritten to an object: %#v", doc["files_changed"])
	}
	modified, _ := fc["modified"].([]any)
	if len(modified) != 2 {
		t.Errorf("modified = %#v, want the two paths the sibling manifest carried", fc["modified"])
	}
	if _, present := doc["files_created"]; present {
		t.Error("the sibling manifest keys must be consumed, not left to disagree with files_changed")
	}
	marker, hasMarker := doc["_deliverable_policy"].(map[string]any)
	if !hasMarker {
		t.Fatal("no _deliverable_policy marker written into the deliverable")
	}
	repairs, _ := marker["repairs"].([]any)
	if len(repairs) < 2 {
		t.Errorf("expected the schema_version stamp and the manifest rebuild to be recorded, got %#v", marker["repairs"])
	}
}

// The gate that "fixes" its way to a pass is worse than no gate. A deliverable
// whose created/modified split was never written must still fail — there is
// nothing to repair from, and inventing one is the failure mode this policy
// exists to forbid.
func TestFeatureDevGate_MissingInformationStillFails(t *testing.T) {
	ws := gitRepo(t)
	writeFile(t, filepath.Join(ws, ".nightgauge", "pipeline", devContextName(211)), `{
  "schema_version": "1.8",
  "issue_number": 211,
  "files_changed": ["src/a.ts", "src/b.ts"],
  "build_verification": {"ran": true, "status": "passed"}
}`)

	gate, _ := LookupByStageName("feature-dev")
	res := gate.Verify(context.Background(), 211, ws)
	if res.Passed {
		t.Fatal("the gate passed a deliverable that is genuinely missing the created/modified split")
	}
	if res.TerminalKind != TerminalKindValidationError {
		t.Errorf("TerminalKind = %q, want %q", res.TerminalKind, TerminalKindValidationError)
	}
}

// A sibling manifest that does not account for every declared path cannot
// produce a TOTAL repair. Partial fill is inference wearing a repair's clothes.
func TestFeatureDevGate_PartialManifestIsNotARepair(t *testing.T) {
	ws := gitRepo(t)
	writeFile(t, filepath.Join(ws, ".nightgauge", "pipeline", devContextName(212)), `{
  "schema_version": "1.8",
  "issue_number": 212,
  "files_changed": ["src/a.ts", "src/b.ts", "src/unaccounted.ts"],
  "files_created": ["src/a.ts"],
  "files_modified": ["src/b.ts"],
  "build_verification": {"ran": true, "status": "passed"}
}`)

	gate, _ := LookupByStageName("feature-dev")
	res := gate.Verify(context.Background(), 212, ws)
	if res.Passed {
		t.Fatal("the gate accepted a partial repair")
	}
	joined := strings.Join(res.Evidence, " ")
	if !strings.Contains(joined, "src/unaccounted.ts") {
		t.Errorf("evidence must name the path no sibling key classified, got %v", res.Evidence)
	}
}

package gates

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// #240 shipped a complete, well-formed dev context whose files_changed was an
// array instead of {created, modified, deleted}. The gate called that "not
// valid JSON", which points triage at a truncated write that never happened.
func TestFeatureDevGate_SchemaMismatchIsNotReportedAsInvalidJSON(t *testing.T) {
	ws := gitRepo(t)
	body := `{
  "schema_version": "1.8",
  "issue_number": 42,
  "files_changed": ["internal/e2e/e2e.go", "internal/e2e/e2e_test.go"],
  "build_verification": {"ran": true, "status": "passed"}
}`
	writeFile(t, filepath.Join(ws, ".nightgauge", "pipeline", devContextName(42)), body)

	gate, ok := LookupByStageName("feature-dev")
	if !ok {
		t.Fatal("no feature-dev gate")
	}
	res := gate.Verify(context.Background(), 42, ws)

	if res.Passed {
		t.Fatal("expected the gate to fail on a shape mismatch")
	}
	if strings.Contains(res.Reason, "not valid JSON") {
		t.Errorf("reason = %q; the file parses cleanly — a shape mismatch must not be reported as invalid JSON", res.Reason)
	}
	if !strings.Contains(res.Reason, "schema") {
		t.Errorf("reason = %q, want it to name a schema mismatch", res.Reason)
	}
	joined := strings.Join(res.Evidence, " ")
	if !strings.Contains(joined, "files_changed") {
		t.Errorf("evidence must name the offending field, got %v", res.Evidence)
	}
	if !strings.Contains(joined, "expected object") || !strings.Contains(joined, "got array") {
		t.Errorf("evidence must state expected vs actual shape, got %v", res.Evidence)
	}
	if strings.Contains(joined, "json:\\\"created\\\"") || strings.Contains(joined, "struct {") {
		t.Errorf("evidence must not dump the raw Go struct type, got %v", res.Evidence)
	}
}

// Genuinely malformed JSON must still say so — the two cases have different
// causes and the distinction is the whole point.
func TestFeatureDevGate_TruncatedContextIsStillInvalidJSON(t *testing.T) {
	ws := gitRepo(t)
	writeFile(t, filepath.Join(ws, ".nightgauge", "pipeline", devContextName(42)),
		`{"schema_version": "1.8", "files_changed": {"created": ["a.go"`)

	gate, ok := LookupByStageName("feature-dev")
	if !ok {
		t.Fatal("no feature-dev gate")
	}
	res := gate.Verify(context.Background(), 42, ws)

	if res.Passed {
		t.Fatal("expected the gate to fail on truncated JSON")
	}
	if !strings.Contains(res.Reason, "not valid JSON") {
		t.Errorf("reason = %q, want it to report invalid JSON for a truncated file", res.Reason)
	}
}

// The shared helper is used by every gate that decodes a context file, so the
// distinction holds across stages rather than only where it was first noticed.
func TestDescribeDecodeFailure_CoversBothShapes(t *testing.T) {
	var dst struct {
		Files struct {
			Created []string `json:"created"`
		} `json:"files_changed"`
	}

	reason, ev := describeDecodeFailure("dev context",
		jsonUnmarshalErr(t, `{"files_changed": []}`, &dst))
	if !strings.Contains(reason, "schema") {
		t.Errorf("type mismatch reason = %q, want a schema mismatch", reason)
	}
	if !strings.Contains(strings.Join(ev, " "), "files_changed") {
		t.Errorf("type mismatch evidence should name the field, got %v", ev)
	}

	reason, _ = describeDecodeFailure("dev context",
		jsonUnmarshalErr(t, `{"files_changed": {`, &dst))
	if !strings.Contains(reason, "not valid JSON") {
		t.Errorf("syntax error reason = %q, want invalid JSON", reason)
	}
}

func jsonUnmarshalErr(t *testing.T, body string, dst any) error {
	t.Helper()
	err := json.Unmarshal([]byte(body), dst)
	if err == nil {
		t.Fatalf("expected %q to fail decoding", body)
	}
	return err
}

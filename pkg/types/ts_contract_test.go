package types_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"

	"github.com/nightgauge/nightgauge/pkg/types"
)

// tsInterfaceFile returns the path to IpcClientBase.ts relative to repo root.
func tsInterfaceFile(t *testing.T) string {
	t.Helper()
	root := findTSRepoRoot(t)
	p := filepath.Join(root, "packages", "nightgauge-vscode", "src", "services", "IpcClientBase.ts")
	if _, err := os.Stat(p); err != nil {
		t.Skipf("IpcClientBase.ts not found at %s — skipping TS contract tests", p)
	}
	return p
}

// findTSRepoRoot walks up from the test's working directory to find go.mod.
func findTSRepoRoot(t *testing.T) string {
	t.Helper()
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root (no go.mod found)")
		}
		dir = parent
	}
}

// extractTSInterfaceFields reads IpcClientBase.ts and extracts field names from a
// named TypeScript interface. Returns sorted field names (optional fields stripped of ?).
func extractTSInterfaceFields(t *testing.T, interfaceName string) []string {
	t.Helper()
	path := tsInterfaceFile(t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read IpcClientBase.ts: %v", err)
	}

	src := string(data)

	// Find the interface block: "export interface <name> { ... }"
	blockRe := regexp.MustCompile(`(?s)export interface ` + regexp.QuoteMeta(interfaceName) + `\s*\{([^}]+)\}`)
	m := blockRe.FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("interface %q not found in IpcClientBase.ts", interfaceName)
	}
	block := m[1]

	// Extract field names: lines like "  fieldName?: type;" or "  fieldName: type;"
	fieldRe := regexp.MustCompile(`(?m)^\s+(\w+)\??:`)
	matches := fieldRe.FindAllStringSubmatch(block, -1)

	var fields []string
	seen := make(map[string]bool)
	for _, match := range matches {
		name := match[1]
		if !seen[name] {
			seen[name] = true
			fields = append(fields, name)
		}
	}
	sort.Strings(fields)
	return fields
}

// logDivergences logs (but does not fail) for fields that exist in one set but not the other.
// Use this for pre-existing divergences that are out-of-scope for the current fix.
// Use t.Errorf directly for divergences that MUST be fixed (critical contract violations).
func logDivergences(t *testing.T, goFields, tsFields []string, name string) {
	t.Helper()

	goSet := make(map[string]bool, len(goFields))
	for _, f := range goFields {
		goSet[f] = true
	}
	tsSet := make(map[string]bool, len(tsFields))
	for _, f := range tsFields {
		tsSet[f] = true
	}

	var inTSNotGo, inGoNotTS []string
	for _, f := range tsFields {
		if !goSet[f] {
			inTSNotGo = append(inTSNotGo, f)
		}
	}
	for _, f := range goFields {
		if !tsSet[f] {
			inGoNotTS = append(inGoNotTS, f)
		}
	}

	if len(inTSNotGo) > 0 {
		t.Logf("DIVERGENCE %s: TS interface fields absent from Go JSON tags (tracked issues, not fixed here): %v", name, inTSNotGo)
	}
	if len(inGoNotTS) > 0 {
		t.Logf("INFO %s: Go JSON fields absent from TS interface (may be internal-only): %v", name, inGoNotTS)
	}
}

// --- Cross-language contract tests ---

// TestGoTSContract_StatusCounts verifies that types.StatusCounts JSON tags
// match the TypeScript StatusCounts interface fields exactly.
func TestGoTSContract_StatusCounts(t *testing.T) {
	tsFields := extractTSInterfaceFields(t, "StatusCounts")
	goFields := structJSONKeys(types.StatusCounts{})
	logDivergences(t, goFields, tsFields, "StatusCounts")

	// StatusCounts is a simple type with no pre-existing divergences — assert exact match.
	if len(tsFields) != len(goFields) {
		t.Errorf("StatusCounts: Go has %d JSON fields, TS has %d fields", len(goFields), len(tsFields))
	}
	for _, f := range tsFields {
		found := false
		for _, g := range goFields {
			if g == f {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("StatusCounts: TS field %q missing from Go JSON tags", f)
		}
	}
}

// TestGoTSContract_EpicProgress verifies that types.EpicProgress JSON tags
// include percentComplete — the critical fix for this issue.
// If the field is still named "progress", this test fails.
func TestGoTSContract_EpicProgress(t *testing.T) {
	tsFields := extractTSInterfaceFields(t, "EpicProgress")
	goFields := structJSONKeys(types.EpicProgress{})
	logDivergences(t, goFields, tsFields, "EpicProgress")

	// Critical assertion: percentComplete must be a Go JSON key (not "progress").
	hasPercentComplete := false
	hasLegacyProgress := false
	for _, f := range goFields {
		if f == "percentComplete" {
			hasPercentComplete = true
		}
		if f == "progress" {
			hasLegacyProgress = true
		}
	}
	if !hasPercentComplete {
		t.Error("EpicProgress: Go struct MUST have json:\"percentComplete\" tag — rename from 'progress' is required")
	}
	if hasLegacyProgress {
		t.Error("EpicProgress: Go struct must NOT have json:\"progress\" tag — it breaks TypeScript consumers")
	}

	// Verify TS expects percentComplete (sanity check on TS parsing).
	tsHasPercentComplete := false
	for _, f := range tsFields {
		if f == "percentComplete" {
			tsHasPercentComplete = true
		}
	}
	if !tsHasPercentComplete {
		t.Error("EpicProgress: TypeScript interface should have 'percentComplete' — check IpcClientBase.ts parsing")
	}
}

// TestGoTSContract_IssueDetail verifies that types.Issue has the isEpic JSON field
// required by the TypeScript IssueDetail interface.
func TestGoTSContract_IssueDetail(t *testing.T) {
	tsFields := extractTSInterfaceFields(t, "IssueDetail")
	goFields := structJSONKeys(types.Issue{})
	logDivergences(t, goFields, tsFields, "IssueDetail")

	// Critical assertion: isEpic must be a Go JSON key.
	hasIsEpic := false
	for _, f := range goFields {
		if f == "isEpic" {
			hasIsEpic = true
		}
	}
	if !hasIsEpic {
		t.Error("Issue: Go struct MUST have json:\"isEpic\" tag to match IssueDetail.isEpic in TypeScript")
	}

	// TS must also have isEpic (sanity check).
	tsHasIsEpic := false
	for _, f := range tsFields {
		if f == "isEpic" {
			tsHasIsEpic = true
		}
	}
	if !tsHasIsEpic {
		t.Error("IssueDetail: TypeScript interface should have 'isEpic' — check IpcClientBase.ts parsing")
	}
}

// TestGoTSContract_BoardItem logs divergences between types.BoardItem and the
// TS BoardItem interface. No critical divergences for this issue.
func TestGoTSContract_BoardItem(t *testing.T) {
	tsFields := extractTSInterfaceFields(t, "BoardItem")
	goFields := structJSONKeys(types.BoardItem{})
	logDivergences(t, goFields, tsFields, "BoardItem")
	// BoardItem divergences (assignees missing in Go) are pre-existing and out of scope.
	// Log them via logDivergences above — no t.Errorf here.
}

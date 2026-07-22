package knowledge

import (
	"strings"
	"testing"
)

func TestParseACsFromPRD(t *testing.T) {
	content := `# PRD

Some intro text.

## Acceptance Criteria

- [ ] The system validates cross-checks on feature inputs
- [x] Coverage map is written to coverage-map-{N}.json
- Pipeline stage exits non-zero on uncovered ACs

## Other Section

This should not be parsed.
`
	acs := ParseACsFromPRD(content)
	if len(acs) != 3 {
		t.Fatalf("expected 3 ACs, got %d: %v", len(acs), acs)
	}
	if acs[0] != "The system validates cross-checks on feature inputs" {
		t.Errorf("unexpected AC[0]: %q", acs[0])
	}
	if acs[1] != "Coverage map is written to coverage-map-{N}.json" {
		t.Errorf("unexpected AC[1]: %q", acs[1])
	}
	if acs[2] != "Pipeline stage exits non-zero on uncovered ACs" {
		t.Errorf("unexpected AC[2]: %q", acs[2])
	}
}

func TestParseDecisionConstraints(t *testing.T) {
	content := `## ADR-001 Use Go IPC for HTTP Routing

**Context:** TypeScript code must not make direct HTTP calls.

**Decision:** Use Go IPC layer for all HTTP requests from TypeScript

**Consequences:** Simpler audit trail.

## Other Section

Not an ADR.
`
	constraints := ParseDecisionConstraints(content)
	if len(constraints) == 0 {
		t.Fatal("expected at least one constraint, got none")
	}
	found := false
	for _, c := range constraints {
		if strings.Contains(c, "Go IPC") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected constraint mentioning 'Go IPC', got: %v", constraints)
	}
}

func TestTokenOverlap(t *testing.T) {
	query := TokenizeText("validates cross checks feature inputs")
	target := TokenizeText("the system validates cross-checks on feature inputs correctly")
	overlap := TokenOverlap(query, target)
	// "validates", "cross", "checks", "feature", "inputs" should all match (5)
	if overlap < 4 {
		t.Errorf("expected overlap >= 4, got %d", overlap)
	}
}

func TestComputeCoverageMap_BasicCoverage(t *testing.T) {
	prd := `# PRD

## Acceptance Criteria

- [ ] The system validates cross-checks on feature inputs
- [ ] Coverage map is written as JSON output file
- [ ] Pipeline exits non-zero when acceptance criteria have no evidence
`
	decisions := `## ADR-001 Placeholder

**Decision:** No HTTP constraints here.
`

	// Test names match AC 1 and AC 2 by token overlap.
	testNames := []string{
		"should validate cross-checks on feature inputs",
		"should write coverage map as JSON output",
	}

	cm := ComputeCoverageMap(3595, prd, decisions, testNames, nil)

	if cm.Issue != 3595 {
		t.Errorf("expected issue 3595, got %d", cm.Issue)
	}
	if len(cm.Criteria) != 3 {
		t.Fatalf("expected 3 criteria, got %d", len(cm.Criteria))
	}

	// AC 0: "validates cross-checks on feature inputs" — covered by test name
	if cm.Criteria[0].Status != "covered" {
		t.Errorf("AC[0] expected covered, got %q (evidence: %v)", cm.Criteria[0].Status, cm.Criteria[0].Evidence)
	}

	// AC 1: "Coverage map is written as JSON output file" — covered by test name
	if cm.Criteria[1].Status != "covered" {
		t.Errorf("AC[1] expected covered, got %q (evidence: %v)", cm.Criteria[1].Status, cm.Criteria[1].Evidence)
	}

	// AC 2: "Pipeline exits non-zero when acceptance criteria have no evidence"
	// No test name or code matches it well enough.
	if cm.Criteria[2].Status != "no_evidence" {
		t.Errorf("AC[2] expected no_evidence, got %q", cm.Criteria[2].Status)
	}
}

func TestComputeCoverageMap_ViolationDetection(t *testing.T) {
	prd := `# PRD

## Acceptance Criteria

- [ ] Widget is created via IPC
`
	decisions := `## ADR-002 Use Go IPC for HTTP requests

**Decision:** Use Go IPC layer for all HTTP requests from TypeScript
`

	// TypeScript file with a direct fetch() call — should trigger violation.
	changedFiles := map[string]string{
		"packages/extension/src/widget.ts": `
export async function createWidget(name: string) {
  const resp = await fetch('/api/widgets', { method: 'POST', body: JSON.stringify({ name }) });
  return resp.json();
}
`,
	}

	cm := ComputeCoverageMap(3595, prd, decisions, nil, changedFiles)

	if len(cm.Violations) == 0 {
		t.Fatal("expected at least one violation for fetch() in TS file, got none")
	}

	v := cm.Violations[0]
	if v.Severity != "warn" {
		t.Errorf("expected severity 'warn', got %q", v.Severity)
	}
	if len(v.ViolatingFiles) == 0 {
		t.Error("expected violating files to be populated")
	}
	found := false
	for _, f := range v.ViolatingFiles {
		if strings.Contains(f, "widget.ts") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected widget.ts in violating files, got: %v", v.ViolatingFiles)
	}
}

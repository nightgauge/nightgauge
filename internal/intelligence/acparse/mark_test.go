package acparse

import (
	"strings"
	"testing"
)

// Issue #1233: the AC completion gate could only be satisfied by a human
// editing the issue body, so a type:docs issue whose author did not pre-tick
// its own criteria deadlocked — retry re-ran the same stage into the same wall.
// Mark is the deterministic write that gives the gate something able to satisfy
// it. Its whole value is that it edits ONLY the box.

const body = `# Title

Some prose with a - [ ] inline mention that is not a list item.

## Acceptance Criteria

- [ ] first criterion
- [x] second criterion, already done
- [ ] third criterion

` + "```" + `
- [ ] fenced, must never be counted or marked
` + "```" + `

## Notes
`

func TestListSharesParseNumbering(t *testing.T) {
	items := List(body)
	if len(items) != 3 {
		t.Fatalf("want 3 top-level items, got %d: %+v", len(items), items)
	}
	// The index List reports and the index Mark writes must name the same
	// criterion as the count Parse gates on, or the gate marks the wrong line.
	if p := Parse(body); p.Total != len(items) {
		t.Fatalf("Parse.Total=%d but List returned %d items — the two scans disagree", p.Total, len(items))
	}
	if items[0].Text != "first criterion" || items[2].Text != "third criterion" {
		t.Errorf("text extraction wrong: %q / %q", items[0].Text, items[2].Text)
	}
	if !items[1].Checked {
		t.Error("item 2 is '- [x]' and must report Checked")
	}
	for _, it := range items {
		if strings.Contains(it.Text, "fenced") || strings.Contains(it.Text, "inline mention") {
			t.Errorf("fenced or prose content leaked into items: %q", it.Text)
		}
	}
}

func TestMarkEditsOnlyTheBox(t *testing.T) {
	got, res := Mark(body, []int{1})

	if len(res.Changed) != 1 || res.Changed[0] != 1 {
		t.Fatalf("want index 1 changed, got %+v", res)
	}
	if !strings.Contains(got, "- [x] first criterion") {
		t.Error("criterion 1 was not ticked")
	}
	// Everything else byte-identical. An issue body is a human artefact; a verb
	// that reflows it while ticking a box will not be trusted with write access.
	wantUntouched := strings.Replace(body, "- [ ] first criterion", "- [x] first criterion", 1)
	if got != wantUntouched {
		t.Errorf("Mark changed more than the box.\n--- got ---\n%q\n--- want ---\n%q", got, wantUntouched)
	}
}

func TestMarkNeverTouchesFencedOrProseCheckboxes(t *testing.T) {
	got, _ := Mark(body, []int{1, 2, 3})
	if !strings.Contains(got, "- [ ] fenced, must never be counted or marked") {
		t.Error("a fenced checkbox was rewritten — fence handling diverged from Parse")
	}
	if !strings.Contains(got, "prose with a - [ ] inline mention") {
		t.Error("an inline (non-anchored) checkbox was rewritten")
	}
}

func TestMarkIsIdempotent(t *testing.T) {
	once, _ := Mark(body, []int{1})
	twice, res := Mark(once, []int{1})

	if once != twice {
		t.Error("marking an already-checked index changed the body")
	}
	// "Already checked" is a success, and must not be reported as a change —
	// a caller counting changes to decide whether to push an edit would push
	// an empty one forever.
	if len(res.Changed) != 0 || len(res.AlreadyChecked) != 1 {
		t.Errorf("want 0 changed / 1 already-checked, got %+v", res)
	}
}

func TestMarkReportsUnknownIndicesInsteadOfGuessing(t *testing.T) {
	got, res := Mark(body, []int{99})

	if got != body {
		t.Error("an unknown index modified the body")
	}
	if len(res.NotFound) != 1 || res.NotFound[0] != 99 {
		t.Fatalf("want index 99 reported not-found, got %+v", res)
	}
	// Silently folding a miscount into success is how a gate comes to believe a
	// criterion nobody marked.
	if len(res.Changed) != 0 {
		t.Errorf("not-found must not be reported as changed: %+v", res)
	}
}

func TestMarkPreservesCRLF(t *testing.T) {
	crlf := "- [ ] one\r\n- [ ] two\r\n"
	got, res := Mark(crlf, []int{2})

	if len(res.Changed) != 1 {
		t.Fatalf("want one change, got %+v", res)
	}
	if got != "- [ ] one\r\n- [x] two\r\n" {
		t.Errorf("CRLF terminators not preserved: %q", got)
	}
}

func TestMarkPreservesBulletStyleAndIndent(t *testing.T) {
	src := "  * [ ] star bullet, indented\n+ [ ] plus bullet\n"
	got, _ := Mark(src, []int{1, 2})

	if !strings.Contains(got, "  * [x] star bullet, indented") {
		t.Errorf("indent or bullet style lost: %q", got)
	}
	if !strings.Contains(got, "+ [x] plus bullet") {
		t.Errorf("plus bullet not handled: %q", got)
	}
}

func TestMarkDedupesRepeatedIndices(t *testing.T) {
	_, res := Mark(body, []int{1, 1, 1})
	if len(res.Changed) != 1 {
		t.Errorf("a repeated index is one request, not three: %+v", res)
	}
}

// The gate's whole purpose is that reaching a phase is not evidence. A verb
// that ticks everything on request is fine; a CALLER that always requests
// everything is the rubber stamp. This pins the verb's half: Mark must never
// tick a box that was not asked for.
func TestMarkNeverTicksUnrequestedBoxes(t *testing.T) {
	got, _ := Mark(body, []int{1})
	if strings.Contains(got, "- [x] third criterion") {
		t.Error("Mark ticked a criterion that was not requested")
	}
	if p := Parse(got); p.Unchecked != 1 {
		t.Errorf("want exactly one criterion still unchecked, got %d", p.Unchecked)
	}
}

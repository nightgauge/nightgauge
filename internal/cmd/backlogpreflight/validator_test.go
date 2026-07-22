package backlogpreflight

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/pkg/types"
)

// --- stubs ---

type stubIssueGetter struct {
	issues map[int]*types.Issue
	err    error
}

func (s *stubIssueGetter) GetIssue(_ context.Context, _, _ string, number int) (*types.Issue, error) {
	if s.err != nil {
		return nil, s.err
	}
	if issue, ok := s.issues[number]; ok {
		return issue, nil
	}
	return nil, fmt.Errorf("issue #%d not found", number)
}

func item(number int, title string, opts ...func(*types.BoardItem)) types.BoardItem {
	bi := types.BoardItem{Number: number, Title: title}
	for _, o := range opts {
		o(&bi)
	}
	return bi
}

func withLabels(labels ...string) func(*types.BoardItem) {
	return func(bi *types.BoardItem) { bi.Labels = labels }
}

func withSize(s types.Size) func(*types.BoardItem) {
	return func(bi *types.BoardItem) { bi.Size = s }
}

func withPriority(p types.Priority) func(*types.BoardItem) {
	return func(bi *types.BoardItem) { bi.Priority = p }
}

func withBlockedBy(numbers ...int) func(*types.BoardItem) {
	return func(bi *types.BoardItem) {
		for _, n := range numbers {
			bi.BlockedBy = append(bi.BlockedBy, types.BlockingRef{Number: n, State: "OPEN"})
		}
	}
}

func newValidator() *Validator {
	return New(nil, &stubIssueGetter{issues: map[int]*types.Issue{}}, "owner", "repo")
}

// --- CheckLabels ---

func TestCheckLabels_Clean(t *testing.T) {
	v := newValidator()
	items := []types.BoardItem{
		item(1, "feat A", withLabels("type:feature")),
		item(2, "fix B", withLabels("type:bug", "priority:high")),
	}
	findings := v.CheckLabels(items)
	if len(findings) != 0 {
		t.Fatalf("expected 0 findings, got %d: %v", len(findings), findings)
	}
}

func TestCheckLabels_Missing(t *testing.T) {
	v := newValidator()
	items := []types.BoardItem{
		item(1, "no label"),
		item(2, "has label", withLabels("type:chore")),
		item(3, "wrong label", withLabels("priority:high")),
	}
	findings := v.CheckLabels(items)
	if len(findings) != 2 {
		t.Fatalf("expected 2 findings, got %d", len(findings))
	}
	if findings[0].IssueNumber != 1 || findings[1].IssueNumber != 3 {
		t.Errorf("unexpected issue numbers: %d, %d", findings[0].IssueNumber, findings[1].IssueNumber)
	}
	for _, f := range findings {
		if f.FindingType != FindingTypeMissingTypeLabel {
			t.Errorf("expected finding type %s, got %s", FindingTypeMissingTypeLabel, f.FindingType)
		}
	}
}

func TestCheckLabels_AllValidTypes(t *testing.T) {
	v := newValidator()
	validTypes := []string{"type:feature", "type:bug", "type:docs", "type:refactor", "type:chore", "type:epic", "type:spike"}
	for _, l := range validTypes {
		items := []types.BoardItem{item(1, "test", withLabels(l))}
		if findings := v.CheckLabels(items); len(findings) != 0 {
			t.Errorf("label %q should be valid but got finding: %v", l, findings[0])
		}
	}
}

// --- CheckBoardFields ---

func TestCheckBoardFields_Clean(t *testing.T) {
	v := newValidator()
	items := []types.BoardItem{
		item(1, "ok", withSize(types.SizeM), withPriority(types.PriorityP1)),
	}
	if findings := v.CheckBoardFields(items); len(findings) != 0 {
		t.Fatalf("expected 0 findings, got %d", len(findings))
	}
}

func TestCheckBoardFields_MissingSize(t *testing.T) {
	v := newValidator()
	items := []types.BoardItem{
		item(1, "no size", withPriority(types.PriorityP2)),
	}
	findings := v.CheckBoardFields(items)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	if findings[0].FindingType != FindingTypeMissingSize {
		t.Errorf("expected %s, got %s", FindingTypeMissingSize, findings[0].FindingType)
	}
}

func TestCheckBoardFields_MissingPriority(t *testing.T) {
	v := newValidator()
	items := []types.BoardItem{
		item(1, "no prio", withSize(types.SizeS)),
	}
	findings := v.CheckBoardFields(items)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	if findings[0].FindingType != FindingTypeMissingPriority {
		t.Errorf("expected %s, got %s", FindingTypeMissingPriority, findings[0].FindingType)
	}
}

func TestCheckBoardFields_BothMissing(t *testing.T) {
	v := newValidator()
	items := []types.BoardItem{item(1, "bare")}
	findings := v.CheckBoardFields(items)
	if len(findings) != 2 {
		t.Fatalf("expected 2 findings (size + priority), got %d", len(findings))
	}
}

// --- CheckAcceptanceCriteria ---

func makeIssueGetter(issues map[int]*types.Issue) *stubIssueGetter {
	return &stubIssueGetter{issues: issues}
}

func TestCheckAcceptanceCriteria_GoodBody(t *testing.T) {
	body := "This feature adds the ability to create a new widget with full CRUD operations.\n\n## Acceptance Criteria\n- [ ] Widget can be created via the API\n- [ ] Widget can be deleted via the API\n- [ ] Widget creation requires authentication\n"
	v := New(nil, makeIssueGetter(map[int]*types.Issue{
		1: {Number: 1, Title: "good issue", Body: body},
	}), "owner", "repo")
	items := []types.BoardItem{item(1, "good issue")}
	findings := v.CheckAcceptanceCriteria(context.Background(), items)
	if len(findings) != 0 {
		t.Fatalf("expected 0 findings, got %d: %v", len(findings), findings)
	}
}

func TestCheckAcceptanceCriteria_ShortBody(t *testing.T) {
	v := New(nil, makeIssueGetter(map[int]*types.Issue{
		1: {Number: 1, Title: "short", Body: "too short"},
	}), "owner", "repo")
	items := []types.BoardItem{item(1, "short")}
	findings := v.CheckAcceptanceCriteria(context.Background(), items)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	if findings[0].FindingType != FindingTypeWeakAcceptanceCriteria {
		t.Errorf("expected weak AC finding, got %s", findings[0].FindingType)
	}
}

func TestCheckAcceptanceCriteria_NoCheckboxes(t *testing.T) {
	body := strings.Repeat("This is a description that is definitely more than one hundred characters long for testing purposes.", 2)
	v := New(nil, makeIssueGetter(map[int]*types.Issue{
		1: {Number: 1, Title: "no boxes", Body: body},
	}), "owner", "repo")
	items := []types.BoardItem{item(1, "no boxes")}
	findings := v.CheckAcceptanceCriteria(context.Background(), items)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding (no checkboxes), got %d", len(findings))
	}
}

func TestCheckAcceptanceCriteria_OneCheckbox(t *testing.T) {
	body := strings.Repeat("x", 100) + "\n- [ ] only one checkbox\n"
	v := New(nil, makeIssueGetter(map[int]*types.Issue{
		1: {Number: 1, Title: "one box", Body: body},
	}), "owner", "repo")
	items := []types.BoardItem{item(1, "one box")}
	findings := v.CheckAcceptanceCriteria(context.Background(), items)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding (1 checkbox < 2), got %d", len(findings))
	}
}

func TestCheckAcceptanceCriteria_FetchError(t *testing.T) {
	v := New(nil, &stubIssueGetter{err: fmt.Errorf("api error")}, "owner", "repo")
	items := []types.BoardItem{item(1, "fetch fail")}
	findings := v.CheckAcceptanceCriteria(context.Background(), items)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding for fetch error, got %d", len(findings))
	}
}

// --- CheckDependencyCycles ---

func TestCheckDependencyCycles_NoCycle(t *testing.T) {
	v := newValidator()
	// A → B (A is blocked by B, linear chain)
	items := []types.BoardItem{
		item(1, "A", withBlockedBy(2)),
		item(2, "B"),
	}
	if findings := v.CheckDependencyCycles(items); len(findings) != 0 {
		t.Fatalf("expected 0 cycle findings, got %d", len(findings))
	}
}

func TestCheckDependencyCycles_SimpleCycle(t *testing.T) {
	v := newValidator()
	// A blocked by B, B blocked by A → cycle
	items := []types.BoardItem{
		item(1, "A", withBlockedBy(2)),
		item(2, "B", withBlockedBy(1)),
	}
	findings := v.CheckDependencyCycles(items)
	if len(findings) == 0 {
		t.Fatal("expected at least 1 cycle finding, got 0")
	}
	if findings[0].FindingType != FindingTypeDependencyCycle {
		t.Errorf("expected dependency_cycle, got %s", findings[0].FindingType)
	}
}

func TestCheckDependencyCycles_ThreeNodeCycle(t *testing.T) {
	v := newValidator()
	// A → B → C → A
	items := []types.BoardItem{
		item(1, "A", withBlockedBy(2)),
		item(2, "B", withBlockedBy(3)),
		item(3, "C", withBlockedBy(1)),
	}
	findings := v.CheckDependencyCycles(items)
	if len(findings) == 0 {
		t.Fatal("expected cycle finding for 3-node cycle")
	}
}

func TestCheckDependencyCycles_OutOfSetBlocker(t *testing.T) {
	v := newValidator()
	// Item 1 blocked by item 99 which is NOT in the item set — should not be treated as a cycle
	items := []types.BoardItem{
		item(1, "A", withBlockedBy(99)),
	}
	if findings := v.CheckDependencyCycles(items); len(findings) != 0 {
		t.Fatalf("out-of-set blocker should not create a cycle finding, got %d", len(findings))
	}
}

// --- CheckGreenfield ---

func TestCheckGreenfield_CompleteProject(t *testing.T) {
	dir := t.TempDir()
	// Create all expected files
	os.MkdirAll(filepath.Join(dir, ".nightgauge"), 0755)
	os.WriteFile(filepath.Join(dir, ".nightgauge", "complexity-model.yaml"), []byte("v: 1"), 0644)
	os.MkdirAll(filepath.Join(dir, "docs"), 0755)
	os.WriteFile(filepath.Join(dir, "docs", "CODE_STANDARDS.md"), []byte("# standards"), 0644)
	os.WriteFile(filepath.Join(dir, "docs", "SECURITY.md"), []byte("# security"), 0644)

	v := newValidator()
	findings := v.CheckGreenfield(dir)
	if len(findings) != 0 {
		t.Fatalf("expected 0 greenfield findings, got %d: %v", len(findings), findings)
	}
}

func TestCheckGreenfield_MissingCodeStandards(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".nightgauge"), 0755)
	os.WriteFile(filepath.Join(dir, ".nightgauge", "complexity-model.yaml"), []byte("v: 1"), 0644)
	os.MkdirAll(filepath.Join(dir, "docs"), 0755)
	os.WriteFile(filepath.Join(dir, "docs", "SECURITY.md"), []byte("# security"), 0644)
	// No CODE_STANDARDS.md

	v := newValidator()
	findings := v.CheckGreenfield(dir)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding (missing CODE_STANDARDS.md), got %d", len(findings))
	}
	if findings[0].FindingType != FindingTypeGreenfieldWarning {
		t.Errorf("expected greenfield_warning, got %s", findings[0].FindingType)
	}
}

func TestCheckGreenfield_EmptyProject(t *testing.T) {
	dir := t.TempDir()
	v := newValidator()
	findings := v.CheckGreenfield(dir)
	// Expect findings for: complexity-model.yaml, docs/, CODE_STANDARDS.md, SECURITY*.md
	if len(findings) < 4 {
		t.Fatalf("expected at least 4 greenfield findings for empty dir, got %d", len(findings))
	}
}

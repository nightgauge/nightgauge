package preflight

import (
	"context"
	"path/filepath"
	"testing"
)

func runShellState(t *testing.T, root string) *SkillShellStateResult {
	t.Helper()
	res, err := RunSkillShellStateCheck(context.Background(), SkillShellStateOptions{Root: root})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return res
}

func fence(body string) string { return "```bash\n" + body + "\n```\n" }

// The #1927 shape: a later phase records a metric against an issue number
// that only an earlier phase assigned.
func TestSkillShellState_ReadWithoutDerivationFails(t *testing.T) {
	root := t.TempDir()
	writeSkillFile(t, root, "nightgauge-feature-validate/_includes/build-and-tests.md",
		"## Record\n\n"+fence(`nightgauge gate record-metric --issue "$ISSUE_NUMBER" --gate lint --result pass`))

	res := runShellState(t, root)
	if len(res.Findings) != 1 {
		t.Fatalf("findings = %d, want 1: %+v", len(res.Findings), res.Findings)
	}
	f := res.Findings[0]
	if f.Check != CheckUnderivedIdentifier || f.Identifier != "ISSUE_NUMBER" || f.Line != 4 {
		t.Errorf("finding = %+v, want underived_identifier ISSUE_NUMBER at line 4", f)
	}
	if f.File != "skills/nightgauge-feature-validate/_includes/build-and-tests.md" {
		t.Errorf("file = %q", f.File)
	}
}

func TestSkillShellState_DerivedBeforeReadPasses(t *testing.T) {
	root := t.TempDir()
	writeSkillFile(t, root, "nightgauge-pr-create/SKILL.md", fence(
		`ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
REPO="${NIGHTGAUGE_REPO:-$(nightgauge git repo-slug)}"
BRANCH=$(git branch --show-current)
echo "$ISSUE_NUMBER $REPO ${BRANCH}"`))
	if res := runShellState(t, root); len(res.Findings) != 0 {
		t.Errorf("findings = %+v, want none", res.Findings)
	}
}

// Every block stands alone: a derivation in the previous block does not carry.
func TestSkillShellState_DerivationDoesNotCarryAcrossBlocks(t *testing.T) {
	root := t.TempDir()
	writeSkillFile(t, root, "nightgauge-pr-merge/SKILL.md",
		fence(`BRANCH=$(git branch --show-current)`)+"\nlater\n\n"+fence(`git push origin "$BRANCH"`))
	res := runShellState(t, root)
	if len(res.Findings) != 1 || res.Findings[0].Identifier != "BRANCH" || res.Findings[0].Line != 8 {
		t.Errorf("findings = %+v, want one BRANCH finding at line 8", res.Findings)
	}
}

func TestSkillShellState_ReadBeforeAssignmentFails(t *testing.T) {
	root := t.TempDir()
	writeSkillFile(t, root, "nightgauge-feature-dev/SKILL.md", fence(
		`echo "$REPO"
REPO=$(nightgauge git repo-slug)`))
	res := runShellState(t, root)
	if len(res.Findings) != 1 || res.Findings[0].Identifier != "REPO" {
		t.Errorf("findings = %+v, want one REPO finding", res.Findings)
	}
}

// ISSUE_NUMBER="${ISSUE_NUMBER:-…}" reads the inherited value; it is the
// defect, not a derivation.
func TestSkillShellState_SelfReferencingAssignmentIsNotADerivation(t *testing.T) {
	root := t.TempDir()
	writeSkillFile(t, root, "nightgauge-issue-pickup/SKILL.md", fence(
		`ISSUE_NUMBER="${ISSUE_NUMBER:-42}"
echo "$ISSUE_NUMBER"`))
	res := runShellState(t, root)
	if len(res.Findings) != 1 || res.Findings[0].Check != CheckUnderivedIdentifier {
		t.Errorf("findings = %+v, want one underived_identifier", res.Findings)
	}
}

func TestSkillShellState_BlankFallbackFails(t *testing.T) {
	root := t.TempDir()
	writeSkillFile(t, root, "nightgauge-issue-pickup/SKILL.md", fence(
		`BRANCH_NAME=$(git branch --show-current)
echo "${BRANCH_NAME:-}" "${BRANCH_NAME-}"`))
	res := runShellState(t, root)
	if len(res.Findings) != 1 || res.Findings[0].Check != CheckBlankFallback || res.Findings[0].Identifier != "BRANCH_NAME" {
		t.Errorf("findings = %+v, want one blank_fallback on BRANCH_NAME", res.Findings)
	}
}

// $BRANCH_NAME is not a read of $BRANCH, and $REPO_ROOT is not a read of $REPO.
func TestSkillShellState_LongerNamesAreDistinct(t *testing.T) {
	root := t.TempDir()
	writeSkillFile(t, root, "nightgauge-feature-dev/SKILL.md", fence(
		`BRANCH_NAME=$(git branch --show-current)
cd "$REPO_ROOT" && echo "$BRANCH_NAME ${REPO_NAME}"`))
	if res := runShellState(t, root); len(res.Findings) != 0 {
		t.Errorf("findings = %+v, want none", res.Findings)
	}
}

func TestSkillShellState_ReadForLoopAndExportDerive(t *testing.T) {
	root := t.TempDir()
	writeSkillFile(t, root, "nightgauge-pr-merge/SKILL.md",
		fence("read -r OWNER REPO <<< \"a b\"\necho \"$REPO\"")+
			fence("for BRANCH in a b; do echo \"$BRANCH\"; done")+
			fence("export ISSUE_NUMBER=7\necho \"$ISSUE_NUMBER\""))
	if res := runShellState(t, root); len(res.Findings) != 0 {
		t.Errorf("findings = %+v, want none", res.Findings)
	}
}

// Only executed text counts: comments, prose and non-shell fences do not run.
func TestSkillShellState_CommentsProseAndOtherFencesIgnored(t *testing.T) {
	root := t.TempDir()
	writeSkillFile(t, root, "nightgauge-feature-planning/SKILL.md",
		"Prose mentions $ISSUE_NUMBER.\n\n"+
			fence("# uses $ISSUE_NUMBER later\necho ok")+
			"```text\nissue $ISSUE_NUMBER on $BRANCH\n```\n"+
			"```json\n{\"repo\": \"$REPO\"}\n```\n")
	if res := runShellState(t, root); len(res.Findings) != 0 {
		t.Errorf("findings = %+v, want none", res.Findings)
	}
}

func TestSkillShellState_IndentedFenceIsScanned(t *testing.T) {
	root := t.TempDir()
	writeSkillFile(t, root, "nightgauge-pr-create/SKILL.md",
		"1. Push:\n\n   ```bash\n   git push -u origin \"$BRANCH_NAME\"\n   ```\n")
	res := runShellState(t, root)
	if len(res.Findings) != 1 || res.Findings[0].Identifier != "BRANCH_NAME" {
		t.Errorf("findings = %+v, want one BRANCH_NAME finding", res.Findings)
	}
}

// Stage skills and _shared run under the orchestrator; other skills are out
// of scope because nothing exports NIGHTGAUGE_* into them.
func TestSkillShellState_ScopeIsStageSkillsAndShared(t *testing.T) {
	root := t.TempDir()
	bad := fence(`echo "$ISSUE_NUMBER"`)
	writeSkillFile(t, root, "nightgauge-retro/SKILL.md", bad)
	writeSkillFile(t, root, "_shared/CONTEXT_LOADING.md", bad)
	writeSkillFile(t, root, "nightgauge-spike-materialize/SKILL.md", bad)
	res := runShellState(t, root)
	got := map[string]bool{}
	for _, f := range res.Findings {
		got[f.File] = true
	}
	if !got["skills/_shared/CONTEXT_LOADING.md"] || !got["skills/nightgauge-spike-materialize/SKILL.md"] {
		t.Errorf("findings = %+v, want _shared and spike-materialize flagged", res.Findings)
	}
	if got["skills/nightgauge-retro/SKILL.md"] {
		t.Error("nightgauge-retro is not a stage skill and must not be scanned")
	}
	if res.FilesChecked != 2 {
		t.Errorf("files_checked = %d, want 2", res.FilesChecked)
	}
}

func TestSkillShellState_MissingRootErrors(t *testing.T) {
	_, err := RunSkillShellStateCheck(context.Background(),
		SkillShellStateOptions{Root: filepath.Join(t.TempDir(), "absent")})
	if err == nil {
		t.Fatal("want error for a missing root")
	}
}

// The gate is only worth having if the tree it guards is clean. CI runs this
// package ungated, so a regression anywhere in a stage skill fails here.
func TestSkillShellState_RealTreeIsClean(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	res := runShellState(t, root)
	for _, f := range res.Findings {
		t.Errorf("%s:%d [%s] $%s: %s", f.File, f.Line, f.Check, f.Identifier, f.Match)
	}
	if res.FilesChecked < 40 {
		t.Errorf("files_checked = %d; the scan should cover every stage skill's SKILL.md, _includes/, _profiles/ and _overlays/ plus _shared/", res.FilesChecked)
	}
}

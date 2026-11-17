package preflight

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/models"
)

func writeOverlayFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runOverlays(t *testing.T, root string) *SkillOverlaysResult {
	t.Helper()
	off := false
	r, err := RunSkillOverlaysCheck(context.Background(), SkillOverlaysOptions{
		SkillsRoot: root, ThinkingDisabledOverride: &off,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func findingsFor(r *SkillOverlaysResult, rule string) []SkillOverlayFinding {
	var out []SkillOverlayFinding
	for _, f := range r.Findings {
		if f.Rule == rule {
			out = append(out, f)
		}
	}
	return out
}

func TestSkillOverlays_KeyValidity(t *testing.T) {
	cases := []struct {
		name, file, want string
		bad              bool
	}{
		{"band", "_shared/_overlays/opus.md", "tier band", true},
		{"deprecated", "_shared/_overlays/claude-opus-4-7.md", "deprecated", true},
		{"typo", "_shared/_overlays/anthropc.md", "typo", true},
		{"provider", "_shared/_overlays/xai.md", "", false},
		{"live id", "_shared/_overlays/claude-opus-5-5.md", "", false},
		{"host ok", "_shared/_overlays/hosts/opencode.md", "", false},
		{"host bad", "_shared/_overlays/hosts/vscodium.md", "not a known adapter", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			writeOverlayFile(t, root, c.file, "Keep it short.\n")
			r := runOverlays(t, root)
			got := findingsFor(r, RuleOverlayKey)
			if !c.bad {
				if len(got) != 0 || r.Blocking() {
					t.Fatalf("want clean, got %+v", r.Findings)
				}
				return
			}
			if len(got) != 1 || !strings.Contains(got[0].Message, c.want) || !r.Blocking() {
				t.Fatalf("want one %q finding, got %+v", c.want, r.Findings)
			}
		})
	}
}

func TestSkillOverlays_AcceptedKeysAreProvidersAndLiveIDs(t *testing.T) {
	want := map[string]bool{}
	for _, m := range models.All() {
		want[m.Provider] = true
		if !m.Deprecated {
			want[m.ID] = true
		}
	}
	got := overlayModelKeys()
	if len(got) != len(want) {
		t.Fatalf("key set size %d, want %d", len(got), len(want))
	}
	for k := range want {
		if !got[k] {
			t.Fatalf("missing key %q", k)
		}
	}
	for _, m := range models.All() {
		for _, tier := range m.Tiers {
			if got[tier] && tier != m.Provider {
				t.Fatalf("band %q accepted as a key", tier)
			}
		}
	}
}

func TestSkillOverlays_Budget(t *testing.T) {
	root := t.TempDir()
	writeOverlayFile(t, root, "_shared/_overlays/xai.md", strings.Repeat("a", OverlayBudgetBytes))
	if r := runOverlays(t, root); r.Blocking() {
		t.Fatalf("at budget should pass: %+v", r.Findings)
	}
	writeOverlayFile(t, root, "_shared/_overlays/xai.md", strings.Repeat("a", OverlayBudgetBytes+1))
	got := findingsFor(runOverlays(t, root), RuleOverlayBudget)
	if len(got) != 1 || !strings.Contains(got[0].Message, "4609 bytes, budget is 4608") {
		t.Fatalf("got %+v", got)
	}
}

func TestSkillOverlays_RegistryFact(t *testing.T) {
	root := t.TempDir()
	// grok-4.6 declares thinking_default: on.
	writeOverlayFile(t, root, "_shared/_overlays/grok-4.6.md", "Be terse.\nThinking is on by default.\n")
	got := findingsFor(runOverlays(t, root), RuleOverlayRegistryFact)
	if len(got) != 1 || got[0].Line != 2 {
		t.Fatalf("got %+v", got)
	}
	// Prose that only mentions thinking is not a restatement.
	writeOverlayFile(t, root, "_shared/_overlays/grok-4.6.md", "Think once, thoroughly.\n")
	if got := findingsFor(runOverlays(t, root), RuleOverlayRegistryFact); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestSkillOverlays_PurityBlocksAndHonoursExempt(t *testing.T) {
	root := t.TempDir()
	skill := "nightgauge-feature-dev/SKILL.md"
	writeOverlayFile(t, root, skill, "# Dev\n\nOn Opus 5, skip the review.\n\n```bash\necho Opus\n```\n")
	r := runOverlays(t, root)
	got := findingsFor(r, RuleBaseSkillPurity)
	if len(got) != 1 || got[0].Severity != OverlaySeverityFail || got[0].Line != 3 || !r.Blocking() {
		t.Fatalf("want one blocking finding on line 3, got %+v", r.Findings)
	}

	writeOverlayFile(t, root, skill, "# Dev\n<!-- overlay-exempt: names the tier band in config docs -->\nOn Opus 5, skip the review.\n")
	r = runOverlays(t, root)
	if len(findingsFor(r, RuleBaseSkillPurity)) != 0 || len(r.Exempt) != 1 || r.Exempt[0].Line != 3 {
		t.Fatalf("want exempt census, got findings=%+v exempt=%+v", r.Findings, r.Exempt)
	}

	writeOverlayFile(t, root, skill, "# Dev\n<!-- overlay-exempt: -->\nplain line\n")
	if got := findingsFor(runOverlays(t, root), RuleBaseSkillPurity); len(got) != 1 ||
		!strings.Contains(got[0].Message, "no reason") {
		t.Fatalf("empty reason should be a finding, got %+v", got)
	}
}

func TestSkillOverlays_OverrideIsWarnOnly(t *testing.T) {
	root := t.TempDir()
	writeOverlayFile(t, root, "nightgauge-pr-merge/_overlays/xai.SKILL.md", strings.Repeat("x", OverlayBudgetBytes*2))
	r := runOverlays(t, root)
	if r.Blocking() || len(r.Overrides) != 1 || len(findingsFor(r, RuleOverlayOverride)) != 1 {
		t.Fatalf("got %+v overrides=%v", r.Findings, r.Overrides)
	}
}

func TestSkillOverlays_ThinkingEffortDelegation(t *testing.T) {
	root := t.TempDir()
	on := true
	r, err := RunSkillOverlaysCheck(context.Background(), SkillOverlaysOptions{
		SkillsRoot:               root,
		ThinkingDisabledOverride: &on,
		Efforts:                  map[string]ModelEffort{"stage_efforts.feature-dev": {Model: "claude-sonnet-5-5", Effort: "max"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := findingsFor(r, RuleThinkingEffort)
	if len(got) != 1 || got[0].File != ".nightgauge/config.yaml" || !r.Blocking() {
		t.Fatalf("got %+v", r.Findings)
	}
}

func TestSkillOverlays_EveryFindingIsActionable(t *testing.T) {
	root := t.TempDir()
	writeOverlayFile(t, root, "_shared/_overlays/opus.md", strings.Repeat("a", OverlayBudgetBytes+1))
	writeOverlayFile(t, root, "nightgauge-feature-dev/SKILL.md", "Sonnet only.\n")
	r := runOverlays(t, root)
	if len(r.Findings) < 3 {
		t.Fatalf("want >=3 findings, got %+v", r.Findings)
	}
	for _, f := range r.Findings {
		if f.File == "" || f.Rule == "" || f.Message == "" || f.Fix == "" {
			t.Fatalf("incomplete finding %+v", f)
		}
	}
}

func TestSkillOverlays_MissingRootIsError(t *testing.T) {
	if _, err := RunSkillOverlaysCheck(context.Background(), SkillOverlaysOptions{SkillsRoot: filepath.Join(t.TempDir(), "nope")}); err == nil {
		t.Fatal("want error")
	}
}

func TestSkillOverlays_WorkingTreeIsClean(t *testing.T) {
	r := runOverlays(t, filepath.Join(repoRoot(t), "skills"))
	if r.FilesChecked == 0 {
		t.Fatal("no files checked")
	}
	for _, f := range r.Findings {
		if f.Severity == OverlaySeverityFail {
			t.Errorf("%s:%d [%s] %s — %s", f.File, f.Line, f.Rule, f.Message, f.Fix)
		}
	}
}

func TestSkillOverlays_MoreFactAndPurityPatterns(t *testing.T) {
	root := t.TempDir()
	// claude-opus-5-5: effort_default low, thinking cannot be disabled.
	writeOverlayFile(t, root, "_shared/_overlays/claude-opus-5-5.md",
		"The default effort is low.\nThinking cannot be disabled.\nUse low effort for lookups.\n")
	// Provider key covers its live models: grok-4.6 effort_default high.
	writeOverlayFile(t, root, "_shared/_overlays/xai.md", "Effort defaults to high.\n")
	writeOverlayFile(t, root, "nightgauge-pr-merge/SKILL.md",
		"Route to claude-sonnet-5-5 here.\nSmaller models skip this. <!-- overlay-exempt: documents the routing table -->\n")
	r := runOverlays(t, root)
	if got := findingsFor(r, RuleOverlayRegistryFact); len(got) != 3 {
		t.Fatalf("want 3 fact findings, got %+v", got)
	}
	got := findingsFor(r, RuleBaseSkillPurity)
	if len(got) != 1 || got[0].Line != 1 || len(r.Exempt) != 1 || r.Exempt[0].Line != 2 {
		t.Fatalf("purity: %+v exempt=%+v", got, r.Exempt)
	}
}

func TestSkillOverlays_PurityScope(t *testing.T) {
	cases := []struct {
		name, file, body string
		want             int
	}{
		{"non-stage skill", "nightgauge-test-gen/SKILL.md", "Run it on Sonnet.\n", 1},
		{"include", "nightgauge-feature-dev/_includes/impl.md", "Run it on Haiku.\n", 1},
		{"shared block", "_shared/AUTO_FIX_LOOP.md", "Run it on Opus.\n", 1},
		{"overlay not scanned", "_shared/_overlays/anthropic.md", "Run it on Sonnet.\n", 0},
		{"skill overlay not scanned", "nightgauge-test-gen/_overlays/anthropic.md", "Run it on Sonnet.\n", 0},
		{"frontmatter skipped", "nightgauge-test-gen/SKILL.md", "---\nmodel: \"haiku\"\ndescription: runs on Sonnet\n---\n# X\n", 0},
		{"after frontmatter", "nightgauge-test-gen/SKILL.md", "---\nname: x\n---\nruns on Sonnet\n", 1},
		{"unterminated frontmatter", "nightgauge-test-gen/SKILL.md", "---\nRun it on Sonnet.\n", 1},
		{"include thematic break", "nightgauge-test-gen/_includes/a.md", "---\nRun it on Sonnet.\n---\n", 1},
		{"non-md include", "nightgauge-test-gen/_includes/notes.txt", "Run it on Sonnet.\n", 0},
		{"nested shared dir", "_shared/sub/x.md", "Run it on Sonnet.\n", 0},
		{"quoted prose pin", "nightgauge-test-gen/SKILL.md", "Use `Task` with `model: \"haiku\"`.\n", 1},
		{"escaped pin", "nightgauge-test-gen/SKILL.md", "Use Task with model: \\\"sonnet\\\".\n", 1},
		{"fenced pin", "nightgauge-test-gen/SKILL.md", "```\nTask (model: \"haiku\")\n```\n", 0},
		{"unquoted prose pin", "nightgauge-test-gen/SKILL.md", "Running (model: sonnet)\n", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			writeOverlayFile(t, root, c.file, c.body)
			if got := findingsFor(runOverlays(t, root), RuleBaseSkillPurity); len(got) != c.want {
				t.Fatalf("want %d purity findings, got %+v", c.want, got)
			}
		})
	}
}

func TestSkillOverlays_SharedSkillScannedOnce(t *testing.T) {
	root := t.TempDir()
	writeOverlayFile(t, root, "_shared/SKILL.md", "Run it on Sonnet.\n")
	r := runOverlays(t, root)
	if got := findingsFor(r, RuleBaseSkillPurity); len(got) != 1 || r.FilesChecked != 1 {
		t.Fatalf("want one finding from one file, got %+v checked=%d", got, r.FilesChecked)
	}
}

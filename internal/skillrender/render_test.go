package skillrender

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
)

// ─── Migrated regression tests ───────────────────────────────────────────────
//
// These came over verbatim in substance from internal/execution/skill_test.go
// when the primitives moved here (#78). They are the guard on AC "existing
// include-expansion behavior is preserved exactly": the implementation was
// MOVED, not rewritten, and these prove the observable behavior came with it.

func TestStageSkillDirs(t *testing.T) {
	for _, stage := range []string{
		"issue-pickup", "feature-planning", "feature-dev",
		"feature-validate", "pr-create", "pr-merge",
	} {
		dir, ok := StageSkillDirs[stage]
		if !ok || dir == "" {
			t.Errorf("missing skill dir for stage %q", stage)
		}
		if !strings.HasPrefix(dir, "nightgauge-") {
			t.Errorf("skill dir %q should start with 'nightgauge-'", dir)
		}
	}
}

func TestFrontmatterParsedVerbatim(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "nightgauge-feature-dev", `---
name: test-skill
allowed-tools: Read Edit Bash AskUserQuestion
programmatic-tools: TodoWrite
---

# Test Skill

Do the thing.
`)
	res := mustRender(t, Options{Stage: "feature-dev", SkillsRoots: []string{root}})

	if res.SkillName != "test-skill" {
		t.Errorf("SkillName = %q, want test-skill", res.SkillName)
	}
	// The envelope reports what the skill DECLARES. The composer serves the
	// interactive dispatcher too, where AskUserQuestion is the whole point, so
	// dropping it here would have been a headless policy applied by a function
	// that cannot know its caller (#79).
	want := []string{"Read", "Edit", "Bash", "AskUserQuestion"}
	if !reflect.DeepEqual(res.AllowedTools, want) {
		t.Errorf("AllowedTools = %v, want %v", res.AllowedTools, want)
	}
	if len(res.ProgrammaticTools) != 1 || res.ProgrammaticTools[0] != "TodoWrite" {
		t.Errorf("ProgrammaticTools = %v", res.ProgrammaticTools)
	}
	if !strings.Contains(res.Content, "# Test Skill") {
		t.Error("content should contain the skill body")
	}
	if strings.Contains(res.Content, "name: test-skill") {
		t.Error("frontmatter must be stripped from the body")
	}
}

func TestFilterHeadlessToolsDropsOnlyAskUserQuestion(t *testing.T) {
	// The headless callers apply this; the interactive ones deliberately do not.
	got := FilterHeadlessTools([]string{"Read", "AskUserQuestion", "Bash", "Task"})
	want := []string{"Read", "Bash", "Task"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("FilterHeadlessTools = %v, want %v", got, want)
	}

	// Nothing to filter is the ordinary case — every shipped stage skill
	// declares no AskUserQuestion — so it must not disturb the list, and an
	// empty input must not become a non-nil empty slice that reads as "the
	// skill declared zero tools" instead of "declared none".
	unchanged := []string{"Read", "Bash"}
	if got := FilterHeadlessTools(unchanged); !reflect.DeepEqual(got, unchanged) {
		t.Errorf("FilterHeadlessTools(%v) = %v, want unchanged", unchanged, got)
	}
	if got := FilterHeadlessTools(nil); got != nil {
		t.Errorf("FilterHeadlessTools(nil) = %v, want nil", got)
	}
}

func TestIncludesAreExpanded(t *testing.T) {
	root := t.TempDir()
	shared := filepath.Join(root, "_shared")
	mkdir(t, shared)
	write(t, filepath.Join(shared, "CONTEXT.md"), "## Shared Context\nThis is shared.")
	writeSkill(t, root, "nightgauge-feature-dev", `---
name: test-include
allowed-tools: Read
---

<!-- include: ../_shared/CONTEXT.md -->

# Main Content
`)
	res := mustRender(t, Options{Stage: "feature-dev", SkillsRoots: []string{root}})

	for _, want := range []string{"## Shared Context", "This is shared.", "# Main Content"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("expanded content missing %q", want)
		}
	}
	if strings.Contains(res.Content, "<!-- include:") {
		t.Error("include directive survived expansion")
	}
}

func TestMissingIncludeIsLeftInPlace(t *testing.T) {
	// Portability contract: the same document must stay readable under a host
	// that does not expand. Erroring here would make a skill unrunnable on any
	// adapter whose bundle omits an optional include.
	root := t.TempDir()
	writeSkill(t, root, "nightgauge-feature-dev", "body\n<!-- include: ../_shared/ABSENT.md -->\n")
	res := mustRender(t, Options{Stage: "feature-dev", SkillsRoots: []string{root}})
	if !strings.Contains(res.Content, "<!-- include: ../_shared/ABSENT.md -->") {
		t.Errorf("missing include should be left as-is, got:\n%s", res.Content)
	}
}

func TestRewriteSkillRelativePaths(t *testing.T) {
	content := "Read `skills/nightgauge-feature-dev/_includes/plan.md` now.\n" +
		"Also see skills/_shared/GOTCHAS.md and skills/feature-dev/_includes/x.md.\n" +
		"Cross-skill ref: skills/nightgauge-pipeline-audit/SKILL.md stays put.\n"
	got := RewriteSkillRelativePaths(content, "feature-dev", "/bundle/dist/skills/nightgauge-feature-dev")

	for _, want := range []string{
		"/bundle/dist/skills/nightgauge-feature-dev/_includes/plan.md",
		"/bundle/dist/skills/_shared/GOTCHAS.md",
		"/bundle/dist/skills/nightgauge-feature-dev/_includes/x.md", // prefix-stripped variant
		"skills/nightgauge-pipeline-audit/SKILL.md",                 // cross-skill ref untouched
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rewritten content missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "`skills/nightgauge-feature-dev/") {
		t.Errorf("own-skill relative path survived the rewrite:\n%s", got)
	}
}

func TestSplitTools(t *testing.T) {
	for _, tt := range []struct {
		input string
		want  int
	}{
		{"Read Edit Bash", 3},
		// Verbatim: AskUserQuestion is counted, not dropped. Filtering is the
		// headless caller's job now (FilterHeadlessTools) — see #79.
		{"Read Edit Bash AskUserQuestion", 4},
		{"", 0},
		{"Read", 1},
	} {
		if got := splitTools(tt.input); len(got) != tt.want {
			t.Errorf("splitTools(%q) = %d tools, want %d", tt.input, len(got), tt.want)
		}
	}
}

// ─── Location ────────────────────────────────────────────────────────────────

func TestLocateFirstMatchWins(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	writeSkill(t, second, "nightgauge-feature-dev", "second root\n")
	res := mustRender(t, Options{Stage: "feature-dev", SkillsRoots: []string{first, second}})
	if !strings.Contains(res.Content, "second root") {
		t.Error("should fall through an empty root to the next one")
	}

	writeSkill(t, first, "nightgauge-feature-dev", "first root\n")
	res = mustRender(t, Options{Stage: "feature-dev", SkillsRoots: []string{first, second}})
	if !strings.Contains(res.Content, "first root") {
		t.Error("first matching root must win")
	}
}

func TestLocateErrorsWithoutRoots(t *testing.T) {
	// Skill location is deliberately the caller's responsibility (ADR 016 §4):
	// silently defaulting to a guessed root is how an agent ends up reading a
	// stale ~/.codex/skills copy (#196).
	if _, err := Render(Options{Stage: "feature-dev"}); err == nil {
		t.Error("expected an error when no roots are supplied")
	}
}

func TestLocateUnknownStage(t *testing.T) {
	if _, err := Render(Options{Stage: "not-a-stage", SkillsRoots: []string{t.TempDir()}}); err == nil {
		t.Error("expected an error for an unknown stage")
	}
}

// ─── Overlay key cascade ─────────────────────────────────────────────────────

func TestOverlayKeysCascade(t *testing.T) {
	for _, tt := range []struct {
		name, model, adapter string
		want                 []string
	}{
		// #582 retired the band segment from the cascade (ADR 016 §2 as
		// amended): provider → concrete id. Band names survive as INPUTS —
		// they resolve through the registry to a concrete model — but no
		// band-keyed overlay file is consulted anymore (none ever existed on
		// disk, so rendered output is unchanged).
		{"concrete anthropic id", "claude-opus-5", "", []string{"anthropic", "claude-opus-5"}},
		{"tier alias", "opus", "", []string{"anthropic", "claude-opus-5"}},
		{"multi-band model keys off the concrete id", "gpt-5.6-sol", "codex",
			[]string{"openai", "gpt-5.6-sol"}},
		{"adapter selects provider", "sonnet", "codex",
			[]string{"openai", "gpt-5.6-terra"}},
		// #532 moved the xai haiku band from grok-build-0.1 (which the Grok
		// Build CLI does not serve) to grok-4.6. Post-#582 the cascade keys
		// off the RESOLVED model, so a haiku-band grok run renders the same
		// overlay set as any other grok-4.6 run: the provider and the
		// concrete id.
		{"xai haiku cascade", "haiku", "grok",
			[]string{"xai", "grok-4.6"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, _, ok := OverlayKeys(tt.model, tt.adapter)
			if !ok {
				t.Fatalf("OverlayKeys(%q, %q) did not resolve", tt.model, tt.adapter)
			}
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("keys = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestOverlayKeysFailOpen(t *testing.T) {
	// Unknown ids and local providers have no registry entry by design. They
	// must resolve NO keys and render base-only — never error, or every local
	// run breaks.
	for _, tt := range []struct{ model, adapter string }{
		{"", ""},
		{"llama-3-70b-local", ""},
		{"opus", "ollama"},
		{"sonnet", "lm-studio"},
	} {
		if keys, _, ok := OverlayKeys(tt.model, tt.adapter); ok || len(keys) > 0 {
			t.Errorf("OverlayKeys(%q, %q) = %v ok=%v, want no keys", tt.model, tt.adapter, keys, ok)
		}
	}
}

// ─── Composition ─────────────────────────────────────────────────────────────

const bodyWithContextIncludes = `---
name: nightgauge-feature-dev
allowed-tools: Read
---

# Feature Dev

<!-- include: ../_shared/PIPELINE_CONTEXT.md -->
<!-- include: ../_shared/AUTONOMY_CONTRACT.md -->

## Procedure

Do the work.
`

func TestNoOverlaysRendersBaseOnly(t *testing.T) {
	root := overlayFixture(t, nil, nil, "")
	res := mustRender(t, Options{Stage: "feature-dev", Model: "claude-opus-5", SkillsRoots: []string{root}})

	if len(res.Fragments) != 0 {
		t.Errorf("expected no fragments, got %v", res.Fragments)
	}
	if strings.Contains(res.Content, AdaptationHeading) {
		t.Error("no fragments must mean no Model Adaptation section at all")
	}
	if res.InjectionSite != SiteNone {
		t.Errorf("InjectionSite = %q, want %q", res.InjectionSite, SiteNone)
	}
	// The cascade still resolves — absence of files, not absence of keys.
	if len(res.Keys) == 0 {
		t.Error("keys should resolve even when no fragment files exist")
	}
}

func TestSharedOnlySkillOnlyAndBothCompose(t *testing.T) {
	t.Run("shared only", func(t *testing.T) {
		root := overlayFixture(t, map[string]string{"anthropic": "SHARED-ANTHROPIC"}, nil, "")
		res := mustRender(t, Options{Stage: "feature-dev", Model: "claude-opus-5", SkillsRoots: []string{root}})
		assertFragments(t, res, "shared:anthropic")
		if !strings.Contains(res.Content, "SHARED-ANTHROPIC") {
			t.Error("shared fragment body missing from output")
		}
	})

	t.Run("skill only", func(t *testing.T) {
		root := overlayFixture(t, nil, map[string]string{"claude-opus-5": "SKILL-MODEL"}, "")
		res := mustRender(t, Options{Stage: "feature-dev", Model: "claude-opus-5", SkillsRoots: []string{root}})
		assertFragments(t, res, "skill:claude-opus-5")
	})

	t.Run("both, shared before skill and general before specific", func(t *testing.T) {
		root := overlayFixture(t,
			map[string]string{"anthropic": "S-PROVIDER", "claude-opus-5": "S-MODEL"},
			map[string]string{"anthropic": "K-PROVIDER", "claude-opus-5": "K-MODEL"}, "")
		res := mustRender(t, Options{Stage: "feature-dev", Model: "claude-opus-5", SkillsRoots: []string{root}})

		// ADR 016 §2 (amended by #582): _shared/anthropic -> _shared/claude-opus-5
		//          -> <skill>/anthropic -> <skill>/claude-opus-5
		assertFragments(t, res,
			"shared:anthropic", "shared:claude-opus-5",
			"skill:anthropic", "skill:claude-opus-5")

		// And the ORDER must hold in the rendered text, not just the metadata —
		// "later fragments may countermand earlier ones" is only true if the
		// composed body preserves the cascade.
		var idx []int
		for _, marker := range []string{"S-PROVIDER", "S-MODEL", "K-PROVIDER", "K-MODEL"} {
			i := strings.Index(res.Content, marker)
			if i < 0 {
				t.Fatalf("fragment %q missing from composed output", marker)
			}
			idx = append(idx, i)
		}
		for i := 1; i < len(idx); i++ {
			if idx[i] < idx[i-1] {
				t.Errorf("fragments out of cascade order in the body: %v", idx)
			}
		}
	})
}

func TestInjectionSites(t *testing.T) {
	t.Run("after context includes by default", func(t *testing.T) {
		root := overlayFixture(t, map[string]string{"claude-opus-5": "OVERLAY"}, nil, "")
		res := mustRender(t, Options{Stage: "feature-dev", Model: "claude-opus-5", SkillsRoots: []string{root}})
		if res.InjectionSite != SiteAfterContext {
			t.Fatalf("InjectionSite = %q, want %q", res.InjectionSite, SiteAfterContext)
		}
		// Read before the procedure, not after it — burying adaptation guidance
		// below a thousand lines of procedure is how it gets ignored (§3).
		overlayAt := strings.Index(res.Content, "OVERLAY")
		procedureAt := strings.Index(res.Content, "## Procedure")
		if overlayAt < 0 || procedureAt < 0 || overlayAt > procedureAt {
			t.Errorf("adaptation block must precede the procedure (overlay=%d procedure=%d)", overlayAt, procedureAt)
		}
	})

	t.Run("explicit anchor wins over the positional fallback", func(t *testing.T) {
		body := strings.Replace(bodyWithContextIncludes, "Do the work.", OverlayAnchor+"\n\nDo the work.", 1)
		root := overlayFixture(t, map[string]string{"claude-opus-5": "OVERLAY"}, nil, body)
		res := mustRender(t, Options{Stage: "feature-dev", Model: "claude-opus-5", SkillsRoots: []string{root}})

		if res.InjectionSite != SiteAnchor {
			t.Fatalf("InjectionSite = %q, want %q", res.InjectionSite, SiteAnchor)
		}
		if strings.Contains(res.Content, OverlayAnchor) {
			t.Error("the anchor comment should be consumed, not left in the output")
		}
		// Anchor sits AFTER "## Procedure" in this fixture, so honouring it must
		// move the block there — proving the anchor really beat the fallback.
		if strings.Index(res.Content, "OVERLAY") < strings.Index(res.Content, "## Procedure") {
			t.Error("block was placed at the positional fallback, not the anchor")
		}
	})

	t.Run("top of body when no context includes exist", func(t *testing.T) {
		root := overlayFixture(t, map[string]string{"claude-opus-5": "OVERLAY"}, nil,
			"---\nname: x\n---\n\n# Bare Skill\n\nNo context includes here.\n")
		res := mustRender(t, Options{Stage: "feature-dev", Model: "claude-opus-5", SkillsRoots: []string{root}})
		if res.InjectionSite != SiteTopOfBody {
			t.Errorf("InjectionSite = %q, want %q", res.InjectionSite, SiteTopOfBody)
		}
		if !strings.Contains(res.Content, "OVERLAY") {
			t.Error("overlay missing from output")
		}
	})
}

func TestWholeFileOverrideReplacesBase(t *testing.T) {
	root := overlayFixture(t, map[string]string{"anthropic": "SHARED"}, map[string]string{"claude-opus-5": "SKILL-FRAG"}, "")
	skillDir := filepath.Join(root, "nightgauge-feature-dev")
	write(t, filepath.Join(skillDir, "_overlays", "claude-opus-5.SKILL.md"),
		"---\nname: overridden\nallowed-tools: Read Bash\n---\n\n# Wholly Different Procedure\n")

	res := mustRender(t, Options{Stage: "feature-dev", Model: "claude-opus-5", SkillsRoots: []string{root}})

	if res.InjectionSite != SiteWholeFile {
		t.Errorf("InjectionSite = %q, want %q", res.InjectionSite, SiteWholeFile)
	}
	if res.WholeFile == "" {
		t.Error("--json must report the override so the drift liability stays visible (ADR 016 §8)")
	}
	if !strings.Contains(res.Content, "# Wholly Different Procedure") {
		t.Error("override body missing")
	}
	// "Replaces the base ENTIRELY" — additive fragments must not also apply.
	for _, leak := range []string{"# Feature Dev", "## Procedure", "SHARED", "SKILL-FRAG", AdaptationHeading} {
		if strings.Contains(res.Content, leak) {
			t.Errorf("override did not replace the base: %q leaked through", leak)
		}
	}
	if res.SkillName != "overridden" {
		t.Errorf("SkillName = %q, want the override's frontmatter", res.SkillName)
	}
}

func TestWholeFileOverridePrefersMostSpecific(t *testing.T) {
	root := overlayFixture(t, nil, nil, "")
	dir := filepath.Join(root, "nightgauge-feature-dev", "_overlays")
	write(t, filepath.Join(dir, "anthropic.SKILL.md"), "# PROVIDER LEVEL\n")
	write(t, filepath.Join(dir, "claude-opus-5.SKILL.md"), "# MODEL LEVEL\n")

	res := mustRender(t, Options{Stage: "feature-dev", Model: "claude-opus-5", SkillsRoots: []string{root}})
	if !strings.Contains(res.Content, "# MODEL LEVEL") {
		t.Error("most specific override must win")
	}
	if strings.Contains(res.Content, "# PROVIDER LEVEL") {
		t.Error("less specific override should not apply")
	}
}

func TestUnknownModelAndLocalProviderRenderBaseOnly(t *testing.T) {
	// Both must produce byte-identical output to a no-model render — that
	// equality IS the fail-open contract, so assert the bytes, not just that
	// no error came back.
	root := overlayFixture(t, map[string]string{"anthropic": "SHOULD-NOT-APPLY"}, nil, "")
	base := mustRender(t, Options{Stage: "feature-dev", SkillsRoots: []string{root}})

	for _, tt := range []struct{ name, model, adapter string }{
		{"unknown model id", "some-unreleased-model", ""},
		{"ollama local", "opus", "ollama"},
		{"lm-studio local", "opus", "lm-studio"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := mustRender(t, Options{
				Stage: "feature-dev", Model: tt.model, Adapter: tt.adapter, SkillsRoots: []string{root},
			})
			if got.Content != base.Content {
				t.Errorf("content differs from base-only render:\n--- got ---\n%s", got.Content)
			}
			if len(got.Fragments) != 0 {
				t.Errorf("fragments applied for a model with no registry entry: %v", got.Fragments)
			}
			if strings.Contains(got.Content, "SHOULD-NOT-APPLY") {
				t.Error("an overlay leaked into a base-only render")
			}
		})
	}
}

func TestUnreadableFragmentWarnsAndSkips(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod 000 does not deny reads")
	}
	root := overlayFixture(t, map[string]string{"anthropic": "READABLE", "claude-opus-5": "UNREADABLE"}, nil, "")
	bad := filepath.Join(root, "_shared", "_overlays", "claude-opus-5.md")
	if err := os.Chmod(bad, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(bad, 0o644) })

	var warned []string
	res, err := Render(Options{
		Stage: "feature-dev", Model: "claude-opus-5", SkillsRoots: []string{root},
		Warn: func(m string) { warned = append(warned, m) },
	})
	if err != nil {
		t.Fatalf("a malformed overlay must never take down a run: %v", err)
	}
	if len(warned) == 0 {
		t.Error("an unreadable fragment should warn — silence makes it undiagnosable")
	}
	if !strings.Contains(res.Content, "READABLE") {
		t.Error("the readable fragment should still apply")
	}
	for _, f := range res.Fragments {
		if strings.HasSuffix(f.Path, "claude-opus-5.md") {
			t.Error("--json reported a fragment that contributed nothing to the output")
		}
	}
}

func TestRenderIsDeterministic(t *testing.T) {
	root := overlayFixture(t,
		map[string]string{"anthropic": "A", "claude-opus-5": "B"},
		map[string]string{"anthropic": "C", "claude-opus-5": "D"}, "")
	opts := Options{Stage: "feature-dev", Model: "claude-opus-5", SkillsRoots: []string{root}}

	first := mustRender(t, opts)
	for i := 0; i < 25; i++ {
		got := mustRender(t, opts)
		if got.Content != first.Content {
			t.Fatalf("render %d differs — output is not byte-stable", i)
		}
		if a, b := jsonOf(t, got), jsonOf(t, first); a != b {
			t.Fatalf("render %d envelope differs:\n%s\n%s", i, a, b)
		}
	}
}

func TestJSONEnvelopeReportsProvenance(t *testing.T) {
	root := overlayFixture(t, map[string]string{"anthropic": "S"}, map[string]string{"claude-opus-5": "K"}, "")
	res := mustRender(t, Options{Stage: "feature-dev", Model: "opus", SkillsRoots: []string{root}})

	var envelope map[string]any
	if err := json.Unmarshal([]byte(jsonOf(t, res)), &envelope); err != nil {
		t.Fatalf("envelope is not valid JSON: %v", err)
	}
	for _, key := range []string{"v", "stage", "resolved_keys", "fragments", "injection_site", "skill_path"} {
		if _, ok := envelope[key]; !ok {
			t.Errorf("envelope missing %q", key)
		}
	}
	if envelope["resolved_model_id"] != "claude-opus-5" {
		t.Errorf("tier alias should report the concrete id it resolved to, got %v", envelope["resolved_model_id"])
	}
	// Content goes to stdout, never into the envelope.
	if _, leaked := envelope["Content"]; leaked {
		t.Error("composed content must not be embedded in the JSON envelope")
	}
}

// ─── Real-skill invariants ───────────────────────────────────────────────────

// TestRealSkillsRenderClean runs the shipped skills through the renderer. It
// asserts INVARIANTS rather than pinning bytes on purpose: golden copies of
// real skills would have to be regenerated on every skill edit (#82 alone
// rewrites 14 of them), and a golden nobody can read is a golden nobody
// checks — it gets regenerated to green instead of read.
func TestRealSkillsRenderClean(t *testing.T) {
	root := filepath.Join("..", "..", "skills")
	if _, err := os.Stat(root); err != nil {
		t.Skipf("skills/ not present: %v", err)
	}
	for stage := range StageSkillDirs {
		t.Run(stage, func(t *testing.T) {
			res, err := Render(Options{Stage: stage, SkillsRoots: []string{root}})
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			// NO directive may survive expansion. Every surviving one ships the
			// literal HTML comment to the model in place of the shared content,
			// and the two ways that happens are indistinguishable in the output:
			// expansion broke, or the target never resolved.
			//
			// This assertion used to be the weaker "a surviving directive must
			// correspond to a genuinely missing file", carved out because all six
			// stage skills included `../_shared/BATCH_MODE.md` against a file that
			// had never been written (#337). The file now exists, so the carve-out
			// is gone. Do not reintroduce it: `preflight skill-includes`
			// (TestSkillIncludes_WorkingTreeIsClean) is where a missing target is
			// caught, and weakening this one is how the hole stayed open.
			skillDir := filepath.Dir(res.SkillPath)
			for _, m := range IncludePattern.FindAllStringSubmatch(res.Content, -1) {
				target := filepath.Join(skillDir, strings.TrimSpace(m[1]))
				reason := "expansion is broken"
				if _, err := os.Stat(target); err != nil {
					reason = "target does not resolve: " + target
				}
				t.Errorf("include %q survived expansion — %s", m[0], reason)
			}
			if res.SkillName == "" {
				t.Error("frontmatter name not parsed")
			}
			if len(res.AllowedTools) == 0 {
				t.Error("allowed-tools not parsed")
			}
			if len(res.Content) < 1000 {
				t.Errorf("suspiciously short render (%d bytes)", len(res.Content))
			}
			// This case passes no Model, so no key resolves and every shipped
			// skill must render base-only — the fail-open contract, not a
			// statement about which overlays exist. (Overlays do exist now:
			// see TestFableOverlayReachesTheStagesThatDispatchIt for the
			// with-a-model half.)
			if len(res.Fragments) != 0 {
				t.Errorf("model-less render applied fragments: %v", res.Fragments)
			}
		})
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func mustRender(t *testing.T, opts Options) *Result {
	t.Helper()
	res, err := Render(opts)
	if err != nil {
		t.Fatalf("Render(%+v): %v", opts, err)
	}
	return res
}

func jsonOf(t *testing.T, res *Result) string {
	t.Helper()
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func mkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	mkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func writeSkill(t *testing.T, root, dir, content string) {
	t.Helper()
	write(t, filepath.Join(root, dir, "SKILL.md"), content)
}

// overlayFixture builds a skills root with a feature-dev skill plus the given
// shared and skill-scoped overlay fragments, keyed by overlay key.
func overlayFixture(t *testing.T, shared, skill map[string]string, body string) string {
	t.Helper()
	root := t.TempDir()
	if body == "" {
		body = bodyWithContextIncludes
	}
	writeSkill(t, root, "nightgauge-feature-dev", body)
	for key, text := range shared {
		write(t, filepath.Join(root, "_shared", "_overlays", key+".md"), text+"\n")
	}
	for key, text := range skill {
		write(t, filepath.Join(root, "nightgauge-feature-dev", "_overlays", key+".md"), text+"\n")
	}
	return root
}

func assertFragments(t *testing.T, res *Result, want ...string) {
	t.Helper()
	var got []string
	for _, f := range res.Fragments {
		got = append(got, f.Scope+":"+f.Key)
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("fragments = %v, want %v", got, want)
	}
}

// TestRewriteIsNotIdempotent pins a property that reads like a defect and is
// in fact the reason the rewrite may run exactly once.
//
// An already-absolute "/abs/skills/_shared/" still CONTAINS the
// "skills/_shared/" needle, so a second pass expands it to
// "/abs//abs/skills/_shared/" and every read directive points at a path that
// does not exist — the #196 filesystem-scan failure the rewrite exists to fix.
// Render owns the rewrite (ADR 016 §4) and BuildPrompt must not repeat it.
// Should someone make the rewrite idempotent, this test is the place to record
// that the second caller is safe again.
func TestRewriteIsNotIdempotent(t *testing.T) {
	dir := "/abs/skills/nightgauge-feature-dev"
	once := RewriteSkillRelativePaths("See skills/_shared/GOTCHAS.md\n", "feature-dev", dir)
	twice := RewriteSkillRelativePaths(once, "feature-dev", dir)

	if once != twice {
		if !strings.Contains(twice, "/abs//abs/") {
			t.Errorf("expected the double-rewrite to duplicate the prefix, got: %q", twice)
		}
		return // documented, expected
	}
	t.Error("rewrite became idempotent — BuildPrompt may now rewrite again safely; " +
		"update the comments in internal/execution/skill.go that cite this property")
}

// TestRelativeRootStillYieldsAbsoluteDirectives is the guard on a silent
// failure mode. A relative --skills-root makes skillDir relative, and
// rewriting "skills/x/" to "skills/x/" is a no-op that looks like success —
// the composed prompt still ships relative paths, and an agent spawned in
// another worktree cannot resolve them (#196). Asserting only "the rewrite
// ran" would pass against exactly that bug.
func TestRelativeRootStillYieldsAbsoluteDirectives(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "nightgauge-feature-dev",
		"Read `skills/nightgauge-feature-dev/_includes/plan.md` and skills/_shared/X.md now.\n")

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	rel, err := filepath.Rel(wd, root)
	if err != nil {
		t.Skipf("cannot relativize %s against %s: %v", root, wd, err)
	}

	res := mustRender(t, Options{Stage: "feature-dev", SkillsRoots: []string{rel}})

	if !filepath.IsAbs(res.SkillPath) {
		t.Errorf("SkillPath = %q, want an absolute path", res.SkillPath)
	}
	for _, leaked := range []string{"`skills/nightgauge-feature-dev/", " skills/_shared/"} {
		if strings.Contains(res.Content, leaked) {
			t.Errorf("relative directive %q survived — a spawned agent cannot resolve it:\n%s",
				leaked, res.Content)
		}
	}
	if !strings.Contains(res.Content, root) {
		t.Errorf("expected directives rewritten under %s, got:\n%s", root, res.Content)
	}
}

// ─── #337: the dead-include capture, before and after ────────────────────────

const deadIncludeFixture = "testdata/dead-include/pr-merge.pre-fix.rendered.md"

// TestDeadIncludeFixture_StillExhibitsThePreFixDefect pins the captured
// artifact. It is a real `nightgauge skill render --stage pr-merge` capture
// from main @ 8c942971 — the composed text the model was handed — and its
// value is entirely in the fact that it is NOT current. If someone
// re-captures it against the fixed tree, the before/after below becomes a
// tautology and the evidence for #337 is gone, silently. Provenance and
// redaction rules: testdata/dead-include/README.md.
func TestDeadIncludeFixture_StillExhibitsThePreFixDefect(t *testing.T) {
	data, err := os.ReadFile(deadIncludeFixture)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	captured := string(data)

	if !strings.Contains(captured, "<!-- include: ../_shared/BATCH_MODE.md -->") {
		t.Fatal("the captured pre-fix render no longer contains the dead directive — " +
			"it has been regenerated against fixed code; restore the original capture")
	}
	// The directive was the ENTIRE body of a declared phase: the marker prints,
	// the phase tracker advances, and the model receives a comment.
	marker := `printf '<!-- phase:start name="batch-detection" index=1 total=14 stage="pr-merge" -->\n'`
	mi := strings.Index(captured, marker)
	di := strings.Index(captured, "<!-- include: ../_shared/BATCH_MODE.md -->")
	if mi < 0 || di < mi {
		t.Fatalf("expected the dead directive to follow the batch-detection phase marker (marker=%d directive=%d)", mi, di)
	}
	if between := captured[mi+len(marker) : di]; strings.TrimSpace(strings.Trim(between, "`\n")) != "" {
		t.Errorf("expected an empty phase body between the marker and the directive, got %q", between)
	}
	// Public repository: the capture script rewrites the repo root and $HOME.
	// Re-assert it here so a careless re-capture cannot land a home path.
	for _, leak := range []string{"/Users/", "/home/", "/root/"} {
		if strings.Contains(captured, leak) {
			t.Errorf("fixture leaks a host path containing %q — redact before committing", leak)
		}
	}
}

// TestDeadIncludeFixture_SameStageRendersCleanNow is the "after" half: the
// stage the fixture captured, rendered from the working tree, hands the model
// the shared batch contract instead of the comment.
func TestDeadIncludeFixture_SameStageRendersCleanNow(t *testing.T) {
	root := filepath.Join("..", "..", "skills")
	if _, err := os.Stat(root); err != nil {
		t.Skipf("skills/ not present: %v", err)
	}
	res := mustRender(t, Options{Stage: "pr-merge", SkillsRoots: []string{root}})
	if strings.Contains(res.Content, "<!-- include: ../_shared/BATCH_MODE.md -->") {
		t.Error("the dead directive is still in the live pr-merge render")
	}
	if !strings.Contains(res.Content, "## Batch Mode") {
		t.Error("expected the expanded batch contract in the live pr-merge render")
	}
}

// TestPRMergeBatchDetailReadIsConditional guards #367's context-cost
// regression: the 124-line batch procedure must stay out of the common
// single-issue path, without moving or renumbering Phase 0.5.
func TestPRMergeBatchDetailReadIsConditional(t *testing.T) {
	path := filepath.Join("..", "..", "skills", "nightgauge-pr-merge", "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	content := string(data)
	marker := `printf '<!-- phase:start name="batch-detection" index=1 total=14 stage="pr-merge" -->\n'`
	phaseStart := strings.Index(content, marker)
	phaseEnd := strings.Index(content, "### Phase 1: Validate Environment")
	if phaseStart < 0 || phaseEnd < phaseStart {
		t.Fatalf("Phase 0.5 boundaries changed (marker=%d next phase=%d)", phaseStart, phaseEnd)
	}
	phase := content[phaseStart:phaseEnd]

	wantInOrder := []string{
		`[ -n "$EPIC_NUMBER" ] && [ -f "$BATCH_DEV" ]`,
		`BATCH_CONTEXT_FOUND=$BATCH_DEV`,
		"Only when the probe prints `BATCH_CONTEXT_FOUND=...`",
		"skills/nightgauge-pr-merge/_includes/batch-detection.md",
		"Do not read the file when the probe prints `SINGLE_ISSUE`",
	}
	last := -1
	for _, want := range wantInOrder {
		idx := strings.Index(phase, want)
		if idx < 0 {
			t.Errorf("Phase 0.5 missing conditional-read contract %q", want)
			continue
		}
		if idx <= last {
			t.Errorf("Phase 0.5 contract %q is out of order", want)
		}
		last = idx
	}

	unconditional := "> **Read `skills/nightgauge-pr-merge/_includes/batch-detection.md` now"
	if strings.Contains(phase, unconditional) {
		t.Errorf("single-issue path still carries the unconditional batch-detail read: %q", unconditional)
	}
}

func TestPRMergeBatchProbeSignalsContextPresence(t *testing.T) {
	path := filepath.Join("..", "..", "skills", "nightgauge-pr-merge", "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	content := string(data)
	marker := `printf '<!-- phase:start name="batch-detection" index=1 total=14 stage="pr-merge" -->\n'`
	phaseStart := strings.Index(content, marker)
	phaseEnd := strings.Index(content, "### Phase 1: Validate Environment")
	if phaseStart < 0 || phaseEnd < phaseStart {
		t.Fatalf("Phase 0.5 boundaries changed (marker=%d next phase=%d)", phaseStart, phaseEnd)
	}
	phase := content[phaseStart:phaseEnd]
	open := strings.Index(phase, "```bash\n")
	if open < 0 {
		t.Fatal("Phase 0.5 has no inline bash probe")
	}
	probeStart := open + len("```bash\n")
	close := strings.Index(phase[probeStart:], "\n```")
	if close < 0 {
		t.Fatal("Phase 0.5 bash probe has no closing fence")
	}
	probe := phase[probeStart : probeStart+close]

	repo := t.TempDir()
	gittest.Run(t, repo, "init", "-q", "-b", "fix/367-test")
	runProbe := func() string {
		t.Helper()
		cmd := exec.Command("bash", "-c", probe)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("run Phase 0.5 probe: %v\n%s", err, out)
		}
		return strings.TrimSpace(string(out))
	}

	if got := runProbe(); got != "SINGLE_ISSUE" {
		t.Fatalf("probe without a batch file = %q, want SINGLE_ISSUE", got)
	}
	batchPath := filepath.Join(repo, ".nightgauge", "pipeline", "dev-batch-367.json")
	if err := os.MkdirAll(filepath.Dir(batchPath), 0o755); err != nil {
		t.Fatalf("create pipeline directory: %v", err)
	}
	if err := os.WriteFile(batchPath, []byte(`{"issue_numbers":[367]}`), 0o644); err != nil {
		t.Fatalf("write batch fixture: %v", err)
	}
	if got := runProbe(); got != "BATCH_CONTEXT_FOUND=.nightgauge/pipeline/dev-batch-367.json" {
		t.Fatalf("probe with a batch file = %q, want the batch context signal", got)
	}
}

// ─── Model overlays over the real corpus ─────────────────────────────────────

// TestFableOverlayReachesTheStagesThatDispatchIt renders the shipped skills
// against the real overlay corpus and proves the claude-fable-5-1 fragment
// both applies and is scoped (#1276).
//
// It is written to be able to go red in both directions. Deleting
// skills/_shared/_overlays/claude-fable-5-1.md fails the "want" half; moving
// the same text up into a provider-level `anthropic.md` fragment fails the
// "not want" half, because claude-opus-5 resolves that same provider key and
// must still render base-only.
//
// The substrings are the load-bearing phrasings the vendor guidance gives
// verbatim ("surgically edit a file", "operating autonomously"), not
// paraphrases. A reflow that wraps a line between two of those words is a real
// regression rather than a cosmetic one: the rendered prompt is what the model
// reads, and the guidance is explicit that the exact wording carries the
// effect.
func TestFableOverlayReachesTheStagesThatDispatchIt(t *testing.T) {
	root := filepath.Join("..", "..", "skills")
	if _, err := os.Stat(root); err != nil {
		t.Skipf("skills/ not present: %v", err)
	}
	overlay := filepath.Join(root, "_shared", "_overlays", "claude-fable-5-1.md")
	if _, err := os.Stat(overlay); err != nil {
		t.Fatalf("the claude-fable-5-1 overlay must exist: %v", err)
	}

	fableText := []string{
		"surgically edit a file",   // targeted edits over whole-file rewrites
		"operating autonomously",   // the unattended-run block
		"report it as a follow-up", // scope and test coverage
		"the scope is the deliverable",
	}

	for stage := range StageSkillDirs {
		t.Run("fable/"+stage, func(t *testing.T) {
			res := mustRender(t, Options{
				Stage:       stage,
				Model:       "claude-fable-5-1",
				SkillsRoots: []string{root},
			})
			if res.ResolvedModel != "claude-fable-5-1" {
				t.Fatalf("resolved model = %q, want claude-fable-5-1", res.ResolvedModel)
			}
			if len(res.Fragments) == 0 {
				t.Fatalf("no overlay fragment applied; keys=%v", res.Keys)
			}
			for _, want := range fableText {
				if !strings.Contains(res.Content, want) {
					t.Errorf("rendered %s is missing overlay text %q", stage, want)
				}
			}
		})
	}

	for stage := range StageSkillDirs {
		t.Run("opus/"+stage, func(t *testing.T) {
			res := mustRender(t, Options{
				Stage:       stage,
				Model:       "claude-opus-5",
				SkillsRoots: []string{root},
			})
			for _, notWant := range fableText {
				if strings.Contains(res.Content, notWant) {
					t.Errorf("rendered %s for claude-opus-5 leaked overlay text %q", stage, notWant)
				}
			}
		})
	}
}

// TestFableBatchingNudgeIsStageScoped pins the stage scoping from #1276.
//
// The vendor scopes this fix by loop shape, not by a measurement: the symptom is
// one tool call per turn in coding and computer-use loops where the next
// independent calls are implied by the task rather than asked for, and the cost
// is extra turns (tokens, round trips, wall-clock) rather than worse answers.
// feature-dev and feature-validate are the pipeline's only stages of that shape,
// so the nudge is scoped by prose to those two. No measurement gates it and none
// could — diagnostics.ToolCallRecord has no assistant-turn identifier, so the
// share of multi-call turns is not derivable even from a populated record.
//
// The composer injects one block per render, so that scoping lives in the
// heading rather than in the cascade. This test is what stops the nudge
// quietly becoming pipeline-wide advice if the heading is ever dropped.
func TestFableBatchingNudgeIsStageScoped(t *testing.T) {
	root := filepath.Join("..", "..", "skills")
	if _, err := os.Stat(root); err != nil {
		t.Skipf("skills/ not present: %v", err)
	}
	const nudge = "First privately list what you need next"
	const scope = "feature-dev and feature-validate only"

	for stage := range StageSkillDirs {
		res := mustRender(t, Options{
			Stage:       stage,
			Model:       "claude-fable-5-1",
			SkillsRoots: []string{root},
		})
		if !strings.Contains(res.Content, nudge) {
			t.Errorf("%s: batching nudge missing from the overlay", stage)
			continue
		}
		if !strings.Contains(res.Content, scope) {
			t.Errorf("%s: batching nudge present without its %q scope line", stage, scope)
		}
	}
}

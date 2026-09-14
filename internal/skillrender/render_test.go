package skillrender

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
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
		"Cross-skill ref: skills/nightgauge-pipeline-audit/SKILL.md stays put.\n" +
		"Skill-relative: Read `_includes/plan.md` (same directory as this SKILL.md) now.\n"
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
	if strings.Contains(got, "`_includes/") {
		t.Errorf("skill-relative `_includes/` path survived the rewrite:\n%s", got)
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
		// No adapter, no host segment: unchanged from before ADR-022 §14.
		{"concrete anthropic id", "claude-opus-5", "", []string{"anthropic", "claude-opus-5"}},
		{"tier alias", "opus", "", []string{"anthropic", "claude-opus-5"}},
		// A non-empty adapter is now also the HOST segment, most general of
		// all (ADR-016 amendment / ADR-022 §14): it leads the cascade.
		{"multi-band model keys off the concrete id", "gpt-5.6-sol", "codex",
			[]string{"codex", "openai", "gpt-5.6-sol"}},
		{"adapter selects provider", "sonnet", "codex",
			[]string{"codex", "openai", "gpt-5.6-terra"}},
		// #532 moved the xai haiku band from grok-build-0.1 (which the Grok
		// Build CLI does not serve) to grok-4.6. Post-#582 the cascade keys
		// off the RESOLVED model, so a haiku-band grok run renders the same
		// overlay set as any other grok-4.6 run: the host, the provider and
		// the concrete id.
		{"xai haiku cascade", "haiku", "grok",
			[]string{"grok", "xai", "grok-4.6"}},
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
	// With no adapter, there is no host segment either: an unknown id and no
	// model at all must resolve NO keys and render base-only — never error, or
	// every local run breaks.
	for _, tt := range []struct{ model, adapter string }{
		{"", ""},
		{"llama-3-70b-local", ""},
	} {
		if keys, _, ok := OverlayKeys(tt.model, tt.adapter); ok || len(keys) > 0 {
			t.Errorf("OverlayKeys(%q, %q) = %v ok=%v, want no keys", tt.model, tt.adapter, keys, ok)
		}
	}
}

// TestOverlayKeysHostSegment is the ADR-016 amendment's central claim
// (ADR-022 §14): the host key is the execution adapter itself, and it
// survives even when the model below it does not resolve — an unknown model,
// a local provider (no registry entry, by design), or no model at all. That
// is what makes an `opencode` host overlay reachable in the first place:
// `opencode` is not a provider, so nothing in the pre-amendment two-segment
// cascade could ever key an overlay to "every opencode run, whatever the
// model". Reverting OverlayKeys to the old `[m.Provider, m.ID]` cascade turns
// every one of these red, because none of them would resolve any key at all.
func TestOverlayKeysHostSegment(t *testing.T) {
	t.Run("host key leads and survives an unresolved local model", func(t *testing.T) {
		for _, tt := range []struct {
			name, model, adapter string
		}{
			{"ollama, tier band", "opus", "ollama"},
			{"lm-studio, tier band", "sonnet", "lm-studio"},
			{"opencode, no model at all", "", "opencode"},
			{"opencode, local lmstudio provider key", "lmstudio/qwen/qwen3.8-27b", "opencode"},
		} {
			t.Run(tt.name, func(t *testing.T) {
				keys, _, ok := OverlayKeys(tt.model, tt.adapter)
				if ok {
					t.Fatalf("OverlayKeys(%q, %q) resolved a descriptor, want unresolved", tt.model, tt.adapter)
				}
				if len(keys) != 1 || keys[0] != tt.adapter {
					t.Errorf("keys = %v, want exactly [%q] (the host key alone)", keys, tt.adapter)
				}
			})
		}
	})

	t.Run("opencode resolves provider and id from the dispatch string, not the raw model", func(t *testing.T) {
		keys, descriptor, ok := OverlayKeys("xai/grok-4.6", "opencode")
		if !ok {
			t.Fatalf("OverlayKeys(%q, %q) did not resolve", "xai/grok-4.6", "opencode")
		}
		want := []string{"opencode", "xai", "grok-4.6"}
		if strings.Join(keys, ",") != strings.Join(want, ",") {
			t.Errorf("keys = %v, want %v", keys, want)
		}
		if descriptor.ID != "grok-4.6" || descriptor.Provider != "xai" {
			t.Errorf("descriptor = %+v, want provider xai id grok-4.6", descriptor)
		}
	})

	t.Run("host and a same-named provider are independent, not merged", func(t *testing.T) {
		// adapter "lm-studio" IS ALSO a provider name for every other adapter
		// (ProviderForAdapter("lm-studio") == "lm-studio"), but lm-studio never
		// has a registry row, so the provider position can never be populated
		// from a real resolution — the host position is the only way this
		// string ever appears as a key. Dedup must not lose it.
		keys, _, ok := OverlayKeys("", "lm-studio")
		if ok {
			t.Fatalf("OverlayKeys(%q, %q) resolved, want unresolved (empty model)", "", "lm-studio")
		}
		if len(keys) != 1 || keys[0] != "lm-studio" {
			t.Errorf("keys = %v, want exactly [\"lm-studio\"]", keys)
		}
	})
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
		"_includes/batch-detection.md",
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

	unconditional := "> **Read `_includes/batch-detection.md` (same directory as this SKILL.md) now"
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
// The scoping lives in the cascade, not in prose. The composer collects BOTH
// skills/_shared/_overlays/<key>.md and skills/<skill>/_overlays/<key>.md and
// joins them into one "## Model Adaptation" section (render.go runs two collect
// loops, shared then skill-specific), so a block that belongs to two stages
// ships as a skill-specific fragment in exactly those two skill directories and
// nothing is duplicated. Folding those two fragments back into the shared
// overlay makes the absence half below fail for the other five stages; deleting
// them makes the presence half fail for feature-dev and feature-validate.
//
// The two stages are chosen on the vendor's own scoping axis, loop shape: the
// symptom is one tool call per turn in coding and computer-use loops where the
// next independent calls are implied by the task rather than explicitly
// requested, and the cost is extra turns (tokens, round trips, wall-clock)
// rather than worse answers. feature-dev and feature-validate are the
// pipeline's only stages of that shape. No measurement gates the choice and
// none could: diagnostics.ToolCallRecord (internal/diagnostics/exit_record.go)
// carries no assistant-turn identifier, so the share of turns holding more than
// one tool call is not derivable even from a fully populated record.
func TestFableBatchingNudgeIsStageScoped(t *testing.T) {
	root := filepath.Join("..", "..", "skills")
	if _, err := os.Stat(root); err != nil {
		t.Skipf("skills/ not present: %v", err)
	}
	const nudge = "First privately list what you need next"
	wantsNudge := map[string]bool{"feature-dev": true, "feature-validate": true}

	for stage, dir := range StageSkillDirs {
		frag := filepath.Join(root, dir, "_overlays", "claude-fable-5-1.md")
		_, err := os.Stat(frag)
		if wantsNudge[stage] && err != nil {
			t.Errorf("%s: the skill-specific fragment %s must exist: %v", stage, frag, err)
		}
		if !wantsNudge[stage] && err == nil {
			t.Errorf("%s: unexpected skill-specific fragment %s — the nudge is scoped to feature-dev and feature-validate", stage, frag)
		}

		res := mustRender(t, Options{
			Stage:       stage,
			Model:       "claude-fable-5-1",
			SkillsRoots: []string{root},
		})
		want := 0
		if wantsNudge[stage] {
			want = 1
		}
		if got := strings.Count(res.Content, nudge); got != want {
			t.Errorf("%s: rendered prompt carries the batching nudge %d time(s), want %d", stage, got, want)
		}
	}
}

// ─── Host overlay segment (#1636, ADR-016 amendment / ADR-022 §14) ──────────

// realSkillsRoot is the real skills/ tree, or "" with the test skipped when it
// is not present (a `go build` of just this package, outside the repo).
func realSkillsRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join("..", "..", "skills")
	if _, err := os.Stat(root); err != nil {
		t.Skipf("skills/ not present: %v", err)
	}
	return root
}

// TestOpenCodeHostOverlayApplies is the AC's golden render: an opencode
// dispatch to a local model with no registry entry still gets the opencode
// host overlay, and the envelope's resolved keys start with the host key.
func TestOpenCodeHostOverlayApplies(t *testing.T) {
	root := realSkillsRoot(t)
	res := mustRender(t, Options{
		Stage:       "feature-dev",
		Model:       "lmstudio/qwen/qwen3.8-27b",
		Adapter:     "opencode",
		SkillsRoots: []string{root},
	})
	if len(res.Keys) == 0 || res.Keys[0] != "opencode" {
		t.Fatalf("resolved_keys = %v, want it to start with the host key %q", res.Keys, "opencode")
	}
	var gotHostsFragment bool
	for _, f := range res.Fragments {
		if f.Key == "opencode" && strings.Contains(filepath.ToSlash(f.Path), "_overlays/hosts/opencode.md") {
			gotHostsFragment = true
		}
	}
	if !gotHostsFragment {
		t.Errorf("fragments = %v, want one sourced from _overlays/hosts/opencode.md", res.Fragments)
	}
	if !strings.Contains(res.Content, "OpenCode is the execution host") {
		t.Error("composed content is missing the opencode host overlay text")
	}
}

// TestOpenCodeXaiGrokDoesNotGetGrokHostOverlay is the AC's negative golden
// render: opencode dispatching an xai model must not pick up the Grok-Build
// host prose or the --effort flag instruction, both of which now live only in
// hosts/grok.md, keyed to the "grok" adapter — never to "opencode".
func TestOpenCodeXaiGrokDoesNotGetGrokHostOverlay(t *testing.T) {
	root := realSkillsRoot(t)
	res := mustRender(t, Options{
		Stage:       "feature-dev",
		Model:       "xai/grok-4.6",
		Adapter:     "opencode",
		SkillsRoots: []string{root},
	})
	if res.ResolvedModel != "grok-4.6" || res.Provider != "xai" {
		t.Fatalf("resolved model/provider = %q/%q, want grok-4.6/xai", res.ResolvedModel, res.Provider)
	}
	for _, forbidden := range []string{"Grok Build is the execution host", "piped stdin", "--effort"} {
		if strings.Contains(res.Content, forbidden) {
			t.Errorf("opencode+xai/grok-4.6 render leaked grok-host text %q:\n%s", forbidden, res.Content)
		}
	}
	// The opencode host overlay still applies — this is a different adapter,
	// not "no host overlay at all".
	if !strings.Contains(res.Content, "OpenCode is the execution host") {
		t.Error("opencode host overlay missing from an opencode dispatch")
	}
	// The model-level grok-4.6 fragment (now host-prose-free) still applies:
	// it is keyed to the concrete id, not the host.
	if !strings.Contains(res.Content, "Thinking is on by default") {
		t.Error("grok-4.6 model-level overlay missing from an opencode dispatch of that model")
	}
}

// TestGrokAdapterCarriesEverySentenceFromBeforeTheSplit is the AC's positive
// golden render for the adapter the prose used to be keyed to directly: every
// sentence xai.md and the pre-split grok-4.6.md carried is still present,
// just sourced from hosts/grok.md plus the trimmed grok-4.6.md. Captured from
// a real `nightgauge skill render --stage feature-dev --adapter grok --model
// grok-4.6` run before this change (see red_green in the PR description).
func TestGrokAdapterCarriesEverySentenceFromBeforeTheSplit(t *testing.T) {
	root := realSkillsRoot(t)
	res := mustRender(t, Options{
		Stage:       "feature-dev",
		Model:       "grok-4.6",
		Adapter:     "grok",
		SkillsRoots: []string{root},
	})
	// Every sentence (or, where a sentence was split at a clause boundary,
	// every clause) the pre-split xai.md + grok-4.6.md pair carried.
	wantSubstrings := []string{
		"Grok Build is the execution host",
		"Prefer the deterministic `nightgauge`\nbinary for board, state, and forge operations",
		"Headless Grok does not\nread piped stdin as the prompt",
		"Do not depend on Claude Stop hooks or\n`AskUserQuestion`",
		"If a decision is\nundecidable without the operator, fail the stage with a clear reason",
		"Subagent fan-out is optional",
		"Grok 4.6 is the default Grok Build model",
		"Thinking is on by default",
		"Keep `--effort` at the\nresolved Nightgauge envelope",
		"Verification and delegation propensity are\nhigh",
		"Prefer one thorough\npass over narrating every tool call",
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(res.Content, want) {
			t.Errorf("grok+grok-4.6 render is missing pre-split text %q", want)
		}
	}
	var fromHosts, fromModel bool
	for _, f := range res.Fragments {
		p := filepath.ToSlash(f.Path)
		if f.Key == "grok" && strings.Contains(p, "_overlays/hosts/grok.md") {
			fromHosts = true
		}
		if f.Key == "grok-4.6" && strings.Contains(p, "_overlays/grok-4.6.md") && !strings.Contains(p, "/hosts/") {
			fromModel = true
		}
	}
	if !fromHosts {
		t.Errorf("fragments = %v, want one sourced from _overlays/hosts/grok.md", res.Fragments)
	}
	if !fromModel {
		t.Errorf("fragments = %v, want one sourced from _overlays/grok-4.6.md", res.Fragments)
	}
	// The old provider-keyed xai.md is gone — nothing should be sourced from
	// a plain (non-hosts) xai.md any more.
	for _, f := range res.Fragments {
		if f.Key == "xai" {
			t.Errorf("fragment %v still resolves a provider-keyed xai overlay, which #1636 deleted", f)
		}
	}
}

// TestClaudeCodexGeminiRendersUnchanged pins byte-for-byte captures taken
// from this exact tree BEFORE #1636's change (see red_green in the PR
// description), for the three adapters that ship no host overlay file. The
// host segment now resolves an extra key for each of them, but since no
// hosts/<adapter>.md exists, nothing new is collected and the composed text
// must be identical to the pre-change capture.
func TestClaudeCodexGeminiRendersUnchanged(t *testing.T) {
	root := realSkillsRoot(t)
	// Render resolves _includes paths to their absolute filesystem form, which
	// bakes the checkout's own absolute path into the composed text. The
	// golden captures below are pinned against a portable placeholder instead
	// of a literal machine path, so this test passes on any checkout (a
	// contributor's machine or a CI runner) rather than only the one it was
	// captured on.
	absRoot, err := filepath.Abs(root)
	if err != nil {
		t.Fatalf("abs skills root: %v", err)
	}
	repoRoot := filepath.Dir(absRoot)
	const placeholder = "<REPO_ROOT>"
	for _, tt := range []struct {
		stage, adapter, model, golden string
	}{
		{"feature-dev", "claude-headless", "claude-sonnet-5", "testdata/host-overlay/feature-dev_claude-headless_claude-sonnet-5.pre-1636.txt"},
		{"feature-dev", "codex", "gpt-5.6-sol", "testdata/host-overlay/feature-dev_codex_gpt-5.6-sol.pre-1636.txt"},
		{"feature-dev", "gemini", "", "testdata/host-overlay/feature-dev_gemini_none.pre-1636.txt"},
		{"pr-merge", "claude-headless", "claude-sonnet-5", "testdata/host-overlay/pr-merge_claude-headless_claude-sonnet-5.pre-1636.txt"},
	} {
		t.Run(tt.golden, func(t *testing.T) {
			want, err := os.ReadFile(tt.golden)
			if err != nil {
				t.Fatalf("read golden capture: %v", err)
			}
			res := mustRender(t, Options{
				Stage: tt.stage, Model: tt.model, Adapter: tt.adapter, SkillsRoots: []string{root},
			})
			got := strings.ReplaceAll(res.Content, repoRoot, placeholder)
			if got != string(want) {
				t.Errorf("%s/%s/%s render changed after the host segment landed:\n--- got ---\n%s", tt.stage, tt.adapter, tt.model, got)
			}
			if len(res.Fragments) != 0 {
				t.Errorf("%s/%s/%s: unexpected fragments applied: %v", tt.stage, tt.adapter, tt.model, res.Fragments)
			}
		})
	}
}

// TestHostAndProviderNamespacesDoNotCollide proves the AC's namespace claim:
// an overlay key that names a host is resolved from _overlays/hosts/, never
// from the plain _overlays/ a same-named provider would use, and vice versa.
// "lm-studio" is deliberately both: an adapter name (a real, registered
// execution adapter) and, for every OTHER adapter, the provider name
// ProviderForAdapter resolves it to.
func TestHostAndProviderNamespacesDoNotCollide(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "nightgauge-feature-dev", bodyWithContextIncludes)
	write(t, filepath.Join(root, "_shared", "_overlays", "lm-studio.md"), "PLAIN-PROVIDER-LMSTUDIO\n")
	write(t, filepath.Join(root, "_shared", "_overlays", "hosts", "lm-studio.md"), "HOST-LMSTUDIO\n")

	// adapter="lm-studio" makes "lm-studio" the HOST key. It must read only
	// the hosts/ copy, never the plain provider-styled file of the same name.
	res := mustRender(t, Options{Stage: "feature-dev", Adapter: "lm-studio", SkillsRoots: []string{root}})
	if !strings.Contains(res.Content, "HOST-LMSTUDIO") {
		t.Error("host-keyed render did not apply _overlays/hosts/lm-studio.md")
	}
	if strings.Contains(res.Content, "PLAIN-PROVIDER-LMSTUDIO") {
		t.Error("host-keyed render leaked the plain, non-host _overlays/lm-studio.md — namespaces collided")
	}
	if len(res.Fragments) != 1 || !strings.Contains(filepath.ToSlash(res.Fragments[0].Path), "_overlays/hosts/lm-studio.md") {
		t.Errorf("fragments = %v, want exactly one from _overlays/hosts/lm-studio.md", res.Fragments)
	}
}

// TestOverlayKeyPathSafety is the AC's security bullet: an overlay key never
// becomes a path segment verbatim. ".." is refused outright (with a warning)
// rather than encoded — there is no encoding that makes it safe — and "/" is
// encoded so a key that legitimately contains one (an OpenCode local model's
// bare id can: "qwen/qwen3.8-27b") stays a single path segment instead of
// reaching into a subdirectory.
func TestOverlayKeyPathSafety(t *testing.T) {
	t.Run("directory traversal is refused, not encoded, and reads nothing outside _overlays/", func(t *testing.T) {
		root := t.TempDir()
		writeSkill(t, root, "nightgauge-feature-dev", bodyWithContextIncludes)
		// A file that WOULD be read if ".." ever reached the filesystem call
		// unsanitized: _overlays/../../../etc/passwd.md relative to _shared,
		// i.e. something outside the skills root entirely.
		outside := filepath.Join(filepath.Dir(root), "outside-the-root.md")
		write(t, outside, "SHOULD-NEVER-BE-READ\n")
		defer os.Remove(outside)

		var warnings []string
		res, err := Render(Options{
			Stage: "feature-dev", Adapter: "../../../" + filepath.Base(filepath.Dir(root)) + "/outside-the-root",
			SkillsRoots: []string{root},
			Warn:        func(msg string) { warnings = append(warnings, msg) },
		})
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		if strings.Contains(res.Content, "SHOULD-NEVER-BE-READ") {
			t.Fatal("a \"..\"-carrying key reached outside _overlays/ and was read")
		}
		if len(res.Fragments) != 0 {
			t.Errorf("fragments = %v, want none — the key must be refused before any read", res.Fragments)
		}
		if len(warnings) == 0 {
			t.Error("a refused \"..\"-carrying key should be warned about, not silently dropped")
		}
	})

	t.Run("a slash in a key is encoded to a single path segment, not a subdirectory", func(t *testing.T) {
		safe, ok := overlayKeySafe("qwen/qwen3.8-27b", func(string) {})
		if !ok {
			t.Fatal("overlayKeySafe should accept a key containing a slash")
		}
		if strings.Contains(safe, "/") || strings.Contains(safe, string(filepath.Separator)) {
			t.Errorf("overlayKeySafe(%q) = %q, want no remaining path separator", "qwen/qwen3.8-27b", safe)
		}
		if safe != "qwen__qwen3.8-27b" {
			t.Errorf("overlayKeySafe(%q) = %q, want %q", "qwen/qwen3.8-27b", safe, "qwen__qwen3.8-27b")
		}
	})

	t.Run("a NUL byte is refused like \"..\"", func(t *testing.T) {
		var warned bool
		if _, ok := overlayKeySafe("bad\x00key", func(string) { warned = true }); ok {
			t.Error("overlayKeySafe should refuse a key containing a NUL byte")
		}
		if !warned {
			t.Error("refusing a NUL-carrying key should warn")
		}
	})
}

// TestADR016DocumentsTheHostSegment is the AC's doc check: ADR-016 must carry
// an "Amendment" heading citing ADR-022, and docs/MODEL_ADAPTATION.md must
// mention the hosts/ directory. Deleting either turns this red.
func TestADR016DocumentsTheHostSegment(t *testing.T) {
	adr, err := os.ReadFile(filepath.Join("..", "..", "docs", "decisions", "016-model-aware-skill-overlays.md"))
	if err != nil {
		t.Fatalf("read ADR-016: %v", err)
	}
	headingRE := regexp.MustCompile(`(?m)^#+.*Amendment.*$`)
	if !headingRE.MatchString(string(adr)) {
		t.Error("ADR-016 has no heading containing \"Amendment\"")
	}
	if !strings.Contains(string(adr), "ADR-022") {
		t.Error("ADR-016 does not cite ADR-022")
	}

	guide, err := os.ReadFile(filepath.Join("..", "..", "docs", "MODEL_ADAPTATION.md"))
	if err != nil {
		t.Fatalf("read docs/MODEL_ADAPTATION.md: %v", err)
	}
	if !strings.Contains(string(guide), "hosts/") {
		t.Error("docs/MODEL_ADAPTATION.md does not mention hosts/")
	}
}

// TestOpenCodeHostOverlayToolIDsArePinned is the AC's guardrail: every
// backticked, single-word tool id named in hosts/opencode.md must be one the
// `build` agent actually exposes. The pinned list below is a literal capture
// of `opencode debug agent build` on opencode 1.18.30 (the version ADR-022
// records as observed) — its "tools" object's keys, exactly. If the assumption
// in #1636 (that the tool ids named in hosts/opencode.md are the ones the
// build agent exposes) ever stops holding, this test goes red and ADR-022
// needs the finding recorded before hosts/opencode.md changes again.
func TestOpenCodeHostOverlayToolIDsArePinned(t *testing.T) {
	// `opencode debug agent build` (opencode 1.18.30), `.tools` object keys.
	pinned := map[string]bool{
		"invalid": true, "question": true, "bash": true, "read": true,
		"glob": true, "grep": true, "edit": true, "write": true,
		"task": true, "webfetch": true, "todowrite": true, "skill": true,
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "skills", "_shared", "_overlays", "hosts", "opencode.md"))
	if err != nil {
		t.Fatalf("read hosts/opencode.md: %v", err)
	}
	// A backticked, all-lowercase-letters token is a candidate tool id; this
	// deliberately excludes `opencode.json`, `.opencode/` and `AskUserQuestion`
	// — a dot, a slash or a capital letter is never a bare tool id.
	toolIDRE := regexp.MustCompile("`([a-z]+)`")
	found := toolIDRE.FindAllStringSubmatch(string(data), -1)
	if len(found) == 0 {
		t.Fatal("no backticked tool ids found in hosts/opencode.md — the guardrail has nothing to check")
	}
	for _, m := range found {
		if !pinned[m[1]] {
			t.Errorf("hosts/opencode.md names tool id %q, which opencode 1.18.30's build agent does not expose — "+
				"record the finding and amend ADR-022 before changing the overlay", m[1])
		}
	}
}

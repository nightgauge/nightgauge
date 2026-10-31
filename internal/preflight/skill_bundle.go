// Skill bundle gate (#2761). Validates a built skills tree, the one a VSIX ships
// in dist/skills, by doing what a stage will do with it: every stage is rendered
// from the tree, for every profile and every overlay the tree carries, and every
// skill path the render names must exist under that tree.
//
// The gate exists because the source tree masks all of it. A file the bundler
// forgot resolves here and nowhere else, and overlay absence is normal at render
// time (ADR 016 §2), so a dropped overlay is visible only by comparing the bundle
// with its source. That comparison runs when SkillBundleOptions.SourceRoot is set.
//
// An absent tree is an error and an empty one renders nothing, which is a finding
// per stage: the gate can never pass by having nothing to look at.
//
// Schema version 1 — field names (v, skills_root, source_root, files_checked,
// renders_checked, findings, warnings) are stable.
package preflight

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/nightgauge/nightgauge/internal/models"
	"github.com/nightgauge/nightgauge/internal/skillrender"
)

// Rule identifiers used in the Rule field of a finding.
const (
	// SkillBundleRuleDeadInclude flags an include directive with no target,
	// anywhere in the tree, including files no stage renders.
	SkillBundleRuleDeadInclude = "dead_include"
	// SkillBundleRuleRenderFailed flags a stage that does not compose.
	SkillBundleRuleRenderFailed = "render_failed"
	// SkillBundleRuleUnresolvedPath flags a skill path a render names that
	// does not exist under the tree.
	SkillBundleRuleUnresolvedPath = "unresolved_path"
	// SkillBundleRuleFileSet flags a file on which bundle and source disagree.
	SkillBundleRuleFileSet = "file_set"
)

// SkillBundleResult is the stable JSON output schema for
// `nightgauge preflight skill-bundle`.
type SkillBundleResult struct {
	V              int                  `json:"v"`               // schema version, always 1
	SkillsRoot     string               `json:"skills_root"`     // absolute path of the tree checked
	SourceRoot     string               `json:"source_root"`     // absolute path, empty when not compared
	FilesChecked   int                  `json:"files_checked"`   // files scanned for include directives
	RendersChecked int                  `json:"renders_checked"` // renders attempted
	Findings       []SkillBundleFinding `json:"findings"`        // one entry per distinct fault
	Warnings       []string             `json:"warnings"`        // non-fatal issues
}

// SkillBundleFinding describes one fault in a built tree.
type SkillBundleFinding struct {
	Rule   string `json:"rule"`             // one of the SkillBundleRule constants
	Path   string `json:"path"`             // the file concerned, or the render that failed
	Detail string `json:"detail"`           // what is wrong
	Render string `json:"render,omitempty"` // first render that hit it
}

// SkillBundleOptions controls a single bundle check.
type SkillBundleOptions struct {
	// SkillsRoot is the built tree, required.
	SkillsRoot string
	// SourceRoot is the source skills tree. When set, the file sets compare.
	SourceRoot string
}

// bundleRender is one composition the gate attempts.
type bundleRender struct {
	stage, profile, model, adapter string
}

func (r bundleRender) String() string {
	parts := []string{r.stage}
	if r.profile != "" {
		parts = append(parts, "profile="+r.profile)
	}
	if r.model != "" {
		parts = append(parts, "model="+r.model)
	}
	if r.adapter != "" {
		parts = append(parts, "adapter="+r.adapter)
	}
	return strings.Join(parts, " ")
}

// RunSkillBundleCheck validates the tree at opts.SkillsRoot. Findings are a
// non-error result; an error means the check could not run.
func RunSkillBundleCheck(ctx context.Context, opts SkillBundleOptions) (*SkillBundleResult, error) {
	if opts.SkillsRoot == "" {
		return nil, fmt.Errorf("skills root is required")
	}
	root, err := filepath.Abs(opts.SkillsRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve skills root: %w", err)
	}
	result := &SkillBundleResult{
		V:          1,
		SkillsRoot: root,
		Findings:   []SkillBundleFinding{},
		Warnings:   []string{},
	}

	includes, err := RunSkillIncludesCheck(ctx, SkillIncludesOptions{Root: root, Trees: []string{"."}})
	if err != nil {
		return nil, err
	}
	result.FilesChecked = includes.FilesChecked
	result.Warnings = append(result.Warnings, includes.Warnings...)
	for _, f := range includes.Findings {
		result.Findings = append(result.Findings, SkillBundleFinding{
			Rule:   SkillBundleRuleDeadInclude,
			Path:   f.File,
			Detail: fmt.Sprintf("line %d: include %q resolves to %s, which does not exist", f.Line, f.Target, f.Resolved),
		})
	}

	seen := map[string]bool{}
	report := func(f SkillBundleFinding) {
		key := f.Rule + "\x00" + f.Path + "\x00" + f.Detail
		if !seen[key] {
			seen[key] = true
			result.Findings = append(result.Findings, f)
		}
	}
	for _, r := range bundleRenders(root) {
		result.RendersChecked++
		// Every include supplied, so its text reaches SkillPaths: what fits the
		// default budget changes with the skill, and any of it can be handed over.
		res, err := skillrender.Render(skillrender.Options{
			Stage:          r.stage,
			Model:          r.model,
			Adapter:        r.adapter,
			Profile:        r.profile,
			SkillsRoots:    []string{root},
			IncludeBudget:  skillrender.EveryPhaseInclude,
			SupplyIncludes: true,
		})
		if err != nil {
			// A joined render error is one fault per line.
			for _, line := range strings.Split(err.Error(), "\n") {
				report(SkillBundleFinding{Rule: SkillBundleRuleRenderFailed, Path: r.stage, Detail: line, Render: r.String()})
			}
			continue
		}
		for _, p := range skillrender.SkillPaths(res.Content, filepath.Dir(res.SkillPath), root) {
			if !p.Exists {
				report(SkillBundleFinding{
					Rule:   SkillBundleRuleUnresolvedPath,
					Path:   p.Path,
					Detail: "named by a rendered prompt, not present under the skills root",
					Render: r.String(),
				})
			}
		}
	}

	if opts.SourceRoot != "" {
		source, err := filepath.Abs(opts.SourceRoot)
		if err != nil {
			return nil, fmt.Errorf("resolve source root: %w", err)
		}
		if info, statErr := os.Stat(source); statErr != nil || !info.IsDir() {
			return nil, fmt.Errorf("source root %q is not a readable directory", opts.SourceRoot)
		}
		result.SourceRoot = source
		diffs, err := skillrender.DiffBundleFiles(source, root)
		if err != nil {
			return nil, err
		}
		for _, d := range diffs {
			detail := "in the bundle, not in source"
			if d.Missing {
				detail = "in source, missing from the bundle"
			}
			report(SkillBundleFinding{Rule: SkillBundleRuleFileSet, Path: d.Path, Detail: detail})
		}
	}
	return result, nil
}

// bundleRenders lists every composition the tree supports, in a stable order:
// each stage for the full and compact profiles, base-only and under each overlay
// the tree carries for it. The overlay files, not a model list, drive this, so
// an overlay added later is exercised without touching this gate.
func bundleRenders(root string) []bundleRender {
	stages := make([]string, 0, len(skillrender.StageSkillDirs))
	for stage := range skillrender.StageSkillDirs {
		stages = append(stages, stage)
	}
	sort.Strings(stages)

	shared := filepath.Join(root, "_shared", "_overlays")
	sharedKeys := overlayKeysIn(shared)
	adapters := overlayKeysIn(filepath.Join(shared, "hosts"))

	var renders []bundleRender
	for _, stage := range stages {
		skill := filepath.Join(root, skillrender.StageSkillDirs[stage], "_overlays")
		keys := append(append([]string{}, sharedKeys...), overlayKeysIn(skill)...)
		adapterKeys := append(append([]string{}, adapters...), overlayKeysIn(filepath.Join(skill, "hosts"))...)
		for _, profile := range []string{"", skillrender.ProfileCompact} {
			renders = append(renders, bundleRender{stage: stage, profile: profile})
			for _, model := range modelsForOverlayKeys(keys) {
				renders = append(renders, bundleRender{stage: stage, profile: profile, model: model})
			}
			for _, adapter := range uniqueSorted(adapterKeys) {
				renders = append(renders, bundleRender{stage: stage, profile: profile, adapter: adapter})
			}
		}
	}
	return renders
}

// overlayKeysIn returns the overlay keys of the fragments directly in dir: a
// fragment `<key>.md` and a whole-file override `<key>.SKILL.md` name the same
// key. An absent directory holds none, the normal case (ADR 016 §2).
func overlayKeysIn(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var keys []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".md") {
			continue
		}
		keys = append(keys, strings.TrimSuffix(strings.TrimSuffix(name, ".md"), ".SKILL"))
	}
	return keys
}

// modelsForOverlayKeys maps overlay keys to the models that select them. A key
// is a model id, or a provider, which only a model of that provider reaches; a
// provider name passed as a model would resolve nothing and test only the base.
func modelsForOverlayKeys(keys []string) []string {
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, modelForOverlayKey(key))
	}
	return uniqueSorted(out)
}

func modelForOverlayKey(key string) string {
	provider := ""
	for _, m := range models.All() {
		if m.ID == key {
			return key
		}
		if provider == "" && m.Provider == key {
			provider = m.ID
		}
	}
	if provider != "" {
		return provider
	}
	return key
}

func uniqueSorted(in []string) []string {
	out := slices.Clone(in)
	slices.Sort(out)
	return slices.Compact(out)
}

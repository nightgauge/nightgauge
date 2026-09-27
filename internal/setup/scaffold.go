// Package setup provides deterministic project-bootstrap verbs. The
// ScaffoldToolingResult JSON schema is stable — field names, types, and the
// closed enums for FileOutcome.Key and FileOutcome.Outcome must not change
// after first merge. Skills parse `nightgauge setup scaffold-tooling
// --json` output via fixed jq paths; any breaking change requires
// incrementing the V field.
//
// The scaffold-tooling verb owns five embedded fixed templates (tsconfig,
// vitest, eslint, prettier, ci.yml) and the brownfield-safety invariant
// (never overwrite existing files; for ESLint and Prettier also probe
// legacy filenames). It replaces the ~303-line heredoc block in
// skills/smart-setup/SKILL.md Phase 4.5 (audit row B37).
package setup

import (
	"context"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
)

// templatesFS embeds the five tooling templates at compile time. Files are
// preserved byte-for-byte against the SKILL.md heredocs they replace.
//
//go:embed templates/tsconfig.json templates/vitest.config.ts templates/eslint.config.js templates/prettierrc.json templates/ci.yml.tmpl
var templatesFS embed.FS

// ScaffoldToolingResult is the stable v1 JSON output schema for
// `nightgauge setup scaffold-tooling`. Field names and the closed
// enums for FileOutcome.Key and FileOutcome.Outcome are stable — adding a
// new value requires bumping V.
type ScaffoldToolingResult struct {
	V        int           `json:"v"`        // schema version, always 1
	Workdir  string        `json:"workdir"`  // absolute path
	Selected []string      `json:"selected"` // requested template keys (post-normalize)
	Detected DetectedDeps  `json:"detected"` // dep + version detection report
	Outcomes []FileOutcome `json:"outcomes"` // one entry per requested template
	// CISteps lists the package.json scripts that became `npm run <script>`
	// steps in the emitted ci.yml, in step order. Empty when ci is not
	// selected or no gate script exists.
	CISteps []string `json:"ci_steps"`
	// RunsOn is the runner label written into ci.yml (empty when ci is not
	// selected).
	RunsOn   string   `json:"runs_on"`
	Warnings []string `json:"warnings"` // non-fatal issues
}

// DetectedDeps records what the package.json scan found. Populated even when
// no package.json is present so the report stays self-describing.
type DetectedDeps struct {
	PackageJSONFound bool   `json:"package_json_found"`
	NodeVersion      string `json:"node_version"` // detected major or default "20"
	HasTypeScript    bool   `json:"has_typescript"`
	HasVitest        bool   `json:"has_vitest"`
	HasESLint        bool   `json:"has_eslint"`
	HasPrettier      bool   `json:"has_prettier"`
	// Scripts lists the gate scripts (typecheck, lint, test, build) defined
	// in package.json, in CI step order.
	Scripts []string `json:"scripts"`
}

// FileOutcome records the result of one template emission.
type FileOutcome struct {
	Key     string `json:"key"`     // closed enum (TemplateKey*)
	Path    string `json:"path"`    // path relative to Workdir
	Outcome string `json:"outcome"` // closed enum (Outcome*)
	Reason  string `json:"reason"`  // human-readable detail (may be empty)
	Bytes   int    `json:"bytes"`   // bytes written (0 when skipped)
}

// Template keys. Closed enum — adding a new key requires bumping the schema V.
const (
	TemplateKeyTsconfig = "tsconfig"
	TemplateKeyVitest   = "vitest"
	TemplateKeyESLint   = "eslint"
	TemplateKeyPrettier = "prettier"
	TemplateKeyCI       = "ci"
)

// Outcome values. Closed enum — adding a new value requires bumping V.
const (
	OutcomeCreated           = "created"
	OutcomeSkippedExisting   = "skipped_existing"    // brownfield-safe: target file already present
	OutcomeSkippedMissingDep = "skipped_missing_dep" // template's parent dep missing from package.json
	OutcomeSkippedDisabled   = "skipped_disabled"    // not in --select list
	OutcomeError             = "error"               // write failure (permissions, etc.)
)

// allTemplateKeys is the canonical order used for "select all" and for
// rendering Outcomes deterministically when multiple keys are emitted.
var allTemplateKeys = []string{
	TemplateKeyTsconfig,
	TemplateKeyVitest,
	TemplateKeyESLint,
	TemplateKeyPrettier,
	TemplateKeyCI,
}

// validTemplateKeys is the lookup form of allTemplateKeys — used by Select
// validation.
var validTemplateKeys = map[string]struct{}{
	TemplateKeyTsconfig: {},
	TemplateKeyVitest:   {},
	TemplateKeyESLint:   {},
	TemplateKeyPrettier: {},
	TemplateKeyCI:       {},
}

// ScaffoldToolingOptions controls a single run.
type ScaffoldToolingOptions struct {
	// Workdir is the project root. When empty, the caller's CWD is used.
	Workdir string
	// Select is the comma-list of template keys to emit. When empty or nil,
	// all five keys are selected.
	Select []string
	// DryRun reports outcomes with Bytes set to template length but does not
	// write any files.
	DryRun bool
	// RunsOn is the runner label for the emitted ci.yml. Empty means
	// DefaultRunsOn.
	RunsOn string
	// PolicyProbe, when set, is asked for the repository's GitHub Actions
	// policy so the verb can warn when the emitted workflow would be
	// rejected. It is best-effort: an error (no token access, no remote)
	// only produces an informational warning.
	PolicyProbe func(ctx context.Context, workdir string) (*ActionsPolicy, error)
}

// DefaultRunsOn is the runner used when ScaffoldToolingOptions.RunsOn is empty.
const DefaultRunsOn = "ubuntu-latest"

// RunScaffoldTooling resolves workdir, detects dependencies via
// package.json, and emits each selected template under workdir. Existing
// files are never overwritten — they are recorded as
// OutcomeSkippedExisting. Templates that require a missing devDep are
// recorded as OutcomeSkippedMissingDep. Per-file write errors land in
// Outcomes[i].Outcome=OutcomeError; only hard input errors (unresolvable
// workdir, unknown template key) return non-nil err.
func RunScaffoldTooling(ctx context.Context, opts ScaffoldToolingOptions) (*ScaffoldToolingResult, error) {
	workdir := opts.Workdir
	if workdir == "" {
		var err error
		workdir, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("resolve workdir: %w", err)
		}
	}
	abs, err := filepath.Abs(workdir)
	if err != nil {
		return nil, fmt.Errorf("resolve workdir: %w", err)
	}
	if info, statErr := os.Stat(abs); statErr != nil || !info.IsDir() {
		return nil, fmt.Errorf("workdir %q is not a readable directory", workdir)
	}
	workdir = abs

	selected, err := normalizeSelect(opts.Select)
	if err != nil {
		return nil, err
	}

	det, warnings, err := readPackageJSON(workdir)
	if err != nil {
		return nil, err
	}

	if !det.PackageJSONFound {
		warnings = append(warnings, "package.json not found — only tsconfig and ci templates can be emitted; templates with devDep gates will be skipped")
	}

	result := &ScaffoldToolingResult{
		V:        1,
		Workdir:  workdir,
		Selected: selected,
		Detected: det,
		Outcomes: []FileOutcome{},
		CISteps:  []string{},
		Warnings: warnings,
	}

	runsOn := strings.TrimSpace(opts.RunsOn)
	if runsOn == "" {
		runsOn = DefaultRunsOn
	}
	if strings.ContainsAny(runsOn, "\n\r#") {
		return nil, fmt.Errorf("invalid --runs-on value %q", opts.RunsOn)
	}

	for _, key := range selected {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		oc := emitTemplate(workdir, key, det, runsOn, opts.DryRun)
		result.Outcomes = append(result.Outcomes, oc)
		if key == TemplateKeyCI {
			result.RunsOn = runsOn
			result.CISteps = append([]string{}, det.Scripts...)
			if len(det.Scripts) == 0 {
				result.Warnings = append(result.Warnings, "ci: package.json defines none of the gate scripts (typecheck, lint, test, build) — ci.yml only installs dependencies")
			}
			if w := selfHostedRunnerWarning(runsOn); w != "" {
				result.Warnings = append(result.Warnings, w)
			}
			if opts.PolicyProbe != nil {
				result.Warnings = append(result.Warnings, probePolicy(ctx, opts.PolicyProbe, workdir)...)
			}
		}
	}

	return result, nil
}

// normalizeSelect validates each requested key against the closed enum and
// returns them in canonical order. Empty input expands to all five keys. An
// unknown key returns an error so typos surface fast at the CLI boundary.
func normalizeSelect(in []string) ([]string, error) {
	if len(in) == 0 {
		return append([]string{}, allTemplateKeys...), nil
	}
	seen := map[string]struct{}{}
	out := []string{}
	for _, k := range in {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if _, ok := validTemplateKeys[k]; !ok {
			return nil, fmt.Errorf("unknown template key %q (valid: tsconfig, vitest, eslint, prettier, ci)", k)
		}
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	// Stable canonical order — sort by allTemplateKeys index.
	sort.SliceStable(out, func(i, j int) bool {
		return canonicalIndex(out[i]) < canonicalIndex(out[j])
	})
	return out, nil
}

func canonicalIndex(key string) int {
	for i, k := range allTemplateKeys {
		if k == key {
			return i
		}
	}
	return len(allTemplateKeys)
}

// emitTemplate dispatches a single template key to its concrete emit
// function.
func emitTemplate(workdir, key string, det DetectedDeps, runsOn string, dryRun bool) FileOutcome {
	switch key {
	case TemplateKeyTsconfig:
		return emitTsconfig(workdir, dryRun)
	case TemplateKeyVitest:
		return emitVitest(workdir, det, dryRun)
	case TemplateKeyESLint:
		return emitESLint(workdir, det, dryRun)
	case TemplateKeyPrettier:
		return emitPrettier(workdir, det, dryRun)
	case TemplateKeyCI:
		return emitCI(workdir, det, runsOn, dryRun)
	}
	// normalizeSelect prevents this; defensive fallthrough.
	return FileOutcome{Key: key, Outcome: OutcomeError, Reason: "internal: unknown template key"}
}

func emitTsconfig(workdir string, dryRun bool) FileOutcome {
	target := "tsconfig.json"
	data, err := templatesFS.ReadFile("templates/tsconfig.json")
	if err != nil {
		return FileOutcome{Key: TemplateKeyTsconfig, Path: target, Outcome: OutcomeError, Reason: err.Error()}
	}
	return writeIfAbsent(workdir, TemplateKeyTsconfig, target, []string{target}, data, dryRun, "")
}

func emitVitest(workdir string, det DetectedDeps, dryRun bool) FileOutcome {
	target := "vitest.config.ts"
	if !det.HasVitest {
		return FileOutcome{Key: TemplateKeyVitest, Path: target, Outcome: OutcomeSkippedMissingDep, Reason: "vitest not in dependencies or devDependencies"}
	}
	data, err := templatesFS.ReadFile("templates/vitest.config.ts")
	if err != nil {
		return FileOutcome{Key: TemplateKeyVitest, Path: target, Outcome: OutcomeError, Reason: err.Error()}
	}
	return writeIfAbsent(workdir, TemplateKeyVitest, target, []string{target}, data, dryRun, "")
}

func emitESLint(workdir string, det DetectedDeps, dryRun bool) FileOutcome {
	target := "eslint.config.js"
	if !det.HasESLint {
		return FileOutcome{Key: TemplateKeyESLint, Path: target, Outcome: OutcomeSkippedMissingDep, Reason: "eslint not in dependencies or devDependencies"}
	}
	data, err := templatesFS.ReadFile("templates/eslint.config.js")
	if err != nil {
		return FileOutcome{Key: TemplateKeyESLint, Path: target, Outcome: OutcomeError, Reason: err.Error()}
	}
	// Probe legacy ESLint config filenames in addition to the canonical
	// flat-config target. Mirrors the `[ ! -f .eslintrc.js ] && [ ! -f
	// .eslintrc.json ]` guard from the SKILL.md heredoc.
	probes := []string{target, ".eslintrc.js", ".eslintrc.json"}
	return writeIfAbsent(workdir, TemplateKeyESLint, target, probes, data, dryRun, "eslint config already exists")
}

func emitPrettier(workdir string, det DetectedDeps, dryRun bool) FileOutcome {
	target := ".prettierrc"
	if !det.HasPrettier {
		return FileOutcome{Key: TemplateKeyPrettier, Path: target, Outcome: OutcomeSkippedMissingDep, Reason: "prettier not in dependencies or devDependencies"}
	}
	data, err := templatesFS.ReadFile("templates/prettierrc.json")
	if err != nil {
		return FileOutcome{Key: TemplateKeyPrettier, Path: target, Outcome: OutcomeError, Reason: err.Error()}
	}
	// Probe legacy Prettier config filenames. Mirrors the
	// `[ ! -f .prettierrc.json ] && [ ! -f prettier.config.js ]` guard.
	probes := []string{target, ".prettierrc.json", "prettier.config.js"}
	return writeIfAbsent(workdir, TemplateKeyPrettier, target, probes, data, dryRun, "prettier config already exists")
}

func emitCI(workdir string, det DetectedDeps, runsOn string, dryRun bool) FileOutcome {
	target := ".github/workflows/ci.yml"
	tmplBytes, err := templatesFS.ReadFile("templates/ci.yml.tmpl")
	if err != nil {
		return FileOutcome{Key: TemplateKeyCI, Path: target, Outcome: OutcomeError, Reason: err.Error()}
	}
	// Use custom delimiters so GitHub Actions ${{ ... }} expressions survive
	// verbatim — `{{` would otherwise be parsed as a Go template directive.
	tmpl, err := template.New("ci").Delims("<%", "%>").Parse(string(tmplBytes))
	if err != nil {
		return FileOutcome{Key: TemplateKeyCI, Path: target, Outcome: OutcomeError, Reason: fmt.Sprintf("parse ci template: %v", err)}
	}
	var buf strings.Builder
	if err := tmpl.Execute(&buf, newCITemplateData(det, runsOn)); err != nil {
		return FileOutcome{Key: TemplateKeyCI, Path: target, Outcome: OutcomeError, Reason: fmt.Sprintf("render ci template: %v", err)}
	}
	data := []byte(buf.String())

	full := filepath.Join(workdir, target)
	if _, statErr := os.Stat(full); statErr == nil {
		return FileOutcome{Key: TemplateKeyCI, Path: target, Outcome: OutcomeSkippedExisting, Reason: "file already exists"}
	}

	if dryRun {
		return FileOutcome{Key: TemplateKeyCI, Path: target, Outcome: OutcomeCreated, Reason: "dry-run", Bytes: len(data)}
	}

	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return FileOutcome{Key: TemplateKeyCI, Path: target, Outcome: OutcomeError, Reason: fmt.Sprintf("create parent dir: %v", err)}
	}
	if err := os.WriteFile(full, data, 0o644); err != nil {
		return FileOutcome{Key: TemplateKeyCI, Path: target, Outcome: OutcomeError, Reason: err.Error()}
	}
	return FileOutcome{Key: TemplateKeyCI, Path: target, Outcome: OutcomeCreated, Bytes: len(data)}
}

// writeIfAbsent records OutcomeSkippedExisting when any of the probe paths
// already exists under workdir; otherwise writes data to target. existingMsg
// is the human-readable Reason for the skip case (defaults to "file already
// exists" when empty).
func writeIfAbsent(workdir, key, target string, probes []string, data []byte, dryRun bool, existingMsg string) FileOutcome {
	for _, p := range probes {
		if _, err := os.Stat(filepath.Join(workdir, p)); err == nil {
			reason := existingMsg
			if reason == "" {
				reason = "file already exists"
			}
			// Report the actual existing path so the consumer sees which
			// brownfield variant tripped the skip.
			return FileOutcome{Key: key, Path: p, Outcome: OutcomeSkippedExisting, Reason: reason}
		}
	}

	if dryRun {
		return FileOutcome{Key: key, Path: target, Outcome: OutcomeCreated, Reason: "dry-run", Bytes: len(data)}
	}

	full := filepath.Join(workdir, target)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return FileOutcome{Key: key, Path: target, Outcome: OutcomeError, Reason: fmt.Sprintf("create parent dir: %v", err)}
	}
	if err := os.WriteFile(full, data, 0o644); err != nil {
		return FileOutcome{Key: key, Path: target, Outcome: OutcomeError, Reason: err.Error()}
	}
	return FileOutcome{Key: key, Path: target, Outcome: OutcomeCreated, Bytes: len(data)}
}

// selfHostedRunnerWarning reminds the caller that a self-hosted runner only
// picks up jobs from repositories its runner group allows. A new repository is
// not in the group by default, and GitHub leaves the job queued with no error.
func selfHostedRunnerWarning(runsOn string) string {
	if !strings.Contains(runsOn, "self-hosted") {
		return ""
	}
	return "ci: runs-on " + runsOn + " — make sure this repository is in a runner group whose runners carry that label; otherwise jobs stay queued with no error"
}

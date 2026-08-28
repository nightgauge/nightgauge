// Package skillrender is the ONE composer for a stage's executable skill text
// (ADR 016 §2–§4, issue #78).
//
// It owns locating SKILL.md, parsing frontmatter, expanding `<!-- include: -->`
// directives, resolving model overlays, injecting them, and rewriting
// skill-relative paths to absolute ones. Every consumer renders through here —
// the Go scheduler today, the extension and the plugin wrappers via
// `nightgauge skill render`. Overlay resolution added to two implementations
// would have doubled an existing drift liability, so the primitives moved here
// rather than being copied alongside a second mechanism.
//
// Skill LOCATION stays a host responsibility: the binary cannot reproduce the
// extension's bundle discovery (dist/skills/, plus the garbage-collected-bundle
// self-heal from #3883), so callers pass roots and this package owns only
// parsing, expansion, resolution, and composition.
//
// Fail-open is the designed default at every step. An unknown model, a local
// provider with no registry entry, or an unreadable fragment all render
// base-only and exit 0 — exactly today's behavior. A malformed overlay must
// never take down a pipeline run.
package skillrender

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/nightgauge/nightgauge/internal/models"
)

// SchemaVersion is the JSON envelope version for `skill render --json`.
const SchemaVersion = 1

// OverlayAnchor optionally marks where the composed block is injected. When a
// base skill contains it, it wins over the positional fallback.
const OverlayAnchor = "<!-- overlay -->"

// AdaptationHeading titles the injected block. A single, predictable heading
// keeps the block greppable and lets the preflight gate (#80) find it.
const AdaptationHeading = "## Model Adaptation"

// Injection sites reported in --json.
const (
	SiteAnchor       = "anchor"
	SiteAfterContext = "after-context-includes"
	SiteTopOfBody    = "top-of-body"
	SiteNone         = "none"
	SiteWholeFile    = "whole-file-override"
)

// Overlay scopes reported in --json.
const (
	ScopeShared = "shared"
	ScopeSkill  = "skill"
)

// StageSkillDirs maps a renderable skill's stage key to its directory name.
//
// Not every key is a pipeline stage. "issue-refine" is dispatched by the
// autonomous refinement loop rather than by runPipeline, and it is in this map
// for the same reason the six stages are: it is the ONE place that answers
// "which directory holds the SKILL.md for this stage string", and a skill
// missing from it cannot be located by Render no matter which roots are
// searched. Every key here must also be shipped by the marketplace bundle —
// see TestBundleShipsEverySkillTheGoDirectPathRenders.
var StageSkillDirs = map[string]string{
	"issue-pickup":     "nightgauge-issue-pickup",
	"feature-planning": "nightgauge-feature-planning",
	"feature-dev":      "nightgauge-feature-dev",
	"feature-validate": "nightgauge-feature-validate",
	"pr-create":        "nightgauge-pr-create",
	"pr-merge":         "nightgauge-pr-merge",
	"issue-refine":     "nightgauge-issue-refine",
}

// Options parameterize a render.
type Options struct {
	// Stage is the pipeline stage whose skill to render.
	Stage string
	// Model is a concrete registry id or a tier band. Empty renders base-only.
	Model string
	// Adapter selects the provider for tier resolution (claude, codex, …).
	// Empty defaults to anthropic, the pipeline's canonical routing currency.
	Adapter string
	// SkillsRoots are searched in order; first match wins. A root is a
	// directory CONTAINING skill directories (e.g. <repo>/skills).
	SkillsRoots []string
	// Warn receives non-fatal diagnostics (unreadable fragments). nil discards.
	Warn func(string)
}

// Fragment is one overlay file that contributed to the composed block.
type Fragment struct {
	Key   string `json:"key"`
	Scope string `json:"scope"`
	Path  string `json:"path"`
}

// Result is the render output plus the provenance `--json` reports.
type Result struct {
	V             int      `json:"v"`
	Stage         string   `json:"stage"`
	Model         string   `json:"model,omitempty"`
	Provider      string   `json:"provider,omitempty"`
	ResolvedModel string   `json:"resolved_model_id,omitempty"`
	SkillPath     string   `json:"skill_path"`
	SkillName     string   `json:"skill_name,omitempty"`
	AllowedTools  []string `json:"allowed_tools,omitempty"`
	// ProgrammaticTools and MCPTools are the remaining frontmatter tool
	// declarations. Carried so a `--json` consumer building a tool allowlist
	// (#79's plugin wrappers) reads them from the renderer rather than
	// re-parsing the frontmatter itself — the duplication #78 removes.
	ProgrammaticTools []string   `json:"programmatic_tools,omitempty"`
	MCPTools          []string   `json:"mcp_tools,omitempty"`
	Keys              []string   `json:"resolved_keys"`
	Fragments         []Fragment `json:"fragments"`
	InjectionSite     string     `json:"injection_site"`
	WholeFile         string     `json:"whole_file_override,omitempty"`
	Warnings          []string   `json:"warnings"`

	// Content is the composed skill text. Excluded from --json (it goes to
	// stdout) so the envelope stays readable.
	Content string `json:"-"`
}

// Locate finds a stage's SKILL.md across roots, first match wins. Each root is
// tried in both layouts before moving to the next root, so a root that carries
// the plugin-command form is not skipped in favour of a later root.
//
// The returned path is ABSOLUTE even when the caller passed a relative root.
// The whole point of the path rewrite is that a spawned agent resolves read
// directives from a worktree that is not this process's working directory
// (#196); a relative skill dir rewrites `skills/x/` to `skills/x/`, a silent
// no-op that looks like it worked and reproduces the exact failure.
func Locate(stage string, roots []string) (string, error) {
	dir, ok := StageSkillDirs[stage]
	if !ok || dir == "" {
		return "", fmt.Errorf("no skill directory for stage %q", stage)
	}
	if len(roots) == 0 {
		return "", fmt.Errorf("no --skills-root supplied; skill location is the caller's responsibility")
	}
	var tried []string
	for _, root := range roots {
		for _, candidate := range []string{
			filepath.Join(root, dir, "SKILL.md"),
			filepath.Join(root, dir+".md"),
		} {
			if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
				abs, err := filepath.Abs(candidate)
				if err != nil {
					return candidate, nil // cwd unreadable: relative beats failing
				}
				return abs, nil
			}
			tried = append(tried, candidate)
		}
	}
	return "", fmt.Errorf("SKILL.md not found for stage %q (tried %s)", stage, strings.Join(tried, ", "))
}

// OverlayKeys derives the overlay cascade for a model, most general first:
// provider, then the concrete id (ADR 016 §2 as amended by #582 — the band
// segment is retired with the band vocabulary; no band-named overlay file
// ever existed on disk, so the cascade is provider → concrete id. A
// rank-keyed middle segment is deliberately NOT added back: no overlay needs
// it, and pre-customer we delete paths rather than speculate).
//
// Returns nil for an unknown model and for every local provider, which have no
// registry entries by design — those render base-only, which is the documented
// fail-open behavior and exactly what happens today.
func OverlayKeys(model, adapter string) (keys []string, descriptor models.ModelDescriptor, ok bool) {
	if strings.TrimSpace(model) == "" {
		return nil, models.ModelDescriptor{}, false
	}
	provider := "anthropic"
	if adapter != "" {
		provider = models.ProviderForAdapter(adapter)
	}
	m, found := models.Resolve(provider, model)
	if !found {
		return nil, models.ModelDescriptor{}, false
	}
	// Use the RESOLVED descriptor's provider, not the requested one: concrete
	// ids are globally unique, so an exact-id lookup legitimately crosses
	// providers and must key off where the model actually lives.
	ordered := []string{m.Provider, m.ID}
	seen := make(map[string]bool, len(ordered))
	for _, k := range ordered {
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		keys = append(keys, k)
	}
	return keys, m, true
}

// Render composes the executable skill text for (stage, model, roots).
//
// Deterministic: for a fixed input triple and unchanged files the output is
// byte-stable. Every ordering below is explicit — no map iteration reaches the
// output.
func Render(opts Options) (*Result, error) {
	res := &Result{
		V:             SchemaVersion,
		Stage:         opts.Stage,
		Model:         opts.Model,
		Keys:          []string{},
		Fragments:     []Fragment{},
		Warnings:      []string{},
		InjectionSite: SiteNone,
	}
	warn := func(msg string) {
		res.Warnings = append(res.Warnings, msg)
		if opts.Warn != nil {
			opts.Warn(msg)
		}
	}

	skillPath, err := Locate(opts.Stage, opts.SkillsRoots)
	if err != nil {
		return nil, err
	}
	res.SkillPath = skillPath
	skillDir := filepath.Dir(skillPath)

	keys, descriptor, resolved := OverlayKeys(opts.Model, opts.Adapter)
	if resolved {
		res.Provider = descriptor.Provider
		res.ResolvedModel = descriptor.ID
	}
	res.Keys = append(res.Keys, keys...)

	// The whole-file escape hatch replaces the base entirely (ADR 016 §8).
	// Most specific match wins, so walk the cascade backwards.
	for i := len(keys) - 1; i >= 0; i-- {
		override := filepath.Join(skillDir, "_overlays", keys[i]+".SKILL.md")
		raw, err := os.ReadFile(override)
		if err != nil {
			continue
		}
		body, fm := splitFrontmatter(string(raw))
		res.applyFrontmatter(fm)
		res.WholeFile = override
		res.InjectionSite = SiteWholeFile
		body = ExpandIncludes(body, skillDir)
		res.Content = RewriteSkillRelativePaths(body, opts.Stage, skillDir)
		return res, nil
	}

	rawBytes, err := os.ReadFile(skillPath)
	if err != nil {
		return nil, fmt.Errorf("read skill: %w", err)
	}
	body, fm := splitFrontmatter(string(rawBytes))
	res.applyFrontmatter(fm)

	// Collect fragments: shared before skill-specific, general before specific
	// (ADR 016 §2). Read once — a fragment that vanishes between the existence
	// check and the read would otherwise be reported in --json as applied while
	// contributing nothing to the output.
	var texts []string
	collect := func(dir, key, scope string) {
		p := filepath.Join(dir, "_overlays", key+".md")
		text, ok := readFragment(p, warn)
		if !ok {
			return
		}
		res.Fragments = append(res.Fragments, Fragment{Key: key, Scope: scope, Path: p})
		texts = append(texts, text)
	}
	sharedDir := filepath.Join(filepath.Dir(skillDir), "_shared")
	for _, key := range keys {
		collect(sharedDir, key, ScopeShared)
	}
	for _, key := range keys {
		collect(skillDir, key, ScopeSkill)
	}

	if len(texts) > 0 {
		var block strings.Builder
		block.WriteString(AdaptationHeading + "\n\n")
		for i, text := range texts {
			if i > 0 {
				block.WriteString("\n")
			}
			block.WriteString(strings.TrimRight(text, "\n"))
			block.WriteString("\n")
		}
		body, res.InjectionSite = inject(body, block.String())
	}

	// Expansion runs AFTER injection so an overlay may itself carry includes,
	// and so the injection point is computed against the directives that are
	// still present — expansion erases them.
	body = ExpandIncludes(body, skillDir)
	res.Content = RewriteSkillRelativePaths(body, opts.Stage, skillDir)
	return res, nil
}

func readFragment(path string, warn func(string)) (string, bool) {
	st, err := os.Stat(path)
	if err != nil {
		return "", false // absence is the norm, not an error (ADR 016 §2)
	}
	if st.IsDir() {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		// Present but unreadable is worth saying out loud — but never fatal.
		warn(fmt.Sprintf("overlay fragment %s is unreadable, skipping: %v", path, err))
		return "", false
	}
	return string(data), true
}

// contextIncludeRE matches an include of the shared context files that occupy
// the "how to behave" region at the head of every stage skill. The composed
// block lands after the LAST of them, which is read before the procedure
// rather than buried under it — the ordering that matters most for the small
// models overlays exist to help (ADR 016 §3).
var contextIncludeRE = regexp.MustCompile(`(?m)^[ \t]*<!-- include:[^>]*(?:PIPELINE_CONTEXT|AUTONOMY_CONTRACT)\.md[ \t]*-->[ \t]*$`)

// inject places the composed block and reports where it landed.
func inject(body, block string) (string, string) {
	if idx := strings.Index(body, OverlayAnchor); idx >= 0 {
		end := idx + len(OverlayAnchor)
		return body[:idx] + strings.TrimRight(block, "\n") + body[end:], SiteAnchor
	}
	if locs := contextIncludeRE.FindAllStringIndex(body, -1); len(locs) > 0 {
		at := locs[len(locs)-1][1]
		return body[:at] + "\n\n" + strings.TrimRight(block, "\n") + body[at:], SiteAfterContext
	}
	trimmed := strings.TrimLeft(body, "\n")
	return "\n" + strings.TrimRight(block, "\n") + "\n\n" + trimmed, SiteTopOfBody
}

// IncludePattern matches `<!-- include: path -->` directives. Exported so the
// dead-include gate (internal/preflight) resolves targets against the exact
// pattern the composer expands — a gate with its own copy of this regex would
// disagree about what the captured path IS, which is the whole bug in #337's
// malformed `EPIC_HANDLING.md (sub-issue fetch section)` directive: the
// parenthetical is part of capture group 1.
var IncludePattern = regexp.MustCompile(`<!-- include: (.+?) -->`)

// ExpandIncludes replaces include directives with file content, resolved
// relative to the SKILL.md's directory. A missing target is left as-is: the
// same document must remain readable under a host that does not expand.
func ExpandIncludes(content string, skillDir string) string {
	return IncludePattern.ReplaceAllStringFunc(content, func(match string) string {
		subs := IncludePattern.FindStringSubmatch(match)
		if len(subs) < 2 {
			return match
		}
		data, err := os.ReadFile(filepath.Join(skillDir, strings.TrimSpace(subs[1])))
		if err != nil {
			return match
		}
		return string(data)
	})
}

// RewriteSkillRelativePaths rewrites skill-relative read-directive paths to
// absolute host paths. ADR-010 assumed CWD is the nightgauge repo root — only
// true when dogfooding nightgauge itself; cross-repo runs spawn in the target
// repo's worktree, which has no skills/ directory, and agents fell back to
// whole-filesystem scans and stale ~/.codex/skills copies (#196). Only the
// skill's OWN references are rewritten, plus the sibling skills/_shared/ —
// cross-skill references keep naming the other skill.
func RewriteSkillRelativePaths(content string, stage string, skillDir string) string {
	dir := strings.TrimRight(skillDir, "/\\")
	shared := filepath.Join(filepath.Dir(dir), "_shared") + string(filepath.Separator)
	out := strings.ReplaceAll(content, "skills/_shared/", shared)
	// Sorted, not map order: the output must be byte-stable across runs.
	names := []string{filepath.Base(dir), "nightgauge-" + stage, stage}
	sort.Strings(names)
	seen := map[string]bool{}
	for _, name := range names {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = strings.ReplaceAll(out, "skills/"+name+"/", dir+string(filepath.Separator))
	}
	return out
}

// frontmatter holds the SKILL.md header fields the pipeline consumes.
type frontmatter struct {
	Name              string
	AllowedTools      []string
	ProgrammaticTools []string
	MCPTools          []string
}

func (r *Result) applyFrontmatter(fm frontmatter) {
	r.SkillName = fm.Name
	r.AllowedTools = fm.AllowedTools
	r.ProgrammaticTools = fm.ProgrammaticTools
	r.MCPTools = fm.MCPTools
}

// splitFrontmatter strips a leading YAML frontmatter block and returns the
// body plus the parsed header. A document without frontmatter is not an error:
// the plugin-command layout carries none.
func splitFrontmatter(content string) (body string, fm frontmatter) {
	body = content
	if !strings.HasPrefix(content, "---\n") {
		return body, fm
	}
	endIdx := strings.Index(content[4:], "\n---")
	if endIdx < 0 {
		return body, fm
	}
	head := content[4 : 4+endIdx]
	body = content[4+endIdx+4:]
	return body, frontmatter{
		Name:              extractYAMLField(head, "name"),
		AllowedTools:      splitTools(extractYAMLField(head, "allowed-tools")),
		ProgrammaticTools: splitTools(extractYAMLField(head, "programmatic-tools")),
		MCPTools:          splitTools(extractYAMLField(head, "mcp-tools")),
	}
}

// extractYAMLField does line-based extraction of a top-level YAML field.
func extractYAMLField(frontmatter string, key string) string {
	prefix := key + ":"
	for _, line := range strings.Split(frontmatter, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, prefix) {
			return strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, prefix)), "\"'")
		}
	}
	return ""
}

// splitTools splits a space-separated frontmatter tool list.
//
// It reports what the skill DECLARES, verbatim. It used to drop
// AskUserQuestion here, which was a headless-execution policy applied at parse
// time by a composer that has no idea who is calling: the same render serves
// the interactive dispatcher, where AskUserQuestion is exactly the tool the
// user is present to answer. Filtering therefore moved to the headless callers
// (FilterHeadlessTools) so the envelope stays a truthful reading of the
// frontmatter — #79 caught this by way of an interactive test that asserted
// the declared tool survives.
func splitTools(tools string) []string {
	if tools == "" {
		return nil
	}
	return strings.Fields(tools)
}

// FilterHeadlessTools removes tools that cannot work in a non-interactive run.
//
// Exactly one tool qualifies today: the Claude CLI treats an AskUserQuestion
// call under `-p` as a permission denial, so the agent retries it in a loop and
// floods the output (#118, #171, #205). Every headless dispatcher calls this —
// the Go scheduler and the extension's headless path — and the interactive
// dispatchers deliberately do not.
func FilterHeadlessTools(tools []string) []string {
	if len(tools) == 0 {
		return tools
	}
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		if t != "AskUserQuestion" {
			out = append(out, t)
		}
	}
	return out
}

// DefaultRoots is the conventional skills-root list for a workspace checkout.
// Hosts with their own discovery (the extension's bundle path) pass roots
// explicitly instead.
//
// One root, deliberately (#79). This used to also list
// `claude-plugins/nightgauge/commands` as the "plugin-command layout" fallback,
// which never resolved a stage skill: `git log --diff-filter=A` over that
// directory returns exactly one file for the life of the repository
// (`model-routing-report.md`), and ADR 007's #3876 amendment retired command
// wrappers altogether — "the skill IS the slash command". The plugin's own
// skills live at `claude-plugins/nightgauge/skills/<short>/` and are a
// GENERATED mirror of this tree (scripts/install-agent-skills.sh
// sync_plugin_skills), read by Claude Code's loader rather than by a render.
// A second root that cannot match is not a harmless fallback: it is a
// documented-looking answer to "where else do we look?" that costs a stat per
// stage and hides the fact that there is no second source.
//
// THE BUNDLE IS A SECOND SOURCE, AND IT CAN MATCH (#874). The paragraph above
// is still right about `claude-plugins/.../commands`, but its conclusion — one
// root, always — was generalised from that one dead layout to "there is no
// second source", and that is false. The VSCode extension ships the pipeline
// skills at `dist/skills/` beside the binary at `dist/bin/nightgauge`, and the
// TypeScript host already searches both (`resolveSkillRoots`, skillRunner.ts).
// Only the Go-direct path did not, so `nightgauge queue run` and the Go
// autonomous scheduler could not render a single stage skill in any repository
// that does not vendor this repo's `skills/` tree — i.e. every user's repo,
// while the file they needed sat one directory over from the running binary.
//
// ADR 016 §4 makes skill LOCATION the host's job because the bundle path
// depends on the extension host. That holds for the IPC path, where the host
// passes roots explicitly. The Go-direct path has no host to ask, so it asks
// the one thing it does know: where its own executable lives.
//
// The objection above is answered rather than ignored: the bundle root is
// appended ONLY when it resolves to a real directory, so the returned list
// never contains a root that cannot match.
func DefaultRoots(workspaceRoot string) []string {
	exe, err := os.Executable()
	if err != nil {
		exe = ""
	}
	return defaultRoots(workspaceRoot, exe)
}

// defaultRoots is DefaultRoots with the executable path injected, so the
// bundle arm is testable without a real bundle on disk.
func defaultRoots(workspaceRoot, exe string) []string {
	roots := []string{filepath.Join(workspaceRoot, "skills")}
	if bundle := bundleSkillsRoot(exe); bundle != "" {
		roots = append(roots, bundle)
	}
	return roots
}

// bundleSkillsRoot returns the skills tree shipped alongside this binary, or
// "" when there is not one — a plain `go build` output, or a binary installed
// somewhere without the bundle layout, both of which must keep working.
//
// The layout is fixed by the extension's packaging: `<bundle>/dist/bin/nightgauge`
// and `<bundle>/dist/skills/`, so the tree is the executable's grandparent plus
// "skills". Symlinks are resolved first — a binary reached through a symlink
// (a PATH shim, Homebrew) would otherwise compute the root from the link's
// directory instead of the real bundle's.
func bundleSkillsRoot(exe string) string {
	if exe == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	candidate := filepath.Join(filepath.Dir(filepath.Dir(exe)), "skills")
	if info, err := os.Stat(candidate); err == nil && info.IsDir() {
		return candidate
	}
	return ""
}

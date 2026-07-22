package tests

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// maxFileBytes caps per-file reads at 1 MiB. Mirrors the convention used by
// `scan tooling` for pyproject.toml — files above the cap are scored against
// the truncated prefix and a warning is emitted.
const maxFileBytes = 1 << 20

// branchingPattern matches the SKILL.md Phase 3.2 grep-cE expression:
//
//	\b(if|else|switch|case|for|while|try|catch|&&|\|\|)\b
//
// `\b` and `&&`/`||` together require a non-word boundary; Go's RE2 engine
// satisfies this with the same regex once we use literal alternation.
var branchingPattern = regexp.MustCompile(`\b(if|else|switch|case|for|while|try|catch)\b|&&|\|\|`)

// criticalityRule maps a case-insensitive regex over file contents to a
// boost. The first matching rule wins, matching the SKILL.md Phase 3.1 table
// in priority order (highest boost first).
type criticalityRule struct {
	re    *regexp.Regexp
	boost int
}

// criticalityRules reproduce the Phase 3.1 scoring table verbatim. The
// regex bodies are unanchored case-insensitive substrings, mirroring the
// SKILL.md inline `grep -iE '(router\.|app\.(get|post|put|delete|patch)|
// middleware|handler|controller|service|auth|payment|billing|checkout)'`
// — which has no word boundaries, so `service` matches `UserService`
// and `util` matches `utility`. Order matters: higher-priority buckets are
// checked first so an auth-and-payment file gets +40, not +35.
var criticalityRules = []criticalityRule{
	{re: regexp.MustCompile(`(?i)(payment|billing|checkout)`), boost: 40},
	{re: regexp.MustCompile(`(?i)(auth|authorization|session)`), boost: 35},
	{re: regexp.MustCompile(`(?i)(router\.|app\.(get|post|put|delete|patch)|handler|controller)`), boost: 25},
	{re: regexp.MustCompile(`(?i)(middleware|interceptor)`), boost: 20},
	{re: regexp.MustCompile(`(?i)(service|repository|repositories)`), boost: 15},
	{re: regexp.MustCompile(`(?i)(util|helper)`), boost: 5},
}

// scoreCriticality returns the first matching boost from criticalityRules
// against the file's content. Skill prose checks both filename patterns and
// content patterns; we run the regex against content because the file is
// already in memory and the content keywords subsume the filename ones.
func scoreCriticality(content []byte) int {
	for _, rule := range criticalityRules {
		if rule.re.Match(content) {
			return rule.boost
		}
	}
	return 0
}

// scoreComplexity counts branching keywords and maps the count to the
// SKILL.md Phase 3.2 bucket boost.
func scoreComplexity(content []byte) int {
	count := len(branchingPattern.FindAll(content, -1))
	switch {
	case count <= 5:
		return 5
	case count <= 15:
		return 15
	case count <= 30:
		return 25
	default:
		return 35
	}
}

// scoreChangeFrequencyBucket maps a 6-month commit count to the Phase 3.3
// bucket boost.
func scoreChangeFrequencyBucket(commits int) int {
	switch {
	case commits <= 2:
		return 0
	case commits <= 5:
		return 10
	case commits <= 15:
		return 20
	default:
		return 30
	}
}

// scoreDependencyDepthBucket maps an importer count to the Phase 3.4 bucket
// boost.
func scoreDependencyDepthBucket(importers int) int {
	switch {
	case importers <= 1:
		return 0
	case importers <= 5:
		return 10
	case importers <= 10:
		return 20
	default:
		return 30
	}
}

// classifyPriority maps a composite score to a priority bucket per the
// SKILL.md Phase 3.5 table.
func classifyPriority(score int) string {
	switch {
	case score >= 80:
		return "critical"
	case score >= 60:
		return "high"
	case score >= 40:
		return "medium"
	default:
		return "low"
	}
}

// gitChecker memoizes whether the workdir is a git repo so we only emit one
// "git not available" warning per RunRiskScore call (deduplicated per issue
// PRD note).
type gitChecker struct {
	checked bool
	isRepo  bool
	warned  bool
}

func (g *gitChecker) ensure(workdir string) bool {
	if g.checked {
		return g.isRepo
	}
	g.checked = true
	if _, err := exec.LookPath("git"); err != nil {
		return false
	}
	cmd := exec.Command("git", "-C", workdir, "rev-parse", "--is-inside-work-tree")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	g.isRepo = strings.TrimSpace(string(out)) == "true"
	return g.isRepo
}

// scoreChangeFrequency runs `git log --since="6 months ago"` per file and
// counts lines. Non-git workdirs return 0 + emit a single deduped warning
// via the gitChecker.
func scoreChangeFrequency(ctx context.Context, gc *gitChecker, workdir, file string, warnings *[]string) int {
	if !gc.ensure(workdir) {
		if !gc.warned {
			*warnings = append(*warnings, "change_frequency: workdir is not a git repository — all change_frequency scores are 0")
			gc.warned = true
		}
		return 0
	}
	cmd := exec.CommandContext(ctx, "git", "-C", workdir, "log", "--oneline", "--since=6 months ago", "--", file)
	out, err := cmd.Output()
	if err != nil {
		// Per-file failure (e.g., file outside the repo): silent +0.
		return 0
	}
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return 0
	}
	return strings.Count(trimmed, "\n") + 1
}

// scoreDependencyDepth counts files within workdir whose contents reference
// the input file's basename-stem. Mirrors the SKILL.md Phase 3.4 prose: a
// recursive grep across the source-extension allowlist, excluding the file
// itself. This is an approximation, NOT an import-graph traversal.
func scoreDependencyDepth(ctx context.Context, workdir, file string, warnings *[]string) int {
	stem := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
	if stem == "" {
		return 0
	}
	// Compute the canonical absolute path of the file so we can exclude it
	// from the importer count even when the caller passed it as a relative
	// path.
	selfAbs := file
	if !filepath.IsAbs(selfAbs) {
		selfAbs = filepath.Join(workdir, file)
	}
	if abs, err := filepath.Abs(selfAbs); err == nil {
		selfAbs = abs
	}

	// stem must be a real word — search via plain Contains rather than
	// regex to mirror the SKILL.md `grep -rl <basename>` semantics, which
	// is also a substring match with no word boundaries. This intentionally
	// over-counts when basenames collide (documented as an approximation in
	// the plan's risks register).
	stemBytes := []byte(stem)
	count := 0

	walkErr := filepath.WalkDir(workdir, func(path string, d fs.DirEntry, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if path == workdir {
				return nil
			}
			if _, skip := excludedDirs[d.Name()]; skip {
				return fs.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		if !contains(sourceExts, ext) {
			return nil
		}
		// Skip the file itself.
		if abs, absErr := filepath.Abs(path); absErr == nil && abs == selfAbs {
			return nil
		}
		// Read up to maxFileBytes and search for the stem.
		data, readErr := readCappedFile(path)
		if readErr != nil {
			if !errors.Is(readErr, errCappedTruncated) {
				return nil
			}
		}
		if bytes.Contains(data, stemBytes) {
			count++
		}
		return nil
	})
	if walkErr != nil {
		*warnings = append(*warnings, fmt.Sprintf("dependency_depth walk for %s aborted: %v", file, walkErr))
	}
	return count
}

// errCappedTruncated signals that readCappedFile returned a prefix because
// the underlying file exceeded maxFileBytes. The prefix is still usable; the
// error is informational so callers can emit a warning.
var errCappedTruncated = errors.New("file truncated at cap")

// readCappedFile reads up to maxFileBytes from path. When the file is
// larger, the returned slice is the truncated prefix and the error is
// errCappedTruncated.
func readCappedFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, maxFileBytes+1)
	n, err := f.Read(buf)
	if err != nil && err.Error() != "EOF" {
		// io.EOF is wrapped as "EOF" in stdlib message; both n=0+EOF and
		// short reads are non-fatal.
	}
	if n > maxFileBytes {
		return buf[:maxFileBytes], errCappedTruncated
	}
	return buf[:n], nil
}

// RunRiskScore scores each file in opts.Files by combining the four
// SKILL.md Phase 3 sub-scores into a composite (capped at 100) and
// classifies the priority. The result entries are sorted by score
// descending, then by file path ascending, for stable output across runs.
func RunRiskScore(ctx context.Context, opts RiskOptions) (*RiskScoreResult, error) {
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
	workdir = abs

	result := &RiskScoreResult{
		V:        SchemaVersion,
		Workdir:  workdir,
		Entries:  []RiskScoreEntry{},
		Warnings: []string{},
	}

	gc := &gitChecker{}

	for _, raw := range opts.Files {
		file := strings.TrimSpace(raw)
		if file == "" {
			continue
		}
		// Resolve to an absolute path for the file read; preserve the
		// caller-supplied form for the result (rel paths round-trip from
		// `inventory --json | jq '.untested_files[]'`).
		fileAbs := file
		if !filepath.IsAbs(fileAbs) {
			fileAbs = filepath.Join(workdir, file)
		}

		content, readErr := readCappedFile(fileAbs)
		if readErr != nil && !errors.Is(readErr, errCappedTruncated) {
			result.Warnings = append(result.Warnings, fmt.Sprintf("risk_score read %s: %v", file, readErr))
			// Still emit an entry with zero sub-scores so consumers can
			// see which files were attempted.
			result.Entries = append(result.Entries, RiskScoreEntry{
				File:     file,
				Score:    0,
				Priority: classifyPriority(0),
			})
			continue
		}
		if errors.Is(readErr, errCappedTruncated) {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("risk_score: %s exceeded %d bytes — scored truncated prefix", file, maxFileBytes))
		}

		bc := scoreCriticality(content)
		cx := scoreComplexity(content)

		// change_frequency takes the file path as supplied by the caller;
		// `git log -- <path>` resolves it relative to workdir.
		gitPath := file
		if filepath.IsAbs(gitPath) {
			if rel, relErr := filepath.Rel(workdir, gitPath); relErr == nil {
				gitPath = rel
			}
		}
		commits := scoreChangeFrequency(ctx, gc, workdir, gitPath, &result.Warnings)
		cf := scoreChangeFrequencyBucket(commits)

		importers := scoreDependencyDepth(ctx, workdir, file, &result.Warnings)
		dd := scoreDependencyDepthBucket(importers)

		composite := bc + cx + cf + dd
		if composite > 100 {
			composite = 100
		}

		result.Entries = append(result.Entries, RiskScoreEntry{
			File:                file,
			BusinessCriticality: bc,
			Complexity:          cx,
			ChangeFrequency:     cf,
			DependencyDepth:     dd,
			Score:               composite,
			Priority:            classifyPriority(composite),
		})
	}

	sort.SliceStable(result.Entries, func(i, j int) bool {
		if result.Entries[i].Score != result.Entries[j].Score {
			return result.Entries[i].Score > result.Entries[j].Score
		}
		return result.Entries[i].File < result.Entries[j].File
	})

	return result, nil
}

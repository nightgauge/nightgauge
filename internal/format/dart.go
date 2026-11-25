package format

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/intelligence/scopeDriftGate"
	"gopkg.in/yaml.v3"
)

// dartFormatterName is the FormatResult.Formatter value for the Dart path.
const dartFormatterName = "dart format"

const analysisOptionsFile = "analysis_options.yaml"

// defaultBaseCandidates is tried in order when no base is given.
var defaultBaseCandidates = []string{"origin/main", "main", "origin/master", "master"}

// runDart runs `dart fix --apply` and `dart format` over only the .dart files
// the branch changed relative to its base, minus any path matched by the root
// analysis_options.yaml `analyzer.exclude` list (#3585). It never formats the
// whole tree: generated files and deliberately unparseable fixtures stay
// untouched, and a parse error in an excluded file cannot fail the run.
func runDart(ctx context.Context, workdir string, opts Options) (FormatResult, error) {
	ts := time.Now().UTC().Format(time.RFC3339)
	files, err := dartTargets(ctx, workdir, opts.Base)
	if err != nil {
		return FormatResult{Formatter: dartFormatterName, Timestamp: ts}, err
	}
	if len(files) == 0 {
		return FormatResult{
			Ran:       false,
			Formatter: dartFormatterName,
			Output:    "no changed .dart files outside analyzer.exclude",
			Timestamp: ts,
		}, nil
	}
	cmds := make([][]string, 0, len(files)+1)
	for _, f := range files {
		cmds = append(cmds, []string{"dart", "fix", "--apply", "--", f})
	}
	// "--" keeps a path such as "-o.dart" from being parsed as an option.
	cmds = append(cmds, append([]string{"dart", "format", "--"}, files...))
	return runMulti(ctx, workdir, dartFormatterName, cmds)
}

// dartTargets returns the workdir-relative .dart files changed on the branch
// (committed, staged, unstaged and untracked) that exist on disk and are not
// excluded by analyzer.exclude.
func dartTargets(ctx context.Context, workdir, base string) ([]string, error) {
	excludes, err := analyzerExcludes(workdir)
	if err != nil {
		return nil, err
	}
	root, err := gitOutput(ctx, workdir, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("dart format: workdir is not a git repository: %w", err)
	}
	root = strings.TrimSpace(root)
	baseRef, err := resolveDartBase(ctx, workdir, base)
	if err != nil {
		return nil, err
	}
	mergeBase, err := gitOutput(ctx, workdir, "merge-base", "HEAD", baseRef)
	if err != nil {
		return nil, fmt.Errorf("dart format: merge-base HEAD %s: %w", baseRef, err)
	}
	mergeBase = strings.TrimSpace(mergeBase)

	// Both lists are repo-root-relative (--full-name keeps ls-files consistent
	// with diff regardless of the working directory).
	diff, err := gitOutput(ctx, workdir, "diff", "--name-only", "-z", "--diff-filter=ACMR", mergeBase)
	if err != nil {
		return nil, fmt.Errorf("dart format: diff against %s: %w", mergeBase, err)
	}
	untracked, err := gitOutput(ctx, workdir, "ls-files", "--others", "--exclude-standard", "--full-name", "-z")
	if err != nil {
		return nil, fmt.Errorf("dart format: list untracked files: %w", err)
	}

	// git reports the root with symlinks resolved (macOS /var → /private/var),
	// so resolve workdir the same way before taking the relative path.
	absWork, err := filepath.EvalSymlinks(workdir)
	if err != nil {
		return nil, fmt.Errorf("dart format: resolve workdir: %w", err)
	}
	absRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("dart format: resolve repository root: %w", err)
	}

	seen := map[string]bool{}
	var out []string
	for _, p := range append(strings.Split(diff, "\x00"), strings.Split(untracked, "\x00")...) {
		if p == "" || !strings.HasSuffix(p, ".dart") {
			continue
		}
		rel, err := filepath.Rel(absWork, filepath.Join(absRoot, filepath.FromSlash(p)))
		if err != nil {
			return nil, fmt.Errorf("dart format: relativize %s: %w", p, err)
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue // outside the package being formatted
		}
		slash := filepath.ToSlash(rel)
		if seen[slash] || scopeDriftGate.MatchPath(slash, excludes) {
			continue
		}
		// Skip deleted paths, and symlinks: dart rewrites in place, and a link
		// could point outside the worktree.
		if fi, err := os.Lstat(filepath.Join(workdir, rel)); err != nil || !fi.Mode().IsRegular() {
			continue
		}
		seen[slash] = true
		out = append(out, slash)
	}
	sort.Strings(out)
	return out, nil
}

// analyzerExcludes reads analyzer.exclude from workdir/analysis_options.yaml.
// A missing file or key means no excludes; malformed YAML is an error so the
// run fails closed instead of formatting files the project excluded.
func analyzerExcludes(workdir string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(workdir, analysisOptionsFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("dart format: read %s: %w", analysisOptionsFile, err)
	}
	var doc struct {
		Analyzer struct {
			Exclude []string `yaml:"exclude"`
		} `yaml:"analyzer"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("dart format: parse %s: %w", analysisOptionsFile, err)
	}
	return doc.Analyzer.Exclude, nil
}

// resolveDartBase returns the ref the branch diff is taken against: origin/<base>
// when it resolves, else <base>. An empty base resolves the repository default
// branch (origin/HEAD, then main, then master). It never falls back to the tree.
func resolveDartBase(ctx context.Context, workdir, base string) (string, error) {
	if strings.HasPrefix(base, "-") {
		return "", fmt.Errorf("dart format: invalid base branch %q", base)
	}
	candidates := []string{}
	if base != "" {
		candidates = append(candidates, "origin/"+base, base)
	} else {
		if ref, err := gitOutput(ctx, workdir, "symbolic-ref", "-q", "refs/remotes/origin/HEAD"); err == nil {
			if r := strings.TrimSpace(ref); r != "" {
				candidates = append(candidates, r)
			}
		}
		candidates = append(candidates, defaultBaseCandidates...)
	}
	for _, c := range candidates {
		if _, err := gitOutput(ctx, workdir, "rev-parse", "--verify", "-q", c+"^{commit}"); err == nil {
			return c, nil
		}
	}
	if base != "" {
		return "", fmt.Errorf("dart format: base branch %q does not resolve (tried origin/%s, %s)", base, base, base)
	}
	return "", fmt.Errorf("dart format: cannot resolve the default branch (origin/HEAD, %s); set Options.Base",
		strings.Join(defaultBaseCandidates, ", "))
}

// gitOutput runs git in workdir and returns stdout; stderr is folded into the error.
func gitOutput(ctx context.Context, workdir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = workdir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

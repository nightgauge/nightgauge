// Package actualsize captures and buckets the non-circular size measurement
// used by the learning corpus: lines actually changed against the PR base.
package actualsize

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

var sizeOrder = []string{"XS", "S", "M", "L", "XL"}

// defaultThresholds are the upper line-count bounds used by
// github.OutcomeService when the complexity model has no learned override.
var defaultThresholds = map[string]int{
	"XS": 75,
	"S":  250,
	"M":  750,
	"L":  1750,
}

// MeasureLines returns insertions + deletions between the current worktree and
// base. A successful zero-line diff is distinct from an error.
func MeasureLines(worktreeRoot, base string) (int, error) {
	if worktreeRoot == "" {
		return 0, fmt.Errorf("empty worktree root")
	}
	if base == "" {
		base = "main"
	}

	baseRef, err := resolveBaseRef(worktreeRoot, base)
	if err != nil {
		return 0, err
	}
	mergeBase, err := resolveMergeBase(worktreeRoot, baseRef)
	if err != nil {
		return 0, err
	}
	// Diff the merge-base tree against the live worktree. Using baseRef
	// directly would count upstream-only commits when the PR base advances
	// after this feature branch was cut; using baseRef...HEAD would omit
	// uncommitted changes. This form matches the PR changeset and retains the
	// pre-merge worktree as the measured side.
	cmd := exec.Command("git", "diff", "--numstat", mergeBase, "--")
	cmd.Dir = worktreeRoot
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("git diff %s --numstat: %w", mergeBase, err)
	}

	total := 0
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		fields := strings.SplitN(scanner.Text(), "\t", 3)
		if len(fields) < 2 {
			return 0, fmt.Errorf("parse git numstat line %q", scanner.Text())
		}
		// Binary files are reported as "-\t-" and contribute no text lines,
		// matching GitHub's insertion/deletion semantics.
		if fields[0] == "-" || fields[1] == "-" {
			continue
		}
		insertions, err := strconv.Atoi(fields[0])
		if err != nil {
			return 0, fmt.Errorf("parse insertions %q: %w", fields[0], err)
		}
		deletions, err := strconv.Atoi(fields[1])
		if err != nil {
			return 0, fmt.Errorf("parse deletions %q: %w", fields[1], err)
		}
		total += insertions + deletions
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("scan git numstat: %w", err)
	}
	return total, nil
}

func resolveMergeBase(root, baseRef string) (string, error) {
	cmd := exec.Command("git", "merge-base", baseRef, "HEAD")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("resolve merge base for %s: %w", baseRef, err)
	}
	mergeBase := strings.TrimSpace(string(out))
	if mergeBase == "" {
		return "", fmt.Errorf("resolve merge base for %s: empty result", baseRef)
	}
	return mergeBase, nil
}

func resolveBaseRef(root, base string) (string, error) {
	refs := make([]string, 0, 2)
	if !strings.HasPrefix(base, "origin/") {
		// The feature branch is forked from the remote-tracking base. Prefer it
		// when available so a stale local main cannot inflate the measurement
		// with changes that landed upstream before this run began.
		refs = append(refs, "origin/"+base)
	}
	refs = append(refs, base)
	for _, ref := range refs {
		cmd := exec.Command("git", "rev-parse", "--verify", "--quiet", ref+"^{commit}")
		cmd.Dir = root
		if err := cmd.Run(); err == nil {
			return ref, nil
		}
	}
	return "", fmt.Errorf("base branch %q is not available in %s", base, root)
}

// ResolveBaseBranch reads the PR context written by pr-create and falls back
// to main when the context is absent (including a pr-create failure before the
// PR sidecar was produced). roots may contain both the issue worktree and the
// target repository root; the first valid sidecar wins.
func ResolveBaseBranch(issueNumber int, roots ...string) string {
	for _, root := range roots {
		if root == "" {
			continue
		}
		path := filepath.Join(root, ".nightgauge", "pipeline", fmt.Sprintf("pr-%d.json", issueNumber))
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var context struct {
			BaseBranch string `json:"base_branch"`
		}
		if json.Unmarshal(data, &context) == nil && strings.TrimSpace(context.BaseBranch) != "" {
			return strings.TrimSpace(context.BaseBranch)
		}
	}
	return "main"
}

// FiveBucket applies the XS/S/M/L/XL line thresholds used by
// github.OutcomeService. thresholds may override any positive upper bound.
func FiveBucket(lines int, thresholds map[string]int) string {
	effective := make(map[string]int, len(defaultThresholds))
	for size, threshold := range defaultThresholds {
		effective[size] = threshold
	}
	for size, threshold := range thresholds {
		if threshold > 0 {
			effective[size] = threshold
		}
	}
	for _, size := range sizeOrder[:len(sizeOrder)-1] {
		if lines <= effective[size] {
			return size
		}
	}
	return "XL"
}

// LearningBucket converts the five-bucket line measurement into the
// small/medium/large vocabulary used on both sides of the learning corpus's
// size-accuracy comparison. The conversion is the same as applying
// orchestrator.SizeBucketForScore to the canonical XS=1/S=2/M=3/L=5/XL=8
// base scores.
func LearningBucket(workspaceRoot string, lines int) string {
	switch FiveBucket(lines, loadThresholds(workspaceRoot)) {
	case "XS", "S", "M":
		return "small"
	case "L":
		return "medium"
	default:
		return "large"
	}
}

type modelThresholds struct {
	SizeCalibration map[string]struct {
		ExpectedLines int `yaml:"expected_lines"`
	} `yaml:"size_calibration"`
}

func loadThresholds(workspaceRoot string) map[string]int {
	if workspaceRoot == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(workspaceRoot, ".nightgauge", "complexity-model.yaml"))
	if err != nil {
		return nil
	}
	var model modelThresholds
	if yaml.Unmarshal(data, &model) != nil {
		return nil
	}
	thresholds := make(map[string]int, len(model.SizeCalibration))
	for size, calibration := range model.SizeCalibration {
		if calibration.ExpectedLines > 0 {
			thresholds[size] = calibration.ExpectedLines
		}
	}
	return thresholds
}

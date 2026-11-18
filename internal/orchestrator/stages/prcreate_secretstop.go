package stages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/scan"
)

// gateSecurityScan is the gate name pr-create Step 2.5.4 records when the
// diff secret scan blocks the PR (#2677).
const gateSecurityScan = "security_scan"

// scannerDiffSecrets and scannerGitleaks name what tripped a security_scan
// stop. The rescan re-judges every stop with both, whatever the name.
const (
	scannerDiffSecrets = "diff-secrets"
	scannerGitleaks    = "gitleaks"
)

// prCreateGateStop is pr-create-gate-stop-{N}.json, the record the pr-create
// skill writes before a blocking exit. Same shape the skill writes.
type prCreateGateStop struct {
	Gate      string `json:"gate"`
	Scanner   string `json:"scanner,omitempty"`
	Reason    string `json:"reason"`
	HeadSHA   string `json:"head_sha"`
	CreatedAt string `json:"created_at"`
}

func gateStopName(issueNumber int) string {
	return fmt.Sprintf("pr-create-gate-stop-%d.json", issueNumber)
}

// defaultReadGateStop returns the record, nil when none exists, or an error
// when one exists but cannot be read or parsed.
func defaultReadGateStop(workdir string, issueNumber int) (*prCreateGateStop, error) {
	path := pipelineContextPath(workdir, gateStopName(issueNumber))
	if path == "" {
		return nil, nil
	}
	data, err := readFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read gate-stop record: %w", err)
	}
	var rec prCreateGateStop
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("parse gate-stop record: %w", err)
	}
	return &rec, nil
}

func defaultWriteGateStop(workdir string, issueNumber int, rec prCreateGateStop) error {
	path := pipelineContextPath(workdir, gateStopName(issueNumber))
	if path == "" {
		return fmt.Errorf("resolve gate-stop record path for %q", workdir)
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal gate-stop record: %w", err)
	}
	return writeFileAtomic(path, data)
}

func defaultRemoveGateStop(workdir string, issueNumber int) error {
	path := pipelineContextPath(workdir, gateStopName(issueNumber))
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove gate-stop record: %w", err)
	}
	return nil
}

// rescanSecurityStop re-judges a recorded security_scan gate stop (#3419).
//
// With no record, or a record for another gate, it is a no-op. Otherwise:
//
//   - a record naming no commit, or a HEAD that is not snap.Branch, stays
//     blocked: the history cannot be judged;
//   - while the recorded commit is still an ancestor of HEAD, the stop holds:
//     the secret is in the history the push would publish;
//   - once it is not, gitleaks runs over <base>..HEAD (the pre-push
//     invocation) and the diff scan over <base>...HEAD. Both clean clears the
//     record (blocked is nil; the caller creates the PR). Any finding keeps
//     the stop and rewrites the record at the scanned HEAD.
//
// Every check that cannot run — the ancestry query, gitleaks missing or
// failing, the diff scan — fails closed: blocked, record untouched.
func (r *DeterministicPRCreateRunner) rescanSecurityStop(ctx context.Context, workdir string, snap *PRCreateSnapshot) *PRCreateResult {
	r.defaultSecretStopHooks()
	blocked := func(detail string) *PRCreateResult {
		return &PRCreateResult{Path: CreatePathBlocked, Reason: ReasonSecurityScanFailed, Detail: detail}
	}
	rec, err := r.readGateStop(workdir, snap.IssueNumber)
	if err != nil {
		// A record exists but cannot be read: its gate is unknown, so fail
		// closed rather than open a PR around it.
		return blocked(fmt.Sprintf("unreadable %s (remove it after checking the diff): %s", gateStopName(snap.IssueNumber), truncateErr(err, 200)))
	}
	if rec == nil || rec.Gate != gateSecurityScan {
		return nil
	}
	sha := strings.TrimSpace(rec.HeadSHA)
	if sha == "" {
		return blocked(fmt.Sprintf("%s names no commit, so the history cannot be judged (%s)", gateStopName(snap.IssueNumber), rec.Reason))
	}

	base := snap.BaseBranch
	if strings.TrimSpace(base) == "" {
		base = defaultBaseBranch
	}
	if strings.HasPrefix(base, "-") {
		return blocked(fmt.Sprintf("invalid base ref %q; the rescan cannot judge it", base))
	}
	// The scans read HEAD, but the runner pushes snap.Branch: a clean scan only
	// clears the stop when HEAD is that branch.
	if r.git != nil {
		if cur, err := r.git.CurrentBranch(ctx, workdir); err != nil || cur != snap.Branch {
			return blocked(fmt.Sprintf("HEAD is not branch %s; the rescan cannot judge it", snap.Branch))
		}
	}

	// History check: a fix committed on top leaves the secret in history.
	ancestor, err := r.isAncestor(ctx, workdir, sha)
	if err != nil {
		return blocked(fmt.Sprintf("could not check whether %s is in the branch history: %s", shortSHA(sha), truncateErr(err, 200)))
	}
	if ancestor {
		return blocked(fmt.Sprintf("commit %s that tripped the secret scan is still in the branch history; "+
			"rewrite the history to drop it (for example an interactive rebase that removes the secret), then retry (%s)",
			shortSHA(sha), rec.Reason))
	}

	// The scanners read their config and ignore files from the tree being
	// judged, so a branch that edits them could silence its own finding.
	changed, err := r.scannerConfigChanges(ctx, workdir, base)
	if err != nil {
		return blocked(fmt.Sprintf("could not check %s...HEAD for scanner config changes: %s", base, truncateErr(err, 200)))
	}
	if len(changed) > 0 {
		return blocked(fmt.Sprintf("the branch changes scanner config (%s), so a clean rescan cannot clear the stop; "+
			"land that change separately, then retry", strings.Join(changed, ", ")))
	}

	// Re-judge scans: gitleaks over the history, then the changed files.
	leaks, err := r.gitleaksScan(ctx, workdir, base)
	if err != nil {
		if errors.Is(err, errGitleaksUnavailable) {
			return blocked("gitleaks is not installed; the stop cannot be re-judged")
		}
		return blocked(fmt.Sprintf("gitleaks rescan of %s..HEAD could not run: %s", base, truncateErr(err, 200)))
	}
	res, err := r.diffScan(ctx, scan.DiffSecretsOptions{Workdir: workdir, Base: base})
	if err != nil {
		return blocked(fmt.Sprintf("diff secret rescan could not run: %s", truncateErr(err, 200)))
	}
	if leaks == 0 && res.Critical == 0 {
		if err := r.removeGateStop(workdir, snap.IssueNumber); err != nil {
			return blocked(truncateErr(err, 200))
		}
		snap.SecurityScan = "passed"
		snap.SecurityStopCleared = true
		return nil
	}

	scanner := scannerDiffSecrets
	var parts []string
	if leaks > 0 {
		scanner = scannerGitleaks
		parts = append(parts, fmt.Sprintf("%d gitleaks finding(s) in %s..HEAD", leaks, base))
	}
	if res.Critical > 0 {
		parts = append(parts, fmt.Sprintf("%d critical secret finding(s) in the changed files", res.Critical))
	}
	reason := strings.Join(parts, "; ")
	out := blocked(reason)
	if err := r.writeGateStop(workdir, snap.IssueNumber, prCreateGateStop{
		Gate:      gateSecurityScan,
		Scanner:   scanner,
		Reason:    reason,
		HeadSHA:   res.HeadSHA,
		CreatedAt: r.now().UTC().Format(time.RFC3339),
	}); err != nil {
		out.Detail = fmt.Sprintf("%s (record not refreshed: %s)", reason, truncateErr(err, 200))
	}
	return out
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// hexSHA matches an abbreviated or full object name. Anything else in the
// record (a ref name, an option) is refused rather than resolved.
var hexSHA = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)

// defaultIsAncestor reports whether sha is an ancestor of (or equal to) HEAD.
// A sha that names no commit in the object database is not an ancestor: a
// commit that no longer exists cannot be in history. Any other failure is an
// error, which the caller treats as blocked.
func defaultIsAncestor(ctx context.Context, workdir, sha string) (bool, error) {
	if !hexSHA.MatchString(sha) {
		return false, fmt.Errorf("recorded head_sha %q is not a commit id", sha)
	}
	if _, err := gitOutput(ctx, workdir, "rev-parse", "--git-dir"); err != nil {
		return false, fmt.Errorf("not a git repository: %w", err)
	}
	if _, err := gitOutput(ctx, workdir, "rev-parse", "--verify", "--quiet", sha+"^{commit}"); err != nil {
		if gitAnsweredNo(err) {
			return false, nil
		}
		return false, err
	}
	_, err := gitOutput(ctx, workdir, "merge-base", "--is-ancestor", sha, "HEAD")
	if err == nil {
		return true, nil
	}
	if gitAnsweredNo(err) {
		return false, nil
	}
	return false, err
}

// gitAnsweredNo reports git's exit status 1: the answer "no" from
// `rev-parse --verify --quiet` and `merge-base --is-ancestor`, as opposed to
// a failure (128 and other codes).
func gitAnsweredNo(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) && exitErr.ExitCode() == 1
}

// errGitleaksUnavailable marks a missing gitleaks binary, so the stop detail
// can tell "not installed" apart from "failed to run".
var errGitleaksUnavailable = errors.New("gitleaks is not installed")

// defaultGitleaksScan runs gitleaks over <base>..HEAD with the invocation and
// output parsing of the pre-push security gate (internal/hooks/pre_push.go)
// and returns the finding count. A non-zero exit without leak output is an
// error: the scan did not run.
func defaultGitleaksScan(ctx context.Context, workdir, base string) (int, error) {
	bin, err := exec.LookPath("gitleaks")
	if err != nil {
		return 0, errGitleaksUnavailable
	}
	cmd := exec.CommandContext(ctx, bin, "detect",
		"--source", ".",
		"--log-opts", base+"..HEAD",
		"--no-banner",
	)
	cmd.Dir = workdir
	out, err := cmd.CombinedOutput()
	if err == nil {
		return 0, nil
	}
	text := string(out)
	if strings.Contains(text, "leaks found") || strings.Contains(text, "RuleID:") {
		n := strings.Count(text, "RuleID:")
		if n == 0 {
			n = 1
		}
		return n, nil
	}
	// The output is not echoed: a partial run may have printed finding lines,
	// and this error reaches stage failure text.
	return 0, fmt.Errorf("gitleaks exited without a leak report: %w", err)
}

// defaultSecretStopHooks fills any hook a runner built without
// NewDeterministicPRCreateRunner left nil, so the rescan never runs on a nil
// function.
func (r *DeterministicPRCreateRunner) defaultSecretStopHooks() {
	if r.diffScan == nil {
		r.diffScan = scan.RunDiffSecretsScan
	}
	if r.readGateStop == nil {
		r.readGateStop = defaultReadGateStop
	}
	if r.writeGateStop == nil {
		r.writeGateStop = defaultWriteGateStop
	}
	if r.removeGateStop == nil {
		r.removeGateStop = defaultRemoveGateStop
	}
	if r.now == nil {
		r.now = time.Now
	}
	if r.isAncestor == nil {
		r.isAncestor = defaultIsAncestor
	}
	if r.gitleaksScan == nil {
		r.gitleaksScan = defaultGitleaksScan
	}
	if r.scannerConfigChanges == nil {
		r.scannerConfigChanges = defaultScannerConfigChanges
	}
}

// defaultScannerConfigChanges lists the gitleaks config and ignore files that
// <base>...HEAD changes. The scanners read both from the tree they judge.
func defaultScannerConfigChanges(ctx context.Context, workdir, base string) ([]string, error) {
	out, err := gitOutput(ctx, workdir, "diff", "--name-only", base+"...HEAD", "--",
		".gitleaks.toml", ".gitleaksignore")
	if err != nil {
		return nil, err
	}
	return strings.Fields(out), nil
}

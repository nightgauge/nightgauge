package contractrollout

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// PR is a pull request on the forge.
type PR struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	// State is OPEN, MERGED or CLOSED.
	State string `json:"state"`
	// Checks is the head commit's check rollup: SUCCESS, FAILURE, PENDING,
	// ERROR or EXPECTED, or empty when the forge reports none yet.
	Checks string `json:"checks"`
}

// Forge is what a rollout needs from the forge, per repository (owner/name).
type Forge interface {
	Labels(repo string) LabelClient
	// FindPR returns the newest pull request from head, open or merged, or
	// nil when there is none.
	FindPR(ctx context.Context, repo, head string) (*PR, error)
	CreatePR(ctx context.Context, repo, head, base, title, body string) (*PR, error)
}

// Status values, in the order a target moves through them.
const (
	StatusPlanned     = "planned"      // dry run: what applying would change
	StatusCompliant   = "compliant"    // nothing to change
	StatusGateMissing = "gate-missing" // no local gate to run; no PR
	StatusGateFailed  = "gate-failed"  // the local gate failed; no PR
	StatusPROpen      = "pr-open"      // PR open, CI not yet green
	StatusCIFailed    = "ci-failed"    // PR open, CI red
	StatusRolledOut   = "rolled-out"   // local gate passed and CI green, or merged
	StatusError       = "error"        // the rollout could not finish this target
)

// TargetStatus is one row of the status table.
type TargetStatus struct {
	Repo   string `json:"repo"`
	Status string `json:"status"`
	// Gate is the local gate's result this run: passed, failed, missing,
	// skipped (nothing to change), or earlier-run (a PR an earlier run opened,
	// which it opened only after the gate passed). rolled-out never rests on
	// the local gate alone: it needs the PR's CI checks green, or a merge.
	Gate string `json:"gate"`
	// GateCommand is the gate that ran (or, planning, would run), as the
	// repository declares it; empty when it has none.
	GateCommand string           `json:"gate_command,omitempty"`
	PR          *PR              `json:"pr,omitempty"`
	Files       []FileResult     `json:"files,omitempty"`
	Labels      LabelResult      `json:"labels"`
	CIJob       *CIJobResult     `json:"ci_job,omitempty"`
	Changelog   *ChangelogResult `json:"changelog,omitempty"`
	// Worktree is kept, and named here, when the gate failed, so the
	// failure can be inspected where it happened.
	Worktree string `json:"worktree,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

// Options configures a rollout.
type Options struct {
	// Apply pushes and opens pull requests. Without it the rollout plans:
	// it reads each checkout and the forge and changes nothing.
	Apply bool
	// WorkDir holds the per-target worktrees. Required with Apply.
	WorkDir string
	// ResolvePath maps a target to its local checkout.
	ResolvePath func(Target) (string, error)
	// Forge is the forge. Nil skips labels and pull requests (plan only).
	Forge Forge
	// Log receives progress lines. Nil discards them.
	Log io.Writer
}

// Rollout rolls c out to every target, one at a time, and returns one row
// per target. A target that fails does not stop the others.
func Rollout(ctx context.Context, c *Contract, opt Options) []TargetStatus {
	if opt.Log == nil {
		opt.Log = io.Discard
	}
	rows := make([]TargetStatus, 0, len(c.Targets))
	if opt.Apply && (opt.Forge == nil || opt.WorkDir == "") {
		for _, t := range c.Targets {
			rows = append(rows, TargetStatus{Repo: t.Repo, Status: StatusError, Detail: "applying needs a forge and a work directory"})
		}
		return rows
	}
	for _, t := range c.Targets {
		if err := ctx.Err(); err != nil {
			rows = append(rows, TargetStatus{Repo: t.Repo, Status: StatusError, Detail: err.Error()})
			continue
		}
		fmt.Fprintf(opt.Log, "[contract %s] %s\n", c.Name, t.Repo)
		rows = append(rows, rolloutTarget(ctx, c, t, opt))
	}
	return rows
}

func rolloutTarget(ctx context.Context, c *Contract, t Target, opt Options) TargetStatus {
	row := TargetStatus{Repo: t.Repo}
	fail := func(err error) TargetStatus {
		row.Status, row.Detail = StatusError, err.Error()
		return row
	}
	checkout, err := opt.ResolvePath(t)
	if err != nil {
		return fail(err)
	}

	// An earlier run's PR for this contract is this target's answer.
	if opt.Forge != nil {
		pr, err := opt.Forge.FindPR(ctx, t.Repo, c.Branch)
		if err != nil {
			return fail(fmt.Errorf("find PR: %w", err))
		}
		if pr != nil {
			row.PR = pr
			row.Gate = "earlier-run"
			row.Status = statusFromPR(pr)
			row.Detail = "an earlier rollout opened this PR after its local gate passed; CI checks decide rolled-out"
			if lr, err := ProvisionLabels(ctx, opt.Forge.Labels(t.Repo), c.Labels, opt.Apply); err != nil {
				return fail(err)
			} else {
				row.Labels = lr
			}
			return row
		}
		lr, err := ProvisionLabels(ctx, opt.Forge.Labels(t.Repo), c.Labels, opt.Apply)
		if err != nil {
			return fail(err)
		}
		row.Labels = lr
	}

	if !opt.Apply {
		return planTarget(c, checkout, row)
	}

	base := c.BaseFor(t)
	if _, err := git(ctx, checkout, "fetch", "--quiet", "origin", base); err != nil {
		return fail(err)
	}
	wt := filepath.Join(opt.WorkDir, sanitizeRepo(t.Repo))
	if _, err := os.Stat(wt); err == nil {
		return fail(fmt.Errorf("worktree %s already exists: an earlier rollout left it; inspect and remove it", wt))
	}
	if _, err := git(ctx, checkout, "worktree", "add", "--quiet", "--detach", wt, "origin/"+base); err != nil {
		return fail(err)
	}
	removeWorktree := func() {
		if _, err := git(context.Background(), checkout, "worktree", "remove", wt); err != nil {
			row.Detail = strings.TrimSpace(row.Detail + "; worktree not removed: " + err.Error())
		}
	}

	// The gate is read from origin/<base> before any contract file lands,
	// so it is the one the repository's base branch declares.
	gate, err := ResolveGate(wt)
	if err == nil {
		err = checkGateUntouched(wt, gate, c.Files)
	}
	if err != nil {
		removeWorktree()
		return fail(err)
	}
	row.GateCommand = gate.String()

	if row.Files, err = CopyFiles(c.SourceDir(), wt, c.Files, true); err != nil {
		removeAfterDiscard(ctx, checkout, wt)
		return fail(err)
	}
	if c.CIJob != nil {
		r, err := InsertCIJob(wt, *c.CIJob, true)
		if err != nil {
			removeAfterDiscard(ctx, checkout, wt)
			return fail(err)
		}
		row.CIJob = &r
	}
	porcelain, err := git(ctx, wt, "status", "--porcelain")
	if err != nil {
		return fail(err)
	}
	if strings.TrimSpace(porcelain) == "" {
		row.Status, row.Gate = StatusCompliant, "skipped"
		removeWorktree()
		return row
	}
	if row.Changelog, err = addChangelog(c, t.Repo, wt, true); err != nil {
		removeAfterDiscard(ctx, checkout, wt)
		return fail(err)
	}

	if _, err := git(ctx, wt, "switch", "--quiet", "-c", c.Branch); err != nil {
		removeAfterDiscard(ctx, checkout, wt)
		return fail(fmt.Errorf("create branch %s: %w", c.Branch, err))
	}
	if _, err := git(ctx, wt, "add", "-A"); err != nil {
		return fail(err)
	}
	if _, err := git(ctx, wt, "commit", "--quiet", "-m", c.CommitMessage); err != nil {
		return fail(err)
	}

	if gate.Missing() {
		row.Status, row.Gate, row.Worktree = StatusGateMissing, "missing", wt
		row.Detail = noGateDetail + "; the commit is on the worktree's branch"
		return row
	}
	fmt.Fprintf(opt.Log, "[contract %s] %s: running %s\n", c.Name, t.Repo, gate)
	if err := runGate(ctx, wt, gate); err != nil {
		row.Status, row.Gate, row.Worktree = StatusGateFailed, "failed", wt
		row.Detail = err.Error()
		return row
	}
	row.Gate = "passed"

	if _, err := git(ctx, wt, "push", "--quiet", "-u", "origin", c.Branch); err != nil {
		row.Worktree = wt
		return fail(err)
	}
	body, err := c.RenderBody(t.Repo)
	if err != nil {
		row.Worktree = wt
		return fail(err)
	}
	pr, err := opt.Forge.CreatePR(ctx, t.Repo, c.Branch, base, c.PR.Title, body)
	if err != nil {
		row.Worktree = wt
		return fail(fmt.Errorf("open PR: %w", err))
	}
	row.PR = pr
	row.Status = statusFromPR(pr)
	removeWorktree()
	return row
}

// planTarget reports, read-only, what applying would change in checkout.
func planTarget(c *Contract, checkout string, row TargetStatus) TargetStatus {
	var err error
	if row.Files, err = CopyFiles(c.SourceDir(), checkout, c.Files, false); err != nil {
		row.Status, row.Detail = StatusError, err.Error()
		return row
	}
	changed := false
	for _, f := range row.Files {
		changed = changed || f.Changed
	}
	if c.CIJob != nil {
		r, err := InsertCIJob(checkout, *c.CIJob, false)
		if err != nil {
			row.Status, row.Detail = StatusError, err.Error()
			return row
		}
		row.CIJob = &r
		changed = changed || r.Action != "present"
	}
	row.Status = StatusPlanned
	if !changed {
		row.Status = StatusCompliant
		row.Detail = "planned against the checkout's working tree"
		return row
	}
	if row.Changelog, err = addChangelog(c, row.Repo, checkout, false); err != nil {
		row.Status, row.Detail = StatusError, err.Error()
		return row
	}
	gate, err := ResolveGate(checkout)
	if err == nil {
		err = checkGateUntouched(checkout, gate, c.Files)
	}
	switch {
	case err != nil:
		row.Status, row.Detail = StatusError, err.Error()
	case gate.Missing():
		row.Detail = "planned against the checkout's working tree; " + noGateDetail + ", so --apply will stop at gate-missing"
	default:
		row.GateCommand = gate.String()
		row.Detail = "planned against the checkout's working tree; gate: " + row.GateCommand
	}
	return row
}

// noGateDetail is why a repository has no gate to run.
const noGateDetail = "no gate: the repository declares no local_gate in " + GateConfigPath + " and has no " + DefaultGateScript

// addChangelog adds the contract's changelog entry to the repository at
// root, or with write false reports what adding it would do.
func addChangelog(c *Contract, repo, root string, write bool) (*ChangelogResult, error) {
	if c.Changelog == nil {
		return nil, nil
	}
	entry, err := c.RenderChangelogEntry(repo)
	if err != nil {
		return nil, err
	}
	r, err := AddChangelogEntry(root, c.Changelog.Section, entry, write)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// Refresh re-reads every target's pull request from the forge and updates
// its status: a PR whose checks are green, or that merged, is rolled out.
func Refresh(ctx context.Context, c *Contract, f Forge) []TargetStatus {
	rows := make([]TargetStatus, 0, len(c.Targets))
	for _, t := range c.Targets {
		row := TargetStatus{Repo: t.Repo}
		pr, err := f.FindPR(ctx, t.Repo, c.Branch)
		switch {
		case err != nil:
			row.Status, row.Detail = StatusError, err.Error()
		case pr == nil:
			row.Status, row.Detail = StatusPlanned, "no PR from "+c.Branch
		default:
			row.PR, row.Gate, row.Status = pr, "earlier-run", statusFromPR(pr)
		}
		rows = append(rows, row)
	}
	return rows
}

func statusFromPR(pr *PR) string {
	switch {
	case pr.State == "MERGED":
		return StatusRolledOut
	case pr.State == "OPEN" && pr.Checks == "SUCCESS":
		return StatusRolledOut
	case pr.State == "OPEN" && (pr.Checks == "FAILURE" || pr.Checks == "ERROR"):
		return StatusCIFailed
	case pr.State == "CLOSED":
		return StatusError
	default:
		return StatusPROpen
	}
}

// removeAfterDiscard removes a worktree whose edits are being abandoned.
func removeAfterDiscard(ctx context.Context, checkout, wt string) {
	_, _ = git(ctx, wt, "checkout", "--quiet", "--", ".")
	_, _ = git(ctx, wt, "clean", "-fdq")
	_, _ = git(ctx, checkout, "worktree", "remove", wt)
}

var nonRepoChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func sanitizeRepo(repo string) string {
	return nonRepoChars.ReplaceAllString(repo, "__")
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	return run(ctx, dir, "git", args...)
}

func run(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return string(out), fmt.Errorf("%s %s: exit %d: %s", name, strings.Join(args, " "), ee.ExitCode(), tail(string(out), 300))
		}
		return string(out), fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return string(out), nil
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

// WriteTable writes rows as one Markdown table: repo, status, local gate,
// PR, CI checks, detail.
func WriteTable(w io.Writer, rows []TargetStatus) {
	fmt.Fprintln(w, "| Repository | Status | Local gate | PR | CI checks | Detail |")
	fmt.Fprintln(w, "| --- | --- | --- | --- | --- | --- |")
	for _, r := range rows {
		pr, checks := "-", "-"
		if r.PR != nil {
			pr = fmt.Sprintf("[#%d](%s)", r.PR.Number, r.PR.URL)
			if r.PR.State == "MERGED" {
				pr += " merged"
			}
			if r.PR.Checks != "" {
				checks = r.PR.Checks
			}
		}
		gate := r.Gate
		if gate == "" {
			gate = "-"
		}
		detail := r.Detail
		if r.Worktree != "" {
			detail = strings.TrimSpace(detail + " (worktree " + r.Worktree + ")")
		}
		if len(r.Labels.Drift) > 0 {
			detail = strings.TrimSpace(detail + " label drift: " + strings.Join(r.Labels.Drift, ", "))
		}
		fmt.Fprintf(w, "| %s | %s | %s | %s | %s | %s |\n", r.Repo, r.Status, gate, pr, checks,
			strings.ReplaceAll(strings.ReplaceAll(detail, "|", `\|`), "\n", " "))
	}
}

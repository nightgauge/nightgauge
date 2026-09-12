package codexprovision

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// The managed steering block is ephemeral: it is written into the working
// tree's AGENTS.md so a Codex stage can read it, and it must never reach a
// commit (issue 1675). Removing it from the working tree after the stage is not
// enough, because the stage's own agent commits WHILE the block is present
// (`git add -A`, `git commit -a`), and every pipeline-owned rescue commit that
// runs `git add -A` sweeps it in too. This file is the deterministic guard:
//
//   - SanitizeStagedAgentsMd runs between staging and committing at every
//     pipeline-owned commit site and rewrites the STAGED AGENTS.md without the
//     block, leaving the working tree alone (a live stage keeps its steering).
//   - RepairCommittedSteering runs after every stage and before every
//     deterministic push: when HEAD's AGENTS.md carries the block, it adds one
//     commit that removes exactly the block, so the branch tip — the tree a
//     squash merge lands — never carries generated steering.
//
// Mirrors steeringGuard.ts.

// RepairCommitMessage is the subject of the commit RepairCommittedSteering adds.
const RepairCommitMessage = "chore(agents): remove generated Nightgauge steering from AGENTS.md"

// ContainsManagedSteering reports whether content carries either managed
// steering marker. Either marker alone is a leak: a half-block is still
// generated content.
func ContainsManagedSteering(content string) bool {
	return strings.Contains(content, steeringManagedBegin) || strings.Contains(content, steeringManagedEnd)
}

// StripManagedSteering removes the managed block (and any orphaned marker
// line) from content, preserving everything the user wrote.
func StripManagedSteering(content string) string {
	out := stripManagedSteeringBlock(content)
	if !ContainsManagedSteering(out) {
		return out
	}
	// A lone or out-of-order marker: drop the marker lines themselves.
	lines := strings.Split(out, "\n")
	kept := lines[:0]
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if t == steeringManagedBegin || t == steeringManagedEnd {
			continue
		}
		kept = append(kept, l)
	}
	return strings.Join(kept, "\n")
}

// RepairResult reports what RepairCommittedSteering did.
type RepairResult struct {
	Repaired bool   // a repair commit was added
	OldHead  string // HEAD before the repair
	NewHead  string // the repair commit
	Pushed   bool   // the repair was pushed to the branch's upstream
	PushErr  error  // non-nil when a push was attempted and failed
}

const guardGitTimeout = 30 * time.Second

func gitOut(ctx context.Context, dir string, env []string, stdin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, guardGitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

// agentsPathIn resolves dir's AGENTS.md as a repository-root-relative path and
// returns the repository top level. The generator writes AGENTS.md into the
// stage's working directory, which is normally the worktree root.
func agentsPathIn(ctx context.Context, dir string) (top, rel string, err error) {
	t, err := gitOut(ctx, dir, nil, "", "rev-parse", "--show-toplevel")
	if err != nil {
		return "", "", err
	}
	prefix, err := gitOut(ctx, dir, nil, "", "rev-parse", "--show-prefix")
	if err != nil {
		return "", "", err
	}
	return strings.TrimSpace(t), filepath.ToSlash(filepath.Join(strings.TrimSpace(prefix), "AGENTS.md")), nil
}

// blobMode returns the mode git records for rel in the given tree-ish listing
// ("100644" when unknown, the only mode an AGENTS.md normally has).
func blobMode(lsOutput string) string {
	if f := strings.Fields(lsOutput); len(f) > 0 && (f[0] == "100644" || f[0] == "100755") {
		return f[0]
	}
	return "100644"
}

// stageStripped points the index (env selects which one) at the stripped
// content of rel, or removes rel from it when nothing but generated steering
// remains.
func stageStripped(ctx context.Context, top, rel, stripped, mode string, env []string) error {
	if strings.TrimSpace(stripped) == "" {
		_, err := gitOut(ctx, top, env, "", "update-index", "--force-remove", "--", rel)
		return err
	}
	blob, err := gitOut(ctx, top, nil, stripped, "hash-object", "-w", "--stdin")
	if err != nil {
		return err
	}
	_, err = gitOut(ctx, top, env, "", "update-index", "--add", "--cacheinfo",
		fmt.Sprintf("%s,%s,%s", mode, strings.TrimSpace(blob), rel))
	return err
}

// SanitizeStagedAgentsMd rewrites the staged AGENTS.md without the managed
// steering block. Call it after staging and before `git commit` at every
// pipeline-owned commit site. The working tree is untouched. Returns true when
// the index was changed. A directory git cannot read, or an AGENTS.md that is
// not staged, is a no-op: there is nothing a commit could publish.
func SanitizeStagedAgentsMd(ctx context.Context, dir string) (bool, error) {
	top, rel, err := agentsPathIn(ctx, dir)
	if err != nil {
		return false, nil
	}
	staged, err := gitOut(ctx, top, nil, "", "show", ":"+rel)
	if err != nil || !ContainsManagedSteering(staged) {
		return false, nil
	}
	ls, _ := gitOut(ctx, top, nil, "", "ls-files", "-s", "--", rel)
	stripped := StripManagedSteering(staged)
	if strings.TrimSpace(stripped) == "" {
		// Nothing but generated steering is staged. Keep the committed version
		// (itself cleaned) rather than deleting a file HEAD carries.
		if headContent, herr := gitOut(ctx, top, nil, "", "show", "HEAD:"+rel); herr == nil {
			stripped = StripManagedSteering(headContent)
		}
	}
	if err := stageStripped(ctx, top, rel, stripped, blobMode(ls), nil); err != nil {
		return false, fmt.Errorf("sanitize staged AGENTS.md: %w", err)
	}
	return true, nil
}

// RepairCommittedSteering adds one commit removing the managed steering block
// when HEAD's AGENTS.md carries it. The commit is built with plumbing on a
// temporary index, so it runs no hooks and never disturbs the working tree;
// the real index is then brought in line. When push is true and the upstream
// tip is the leaked history plus nothing but repairs, the repair is pushed
// there too, so an open pull request stops carrying the block. A push failure
// is reported in the result, not as an error: the repair commit exists and the
// next pipeline push carries it.
func RepairCommittedSteering(ctx context.Context, dir string, push bool) (RepairResult, error) {
	var res RepairResult
	top, rel, err := agentsPathIn(ctx, dir)
	if err != nil {
		return res, nil
	}
	committed, err := gitOut(ctx, top, nil, "", "show", "HEAD:"+rel)
	if err != nil || !ContainsManagedSteering(committed) {
		// Nothing to repair here — but an earlier local repair may still be
		// unpublished while the upstream tip carries the block.
		if push {
			res.Pushed, res.PushErr = pushRepairIfPublished(ctx, top)
		}
		return res, nil
	}
	oldHead, err := gitOut(ctx, top, nil, "", "rev-parse", "HEAD")
	if err != nil {
		return res, err
	}
	res.OldHead = strings.TrimSpace(oldHead)
	ls, _ := gitOut(ctx, top, nil, "", "ls-tree", "HEAD", "--", rel)

	tmp, err := os.CreateTemp("", "nightgauge-steering-index-*")
	if err != nil {
		return res, err
	}
	tmpIndex := tmp.Name()
	_ = tmp.Close()
	_ = os.Remove(tmpIndex) // read-tree creates it; an empty file is not a valid index
	defer os.Remove(tmpIndex)
	env := []string{"GIT_INDEX_FILE=" + tmpIndex}

	if _, err := gitOut(ctx, top, env, "", "read-tree", "HEAD"); err != nil {
		return res, err
	}
	if err := stageStripped(ctx, top, rel, StripManagedSteering(committed), blobMode(ls), env); err != nil {
		return res, err
	}
	tree, err := gitOut(ctx, top, env, "", "write-tree")
	if err != nil {
		return res, err
	}
	newHead, err := gitOut(ctx, top, nil, "", "commit-tree", strings.TrimSpace(tree), "-p", res.OldHead,
		"-m", RepairCommitMessage,
		"-m", "The pipeline writes this block for Codex stages and removes it afterwards; it was committed while present.")
	if err != nil {
		return res, err
	}
	res.NewHead = strings.TrimSpace(newHead)
	if _, err := gitOut(ctx, top, nil, "", "update-ref", "-m", RepairCommitMessage, "HEAD", res.NewHead, res.OldHead); err != nil {
		return res, err
	}
	res.Repaired = true
	// The real index still holds the leaked blob; point it at the repaired one.
	if _, err := SanitizeStagedAgentsMd(ctx, top); err != nil {
		return res, err
	}

	if push {
		res.Pushed, res.PushErr = pushRepairIfPublished(ctx, top)
	}
	return res, nil
}

// pushRepairIfPublished publishes steering repairs to the branch's upstream
// when the upstream tip still carries the block the local repair removed — the
// case where a pull request already shows it. The push is a fast-forward of
// repair commits only (see PublishSteeringRepair); any other upstream state is
// left to the pipeline's own push, which owns publishing.
func pushRepairIfPublished(ctx context.Context, top string) (bool, error) {
	branch, err := gitOut(ctx, top, nil, "", "symbolic-ref", "--short", "-q", "HEAD")
	if err != nil || strings.TrimSpace(branch) == "" {
		return false, nil
	}
	b := strings.TrimSpace(branch)
	remote, err := gitOut(ctx, top, nil, "", "config", "--get", "branch."+b+".remote")
	if err != nil {
		return false, nil
	}
	merge, err := gitOut(ctx, top, nil, "", "config", "--get", "branch."+b+".merge")
	if err != nil {
		return false, nil
	}
	upstream, err := gitOut(ctx, top, nil, "", "rev-parse", "-q", "--verify", "@{upstream}")
	if err != nil {
		return false, nil
	}
	return PublishSteeringRepair(ctx, top, strings.TrimSpace(remote), strings.TrimSpace(merge), strings.TrimSpace(upstream))
}

// PublishSteeringRepair pushes dir's HEAD to remote:ref when that is exactly
// "publish the steering repair": remoteTip is an ancestor of HEAD, every
// commit between them is a repair commit (RepairCommitMessage), and HEAD
// itself carries no managed block. That covers both a repair made just now and
// a local HEAD repaired earlier whose upstream tip is still leaked. It never
// publishes other unpushed work: returns (false, nil) when the shape does not
// match, so the caller can say why nothing was pushed.
func PublishSteeringRepair(ctx context.Context, dir, remote, ref, remoteTip string) (bool, error) {
	if remote == "" || ref == "" || remoteTip == "" {
		return false, nil
	}
	if _, err := gitOut(ctx, dir, nil, "", "merge-base", "--is-ancestor", remoteTip, "HEAD"); err != nil {
		return false, nil
	}
	subjects, err := gitOut(ctx, dir, nil, "", "log", "--format=%s", remoteTip+"..HEAD")
	if err != nil {
		return false, err
	}
	lines := strings.Split(strings.TrimSpace(subjects), "\n")
	if strings.TrimSpace(subjects) == "" {
		return false, nil
	}
	for _, l := range lines {
		if strings.TrimSpace(l) != RepairCommitMessage {
			return false, nil
		}
	}
	if leaked, err := CommittedSteeringAt(ctx, dir, "HEAD"); err != nil || len(leaked) > 0 {
		return false, err
	}
	pctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(pctx, "git", "-C", dir, "push", remote, "HEAD:"+ref)
	if out, err := cmd.CombinedOutput(); err != nil {
		return false, fmt.Errorf("push steering repair: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return true, nil
}

// CommittedSteeringAt lists every AGENTS.md tracked at rev whose content
// carries the managed steering markers.
func CommittedSteeringAt(ctx context.Context, dir, rev string) ([]string, error) {
	list, err := gitOut(ctx, dir, nil, "", "ls-tree", "-r", "--name-only", "-z", rev)
	if err != nil {
		return nil, err
	}
	var leaked []string
	for _, p := range strings.Split(list, "\x00") {
		if p != "AGENTS.md" && !strings.HasSuffix(p, "/AGENTS.md") {
			continue
		}
		content, err := gitOut(ctx, dir, nil, "", "show", rev+":"+p)
		if err != nil {
			return nil, err
		}
		if ContainsManagedSteering(content) {
			leaked = append(leaked, p)
		}
	}
	return leaked, nil
}

// PRHeadVerdict is GuardPRHead's report on a pull request's head commit.
type PRHeadVerdict struct {
	HeadSHA   string   // the remote head tip that was inspected
	Leaked    []string // AGENTS.md paths at the head that carry the block
	Worktree  string   // local worktree checked out on the head branch, if any
	RepairSHA string   // the repair commit now at the worktree's HEAD
	Pushed    bool     // the repair was pushed to the head branch
	PushErr   error    // why a repair could not be published
}

// GuardPRHead fetches the pull request's head branch from remote and reports
// whether any AGENTS.md at its tip carries the managed steering block. When it
// does and a local worktree is checked out on that branch, the block is
// repaired there and the repair published (PublishSteeringRepair). The caller
// decides what a leaked head means; the pr-merge gate refuses the merge either
// way, because a push changes the head and required checks must re-run.
// An error means the head could not be inspected.
func GuardPRHead(ctx context.Context, workdir, remote, headRef string) (PRHeadVerdict, error) {
	var v PRHeadVerdict
	tracking := "refs/remotes/" + remote + "/" + headRef
	if _, err := gitOut(ctx, workdir, nil, "", "fetch", "-q", remote, "+refs/heads/"+headRef+":"+tracking); err != nil {
		return v, err
	}
	tip, err := gitOut(ctx, workdir, nil, "", "rev-parse", tracking)
	if err != nil {
		return v, err
	}
	v.HeadSHA = strings.TrimSpace(tip)
	if v.Leaked, err = CommittedSteeringAt(ctx, workdir, v.HeadSHA); err != nil || len(v.Leaked) == 0 {
		return v, err
	}
	v.Worktree = worktreeOnBranch(ctx, workdir, headRef)
	if v.Worktree == "" {
		return v, nil
	}
	// The generator only ever writes the root AGENTS.md; a nested one carrying
	// the markers needs a human, and PublishSteeringRepair will refuse it.
	if _, err := RepairCommittedSteering(ctx, v.Worktree, false); err != nil {
		v.PushErr = err
		return v, nil
	}
	if head, err := gitOut(ctx, v.Worktree, nil, "", "rev-parse", "HEAD"); err == nil {
		v.RepairSHA = strings.TrimSpace(head)
	}
	v.Pushed, v.PushErr = PublishSteeringRepair(ctx, v.Worktree, remote, "refs/heads/"+headRef, v.HeadSHA)
	if !v.Pushed && v.PushErr == nil {
		v.PushErr = fmt.Errorf("the worktree's branch is not the remote head plus repair commits only (diverged, or other unpushed work)")
	}
	return v, nil
}

// worktreeOnBranch returns the path of the worktree (of dir's repository) that
// has branch checked out, or "".
func worktreeOnBranch(ctx context.Context, dir, branch string) string {
	out, err := gitOut(ctx, dir, nil, "", "worktree", "list", "--porcelain")
	if err != nil {
		return ""
	}
	var path string
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			path = strings.TrimPrefix(line, "worktree ")
		case line == "branch refs/heads/"+branch:
			return path
		}
	}
	return ""
}

// AfterStage is the post-stage half of provisioning (issue 1675), at parity with the
// TypeScript CodexContextGenerator.cleanup: for the codex adapter it removes
// the managed block from the working tree (deleting AGENTS.md when the block
// was all it held), and for every adapter it repairs a block the stage
// committed. Non-codex adapters never provision a block, but a stage of any
// adapter can commit one left behind by an interrupted Codex stage.
func AfterStage(ctx context.Context, adapterName, workspaceRoot string) (RepairResult, error) {
	if adapterName == "codex" {
		if err := stripWorkingTreeSteering(workspaceRoot); err != nil {
			return RepairResult{}, err
		}
	}
	return RepairCommittedSteering(ctx, workspaceRoot, true)
}

// stripWorkingTreeSteering removes the managed block from the working tree's
// AGENTS.md, deleting the file when nothing else remains.
func stripWorkingTreeSteering(workspaceRoot string) error {
	p := filepath.Join(workspaceRoot, "AGENTS.md")
	content, ok := readFileGracefully(p)
	if !ok || !ContainsManagedSteering(content) {
		return nil
	}
	stripped := StripManagedSteering(content)
	if strings.TrimSpace(stripped) == "" {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove generated AGENTS.md: %w", err)
		}
		return nil
	}
	if err := os.WriteFile(p, []byte(stripped), 0o644); err != nil {
		return fmt.Errorf("strip AGENTS.md steering: %w", err)
	}
	return nil
}

// ShortSHA abbreviates a commit id for log lines.
func ShortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// CommittedSteeringResult is the schema-v1 report of
// `nightgauge preflight managed-steering`. Field names are stable.
type CommittedSteeringResult struct {
	V            int      `json:"v"`
	Root         string   `json:"root"`
	FilesChecked int      `json:"files_checked"`
	Findings     []string `json:"findings"`
	Fixed        []string `json:"fixed"`
}

// FindCommittedSteering reports every AGENTS.md tracked at HEAD that carries
// the managed steering markers — generated content that was committed. With
// fix, each offending file's working-tree copy is rewritten without the block
// (deleted when nothing else remains) and left uncommitted, so the removal
// lands through the repository's normal review. A repository without commits
// has nothing committed and reports clean.
func FindCommittedSteering(ctx context.Context, root string, fix bool) (CommittedSteeringResult, error) {
	res := CommittedSteeringResult{V: 1, Findings: []string{}, Fixed: []string{}}
	top, err := gitOut(ctx, root, nil, "", "rev-parse", "--show-toplevel")
	if err != nil {
		return res, fmt.Errorf("not a git repository: %w", err)
	}
	res.Root = strings.TrimSpace(top)
	if _, err := gitOut(ctx, res.Root, nil, "", "rev-parse", "-q", "--verify", "HEAD"); err != nil {
		return res, nil
	}
	list, err := gitOut(ctx, res.Root, nil, "", "ls-tree", "-r", "--name-only", "-z", "HEAD")
	if err != nil {
		return res, err
	}
	for _, p := range strings.Split(list, "\x00") {
		if p != "AGENTS.md" && !strings.HasSuffix(p, "/AGENTS.md") {
			continue
		}
		res.FilesChecked++
		content, err := gitOut(ctx, res.Root, nil, "", "show", "HEAD:"+p)
		if err != nil {
			return res, err
		}
		if !ContainsManagedSteering(content) {
			continue
		}
		res.Findings = append(res.Findings, p)
		if fix {
			if err := stripWorkingTreeSteering(filepath.Join(res.Root, filepath.Dir(filepath.FromSlash(p)))); err != nil {
				return res, err
			}
			res.Fixed = append(res.Fixed, p)
		}
	}
	return res, nil
}

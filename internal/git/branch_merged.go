package git

import (
	"fmt"
	"os/exec"
	"strings"
)

// MergedVerdict is the answer to "is this branch safe to delete?". It is the Go
// twin of scripts/branch-merged-check.sh and uses the same three outcomes, so
// the CLI sweep and the script cannot disagree about what "merged" means
// (#2259).
type MergedVerdict string

const (
	// VerdictSafeDelete — the branch's content is provably on base: its tip is
	// an ancestor of base, its own files are identical on base, or a merged PR's
	// head is (or has as a parent) the branch tip.
	VerdictSafeDelete MergedVerdict = "SAFE-DELETE"
	// VerdictKeep — the branch carries work base does not have, or is in use.
	VerdictKeep MergedVerdict = "KEEP"
	// VerdictUnknown — undecidable. Never delete on it.
	VerdictUnknown MergedVerdict = "UNKNOWN"
)

// MergedPRLookup reports the head OID of the newest merged PR whose head ref is
// branch, plus that commit's parents. ok=false means "no merged PR known",
// which never authorizes a deletion on its own.
type MergedPRLookup func(branch string) (headSHA string, parents []string, ok bool)

// MergedProofInputs carries the forge facts the proof consults. Every field is
// optional; a missing fact can only turn SAFE-DELETE into KEEP/UNKNOWN, never
// the reverse.
type MergedProofInputs struct {
	// Base is the ref to judge against, e.g. "origin/main".
	Base string
	// OpenPRHeads / OpenPRBases map a branch name to an open PR number that
	// uses it as head or base. Deleting either would close or break that PR.
	OpenPRHeads map[string]int
	OpenPRBases map[string]int
	// EpicIssueState answers the state (OPEN/CLOSED) of the issue behind an
	// epic/<N>-… branch. An error means the forge could not say.
	EpicIssueState func(issue int) (string, error)
	// MergedPR is the merged-PR door. Nil means content-only (conservative).
	MergedPR MergedPRLookup
}

// MergedProof is one judgment: the verdict, a human reason, and the tip that
// was judged (the SHA a deleter should pin to).
type MergedProof struct {
	Verdict    MergedVerdict
	Reason     string
	Tip        string
	RemoteOnly bool
	// LocalTip / OriginTip are the refs the proof covered, "" when that half
	// did not exist. BranchCleanupPinned deletes each half only while it still
	// points there.
	LocalTip  string
	OriginTip string
}

// ProveBranchMerged judges branch against in.Base with the semantics of
// scripts/branch-merged-check.sh. The local ref is judged when it exists,
// otherwise origin's copy — after confirming via ls-remote that the cached
// tracking ref is origin's live tip.
//
// Beyond the script: when both a local and an origin copy exist, the origin
// copy must not carry commits the local tip lacks, because BranchCleanup
// deletes both halves and the proof must cover everything it deletes.
func (s *Service) ProveBranchMerged(branch string, in MergedProofInputs) MergedProof {
	unknown := func(format string, a ...any) MergedProof {
		return MergedProof{Verdict: VerdictUnknown, Reason: fmt.Sprintf(format, a...)}
	}
	if branch == "HEAD" {
		return unknown("branch is HEAD — refuses to judge git's own current-commit pointer")
	}
	if err := validateRefArg("branch", branch); err != nil {
		return unknown("%v", err)
	}
	base := in.Base
	if base == "" {
		return unknown("no base ref given")
	}

	ref := "refs/heads/" + branch
	remoteOnly := false
	note := ""
	tip, ok := s.revParse(ref)
	if !ok {
		trackRef := "refs/remotes/origin/" + branch
		tracking, tok := s.revParse(trackRef)
		if !tok {
			return unknown("no such ref: %s", branch)
		}
		ref, tip, remoteOnly = trackRef, tracking, true
	}
	if _, ok := s.revParse(base); !ok {
		return unknown("no such base ref: %s", base)
	}
	localTip, originTip := tip, ""
	if remoteOnly {
		localTip, originTip = "", tip
	} else if tracking, tok := s.revParse("refs/remotes/origin/" + branch); tok {
		originTip = tracking
	}

	if remoteOnly {
		if branch == strings.TrimPrefix(base, "origin/") {
			return unknown("remote-only ref %s is the base branch's own name (base=%s)", branch, base)
		}
		live, err := s.liveRemoteTip(branch)
		if err != nil || live == "" {
			return unknown("remote-only ref %s — cannot confirm the cached tracking ref %s is origin's live tip", branch, short(tip))
		}
		if live != tip {
			return unknown("remote-only ref %s is STALE — cached %s but origin's live tip is %s; fetch and re-check", branch, short(tip), short(live))
		}
		note = "remote-only ref, judged from the remote tip " + tip + " — "
	} else {
		held, err := s.branchesHeldByWorktrees()
		if err != nil {
			return unknown("cannot list worktrees: %v", err)
		}
		if wt, isHeld := held[branch]; isHeld {
			return MergedProof{Verdict: VerdictKeep, Reason: "checked out in a worktree: " + wt, Tip: tip}
		}
		// The origin copy is deleted too, so it must hold nothing the local
		// tip lacks.
		if tracking, tok := s.revParse("refs/remotes/origin/" + branch); tok && tracking != tip {
			if !s.isAncestor(tracking, tip) {
				return MergedProof{Verdict: VerdictKeep, Tip: tip, Reason: fmt.Sprintf(
					"origin/%s (%s) has commits the local tip (%s) lacks", branch, short(tracking), short(tip))}
			}
		}
	}

	keep := func(reason string) MergedProof {
		return MergedProof{Verdict: VerdictKeep, Reason: note + reason, Tip: tip, RemoteOnly: remoteOnly}
	}
	safe := func(reason string) MergedProof {
		return MergedProof{Verdict: VerdictSafeDelete, Reason: note + reason, Tip: tip, RemoteOnly: remoteOnly,
			LocalTip: localTip, OriginTip: originTip}
	}

	if n, ok := in.OpenPRHeads[branch]; ok {
		return keep(fmt.Sprintf("open PR #%d — deleting this branch would close it", n))
	}
	if n, ok := in.OpenPRBases[branch]; ok {
		return keep(fmt.Sprintf("open PR #%d targets this branch as its base — deleting it would break that PR", n))
	}

	if num, isEpic := epicIssueNumber(branch); isEpic {
		if in.EpicIssueState == nil {
			return MergedProof{Verdict: VerdictUnknown, Tip: tip, RemoteOnly: remoteOnly, Reason: fmt.Sprintf(
				"%sepic branch for issue #%d — cannot confirm the issue is closed (forge lookup unavailable)", note, num)}
		}
		state, err := in.EpicIssueState(num)
		if err != nil || state == "" {
			return MergedProof{Verdict: VerdictUnknown, Tip: tip, RemoteOnly: remoteOnly, Reason: fmt.Sprintf(
				"%sepic branch for issue #%d — cannot confirm the issue is closed", note, num)}
		}
		if state == "OPEN" {
			return keep(fmt.Sprintf("epic branch for open issue #%d — sub-issue runs build on it", num))
		}
	}

	if s.isAncestor(ref, base) {
		return safe("tip is an ancestor of " + base + " — fully contained")
	}

	files, err := s.gitExec("diff", "--name-only", "-z", base+"..."+ref)
	if err != nil {
		return MergedProof{Verdict: VerdictUnknown, Tip: tip, RemoteOnly: remoteOnly, Reason: fmt.Sprintf("diff vs %s failed: %v", base, err)}
	}
	paths := splitNULPaths(files)
	if len(paths) == 0 {
		return MergedProof{Verdict: VerdictUnknown, Tip: tip, RemoteOnly: remoteOnly, Reason: fmt.Sprintf(
			"touches no files vs %s, and not an ancestor — check the base ref", base)}
	}
	args := append([]string{"diff", "--stat", base, ref, "--"}, paths...)
	residual, err := s.gitExec(args...)
	if err != nil {
		return MergedProof{Verdict: VerdictUnknown, Tip: tip, RemoteOnly: remoteOnly, Reason: fmt.Sprintf("content diff vs %s failed: %v", base, err)}
	}
	residual = strings.TrimSpace(residual)
	if residual == "" {
		return safe(fmt.Sprintf("content identical in %s (%d files)", base, len(paths)))
	}

	if in.MergedPR != nil {
		if head, parents, found := in.MergedPR(branch); found && head != "" {
			if head == tip {
				return safe("merged PR head is this exact tip; " + base + " moved on since")
			}
			for _, p := range parents {
				if p == tip {
					return safe("tip is a parent of the merged PR head (update-branch) — " + base + " moved on since")
				}
			}
			return keep(fmt.Sprintf("a merged PR merged a DIFFERENT tip (%s vs %s) — commits past the merge", short(head), short(tip)))
		}
	}

	lines := strings.Split(residual, "\n")
	return keep("unmerged content: " + strings.TrimSpace(lines[len(lines)-1]))
}

// BranchMovedError says a pinned cleanup deleted nothing further because a ref
// no longer points at the SHA the merged proof judged — someone pushed or
// committed after the judgment. The branch must be kept and re-judged.
type BranchMovedError struct {
	Branch, Ref, Judged, Now string
}

func (e *BranchMovedError) Error() string {
	now := e.Now
	if now == "" {
		now = "(unreadable)"
	}
	return fmt.Sprintf("%s moved since it was judged (%s, now %s) — kept; re-run to re-judge",
		e.Ref, short(e.Judged), short(now))
}

// BranchCleanupPinned is BranchCleanup pinned to a merged proof (#2259): each
// half is deleted only while it still points at the SHA that was judged.
//
// Both pins are checked before anything is deleted. The local half then goes
// through deleteLocalBranch (keeping git's worktree refusal), and the origin
// half through `git push --force-with-lease=refs/heads/<b>:<originTip>`, so a
// push landing between judgment and delete makes the server refuse and the
// branch survives with a *BranchMovedError. originTip == "" means the proof saw
// no origin copy; one appearing since is a move, not something to delete.
func (s *Service) BranchCleanupPinned(name, localTip, originTip string) error {
	if name == "main" || name == "master" {
		return fmt.Errorf("refusing to delete protected branch %q", name)
	}
	if err := validateRefArg("branch", name); err != nil {
		return err
	}

	curLocal, _ := s.revParse("refs/heads/" + name)
	if curLocal != "" && curLocal != localTip {
		return &BranchMovedError{Branch: name, Ref: "refs/heads/" + name, Judged: localTip, Now: curLocal}
	}
	live, err := s.liveRemoteTip(name)
	if err != nil {
		return fmt.Errorf("branch cleanup %s: read origin: %w", name, err)
	}
	if live != "" && live != originTip {
		return &BranchMovedError{Branch: name, Ref: "origin/" + name, Judged: originTip, Now: live}
	}

	localGone, err := s.deleteLocalBranch(name)
	if !localGone {
		if err == nil {
			err = fmt.Errorf("branch cleanup %s: local branch was not removed", name)
		}
		return err
	}
	if live == "" {
		return nil
	}

	lease := "--force-with-lease=refs/heads/" + name + ":" + originTip
	if _, pushErr := s.gitExec("push", lease, "origin", ":refs/heads/"+name); pushErr != nil {
		now, lsErr := s.liveRemoteTip(name)
		if lsErr == nil && now == "" {
			return nil // deleted server-side meanwhile; the end state holds
		}
		if lsErr == nil && now != originTip {
			return &BranchMovedError{Branch: name, Ref: "origin/" + name, Judged: originTip, Now: now}
		}
		return fmt.Errorf("branch cleanup %s: remote: %w", name, pushErr)
	}
	_ = s.Fetch(true)
	return nil
}

func (s *Service) revParse(ref string) (string, bool) {
	out, err := s.gitExec("rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		return "", false
	}
	sha := strings.TrimSpace(out)
	return sha, sha != ""
}

// isAncestor is `git merge-base --is-ancestor`; any failure reads as "no",
// which is the direction that keeps the branch.
func (s *Service) isAncestor(commit, of string) bool {
	cmd := exec.Command("git", "merge-base", "--is-ancestor", commit, of)
	cmd.Dir = s.repoPath
	return cmd.Run() == nil
}

// liveRemoteTip asks origin (not the cached tracking ref) for refs/heads/<branch>.
func (s *Service) liveRemoteTip(branch string) (string, error) {
	want := "refs/heads/" + branch
	out, err := s.gitExec("ls-remote", "origin", want)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[1] == want {
			return f[0], nil
		}
	}
	return "", nil
}

func epicIssueNumber(branch string) (int, bool) {
	rest, ok := strings.CutPrefix(branch, "epic/")
	if !ok {
		return 0, false
	}
	digits, _, _ := strings.Cut(rest, "-")
	n := 0
	if digits == "" {
		return 0, false
	}
	for _, c := range digits {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}

func splitNULPaths(s string) []string {
	var out []string
	for _, p := range strings.Split(s, "\x00") {
		if p != "" && p != "\n" {
			out = append(out, p)
		}
	}
	return out
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

package doctor

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/nightgauge/nightgauge/internal/config"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/reclaim"
)

// verbStrandedRefPrune deletes a stranded fork ref whose issue is closed.
const verbStrandedRefPrune = "stranded_ref.prune"

// issueStateFactory yields, per repo root, a lookup returning an issue's state
// ("OPEN" or "CLOSED"). A nil lookup means the state cannot be resolved there.
type issueStateFactory func(repoRoot string) func(issue int) (string, error)

// issueStateDoor builds the issue-state lookup from doctor's own client. A nil
// client, or an origin that is not a GitHub remote, yields the closed door.
func issueStateDoor(ctx context.Context, client *gh.Client) issueStateFactory {
	return func(repoRoot string) func(issue int) (string, error) {
		if client == nil {
			return nil
		}
		return gh.NewIssueStateLookupForRoot(ctx, func() (*gh.Client, error) { return client, nil }, repoRoot)
	}
}

// Issue states as GetIssueState normalises them.
const (
	issueOpen   = "OPEN"
	issueClosed = "CLOSED"
)

// lookupFor resolves states for one repo root; nil means unresolvable there.
func lookupFor(states issueStateFactory, repoRoot string) func(int) (string, error) {
	if states == nil {
		return nil
	}
	return states(repoRoot)
}

// resolveIssueState reads issue's state through lookup, returning "" and a
// reason when it cannot be decided.
func resolveIssueState(lookup func(int) (string, error), issue int) (string, string) {
	if issue <= 0 {
		return "", "the ref's suffix is not an issue number"
	}
	if lookup == nil {
		return "", "no GitHub client, or origin is not a GitHub remote"
	}
	state, err := lookup(issue)
	if err != nil {
		return "", err.Error()
	}
	if state != issueOpen && state != issueClosed {
		return "", fmt.Sprintf("unexpected issue state %q", state)
	}
	return state, ""
}

// strandedRefFindings reports refs/nightgauge/stranded/<issue> refs (#2753),
// which PreserveStrandedTip writes for a branch_forked run (#2699) and nothing
// else reads. A ref whose issue is closed gets an auto prune remedy; one whose
// issue is open is kept and only counted; one whose issue state cannot be
// resolved is reported unverifiable with a manual remedy and never pruned.
func strandedRefFindings(startDir string, states issueStateFactory) ([]Finding, string) {
	const check, code = "stranded_refs", "NGD052"
	roots := config.WorkspaceRepoRoots(startDir)
	if len(roots) == 0 {
		return []Finding{unverifiableFinding(check, code, SeverityHousekeeping, "stranded fork refs",
			"no repo roots resolved — not inside a git repository or workspace")}, "could not scan for stranded fork refs"
	}
	var out []Finding
	total, kept := 0, 0
	for _, root := range roots {
		refs, err := reclaim.ListStrandedRefs(root)
		if err != nil {
			return []Finding{unverifiableFinding(check, code, SeverityHousekeeping, "stranded fork refs",
				fmt.Sprintf("%s: %v", root, err))}, "could not read a repo's stranded refs"
		}
		repo := filepath.Base(root)
		lookup := lookupFor(states, root)
		for _, r := range refs {
			total++
			state, reason := resolveIssueState(lookup, r.Issue)
			issue := "unknown issue"
			if r.Issue > 0 {
				issue = fmt.Sprintf("#%d", r.Issue)
			}
			ev := map[string]string{"repo_root": root, "ref": r.Ref, "commit": r.Commit, "issue": strconv.Itoa(r.Issue)}
			inspect := fmt.Sprintf("Inspect it: git -C %s log -1 %s", root, r.Commit)
			switch state {
			case issueOpen:
				kept++
			case issueClosed:
				ev["state"] = "closed"
				out = append(out, newFinding(check, code, SeverityHousekeeping,
					fmt.Sprintf("stranded fork ref %s for closed issue %s in %s (%s)", r.Ref, issue, repo, shortSHA(r.Commit)),
					"a branch_forked run pinned its tip here; the issue is closed, so nothing will resume it",
					ev, []string{root, r.Ref},
					Remedy{ID: "prune", Kind: RemedyAuto, Verb: verbStrandedRefPrune, Verify: check,
						Summary: "Delete the stranded ref of a closed issue",
						Preview: fmt.Sprintf("delete %s (%s) in %s, only while issue %s is still closed and the ref still points there",
							r.Ref, shortSHA(r.Commit), root, issue)},
					manualRemedy("inspect", "Inspect the pinned commit before it is pruned", check, inspect)))
			default:
				ev["state"] = "unverifiable"
				ev["reason"] = reason
				out = append(out, newFinding(check, code, SeverityHousekeeping,
					fmt.Sprintf("stranded fork ref %s for issue %s in %s: issue state unverifiable", r.Ref, issue, repo),
					"could not resolve the issue's state ("+reason+"), so the ref is kept",
					ev, []string{root, r.Ref},
					manualRemedy("review", "Check the issue and delete the ref by hand if it is closed", check,
						inspect, fmt.Sprintf("Once the issue is closed: git -C %s update-ref -d %s %s", root, r.Ref, r.Commit))))
			}
		}
	}
	if total == 0 {
		return nil, "no stranded fork refs"
	}
	return out, fmt.Sprintf("%d stranded fork ref(s), %d kept: issue open", total, kept)
}

// --- stranded_ref.prune -------------------------------------------------------

func (v *verbs) strandedRefPrecondition(ctx context.Context, f Finding) error {
	_, _, _, err := v.strandedRefPlan(ctx, f)
	return err
}

func (v *verbs) strandedRefApply(ctx context.Context, f Finding) error {
	root, ref, commit, err := v.strandedRefPlan(ctx, f)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRemedyBlocked, err)
	}
	if err := reclaim.DeleteStrandedRef(root, ref, commit); err != nil {
		return fmt.Errorf("%w: %v", ErrRemedyBlocked, err)
	}
	return nil
}

// strandedRefPlan re-derives the scan's claim: the issue is still CLOSED and
// the ref still points at the scanned commit.
func (v *verbs) strandedRefPlan(ctx context.Context, f Finding) (root, ref, commit string, err error) {
	if root, err = evidence(f, "repo_root"); err != nil {
		return
	}
	if ref, err = evidence(f, "ref"); err != nil {
		return
	}
	if commit, err = evidence(f, "commit"); err != nil {
		return
	}
	issue, aerr := strconv.Atoi(f.Evidence["issue"])
	if aerr != nil {
		err = fmt.Errorf("finding %s carries an unusable %q evidence: %q", f.Code, "issue", f.Evidence["issue"])
		return
	}
	states := v.deps.issueStates
	if states == nil {
		states = issueStateDoor(ctx, v.env.Client)
	}
	state, reason := resolveIssueState(lookupFor(states, root), issue)
	if state != issueClosed {
		if reason == "" {
			reason = "issue is " + state
		}
		err = fmt.Errorf("%s kept: issue #%d is not confirmed closed (%s)", ref, issue, reason)
		return
	}
	cur, rerr := reclaim.ResolveStrandedRef(root, ref)
	if rerr != nil {
		err = rerr
		return
	}
	if cur == "" {
		err = fmt.Errorf("%s no longer exists in %s", ref, root)
		return
	}
	if cur != commit {
		err = fmt.Errorf("%s now points at %s, not the scanned %s", ref, shortSHA(cur), shortSHA(commit))
	}
	return
}

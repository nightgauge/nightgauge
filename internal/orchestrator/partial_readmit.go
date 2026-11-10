package orchestrator

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"

	gh "github.com/nightgauge/nightgauge/internal/github"
	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
)

// Vetting a re-admitted partial delivery against main (#3160).
//
// The reconcile re-admits an open issue it recorded as completed, which is
// what a `Refs` merge leaves behind. When the issue's partial-delivery comment
// lists a remainder, each criterion is re-checked on main with the verifier
// pr-create uses. Only an answer of "every criterion met" changes anything:
// the issue is not dispatched, the verdicts are posted, a card asks a human to
// close it, and a held entry keeps the 60 s reconcile from re-admitting it.
// Every other answer, including any read error, dispatches as before. "Every
// criterion met" counts only when the checkout is clean and its HEAD is
// origin's base branch, so an unmerged local change cannot read as delivered.
// A stale tree or a failed read can therefore only cost the run it always
// cost; it can never suppress work that is still owed.

// partialRemainderMetMarkerFormat marks the verdict comment.
const partialRemainderMetMarkerFormat = "<!-- nightgauge:partial-remainder-met pr=%d -->"

// partialReadmitVerdict is what vetPartialReadmit found.
type partialReadmitVerdict struct {
	PR       int
	Verdicts []pmstages.ACVerdict
	// AllMet is true only when there is a listed remainder and every one of
	// its criteria verified met on main.
	AllMet bool
}

// vetPartialReadmit re-checks repo#issue's pending remainder on the repo's
// main checkout. AllMet is false for no remainder, an unlisted one, a read
// error, an unresolvable checkout, or any criterion not met.
func (s *Scheduler) vetPartialReadmit(ctx context.Context, repo string, issue int) partialReadmitVerdict {
	status, err := s.readPartialDelivery(ctx, repo, issue)
	if err != nil {
		log.Printf("autonomous: %s#%d re-admit not vetted — partial-delivery remainder unreadable: %v", repo, issue, err)
		return partialReadmitVerdict{}
	}
	if !status.Pending || status.Unlisted || len(status.Criteria) == 0 {
		return partialReadmitVerdict{}
	}
	v := partialReadmitVerdict{PR: status.PR}
	if s.execMgr == nil {
		log.Printf("autonomous: %s#%d re-admit not vetted — no checkout to verify on", repo, issue)
		return v
	}
	root, err := s.resolveRunRoot(repo)
	if err != nil {
		log.Printf("autonomous: %s#%d re-admit not vetted — %v", repo, issue, err)
		return v
	}
	verify := s.partialRemainderVerify
	if verify == nil {
		verify = pmstages.VerifyCriteriaOnTree
	}
	v.Verdicts = verify(root, issue, status.Criteria)
	v.AllMet = pmstages.IssueClosedBy(v.Verdicts)
	if v.AllMet {
		atBase := s.partialCheckoutAtBase
		if atBase == nil {
			atBase = checkoutAtBase
		}
		if err := atBase(root); err != nil {
			log.Printf("autonomous: %s#%d remainder reads met on %s, but the checkout is not origin's base (%v) — dispatching", repo, issue, root, err)
			v.AllMet = false
		}
	}
	log.Printf("autonomous: %s#%d remainder of PR #%d vetted on %s (HEAD %s): %d criteria, %d open",
		repo, issue, status.PR, root, checkoutHead(root), len(v.Verdicts), len(pmstages.OpenACs(v.Verdicts)))
	return v
}

// checkoutAtBase returns nil only when root has no tracked changes and its
// HEAD is the commit origin's default branch names (origin/HEAD, else
// origin/main). Any other state, or any git error, is an error: the vet's
// "all met" must describe the base, not the operator's working tree.
func checkoutAtBase(root string) error {
	git := func(args ...string) (string, error) {
		out, err := exec.Command("git", append([]string{"-C", root}, args...)...).Output()
		return strings.TrimSpace(string(out)), err
	}
	dirty, err := git("status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return fmt.Errorf("git status: %w", err)
	}
	if dirty != "" {
		return fmt.Errorf("checkout has uncommitted changes")
	}
	head, err := git("rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("resolve HEAD: %w", err)
	}
	base, err := git("rev-parse", "--verify", "-q", "refs/remotes/origin/HEAD")
	if err != nil || base == "" {
		if base, err = git("rev-parse", "--verify", "-q", "refs/remotes/origin/main"); err != nil || base == "" {
			return fmt.Errorf("no origin base branch to compare with")
		}
	}
	if head != base {
		return fmt.Errorf("HEAD %.12s is not origin's base %.12s", head, base)
	}
	return nil
}

// checkoutHead names the commit a vet read, for the log: the plan's risk is a
// main checkout that is not on main.
func checkoutHead(root string) string {
	out, err := exec.Command("git", "-C", root, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	branch := strings.TrimSpace(string(out))
	sha, err := exec.Command("git", "-C", root, "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return branch
	}
	return branch + "@" + strings.TrimSpace(string(sha))
}

// partialRemainderMetComment is the verdict comment posted on an issue whose
// remainder all verifies on main.
func partialRemainderMetComment(issue int, v partialReadmitVerdict) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf(partialRemainderMetMarkerFormat, v.PR) + "\n")
	b.WriteString(fmt.Sprintf("Before re-running #%d, the scheduler re-checked the criteria PR #%d left unmet against `main`, "+
		"with the verifier pr-create uses. Every one is now met, so no run was dispatched (#3160):\n\n", issue, v.PR))
	for _, verdict := range v.Verdicts {
		b.WriteString("- " + pmstages.CommentSafeACText(verdict.Text))
		if verdict.Evidence != "" {
			b.WriteString(fmt.Sprintf(" (%s: %s)", verdict.Source, pmstages.CommentSafeACText(verdict.Evidence)))
		}
		b.WriteString("\n")
	}
	b.WriteString("\nClosing the issue is left to a human. Close it if this evidence holds, or reopen the work from the Action Center.\n")
	return b.String()
}

// postIssueComment comments body on repo#issue. The issueCommentPost seam
// replaces it in tests.
func (s *Scheduler) postIssueComment(ctx context.Context, repo string, issue int, body string) error {
	if s.issueCommentPost != nil {
		return s.issueCommentPost(ctx, repo, issue, body)
	}
	owner, name := splitRepo(repo)
	client, err := s.repoClient(ctx, owner, name)
	if err != nil {
		return err
	}
	svc := gh.NewIssueService(client)
	iss, err := svc.GetIssue(ctx, owner, name, issue)
	if err != nil {
		return fmt.Errorf("read %s#%d: %w", repo, issue, err)
	}
	if iss == nil || iss.NodeID == "" {
		return fmt.Errorf("%s#%d has no node id", repo, issue)
	}
	return svc.AddComment(ctx, iss.NodeID, body)
}

// vetPartialReadmits vets each re-admitted completed item before the candidate
// pass (#3160). It must run without as.mu held: it reads the forge and the
// tree, and it raises attention cards.
func (as *AutonomousScheduler) vetPartialReadmits(ctx context.Context, items []CompletedItem) {
	if len(items) == 0 || as.scheduler == nil {
		return
	}
	for _, c := range items {
		v := as.scheduler.vetPartialReadmit(ctx, c.Repo, c.Number)
		if !v.AllMet {
			continue
		}
		if err := as.scheduler.postIssueComment(ctx, c.Repo, c.Number, partialRemainderMetComment(c.Number, v)); err != nil {
			log.Printf("autonomous: %s#%d: could not post the remainder verdicts: %v", c.Repo, c.Number, err)
		}
		as.raiseAttention(BuildPartialRemainderMet(c.Repo, c.Number, v.PR, v.Verdicts))
		detail := fmt.Sprintf("partial-delivery remainder of PR #%d verifies on main — not re-admitted; a human closes #%d [#3160]",
			v.PR, c.Number)
		as.mu.Lock()
		as.recordFailureLocked(c.Repo, c.Number, c.Title, time.Now().UTC().Format(time.RFC3339), detail, TerminalKindPartialRemainderMet)
		as.persistStateLocked()
		as.mu.Unlock()
		log.Printf("autonomous: %s#%d held — %s", c.Repo, c.Number, detail)
	}
}

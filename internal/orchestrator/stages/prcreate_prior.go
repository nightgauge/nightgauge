package stages

// Criteria an earlier merged PR on the same issue delivered (#3362).
//
// ResolveACVerdicts sees only this branch: the tree reconcile, the diff
// verifier and feature-validate. A criterion an earlier merged PR met is
// not_verifiable on every later PR, so an issue delivered across two PRs never
// closed. Each `Refs` merge already records what it met: post-merge posts a
// trusted `<!-- nightgauge:partial-delivery pr=N -->` comment whose
// `Met by this PR:` list hooks.ParsePartialDeliveryMet reads (#3318). This
// module reads those records back, keeps the merged PRs', and credits the
// criteria they list. A verdict whose evidence names a commit already on the
// base branch is credited the same way.

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"regexp"
	"sort"
	"strings"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/hooks"
	"github.com/nightgauge/nightgauge/internal/trust"
)

const (
	// ACSourcePriorPR credits a criterion an earlier merged PR's
	// partial-delivery record lists as met.
	ACSourcePriorPR = "prior-pr"
	// ACSourcePriorCommit credits a criterion whose evidence names a commit
	// that is an ancestor of the base branch.
	ACSourcePriorCommit = "prior-commit"
)

// PriorDelivery is one earlier merged PR's partial-delivery record.
type PriorDelivery struct {
	PR  int
	Met []string
	// Remaining is what the record left unmet. A criterion a newer record
	// lists here is not credited to an older record's Met list.
	Remaining []string
	// NotVerifiable is what the record left as not verifiable by the
	// pipeline (#3362). It is no evidence against an older record's Met, so
	// the search walks past it.
	NotVerifiable []string
}

// PriorDeliveryReader lists the issue's earlier merged deliveries. An error
// means the record could not be read; the caller credits nothing from it.
type PriorDeliveryReader func(ctx context.Context, issueNumber int) ([]PriorDelivery, error)

// CommitOnBase reports whether sha names a commit reachable from the base
// branch. Any failure is false.
type CommitOnBase func(sha string) bool

// prCreatePriorDeliveries returns the reader pr-create credits earlier merged
// PRs through. Swapped in tests so they stay offline.
var prCreatePriorDeliveries = func(workdir string) PriorDeliveryReader {
	return execPriorDeliveryReader(workdir)
}

// prCreateCommitOnBase returns the ancestry check for base. Swapped in tests.
var prCreateCommitOnBase = func(workdir, base string) CommitOnBase {
	return gitCommitOnBase(workdir, base)
}

// acCommitSHAPattern finds a commit the evidence says delivered the
// criterion: a delivery verb, then by/in/with/via, optionally "commit", then
// git's shortest default abbreviation (7) up to a full SHA-1 (40). A SHA cited
// for any other reason ("compared against merge-base 1a2b3c4") credits nothing.
var acCommitSHAPattern = regexp.MustCompile(
	`(?i)\b(?:delivered|implemented|landed|met|added|fixed|shipped|done)\s+(?:by|in|with|via)\s+(?:commit\s+)?([0-9a-f]{7,40})\b`)

// acHedgePattern rejects a delivery claim the words before it negate or
// hedge: "could not confirm it was implemented in abc1234", "not added in
// commit abc1234", "check whether it landed in abc1234".
var acHedgePattern = regexp.MustCompile(
	`(?i)\b(?:not|never|no|cannot|can't|couldn't|unable|whether|if|unclear|unconfirmed|unverified|will|would|should|may|might|expected|planned|pending)\b|n't\b`)

// acHedgeWindow is how many bytes before a delivery claim are read for a
// negation or hedge: the clause the claim sits in.
const acHedgeWindow = 48

// maxSHAProbesPerVerdict bounds the git checks one verdict's evidence costs.
const maxSHAProbesPerVerdict = 4

// deliveredCommits returns the SHAs evidence plainly says delivered the
// criterion, lower-cased, at most maxSHAProbesPerVerdict.
func deliveredCommits(evidence string) []string {
	var out []string
	for _, m := range acCommitSHAPattern.FindAllStringSubmatchIndex(evidence, maxSHAProbesPerVerdict) {
		clause := evidence[max(0, m[0]-acHedgeWindow):m[0]]
		if i := strings.LastIndexAny(clause, ".;\n"); i >= 0 {
			clause = clause[i+1:]
		}
		if acHedgePattern.MatchString(clause) {
			continue
		}
		out = append(out, strings.ToLower(evidence[m[2]:m[3]]))
	}
	return out
}

// issueWording maps an ac-reconcile criterion to the issue's own wording, the
// text post-merge recorded. ac-reconcile joins a checkbox's sub-bullets and a
// numbered item's wrapped lines onto its text, so the issue criterion is the
// longest one equal to text or followed in it by a space (after its own
// period, which normalising drops). With no issue
// criterion that fits, text is its own wording.
func issueWording(text string, issueCriteria []string) string {
	nt := hooks.NormaliseCriterion(text)
	best, bestLen := text, -1
	for _, c := range issueCriteria {
		nc := hooks.NormaliseCriterion(c)
		if nc == "" || len(nc) <= bestLen {
			continue
		}
		if rest, ok := strings.CutPrefix(nt, nc); ok && (rest == "" || strings.HasPrefix(rest, " ") || strings.HasPrefix(rest, ". ")) {
			best, bestLen = c, len(nc)
		}
	}
	return best
}

// ApplyPriorDeliveries lifts not_verifiable verdicts an earlier merged PR or a
// commit on base delivered. Only not_verifiable is lifted: an unmet from any
// current source still wins (#2665), and not_judged stays as #3228 made it.
// A criterion is matched in the issue's wording (issueCriteria, the current
// body's criteria) against each record under the writer's tick rule, so a
// stronger or reworded criterion stays not_verifiable. The newest merged
// record that names the criterion decides: met there credits that PR, left
// open there credits nothing.
func ApplyPriorDeliveries(verdicts []ACVerdict, priors []PriorDelivery, onBase CommitOnBase, base string, issueCriteria []string) []ACVerdict {
	newest := make([]PriorDelivery, len(priors))
	copy(newest, priors)
	sort.SliceStable(newest, func(i, j int) bool { return newest[i].PR > newest[j].PR })
	names := func(list []string, crit string) bool {
		for _, e := range list {
			if hooks.RecordedCriterionMatches(crit, e) {
				return true
			}
		}
		return false
	}
	creditedPR := func(text string) int {
		crit := issueWording(text, issueCriteria)
		for _, p := range newest {
			if names(p.Met, crit) {
				return p.PR
			}
			if names(p.Remaining, crit) {
				return 0
			}
			// A NotVerifiable entry says nothing either way: keep walking.
		}
		return 0
	}
	out := make([]ACVerdict, len(verdicts))
	copy(out, verdicts)
	for i, v := range out {
		if v.Status != ACNotVerifiable {
			continue
		}
		if pr := creditedPR(v.Text); pr > 0 {
			out[i].Status, out[i].Source, out[i].MetByPR = ACMet, ACSourcePriorPR, pr
			out[i].Evidence = fmt.Sprintf("met by merged PR #%d (partial-delivery record)", pr)
			out[i].Step = ""
			continue
		}
		// A criterion that needs a runtime or operator step is not delivered
		// by a commit alone.
		if onBase == nil || v.Step != "" {
			continue
		}
		for _, sha := range deliveredCommits(v.Evidence) {
			if onBase(sha) {
				out[i].Status, out[i].Source, out[i].MetByCommit = ACMet, ACSourcePriorCommit, sha
				out[i].Evidence = fmt.Sprintf("delivered by %s, an ancestor of %s", sha, base)
				out[i].Step = ""
				break
			}
		}
	}
	return out
}

// gitCommitOnBase checks ancestry against origin/<base>, or <base> when the
// remote ref is absent. A SHA only on the feature branch is not on base.
func gitCommitOnBase(workdir, base string) CommitOnBase {
	git := func(args ...string) bool {
		return exec.Command("git", append([]string{"-C", workdir}, args...)...).Run() == nil
	}
	if base == "" || workdir == "" || strings.HasPrefix(base, "-") || !git("check-ref-format", "--branch", base) {
		return func(string) bool { return false }
	}
	ref := "origin/" + base
	if !git("rev-parse", "--verify", "--quiet", "--end-of-options", ref) {
		ref = base
	}
	seen := map[string]bool{}
	return func(sha string) bool {
		if v, ok := seen[sha]; ok {
			return v
		}
		v := git("rev-parse", "--verify", "--quiet", "--end-of-options", sha+"^{commit}") &&
			git("merge-base", "--is-ancestor", "--end-of-options", sha, ref)
		seen[sha] = v
		return v
	}
}

// execPriorDeliveryReader reads the issue's trusted comments through the same
// trust gate `issue partial-delivery` builds — write access or a configured
// trusted bot — and keeps each partial-delivery record whose PR is MERGED.
func execPriorDeliveryReader(workdir string) PriorDeliveryReader {
	return func(ctx context.Context, issueNumber int) ([]PriorDelivery, error) {
		owner, repo, ok := github.OriginSlug(workdir)
		if !ok {
			return nil, fmt.Errorf("cannot read the repository from %s's origin remote", workdir)
		}
		cfg, err := config.Load(workdir)
		if err != nil {
			return nil, fmt.Errorf("load config: %w", err)
		}
		var bots, appOwners []string
		var resolver github.TokenResolver
		if cfg != nil {
			appOwners = cfg.AppOwners()
			resolver = cfg
			if cfg.Autonomous != nil {
				bots = cfg.Autonomous.TrustedBots
			}
		}
		client, err := github.NewClientFromConfig(resolver, owner, "")
		if err != nil {
			return nil, fmt.Errorf("build GitHub client: %w", err)
		}
		gate := trust.NewGate(github.NewTrustForge(client), bots).WithAppOwners(appOwners...)
		comments, err := gate.TrustedComments(ctx, owner, repo, issueNumber)
		if err != nil {
			return nil, fmt.Errorf("read trusted comments on #%d: %w", issueNumber, err)
		}
		prs := github.NewPRService(client)
		return mergedPriorDeliveries(comments, func(pr int) (string, error) {
			return prs.GetPRState(ctx, owner, repo, pr)
		})
	}
}

// mergedPriorDeliveries keeps each partial-delivery record among comments
// whose PR state reads MERGED. A closed-unmerged or open PR contributes
// nothing; a state that cannot be read is an error.
func mergedPriorDeliveries(comments []trust.Comment, prState func(pr int) (string, error)) ([]PriorDelivery, error) {
	met, remaining, notVerifiable := map[int][]string{}, map[int][]string{}, map[int][]string{}
	for _, c := range comments {
		pr, rest, ok := hooks.ParsePartialDeliveryComment(c.Body)
		if !ok || pr <= 0 {
			continue
		}
		met[pr] = append(met[pr], hooks.ParsePartialDeliveryMet(c.Body)...)
		nv := hooks.ParsePartialDeliveryNotVerifiable(c.Body)
		notVerifiable[pr] = append(notVerifiable[pr], nv...)
		for _, r := range rest {
			if !containsCriterion(nv, r) {
				remaining[pr] = append(remaining[pr], r)
			}
		}
	}
	prs := make([]int, 0, len(met))
	for pr := range met {
		prs = append(prs, pr)
	}
	sort.Ints(prs)
	var out []PriorDelivery
	for _, pr := range prs {
		if len(met[pr]) == 0 && len(remaining[pr]) == 0 && len(notVerifiable[pr]) == 0 {
			continue
		}
		state, err := prState(pr)
		if err != nil {
			return nil, fmt.Errorf("read PR #%d state: %w", pr, err)
		}
		if !strings.EqualFold(state, "MERGED") {
			log.Printf("pr-create: PR #%d's partial-delivery record is not credited: the PR is %s, not merged", pr, state)
			continue
		}
		out = append(out, PriorDelivery{PR: pr, Met: met[pr], Remaining: remaining[pr], NotVerifiable: notVerifiable[pr]})
	}
	return out, nil
}

// containsCriterion reports whether list holds c after normalisation.
func containsCriterion(list []string, c string) bool {
	n := hooks.NormaliseCriterion(c)
	for _, e := range list {
		if hooks.NormaliseCriterion(e) == n {
			return true
		}
	}
	return false
}

// hasNotVerifiable reports whether any verdict could be lifted.
func hasNotVerifiable(verdicts []ACVerdict) bool {
	for _, v := range verdicts {
		if v.Status == ACNotVerifiable {
			return true
		}
	}
	return false
}

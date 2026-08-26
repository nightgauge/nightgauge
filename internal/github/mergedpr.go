package github

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"sync"

	"github.com/shurcooL/graphql"
)

// The merged-PR door for branch reclamation (#916, mechanism from #593).
//
// `execution.SweepMergedWorktrees` decides merged-ness by CONTENT diff, which
// is correct for squash merges and wrong in one direction: once the default
// branch evolves the same files a merged branch touched, the diff is non-empty
// again and the branch reads as unmerged forever. `WorktreeSweepOptions.
// MergedPRLookup` exists as the fail-open second door for exactly that, and
// between #593 and #916 **no production call site ever supplied it** — the
// door was built, documented and unit-tested, and never opened. This file is
// the production implementation.
//
// The shape mirrors `scripts/branch-merged-check.sh`, which has been getting
// this right in shell the whole time: fetch the merged-PR index ONCE, then
// look up a commit's parents only for the branches that actually need it.

// mergedPRIndexSize bounds how many merged PRs the index carries.
//
// **100 is GitHub's hard maximum for a single connection page, not a taste
// choice.** `first: 250` is rejected outright — and the rejection is invisible
// from the Go side: the door swallows the error, every lookup answers
// not-found, and the whole feature reads as "no branch has a merged PR". That
// is exactly what happened on the first run of this code, and only a live
// probe found it, because a fake-backed test never sees GitHub's limit.
// Pinned by TestMergedPRIndexSize_FitsOneGitHubPage.
//
// One page is a WINDOW, and a deliberate one: a branch whose merge has fallen
// out of the newest 100 falls back to the content test, which is the same
// fail-closed answer the door gives on any other miss. Paginating would buy a
// longer window at a cost paid on every sweep, and #842 is an open epic about
// that budget. `scripts/branch-merged-check.sh` uses `--limit 500` because
// `gh` paginates for free at the CLI layer; this door does not.
const mergedPRIndexSize = 100

// maxGraphQLPageSize is GitHub's per-connection ceiling. Named so the bound
// above cites a reason rather than a number.
const maxGraphQLPageSize = 100

// MergedPRHead is one merged pull request's head commit — the branch name it
// merged from and the OID that branch pointed at.
type MergedPRHead struct {
	Number      int
	HeadRefName string
	HeadRefOid  string
}

type mergedPRHeadsQuery struct {
	Repository struct {
		PullRequests struct {
			Nodes []struct {
				Number      graphql.Int
				HeadRefName graphql.String
				HeadRefOid  graphql.String
			}
		} `graphql:"pullRequests(first: $first, states: MERGED, orderBy: {field: UPDATED_AT, direction: DESC})"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

// ListMergedPRHeads returns merged pull requests newest-first with the head
// ref each merged from and the OID it pointed at.
//
// Deliberately its own narrow query rather than a field added to ListPRs:
// `headRefOid` on the shared list path would be paid for by every caller of a
// query that already carries labels and a check rollup, and none of them want
// it. This one selects three scalars.
func (s *PRService) ListMergedPRHeads(ctx context.Context, owner, repo string, limit int) ([]MergedPRHead, error) {
	limit = clampPageSize(limit)
	var q mergedPRHeadsQuery
	vars := map[string]interface{}{
		"owner": graphql.String(owner),
		"name":  graphql.String(repo),
		"first": graphql.Int(limit),
	}
	if err := s.client.query(ctx, &q, vars); err != nil {
		return nil, fmt.Errorf("list merged PR heads: %w", err)
	}
	heads := make([]MergedPRHead, 0, len(q.Repository.PullRequests.Nodes))
	for _, n := range q.Repository.PullRequests.Nodes {
		heads = append(heads, MergedPRHead{
			Number:      int(n.Number),
			HeadRefName: string(n.HeadRefName),
			HeadRefOid:  string(n.HeadRefOid),
		})
	}
	return heads, nil
}

// clampPageSize resolves a caller's limit into a legal connection page size.
// Clamps rather than errors: GitHub rejects an oversized `first:` outright,
// and that rejection surfaces as "no merged PRs anywhere" rather than as a
// failure — so the safe move is to ask for a page GitHub will actually serve.
func clampPageSize(limit int) int {
	if limit <= 0 {
		return mergedPRIndexSize
	}
	if limit > maxGraphQLPageSize {
		return maxGraphQLPageSize
	}
	return limit
}

// commitParentsResponse is the slice of `GET /repos/{owner}/{repo}/commits/
// {sha}` this door reads. The endpoint returns the full commit — author,
// message, stats, and the complete file list — and only the parent SHAs are
// mapped.
type commitParentsResponse struct {
	Parents []struct {
		SHA string `json:"sha"`
	} `json:"parents"`
}

// CommitParents returns a commit's parent SHAs. An unknown OID is not an
// error — it yields no parents, which the door reads as "no containment".
//
// REST, deliberately (#849). The predecessor spent a GraphQL point per lookup
// on `repository.object(oid:)`, and this is a per-branch call: a sweep over N
// candidate branches that miss the merged-PR index pays N points on the bucket
// this repo exhausts. Moving them to the near-idle core bucket is the whole
// saving on the one-shot CLI path.
//
// The conditional-GET layer (#486) adds a second saving, but ONLY inside a
// long-lived process: its ETag cache is in-memory and hangs off one *Client
// (see installHeaderInterceptor), so it is not shared between separate
// `nightgauge` invocations. A sweep running under `nightgauge serve` re-reads
// the same merged commits every cycle and pays nothing after the first; a
// one-shot `nightgauge worktree sweep` starts with a cold cache every time.
// Do not describe this call as free — it is cheaper, and free only in the
// daemon.
//
// **An unknown SHA comes back 422, not 404**, and that is the whole trap in
// this migration. GraphQL expressed "no such object" as a null `object` field,
// which this function mapped to (nil, nil). REST splits the same answer across
// two status codes — 404 when the repository is unreachable or the ref path is
// wrong, 422 ("No commit found for SHA") when the repository is fine and the
// commit simply is not in it — and 422 is the one the door actually meets.
// Treating it as an error would turn every index miss into a sweep failure;
// treating an unrecognised status as "no parents" would let a permission
// problem read as a clean answer. So exactly 404 and 422 map to no-parents and
// everything else is an error. Pinned by
// TestCommitParents_UnknownSHAIsNotAnError.
func (s *PRService) CommitParents(ctx context.Context, owner, repo, oid string) ([]string, error) {
	path := fmt.Sprintf("/repos/%s/%s/commits/%s",
		url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(oid))
	body, status, err := s.client.restDoStatus(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("commit parents for %s: %w", oid, err)
	}
	switch {
	case status == http.StatusNotFound || status == http.StatusUnprocessableEntity:
		// The forge says this commit is not here. Same answer the GraphQL null
		// object carried, and the door reads it as "no containment".
		return nil, nil
	case status < 200 || status >= 300:
		return nil, fmt.Errorf("commit parents for %s: REST %d: %s", oid, status, restErrorSummary(body))
	}
	var resp commitParentsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("commit parents for %s: decode commit: %w", oid, err)
	}
	parents := make([]string, 0, len(resp.Parents))
	for _, p := range resp.Parents {
		parents = append(parents, p.SHA)
	}
	return parents, nil
}

// MergedPRHeadLister is the slice of PRService this door needs. Named so the
// constructor below can be driven by a fake in tests without a GraphQL client,
// and so nothing else about PRService leaks into the wiring.
type MergedPRHeadLister interface {
	ListMergedPRHeads(ctx context.Context, owner, repo string, limit int) ([]MergedPRHead, error)
	CommitParents(ctx context.Context, owner, repo, oid string) ([]string, error)
}

// NewMergedPRLookup returns the production merged-PR door for one repo, in the
// shape `execution.WorktreeSweepOptions.MergedPRLookup` and
// `execution.StrandedBranchOptions.MergedPRLookup` expect. Returned as a bare
// func type rather than the named one so this package does not import
// execution — assignment does the rest.
//
// **LAZY, and that is load-bearing, not an optimization.** The index query is
// issued on the FIRST call and never before. Callers consult the door only
// after the content test has already reported a branch unmerged, so a healthy
// repo — every branch either merged-by-content or genuinely unlanded-and-
// obvious — issues zero requests. The daemon's periodic sweep runs this on
// every cycle across every repo root, and #842 is an open epic about the API
// budget: a door that costs a query per sweep whether or not it is needed
// would be a regression paid on every idle tick.
//
// Fail-open toward KEEPING, in both senses. A lookup failure (no auth, no
// network, no such repo) reports not-found, the content test's verdict stands,
// and the branch is kept. The door can only ever move a branch from "kept" to
// "reported/reclaimable", never the reverse — so a broken door costs
// visibility, never work.
//
// The index is fetched at most once per lookup instance. Construct one per
// sweep, not one per branch.
func NewMergedPRLookup(ctx context.Context, svc MergedPRHeadLister, owner, repo string) func(branch string) (string, []string, bool) {
	if svc == nil || owner == "" || repo == "" {
		return nil
	}
	d := &mergedPRDoor{ctx: ctx, svc: svc, owner: owner, repo: repo}
	return d.lookup
}

type mergedPRDoor struct {
	ctx   context.Context
	svc   MergedPRHeadLister
	owner string
	repo  string

	once  sync.Once
	index map[string]string // headRefName -> headRefOid
	// indexErr is kept so a failed fetch is not retried per branch. One
	// failure is a failure for the whole sweep; hammering a rate-limited or
	// unauthenticated API once per branch is how a cheap door becomes an
	// expensive one.
	indexErr error
}

func (d *mergedPRDoor) load() {
	heads, err := d.svc.ListMergedPRHeads(d.ctx, d.owner, d.repo, mergedPRIndexSize)
	if err != nil {
		d.indexErr = err
		// LOG IT. The door's contract is to fail toward keeping, and it does
		// — but a door that fails silently is indistinguishable from a repo
		// where no branch has a merged PR, which is precisely how the
		// `first: 250` bug above presented. Once per sweep, at WARN: this is
		// a supplementary check degrading, not the sweep failing.
		log.Printf("[WARN] merged-PR door: %s/%s index unavailable (%v) — falling back to the content test alone; merged branches whose files the base has since touched will not be reported", d.owner, d.repo, err)
		return
	}
	d.index = make(map[string]string, len(heads))
	for _, h := range heads {
		// Newest-first, so the FIRST entry for a ref name wins: a branch name
		// reused across several merged PRs resolves to its most recent merge,
		// which is the one whose head could still be the local tip.
		if _, seen := d.index[h.HeadRefName]; !seen {
			d.index[h.HeadRefName] = h.HeadRefOid
		}
	}
}

// lookup reports the merged PR head for branch, plus that commit's parents.
//
// Parents are fetched ONLY when the head OID is not itself the answer the
// caller is looking for — the caller compares head-vs-tip first and the parent
// hop matters only for the `gh pr update-branch` shape (#593). Fetching them
// unconditionally would double the door's cost for the common case.
func (d *mergedPRDoor) lookup(branch string) (string, []string, bool) {
	d.once.Do(d.load)
	if d.indexErr != nil || d.index == nil {
		return "", nil, false
	}
	head, ok := d.index[strings.TrimSpace(branch)]
	if !ok || head == "" {
		return "", nil, false
	}
	parents, err := d.svc.CommitParents(d.ctx, d.owner, d.repo, head)
	if err != nil {
		// The head SHA alone is still a usable answer — the equality case
		// needs no parents. Report it rather than discarding a real hit.
		return head, nil, true
	}
	return head, parents, true
}

// NewMergedPRLookupForRoot is the wiring convenience the three production
// sweep call sites use: it resolves owner/repo from a repo root's `origin`
// remote and returns the door for it.
//
// Returns nil — the closed door, which every caller already handles — when the
// client is nil, the root is not a git repo, `origin` is missing, or the
// remote is not a GitHub URL. A sweep on a repo whose forge this package
// cannot speak to keeps working with the content test alone, which is what it
// did before this door existed.
// The slug is resolved BEFORE the client is built, and the client arrives as a
// factory for that reason. Resolving `origin` is a local `git` call; building
// the client can shell out to `gh auth token`, which costs seconds. A repo
// whose remote is not a GitHub URL — a local clone, another forge — must not
// pay for authentication it will never use, and unit tests over temp-dir
// fixtures must not touch the network at all.
func NewMergedPRLookupForRoot(ctx context.Context, clientFn func() (*Client, error), repoRoot string) func(branch string) (string, []string, bool) {
	if clientFn == nil || repoRoot == "" {
		return nil
	}
	owner, repo, ok := originSlug(repoRoot)
	if !ok {
		return nil
	}
	client, err := clientFn()
	if err != nil || client == nil {
		return nil
	}
	return NewMergedPRLookup(ctx, NewPRService(client), owner, repo)
}

// originSlug reads owner/name from a repo root's `origin` remote. Shells out
// to git rather than taking a go-git dependency here: this package is the
// forge layer, the callers are already git-driven, and `remote get-url` is the
// same thing `gh` itself reads.
func originSlug(repoRoot string) (owner, name string, ok bool) {
	out, err := exec.Command("git", "-C", repoRoot, "remote", "get-url", "origin").Output()
	if err != nil {
		return "", "", false
	}
	return parseOriginSlug(strings.TrimSpace(string(out)))
}

// parseOriginSlug extracts owner/name from the SSH and HTTPS remote forms.
// Split out from originSlug so the parsing is testable without a repository.
//
// **A remote that is not a forge URL must return false, not a plausible-looking
// slug.** The first version took the last two path segments of anything, so a
// local-path remote — `/private/var/…/base/origin.git`, which is what every git
// test fixture in this repo uses — parsed as `owner="base" name="origin"`. The
// door was then constructed for a repository that does not exist, spending an
// API call per sweep to be told so, logging a WARN each time, and dragging the
// network into unit tests that have no business touching it.
//
// So the shape is required, not inferred: an scp-style `user@host:owner/name`,
// or a URL with a forge scheme. A bare filesystem path, a `file://` URL, and
// anything else are all "not a GitHub remote", which is the closed door.
func parseOriginSlug(remote string) (owner, name string, ok bool) {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return "", "", false
	}

	switch {
	case strings.HasPrefix(remote, "/"), strings.HasPrefix(remote, "."),
		strings.HasPrefix(remote, "file://"):
		// A local clone. Not a forge, and the case that produced the bogus
		// slug above.
		return "", "", false
	}

	remote = strings.TrimSuffix(remote, ".git")

	var path string
	switch {
	case strings.HasPrefix(remote, "https://"), strings.HasPrefix(remote, "http://"),
		strings.HasPrefix(remote, "ssh://"), strings.HasPrefix(remote, "git://"):
		rest := remote[strings.Index(remote, "://")+3:]
		slash := strings.Index(rest, "/")
		if slash < 0 {
			return "", "", false
		}
		host := rest[:slash]
		if host == "" {
			return "", "", false
		}
		path = rest[slash+1:]
	case strings.Contains(remote, "@") && strings.Contains(remote, ":"):
		// scp-style: user@host:owner/name
		colon := strings.Index(remote, ":")
		at := strings.Index(remote, "@")
		if at > colon {
			return "", "", false
		}
		path = remote[colon+1:]
	default:
		return "", "", false
	}

	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 {
		return "", "", false
	}
	owner, name = parts[len(parts)-2], parts[len(parts)-1]
	if owner == "" || name == "" {
		return "", "", false
	}
	return owner, name, true
}

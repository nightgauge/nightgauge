package depgraph

import (
	"context"
	"fmt"
	"log"
	"strings"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// defaultDropThreshold is the fraction of raw board nodes that must be dropped
// before a WARN log is emitted. 10% means 4+ drops on a 37-item board triggers
// the warning.
const defaultDropThreshold = 0.10

// repoItemsFetcher abstracts board item fetching for testability.
type repoItemsFetcher func(ctx context.Context, repo RepoConfig) (items []types.BoardItem, rawCount int, err error)

// issueBodyFetcher abstracts single-issue body fetching for testability and
// for the slow per-issue fallback path. Prefer issueBodiesBatchFetcher when
// fetching for many nodes — it issues one aliased GraphQL query per repo
// instead of N sequential ones (#3400).
type issueBodyFetcher func(ctx context.Context, owner, name string, number int) (body string, err error)

// issueBodiesBatchFetcher fetches issue bodies for many numbers in a single
// repo via aliased GraphQL. Returns a map keyed by issue number. When
// non-nil, BuildGraph's body-fetch pass uses this instead of issueBodyFetcher
// to avoid the N×serial-API-call delay observed at autonomous startup
// (Issue #3400 — ~2 minutes for ~80 issues across 4 repos).
type issueBodiesBatchFetcher func(ctx context.Context, owner, name string, numbers []int) (bodies map[int]string, err error)

// RepoConfig identifies a repo and its project board for graph building.
type RepoConfig struct {
	Owner     string       `json:"owner"`
	OwnerType gh.OwnerType `json:"ownerType,omitempty"`
	Name      string       `json:"name"`
	Project   int          `json:"project"` // project board number
}

// FullName returns "owner/name".
func (rc RepoConfig) FullName() string {
	return rc.Owner + "/" + rc.Name
}

// BuildGraph constructs the full cross-repo dependency graph:
//  1. Fetches all open issues from each repo's project board
//  2. Reads blockedBy relationships from the board data
//  3. Parses issue bodies for cross-repo references
//  4. Builds the unified DAG
//  5. Computes waves and critical path
func BuildGraph(ctx context.Context, client *gh.Client, repos []RepoConfig, repoAliases map[string]string) (*Graph, error) {
	if len(repos) == 0 {
		return nil, fmt.Errorf("no repos configured")
	}

	fetcher := func(ctx context.Context, repo RepoConfig) ([]types.BoardItem, int, error) {
		return FetchOpenBoardItems(ctx, client, repo)
	}
	issueSvc := gh.NewIssueService(client)
	bodyFetcher := func(ctx context.Context, owner, name string, number int) (string, error) {
		issue, err := issueSvc.GetIssue(ctx, owner, name, number)
		if err != nil {
			return "", err
		}
		return issue.Body, nil
	}
	// Bulk path: one aliased GraphQL query per repo instead of N sequential
	// GetIssue calls. For a workspace with ~80 open issues across 4 repos
	// this drops the autonomous-startup graph build from ~2 minutes to a few
	// seconds (Issue #3400).
	bodiesBatch := func(ctx context.Context, owner, name string, numbers []int) (map[int]string, error) {
		issues, err := issueSvc.GetIssuesByNumbers(ctx, owner, name, numbers)
		if err != nil {
			return nil, err
		}
		out := make(map[int]string, len(issues))
		for n, iss := range issues {
			if iss != nil {
				out[n] = iss.Body
			}
		}
		return out, nil
	}

	return buildGraphFromFetcherWithBatch(ctx, fetcher, bodyFetcher, bodiesBatch, repos, repoAliases)
}

// buildGraphFromFetcher is the testable core of BuildGraph. It accepts injected
// fetcher functions so unit tests can substitute fakes without a real GitHub
// client. Equivalent to buildGraphFromFetcherWithBatch with a nil bodies-batch
// fetcher (per-issue fallback path).
func buildGraphFromFetcher(
	ctx context.Context,
	fetcher repoItemsFetcher,
	bodyFetcher issueBodyFetcher,
	repos []RepoConfig,
	repoAliases map[string]string,
) (*Graph, error) {
	return buildGraphFromFetcherWithBatch(ctx, fetcher, bodyFetcher, nil, repos, repoAliases)
}

// buildGraphFromFetcherWithBatch is the full-fidelity testable core. When
// bodiesBatch is non-nil it is used to fetch issue bodies one repo at a time
// (one aliased GraphQL query per repo), which is the production-fast path
// (Issue #3400). When bodiesBatch is nil, falls back to per-issue
// bodyFetcher calls — preserved so existing tests don't have to plumb a
// batch fake through unrelated assertions.
func buildGraphFromFetcherWithBatch(
	ctx context.Context,
	fetcher repoItemsFetcher,
	bodyFetcher issueBodyFetcher,
	bodiesBatch issueBodiesBatchFetcher,
	repos []RepoConfig,
	repoAliases map[string]string,
) (*Graph, error) {
	g := NewGraph()

	// Track which repos are in the workspace for resolvability
	workspaceRepos := make(map[string]bool)
	for _, r := range repos {
		workspaceRepos[r.FullName()] = true
	}

	// Edge dedup
	edgeSeen := make(map[string]bool) // "from#to" key

	totalRawNodes := 0
	totalPRsSkipped := 0
	totalCrossListed := 0

	for _, repo := range repos {
		items, rawCount, err := fetcher(ctx, repo)
		if err != nil {
			return nil, fmt.Errorf("depgraph: fetch items for %s: %w", repo.FullName(), err)
		}
		totalRawNodes += rawCount

		// Per-repo fetch diagnostic — without this, "graph has N nodes, scanning
		// M repos" hides which repos returned items. When one repo's project
		// number is wrong / its board is empty / its API call silently failed,
		// the symptom is "0 candidates" with no clue which repo dropped out.
		nodesAdded := 0
		prsSkipped := 0
		crossListed := 0
		for i := range items {
			item := &items[i]
			if item.IsPR {
				totalPRsSkipped++
				prsSkipped++
				continue // skip PRs, only issues form the DAG
			}

			node := boardItemToNode(item, repo.FullName())
			nodeKey := g.NodeKey(node.ID())

			// When the same issue is cross-listed on multiple project boards
			// (e.g., a dashboard sub-issue tracked on the platform board for
			// visibility), only its home-repo board is authoritative for
			// BoardStatus. Without this guard, the second board to be
			// processed silently overwrites the first via AddNode, so a
			// "Ready" status on the home board can be replaced by "Backlog"
			// from a foreign board — the autonomous scheduler then rejects
			// the issue with status="Backlog" even though the home board
			// has it Ready.
			isHome := item.Repo == "" || item.Repo == repo.FullName()
			_, alreadyAdded := g.Nodes[nodeKey]
			if alreadyAdded {
				crossListed++
				if isHome {
					// Home board overwrites whatever a foreign board added first.
					g.AddNode(node)
				}
				// else: foreign-board entry, leave the existing node intact.
			} else {
				g.AddNode(node)
				nodesAdded++
			}

			// GraphQL blockedBy edges
			for _, blocker := range item.BlockedBy {
				edge := Edge{
					From:       node.ID(),
					To:         NodeID{Repo: blocker.Repo, Number: blocker.Number},
					Type:       "blockedBy",
					Source:     "graphql",
					Resolvable: workspaceRepos[blocker.Repo],
				}
				eKey := edgeKey(edge)
				if !edgeSeen[eKey] {
					edgeSeen[eKey] = true
					g.AddEdge(edge)
				}
			}
		}
		totalCrossListed += crossListed
		log.Printf("depgraph: repo=%s project=%d raw=%d added=%d prs=%d cross-listed=%d dropped=%d",
			repo.FullName(), repo.Project, rawCount, nodesAdded, prsSkipped, crossListed, rawCount-nodesAdded-prsSkipped-crossListed)
	}

	// Gap accounting: items fetched but not added as graph nodes (DraftIssue, unknown type, etc.)
	// Cross-listed items are not drops — they're the same issue appearing on
	// multiple boards, intentionally deduped to a single graph node.
	if totalRawNodes > 0 {
		droppedCount := totalRawNodes - totalPRsSkipped - totalCrossListed - len(g.Nodes)
		if droppedCount > 0 {
			pct := float64(droppedCount) / float64(totalRawNodes) * 100
			log.Printf("WARN depgraph: BuildGraph: %d of %d raw board nodes dropped (%.1f%%) — DraftIssue or unknown type",
				droppedCount, totalRawNodes, pct)
		}
		g.Stats.DroppedItemsCount = droppedCount
	}

	// Second pass: fetch issue bodies for cross-repo references.
	// We need the body text which is not returned by the board query.
	//
	// Prefer the bulk path (one aliased GraphQL query per repo) over the
	// per-issue path. With ~80 nodes across 4 repos, the per-issue path
	// took ~2 minutes of serial GitHub API calls before autonomous could
	// dispatch its first item (Issue #3400). The bulk path collapses that
	// to one query per repo (≤ 4 queries here), typically completing in
	// a few seconds.
	bodiesByKey := make(map[string]string, len(g.Nodes))
	if bodiesBatch != nil {
		// Group node numbers by repo, batch-fetch per repo.
		byRepo := make(map[string][]int)
		for _, node := range g.Nodes {
			byRepo[node.Repo] = append(byRepo[node.Repo], node.Number)
		}
		for repoFull, numbers := range byRepo {
			owner := splitOwner(repoFull)
			name := splitName(repoFull)
			bodies, err := bodiesBatch(ctx, owner, name, numbers)
			if err != nil {
				// Fall back to per-issue fetch for this repo's nodes only.
				log.Printf("depgraph: bulk body fetch failed for %s (%d issues): %v — falling back to per-issue", repoFull, len(numbers), err)
				for _, num := range numbers {
					body, perr := bodyFetcher(ctx, owner, name, num)
					if perr != nil {
						log.Printf("depgraph: failed to fetch body for %s#%d: %v", repoFull, num, perr)
						continue
					}
					bodiesByKey[fmt.Sprintf("%s#%d", repoFull, num)] = body
				}
				continue
			}
			for num, body := range bodies {
				bodiesByKey[fmt.Sprintf("%s#%d", repoFull, num)] = body
			}
		}
	} else {
		// No bulk fetcher provided (test path). Use the legacy per-issue
		// loop so existing tests don't have to plumb a batch fake through
		// every assertion.
		for _, node := range g.Nodes {
			body, err := bodyFetcher(ctx, splitOwner(node.Repo), splitName(node.Repo), node.Number)
			if err != nil {
				log.Printf("depgraph: failed to fetch body for %s#%d: %v", node.Repo, node.Number, err)
				continue
			}
			bodiesByKey[fmt.Sprintf("%s#%d", node.Repo, node.Number)] = body
		}
	}

	// Parse cross-repo refs from the fetched bodies. Iteration order matches
	// g.Nodes for deterministic edge ordering across runs.
	for _, node := range g.Nodes {
		body, ok := bodiesByKey[fmt.Sprintf("%s#%d", node.Repo, node.Number)]
		if !ok {
			continue
		}
		refs := ParseCrossRepoRefs(body, repoAliases)
		for _, ref := range refs {
			if ref.Repo == node.Repo && ref.Number == node.Number {
				continue // self-reference
			}
			edge := Edge{
				From:       node.ID(),
				To:         NodeID{Repo: ref.Repo, Number: ref.Number},
				Type:       "crossRepo",
				Source:     ref.Source,
				Resolvable: workspaceRepos[ref.Repo],
			}
			eKey := edgeKey(edge)
			if !edgeSeen[eKey] {
				edgeSeen[eKey] = true
				g.AddEdge(edge)
			}
		}
	}

	// Compute topology
	g.Waves, g.Cycles = ComputeWaves(g)
	g.CriticalPath = ComputeCriticalPath(g)
	g.ComputeStats()

	return g, nil
}

// BuildGraphFromItems constructs the graph from pre-fetched items and bodies.
// This is useful for testing or when data is already available.
func BuildGraphFromItems(items []types.BoardItem, bodies map[string]string, workspaceRepos map[string]bool, repoAliases map[string]string) *Graph {
	g := NewGraph()
	edgeSeen := make(map[string]bool)

	for i := range items {
		item := &items[i]
		if item.IsPR {
			continue
		}

		node := boardItemToNode(item, item.Repo)
		g.AddNode(node)

		// blockedBy from board data
		for _, blocker := range item.BlockedBy {
			edge := Edge{
				From:       node.ID(),
				To:         NodeID{Repo: blocker.Repo, Number: blocker.Number},
				Type:       "blockedBy",
				Source:     "graphql",
				Resolvable: workspaceRepos[blocker.Repo],
			}
			eKey := edgeKey(edge)
			if !edgeSeen[eKey] {
				edgeSeen[eKey] = true
				g.AddEdge(edge)
			}
		}
	}

	// Cross-repo references from bodies
	for key, body := range bodies {
		node, ok := g.Nodes[key]
		if !ok {
			continue
		}
		refs := ParseCrossRepoRefs(body, repoAliases)
		for _, ref := range refs {
			if ref.Repo == node.Repo && ref.Number == node.Number {
				continue
			}
			edge := Edge{
				From:       node.ID(),
				To:         NodeID{Repo: ref.Repo, Number: ref.Number},
				Type:       "crossRepo",
				Source:     ref.Source,
				Resolvable: workspaceRepos[ref.Repo],
			}
			eKey := edgeKey(edge)
			if !edgeSeen[eKey] {
				edgeSeen[eKey] = true
				g.AddEdge(edge)
			}
		}
	}

	g.Waves, g.Cycles = ComputeWaves(g)
	g.CriticalPath = ComputeCriticalPath(g)
	g.ComputeStats()

	return g
}

// fetchBoardItems fetches all board items for a repo.
func fetchBoardItems(ctx context.Context, client *gh.Client, repo RepoConfig) ([]types.BoardItem, error) {
	boardSvc := gh.NewBoardService(client, repo.Owner, repo.Project, repo.OwnerType)
	// Use server-side "is:open" filter to avoid paginating through hundreds of
	// closed/archived items. The autonomous scheduler only needs open issues.
	return boardSvc.ListItems(ctx, "")
}

// FetchOpenBoardItems fetches only open items from a repo's project board.
// Returns the filtered items, the raw node count from GraphQL (before
// nodeToItem filtering), and any error.
func FetchOpenBoardItems(ctx context.Context, client *gh.Client, repo RepoConfig) ([]types.BoardItem, int, error) {
	boardSvc := gh.NewBoardService(client, repo.Owner, repo.Project, repo.OwnerType)
	return boardSvc.ListOpenItems(ctx)
}

// boardItemToNode converts a BoardItem to a graph Node.
func boardItemToNode(item *types.BoardItem, repoName string) *Node {
	// Prefer the item's own repo (from GraphQL Repository.NameWithOwner) when
	// available — a single project board can contain issues from multiple repos.
	effectiveRepo := repoName
	if item.Repo != "" {
		effectiveRepo = item.Repo
	}
	node := &Node{
		Repo:        effectiveRepo,
		Number:      item.Number,
		Title:       item.Title,
		State:       item.State,
		BoardStatus: item.Status, // Project board status (Ready, Backlog, etc.)
		Size:        string(item.Size),
		Priority:    string(item.Priority),
		Labels:      item.Labels,
	}
	if item.ParentNumber != 0 {
		node.EpicNumber = item.ParentNumber
	}
	node.Weight = SizeWeight(node.Size)
	return node
}

// edgeKey returns a dedup key for an edge.
func edgeKey(e Edge) string {
	return fmt.Sprintf("%s->%s", e.From.String(), e.To.String())
}

// splitOwner extracts the owner from "owner/repo".
func splitOwner(fullName string) string {
	parts := strings.SplitN(fullName, "/", 2)
	if len(parts) < 2 {
		return fullName
	}
	return parts[0]
}

// splitName extracts the repo name from "owner/repo".
func splitName(fullName string) string {
	parts := strings.SplitN(fullName, "/", 2)
	if len(parts) < 2 {
		return fullName
	}
	return parts[1]
}

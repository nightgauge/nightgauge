package orchestrator

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/depgraph"
	"github.com/nightgauge/nightgauge/internal/doctor"
	"github.com/nightgauge/nightgauge/internal/layout"
)

// ProducerDependencyCycle names the card for a cycle in open native blockedBy
// edges (#3081). Every issue in such a cycle waits on another, so none of
// them can ever dispatch, and before this nothing said so.
const ProducerDependencyCycle = "dependency-cycle"

// DependencyCyclesFile is the record of the cycles the scheduler last saw, in
// the pipeline state directory. doctor reads it (NGD114), and owns the name.
const DependencyCyclesFile = doctor.DependencyCyclesFile

// cycleEdge is one blockedBy edge of a cycle: Blocked waits on Blocker.
type cycleEdge struct {
	Blocked string `json:"blocked"`
	Blocker string `json:"blocker"`
}

// dependencyCycle is one strongly connected set of open issues joined by
// native blockedBy edges. Issues and Edges are sorted, so the same cycle has
// the same signature however the graph lists it.
type dependencyCycle struct {
	Issues []string    `json:"issues"`
	Edges  []cycleEdge `json:"edges"`
}

// signature identifies the cycle by its issues: rotation- and order-free.
func (c dependencyCycle) signature() string { return strings.Join(c.Issues, ",") }

// fingerprint changes when the cycle's edges change, so a different set of
// edges among the same issues re-alerts.
func (c dependencyCycle) fingerprint() string {
	parts := make([]string, len(c.Edges))
	for i, e := range c.Edges {
		parts[i] = e.Blocked + ">" + e.Blocker
	}
	return "cycle:" + strings.Join(parts, ",")
}

// detectDependencyCycles returns every cycle among OPEN issues in g's native
// blockedBy edges (graph edges sourced from the forge's own relation, not
// issue-body prose, which is what issue.removeBlockedBy can remove). A cycle
// is a strongly connected component of two or more issues, or one issue that
// blocks itself; its Issues and Edges are sorted, and the cycles are sorted by
// signature. Pure: same graph, same answer.
func detectDependencyCycles(g *depgraph.Graph) []dependencyCycle {
	if g == nil {
		return nil
	}
	open := func(key string) bool {
		n, ok := g.Nodes[key]
		return ok && strings.EqualFold(n.State, "OPEN")
	}
	adj := map[string][]string{}
	var edges []cycleEdge
	seenEdge := map[cycleEdge]bool{}
	for _, e := range g.Edges {
		if e.Source != depgraph.EdgeSourceGraphQL {
			continue
		}
		from, to := g.NodeKey(e.From), g.NodeKey(e.To)
		if !open(from) || !open(to) {
			continue
		}
		ce := cycleEdge{Blocked: from, Blocker: to}
		if seenEdge[ce] {
			continue
		}
		seenEdge[ce] = true
		edges = append(edges, ce)
		adj[from] = append(adj[from], to)
	}
	nodes := make([]string, 0, len(adj))
	for k := range adj {
		nodes = append(nodes, k)
		sort.Strings(adj[k])
	}
	sort.Strings(nodes)

	// Tarjan's strongly connected components.
	index := map[string]int{}
	low := map[string]int{}
	onStack := map[string]bool{}
	var stack []string
	next := 0
	comp := map[string]int{}
	var comps [][]string
	var visit func(v string)
	visit = func(v string) {
		index[v], low[v] = next, next
		next++
		stack = append(stack, v)
		onStack[v] = true
		for _, w := range adj[v] {
			if _, seen := index[w]; !seen {
				visit(w)
				low[v] = min(low[v], low[w])
			} else if onStack[w] {
				low[v] = min(low[v], index[w])
			}
		}
		if low[v] == index[v] {
			var c []string
			for {
				w := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				onStack[w] = false
				comp[w] = len(comps)
				c = append(c, w)
				if w == v {
					break
				}
			}
			comps = append(comps, c)
		}
	}
	for _, v := range nodes {
		if _, seen := index[v]; !seen {
			visit(v)
		}
	}

	byComp := map[int]*dependencyCycle{}
	for _, e := range edges {
		cf, okF := comp[e.Blocked]
		ct, okT := comp[e.Blocker]
		if !okF || !okT || cf != ct {
			continue
		}
		members := comps[cf]
		if len(members) < 2 && e.Blocked != e.Blocker {
			continue
		}
		c := byComp[cf]
		if c == nil {
			c = &dependencyCycle{Issues: append([]string(nil), members...)}
			sort.Strings(c.Issues)
			byComp[cf] = c
		}
		c.Edges = append(c.Edges, e)
	}
	out := make([]dependencyCycle, 0, len(byComp))
	for _, c := range byComp {
		sort.Slice(c.Edges, func(i, j int) bool {
			if c.Edges[i].Blocked != c.Edges[j].Blocked {
				return c.Edges[i].Blocked < c.Edges[j].Blocked
			}
			return c.Edges[i].Blocker < c.Edges[j].Blocker
		})
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].signature() < out[j].signature() })
	return out
}

// DependencyCycleEdgeOption is the option that removes one cycle edge, blocked
// waiting on blocker (both "owner/repo#N"), through issue.removeBlockedBy. The
// option names both issues and both repositories, because a cycle's edges join
// different issues, possibly across repositories. ok is false for a key that
// names no issue.
func DependencyCycleEdgeOption(n int, blocked, blocker string) (attention.Option, bool) {
	blockedRepo, blockedN, okA := splitNodeKey(blocked)
	blockerRepo, blockerN, okB := splitNodeKey(blocker)
	if !okA || !okB {
		return attention.Option{}, false
	}
	return attention.Option{
		ID:    fmt.Sprintf("remove-edge-%d", n),
		Label: fmt.Sprintf("Remove: %s#%d blocked by %s#%d", blockedRepo, blockedN, blockerRepo, blockerN),
		Verb:  attention.VerbIssueRemoveBlockedBy,
		Args: map[string]any{
			"blockedRepo": blockedRepo, "blockedNumber": blockedN,
			"blockerRepo": blockerRepo, "blockerNumber": blockerN,
		},
		Style: attention.StyleDefault,
	}, true
}

func keyDependencyCycle(c dependencyCycle) string {
	return ProducerDependencyCycle + ":" + c.signature()
}

// BuildDependencyCycleCard is the card for one cycle: it names every issue and
// edge and offers to remove each edge, one option per edge, through
// issue.removeBlockedBy. Standing, keyed by the cycle's issues, so each pass
// that still sees the cycle refreshes one card, and the pass that no longer
// sees it retracts the card.
func BuildDependencyCycleCard(c dependencyCycle) attention.DecisionRequest {
	short := func(key string) string {
		repo, n, ok := splitNodeKey(key)
		if !ok {
			return key
		}
		return fmt.Sprintf("%s#%d", repo, n)
	}
	var body strings.Builder
	fmt.Fprintf(&body, "These %d open issues block each other through native blockedBy edges, so none of them "+
		"can ever dispatch:\n\n", len(c.Issues))
	for _, k := range c.Issues {
		fmt.Fprintf(&body, "- %s\n", short(k))
	}
	body.WriteString("\nEdges (blocked → blocked by):\n\n")
	options := make([]attention.Option, 0, len(c.Edges)+1)
	for i, e := range c.Edges {
		fmt.Fprintf(&body, "- %s → %s\n", short(e.Blocked), short(e.Blocker))
		if opt, ok := DependencyCycleEdgeOption(i+1, e.Blocked, e.Blocker); ok {
			options = append(options, opt)
		}
	}
	body.WriteString("\nRemove the edge that is wrong. The card clears on the next scan that no longer sees the cycle.")
	options = append(options, noopOption("leave", "Leave the cycle"))
	var repo string
	var issue int
	for _, k := range c.Issues {
		if r, n, ok := splitNodeKey(k); ok {
			repo, issue = r, n
			break
		}
	}
	return attention.DecisionRequest{
		IdempotencyKey: keyDependencyCycle(c),
		Kind:           attention.KindChoose,
		Severity:       attention.SeverityBlockingRun,
		Title:          fmt.Sprintf("Dependency cycle: %d issues block each other", len(c.Issues)),
		Body:           body.String(),
		Producer:       ProducerDependencyCycle,
		Standing:       true,
		Fingerprint:    c.fingerprint(),
		Context:        attention.Context{Repo: repo, Issue: issue, Blocker: "blockedBy cycle: " + c.signature()},
		Options:        options,
		DefaultAction:  "leave",
		ExpiresAt:      standingExpiry(),
	}
}

// reconcileDependencyCycles raises one card per cycle the graph holds and
// retracts the card of every cycle it no longer holds (#3081). The graph is
// the complete set, so the retraction is sound. It records the cycles for
// doctor when they change.
func (as *AutonomousScheduler) reconcileDependencyCycles(g *depgraph.Graph, now time.Time) {
	cycles := detectDependencyCycles(g)
	observed := make([]string, 0, len(cycles))
	for _, c := range cycles {
		observed = append(observed, keyDependencyCycle(c))
		as.raiseAttention(BuildDependencyCycleCard(c))
	}
	as.autoResolveAttention(ProducerDependencyCycle, observed)

	sig := strings.Join(observed, "|")
	as.mu.Lock()
	changed := !as.cyclesRecorded || sig != as.lastCycleSignature
	as.mu.Unlock()
	if !changed {
		return
	}
	for _, c := range cycles {
		log.Printf("autonomous: dependency cycle among %s — none of them can dispatch (#3081)", c.signature())
	}
	// The signature is recorded only after a successful write, so a failed
	// write is retried on the next pass instead of leaving doctor stale.
	if err := writeDependencyCycleRecord(as.workspaceRoot, cycles, now); err != nil {
		log.Printf("autonomous: could not record dependency cycles for doctor: %v", err)
		return
	}
	as.mu.Lock()
	as.lastCycleSignature, as.cyclesRecorded = sig, true
	as.mu.Unlock()
}

// writeDependencyCycleRecord writes the cycles to DependencyCyclesFile under
// root's pipeline state directory, replacing the previous record. No root, or
// a root outside git, records nothing.
func writeDependencyCycleRecord(root string, cycles []dependencyCycle, now time.Time) error {
	if root == "" {
		return nil
	}
	dir, err := layout.PipelineStateDir(root)
	if errors.Is(err, layout.ErrNotGitRepository) {
		return nil
	}
	if err != nil {
		return err
	}
	if cycles == nil {
		cycles = []dependencyCycle{}
	}
	data, err := json.MarshalIndent(struct {
		ObservedAt string            `json:"observed_at"`
		Cycles     []dependencyCycle `json:"cycles"`
	}{now.UTC().Format(time.RFC3339), cycles}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, DependencyCyclesFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

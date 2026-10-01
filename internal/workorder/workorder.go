// Package workorder ranks Ready, unblocked issues by an ordered programs file.
//
// A programs file names the workspace's programs in rank order, each selecting
// its issues by a label:
//
//	label_prefix: "program:"      # optional; selector is <prefix><id>
//	ready_status: Ready           # optional board Status that means "ready"
//	exclude_labels: [blocked]     # optional; an issue carrying one is blocked
//	programs:
//	  - id: billing
//	    rank: 1
//	    title: Customers can pay
//	    repos: [acme-api, acme-web]   # optional; default every repo read
//	  - id: onboarding
//	    rank: 2
//	    label: area:onboarding        # optional explicit selector
//
// Rank answers, per program in rank order, which of its issues are on-board
// Ready and unblocked (dispatchable now) and which are Ready but blocked, then
// lists Ready, unblocked issues no program selects. Blockers come from the
// cross-repo dependency graph (internal/depgraph): an issue is unblocked when
// it is in the graph's first topological wave, i.e. no open issue on a board
// that was read blocks it, and it is not in a cycle.
//
// Rank is pure and deterministic. No LLM participates (#1481).
package workorder

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/nightgauge/nightgauge/internal/depgraph"
	"gopkg.in/yaml.v3"
)

// Defaults for the optional programs-file keys.
const (
	DefaultLabelPrefix = "program:"
	DefaultReadyStatus = "Ready"
	BlockedLabel       = "blocked"
)

// Program is one entry of the programs file.
type Program struct {
	ID    string   `yaml:"id" json:"id"`
	Rank  *int     `yaml:"rank" json:"rank,omitempty"`
	Title string   `yaml:"title" json:"title,omitempty"`
	Label string   `yaml:"label" json:"label,omitempty"`
	Repos []string `yaml:"repos" json:"repos,omitempty"`
}

// File is the programs file. Unknown keys are ignored, so a richer registry
// (goals, exit criteria, owner actions) can serve as the programs file as-is.
type File struct {
	LabelPrefix   string    `yaml:"label_prefix"`
	ReadyStatus   string    `yaml:"ready_status"`
	ExcludeLabels []string  `yaml:"exclude_labels"`
	Programs      []Program `yaml:"programs"`
}

// Load reads and validates a programs file.
func Load(path string) (*File, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

// Parse validates a programs file's bytes and fills defaults.
func Parse(b []byte) (*File, error) {
	var f File
	if err := yaml.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("programs file is not valid YAML: %w", err)
	}
	if f.LabelPrefix == "" {
		f.LabelPrefix = DefaultLabelPrefix
	}
	if f.ReadyStatus == "" {
		f.ReadyStatus = DefaultReadyStatus
	}
	if f.ExcludeLabels == nil {
		f.ExcludeLabels = []string{BlockedLabel}
	}
	seen := map[string]bool{}
	for i, p := range f.Programs {
		if strings.TrimSpace(p.ID) == "" {
			return nil, fmt.Errorf("programs[%d] has no id", i)
		}
		if seen[p.ID] {
			return nil, fmt.Errorf("program %q is listed twice", p.ID)
		}
		seen[p.ID] = true
	}
	return &f, nil
}

// Selector is the label that puts an issue in program p.
func (f *File) Selector(p Program) string {
	if p.Label != "" {
		return p.Label
	}
	return f.LabelPrefix + p.ID
}

// Ordered returns the programs by rank (unranked last), file order breaking
// ties.
func (f *File) Ordered() []Program {
	out := append([]Program(nil), f.Programs...)
	sort.SliceStable(out, func(i, j int) bool {
		return rankOf(out[i]) < rankOf(out[j])
	})
	return out
}

func rankOf(p Program) int {
	if p.Rank == nil {
		return int(^uint(0) >> 1)
	}
	return *p.Rank
}

// Repos is every repo the programs name, in first-mention order. A program
// with no repos applies to every repo read; it contributes nothing here.
func (f *File) Repos() []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range f.Programs {
		for _, r := range p.Repos {
			if !seen[r] {
				seen[r] = true
				out = append(out, r)
			}
		}
	}
	return out
}

// Item is one issue in the result.
type Item struct {
	Ref       string   `json:"ref"`
	Repo      string   `json:"repo"`
	Number    int      `json:"number"`
	Title     string   `json:"title"`
	Status    string   `json:"status"`
	Priority  string   `json:"priority,omitempty"`
	Size      string   `json:"size,omitempty"`
	Labels    []string `json:"labels"`
	BlockedBy []string `json:"blocked_by,omitempty"`
	// Unresolved lists dependencies on repos outside the read set. Their state
	// is unknown, so they do not block; they are reported for the consumer.
	Unresolved []string `json:"unresolved_dependencies,omitempty"`
}

// ProgramResult is one program's slice of the work.
type ProgramResult struct {
	ID       string   `json:"id"`
	Rank     *int     `json:"rank,omitempty"`
	Title    string   `json:"title,omitempty"`
	Selector string   `json:"selector"`
	Repos    []string `json:"repos"`
	Ready    []Item   `json:"ready"`
	Blocked  []Item   `json:"blocked"`
}

// Totals counts the result.
type Totals struct {
	Ready             int `json:"ready"`
	Blocked           int `json:"blocked"`
	UnprogrammedReady int `json:"unprogrammed_ready"`
}

// Result is the whole ranking. Its JSON shape is documented in
// docs/GO_BINARY.md § Handoff and Work-Order Operations.
type Result struct {
	ReadyStatus       string          `json:"ready_status"`
	ExcludeLabels     []string        `json:"exclude_labels"`
	Programs          []ProgramResult `json:"programs"`
	UnprogrammedReady []Item          `json:"unprogrammed_ready"`
	Totals            Totals          `json:"totals"`
}

// Rank classifies the graph's Ready issues by the programs file.
func Rank(f *File, g *depgraph.Graph) *Result {
	res := &Result{
		ReadyStatus:       f.ReadyStatus,
		ExcludeLabels:     append([]string{}, f.ExcludeLabels...),
		Programs:          []ProgramResult{},
		UnprogrammedReady: []Item{},
	}
	if g == nil {
		g = depgraph.NewGraph()
	}

	firstWave := map[string]bool{}
	if len(g.Waves) > 0 {
		for _, id := range g.Waves[0] {
			firstWave[id.String()] = true
		}
	}
	openBlockers := map[string][]string{}
	unresolved := map[string][]string{}
	for _, e := range g.Edges {
		from := e.From.String()
		to := e.To.String()
		if _, ok := g.Nodes[to]; ok {
			openBlockers[from] = append(openBlockers[from], to)
		} else if !e.Resolvable {
			unresolved[from] = append(unresolved[from], to)
		}
	}

	var ready []*depgraph.Node
	for _, n := range g.Nodes {
		if !strings.EqualFold(strings.TrimSpace(n.BoardStatus), f.ReadyStatus) {
			continue
		}
		if n.State != "" && !strings.EqualFold(n.State, "OPEN") {
			continue
		}
		ready = append(ready, n)
	}

	classify := func(n *depgraph.Node) (Item, bool) {
		key := n.ID().String()
		it := Item{
			Ref:        key,
			Repo:       n.Repo,
			Number:     n.Number,
			Title:      n.Title,
			Status:     n.BoardStatus,
			Priority:   n.Priority,
			Size:       n.Size,
			Labels:     append([]string{}, n.Labels...),
			BlockedBy:  dedupe(openBlockers[key]),
			Unresolved: dedupe(unresolved[key]),
		}
		if it.Labels == nil {
			it.Labels = []string{}
		}
		blocked := len(it.BlockedBy) > 0
		for _, l := range f.ExcludeLabels {
			if hasLabel(n.Labels, l) {
				it.BlockedBy = append(it.BlockedBy, "label `"+l+"`")
				blocked = true
			}
		}
		// A truncated label set cannot prove an exclusion label is absent, so
		// it fails closed, as the dispatcher's owner-action guard does (#998).
		if n.LabelsTruncated {
			it.BlockedBy = append(it.BlockedBy, "labels truncated: exclusions unverifiable")
			blocked = true
		}
		if !blocked && len(g.Waves) > 0 && !firstWave[key] {
			// Not in the first wave and no open blocker named: a cycle.
			it.BlockedBy = append(it.BlockedBy, "dependency cycle")
			blocked = true
		}
		return it, blocked
	}

	programmed := map[string]bool{}
	for _, p := range f.Ordered() {
		sel := f.Selector(p)
		pr := ProgramResult{
			ID:       p.ID,
			Rank:     p.Rank,
			Title:    p.Title,
			Selector: sel,
			Repos:    append([]string{}, p.Repos...),
			Ready:    []Item{},
			Blocked:  []Item{},
		}
		for _, n := range ready {
			if !hasLabel(n.Labels, sel) || !inRepos(n.Repo, p.Repos) {
				continue
			}
			programmed[n.ID().String()] = true
			it, blocked := classify(n)
			if blocked {
				pr.Blocked = append(pr.Blocked, it)
			} else {
				pr.Ready = append(pr.Ready, it)
			}
		}
		sortItems(pr.Ready)
		sortItems(pr.Blocked)
		res.Totals.Ready += len(pr.Ready)
		res.Totals.Blocked += len(pr.Blocked)
		res.Programs = append(res.Programs, pr)
	}

	for _, n := range ready {
		if programmed[n.ID().String()] {
			continue
		}
		if it, blocked := classify(n); !blocked {
			res.UnprogrammedReady = append(res.UnprogrammedReady, it)
		}
	}
	sortItems(res.UnprogrammedReady)
	res.Totals.UnprogrammedReady = len(res.UnprogrammedReady)
	return res
}

var priorityOrder = map[string]int{"P0": 0, "P1": 1, "P2": 2, "P3": 3}

func priorityRank(p string) int {
	if r, ok := priorityOrder[strings.ToUpper(strings.TrimSpace(p))]; ok {
		return r
	}
	return len(priorityOrder)
}

// sortItems orders by board Priority (P0 first, none last), then repo, then
// issue number, so the output is byte-stable for one board read.
func sortItems(items []Item) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if pa, pb := priorityRank(a.Priority), priorityRank(b.Priority); pa != pb {
			return pa < pb
		}
		if a.Repo != b.Repo {
			return a.Repo < b.Repo
		}
		return a.Number < b.Number
	})
}

func hasLabel(labels []string, want string) bool {
	for _, l := range labels {
		if strings.EqualFold(l, want) {
			return true
		}
	}
	return false
}

// inRepos matches a node's repo ("owner/name") against a program's repo list,
// which may name repos bare or qualified. An empty list matches every repo.
func inRepos(repo string, repos []string) bool {
	if len(repos) == 0 {
		return true
	}
	bare := repo
	if i := strings.LastIndex(repo, "/"); i >= 0 {
		bare = repo[i+1:]
	}
	for _, r := range repos {
		if strings.EqualFold(r, repo) || strings.EqualFold(r, bare) {
			return true
		}
	}
	return false
}

func dedupe(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

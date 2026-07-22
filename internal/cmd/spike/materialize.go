// Package spike implements the spike-materialize stage: parses the YAML
// recommendations block from a spike artifact and creates follow-up GitHub
// issues idempotently. See docs/SPIKE_CONTRACT.md for the artifact contract.
package spike

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Recommendation is one entry in the YAML recommendations block.
type Recommendation struct {
	ID        string   `yaml:"id" json:"id"`
	Action    string   `yaml:"action" json:"action"`
	Title     string   `yaml:"title" json:"title"`
	Type      string   `yaml:"type" json:"type"`
	Priority  string   `yaml:"priority" json:"priority"`
	Size      string   `yaml:"size" json:"size"`
	Labels    []string `yaml:"labels,omitempty" json:"labels,omitempty"`
	Body      string   `yaml:"body,omitempty" json:"body,omitempty"`
	DependsOn []string `yaml:"depends_on,omitempty" json:"depends_on,omitempty"`
}

// SpikeArtifact is the parsed YAML recommendations block.
type SpikeArtifact struct {
	Spike           int              `yaml:"spike" json:"spike"`
	Recommendations []Recommendation `yaml:"recommendations" json:"recommendations"`
}

// Allowed enum values per docs/SPIKE_CONTRACT.md.
var (
	allowedActions    = map[string]bool{"adopt": true, "defer": true, "skip": true}
	allowedTypes      = map[string]bool{"feature": true, "bug": true, "docs": true, "chore": true, "spike": true}
	allowedPriorities = map[string]bool{"critical": true, "high": true, "medium": true, "low": true}
	allowedSizes      = map[string]bool{"XS": true, "S": true, "M": true, "L": true, "XL": true}
	kebabCase         = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
)

// recommendationsBlockRe matches a fenced block with info string
// "yaml recommendations" (allowing surrounding whitespace).
var recommendationsBlockRe = regexp.MustCompile("(?ms)^```yaml +recommendations\\s*\\n(.*?)\\n```\\s*$")

// ParseArtifact reads a spike Markdown artifact and extracts the
// `yaml recommendations` fenced block. Returns an error when the block is
// missing or unparseable.
func ParseArtifact(path string) (*SpikeArtifact, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read artifact %s: %w", path, err)
	}
	return ParseArtifactBytes(data)
}

// ParseArtifactBytes is the in-memory variant of ParseArtifact.
func ParseArtifactBytes(data []byte) (*SpikeArtifact, error) {
	matches := recommendationsBlockRe.FindSubmatch(data)
	if len(matches) < 2 {
		return nil, fmt.Errorf("no fenced ```yaml recommendations block found — see docs/SPIKE_CONTRACT.md")
	}

	// Reject multiple recommendations blocks: ambiguous which is authoritative.
	all := recommendationsBlockRe.FindAllSubmatch(data, -1)
	if len(all) > 1 {
		return nil, fmt.Errorf("multiple ```yaml recommendations blocks found (%d) — exactly one required", len(all))
	}

	var art SpikeArtifact
	if err := yaml.Unmarshal(matches[1], &art); err != nil {
		return nil, fmt.Errorf("parse recommendations YAML: %w", err)
	}

	// Reject unknown top-level fields by re-decoding into a map and checking.
	var raw map[string]interface{}
	if err := yaml.Unmarshal(matches[1], &raw); err != nil {
		return nil, fmt.Errorf("parse recommendations YAML (raw): %w", err)
	}
	for k := range raw {
		if k != "spike" && k != "recommendations" {
			return nil, fmt.Errorf("unknown top-level field %q (allowed: spike, recommendations)", k)
		}
	}
	if rawList, ok := raw["recommendations"].([]interface{}); ok {
		allowed := map[string]bool{
			"id": true, "action": true, "title": true, "type": true,
			"priority": true, "size": true, "labels": true, "body": true,
			"depends_on": true,
		}
		for i, entry := range rawList {
			m, ok := entry.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("recommendation[%d]: must be a mapping", i)
			}
			for k := range m {
				if !allowed[k] {
					return nil, fmt.Errorf("recommendation[%d]: unknown field %q", i, k)
				}
			}
		}
	}

	return &art, nil
}

// ValidateSchema enforces required fields, enum constraints, id uniqueness,
// and topological solvability of depends_on. Returns the first violation
// encountered with a descriptive error.
func ValidateSchema(art *SpikeArtifact) error {
	if art == nil {
		return fmt.Errorf("artifact is nil")
	}
	if art.Spike <= 0 {
		return fmt.Errorf("spike: must be a positive integer (got %d)", art.Spike)
	}
	if len(art.Recommendations) == 0 {
		return fmt.Errorf("recommendations: at least one entry required")
	}

	seen := make(map[string]bool, len(art.Recommendations))
	for i, r := range art.Recommendations {
		ref := fmt.Sprintf("recommendation[%d] (id=%q)", i, r.ID)
		if r.ID == "" {
			return fmt.Errorf("%s: id is required", ref)
		}
		if !kebabCase.MatchString(r.ID) {
			return fmt.Errorf("%s: id must be kebab-case (got %q)", ref, r.ID)
		}
		if seen[r.ID] {
			return fmt.Errorf("duplicate id %q", r.ID)
		}
		seen[r.ID] = true

		if !allowedActions[r.Action] {
			return fmt.Errorf("%s: action must be one of adopt|defer|skip (got %q)", ref, r.Action)
		}
		if strings.TrimSpace(r.Title) == "" {
			return fmt.Errorf("%s: title is required", ref)
		}
		if !allowedTypes[r.Type] {
			return fmt.Errorf("%s: type must be one of feature|bug|docs|chore|spike (got %q)", ref, r.Type)
		}
		if !allowedPriorities[r.Priority] {
			return fmt.Errorf("%s: priority must be one of critical|high|medium|low (got %q)", ref, r.Priority)
		}
		if !allowedSizes[r.Size] {
			return fmt.Errorf("%s: size must be one of XS|S|M|L|XL (got %q)", ref, r.Size)
		}
	}

	for i, r := range art.Recommendations {
		for _, dep := range r.DependsOn {
			if !seen[dep] {
				return fmt.Errorf("recommendation[%d] (id=%q): depends_on references unknown id %q", i, r.ID, dep)
			}
		}
	}

	if _, err := topoSort(art.Recommendations); err != nil {
		return err
	}
	return nil
}

// topoSort returns the recommendations in dependency order (a recommendation's
// dependencies appear before it). Returns an error if a cycle is detected.
func topoSort(recs []Recommendation) ([]Recommendation, error) {
	indeg := make(map[string]int, len(recs))
	byID := make(map[string]Recommendation, len(recs))
	dependents := make(map[string][]string, len(recs))

	// Stable order: keep YAML order as the secondary sort key.
	order := make([]string, 0, len(recs))
	for _, r := range recs {
		indeg[r.ID] = len(r.DependsOn)
		byID[r.ID] = r
		order = append(order, r.ID)
		for _, dep := range r.DependsOn {
			dependents[dep] = append(dependents[dep], r.ID)
		}
	}

	queue := make([]string, 0, len(recs))
	for _, id := range order {
		if indeg[id] == 0 {
			queue = append(queue, id)
		}
	}
	sort.Strings(queue)

	out := make([]Recommendation, 0, len(recs))
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		out = append(out, byID[id])

		nextWave := make([]string, 0)
		for _, dep := range dependents[id] {
			indeg[dep]--
			if indeg[dep] == 0 {
				nextWave = append(nextWave, dep)
			}
		}
		sort.Strings(nextWave)
		queue = append(queue, nextWave...)
	}

	if len(out) != len(recs) {
		var stuck []string
		for id, d := range indeg {
			if d > 0 {
				stuck = append(stuck, id)
			}
		}
		sort.Strings(stuck)
		return nil, fmt.Errorf("depends_on cycle detected involving: %s", strings.Join(stuck, ", "))
	}
	return out, nil
}

// MaterializedIssue reports the result of processing one recommendation.
type MaterializedIssue struct {
	ID            string `json:"id"`
	Action        string `json:"action"`
	Title         string `json:"title"`
	IssueNumber   int    `json:"issue_number,omitempty"`
	URL           string `json:"url,omitempty"`
	AlreadyExists bool   `json:"already_exists,omitempty"`
	Skipped       bool   `json:"skipped,omitempty"`
	DryRun        bool   `json:"dry_run,omitempty"`
}

// MaterializeResult is the structured output of a materialize run.
type MaterializeResult struct {
	Spike     int                 `json:"spike"`
	Repo      string              `json:"repo"`
	DryRun    bool                `json:"dry_run"`
	Issues    []MaterializedIssue `json:"issues"`
	BlockedBy []BlockedByEdge     `json:"blocked_by,omitempty"`
}

// BlockedByEdge records a `blockedBy` relationship that was (or would be) added.
type BlockedByEdge struct {
	BlockedID string `json:"blocked_id"`
	BlockerID string `json:"blocker_id"`
}

// Materializer is the dependency-injected interface to GitHub. It exposes only
// the operations spike-materialize needs, so tests can stand in a fake.
type Materializer interface {
	// FindExistingByID returns the issue number of an issue already linked as a
	// sub-issue of the spike whose body contains the marker for `id`. Returns
	// 0 when not found.
	FindExistingByID(ctx context.Context, spikeNumber int, id string) (int, string, error)

	// CreateIssue creates a new issue with the given title, body, and labels,
	// adds it to the project board with Priority/Size/Status fields, and links
	// it as a sub-issue of the spike. Returns the new issue number and URL.
	CreateIssue(ctx context.Context, spikeNumber int, rec Recommendation, body string) (int, string, error)

	// AddBlockedByByNumber adds a `blockedBy` edge: blockedNumber is blocked by
	// blockerNumber. Idempotent at the GitHub layer.
	AddBlockedByByNumber(ctx context.Context, blockedNumber, blockerNumber int) error
}

// MarkerFor returns the idempotency marker line for a recommendation.
func MarkerFor(spikeNumber int, id string) string {
	return fmt.Sprintf("<!-- spike-recommendation: id=%s spike=#%d -->", id, spikeNumber)
}

// BodyFor renders the issue body for a recommendation. The body always starts
// with the idempotency marker followed by either the author-supplied body or a
// generated stub linking back to the spike.
func BodyFor(spikeNumber int, rec Recommendation) string {
	body := strings.TrimSpace(rec.Body)
	if body == "" {
		body = fmt.Sprintf("Materialized from spike #%d recommendation `%s`.\n\nSee `docs/spikes/%d-*.md` for context.",
			spikeNumber, rec.ID, spikeNumber)
	}
	return MarkerFor(spikeNumber, rec.ID) + "\n\n" + body + fmt.Sprintf("\n\nPart of #%d", spikeNumber)
}

// Materialize processes recommendations in topological order. For each
// `adopt`/`defer` recommendation it either skips (already materialized) or
// creates an issue. `skip` recommendations are recorded but not created.
// When dryRun is true, no GitHub mutations occur — only lookups.
func Materialize(ctx context.Context, art *SpikeArtifact, repo string, m Materializer, dryRun bool) (*MaterializeResult, error) {
	if err := ValidateSchema(art); err != nil {
		return nil, err
	}
	ordered, err := topoSort(art.Recommendations)
	if err != nil {
		return nil, err
	}

	res := &MaterializeResult{
		Spike:  art.Spike,
		Repo:   repo,
		DryRun: dryRun,
	}

	// Map id → resolved issue number for blockedBy chain assembly.
	resolved := make(map[string]int, len(ordered))

	for _, rec := range ordered {
		mi := MaterializedIssue{ID: rec.ID, Action: rec.Action, Title: rec.Title, DryRun: dryRun}

		if rec.Action == "skip" {
			mi.Skipped = true
			res.Issues = append(res.Issues, mi)
			continue
		}

		existingNum, existingURL, err := m.FindExistingByID(ctx, art.Spike, rec.ID)
		if err != nil {
			return res, fmt.Errorf("idempotency lookup for id=%s: %w", rec.ID, err)
		}
		if existingNum != 0 {
			mi.AlreadyExists = true
			mi.IssueNumber = existingNum
			mi.URL = existingURL
			resolved[rec.ID] = existingNum
			res.Issues = append(res.Issues, mi)
			continue
		}

		if dryRun {
			// Record the would-be-created id so blockedBy planning sees it.
			resolved[rec.ID] = -1
			res.Issues = append(res.Issues, mi)
			continue
		}

		body := BodyFor(art.Spike, rec)
		num, url, err := m.CreateIssue(ctx, art.Spike, rec, body)
		if err != nil {
			return res, fmt.Errorf("create issue for id=%s: %w", rec.ID, err)
		}
		mi.IssueNumber = num
		mi.URL = url
		resolved[rec.ID] = num
		res.Issues = append(res.Issues, mi)
	}

	// Apply blockedBy chains. Run in topological order so blockers always
	// resolve before their dependents.
	for _, rec := range ordered {
		if rec.Action == "skip" {
			continue
		}
		blockedNum, ok := resolved[rec.ID]
		if !ok {
			continue
		}
		for _, depID := range rec.DependsOn {
			blockerNum, ok := resolved[depID]
			if !ok {
				continue
			}
			res.BlockedBy = append(res.BlockedBy, BlockedByEdge{
				BlockedID: rec.ID,
				BlockerID: depID,
			})
			if dryRun {
				continue
			}
			if err := m.AddBlockedByByNumber(ctx, blockedNum, blockerNum); err != nil {
				return res, fmt.Errorf("add blockedBy %s ← %s: %w", rec.ID, depID, err)
			}
		}
	}

	return res, nil
}

// LocateArtifact returns the path to the spike artifact for the given issue
// number, scanning `docs/spikes/{N}-*.md` under workdir. Returns the first
// match or an error when no candidate is found.
func LocateArtifact(workdir string, spikeNumber int) (string, error) {
	dir := workdir
	if dir == "" {
		dir = "."
	}
	spikesDir := dir + "/docs/spikes"
	entries, err := os.ReadDir(spikesDir)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", spikesDir, err)
	}
	prefix := fmt.Sprintf("%d-", spikeNumber)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, prefix) && strings.HasSuffix(name, ".md") {
			return spikesDir + "/" + name, nil
		}
	}
	return "", fmt.Errorf("no spike artifact found at %s/%s*.md", spikesDir, prefix)
}

package platform

import (
	"context"
	"fmt"
	"sync"
	"time"

	api "github.com/nightgauge/nightgauge/api/generated/go/platform"
)

// CachedSkill holds a resolved skill with cache metadata.
type CachedSkill struct {
	Stage    string
	Content  string
	Version  string
	Variant  string
	CachedAt time.Time
}

const skillCacheTTL = 1 * time.Hour

// SkillService resolves skills from the platform with local caching.
type SkillService struct {
	client *Client
	mu     sync.RWMutex
	cache  map[string]*CachedSkill // keyed by "stage:version"
}

// NewSkillService creates a skill resolution service.
func NewSkillService(client *Client) *SkillService {
	return &SkillService{
		client: client,
		cache:  make(map[string]*CachedSkill),
	}
}

// Resolve fetches a skill for a pipeline stage, using cache when available.
func (s *SkillService) Resolve(ctx context.Context, stage string, opts *SkillResolveOptions) (*CachedSkill, error) {
	version := "latest"
	if opts != nil && opts.Version != "" {
		version = opts.Version
	}

	cacheKey := fmt.Sprintf("%s:%s", stage, version)

	// Check cache
	s.mu.RLock()
	cached, ok := s.cache[cacheKey]
	s.mu.RUnlock()

	if ok && time.Since(cached.CachedAt) < skillCacheTTL {
		return cached, nil
	}

	// If offline, return cached (any age) or error
	if !s.client.IsOnline() {
		if cached != nil {
			return cached, nil
		}
		return nil, fmt.Errorf("skill %s not cached and platform offline", stage)
	}

	// Resolve from platform
	pipelineStage := api.PipelineStage(stage)
	req := api.SkillResolveJSONRequestBody{
		Stage: pipelineStage,
	}
	if version != "latest" {
		req.Version = &version
	}
	if opts != nil {
		if opts.ComplexityScore > 0 || opts.IssueType != "" || opts.SizeLabel != "" {
			req.Context = &struct {
				ComplexityScore *int           `json:"complexity_score,omitempty"`
				IssueType       *api.IssueType `json:"issue_type,omitempty"`
				Labels          *[]string      `json:"labels,omitempty"`
				SizeLabel       *api.SizeLabel `json:"size_label,omitempty"`
			}{}
			if opts.ComplexityScore > 0 {
				req.Context.ComplexityScore = &opts.ComplexityScore
			}
			if opts.IssueType != "" {
				issueType := api.IssueType(opts.IssueType)
				req.Context.IssueType = &issueType
			}
			if opts.SizeLabel != "" {
				sizeLabel := api.SizeLabel(opts.SizeLabel)
				req.Context.SizeLabel = &sizeLabel
			}
		}
	}

	resp, err := s.client.api.SkillResolveWithResponse(ctx, req)
	if err != nil {
		if cached != nil {
			return cached, nil // Stale cache on error
		}
		return nil, fmt.Errorf("resolve skill %s: %w", stage, err)
	}

	if resp.JSON200 == nil {
		if cached != nil {
			return cached, nil
		}
		return nil, fmt.Errorf("resolve skill %s: unexpected response %d", stage, resp.StatusCode())
	}

	skill := &CachedSkill{
		Stage:    stage,
		Content:  resp.JSON200.SkillContent,
		Version:  resp.JSON200.Version,
		Variant:  resp.JSON200.Variant,
		CachedAt: time.Now(),
	}

	s.mu.Lock()
	s.cache[cacheKey] = skill
	s.mu.Unlock()

	return skill, nil
}

// SkillResolveOptions holds optional parameters for skill resolution.
type SkillResolveOptions struct {
	Version         string
	ComplexityScore int
	IssueType       string
	SizeLabel       string
}

// ClearCache removes all cached skills.
func (s *SkillService) ClearCache() {
	s.mu.Lock()
	s.cache = make(map[string]*CachedSkill)
	s.mu.Unlock()
}

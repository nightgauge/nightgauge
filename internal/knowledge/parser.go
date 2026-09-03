// Package knowledge implements knowledge base scaffolding, indexing,
// validation and recall. Knowledge files carry YAML frontmatter delimited by
// --- sentinels using the Open Knowledge Format v0.2 field vocabulary — one
// contract shared by the Go binary and the TypeScript SDK, implemented in
// internal/knowledge/okf and re-exported here. See docs/KNOWLEDGE_BASE.md for
// the full schema.
package knowledge

import (
	"fmt"
	"strings"

	"github.com/nightgauge/nightgauge/internal/knowledge/okf"
)

// The frontmatter contract lives in the leaf package internal/knowledge/okf so
// that packages this one depends on can also write conformant entries. These
// aliases keep it addressable as knowledge.X at every existing call site.
type (
	// FrontmatterBlock is the parsed frontmatter of a knowledge entry.
	FrontmatterBlock = okf.FrontmatterBlock
	// Provenance records who produced or confirmed an entry and when.
	Provenance = okf.Provenance
	// Source records material an entry was derived from.
	Source = okf.Source
)

const (
	// OKFVersion is the Open Knowledge Format revision this contract implements.
	OKFVersion = okf.OKFVersion

	// StatusDraft marks an entry nobody has reviewed.
	StatusDraft = okf.StatusDraft
	// StatusStable marks an entry treated as current guidance.
	StatusStable = okf.StatusStable
	// StatusDeprecated marks an entry kept for history only.
	StatusDeprecated = okf.StatusDeprecated
	// DefaultStatus is the status of an entry whose frontmatter omits it.
	DefaultStatus = okf.DefaultStatus

	// ScaffoldActor is the actor every deterministic scaffold path stamps.
	ScaffoldActor = okf.ScaffoldActor

	// Trust tiers, derived from the verified log.
	TrustHumanReviewed    = okf.TrustHumanReviewed
	TrustMachineConfirmed = okf.TrustMachineConfirmed
	TrustUnverified       = okf.TrustUnverified

	// Entry types stamped by the scaffold paths.
	TypePRD          = okf.TypePRD
	TypeDecisions    = okf.TypeDecisions
	TypeIndex        = okf.TypeIndex
	TypeLog          = okf.TypeLog
	TypeArchitecture = okf.TypeArchitecture
	TypeGlossary     = okf.TypeGlossary
	TypeRunbook      = okf.TypeRunbook
	TypePostMortem   = okf.TypePostMortem
)

// ErrStatusSuperseded is returned when an entry still carries the deleted
// `superseded` status.
var ErrStatusSuperseded = okf.ErrStatusSuperseded

var (
	// ParseFrontmatter extracts and parses YAML frontmatter from markdown.
	ParseFrontmatter = okf.ParseFrontmatter
	// ParseFrontmatterFile parses a file's frontmatter, naming it on failure.
	ParseFrontmatterFile = okf.ParseFrontmatterFile
	// SplitFrontmatter separates a document into frontmatter YAML and body.
	SplitFrontmatter = okf.SplitFrontmatter
	// RenderFrontmatter serialises a block back to a --- delimited document.
	RenderFrontmatter = okf.RenderFrontmatter
	// WithFrontmatter prefixes a body with a rendered block.
	WithFrontmatter = okf.WithFrontmatter
	// ScaffoldFrontmatter renders the block every scaffolded entry carries.
	ScaffoldFrontmatter = okf.ScaffoldFrontmatter
	// WithTags, WithTitle and WithRepos are ScaffoldFrontmatter options.
	WithTags  = okf.WithTags
	WithTitle = okf.WithTitle
	WithRepos = okf.WithRepos

	// ValidActor reports whether s matches the actor convention.
	ValidActor = okf.ValidActor
	// ValidateActor returns an error naming a malformed actor string.
	ValidateActor = okf.ValidateActor
	// StageActor builds `<stage>/<served-model>`.
	StageActor = okf.StageActor
	// ProcessActor builds `process:<id>`.
	ProcessActor = okf.ProcessActor
	// HumanActor builds `human:<id>`.
	HumanActor = okf.HumanActor
	// NewProvenance builds a Provenance stamped at the current time.
	NewProvenance = okf.NewProvenance
	// NowStamp is the current time as an RFC3339 provenance timestamp.
	NowStamp = okf.NowStamp
	// IsExpiredStamp reports whether a raw stale_after value has passed.
	IsExpiredStamp = okf.IsExpiredStamp
)

// WorkspaceRepository is a minimal interface for repo name validation.
// The full struct lives in the TypeScript layer; Go uses this subset.
type WorkspaceRepository struct {
	Name string `yaml:"name"`
	Path string `yaml:"path"`
	Role string `yaml:"role"`
}

// WorkspaceConfig holds the relevant subset of workspace config for repo validation.
type WorkspaceConfig struct {
	Repositories []WorkspaceRepository `yaml:"repositories"`
}

// ValidationError is returned when one or more repo names in frontmatter are
// not declared in the workspace configuration.
type ValidationError struct {
	UnknownRepos []string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("unknown repository names in frontmatter repos field: %s", strings.Join(e.UnknownRepos, ", "))
}

// ValidateRepos checks that every repo name in repoNames is declared in the
// workspace configuration. Returns a *ValidationError listing unknown names,
// or nil when all names are valid (or when repoNames is empty).
func ValidateRepos(repoNames []string, workspaceConfig *WorkspaceConfig) error {
	if len(repoNames) == 0 {
		return nil
	}
	if workspaceConfig == nil {
		return fmt.Errorf("workspace config is required for repo validation")
	}

	known := make(map[string]struct{}, len(workspaceConfig.Repositories))
	for _, r := range workspaceConfig.Repositories {
		known[r.Name] = struct{}{}
	}

	var unknown []string
	for _, name := range repoNames {
		if _, ok := known[name]; !ok {
			unknown = append(unknown, name)
		}
	}

	if len(unknown) > 0 {
		return &ValidationError{UnknownRepos: unknown}
	}
	return nil
}

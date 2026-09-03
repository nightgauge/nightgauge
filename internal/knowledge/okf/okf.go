// Package okf implements the single knowledge frontmatter contract, using the
// field vocabulary of the Open Knowledge Format v0.2 (Google Cloud,
// Apache-2.0).
//
// It is a leaf package with no other internal dependencies so that every
// producer of knowledge — the issue scaffolder, the workspace scaffolder, the
// stamp verb, the SDK's Go-side callers — writes through one implementation
// rather than hand-rolling YAML. We adopt the vocabulary only, not the
// reference tooling: the spec broke compatibility between v0.1 and v0.2 three
// months after publication, so fields are cheap to carry and tooling is not.
package okf

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// The knowledge base carries one frontmatter contract, shared by the Go binary
// and the TypeScript SDK, using the field vocabulary of the Open Knowledge
// Format v0.2 (Google Cloud, Apache-2.0). We adopt the vocabulary only, not the
// reference tooling: the spec broke compatibility between v0.1 and v0.2 three
// months after publication, so fields are cheap to carry and tooling is not.
//
// The contract is: type (required on every non-reserved entry), title,
// description, tags, related, repos, status, superseded_by, generated,
// verified, sources, stale_after. Unknown keys and unknown type values are
// accepted — an OKF consumer tolerates what it does not understand.

// OKFVersion is the Open Knowledge Format revision whose field vocabulary this
// contract implements. It is stamped into the root index bundle frontmatter.
const OKFVersion = "0.2"

// Lifecycle statuses. `superseded` was deleted in favour of `deprecated`
// alongside superseded_by; it is rejected rather than aliased.
const (
	StatusDraft      = "draft"
	StatusStable     = "stable"
	StatusDeprecated = "deprecated"
)

// DefaultStatus is the status an entry has when its frontmatter omits the field.
const DefaultStatus = StatusStable

// actorRe is the actor convention shared by generated.by and verified[].by:
// `<producer>/<version>` for agents (for example `feature-dev/claude-sonnet-5`),
// `human:<id>` for a person, `process:<id>` for a deterministic writer.
//
// Every actor the binary writes is constructed from stage and model
// identifiers or a fixed process name — never from model-authored prose.
var actorRe = regexp.MustCompile(`^([a-z0-9._-]+/[A-Za-z0-9._-]+|human:\S+|process:\S+)$`)

// ValidActor reports whether s matches the actor convention.
func ValidActor(s string) bool { return actorRe.MatchString(s) }

// ValidateActor returns an error naming the offending value when s is not a
// well-formed actor string.
func ValidateActor(s string) error {
	if !ValidActor(s) {
		return fmt.Errorf("invalid actor %q: want <producer>/<version>, human:<id> or process:<id>", s)
	}
	return nil
}

// StageActor builds the actor string for a pipeline stage running a served
// model, e.g. StageActor("feature-planning", "claude-sonnet-5") ->
// "feature-planning/claude-sonnet-5". Both inputs come from the pipeline
// context file, never from model output. An input that cannot produce a valid
// actor returns an error rather than a silently mangled string.
func StageActor(stage, servedModel string) (string, error) {
	actor := strings.ToLower(strings.TrimSpace(stage)) + "/" + strings.TrimSpace(servedModel)
	if err := ValidateActor(actor); err != nil {
		return "", fmt.Errorf("stage %q model %q: %w", stage, servedModel, err)
	}
	return actor, nil
}

// ProcessActor builds the actor string for a deterministic writer, e.g.
// ProcessActor("retro") -> "process:retro".
func ProcessActor(name string) (string, error) {
	actor := "process:" + strings.TrimSpace(name)
	if err := ValidateActor(actor); err != nil {
		return "", err
	}
	return actor, nil
}

// HumanActor builds the actor string for a person, e.g.
// HumanActor("octocat") -> "human:octocat".
func HumanActor(login string) (string, error) {
	actor := "human:" + strings.TrimSpace(login)
	if err := ValidateActor(actor); err != nil {
		return "", err
	}
	return actor, nil
}

// Provenance records who produced or confirmed an entry and when.
// It is the OKF `generated` object and each element of `verified`.
type Provenance struct {
	// By is an actor string; see ValidActor.
	By string `yaml:"by" json:"by"`
	// At is an RFC3339 timestamp.
	At string `yaml:"at,omitempty" json:"at,omitempty"`
}

// Source records material an entry was derived from: an issue URL, a merged
// PR, a specification, or another bundle entry.
type Source struct {
	// Resource is an https:// URL, a bundle-absolute path, or a
	// repository-relative path.
	Resource string `yaml:"resource" json:"resource"`
	// Title is an optional human-readable label.
	Title string `yaml:"title,omitempty" json:"title,omitempty"`
}

// Now is the clock frontmatter writers read. Tests replace it.
var Now = func() time.Time { return time.Now().UTC() }

// NowStamp returns the current time formatted for a provenance timestamp.
func NowStamp() string { return Now().UTC().Format(time.RFC3339) }

// NewProvenance builds a Provenance stamped at the current time, validating the
// actor.
func NewProvenance(by string) (Provenance, error) {
	if err := ValidateActor(by); err != nil {
		return Provenance{}, err
	}
	return Provenance{By: by, At: NowStamp()}, nil
}

// ScaffoldActor is the actor every deterministic scaffold path stamps into
// generated.by. Enrichment by a model stage overwrites it via `knowledge stamp`.
const ScaffoldActor = "process:knowledge-scaffold"

// Entry types stamped by the scaffold paths.
const (
	TypePRD          = "prd"
	TypeDecisions    = "decisions"
	TypeIndex        = "index"
	TypeLog          = "log"
	TypeArchitecture = "architecture"
	TypeGlossary     = "glossary"
	TypeRunbook      = "runbook"
	TypePostMortem   = "post-mortem"
)

// ScaffoldFrontmatter renders the frontmatter block every scaffolded entry
// carries: its type, a draft status, and provenance naming the scaffolder.
// Scaffolded content is a template nobody has reviewed, so it starts as a
// draft; the stamp verb promotes it once a stage or a person has been through
// it.
func ScaffoldFrontmatter(entryType string, opts ...func(*FrontmatterBlock)) (string, error) {
	gen, err := NewProvenance(ScaffoldActor)
	if err != nil {
		return "", err
	}
	block := &FrontmatterBlock{
		Type:      entryType,
		Status:    StatusDraft,
		Generated: &gen,
	}
	for _, o := range opts {
		o(block)
	}
	return RenderFrontmatter(block)
}

// WithTags sets the tags of a scaffolded block.
func WithTags(tags ...string) func(*FrontmatterBlock) {
	return func(b *FrontmatterBlock) { b.Tags = tags }
}

// WithTitle sets the title of a scaffolded block.
func WithTitle(title string) func(*FrontmatterBlock) {
	return func(b *FrontmatterBlock) { b.Title = title }
}

// WithRepos scopes a scaffolded workspace entry to a repository set.
func WithRepos(repos ...string) func(*FrontmatterBlock) {
	return func(b *FrontmatterBlock) { b.Repos = repos }
}

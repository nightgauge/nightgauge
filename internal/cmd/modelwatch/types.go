// Package modelwatch turns a vendor model release into reviewable work: a
// proposed registry patch, skill-overlay proposals and defect findings. It
// backs `nightgauge model-watch classify` and the nightgauge-model-watch skill.
//
// The classifier is provider-neutral by construction. Provider and capability
// bands come from the input directory's manifest.json, and every extraction
// rule is a generic sentence pattern, so a new provider's release needs a new
// manifest, not a code change.
//
// Vendor text is untrusted input. Facts enter the patch only as typed values
// (numbers, closed-set enums, ids matching a strict pattern). Source sentences
// appear only as quotes on findings, truncated to MaxQuoteLen, stripped of
// backticks, screened for prompt injection in block mode, and fenced when
// rendered into an issue body. The package never writes the registry.
package modelwatch

import "github.com/nightgauge/nightgauge/internal/models"

// SchemaVersion is the "v" field of every Result. Additive evolution only.
const SchemaVersion = 1

// MaxQuoteLen bounds a quoted source sentence, in runes.
const MaxQuoteLen = 240

// ManifestFile is the name of the manifest inside an input directory.
const ManifestFile = "manifest.json"

// Source is one fetched vendor page, stored as a file inside the input dir.
type Source struct {
	File string `json:"file"`
	URL  string `json:"url,omitempty"`
	Kind string `json:"kind,omitempty"`
}

// Manifest describes an input directory: whose release it is, which
// capability bands the new model serves, and where each page came from.
type Manifest struct {
	Provider    string   `json:"provider"`
	Tiers       []string `json:"tiers"`
	ModelID     string   `json:"model_id,omitempty"`
	DisplayName string   `json:"display_name,omitempty"`
	Sources     []Source `json:"sources"`
}

// Demotion deprecates a baseline model that the new model displaces from a
// (provider, band) pair, keeping band resolution unique.
type Demotion struct {
	ID          string `json:"id"`
	Replacement string `json:"replacement"`
}

// Patch is a proposed registry change. It is never applied by this package.
type Patch struct {
	Add         *models.ModelDescriptor `json:"add,omitempty"`
	Demote      []Demotion              `json:"demote"`
	TargetFiles []string                `json:"target_files"`
}

// IssueSpec is an issue ready for `nightgauge forge issue create`. Labels
// come only from IssueLabels.
type IssueSpec struct {
	Title  string   `json:"title"`
	Body   string   `json:"body"`
	Labels []string `json:"labels"`
}

// OverlayProposal proposes a skill-overlay change for one propensity axis.
type OverlayProposal struct {
	Axis   string    `json:"axis"`
	Level  string    `json:"level"`
	Target string    `json:"target"`
	Action string    `json:"action"`
	Quote  string    `json:"quote"`
	Source string    `json:"source"`
	Issue  IssueSpec `json:"issue"`
}

// Defect is a configuration the release makes invalid, filed as a bug.
type Defect struct {
	Kind    string    `json:"kind"`
	Summary string    `json:"summary"`
	Quote   string    `json:"quote"`
	Source  string    `json:"source"`
	Issue   IssueSpec `json:"issue"`
}

// Capability is a typed finding. Kind "effort_level" names an effort level the
// band's prior holder lacked; kind "unextracted" names a fact no rule found.
type Capability struct {
	Kind   string `json:"kind"`
	Value  string `json:"value"`
	Quote  string `json:"quote,omitempty"`
	Source string `json:"source,omitempty"`
}

// Dropped records a finding withheld because its quote failed screening.
type Dropped struct {
	Source string `json:"source"`
	Reason string `json:"reason"`
}

// Result is the classifier output.
type Result struct {
	V            int               `json:"v"`
	Provider     string            `json:"provider"`
	ModelID      string            `json:"model_id"`
	Patch        Patch             `json:"patch"`
	Overlays     []OverlayProposal `json:"overlays"`
	Defects      []Defect          `json:"defects"`
	Capabilities []Capability      `json:"capabilities"`
	Dropped      []Dropped         `json:"dropped"`
}

// Finding kinds and label names. IssueLabels is the only label source.
const (
	KindOverlay        = "overlay"
	KindDefect         = "defect"
	DefectThinking400  = "thinking_effort_400"
	CapEffortLevel     = "effort_level"
	CapUnextracted     = "unextracted"
	labelBug           = "type:bug"
	labelFeature       = "type:feature"
	actionCreate       = "create"
	actionEdit         = "edit"
	defaultOverlayRoot = "skills/_shared/_overlays"
)

// IssueLabels returns the closed label set for a finding kind. It never
// returns a label that would dispatch work or skip triage.
func IssueLabels(kind string) []string {
	switch kind {
	case KindDefect:
		return []string{labelBug}
	case KindOverlay:
		return []string{labelFeature}
	}
	return []string{}
}

package modelwatch

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/hooks"
	"github.com/nightgauge/nightgauge/internal/models"
)

// Options tunes Classify.
type Options struct {
	// OverlayRoot is the directory holding per-model overlay files. A proposal
	// targets an existing file as "edit" and a missing one as "create". Empty
	// means no overlay exists yet.
	OverlayRoot string
}

var (
	slugPattern    = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	displayPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 .\-]{0,63}$`)
	urlPattern     = regexp.MustCompile(`^https://[A-Za-z0-9./_#?=&%~:+\-]+$`)
)

// registryTargets are the files a maintainer edits to apply a patch; the
// first is the authority and scripts/sync-model-registry.sh copies it.
var registryTargets = []string{
	"packages/nightgauge-sdk/src/eval/model-registry.json",
	"internal/models/model-registry.json",
}

// LoadManifest reads and validates dir/manifest.json.
func LoadManifest(dir string) (Manifest, error) {
	var m Manifest
	raw, err := os.ReadFile(filepath.Join(dir, ManifestFile))
	if err != nil {
		return m, fmt.Errorf("read manifest: %w", err)
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return m, fmt.Errorf("decode manifest: %w", err)
	}
	if !slugPattern.MatchString(m.Provider) {
		return m, fmt.Errorf("manifest provider %q must match %s", m.Provider, slugPattern)
	}
	if len(m.Tiers) == 0 {
		return m, fmt.Errorf("manifest tiers must name at least one band")
	}
	for _, t := range m.Tiers {
		if !slugPattern.MatchString(t) {
			return m, fmt.Errorf("manifest tier %q must match %s", t, slugPattern)
		}
	}
	if m.ModelID != "" && !idPattern.MatchString(m.ModelID) {
		return m, fmt.Errorf("manifest model_id %q must match %s", m.ModelID, idPattern)
	}
	if m.DisplayName != "" && !displayPattern.MatchString(m.DisplayName) {
		return m, fmt.Errorf("manifest display_name %q must match %s", m.DisplayName, displayPattern)
	}
	if len(m.Sources) == 0 {
		return m, fmt.Errorf("manifest sources must name at least one file")
	}
	for _, s := range m.Sources {
		if s.File == "" || filepath.IsAbs(s.File) || !filepath.IsLocal(s.File) {
			return m, fmt.Errorf("manifest source file %q must be a relative path inside the input dir", s.File)
		}
		if s.URL != "" && !urlPattern.MatchString(s.URL) {
			return m, fmt.Errorf("manifest source url %q must be a plain https URL", s.URL)
		}
	}
	return m, nil
}

// LoadBaseline reads a registry snapshot ({"models": [...]}) from path.
func LoadBaseline(path string) ([]models.ModelDescriptor, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read registry: %w", err)
	}
	var rf struct {
		Models []models.ModelDescriptor `json:"models"`
	}
	if err := json.Unmarshal(raw, &rf); err != nil {
		return nil, fmt.Errorf("decode registry: %w", err)
	}
	return rf.Models, nil
}

// Classify turns the release pages in dir into a Result, comparing against
// the read-only baseline registry. A release naming no new model yields an
// empty patch and no findings.
func Classify(dir string, baseline []models.ModelDescriptor, opts Options) (Result, error) {
	man, err := LoadManifest(dir)
	if err != nil {
		return Result{}, err
	}
	var sentences []sentence
	for _, src := range man.Sources {
		raw, err := os.ReadFile(filepath.Join(dir, src.File))
		if err != nil {
			return Result{}, fmt.Errorf("read source: %w", err)
		}
		label := src.URL
		if label == "" {
			label = src.File
		}
		sentences = append(sentences, splitSentences(string(raw), label)...)
	}
	f := extract(sentences)

	res := Result{
		V: SchemaVersion, Provider: man.Provider,
		Patch:    Patch{Demote: []Demotion{}, TargetFiles: []string{}},
		Overlays: []OverlayProposal{}, Defects: []Defect{},
		Capabilities: []Capability{}, Dropped: []Dropped{},
	}
	id := man.ModelID
	if f.ModelID.Found && id != "" && f.ModelID.Value != id {
		return Result{}, fmt.Errorf("manifest model_id %q conflicts with %q named by the source", id, f.ModelID.Value)
	}
	if id == "" {
		id = f.ModelID.Value
	}
	res.ModelID = id
	if id == "" {
		return res, nil
	}
	for _, m := range baseline {
		if m.ID == id {
			return res, nil
		}
	}

	desc := describe(id, man, f)
	res.Patch.Add = &desc
	res.Patch.TargetFiles = append([]string{}, registryTargets...)

	priorEfforts := map[string]bool{}
	for _, m := range baseline {
		if m.Deprecated || m.Provider != man.Provider || !sharesTier(m, man.Tiers) {
			continue
		}
		res.Patch.Demote = append(res.Patch.Demote, Demotion{ID: m.ID, Replacement: id})
		for _, e := range m.SupportedEfforts {
			priorEfforts[e] = true
		}
	}

	if f.Efforts.Found && len(res.Patch.Demote) > 0 {
		for _, e := range f.Efforts.Value {
			if priorEfforts[e] {
				continue
			}
			if q, ok := res.screen(f.Efforts.From); ok {
				res.Capabilities = append(res.Capabilities, Capability{
					Kind: CapEffortLevel, Value: e, Quote: q, Source: f.Efforts.From.Source,
				})
			}
		}
	}
	for name, found := range map[string]bool{
		"rates":             f.RateInput.Found && f.RateOutput.Found,
		"context_window":    f.ContextWindow.Found,
		"supported_efforts": f.Efforts.Found,
		"max_output_tokens": f.MaxOutputTokens.Found,
	} {
		if !found {
			res.Capabilities = append(res.Capabilities, Capability{Kind: CapUnextracted, Value: name})
		}
	}
	sortCapabilities(res.Capabilities)

	if c := f.ThinkingCeiling; c.Found && thinkingCeilingBites(c.Value, desc.SupportedEfforts) {
		if q, ok := res.screen(c.From); ok {
			summary := fmt.Sprintf("%s rejects requests that disable thinking above effort %s (HTTP 400)", id, c.Value)
			if c.Value == models.ThinkingDisableNever {
				summary = fmt.Sprintf("%s rejects every request that disables thinking (HTTP 400)", id)
			}
			res.Defects = append(res.Defects, Defect{
				Kind: DefectThinking400, Summary: summary, Quote: q, Source: c.From.Source,
				Issue: IssueSpec{
					Title:  fmt.Sprintf("fix(models): %s thinking/effort pairing returns 400", id),
					Body:   issueBody(summary, "Clamp effort or keep thinking enabled wherever a stage disables thinking for this model.", c.From.Source, q),
					Labels: IssueLabels(KindDefect),
				},
			})
		}
	}

	target := filepath.ToSlash(filepath.Join(defaultOverlayRoot, id+".md"))
	action := actionCreate
	if opts.OverlayRoot != "" {
		if _, err := os.Stat(filepath.Join(opts.OverlayRoot, id+".md")); err == nil {
			action = actionEdit
		}
	}
	for _, ax := range propensityAxes {
		p, ok := f.Propensity[ax.Axis]
		if !ok {
			continue
		}
		q, ok := res.screen(p.From)
		if !ok {
			continue
		}
		summary := fmt.Sprintf("%s documents %s %s propensity; %s %s to adapt skill guidance on this axis",
			id, p.Value, ax.Axis, action, target)
		res.Overlays = append(res.Overlays, OverlayProposal{
			Axis: ax.Axis, Level: p.Value, Target: target, Action: action, Quote: q, Source: p.From.Source,
			Issue: IssueSpec{
				Title:  fmt.Sprintf("feat(skills): %s overlay for %s %s", id, p.Value, ax.Axis),
				Body:   issueBody(summary, "Review the quoted vendor guidance before changing the overlay.", p.From.Source, q),
				Labels: IssueLabels(KindOverlay),
			},
		})
	}
	return res, nil
}

// describe builds the proposed descriptor from typed facts only.
func describe(id string, man Manifest, f facts) models.ModelDescriptor {
	display := man.DisplayName
	if display == "" {
		display = id
	}
	d := models.ModelDescriptor{
		ID: id, Provider: man.Provider, Tiers: append([]string{}, man.Tiers...),
		DisplayName: display, ConcreteVersion: id, SupportedEfforts: []string{},
	}
	if f.RateInput.Found && f.RateOutput.Found {
		d.Rates = models.Rates{Input: f.RateInput.Value, Output: f.RateOutput.Value}
		if f.RateCacheRead.Found {
			v := f.RateCacheRead.Value
			d.Rates.CacheRead = &v
		}
		d.RateProvenance = models.RateProvenanceList
	}
	if f.Efforts.Found {
		d.SupportedEfforts = f.Efforts.Value
	}
	d.ContextWindow = f.ContextWindow.Value
	b := models.Behavior{
		ThinkingDefault:          f.ThinkingDefault.Value,
		ThinkingDisableMaxEffort: f.ThinkingCeiling.Value,
		EffortDefault:            f.EffortDefault.Value,
		MaxOutputTokens:          f.MaxOutputTokens.Value,
	}
	if len(f.Propensity) > 0 {
		b.Propensity = &models.Propensity{
			Verification: f.Propensity["verification"].Value,
			Delegation:   f.Propensity["delegation"].Value,
			Narration:    f.Propensity["narration"].Value,
		}
	}
	if b != (models.Behavior{}) {
		d.Behavior = &b
	}
	return d
}

func sharesTier(m models.ModelDescriptor, tiers []string) bool {
	for _, t := range tiers {
		if m.HasTier(t) {
			return true
		}
	}
	return false
}

// thinkingCeilingBites reports whether a thinking-disable ceiling makes some
// supported effort level unusable with thinking off — a pairing that was
// expressible before and is now a 400.
func thinkingCeilingBites(ceiling string, efforts []string) bool {
	if ceiling == models.ThinkingDisableNever {
		return true
	}
	if len(efforts) == 0 {
		return false
	}
	return rank(ceiling) < rank(efforts[len(efforts)-1])
}

func rank(level string) int {
	for i, l := range models.EffortOrder {
		if l == level {
			return i
		}
	}
	return -1
}

// screen returns the sanitized quote for s, or records s as dropped when the
// quote matches a prompt-injection pattern. Block mode is forced here
// regardless of sanitization.mode: this text is vendor input headed for
// issues that a trusted account files (ADR-002).
func (r *Result) screen(s sentence) (string, bool) {
	q := quote(s.Text)
	if d := hooks.EvaluateSanitizeText(q, config.SanitizationModeBlock); d.Decision == "block" {
		r.Dropped = append(r.Dropped, Dropped{Source: s.Source, Reason: d.Reason})
		return "", false
	}
	return q, true
}

// quote strips backticks, collapses whitespace and truncates to MaxQuoteLen.
func quote(s string) string {
	s = strings.Join(strings.Fields(strings.ReplaceAll(s, "`", "")), " ")
	if r := []rune(s); len(r) > MaxQuoteLen {
		s = string(r[:MaxQuoteLen-1]) + "…"
	}
	return s
}

func issueBody(summary, action, source, q string) string {
	return fmt.Sprintf("%s\n\n%s\n\nSource: %s\n\n```text\n%s\n```\n\nFound by nightgauge-model-watch. Triage before dispatch.\n",
		summary, action, source, q)
}

func sortCapabilities(cs []Capability) {
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].Kind != cs[j].Kind {
			return cs[i].Kind < cs[j].Kind
		}
		return cs[i].Value < cs[j].Value
	})
}

package routing

import (
	"fmt"
	"sort"
	"strings"
)

// Issue-label model overrides (#8). Two label forms, and only these:
//
//	model:pin-<band>   the stage runs on <band> instead of the pipeline's choice
//	model:max-<band>   the stage never runs above <band>, escalation included
//
// <band> is the registry band vocabulary (haiku, sonnet, opus, fable). The
// TypeScript mirror is parseModelLabels / applyModelLabelPin /
// applyModelLabelCap in packages/nightgauge/src/utils/skillRunner.ts; both
// suites drive testdata/model-label-overrides-crosslang.json, so a divergence
// fails both.
//
// A label is a PIPELINE input, never operator config: an explicit per-stage
// model ignores it, the model_routing.minimum_model floor and the performance
// mode's ceiling still bind it, and an excluded band steps down like every
// other automatic choice (#2063). See docs/CONFIGURATION.md § Issue-label model
// overrides for the precedence both resolvers apply.
const (
	modelLabelPrefix    = "model:"
	modelLabelPinPrefix = "model:pin-"
	modelLabelMaxPrefix = "model:max-"
	// ModelOverrideSourceLabel is the outcome-record source for a run whose
	// labels forced or capped its model (state.OutcomePrediction).
	ModelOverrideSourceLabel = "label"
)

// ModelLabelOverride is the parsed model:* label set of one issue. The zero
// value applies nothing.
type ModelLabelOverride struct {
	// Pin is the band that replaces a pipeline-chosen base, or "".
	Pin string
	// Max is the band no pipeline-chosen model may exceed, or "".
	Max string
	// Label is the comma-joined, sorted list of labels that took effect, for
	// outcome attribution. "" when nothing took effect.
	Label string
}

// Source is ModelOverrideSourceLabel when the override changes anything, "".
func (o ModelLabelOverride) Source() string {
	if o.Pin == "" && o.Max == "" {
		return ""
	}
	return ModelOverrideSourceLabel
}

// ParseModelLabels reads the model:* labels from an issue's labels. Matching
// is case-insensitive after trimming. Rules, in order:
//
//   - any other model:* label (unknown band, model:<band> shorthand) is
//     ignored with a warning;
//   - two or more DISTINCT pins are all ignored with a warning;
//   - two or more distinct caps resolve to the LOWEST (the conservative read);
//   - a pin above the cap is lowered to the cap with a warning.
//
// Warnings are returned, not logged: the caller logs them once per run.
func ParseModelLabels(labels []string) (ModelLabelOverride, []string) {
	var warnings []string
	pins := map[string]string{}
	caps := map[string]string{}
	for _, raw := range labels {
		label := strings.ToLower(strings.TrimSpace(raw))
		if !strings.HasPrefix(label, modelLabelPrefix) {
			continue
		}
		switch {
		case strings.HasPrefix(label, modelLabelPinPrefix) && IsTierBand(strings.TrimPrefix(label, modelLabelPinPrefix)):
			pins[strings.TrimPrefix(label, modelLabelPinPrefix)] = label
		case strings.HasPrefix(label, modelLabelMaxPrefix) && IsTierBand(strings.TrimPrefix(label, modelLabelMaxPrefix)):
			caps[strings.TrimPrefix(label, modelLabelMaxPrefix)] = label
		default:
			warnings = append(warnings, fmt.Sprintf(
				"ignoring label %q: model labels are model:pin-<band> or model:max-<band> with band one of %s",
				label, strings.Join(TierBandsAscending(), ", ")))
		}
	}

	var o ModelLabelOverride
	applied := []string{}
	if len(caps) > 0 {
		for band := range caps {
			if o.Max == "" || tierRank(band) < tierRank(o.Max) {
				o.Max = band
			}
		}
		applied = append(applied, caps[o.Max])
	}
	switch {
	case len(pins) > 1:
		warnings = append(warnings, fmt.Sprintf(
			"ignoring conflicting model pins %s: an issue carries at most one model:pin-<band>",
			strings.Join(sortedValues(pins), ", ")))
	case len(pins) == 1:
		for band, label := range pins {
			o.Pin = band
			applied = append(applied, label)
		}
		if o.Max != "" && tierRank(o.Pin) > tierRank(o.Max) {
			warnings = append(warnings, fmt.Sprintf(
				"label %s exceeds %s: the cap wins, pinning %s",
				pins[o.Pin], caps[o.Max], o.Max))
			o.Pin = o.Max
		}
	}
	sort.Strings(applied)
	o.Label = strings.Join(applied, ",")
	return o, warnings
}

// ApplyModelLabelPin returns the pin when one is set, otherwise base. Callers
// apply it only to a base the PIPELINE chose, never an explicit stage model.
func (o ModelLabelOverride) ApplyModelLabelPin(base string) string {
	if o.Pin != "" {
		return o.Pin
	}
	return base
}

// ApplyModelLabelCap lowers model to the cap when it ranks above it. It never
// raises, and a model the registry has no band for passes through untouched.
func (o ModelLabelOverride) ApplyModelLabelCap(model string) string {
	if o.Max == "" {
		return model
	}
	if rank := TierRank(model); rank >= 0 && rank > tierRank(o.Max) {
		return o.Max
	}
	return model
}

func sortedValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

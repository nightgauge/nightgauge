package adapters

import (
	"fmt"
	"os"
	"strings"

	"github.com/nightgauge/nightgauge/internal/models"
)

// Grok CLI extra effort rungs that are not Nightgauge EFFORT_LEVELS.
// They collapse to "low" for registry / thinking-interlock purposes (#523).
func mapGrokEffortToNightgauge(effort string) string {
	e := strings.ToLower(strings.TrimSpace(effort))
	switch e {
	case "":
		return ""
	case "none", "minimal":
		return "low"
	case "low", "medium", "high", "xhigh", "max":
		return e
	default:
		return ""
	}
}

// grokCliEffortFlag is the value to pass as `grok --effort`. SYNTAX validation
// only — membership in the Grok CLI's documented vocabulary. Whether the
// resolved model actually serves a rung is the registry's call, enforced by
// validateGrokEffort before spawn (#569); this list must never grow back into
// a second authority on that question.
func grokCliEffortFlag(effort string) string {
	e := strings.ToLower(strings.TrimSpace(effort))
	switch e {
	case "none", "minimal", "low", "medium", "high", "xhigh", "max":
		return e
	default:
		return ""
	}
}

// validateGrokEffort enforces the registry's supported_efforts for the model a
// Grok dispatch will actually serve (#569). The provider-global
// NIGHTGAUGE_GROK_EFFORT used to pass the static vocabulary filter above and
// reach `grok --effort` unchecked — a rung the resolved model does not declare
// (e.g. `xhigh` against grok-4.5, which tops out at `high`) died as #532's
// signature: exit 1 in seconds, no work, nothing classified. The registry knew
// and was never asked.
//
// Rules, in order:
//   - No explicit effort → nothing to enforce.
//   - A value outside the Grok CLI vocabulary is dropped by BuildCommand's
//     syntax filter exactly as before, but the drop is logged, never silent.
//   - The model is resolved the same way BuildCommand resolves it (band → the
//     registry's xai model, concrete id → itself). A model with NO registry
//     descriptor passes through with a logged warning, never a hard failure
//     (#336) — there is nothing to validate against.
//   - Enforcement runs AFTER normalization (#523): grok-native none/minimal
//     collapse to "low" before the rung is compared to the ladder.
//   - `supported_efforts: []` is a positive declaration — no effort axis — so
//     ANY explicit effort is rejected (#336).
//   - A normalized rung missing from the declared ladder fails closed with an
//     error naming the model, the requested effort, and the ladder — never a
//     silent pass-through, never a silent downgrade (#75).
func validateGrokEffort(model, effort string) error {
	e := strings.ToLower(strings.TrimSpace(effort))
	if e == "" {
		return nil
	}
	if grokCliEffortFlag(e) == "" {
		fmt.Fprintf(os.Stderr,
			"[grok] warning: NIGHTGAUGE_GROK_EFFORT=%q is not a Grok CLI effort (none|minimal|low|medium|high|xhigh|max) — the --effort flag will be omitted and the provider default used\n",
			effort)
		return nil
	}
	resolved, ok := models.Resolve(models.ProviderForAdapter("grok"), strings.TrimSpace(model))
	if !ok {
		fmt.Fprintf(os.Stderr,
			"[grok] warning: model %q has no registry descriptor — cannot verify effort %q against supported_efforts; passing through\n",
			model, effort)
		return nil
	}
	if len(resolved.SupportedEfforts) == 0 {
		return fmt.Errorf(
			"effort %q is not supported by model %q: the model declares no effort axis (supported_efforts: []); unset NIGHTGAUGE_GROK_EFFORT or route to a model with an effort ladder",
			e, resolved.ID,
		)
	}
	normalized := mapGrokEffortToNightgauge(e)
	for _, s := range resolved.SupportedEfforts {
		if s == normalized {
			return nil
		}
	}
	note := ""
	if normalized != e {
		note = fmt.Sprintf(" (normalized to %q)", normalized)
	}
	return fmt.Errorf(
		"effort %q%s is not supported by model %q (supports: %s); choose a supported level or route to a model that accepts %q",
		e, note, resolved.ID, strings.Join(resolved.SupportedEfforts, ", "), e,
	)
}

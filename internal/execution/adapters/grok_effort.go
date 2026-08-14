package adapters

import "strings"

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

// grokCliEffortFlag is the value to pass as `grok --effort`.
func grokCliEffortFlag(effort string) string {
	e := strings.ToLower(strings.TrimSpace(effort))
	switch e {
	case "none", "minimal", "low", "medium", "high", "xhigh", "max":
		return e
	default:
		return ""
	}
}

package focus

import (
	"sort"
	"strings"
)

// Proposal is the input/output type for focus ranking.
// It mirrors the JSON schema from continuous-improvement Phase 4.
type Proposal struct {
	ID          string `json:"id"`
	Category    string `json:"category"`
	Priority    string `json:"priority"`
	Loop        string `json:"loop"`
	Title       string `json:"title"`
	Description string `json:"description"`
	// Set by Rank():
	FocusAligned         bool     `json:"focus_aligned"`
	FocusKeywordsMatched []string `json:"focus_keywords_matched"`
	// Pass-through fields:
	Evidence        interface{} `json:"evidence,omitempty"`
	SuggestedAction string      `json:"suggestedAction,omitempty"`
	Mode            string      `json:"mode,omitempty"`
	EstimatedImpact string      `json:"estimatedImpact,omitempty"`
	// LoopVerdict carries the verdict string from the loop-verdicts report
	// (e.g. "degrading", "stalling") for priority adjustment logic.
	LoopVerdict string `json:"loopVerdict,omitempty"`
}

// RankResult is the output of Rank().
type RankResult struct {
	V         int        `json:"v"`
	Lens      string     `json:"lens"`
	Proposals []Proposal `json:"proposals"`
}

// Rank applies focus lens alignment and priority weighting to proposals.
// When lens is nil or lens.Name == "general", no priority adjustments are made
// but alignment is still computed (always false for general).
// The returned RankResult.Proposals is sorted: focus-aligned first, then by
// priority tier (critical > high > medium > low).
func Rank(proposals []Proposal, lens *Lens) RankResult {
	lensName := "general"
	var keywords []string
	if lens != nil {
		lensName = lens.Name
		keywords = lens.Keywords
	}

	result := make([]Proposal, len(proposals))
	for i, p := range proposals {
		p.FocusAligned = false
		p.FocusKeywordsMatched = []string{}

		if lensName != "general" && len(keywords) > 0 {
			text := strings.ToLower(p.Title + " " + p.Description)
			for _, kw := range keywords {
				if strings.Contains(text, strings.ToLower(kw)) {
					p.FocusAligned = true
					p.FocusKeywordsMatched = append(p.FocusKeywordsMatched, kw)
				}
			}
		}

		// Apply priority adjustment
		if lensName != "general" {
			p.Priority = adjustPriority(p, p.FocusAligned)
		}

		result[i] = p
	}

	// Sort: aligned first, then by priority tier descending
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].FocusAligned != result[j].FocusAligned {
			return result[i].FocusAligned
		}
		return priorityTier(result[i].Priority) > priorityTier(result[j].Priority)
	})

	return RankResult{
		V:         1,
		Lens:      lensName,
		Proposals: result,
	}
}

// adjustPriority applies the focus-aware priority rules from SKILL.md Phase 4.
//
// Hard constraints (applied last):
//   - degrading loop proposals never go below "high"
//   - reliability/security category proposals always retain at least "high"
func adjustPriority(p Proposal, aligned bool) string {
	prio := p.Priority
	verdict := strings.ToLower(p.LoopVerdict)

	if aligned {
		switch verdict {
		case "degrading":
			prio = bumpUp(prio)
		case "stalling":
			// keep as-is
		}
	} else {
		switch verdict {
		case "stalling":
			prio = bumpDown(prio)
		case "degrading":
			// keep as-is (don't suppress critical findings)
		}
	}

	// Hard constraints
	if verdict == "degrading" && priorityTier(prio) < priorityTier("high") {
		prio = "high"
	}
	if (p.Category == "reliability" || p.Category == "security") && priorityTier(prio) < priorityTier("high") {
		prio = "high"
	}

	return prio
}

func bumpUp(prio string) string {
	switch prio {
	case "low":
		return "medium"
	case "medium":
		return "high"
	default:
		return prio // critical and high stay
	}
}

func bumpDown(prio string) string {
	switch prio {
	case "high":
		return "medium"
	case "medium":
		return "low"
	default:
		return prio // critical and low stay
	}
}

func priorityTier(prio string) int {
	switch prio {
	case "critical":
		return 4
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	default:
		return 0
	}
}

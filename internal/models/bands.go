package models

import "strings"

// Band NAME spellings — the one place the Go side quotes the registry band
// vocabulary (#582). Everything else derives: the routing layer's Tier*
// consts alias these, adapter error prose interpolates BandAlternation, and
// the claude-id prefix classifier below replaces three per-adapter copies of
// the same switch. The band ORDER authority stays where #581 put it —
// routing.TierBandsStrongestFirst (strongest-first, from these consts) ⇄ the
// TS TIER_BANDS declaration, pinned by the cross-language ladder tests.
//
// This file lives in internal/models (not routing) because the execution
// adapters need the vocabulary and models is the lowest shared layer: routing
// imports models, never the reverse.
const (
	BandHaiku  = "haiku"
	BandSonnet = "sonnet"
	BandOpus   = "opus"
	BandFable  = "fable"
)

// BandsAscending is the band vocabulary weakest → strongest, matching the TS
// TIER_BANDS order. Membership/spelling only — order-sensitive walks go
// through routing.TierBandsStrongestFirst.
var BandsAscending = []string{BandHaiku, BandSonnet, BandOpus, BandFable}

// BandAlternation renders the vocabulary as a "haiku|sonnet|opus|fable"
// alternation for error prose and diagnostics — derived, never re-typed: a
// hand-written three-band alternation silently dropped fable twice (#582).
func BandAlternation() string {
	return strings.Join(BandsAscending, "|")
}

// ClaudeIDTier classifies a Claude concrete id onto the tier band used for
// cross-provider resolution, by PREFIX (not registry-exact) so future dated
// ids like "claude-sonnet-9" still land on the matching band. `fable`
// collapses onto the opus band — every provider's fable-band model is also
// its opus-band model, and each pre-#582 adapter copy of this switch already
// collapsed. Non-Claude inputs return ("", false); callers pass their input
// through unchanged.
func ClaudeIDTier(model string) (string, bool) {
	switch {
	case strings.HasPrefix(model, "claude-haiku"):
		return BandHaiku, true
	case strings.HasPrefix(model, "claude-sonnet"):
		return BandSonnet, true
	case strings.HasPrefix(model, "claude-opus"), strings.HasPrefix(model, "claude-fable"):
		return BandOpus, true
	}
	return "", false
}

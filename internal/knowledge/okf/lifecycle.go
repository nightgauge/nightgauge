package okf

import (
	"strings"
	"time"
)

// Trust tiers rank an entry by who has confirmed it, derived from the
// `verified` log rather than declared. An entry cannot assert its own tier.
const (
	// TrustHumanReviewed means a person recorded a verification event. This is
	// the strongest signal the base carries: someone read the entry and said
	// so.
	TrustHumanReviewed = "human-reviewed"
	// TrustMachineConfirmed means a deterministic writer or an agent stage
	// recorded one — retro after a merge, or graduation into docs/.
	TrustMachineConfirmed = "machine-confirmed"
	// TrustUnverified means nothing has confirmed the entry. A model's first
	// draft starts here and stays here until something reviews it.
	TrustUnverified = "unverified"
)

// TrustTier derives the entry's tier from its verification log. The highest
// tier present wins: one human event outranks any number of machine ones.
//
// Actor matching is on the `human:` PREFIX, not a substring: an actor like
// `feature-dev/human-review-model` is a stage, not a person, and a substring
// match would silently promote it to the top tier.
func (b *FrontmatterBlock) TrustTier() string {
	if b == nil || len(b.Verified) == 0 {
		return TrustUnverified
	}
	for _, v := range b.Verified {
		if strings.HasPrefix(v.By, "human:") && len(v.By) > len("human:") {
			return TrustHumanReviewed
		}
	}
	return TrustMachineConfirmed
}

// IsExpiredAt reports whether the entry's stale_after has passed.
//
// An absent or unparseable stale_after is NOT expired. Failing open matches
// the consumer-tolerance rule the rest of the contract follows: a producer we
// do not understand should not have its entries quietly demoted.
func (b *FrontmatterBlock) IsExpiredAt(now time.Time) bool {
	if b == nil || b.StaleAfter == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, b.StaleAfter)
	if err != nil {
		return false
	}
	return now.After(t)
}

// IsExpiredStamp is the same check against a raw stale_after value, for
// callers that carry the stamp rather than the whole block — the recall cache
// stores the string precisely so expiry is evaluated at query time.
//
// A bool frozen at index time would never flip: an entry expiring is a clock
// event with no file change, so a cached flag stays false forever for every
// entry indexed before its stale_after.
func IsExpiredStamp(staleAfter string, now time.Time) bool {
	if staleAfter == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, staleAfter)
	if err != nil {
		return false
	}
	return now.After(t)
}

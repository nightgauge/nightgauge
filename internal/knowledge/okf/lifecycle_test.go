package okf_test

import (
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/knowledge/okf"
)

func TestTrustTier(t *testing.T) {
	cases := []struct {
		name     string
		verified []okf.Provenance
		want     string
	}{
		{"no verified log", nil, okf.TrustUnverified},
		{"empty verified log", []okf.Provenance{}, okf.TrustUnverified},
		{"a deterministic writer", []okf.Provenance{{By: "process:retro"}}, okf.TrustMachineConfirmed},
		{"an agent stage", []okf.Provenance{{By: "feature-dev/claude-sonnet-5"}}, okf.TrustMachineConfirmed},
		{"a person", []okf.Provenance{{By: "human:octocat"}}, okf.TrustHumanReviewed},
		{
			"the highest tier wins",
			[]okf.Provenance{{By: "process:retro"}, {By: "human:octocat"}},
			okf.TrustHumanReviewed,
		},
		{
			// The discriminating case. A substring match on "human" would
			// promote this stage to the top tier, which is exactly the
			// silent-wrong-answer the derivation exists to avoid.
			"a stage whose model name contains 'human'",
			[]okf.Provenance{{By: "feature-dev/human-review-model"}},
			okf.TrustMachineConfirmed,
		},
		{
			"a bare human: prefix with no id is not a person",
			[]okf.Provenance{{By: "human:"}},
			okf.TrustMachineConfirmed,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := &okf.FrontmatterBlock{Verified: tc.verified}
			if got := b.TrustTier(); got != tc.want {
				t.Errorf("TrustTier() = %q, want %q", got, tc.want)
			}
		})
	}

	// Nil-receiver safe: an entry with no frontmatter is unverified, not a panic.
	var nilBlock *okf.FrontmatterBlock
	if got := nilBlock.TrustTier(); got != okf.TrustUnverified {
		t.Errorf("nil block TrustTier() = %q", got)
	}
}

func TestIsExpiredAt(t *testing.T) {
	now := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		name       string
		staleAfter string
		want       bool
	}{
		{"absent", "", false},
		{"in the future", "2027-01-01T00:00:00Z", false},
		{"in the past", "2020-01-01T00:00:00Z", true},
		// Fail open: a producer whose format we do not understand should not
		// have its entries quietly demoted, matching the tolerance rule the
		// rest of the contract follows.
		{"unparseable", "next tuesday", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := &okf.FrontmatterBlock{StaleAfter: tc.staleAfter}
			if got := b.IsExpiredAt(now); got != tc.want {
				t.Errorf("IsExpiredAt(%q) = %v, want %v", tc.staleAfter, got, tc.want)
			}
			if got := okf.IsExpiredStamp(tc.staleAfter, now); got != tc.want {
				t.Errorf("IsExpiredStamp(%q) = %v, want %v", tc.staleAfter, got, tc.want)
			}
		})
	}

	var nilBlock *okf.FrontmatterBlock
	if nilBlock.IsExpiredAt(now) {
		t.Error("nil block reported as expired")
	}
}

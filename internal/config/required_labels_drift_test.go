package config

import (
	"testing"

	gh "github.com/nightgauge/nightgauge/internal/github"
)

// TestRequiredLabels_ArchitectureApprovalMatchesConfig pins the one value that
// internal/github must copy rather than import.
//
// internal/config reaches internal/github transitively
// (config → intelligence/routing → platform → state → github), so the required
// label registry cannot import DefaultArchitectureApprovalLabel — it restates
// it. This test lives HERE, in the package that can see both, and is the reason
// that copy is safe. If someone renames the approval label in config, the
// registry stops provisioning it, the gate silently becomes unsatisfiable by
// label, and nothing else in the tree would notice.
func TestRequiredLabels_ArchitectureApprovalMatchesConfig(t *testing.T) {
	if gh.LabelArchitectureApproved != DefaultArchitectureApprovalLabel {
		t.Fatalf("github.LabelArchitectureApproved = %q but config.DefaultArchitectureApprovalLabel = %q — "+
			"the label registry would provision a label the approval gate does not read",
			gh.LabelArchitectureApproved, DefaultArchitectureApprovalLabel)
	}

	found := false
	for _, name := range gh.RequiredLabelNames() {
		if name == DefaultArchitectureApprovalLabel {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("%q is read as an approval gate but is not in the required-label registry — "+
			"a repo without it has a gate that cannot be satisfied by its documented mechanism",
			DefaultArchitectureApprovalLabel)
	}
}

// TestRequiredLabels_CoverDefaultExcludeLabels guards the dispatch-exclusion
// direction. An exclusion label that does not exist in a repo cannot be applied
// to anything, so the exclusion is inert and human-only work gets dispatched.
func TestRequiredLabels_CoverDefaultExcludeLabels(t *testing.T) {
	required := make(map[string]bool)
	for _, n := range gh.RequiredLabelNames() {
		required[n] = true
	}
	for _, name := range DefaultExcludeLabels {
		if !required[name] {
			t.Errorf("%q is a default dispatch-exclusion label but is not in the required-label "+
				"registry — nothing provisions it, so the exclusion cannot fire", name)
		}
	}
}

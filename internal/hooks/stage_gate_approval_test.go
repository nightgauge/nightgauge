package hooks

import (
	"strings"
	"testing"
)

// No pipeline stage may apply the approval label, by any forge client
// (#2130): approving is a maintainer's own act. Every stage is fenced,
// pr-merge and feature-dev included, because the label can be applied from
// anywhere a model runs a command.
func TestStageGate_BlocksApplyingTheApprovalLabelInEveryStage(t *testing.T) {
	stages := []string{"issue-pickup", "feature-planning", "feature-dev", "feature-validate", "pr-create", "pr-merge", "issue-refine"}
	blocked := []string{
		"gh issue edit 5 --add-label pipeline:approved",
		"gh issue edit 5 --add-label 'type:bug,Pipeline:Approved'",
		"gh api -X POST repos/o/r/issues/5/labels -f 'labels[]=pipeline:approved'",
		`gh api graphql -f query='mutation { addLabelsToLabelable(input:{labelableId:"I_1", labelIds:["LA_x"]}) { clientMutationId } }' # pipeline:approved`,
		"nightgauge forge label add --issue-id I_1 --labels pipeline:approved",
		"glab issue update 5 --label pipeline:approved",
		`curl -X POST -H "Authorization: token $T" https://api.github.com/repos/o/r/issues/5/labels -d '{"labels":["pipeline:approved"]}'`,
		`L=pipeline:approved; gh issue edit 5 --add-label "$L"`,
		`bash -c "gh issue edit 5 --add-label pipeline:approved"`,
	}
	for _, stage := range stages {
		for _, cmd := range blocked {
			got := evaluateStageGate(stageBash(cmd), stage)
			if got.Decision != "block" {
				t.Errorf("[%s] expected block for %q, got %q", stage, cmd, got.Decision)
				continue
			}
			if !strings.Contains(got.Reason, "pipeline:approved") {
				t.Errorf("[%s] reason %q does not name the label", stage, got.Reason)
			}
		}
	}
}

// Prose and unrelated commands are not fenced, and outside a pipeline stage
// nothing is.
func TestStageGate_ApprovalLabelFenceIsScoped(t *testing.T) {
	allowed := []string{
		"gh issue edit 5 --add-label type:bug",
		`echo "a maintainer applies pipeline:approved to approve it"`,
		"grep -rn pipeline:approved docs/",
	}
	for _, cmd := range allowed {
		if got := evaluateStageGate(stageBash(cmd), "feature-dev"); got.Decision == "block" {
			t.Errorf("blocked %q: %s", cmd, got.Reason)
		}
	}
	if got := evaluateStageGate(stageBash("gh issue edit 5 --add-label pipeline:approved"), ""); got.Decision == "block" {
		t.Error("the fence fired outside a pipeline stage")
	}
}

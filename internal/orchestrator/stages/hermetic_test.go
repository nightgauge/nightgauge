package stages

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/nightgauge/nightgauge/internal/preflight"
)

// TestMain keeps pr-create's issue-body read (#3228) off the network for the
// whole test binary. The default answer is "the issue body has not changed
// since ac-reconcile ran": a body rebuilt from the report's own criteria. A
// test of a changed or unreadable body installs its own reader.
func TestMain(m *testing.M) {
	prCreateIssueBody = func(workdir string) IssueBodyReader {
		return func(_ context.Context, issueNumber int) (string, error) {
			data, err := os.ReadFile(pipelineContextPath(workdir, fmt.Sprintf("ac-reconcile-%d.json", issueNumber)))
			if err != nil {
				return "", err
			}
			var rec preflight.ACReconcileResult
			if err := json.Unmarshal(data, &rec); err != nil {
				return "", err
			}
			texts := make([]string, 0, len(rec.AcceptanceCriteria))
			for _, c := range rec.AcceptanceCriteria {
				texts = append(texts, c.Text)
			}
			return acCheckboxBody(texts), nil
		}
	}
	os.Exit(m.Run())
}

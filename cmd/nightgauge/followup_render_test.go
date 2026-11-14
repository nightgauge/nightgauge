package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/cmd/followup"
)

// #3278: the run summary names the path each follow-up took.
func TestRenderFollowupHuman_NamesFoldedAndFiled(t *testing.T) {
	var buf bytes.Buffer
	renderFollowupHuman(&buf, &followup.Result{Issue: 42, Items: []followup.Item{
		{ID: "dw-1", Title: "a", Outcome: followup.OutcomeFolded, IssueNumber: 60, Detail: "appended as Part B of #60"},
		{ID: "dw-2", Title: "b", Outcome: followup.OutcomeFiled, IssueNumber: 61},
	}})
	out := buf.String()
	for _, want := range []string{"folded into #60 (appended as Part B of #60) a", "filed #61 b"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary missing %q:\n%s", want, out)
		}
	}
}

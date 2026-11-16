package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/diagnostics"
)

// #3081: exit-records shows a stop on an already-closed issue as a skip, and
// a real failure as FAIL.
func TestPrintExitRecordsHuman_IssueClosedIsASkip(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	printExitRecordsHuman([]diagnostics.StageExitRecord{
		{Repo: "o/a", Issue: 1, Stage: diagnostics.StagePreDispatch, TerminalKind: "issue_closed"},
		{Repo: "o/a", Issue: 2, Stage: "feature-dev", TerminalKind: "subagent_crash"},
	})
	w.Close()
	os.Stdout = stdout
	out, _ := io.ReadAll(r)
	text := string(out)
	if !strings.Contains(text, "skip/issue_closed") || strings.Contains(text, "FAIL/issue_closed") {
		t.Fatalf("issue_closed is not shown as a skip:\n%s", text)
	}
	if !strings.Contains(text, "FAIL/subagent_crash") {
		t.Fatalf("a real failure is not shown as FAIL:\n%s", text)
	}
}

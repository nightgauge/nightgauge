package github

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Recorded `GET /repos/{o}/{r}/actions/jobs/{id}` payloads (#3198), trimmed
// to the fields the classifier reads.

// playwrightSetupFailure: #3191's merge-group job 130571120205 of run
// 43034809958. Build passed, then the OS-dependency install failed (an apt
// mirror returned DNS SERVFAIL) and the test step was skipped.
const playwrightSetupFailure = `{
  "conclusion": "failure",
  "id": 130571120205,
  "name": "Playwright webview tests",
  "run_id": 43034809958,
  "status": "completed",
  "steps": [
    {
      "conclusion": "success",
      "name": "Set up job",
      "number": 1,
      "status": "completed"
    },
    {
      "conclusion": "success",
      "name": "Run actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1",
      "number": 2,
      "status": "completed"
    },
    {
      "conclusion": "success",
      "name": "Run actions/setup-node@949feb2413d6458794dcd2491c4babbbce0c15c1",
      "number": 3,
      "status": "completed"
    },
    {
      "conclusion": "success",
      "name": "Install",
      "number": 4,
      "status": "completed"
    },
    {
      "conclusion": "success",
      "name": "Build",
      "number": 5,
      "status": "completed"
    },
    {
      "conclusion": "success",
      "name": "Get Playwright version",
      "number": 6,
      "status": "completed"
    },
    {
      "conclusion": "success",
      "name": "Cache Playwright browser binary",
      "number": 7,
      "status": "completed"
    },
    {
      "conclusion": "skipped",
      "name": "Install Playwright browser (chromium, cache miss)",
      "number": 8,
      "status": "completed"
    },
    {
      "conclusion": "failure",
      "name": "Install Playwright OS dependencies (cache hit)",
      "number": 9,
      "status": "completed"
    },
    {
      "conclusion": "skipped",
      "name": "Test (Playwright)",
      "number": 10,
      "status": "completed"
    },
    {
      "conclusion": "success",
      "name": "Upload dashboard fixture screenshots",
      "number": 11,
      "status": "completed"
    },
    {
      "conclusion": "success",
      "name": "Upload Playwright failure artifacts (screenshots, traces, video)",
      "number": 12,
      "status": "completed"
    },
    {
      "conclusion": "skipped",
      "name": "Post Cache Playwright browser binary",
      "number": 22,
      "status": "completed"
    },
    {
      "conclusion": "skipped",
      "name": "Post Run actions/setup-node@949feb2413d6458794dcd2491c4babbbce0c15c1",
      "number": 23,
      "status": "completed"
    },
    {
      "conclusion": "success",
      "name": "Post Run actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1",
      "number": 24,
      "status": "completed"
    },
    {
      "conclusion": "success",
      "name": "Complete job",
      "number": 25,
      "status": "completed"
    }
  ]
}`

// goTestFailure: #3193's Go job 130605138765 of run 43044474409, which
// failed in its own test step.
const goTestFailure = `{
  "conclusion": "failure",
  "id": 130605138765,
  "name": "Go build & test",
  "run_id": 43044474409,
  "status": "completed",
  "steps": [
    {
      "conclusion": "success",
      "name": "Set up job",
      "number": 1,
      "status": "completed"
    },
    {
      "conclusion": "success",
      "name": "Run actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1",
      "number": 2,
      "status": "completed"
    },
    {
      "conclusion": "success",
      "name": "Configure git identity (tests create local repos)",
      "number": 3,
      "status": "completed"
    },
    {
      "conclusion": "success",
      "name": "Run actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e",
      "number": 4,
      "status": "completed"
    },
    {
      "conclusion": "success",
      "name": "CI change-class gate regression suite",
      "number": 5,
      "status": "completed"
    },
    {
      "conclusion": "success",
      "name": "Working-tree content guards (encoding, skill includes, portability)",
      "number": 6,
      "status": "completed"
    },
    {
      "conclusion": "success",
      "name": "Platform operation registry in sync",
      "number": 7,
      "status": "completed"
    },
    {
      "conclusion": "success",
      "name": "Build",
      "number": 8,
      "status": "completed"
    },
    {
      "conclusion": "success",
      "name": "Vet",
      "number": 9,
      "status": "completed"
    },
    {
      "conclusion": "success",
      "name": "Gofmt",
      "number": 10,
      "status": "completed"
    },
    {
      "conclusion": "failure",
      "name": "Test (plain and race, concurrently)",
      "number": 11,
      "status": "completed"
    },
    {
      "conclusion": "success",
      "name": "OpenCode integration (opencode-ai@1.18.30)",
      "number": 12,
      "status": "completed"
    },
    {
      "conclusion": "success",
      "name": "Upload Go test logs",
      "number": 13,
      "status": "completed"
    },
    {
      "conclusion": "success",
      "name": "branch-merged-check.sh regression suite",
      "number": 14,
      "status": "completed"
    },
    {
      "conclusion": "success",
      "name": "Verify required steps did not silently skip",
      "number": 15,
      "status": "completed"
    },
    {
      "conclusion": "skipped",
      "name": "Post Run actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e",
      "number": 29,
      "status": "completed"
    },
    {
      "conclusion": "success",
      "name": "Post Run actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1",
      "number": 30,
      "status": "completed"
    },
    {
      "conclusion": "success",
      "name": "Complete job",
      "number": 31,
      "status": "completed"
    }
  ]
}`

func TestDecodeWorkflowRunJob_RecordedPayloads(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantFailed string
		wantSteps  int
	}{
		{"#3191 playwright setup", playwrightSetupFailure, "Install Playwright OS dependencies (cache hit)", 16},
		{"#3193 go test", goTestFailure, "Test (plain and race, concurrently)", 18},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			job, err := DecodeWorkflowRunJob([]byte(tc.body))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if len(job.Steps) != tc.wantSteps {
				t.Fatalf("steps = %d, want %d", len(job.Steps), tc.wantSteps)
			}
			var failed string
			for _, s := range job.Steps {
				if s.Conclusion == "failure" {
					failed = s.Name
					break
				}
			}
			if failed != tc.wantFailed {
				t.Errorf("failed step = %q, want %q", failed, tc.wantFailed)
			}
		})
	}
}

func TestDecodeWorkflowRunJob_RejectsNonJob(t *testing.T) {
	if _, err := DecodeWorkflowRunJob([]byte(`{"message":"Not Found"}`)); err == nil {
		t.Fatal("a body with no job id decoded as a job")
	}
	if _, err := DecodeWorkflowRunJob([]byte(`not json`)); err == nil {
		t.Fatal("invalid JSON decoded as a job")
	}
}

// recordedGoJob is #3193's Go job 130605138765 (run 43044474409): its steps
// and its plain-text log, trimmed to each step's header and first lines, the
// end of the failed test step, and the always() steps and post-job cleanup
// that ran after it.
func recordedGoJob(t *testing.T) (WorkflowRunJob, string) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "job-130605138765.json"))
	if err != nil {
		t.Fatal(err)
	}
	job, err := DecodeWorkflowRunJob(body)
	if err != nil {
		t.Fatal(err)
	}
	log, err := os.ReadFile(filepath.Join("testdata", "job-130605138765.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return job, string(log)
}

func TestFailedStepExcerpt_RecordedJobIsTheFailedStepOnly(t *testing.T) {
	job, log := recordedGoJob(t)
	const failed = 11 // "Test (plain and race, concurrently)"
	got := FailedStepExcerpt(log, job.Steps, failed)
	if !strings.Contains(got, "##[error]skip accounting failed (exit 1)") ||
		!strings.Contains(got, "##[error]Process completed with exit code 1.") {
		t.Fatalf("excerpt lacks the failed step's error lines:\n%s", got)
	}
	if !strings.HasSuffix(got, "##[error]Process completed with exit code 1.") {
		t.Errorf("excerpt runs past the failed step's end:\n%s", got)
	}
	for _, later := range []string{"opencode-integration", "branch-merged-check", "Cleaning up orphan processes", "##[group]Run {"} {
		if strings.Contains(got, later) {
			t.Errorf("excerpt carries %q from a step after the failed one:\n%s", later, got)
		}
	}
	if strings.Contains(got, "2026-11-11T") || strings.Contains(got, "\x1b[") {
		t.Errorf("timestamps or ANSI codes not stripped:\n%s", got)
	}

	// Another step by number: Vet (9) is its own section.
	if vet := FailedStepExcerpt(log, job.Steps, 9); !strings.HasPrefix(vet, "##[group]Run go vet ./...") || strings.Contains(vet, "gofmt") {
		t.Errorf("step 9 excerpt = %q, want the Vet step's section", vet)
	}
}

func TestFailedStepExcerpt_EmptyRatherThanWrong(t *testing.T) {
	job, log := recordedGoJob(t)
	if got := FailedStepExcerpt(log, job.Steps, 99); got != "" {
		t.Errorf("an absent step gave %q", got)
	}
	if got := FailedStepExcerpt(log, job.Steps, 31); got != "" { // "Complete job": no header
		t.Errorf("a step with no log section gave %q", got)
	}
	// A composite action's own header shifts the count: no excerpt.
	extra := strings.Replace(log, "##[group]Run go vet ./...", "##[group]Run go vet ./...\n2026-11-11T15:04:50.6000000Z ##[group]Run nested", 1)
	if got := FailedStepExcerpt(extra, job.Steps, 11); got != "" {
		t.Errorf("a header count mismatch gave %q", got)
	}
	// A header outside the step's time window: no excerpt.
	steps := append([]WorkflowRunStep(nil), job.Steps...)
	for i := range steps {
		if steps[i].Number == 11 {
			steps[i].StartedAt, steps[i].CompletedAt = "2026-11-11T16:00:00Z", "2026-11-11T16:05:00Z"
		}
	}
	if got := FailedStepExcerpt(log, steps, 11); got != "" {
		t.Errorf("a header outside the step's window gave %q", got)
	}
	if got := FailedStepExcerpt("", job.Steps, 11); got != "" {
		t.Errorf("an empty log gave %q", got)
	}
}

func TestFailedStepExcerpt_Bounded(t *testing.T) {
	var b strings.Builder
	b.WriteString("2026-01-01T00:00:00.1Z ##[group]Run go test ./...\n")
	for i := 1; i <= 200; i++ {
		fmt.Fprintf(&b, "2026-01-01T00:00:01.%03dZ %s %d\n", i, strings.Repeat("x", 300), i)
	}
	steps := []WorkflowRunStep{{Name: "Test", Number: 2, Conclusion: "failure", StartedAt: "2026-01-01T00:00:00Z", CompletedAt: "2026-01-01T00:00:01Z"}}
	got := FailedStepExcerpt(b.String(), steps, 2)
	if got == "" || len(got) > JobLogExcerptMaxBytes || len(strings.Split(got, "\n")) > JobLogExcerptMaxLines {
		t.Fatalf("excerpt not bounded: %d bytes, %d lines", len(got), len(strings.Split(got, "\n")))
	}
	if !strings.HasSuffix(got, " 200") || strings.HasPrefix(got, "x ") {
		t.Errorf("excerpt is not the step's end on a line boundary: %q...", got[:40])
	}
}

// The merge-group rollup names each failed CheckRun's Actions job, so the
// removal can be classified from its steps; a commit status has no job.
func TestMergeQueueFailure_CarriesFailedJobs(t *testing.T) {
	rollupBody := `{"data":{"node":{"baseRepository":{"object":{"statusCheckRollup":{"contexts":{"nodes":[
	  {"__typename":"CheckRun","name":"Playwright webview tests","status":"COMPLETED","conclusion":"FAILURE","databaseId":130605138386,"checkSuite":{"workflowRun":{"databaseId":43044474409}}},
	  {"__typename":"CheckRun","name":"Go build & test","status":"COMPLETED","conclusion":"FAILURE","databaseId":130605138765,"checkSuite":{"workflowRun":{"databaseId":43044474409}}},
	  {"__typename":"CheckRun","name":"Lint","status":"COMPLETED","conclusion":"SUCCESS","databaseId":51234567892,"checkSuite":{"workflowRun":{"databaseId":17000000001}}},
	  {"__typename":"StatusContext","context":"cla/check","state":"ERROR"}
	]}}}}}}}`
	var gotQuery string
	q := NewMergeQueue(func(_ context.Context, query string, _ map[string]any) ([]byte, error) {
		gotQuery = query
		return []byte(rollupBody), nil
	})
	err := q.failure(context.Background(), "PR_1", 7, MergeQueueDequeued, "abc123", nil)
	if !strings.Contains(gotQuery, "databaseId") || !strings.Contains(gotQuery, "workflowRun") {
		t.Fatalf("merge-group checks query does not ask for job and run ids: %s", gotQuery)
	}
	qe, ok := err.(*MergeQueueError)
	if !ok {
		t.Fatalf("failure() = %T, want *MergeQueueError", err)
	}
	if strings.Join(qe.FailedChecks, ",") != "Go build & test,Playwright webview tests,cla/check" {
		t.Errorf("FailedChecks = %v", qe.FailedChecks)
	}
	want := []MergeGroupJob{
		{Name: "Go build & test", JobID: 130605138765, RunID: 43044474409},
		{Name: "Playwright webview tests", JobID: 130605138386, RunID: 43044474409},
	}
	if len(qe.FailedJobs) != len(want) {
		t.Fatalf("FailedJobs = %+v, want %+v", qe.FailedJobs, want)
	}
	for i := range want {
		if qe.FailedJobs[i] != want[i] {
			t.Errorf("FailedJobs[%d] = %+v, want %+v", i, qe.FailedJobs[i], want[i])
		}
	}
}

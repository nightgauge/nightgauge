package stages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/ci"
	"github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
)

// stepKinds is the step classifier built from this repository's own
// workflows, so a change to ci.yml that this classifier cannot read fails here.
func stepKinds(t *testing.T) func(string) ci.StepKind {
	t.Helper()
	root := filepath.Join("..", "..", "..")
	disc, err := ci.DiscoverCommands(context.Background(), root, "")
	if err != nil {
		t.Fatal(err)
	}
	kind := StepKinds(ci.ListWorkflowSteps(root), disc.Commands)
	if kind == nil {
		t.Fatal("no check step found in this repository's workflows")
	}
	return kind
}

func steps(spec ...string) []github.WorkflowRunStep {
	out := make([]github.WorkflowRunStep, 0, len(spec))
	for i, s := range spec {
		name, concl, _ := strings.Cut(s, "=")
		out = append(out, github.WorkflowRunStep{Name: name, Number: i + 1, Status: "completed", Conclusion: concl})
	}
	return out
}

var (
	// #3191 / #3195: the merge-group job of run 43034809958 (and again in
	// 43044474409), as GitHub recorded it. Build passed, then apt failed
	// while installing Playwright's OS dependencies; the test never ran.
	playwrightSetupJob = MergeGroupJobResult{Check: "Playwright webview tests", JobID: 130571120205, RunID: 43034809958, Steps: steps(
		"Set up job=success",
		"Run actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1=success",
		"Run actions/setup-node@949feb2413d6458794dcd2491c4babbbce0c15c1=success",
		"Install=success",
		"Build=success",
		"Get Playwright version=success",
		"Cache Playwright browser binary=success",
		"Install Playwright browser (chromium, cache miss)=skipped",
		"Install Playwright OS dependencies (cache hit)=failure",
		"Test (Playwright)=skipped",
		"Upload dashboard fixture screenshots=success",
		"Upload Playwright failure artifacts (screenshots, traces, video)=success",
		"Post Cache Playwright browser binary=skipped",
		"Post Run actions/setup-node@949feb2413d6458794dcd2491c4babbbce0c15c1=skipped",
		"Post Run actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1=success",
		"Complete job=success")}
	// #3193: the Go job of run 43044474409 failed in its own test step.
	goTestJob = MergeGroupJobResult{Check: "Go build & test", JobID: 130605138765, RunID: 43044474409, Steps: steps(
		"Set up job=success",
		"Run actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1=success",
		"Configure git identity (tests create local repos)=success",
		"Run actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e=success",
		"CI change-class gate regression suite=success",
		"Working-tree content guards (encoding, skill includes, portability)=success",
		"Platform operation registry in sync=success",
		"Build=success",
		"Vet=success",
		"Gofmt=success",
		"Test (plain and race, concurrently)=failure",
		"OpenCode integration (opencode-ai@1.18.30)=success",
		"Upload Go test logs=success",
		"branch-merged-check.sh regression suite=success",
		"Verify required steps did not silently skip=success",
		"Post Run actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e=skipped",
		"Post Run actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1=success",
		"Complete job=success")}
)

const goTestStep = "Test (plain and race, concurrently)"

func TestClassifyMergeGroupFailure(t *testing.T) {
	kind := stepKinds(t)
	goCancelled := MergeGroupJobResult{Check: "Go build & test", Steps: steps(
		"Set up job=success", "Build=success", "Vet=success", "Gofmt=success", goTestStep+"=cancelled")}
	cases := []struct {
		name      string
		jobs      []MergeGroupJobResult
		wantClass MergeGroupFailureClass
		wantJob   string
		wantStep  string
	}{
		{"infra only (#3191, recorded)", []MergeGroupJobResult{playwrightSetupJob}, MergeGroupInfra, "Playwright webview tests", "Install Playwright OS dependencies (cache hit)"},
		{"code only (recorded)", []MergeGroupJobResult{goTestJob}, MergeGroupCode, "Go build & test", goTestStep},
		{"mixed, code decides (#3193, recorded)", []MergeGroupJobResult{playwrightSetupJob, goTestJob}, MergeGroupCode, "Go build & test", goTestStep},
		{"mixed, code decides in any order", []MergeGroupJobResult{goTestJob, playwrightSetupJob}, MergeGroupCode, "Go build & test", goTestStep},
		{"a cancelled own step does not outvote infra", []MergeGroupJobResult{playwrightSetupJob, goCancelled}, MergeGroupInfra, "Playwright webview tests", "Install Playwright OS dependencies (cache hit)"},
		{"cancelled only", []MergeGroupJobResult{goCancelled}, MergeGroupUnclassified, "", ""},
		{"setup failure before build", []MergeGroupJobResult{{Check: "Go build & test", Steps: steps(
			"Set up job=success", "Run actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e=failure", "Build=skipped", goTestStep+"=skipped")}}, MergeGroupInfra, "Go build & test", "Run actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e"},
		{"setup step timed out", []MergeGroupJobResult{{Check: "Go build & test", Steps: steps(
			"Configure git identity (tests create local repos)=timed_out", "Build=skipped")}}, MergeGroupInfra, "Go build & test", "Configure git identity (tests create local repos)"},
		{"a lockfile install the PR can break is code (#3198 review)", []MergeGroupJobResult{{Check: "Go build & test", Steps: steps(
			"Install=failure", "Build=skipped")}}, MergeGroupCode, "Go build & test", "Install"},
		{"checks after a setup failure still ran", []MergeGroupJobResult{{Check: "Go build & test", Steps: steps(
			"Build=success", "Run actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e=failure",
			"branch-merged-check.sh regression suite=success")}}, MergeGroupUnclassified, "", ""},
		{"a check the classifier does not recognise is code", []MergeGroupJobResult{{Check: "Go build & test", Steps: steps(
			"Build=success", "Vet=failure", goTestStep+"=skipped")}}, MergeGroupCode, "Go build & test", "Vet"},
		{"a mixed install-and-test step is code", []MergeGroupJobResult{{Check: "x", Steps: steps(
			"Test retro engine (pytest)=failure", goTestStep+"=skipped")}}, MergeGroupCode, "x", "Test retro engine (pytest)"},
		{"a step the workflows do not name is code", []MergeGroupJobResult{{Check: "x", Steps: steps(
			"Install=success", "Custom verify=failure", goTestStep+"=skipped")}}, MergeGroupCode, "x", "Custom verify"},
		{"a failed step with no name is unclassified", []MergeGroupJobResult{{Check: "x", Steps: steps(
			"=failure", goTestStep+"=skipped")}}, MergeGroupUnclassified, "", ""},
		{"no own step after the failure", []MergeGroupJobResult{{Check: "Docs", Steps: steps(
			"Set up job=success", "Configure git identity (tests create local repos)=failure", "Upload Go test logs=skipped")}}, MergeGroupUnclassified, "", ""},
		{"no step failed", []MergeGroupJobResult{{Check: "Go build & test", Steps: steps("Build=success", goTestStep+"=success")}}, MergeGroupUnclassified, "", ""},
		{"unreadable steps", []MergeGroupJobResult{{Check: "Go build & test", JobID: 12}}, MergeGroupUnclassified, "", ""},
		{"infra plus an unclassifiable status", []MergeGroupJobResult{playwrightSetupJob, {Check: "cla/check"}}, MergeGroupUnclassified, "", ""},
		{"code plus an unclassifiable status", []MergeGroupJobResult{goTestJob, {Check: "cla/check"}}, MergeGroupCode, "Go build & test", goTestStep},
		{"no jobs", nil, MergeGroupUnclassified, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyMergeGroupFailure(tc.jobs, kind)
			if got.Class != tc.wantClass || got.Job != tc.wantJob || got.Step != tc.wantStep {
				t.Errorf("got %+v, want class=%s job=%q step=%q", got, tc.wantClass, tc.wantJob, tc.wantStep)
			}
		})
	}
}

func TestClassifyMergeGroupFailure_NoOwnStepsIsUnclassified(t *testing.T) {
	if got := ClassifyMergeGroupFailure([]MergeGroupJobResult{goTestJob}, nil); got.Class != MergeGroupUnclassified {
		t.Errorf("without own steps a removal must stay unclassified, got %+v", got)
	}
}

func TestStepKinds_RepositoryWorkflows(t *testing.T) {
	kind := stepKinds(t)
	for step, want := range map[string]ci.StepKind{
		goTestStep:                           ci.StepCheck, // a multi-line block parity discovery drops
		"Test (Playwright)":                  ci.StepCheck,
		"Build":                              ci.StepCheck,
		"Gofmt":                              ci.StepCheck,
		"Install Playwright OS dependencies": ci.StepSetup,
		"Install Playwright OS dependencies (cache hit)": ci.StepSetup, // rendered with a suffix
		"Install":                ci.StepOther, // npm ci reads the PR's lockfile
		"Get Playwright version": ci.StepCheck, // names @playwright/test: a check wins over GITHUB_OUTPUT
		"Configure git identity (tests create local repos)":             ci.StepSetup,
		"Run actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1": ci.StepSetup,
		"Set up job":                 ci.StepSetup,
		"Vet":                        ci.StepOther, // go vet: a check the classifier does not recognise
		"Test retro engine (pytest)": ci.StepCheck, // pip install pytest && pytest: the check wins
		"Install Playwright browser (chromium, cache miss)": ci.StepSetup,
		"No such step": ci.StepOther,
		"":             ci.StepOther,
	} {
		if got := kind(step); got != want {
			t.Errorf("kind(%q) = %q, want %q", step, got, want)
		}
	}
}

func TestStepKinds_NamesAndExpressions(t *testing.T) {
	kind := StepKinds([]ci.WorkflowStep{
		{Name: "", Run: "npm run lint"},
		{Name: "Test ${{ matrix.os }}", Run: "go test ./..."},
		{Name: "Install", Run: "npm ci"},
		{Uses: "actions/setup-go@v5"},
		{Name: "Lint", Run: "eslint --cache ."},
		{Name: "Unit", Run: "go test ./internal/cache/..."},
		{Uses: "golangci/golangci-lint-action@v6"},
	}, nil)
	for step, want := range map[string]ci.StepKind{
		"Run npm run lint":                     ci.StepCheck, // GitHub's name for an unnamed step
		"Run npm run lint -- --max-warnings":   ci.StepCheck,
		"Test ubuntu-latest":                   ci.StepCheck,
		"Install":                              ci.StepOther,
		"Run actions/setup-go@v5":              ci.StepSetup,
		"Lint":                                 ci.StepCheck,
		"Unit":                                 ci.StepCheck,
		"Run golangci/golangci-lint-action@v6": ci.StepOther, // an action may run a check
	} {
		if got := kind(step); got != want {
			t.Errorf("kind(%q) = %q, want %q", step, got, want)
		}
	}
	if StepKinds([]ci.WorkflowStep{{Name: "Install", Run: "npm ci"}, {Uses: "golangci/golangci-lint-action@v6"}}, nil) != nil {
		t.Error("a step set with no check must yield no classifier")
	}
}

// removalQueueGh is a merge-queue client whose successive waits return the
// given outcomes, and which serves recorded job steps and logs.
type removalQueueGh struct {
	queueGh
	outcomes []error
	jobs     map[int64][]github.WorkflowRunStep
	logs     map[int64]string
}

func (q *removalQueueGh) EnqueueAndWait(_ context.Context, st github.MergeQueueStatus, _ github.MergeQueueWaitOptions) (github.MergeQueueStatus, error) {
	q.queueCalls++
	q.gotStatus = st
	if q.queueCalls > len(q.outcomes) || q.outcomes[q.queueCalls-1] == nil {
		return github.MergeQueueStatus{State: "MERGED"}, nil
	}
	return github.MergeQueueStatus{State: "OPEN"}, q.outcomes[q.queueCalls-1]
}

func (q *removalQueueGh) WorkflowJob(_ context.Context, id int64) (github.WorkflowRunJob, error) {
	s, ok := q.jobs[id]
	if !ok {
		return github.WorkflowRunJob{}, errors.New("HTTP 404")
	}
	return github.WorkflowRunJob{ID: id, Steps: s}, nil
}

func (q *removalQueueGh) WorkflowJobLog(_ context.Context, id int64) (string, error) {
	l, ok := q.logs[id]
	if !ok {
		return "", errors.New("HTTP 404")
	}
	return l, nil
}

// timedJobLog stamps steps one minute apart and renders the plain-text job
// log GitHub would serve for them: a `##[group]Run` header per step that ran,
// then that step's output (body[name], or a line naming the step).
func timedJobLog(steps []github.WorkflowRunStep, body map[string]string) ([]github.WorkflowRunStep, string) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	out := make([]github.WorkflowRunStep, len(steps))
	var b strings.Builder
	for i, st := range steps {
		start := base.Add(time.Duration(i) * time.Minute)
		st.StartedAt = start.Format(time.RFC3339)
		st.CompletedAt = start.Add(30 * time.Second).Format(time.RFC3339)
		out[i] = st
		ran := st.Conclusion != "skipped" && st.Name != "Set up job" && st.Name != "Complete job" && !strings.HasPrefix(st.Name, "Post ")
		if !ran {
			continue
		}
		text, ok := body[st.Name]
		if !ok {
			text = "output of " + st.Name
		}
		fmt.Fprintf(&b, "%s ##[group]Run %s\n", start.Add(100*time.Millisecond).Format(time.RFC3339Nano), st.Name)
		for j, line := range strings.Split(text, "\n") {
			fmt.Fprintf(&b, "%s %s\n", start.Add(time.Duration(j+2)*time.Second).Format(time.RFC3339Nano), line)
		}
	}
	return out, b.String()
}

func removal(jobs ...MergeGroupJobResult) error {
	e := &github.MergeQueueError{PRNumber: 42, Kind: github.MergeQueueDequeued, EntryHeadOID: "abc"}
	for _, j := range jobs {
		e.FailedChecks = append(e.FailedChecks, j.Check)
		e.FailedJobs = append(e.FailedJobs, github.MergeGroupJob{Name: j.Check, JobID: j.JobID, RunID: j.RunID})
	}
	return e
}

func newRemovalRunner(t *testing.T, q *removalQueueGh) (*DeterministicRunner, *[]time.Duration) {
	t.Helper()
	q.fakeGh = fakeGh{preMerge: cleanOpen}
	q.status = github.MergeQueueStatus{PRID: "PR_1", State: "OPEN", QueueRequired: true, BaseRef: "main"}
	q.jobs = map[int64][]github.WorkflowRunStep{playwrightSetupJob.JobID: playwrightSetupJob.Steps, goTestJob.JobID: goTestJob.Steps}
	r := newQueueRunner(&q.queueGh)
	r.gh = q
	kind := stepKinds(t)
	r.stepKinds = func(context.Context, string) (func(string) ci.StepKind, error) { return kind, nil }
	var slept []time.Duration
	r.queueSleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	return r, &slept
}

func TestRun_MergeQueueInfraRemoval_ReEnqueuesThenMerges(t *testing.T) {
	q := &removalQueueGh{outcomes: []error{removal(playwrightSetupJob), nil}}
	r, slept := newRemovalRunner(t, q)
	res, err := r.Run(context.Background(), 7, "", "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Path != PathMerged || res.Reason != ReasonMergeQueueMerged {
		t.Fatalf("got %+v, want merged after one re-enqueue", res)
	}
	if q.queueCalls != 2 || len(*slept) != 1 || (*slept)[0] != DefaultQueueInfraRetryDelay {
		t.Errorf("queue calls %d, sleeps %v; want 2 calls and one %s delay", q.queueCalls, *slept, DefaultQueueInfraRetryDelay)
	}
	if q.gotStatus.InQueue {
		t.Error("the re-enqueue reused a stale in-queue status, so it would never enqueue again")
	}
}

func TestRun_MergeQueueInfraRemoval_ExhaustedIsNamedAndNeedsNoChange(t *testing.T) {
	q := &removalQueueGh{outcomes: []error{removal(playwrightSetupJob), removal(playwrightSetupJob), removal(playwrightSetupJob)}}
	r, slept := newRemovalRunner(t, q)
	res, err := r.Run(context.Background(), 7, "", "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Path != PathRefused || !strings.HasPrefix(res.Reason, ReasonMergeQueueInfraExhausted+":") {
		t.Fatalf("got %+v, want a %s refusal", res, ReasonMergeQueueInfraExhausted)
	}
	for _, want := range []string{`job "Playwright webview tests"`, `step "Install Playwright OS dependencies (cache hit)"`, "re-enqueued 2 time(s)", "the PR needs no change"} {
		if !strings.Contains(res.Reason, want) {
			t.Errorf("reason %q does not name %q", res.Reason, want)
		}
	}
	if q.queueCalls != 1+DefaultQueueInfraRetries || len(*slept) != DefaultQueueInfraRetries {
		t.Errorf("queue calls %d, sleeps %d; want %d and %d", q.queueCalls, len(*slept), 1+DefaultQueueInfraRetries, DefaultQueueInfraRetries)
	}
}

// A stage stopped while it retries an infrastructure removal (its context
// ends during the delay, or during the re-enqueued wait) ends
// merge-queue-infra-exhausted, never in a timeout or enqueue-failed kind.
func TestRun_MergeQueueInfraRemoval_InterruptedRetryIsInfraExhausted(t *testing.T) {
	for _, tc := range []struct {
		name           string
		outcomes       []error
		sleepErr       bool
		wantReenqueues string
	}{
		{"during the delay", []error{removal(playwrightSetupJob)}, true, "re-enqueued 0 time(s)"},
		{"during the re-enqueued wait", []error{removal(playwrightSetupJob), context.Canceled}, false, "re-enqueued 1 time(s)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := &removalQueueGh{outcomes: tc.outcomes}
			r, _ := newRemovalRunner(t, q)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r.queueSleep = func(context.Context, time.Duration) error {
				cancel()
				if tc.sleepErr {
					return context.Canceled
				}
				return nil
			}
			res, err := r.Run(ctx, 7, "", "")
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if res.Path != PathRefused || !strings.HasPrefix(res.Reason, ReasonMergeQueueInfraExhausted+":") ||
				!strings.Contains(res.Reason, tc.wantReenqueues) || !strings.Contains(res.Reason, "the PR needs no change") {
				t.Fatalf("got %+v, want a %s refusal naming %q", res, ReasonMergeQueueInfraExhausted, tc.wantReenqueues)
			}
		})
	}
}

func TestRun_MergeQueueCodeRemoval_MarkedAndRecorded(t *testing.T) {
	for _, tc := range []struct {
		name string
		jobs []MergeGroupJobResult
	}{
		{"code only", []MergeGroupJobResult{goTestJob}},
		{"mixed (#3193): code decides", []MergeGroupJobResult{playwrightSetupJob, goTestJob}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ws := layouttest.Repo(t)
			q := &removalQueueGh{outcomes: []error{removal(tc.jobs...)}}
			r, slept := newRemovalRunner(t, q)
			stamped, jobLog := timedJobLog(goTestJob.Steps, map[string]string{
				goTestStep: "=== RUN TestSkipAccounting\n--- FAIL: TestSkipAccounting (0.00s)\nFAIL",
			})
			q.jobs[goTestJob.JobID] = stamped
			q.logs = map[int64]string{goTestJob.JobID: jobLog}
			res, err := r.Run(context.Background(), 7, "", ws)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			want := fmt.Sprintf(`%s: %s: job "Go build & test" step "Test (plain and race, concurrently)"`, ReasonMergeQueueFailed, MergeQueueCodeFailureMarker)
			if res.Path != PathRefused || !strings.HasPrefix(res.Reason, want) {
				t.Fatalf("got %+v, want reason prefix %q", res, want)
			}
			if q.queueCalls != 1 || len(*slept) != 0 {
				t.Errorf("a code failure was re-enqueued (%d calls, %d sleeps)", q.queueCalls, len(*slept))
			}
			data, err := os.ReadFile(filepath.Join(layouttest.PipelineDir(t, ws), MergeQueueFailureFile(7)))
			if err != nil {
				t.Fatalf("failure record: %v", err)
			}
			var rec MergeQueueFailureRecord
			if err := json.Unmarshal(data, &rec); err != nil {
				t.Fatal(err)
			}
			if rec.Check != goTestJob.Check || rec.Step != goTestStep || rec.JobID != goTestJob.JobID || rec.PRNumber != 42 ||
				!strings.Contains(rec.LogExcerpt, "--- FAIL: TestSkipAccounting") {
				t.Errorf("record = %+v", rec)
			}
			// The excerpt is the failed step's own section, not the job's tail:
			// the always() steps after it ran and logged too.
			if strings.Contains(rec.LogExcerpt, "OpenCode") || strings.Contains(rec.LogExcerpt, "Verify required steps") {
				t.Errorf("excerpt carries lines from steps after the failed one:\n%s", rec.LogExcerpt)
			}
		})
	}
}

func TestRun_MergeQueueUnclassifiedRemoval_KeepsTodaysReason(t *testing.T) {
	q := &removalQueueGh{outcomes: []error{removal(MergeGroupJobResult{Check: "Go", JobID: 99})}} // job unreadable
	r, slept := newRemovalRunner(t, q)
	res, err := r.Run(context.Background(), 7, "", "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Path != PathRefused || !strings.HasPrefix(res.Reason, ReasonMergeQueueFailed+": merge queue:") ||
		strings.Contains(res.Reason, MergeQueueCodeFailureMarker) {
		t.Fatalf("got %+v, want the plain merge-queue-failed reason", res)
	}
	if q.queueCalls != 1 || len(*slept) != 0 {
		t.Errorf("an unclassified removal was re-enqueued")
	}
}

func TestRun_MergeQueueRemoval_DiscoveryFailureIsUnclassified(t *testing.T) {
	q := &removalQueueGh{outcomes: []error{removal(playwrightSetupJob)}}
	r, _ := newRemovalRunner(t, q)
	r.stepKinds = func(context.Context, string) (func(string) ci.StepKind, error) {
		return nil, errors.New("no workflows")
	}
	res, _ := r.Run(context.Background(), 7, "", "")
	if strings.HasPrefix(res.Reason, ReasonMergeQueueInfraExhausted) || q.queueCalls != 1 {
		t.Fatalf("without discovered checks a removal must not be treated as infrastructure: %+v", res)
	}
}

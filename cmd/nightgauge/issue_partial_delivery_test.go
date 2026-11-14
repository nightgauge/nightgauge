package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	stagecontext "github.com/nightgauge/nightgauge/internal/execution/context"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/hooks"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
	"github.com/nightgauge/nightgauge/internal/trust"
)

type stubPartialReader struct {
	comments []trust.Comment
	err      error
}

func (s stubPartialReader) TrustedComments(context.Context, string, string, int) ([]trust.Comment, error) {
	return s.comments, s.err
}

func (s stubPartialReader) LatestClosingMerge(context.Context, string, string, int) (*gh.ClosingMerge, error) {
	return nil, nil
}

func withPartialReader(t *testing.T, r hooks.PartialDeliveryReader) {
	t.Helper()
	withPartialDeps(t, r, orchestrator.PartialScopeReads{
		Body: func(context.Context) (string, error) { return "", errors.New("no body in this test") },
	})
}

func TestIssuePartialDeliveryJSONPending(t *testing.T) {
	body := "<!-- nightgauge:partial-delivery pr=3085 -->\nPR #3085 merged part.\n\n" +
		hooks.RemainingCriteriaHeading + "\n\n- criterion one\n"
	withPartialReader(t, stubPartialReader{comments: []trust.Comment{{Body: body, CreatedAt: time.Now()}}})
	var out, errOut bytes.Buffer
	if code := runIssuePartialDelivery(context.Background(), &out, &errOut, "o", "r", 3084, true, false); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	var got hooks.PartialDeliveryStatus
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Pending || got.PR != 3085 || len(got.Criteria) != 1 || got.Criteria[0] != "criterion one" {
		t.Fatalf("got %+v", got)
	}
}

func TestIssuePartialDeliveryNoMarker(t *testing.T) {
	withPartialReader(t, stubPartialReader{})
	var out, errOut bytes.Buffer
	if code := runIssuePartialDelivery(context.Background(), &out, &errOut, "o", "r", 1, true, false); code != 0 {
		t.Fatalf("exit %d", code)
	}
	var got hooks.PartialDeliveryStatus
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.Pending {
		t.Fatalf("got %+v err %v", got, err)
	}
}

func TestIssuePartialDeliveryReadFailureExits2(t *testing.T) {
	withPartialReader(t, stubPartialReader{err: errors.New("rate limited")})
	var out, errOut bytes.Buffer
	if code := runIssuePartialDelivery(context.Background(), &out, &errOut, "o", "r", 1, true, false); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if out.Len() != 0 {
		t.Fatalf("stdout should be empty on failure: %q", out.String())
	}
}

// The extension's stage loop scopes a re-run from these blocks (#3095): a
// pending, listed remainder carries one per scoped stage; none otherwise.
func TestIssuePartialDeliveryJSONPromptBlocks(t *testing.T) {
	body := "<!-- nightgauge:partial-delivery pr=3085 -->\nPR #3085 merged part.\n\n" +
		hooks.RemainingCriteriaHeading + "\n\n- criterion one\n"
	withPartialReader(t, stubPartialReader{comments: []trust.Comment{{Body: body, CreatedAt: time.Now()}}})
	var out, errOut bytes.Buffer
	if code := runIssuePartialDelivery(context.Background(), &out, &errOut, "o", "r", 3084, true, false); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	var got struct {
		PromptBlocks map[string]string `json:"prompt_blocks"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"feature-planning", "feature-dev"} {
		b := got.PromptBlocks[stage]
		if !strings.Contains(b, "## Partial-Delivery Re-run Scope") || !strings.Contains(b, "criterion one") ||
			!strings.Contains(b, "PR #3085") {
			t.Errorf("%s block = %q, want the scope heading, PR #3085 and the criterion", stage, b)
		}
	}
	if len(got.PromptBlocks) != 2 {
		t.Errorf("prompt_blocks has %d stages, want feature-planning and feature-dev only", len(got.PromptBlocks))
	}

	withPartialReader(t, stubPartialReader{})
	out.Reset()
	if code := runIssuePartialDelivery(context.Background(), &out, &errOut, "o", "r", 1, true, false); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if strings.Contains(out.String(), "prompt_blocks") {
		t.Fatalf("no remainder, yet prompt_blocks present: %s", out.String())
	}
}

func withPartialDeps(t *testing.T, r hooks.PartialDeliveryReader, reads orchestrator.PartialScopeReads) {
	t.Helper()
	prev := partialDeliveryDepsFactory
	partialDeliveryDepsFactory = func(string, string, int) (hooks.PartialDeliveryReader, orchestrator.PartialScopeReads, error) {
		return r, reads, nil
	}
	t.Cleanup(func() { partialDeliveryDepsFactory = prev })
}

var partialCmdOpened = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

// partialCmdReads stubs the forge: the issue body holds bodyCriteria, the
// partial PR (opened at partialCmdOpened) reported delivered met and listed
// unmet, and the body was last edited at edited.
func partialCmdReads(bodyCriteria, delivered, listed []string, edited time.Time) orchestrator.PartialScopeReads {
	var lines []string
	for _, c := range bodyCriteria {
		lines = append(lines, "- [ ] "+c)
	}
	body := "## Acceptance criteria\n\n" + strings.Join(lines, "\n") + "\n"
	var vs []pmstages.ACVerdict
	for i, m := range delivered {
		vs = append(vs, pmstages.ACVerdict{Index: i, Text: m, Status: pmstages.ACMet, Source: "diff", Evidence: "internal/x.go"})
	}
	for i, u := range listed {
		vs = append(vs, pmstages.ACVerdict{Index: len(delivered) + i, Text: u, Status: pmstages.ACNotVerifiable, Source: "judge"})
	}
	prBody := pmstages.RenderBody(pmstages.PRCreateSnapshot{IssueNumber: 3242, ACVerdicts: vs})
	return orchestrator.PartialScopeReads{
		Body: func(context.Context) (string, error) { return body, nil },
		PR: func(context.Context, int) (time.Time, string, error) {
			return partialCmdOpened, prBody, nil
		},
		BodyEditedAt: func(context.Context) (*time.Time, error) { return &edited, nil },
	}
}

type partialCmdJSON struct {
	Criteria     []string          `json:"criteria"`
	Scope        []string          `json:"scope"`
	Dropped      []string          `json:"dropped"`
	PromptBlocks map[string]string `json:"prompt_blocks"`
}

// AC1 (#3242): the command's brief is the scheduler's reconcile of the same
// remainder, body and partial PR — unchanged body, criterion added after the
// PR, criterion removed from the body.
func TestIssuePartialDeliveryJSONReconcilesLikeScheduler(t *testing.T) {
	listed := []string{"Listed behaviour one is handled by the scheduler", "Listed behaviour two is handled by the scheduler"}
	delivered := []string{"Delivered behaviour is handled by the scheduler"}
	added := "New behaviour is handled by the scheduler"
	cases := []struct {
		name        string
		body        []string
		edited      time.Time
		wantScope   []string
		wantDropped []string
	}{
		{"unchanged body", append(append([]string{}, listed...), delivered...), partialCmdOpened.Add(-time.Hour),
			listed, nil},
		{"criterion added after the PR", append(append(append([]string{}, listed...), delivered...), added), partialCmdOpened.Add(48 * time.Hour),
			append(append([]string{}, listed...), added), nil},
		{"criterion removed", append([]string{listed[0]}, delivered...), partialCmdOpened.Add(48 * time.Hour),
			listed[:1], listed[1:]},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			comment := "<!-- nightgauge:partial-delivery pr=3085 -->\nPR #3085 merged part.\n\n" +
				hooks.RemainingCriteriaHeading + "\n\n- " + strings.Join(listed, "\n- ") + "\n"
			reads := partialCmdReads(tc.body, delivered, listed, tc.edited)
			withPartialDeps(t, stubPartialReader{comments: []trust.Comment{{Body: comment, CreatedAt: time.Now()}}}, reads)
			var out, errOut, logs bytes.Buffer
			prevLog := log.Writer()
			log.SetOutput(&logs)
			t.Cleanup(func() { log.SetOutput(prevLog) })
			if code := runIssuePartialDelivery(context.Background(), &out, &errOut, "o", "r", 3242, true, false); code != 0 {
				t.Fatalf("exit %d: %s", code, errOut.String())
			}
			var got partialCmdJSON
			if err := json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatalf("stdout is not JSON: %v\n%s", err, out.String())
			}
			if strings.Join(got.Scope, "|") != strings.Join(tc.wantScope, "|") ||
				strings.Join(got.Dropped, "|") != strings.Join(tc.wantDropped, "|") {
				t.Fatalf("scope=%q dropped=%q, want %q / %q", got.Scope, got.Dropped, tc.wantScope, tc.wantDropped)
			}
			if strings.Join(got.Criteria, "|") != strings.Join(listed, "|") {
				t.Errorf("criteria = %q, want the comment's list", got.Criteria)
			}
			// The scheduler path runs this same function over its seams.
			ws := partialDeliveryWorkspace(context.Background())
			sched, schedDropped := orchestrator.ReconcilePartialScope(context.Background(), reads, ws, 3242, listed, 3085)
			if strings.Join(sched, "|") != strings.Join(got.Scope, "|") || strings.Join(schedDropped, "|") != strings.Join(got.Dropped, "|") {
				t.Errorf("command scope %q differs from scheduler reconcile %q", got.Scope, sched)
			}
			for stage, b := range got.PromptBlocks {
				for _, c := range tc.wantScope {
					if !strings.Contains(b, c) {
						t.Errorf("%s block lacks %q", stage, c)
					}
				}
				for _, d := range tc.wantDropped {
					if strings.Contains(b, d) {
						t.Errorf("%s block still carries dropped %q", stage, d)
					}
				}
			}
			if len(got.PromptBlocks) != 2 {
				t.Errorf("prompt_blocks has %d stages, want 2", len(got.PromptBlocks))
			}
			for _, d := range tc.wantDropped {
				if !strings.Contains(logs.String(), "dropped: \""+d) {
					t.Errorf("drop of %q not logged: %s", d, logs.String())
				}
			}
		})
	}
}

// partialDeliveryClone runs the test from a fresh git clone, so the record
// lands in that clone's pipeline state directory, never this checkout's.
func partialDeliveryClone(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gittest.InitRepo(t, dir, "-q")
	t.Chdir(dir)
	return partialDeliveryWorkspace(context.Background())
}

// #3163 AC 0: --write-context writes the remainder the extension splices, the
// --json output carries the same record, and a later read with nothing pending
// removes the file so a stale remainder is never spliced.
func TestIssuePartialDeliveryWriteContext(t *testing.T) {
	ws := partialDeliveryClone(t)
	path, err := stagecontext.ContextPath(ws, 3084, "partial-delivery")
	if err != nil {
		t.Fatal(err)
	}
	body := "<!-- nightgauge:partial-delivery pr=3085 -->\nPR #3085 merged part.\n\n" +
		hooks.RemainingCriteriaHeading + "\n\n- criterion one\n"
	withPartialReader(t, stubPartialReader{comments: []trust.Comment{{Body: body, CreatedAt: time.Now()}}})
	var out, errOut bytes.Buffer
	if code := runIssuePartialDelivery(context.Background(), &out, &errOut, "o", "r", 3084, true, true); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("partial-delivery context not written: %v", err)
	}
	var file orchestrator.PartialDeliveryFile
	if err := json.Unmarshal(written, &file); err != nil || file.PR != 3085 ||
		len(file.Criteria) != 1 || file.Criteria[0] != "criterion one" {
		t.Fatalf("written record = %s (%v)", written, err)
	}
	var got struct {
		PartialDelivery json.RawMessage `json:"partial_delivery"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	var a, b any
	_ = json.Unmarshal(got.PartialDelivery, &a)
	_ = json.Unmarshal(written, &b)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("--json partial_delivery %s differs from the written record %s", got.PartialDelivery, written)
	}

	withPartialReader(t, stubPartialReader{})
	out.Reset()
	if code := runIssuePartialDelivery(context.Background(), &out, &errOut, "o", "r", 3084, true, true); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("stale partial-delivery context survived a read with nothing pending: %v", err)
	}
	if strings.Contains(out.String(), "partial_delivery") {
		t.Fatalf("nothing pending, yet partial_delivery present: %s", out.String())
	}

	// A failed read removes it too: the run is unscoped.
	if err := orchestrator.WritePartialDeliveryContext(ws, 3084, file); err != nil {
		t.Fatal(err)
	}
	withPartialReader(t, stubPartialReader{err: errors.New("rate limited")})
	if code := runIssuePartialDelivery(context.Background(), &out, &errOut, "o", "r", 3084, true, true); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("partial-delivery context survived a failed read: %v", err)
	}
}

// Without --write-context nothing is written.
func TestIssuePartialDeliveryNoWriteWithoutFlag(t *testing.T) {
	ws := partialDeliveryClone(t)
	body := "<!-- nightgauge:partial-delivery pr=3085 -->\nPR #3085 merged part.\n\n" +
		hooks.RemainingCriteriaHeading + "\n\n- criterion one\n"
	withPartialReader(t, stubPartialReader{comments: []trust.Comment{{Body: body, CreatedAt: time.Now()}}})
	var out, errOut bytes.Buffer
	if code := runIssuePartialDelivery(context.Background(), &out, &errOut, "o", "r", 3084, true, false); code != 0 {
		t.Fatalf("exit %d", code)
	}
	path, _ := stagecontext.ContextPath(ws, 3084, "partial-delivery")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("record written without --write-context: %v", err)
	}
}

// #3163 AC 3: the extension's feature-dev trailer comes from the planning
// context through this verb; nothing is printed when it carries no steer.
func TestIssueOperatorRevision(t *testing.T) {
	ws := partialDeliveryClone(t)
	var out bytes.Buffer
	if err := runIssueOperatorRevision(context.Background(), &out, 1051); err != nil || out.Len() != 0 {
		t.Fatalf("no planning context: out=%q err=%v", out.String(), err)
	}
	path, _ := stagecontext.ContextPath(ws, 1051, "planning")
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	plan := `{"revision_reasons":["operator-origin: action-center: x"],"files_to_modify":["a.go"],` +
		`"operator_steer":[{"text":"Do the small fix.","evidence":[]}]}`
	if err := os.WriteFile(path, []byte(plan), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runIssueOperatorRevision(context.Background(), &out, 1051); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "> Do the small fix.") || !strings.Contains(out.String(), "`a.go`") {
		t.Fatalf("block = %q", out.String())
	}
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	gh "github.com/nightgauge/nightgauge/internal/github"
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
	if code := runIssuePartialDelivery(context.Background(), &out, &errOut, "o", "r", 3084, true); code != 0 {
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
	if code := runIssuePartialDelivery(context.Background(), &out, &errOut, "o", "r", 1, true); code != 0 {
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
	if code := runIssuePartialDelivery(context.Background(), &out, &errOut, "o", "r", 1, true); code != 2 {
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
	if code := runIssuePartialDelivery(context.Background(), &out, &errOut, "o", "r", 3084, true); code != 0 {
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
	if code := runIssuePartialDelivery(context.Background(), &out, &errOut, "o", "r", 1, true); code != 0 {
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
			if code := runIssuePartialDelivery(context.Background(), &out, &errOut, "o", "r", 3242, true); code != 0 {
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

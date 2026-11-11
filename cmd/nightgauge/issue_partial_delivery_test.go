package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/hooks"
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
	prev := partialDeliveryReaderFactory
	partialDeliveryReaderFactory = func() (hooks.PartialDeliveryReader, error) { return r, nil }
	t.Cleanup(func() { partialDeliveryReaderFactory = prev })
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

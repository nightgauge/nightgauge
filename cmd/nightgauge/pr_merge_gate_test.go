package main

import (
	"context"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/pkg/types"
)

type mergeGateIssueFetcher struct {
	issue *types.Issue
	err   error
}

func (m mergeGateIssueFetcher) GetIssue(_ context.Context, owner, repo string, number int) (*types.Issue, error) {
	return m.issue, m.err
}

func TestCheckPRMergeBlockers_OpenBlockerDetected(t *testing.T) {
	err := checkPRMergeBlockers(context.Background(), mergeGateIssueFetcher{
		issue: &types.Issue{
			BlockedBy: []types.BlockingRef{{
				Number: 123,
				State:  "OPEN",
				Repo:   "nightgauge/nightgauge",
			}},
		},
	}, "nightgauge", "nightgauge", 2935, false)
	if err == nil {
		t.Fatal("expected merge blocker error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "Cannot merge: #2935 is blocked by #123 (OPEN)") {
		t.Fatalf("unexpected error message: %s", msg)
	}
	if !strings.Contains(msg, "https://github.com/nightgauge/nightgauge/issues/123") {
		t.Fatalf("expected blocker URL in message: %s", msg)
	}
}

func TestCheckPRMergeBlockers_AllClosedProceedNormally(t *testing.T) {
	err := checkPRMergeBlockers(context.Background(), mergeGateIssueFetcher{
		issue: &types.Issue{
			BlockedBy: []types.BlockingRef{{
				Number: 123,
				State:  "CLOSED",
				Repo:   "nightgauge/nightgauge",
			}},
		},
	}, "nightgauge", "nightgauge", 2935, false)
	if err != nil {
		t.Fatalf("expected no merge blocker error, got %v", err)
	}
}

func TestCheckPRMergeBlockers_NoRelationshipsProceedNormally(t *testing.T) {
	err := checkPRMergeBlockers(context.Background(), mergeGateIssueFetcher{
		issue: &types.Issue{},
	}, "nightgauge", "nightgauge", 2935, false)
	if err != nil {
		t.Fatalf("expected no merge blocker error, got %v", err)
	}
}

func TestCheckPRMergeBlockers_ForceBypassesGuard(t *testing.T) {
	err := checkPRMergeBlockers(context.Background(), mergeGateIssueFetcher{
		issue: &types.Issue{
			BlockedBy: []types.BlockingRef{{
				Number: 123,
				State:  "OPEN",
				Repo:   "nightgauge/nightgauge",
			}},
		},
	}, "nightgauge", "nightgauge", 2935, true)
	if err != nil {
		t.Fatalf("expected --force to bypass merge blocker, got %v", err)
	}
}

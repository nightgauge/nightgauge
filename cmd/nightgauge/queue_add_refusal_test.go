package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
)

type fakeRoots map[string]bool

func (f fakeRoots) RepoRoot(repo string) (string, error) {
	if f[repo] {
		return "/ws/" + repo, nil
	}
	return "", errors.New("no checkout")
}

func refusalCodes(rs []orchestrator.SkipReason) string {
	var c []string
	for _, r := range rs {
		c = append(c, r.Code)
	}
	return strings.Join(c, ",")
}

// #3278: `queue add` refuses what the queue could not run, at add time.
func TestQueueAddRefusals(t *testing.T) {
	roots := fakeRoots{"acme/app": true}
	board := readyBoard(map[int]gh.BoardPlacement{
		1: {OnBoard: true, Status: "Ready", Labels: []string{gh.LabelRefined}},
		// The queue runs an explicitly queued Backlog, unrefined issue.
		2: {OnBoard: true, Status: "Backlog"},
		// 3 is off the queue's board.
		4: {OnBoard: true, Status: "Ready", Labels: []string{"blocked"}},
	})
	cases := []struct {
		repo   string
		number int
		want   string
	}{
		{"app", 1, ""},
		{"app", 2, ""},
		{"app", 3, "not-on-board"},
		{"app", 4, "excluded-label"},
		{"other", 1, SkipRepoNotInWorkspace},
	}
	for _, tc := range cases {
		got, err := queueAddRefusals(context.Background(), roots, board, "acme", tc.repo, tc.number)
		if err != nil {
			t.Fatal(err)
		}
		if refusalCodes(got) != tc.want {
			t.Errorf("acme/%s#%d refusals = %q, want %q", tc.repo, tc.number, refusalCodes(got), tc.want)
		}
		for _, r := range got {
			if r.Remedy == "" || r.Detail == "" {
				t.Errorf("refusal %+v lacks a detail or remedy", r)
			}
		}
	}
}

func TestFormatQueueListEntry(t *testing.T) {
	if got := formatQueueListEntry(1, orchestrator.QueueEntry{Repo: "acme/app", IssueNumber: 9, Status: "pending"}); got != "1. #9 (acme/app)" {
		t.Errorf("pending line = %q", got)
	}
	got := formatQueueListEntry(2, orchestrator.QueueEntry{Repo: "acme/app", IssueNumber: 9,
		Status: orchestrator.QueueStatusDropped, DropReason: "not found on project board"})
	if got != "2. #9 (acme/app) — dropped: not found on project board" {
		t.Errorf("dropped line = %q", got)
	}
}

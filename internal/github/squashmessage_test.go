package github

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestSquashCommitMessage(t *testing.T) {
	wip := "wip(feature-dev): preserve work in progress\n\nRefs: #7\n\nNightgauge-WIP: feature-dev"
	cases := []struct {
		name     string
		msgs     []string
		wantBody string
	}{
		{"sole WIP commit", []string{wip}, ""},
		{"single real commit", []string{"fix: a\n\nwhy a"}, "why a"},
		{"WIP dropped among real", []string{"fix: a\n\nwhy a", wip, "test: b"}, "* fix: a\n\nwhy a\n\n* test: b"},
		{"no commits", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, b := SquashCommitMessage("Fix the thing", 7, tc.msgs)
			if h != "Fix the thing (#7)" {
				t.Errorf("headline = %q", h)
			}
			if b != tc.wantBody {
				t.Errorf("body = %q, want %q", b, tc.wantBody)
			}
			if strings.Contains(b, WIPCommitTrailer) {
				t.Errorf("body carries the WIP trailer: %q", b)
			}
		})
	}
}

func TestIsWIPPreservationMessage(t *testing.T) {
	if !IsWIPPreservationMessage("wip: x\n\nNightgauge-WIP: feature-dev") {
		t.Error("WIP commit not detected")
	}
	if IsWIPPreservationMessage("fix: mention Nightgauge-WIP trailer handling") {
		t.Error("headline mention misread as trailer")
	}
}

// rewordStatuses: detection, the re-read after the reword, then queued and
// merged (#2988).
func rewordStatuses() []map[string]any {
	return []map[string]any{
		prNode("OPEN", true, false, nil, ""),
		prNode("OPEN", true, false, nil, ""),
		prNode("OPEN", true, true, entry("AWAITING_CHECKS", "mg1"), ""),
		prNode("MERGED", true, false, nil, "landed1"),
	}
}

func nonPRTitled(qs *queueServer) *queueServer {
	qs.squashTitle, qs.squashMessage = "COMMIT_OR_PR_TITLE", "COMMIT_MESSAGES"
	return qs
}

func TestMergeQueue_SoleWIP_NonPRTitledDefaults_RewordsAndEnqueues(t *testing.T) {
	qs := nonPRTitled(&queueServer{statuses: rewordStatuses(), commitMessages: []string{wipMessage}})
	if _, err := newQueuePRService(t, qs).MergePRWithStrategy(context.Background(), "PR_1", "SQUASH"); err != nil {
		t.Fatalf("MergePRWithStrategy: %v", err)
	}
	msg, _ := qs.created["message"].(string)
	if headline, _, _ := strings.Cut(msg, "\n"); headline != "fix the thing" {
		t.Errorf("reworded headline = %q, want the PR title", headline)
	}
	if strings.Contains(msg, WIPCommitTrailer) {
		t.Errorf("reworded message %q carries the WIP trailer", msg)
	}
	if qs.created["tree"] != "treec0" || fmt.Sprint(qs.created["parents"]) != "[base1]" {
		t.Errorf("created = %v, want the WIP commit's tree and parent", qs.created)
	}
	want := map[string]any{"repo": "R_1", "name": "refs/heads/feat/42-x", "after": "reworded1", "before": "c0"}
	for k, v := range want {
		if qs.refUpdate[k] != v {
			t.Errorf("updateRefs %s = %v, want %v", k, qs.refUpdate[k], v)
		}
	}
	if got := strings.Join(qs.mutations, ","); got != "updateRefs,enqueuePullRequest" {
		t.Errorf("mutations = %s, want updateRefs then enqueue only", got)
	}
	if qs.patched != 0 {
		t.Errorf("patched = %d, repository settings must not change", qs.patched)
	}
	if qs.readsAtEnqueue < 2 {
		t.Errorf("enqueue after %d status reads, want a re-read after the reword", qs.readsAtEnqueue)
	}
}

// GitHub syncs a PR's head after updateRefs asynchronously: reads that still
// report the WIP head must be waited out, not guarded again (#2988).
func TestMergeQueue_SoleWIP_StaleHeadAfterReword_WaitsForNewHead(t *testing.T) {
	open := prNode("OPEN", true, false, nil, "")
	statuses := append([]map[string]any{open, open}, rewordStatuses()...)
	qs := nonPRTitled(&queueServer{statuses: statuses, commitMessages: []string{wipMessage}, headLag: 2})
	svc := newQueuePRService(t, qs)
	svc.QueueWait.ReadyPollInterval = time.Millisecond
	if _, err := svc.MergePRWithStrategy(context.Background(), "PR_1", "SQUASH"); err != nil {
		t.Fatalf("MergePRWithStrategy: %v", err)
	}
	if got := strings.Join(qs.mutations, ","); got != "updateRefs,enqueuePullRequest" {
		t.Errorf("mutations = %s, want one reword then enqueue", got)
	}
	if qs.headLag != 0 {
		t.Errorf("headLag = %d, want every stale read consumed before the enqueue", qs.headLag)
	}
}

func TestMergeQueue_RewordRefusals(t *testing.T) {
	cases := []struct {
		name  string
		qs    *queueServer
		cause string
	}{
		{"mixed", &queueServer{commitMessages: []string{"feat: real\n\nbody", wipMessage}}, "sole head commit"},
		{"cross-repo", &queueServer{commitMessages: []string{wipMessage}, crossRepo: true}, "fork"},
		{"create fails", &queueServer{commitMessages: []string{wipMessage}, createStatus: 422}, "create reworded commit"},
		{"cas lost", &queueServer{commitMessages: []string{wipMessage}, refsErr: "Expected ref to point at c0"}, "move feat/42-x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			qs := nonPRTitled(tc.qs)
			qs.statuses = rewordStatuses()
			_, err := newQueuePRService(t, qs).MergePRWithStrategy(context.Background(), "PR_1", "SQUASH")
			var mqe *MergeQueueError
			if !errors.As(err, &mqe) || mqe.Kind != MergeQueueSquashDefaults {
				t.Fatalf("err = %v, want squash_defaults MergeQueueError", err)
			}
			if !strings.Contains(err.Error(), tc.cause) {
				t.Errorf("error %q does not name %q", err, tc.cause)
			}
			if qs.enqueued != 0 || qs.patched != 0 {
				t.Errorf("enqueued=%d patched=%d, want neither", qs.enqueued, qs.patched)
			}
		})
	}
}

func TestMergeQueue_SoleWIP_NoCommitWriter_Refuses(t *testing.T) {
	src := PRSquashSource{Number: 42, Title: "t", HeadRefOid: "c0", HeadRefName: "b", RepoID: "R",
		Commits: []PRSquashCommit{{OID: "c0", Tree: "t0", Parents: []string{"p"}, Message: wipMessage}}}
	wip, ok := src.SoleWIP()
	if !ok {
		t.Fatal("SoleWIP = false for a sole head WIP commit")
	}
	_, err := NewMergeQueue(nil).rewordSoleWIP(context.Background(), src, wip, MergeQueueWaitOptions{})
	if err == nil || !strings.Contains(err.Error(), "no commit writer") {
		t.Fatalf("err = %v, want the missing writer named", err)
	}
}

package orchestrator

import (
	"strings"
	"sync"
	"testing"
)

// newDequeueExcludeFixture builds the minimum Scheduler DequeueIndependent
// needs: a workspace root for persistQueue, a non-nil issueSvc for
// refreshBlockerStates, and the maps the dispatcher reads.
func newDequeueExcludeFixture(t *testing.T, excludeLabels []string) *Scheduler {
	t.Helper()
	return &Scheduler{
		workspaceRoot: t.TempDir(),
		repoRunning:   make(map[string]int),
		mergeLocks:    make(map[string]*sync.Mutex),
		issueSvc:      newMockIssueSvc(),
		excludeLabels: excludeLabels,
		// Room for every fixture item; the per-repo cap defaults to 1 and
		// would otherwise mask what these tests are measuring.
		maxPerRepo: 10,
	}
}

// TestDequeueIndependent_SkipsExcludedLabelWhateverRouteEnqueuedIt is the
// core #1146 regression. exclude_labels used to be enforced only on the two
// enqueue routes (the autonomous candidate loop and EnqueueEpic), so an item
// that reached the queue by any other route — here QueueAddItem, the call
// `queue.add` over IPC, the dashboard trigger, epic drag, manual "Add to
// Queue" and retry all funnel into — was dispatched regardless of its labels.
// DequeueIndependent is the chokepoint where a slot is actually claimed, so
// the guard there covers every route.
func TestDequeueIndependent_SkipsExcludedLabelWhateverRouteEnqueuedIt(t *testing.T) {
	s := newDequeueExcludeFixture(t, []string{"owner-action"})

	// Enqueue through the un-filtered route, exactly as IPC queue.add /
	// dashboard trigger / retry do.
	s.QueueAddItem(
		QueueItem{Repo: "Org/repo", IssueNumber: 401, Title: "Rotate the leaked token", Labels: []string{"owner-action"}},
		QueueItem{Repo: "Org/repo", IssueNumber: 402, Title: "Ordinary work", Labels: []string{"type:bug"}},
	)

	got := s.DequeueIndependent(t.Context(), 5, nil)

	for _, item := range got {
		if item.IssueNumber == 401 {
			t.Fatalf("dequeued #401 for dispatch despite its human-only label %q", "owner-action")
		}
	}
	if len(got) != 1 || got[0].IssueNumber != 402 {
		t.Fatalf("dequeued %+v, want exactly #402", got)
	}

	// Held, not discarded: the operator who queued it can still see it and
	// why it is not running.
	if len(s.queue) != 2 {
		t.Fatalf("queue has %d items, want 2 (the excluded item is held, not dropped)", len(s.queue))
	}
	var excluded *QueueItem
	for i := range s.queue {
		if s.queue[i].IssueNumber == 401 {
			excluded = &s.queue[i]
		}
	}
	if excluded == nil {
		t.Fatal("excluded item #401 was removed from the queue; want it held as paused")
	}
	if excluded.Status != "paused" {
		t.Errorf("excluded item status = %q, want %q", excluded.Status, "paused")
	}
	if excluded.PausedReason == nil {
		t.Fatal("excluded item has no PausedReason")
	}
	if excluded.PausedReason.Kind != "excluded_label" {
		t.Errorf("PausedReason.Kind = %q, want %q", excluded.PausedReason.Kind, "excluded_label")
	}
	if !strings.Contains(excluded.PausedReason.Summary, "owner-action") {
		t.Errorf("PausedReason.Summary = %q, want it to name the matched label", excluded.PausedReason.Summary)
	}
	// The label is carried structurally as well as in the prose summary, so
	// readers (the queue tree, the dashboard card) can name it without
	// parsing the summary.
	if excluded.PausedReason.Label != "owner-action" {
		t.Errorf("PausedReason.Label = %q, want %q", excluded.PausedReason.Label, "owner-action")
	}
}

// TestDequeueIndependent_LogsExcludedLabelSkip pins the operator-visible
// signal: the skip names both the issue and the label that matched, in the
// style of the prioritize / EnqueueEpic messages.
func TestDequeueIndependent_LogsExcludedLabelSkip(t *testing.T) {
	s := newDequeueExcludeFixture(t, []string{"owner-action"})
	s.QueueAddItem(QueueItem{Repo: "Org/repo", IssueNumber: 401, Title: "Rotate the leaked token", Labels: []string{"owner-action"}})

	out := captureLog(t, func() {
		s.DequeueIndependent(t.Context(), 5, nil)
	})
	if !strings.Contains(out, "#401") {
		t.Errorf("log %q does not name the skipped issue #401", out)
	}
	if !strings.Contains(out, `"owner-action"`) {
		t.Errorf("log %q does not name the matched label", out)
	}
	if !strings.Contains(out, "autonomous.exclude_labels") {
		t.Errorf("log %q does not name the config key that caused the skip", out)
	}

	// Second cycle: the item is now paused, so the paused guard short-circuits
	// before the label guard and the skip is not re-logged every cycle.
	second := captureLog(t, func() {
		s.DequeueIndependent(t.Context(), 5, nil)
	})
	if strings.Contains(second, "#401") {
		t.Errorf("skip was re-logged on a later cycle: %q", second)
	}
}

// TestDequeueIndependent_ExcludedLabelMatchIsCaseInsensitive covers both
// halves of the resolution contract: matching ignores case, and an unset
// config resolves through resolvedExcludeLabels to the existing
// ["owner-action"] default rather than disabling the guard.
func TestDequeueIndependent_ExcludedLabelMatchIsCaseInsensitive(t *testing.T) {
	t.Run("case-insensitive", func(t *testing.T) {
		s := newDequeueExcludeFixture(t, []string{"Owner-Action"})
		s.QueueAddItem(QueueItem{Repo: "Org/repo", IssueNumber: 401, Labels: []string{"OWNER-action"}})

		if got := s.DequeueIndependent(t.Context(), 5, nil); len(got) != 0 {
			t.Fatalf("dequeued %+v, want none (label differs only by case)", got)
		}
	})

	t.Run("unset config resolves to the default", func(t *testing.T) {
		s := newDequeueExcludeFixture(t, nil)
		s.QueueAddItem(QueueItem{Repo: "Org/repo", IssueNumber: 401, Labels: []string{"owner-action"}})

		if got := s.DequeueIndependent(t.Context(), 5, nil); len(got) != 0 {
			t.Fatalf("dequeued %+v, want none (unset config must resolve to %v)", got, defaultExcludeLabels)
		}
		// ExcludeLabels() is what the CLI `queue add` and the IPC door check
		// read. NewScheduler always resolves, so this only bites a Scheduler
		// built as a struct literal (tests): before #1146 it handed callers the
		// raw, unresolved field and an empty list disables the check entirely.
		got := s.ExcludeLabels()
		if len(got) == 0 || got[0] != defaultExcludeLabels[0] {
			t.Errorf("ExcludeLabels() = %v, want the resolved default %v", got, defaultExcludeLabels)
		}
	})

	t.Run("configured list replaces the default", func(t *testing.T) {
		s := newDequeueExcludeFixture(t, []string{"needs-human"})
		s.QueueAddItem(
			QueueItem{Repo: "Org/repo", IssueNumber: 401, Labels: []string{"owner-action"}},
			QueueItem{Repo: "Org/repo", IssueNumber: 402, Labels: []string{"needs-human"}},
		)

		got := s.DequeueIndependent(t.Context(), 5, nil)
		if len(got) != 1 || got[0].IssueNumber != 401 {
			t.Fatalf("dequeued %+v, want exactly #401 (owner-action is not in the configured list)", got)
		}
	})
}

// TestDequeueIndependent_DequeuesUnlabeledItems proves the guard is not
// over-broad: items with no labels, or with labels that are not excluded, are
// dequeued exactly as before.
func TestDequeueIndependent_DequeuesUnlabeledItems(t *testing.T) {
	s := newDequeueExcludeFixture(t, []string{"owner-action"})
	s.QueueAddItem(
		QueueItem{Repo: "Org/repo", IssueNumber: 401},
		QueueItem{Repo: "Org/repo", IssueNumber: 402, Labels: []string{}},
		QueueItem{Repo: "Org/repo", IssueNumber: 403, Labels: []string{"type:bug", "owner-actionable"}},
	)

	got := s.DequeueIndependent(t.Context(), 5, nil)
	if len(got) != 3 {
		t.Fatalf("dequeued %d items, want 3 (none carries an excluded label): %+v", len(got), got)
	}
	for _, item := range got {
		if item.Status != "processing" {
			t.Errorf("#%d dequeued with status %q, want %q", item.IssueNumber, item.Status, "processing")
		}
	}
	for _, item := range s.queue {
		if item.PausedReason != nil {
			t.Errorf("#%d was paused by the label guard; want untouched", item.IssueNumber)
		}
	}
}

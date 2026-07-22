package orchestrator

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/depgraph"
	"github.com/nightgauge/nightgauge/internal/notify"
	"github.com/nightgauge/nightgauge/internal/state"
)

// buildEpicGraph wires an epic (#142) with the given sub-issue nodes and
// blockedBy edges. blockers maps a sub number -> the numbers it is blocked by.
func buildEpicGraph(epicStatus string, subs []*depgraph.Node, blockers map[int][]int) *depgraph.Graph {
	g := depgraph.NewGraph()
	g.AddNode(&depgraph.Node{
		Repo: "o/r", Number: 142, Title: "Epic", State: "OPEN",
		BoardStatus: epicStatus, Labels: []string{"type:epic"},
	})
	for _, s := range subs {
		if s.Repo == "" {
			s.Repo = "o/r"
		}
		if s.EpicNumber == 0 {
			s.EpicNumber = 142
		}
		g.AddNode(s)
	}
	for from, tos := range blockers {
		for _, to := range tos {
			g.AddEdge(depgraph.Edge{
				From: depgraph.NodeID{Repo: "o/r", Number: from},
				To:   depgraph.NodeID{Repo: "o/r", Number: to},
				Type: "blockedBy",
			})
		}
	}
	return g
}

func noRecovery(string, int) bool { return false }
func noReason(string, int) string { return "" }

func TestStuckEpics_AllBlocked_IsStuck(t *testing.T) {
	// #143 is in progress with no run; #144 is blocked by the open #143.
	g := buildEpicGraph("In progress",
		[]*depgraph.Node{
			{Number: 143, Title: "Sub A", State: "OPEN", BoardStatus: "In progress"},
			{Number: 144, Title: "Sub B", State: "OPEN", BoardStatus: "Backlog"},
		},
		map[int][]int{144: {143}},
	)

	got := stuckEpicsFromGraph(g, stuckEpicScanOpts{
		now: time.Unix(1_700_000_000, 0), runningSet: map[string]bool{},
		isRecovering: noRecovery, failureReason: noReason,
	})
	if len(got) != 1 {
		t.Fatalf("want 1 stuck epic, got %d (%+v)", len(got), got)
	}
	e := got[0]
	if e.Number != 142 || len(e.Blockers) != 2 {
		t.Fatalf("unexpected epic %+v", e)
	}
	// #144 must name its open blocker #143.
	var b144 StuckBlocker
	for _, b := range e.Blockers {
		if b.Number == 144 {
			b144 = b
		}
	}
	if b144.Reason == "" || !strings.Contains(b144.Reason, "#143") {
		t.Errorf("#144 reason must name blocker #143, got %q", b144.Reason)
	}
}

func TestStuckEpics_RecoveringSub_NotStuck(t *testing.T) {
	g := buildEpicGraph("In progress",
		[]*depgraph.Node{
			{Number: 143, Title: "Sub A", State: "OPEN", BoardStatus: "In progress"},
		}, nil)

	got := stuckEpicsFromGraph(g, stuckEpicScanOpts{
		now:        time.Unix(1_700_000_000, 0),
		runningSet: map[string]bool{},
		isRecovering: func(_ string, n int) bool { return n == 143 }, // #143 mid-recovery
		failureReason: noReason,
	})
	if len(got) != 0 {
		t.Fatalf("an actively-recovering sub-issue must keep the epic NOT stuck, got %+v", got)
	}
}

func TestStuckEpics_EligibleSub_NotStuck(t *testing.T) {
	// #143 is Ready and unblocked → dispatchable → epic is progressing.
	g := buildEpicGraph("In progress",
		[]*depgraph.Node{
			{Number: 143, Title: "Sub A", State: "OPEN", BoardStatus: "Ready"},
		}, nil)

	got := stuckEpicsFromGraph(g, stuckEpicScanOpts{
		now: time.Unix(1_700_000_000, 0), runningSet: map[string]bool{},
		isRecovering: noRecovery, failureReason: noReason,
	})
	if len(got) != 0 {
		t.Fatalf("a Ready+unblocked sub must keep the epic NOT stuck, got %+v", got)
	}
}

func TestStuckEpics_RunningSub_NotStuck(t *testing.T) {
	g := buildEpicGraph("In progress",
		[]*depgraph.Node{
			{Number: 143, Title: "Sub A", State: "OPEN", BoardStatus: "In progress"},
		}, nil)

	got := stuckEpicsFromGraph(g, stuckEpicScanOpts{
		now:          time.Unix(1_700_000_000, 0),
		runningSet:   map[string]bool{"o/r#143": true},
		isRecovering: noRecovery, failureReason: noReason,
	})
	if len(got) != 0 {
		t.Fatalf("a running sub must keep the epic NOT stuck, got %+v", got)
	}
}

func TestStuckEpics_AllSubsClosed_NotStuck(t *testing.T) {
	g := buildEpicGraph("In progress",
		[]*depgraph.Node{
			{Number: 143, Title: "Sub A", State: "CLOSED", BoardStatus: "Done"},
		}, nil)

	got := stuckEpicsFromGraph(g, stuckEpicScanOpts{
		now: time.Unix(1_700_000_000, 0), runningSet: map[string]bool{},
		isRecovering: noRecovery, failureReason: noReason,
	})
	if len(got) != 0 {
		t.Fatalf("no open sub-issues → not this watchdog's concern, got %+v", got)
	}
}

func TestStuckEpics_FailureReasonFromHistory(t *testing.T) {
	g := buildEpicGraph("In progress",
		[]*depgraph.Node{
			{Number: 143, Title: "Sub A", State: "OPEN", BoardStatus: "In progress"},
		}, nil)

	got := stuckEpicsFromGraph(g, stuckEpicScanOpts{
		now: time.Unix(1_700_000_000, 0), runningSet: map[string]bool{},
		isRecovering: noRecovery,
		failureReason: func(_ string, n int) string {
			if n == 143 {
				return "merge failed: PR conflicting"
			}
			return ""
		},
	})
	if len(got) != 1 || len(got[0].Blockers) != 1 {
		t.Fatalf("unexpected: %+v", got)
	}
	if !strings.Contains(got[0].Blockers[0].Reason, "merge failed: PR conflicting") {
		t.Errorf("blocker reason must include the history failure, got %q", got[0].Blockers[0].Reason)
	}
}

func TestRunRecordIsRecovering(t *testing.T) {
	now := time.Now().UTC()
	recent := now.Add(-5 * time.Minute).Format(time.RFC3339)
	old := now.Add(-2 * time.Hour).Format(time.RFC3339)

	// Recent conflict-recovery attempt → recovering.
	rec := &state.V2RunRecord{
		IssueNumber: 143, CompletedAt: recent,
		Stages: map[string]state.V2StageDetail{
			"pr-merge": {RecoveryAttempts: []state.RecoveryAttempt{{Action: "conflict-recovery-loop"}}},
		},
	}
	if !runRecordIsRecovering(rec) {
		t.Error("recent conflict-recovery attempt must count as recovering")
	}

	// Same attempt but old → settled (not recovering).
	rec.CompletedAt = old
	if runRecordIsRecovering(rec) {
		t.Error("an old recovery run must NOT count as actively recovering")
	}

	// Recent but no recovery markers → not recovering.
	if runRecordIsRecovering(&state.V2RunRecord{IssueNumber: 143, CompletedAt: recent}) {
		t.Error("a plain run is not recovering")
	}
}

func TestLatestRunRecordFromDir(t *testing.T) {
	dir := t.TempDir()
	// Two records for #143; the later CompletedAt must win.
	older := `{"issue_number":143,"repo":"o/r","completed_at":"2026-06-01T10:00:00Z","outcome":"failed","terminal_failure_kind":"stall_kill"}`
	newer := `{"issue_number":143,"repo":"o/r","completed_at":"2026-06-02T10:00:00Z","outcome":"failed","terminal_failure_kind":"validation_error"}`
	other := `{"issue_number":999,"repo":"o/r","completed_at":"2026-06-03T10:00:00Z","outcome":"success"}`
	if err := os.WriteFile(filepath.Join(dir, "2026-06-02.jsonl"), []byte(older+"\n"+newer+"\n"+other+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	rec, ok := latestRunRecordFromDir(dir, "o/r", 143)
	if !ok {
		t.Fatal("expected a record for #143")
	}
	if rec.TerminalFailureKind != "validation_error" {
		t.Errorf("latest record should win, got %q", rec.TerminalFailureKind)
	}

	if _, ok := latestRunRecordFromDir(dir, "o/r", 12345); ok {
		t.Error("missing issue must return ok=false")
	}
}

// TestDetectStuckEpics_RetryBackoffExcludes verifies the scheduler-level method
// treats an issue with a pending retry (in-memory backoff) as actively
// recovering — so the epic is NOT flagged as stuck (#4073).
func TestDetectStuckEpics_RetryBackoffExcludes(t *testing.T) {
	g := buildEpicGraph("In progress",
		[]*depgraph.Node{
			{Number: 143, Title: "Sub A", State: "OPEN", BoardStatus: "In progress"},
		}, nil)

	as := &AutonomousScheduler{
		state:                    &AutonomousState{},
		config:                   AutonomousConfig{StuckEpicDetectionEnabled: true},
		retryBackoff:             map[string]time.Time{"o/r#143": time.Now().Add(2 * time.Minute)},
		inReviewRecoveryAttempts: map[string]int{},
		conflictRestartCount:     map[string]int{},
		stuckEpicHistoryFn:       func(string, int) (*state.V2RunRecord, bool) { return nil, false },
	}

	if got := as.detectStuckEpics(g); len(got) != 0 {
		t.Fatalf("a pending-retry sub must keep the epic NOT stuck, got %+v", got)
	}

	// Expire the backoff → now genuinely stuck.
	as.retryBackoff["o/r#143"] = time.Now().Add(-time.Minute)
	if got := as.detectStuckEpics(g); len(got) != 1 {
		t.Fatalf("expired backoff → epic must be stuck, got %d", len(got))
	}
}

// TestDetectStuckEpics_HistoryRecoveryExcludes verifies a recent
// conflict-recovery run (from history) keeps the epic NOT stuck.
func TestDetectStuckEpics_HistoryRecoveryExcludes(t *testing.T) {
	g := buildEpicGraph("In progress",
		[]*depgraph.Node{{Number: 143, Title: "Sub A", State: "OPEN", BoardStatus: "In progress"}}, nil)

	recent := time.Now().UTC().Add(-3 * time.Minute).Format(time.RFC3339)
	as := &AutonomousScheduler{
		state:                    &AutonomousState{},
		config:                   AutonomousConfig{StuckEpicDetectionEnabled: true},
		retryBackoff:             map[string]time.Time{},
		inReviewRecoveryAttempts: map[string]int{},
		conflictRestartCount:     map[string]int{},
		stuckEpicHistoryFn: func(_ string, n int) (*state.V2RunRecord, bool) {
			return &state.V2RunRecord{
				IssueNumber: n, CompletedAt: recent,
				Stages: map[string]state.V2StageDetail{
					"pr-merge": {RecoveryAttempts: []state.RecoveryAttempt{{Action: "branch-out-of-date"}}},
				},
			}, true
		},
	}
	if got := as.detectStuckEpics(g); len(got) != 0 {
		t.Fatalf("a recent branch-out-of-date recovery must keep the epic NOT stuck, got %+v", got)
	}
}

func TestAlertStuckEpics_DeDup(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	as := &AutonomousScheduler{
		config: AutonomousConfig{
			StuckEpicWebhookURL:   srv.URL,
			StuckEpicReAlertAfter: time.Hour,
		},
		alertedStuckEpics: map[string]time.Time{},
	}
	epics := []StuckEpic{{Repo: "o/r", Number: 142, Title: "Epic", Blockers: []StuckBlocker{{Number: 143, Reason: "in progress with no active run"}}}}

	as.alertStuckEpics(context.Background(), epics)
	as.alertStuckEpics(context.Background(), epics) // within cooldown → suppressed

	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("expected exactly 1 alert (de-dup within cooldown), got %d", got)
	}
}

// TestStuckEpics_CrossRepoNumberCollision locks the #4073 review HIGH fix: two
// epics that REUSE issue number #142 in different repos must be detected as TWO
// distinct stalled epics with correctly-attributed blockers — never merged or
// shadowed by the bare-number index.
func TestStuckEpics_CrossRepoNumberCollision(t *testing.T) {
	g := depgraph.NewGraph()
	for _, repo := range []string{"o/A", "o/B"} {
		g.AddNode(&depgraph.Node{Repo: repo, Number: 142, Title: "Epic " + repo, State: "OPEN", BoardStatus: "In progress", Labels: []string{"type:epic"}})
		g.AddNode(&depgraph.Node{Repo: repo, Number: 143, Title: "Sub " + repo, State: "OPEN", BoardStatus: "In progress", EpicNumber: 142})
	}

	got := stuckEpicsFromGraph(g, stuckEpicScanOpts{
		now: time.Unix(1_700_000_000, 0), runningSet: map[string]bool{},
		isRecovering: noRecovery, failureReason: noReason,
	})
	if len(got) != 2 {
		t.Fatalf("two same-numbered cross-repo epics must yield 2 stuck epics, got %d (%+v)", len(got), got)
	}
	for _, e := range got {
		if len(e.Blockers) != 1 {
			t.Fatalf("%s#%d should have exactly its OWN sub, got %+v", e.Repo, e.Number, e.Blockers)
		}
		if !strings.Contains(e.Blockers[0].Title, e.Repo) {
			t.Errorf("%s blocker misattributed: %q", e.Repo, e.Blockers[0].Title)
		}
	}
}

// TestStuckEpics_CrossRepoEligibleDoesNotSuppress: a Ready+unblocked sub in repo
// B must NOT suppress a genuinely-stuck same-numbered epic in repo A.
func TestStuckEpics_CrossRepoEligibleDoesNotSuppress(t *testing.T) {
	g := depgraph.NewGraph()
	g.AddNode(&depgraph.Node{Repo: "o/A", Number: 142, Title: "Epic A", State: "OPEN", BoardStatus: "In progress", Labels: []string{"type:epic"}})
	g.AddNode(&depgraph.Node{Repo: "o/A", Number: 143, Title: "Sub A", State: "OPEN", BoardStatus: "In progress", EpicNumber: 142})
	g.AddNode(&depgraph.Node{Repo: "o/B", Number: 142, Title: "Epic B", State: "OPEN", BoardStatus: "In progress", Labels: []string{"type:epic"}})
	g.AddNode(&depgraph.Node{Repo: "o/B", Number: 143, Title: "Sub B", State: "OPEN", BoardStatus: "Ready", EpicNumber: 142})

	got := stuckEpicsFromGraph(g, stuckEpicScanOpts{
		now: time.Unix(1_700_000_000, 0), runningSet: map[string]bool{},
		isRecovering: noRecovery, failureReason: noReason,
	})
	if len(got) != 1 || got[0].Repo != "o/A" {
		t.Fatalf("only epic A must be stuck (B is progressing), got %+v", got)
	}
}

// TestStuckEpics_TodoStatusDispatchable: a "Todo"/"To Do" sub is dispatchable per
// the real gate, so the epic must NOT be flagged stuck (#4073 review).
func TestStuckEpics_TodoStatusDispatchable(t *testing.T) {
	for _, status := range []string{"Todo", "To Do"} {
		g := buildEpicGraph("In progress",
			[]*depgraph.Node{{Number: 143, Title: "Sub A", State: "OPEN", BoardStatus: status}}, nil)
		got := stuckEpicsFromGraph(g, stuckEpicScanOpts{
			now: time.Unix(1_700_000_000, 0), runningSet: map[string]bool{},
			isRecovering: noRecovery, failureReason: noReason,
		})
		if len(got) != 0 {
			t.Errorf("a %q sub is dispatchable → epic NOT stuck, got %+v", status, got)
		}
	}
}

// TestStuckEpics_InReviewDepNotBlocking: a sub blocked only by an "In review" dep
// is dispatchable (the dep's PR is up), so the epic must NOT be flagged stuck.
func TestStuckEpics_InReviewDepNotBlocking(t *testing.T) {
	g := buildEpicGraph("In progress",
		[]*depgraph.Node{
			{Number: 143, Title: "Sub A", State: "OPEN", BoardStatus: "In review"},
			{Number: 144, Title: "Sub B", State: "OPEN", BoardStatus: "Ready"},
		},
		map[int][]int{144: {143}},
	)
	got := stuckEpicsFromGraph(g, stuckEpicScanOpts{
		now: time.Unix(1_700_000_000, 0), runningSet: map[string]bool{},
		isRecovering: noRecovery, failureReason: noReason,
	})
	if len(got) != 0 {
		t.Fatalf("#144 blocked only by an In-review dep is dispatchable → NOT stuck, got %+v", got)
	}
}

// TestStuckEpics_EpicCascadeBlocksSub: a Ready unblocked sub whose parent epic is
// itself blocked by an open upstream epic is NOT dispatchable (cascade), so the
// epic IS stuck — matching the real dispatcher.
func TestStuckEpics_EpicCascadeBlocksSub(t *testing.T) {
	g := depgraph.NewGraph()
	g.AddNode(&depgraph.Node{Repo: "o/r", Number: 100, Title: "Upstream epic", State: "OPEN", BoardStatus: "In progress", Labels: []string{"type:epic"}})
	g.AddNode(&depgraph.Node{Repo: "o/r", Number: 142, Title: "Epic", State: "OPEN", BoardStatus: "In progress", Labels: []string{"type:epic"}})
	g.AddNode(&depgraph.Node{Repo: "o/r", Number: 143, Title: "Sub", State: "OPEN", BoardStatus: "Ready", EpicNumber: 142})
	// epic #142 blockedBy upstream epic #100 (open).
	g.AddEdge(depgraph.Edge{From: depgraph.NodeID{Repo: "o/r", Number: 142}, To: depgraph.NodeID{Repo: "o/r", Number: 100}, Type: "blockedBy"})

	got := stuckEpicsFromGraph(g, stuckEpicScanOpts{
		now: time.Unix(1_700_000_000, 0), runningSet: map[string]bool{},
		isRecovering: noRecovery, failureReason: noReason,
	})
	// Epic #142's only sub is cascade-blocked → #142 is stuck.
	found142 := false
	for _, e := range got {
		if e.Number == 142 {
			found142 = true
		}
	}
	if !found142 {
		t.Errorf("epic #142 (sub cascade-blocked by upstream epic #100) must be flagged stuck, got %+v", got)
	}

	// With the cascade DISABLED, the Ready sub is dispatchable → #142 not stuck.
	got2 := stuckEpicsFromGraph(g, stuckEpicScanOpts{
		now: time.Unix(1_700_000_000, 0), runningSet: map[string]bool{}, disableEpicCascade: true,
		isRecovering: noRecovery, failureReason: noReason,
	})
	for _, e := range got2 {
		if e.Number == 142 {
			t.Errorf("with cascade disabled, #142 must NOT be stuck, got %+v", got2)
		}
	}
}

// TestParseRecordTime locks the deterministic tie-break: completed_at preferred,
// recorded_at fallback, ok=false when neither parses.
func TestParseRecordTime(t *testing.T) {
	if tm, ok := parseRecordTime("2026-06-02T10:00:00Z", "2026-06-01T10:00:00Z"); !ok || tm.Day() != 2 {
		t.Errorf("completed_at must win, got %v ok=%v", tm, ok)
	}
	if tm, ok := parseRecordTime("", "2026-06-01T10:00:00Z"); !ok || tm.Day() != 1 {
		t.Errorf("recorded_at fallback failed, got %v ok=%v", tm, ok)
	}
	if _, ok := parseRecordTime("", ""); ok {
		t.Error("no timestamps must return ok=false")
	}
	if _, ok := parseRecordTime("garbage", "also-bad"); ok {
		t.Error("unparseable timestamps must return ok=false")
	}
}

// TestAlertStuckEpics_NoCooldownOnFailure locks the #4073 review fix: a failed
// Discord POST must NOT arm the re-alert cooldown, so the next cycle retries.
func TestAlertStuckEpics_NoCooldownOnFailure(t *testing.T) {
	var fail int32 = 1
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.LoadInt32(&fail) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	old := notify.RetryDelay
	notify.RetryDelay = 0
	defer func() { notify.RetryDelay = old }()

	as := &AutonomousScheduler{
		config:            AutonomousConfig{StuckEpicWebhookURL: srv.URL, StuckEpicReAlertAfter: time.Hour},
		alertedStuckEpics: map[string]time.Time{},
	}
	epics := []StuckEpic{{Repo: "o/r", Number: 142, Title: "Epic"}}

	as.alertStuckEpics(context.Background(), epics) // server returns 500 → fails
	if len(as.alertedStuckEpics) != 0 {
		t.Fatalf("failed delivery must not arm the cooldown, got %v", as.alertedStuckEpics)
	}

	atomic.StoreInt32(&fail, 0)              // server now succeeds
	as.alertStuckEpics(context.Background(), epics)
	if _, ok := as.alertedStuckEpics["o/r#142"]; !ok {
		t.Error("successful delivery must arm the cooldown")
	}
}

// TestStuckEpics_EpicCascadeReason locks the round-2 review fix: a Ready sub held
// back only by its parent epic's blocker is described as "(via epic #N)", not
// mislabeled "ready but undispatched".
func TestStuckEpics_EpicCascadeReason(t *testing.T) {
	g := depgraph.NewGraph()
	g.AddNode(&depgraph.Node{Repo: "o/r", Number: 100, Title: "Upstream", State: "OPEN", BoardStatus: "In progress", Labels: []string{"type:epic"}})
	g.AddNode(&depgraph.Node{Repo: "o/r", Number: 142, Title: "Epic", State: "OPEN", BoardStatus: "In progress", Labels: []string{"type:epic"}})
	g.AddNode(&depgraph.Node{Repo: "o/r", Number: 143, Title: "Sub", State: "OPEN", BoardStatus: "Ready", EpicNumber: 142})
	g.AddEdge(depgraph.Edge{From: depgraph.NodeID{Repo: "o/r", Number: 142}, To: depgraph.NodeID{Repo: "o/r", Number: 100}, Type: "blockedBy"})

	got := stuckEpicsFromGraph(g, stuckEpicScanOpts{
		now: time.Unix(1_700_000_000, 0), runningSet: map[string]bool{},
		isRecovering: noRecovery, failureReason: noReason,
	})
	var reason string
	for _, e := range got {
		if e.Number == 142 {
			for _, b := range e.Blockers {
				if b.Number == 143 {
					reason = b.Reason
				}
			}
		}
	}
	if !strings.Contains(reason, "via epic #142") || !strings.Contains(reason, "#100") {
		t.Errorf("cascade-blocked sub reason must cite the epic blocker, got %q", reason)
	}
}

// TestAlertStuckEpics_PartialBatchArmsDelivered: when batch 1 lands but batch 2
// fails, the cooldown must be armed ONLY for the delivered epics (the failed
// batch re-alerts next cycle) (#4073 round-2 review).
func TestAlertStuckEpics_PartialBatchArmsDelivered(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&hits, 1) == 1 {
			w.WriteHeader(http.StatusNoContent) // batch 1 (10) lands
			return
		}
		w.WriteHeader(http.StatusInternalServerError) // batch 2 fails
	}))
	defer srv.Close()
	old := notify.RetryDelay
	notify.RetryDelay = 0
	defer func() { notify.RetryDelay = old }()

	as := &AutonomousScheduler{
		config:            AutonomousConfig{StuckEpicWebhookURL: srv.URL, StuckEpicReAlertAfter: time.Hour},
		alertedStuckEpics: map[string]time.Time{},
	}
	epics := make([]StuckEpic, 13) // → batch 1 (10) + batch 2 (3, fails)
	for i := range epics {
		epics[i] = StuckEpic{Repo: "o/r", Number: 1000 + i, Title: "Epic"}
	}

	as.alertStuckEpics(context.Background(), epics)
	if len(as.alertedStuckEpics) != 10 {
		t.Errorf("only the 10 delivered epics must be armed, got %d", len(as.alertedStuckEpics))
	}
}

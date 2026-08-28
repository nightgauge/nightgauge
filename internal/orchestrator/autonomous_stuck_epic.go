package orchestrator

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/depgraph"
	"github.com/nightgauge/nightgauge/internal/notify"
	"github.com/nightgauge/nightgauge/internal/state"
)

// StuckEpic describes an epic that is OPEN with open sub-issues but has zero
// eligible (unblocked, dispatchable) work, no running pipeline, and no sub-issue
// actively recovering — the "idle looks like done" silent stall (#4073). An epic
// in this state is indistinguishable from a finished epic without this watchdog.
type StuckEpic struct {
	Repo       string         `json:"repo"`
	Number     int            `json:"number"`
	Title      string         `json:"title"`
	DetectedAt string         `json:"detectedAt"`
	Blockers   []StuckBlocker `json:"blockers"`
}

// StuckBlocker names one open sub-issue holding the epic back and why.
type StuckBlocker struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Reason string `json:"reason"`
}

// Summary renders a one-line human description, e.g.
// "epic #142 stalled: #143 in progress with no active run; #144 blocked by #143".
func (e StuckEpic) Summary() string {
	parts := make([]string, 0, len(e.Blockers))
	for _, b := range e.Blockers {
		parts = append(parts, fmt.Sprintf("#%d %s", b.Number, b.Reason))
	}
	return fmt.Sprintf("epic #%d stalled: %s", e.Number, strings.Join(parts, "; "))
}

// stuckEpicScanOpts carries the per-scan inputs the pure detector needs so it can
// be unit-tested without a live scheduler.
type stuckEpicScanOpts struct {
	now                time.Time
	runningSet         map[string]bool                      // "repo#number" currently running
	pickupBacklog      bool                                 // Backlog+unblocked counts as eligible
	disableEpicCascade bool                                 // honor autonomous.disable_epic_blockedby_cascade
	isRecovering       func(repo string, number int) bool   // actively recovering → not stuck
	failureReason      func(repo string, number int) string // best-effort last-run reason for the alert

	// prEvidence answers whether an "In review" sub-issue actually has an open
	// PR behind it. THREE-state on purpose (#265): the board status "In review"
	// is overloaded — the PR-review phase and the architecture-approval gate
	// both land an issue there — so rendering "PR open, awaiting merge" from
	// the status alone sends the operator hunting for a PR that may not exist.
	// nil means no evidence source was wired, which reads as prUnverified
	// rather than as an assertion either way.
	prEvidence func(repo string, number int) prVerdict

	// awaitingArchApproval reports that the issue's last run halted on the
	// architecture-approval gate — the other, opposite-action cause of an "In
	// review" status. nil means "cannot tell", never "no".
	awaitingArchApproval func(repo string, number int) bool
}

// prVerdict is the three-state answer to "does this issue have an open PR?".
// The third state exists because #265 shipped a card that stated the answer
// confidently without ever asking the question; a diagnostic that cannot tell
// "no" from "I did not look" must say so rather than pick the likelier story.
type prVerdict int

const (
	// prUnverified means no successful PR lookup covered this issue — the
	// repo's PR query failed, or no evidence source was wired. Never render
	// this as either the presence or the absence of a PR.
	prUnverified prVerdict = iota
	// prOpen means a PR listing succeeded and found an OPEN PR for the issue.
	prOpen
	// prAbsent means a PR listing succeeded and found no OPEN PR for the issue.
	prAbsent
)

// stuckEpicsFromGraph is the pure detector. It returns one StuckEpic per OPEN
// epic that has at least one open sub-issue, NO sub-issue that is eligible
// (Ready/Backlog-pickup AND unblocked), running, or actively recovering. The
// blockers list explains why each open sub-issue is held back.
func stuckEpicsFromGraph(graph *depgraph.Graph, o stuckEpicScanOpts) []StuckEpic {
	if graph == nil {
		return nil
	}
	adj := graph.Adjacency() // blocked -> [blockers]

	// Index epic nodes AND group sub-issues by REPO-SCOPED key ("repo#number"),
	// not bare issue number — issue numbers repeat across repos, so a bare-number
	// index would merge or shadow distinct epics in a multi-repo workspace
	// (#4073 review). Mirrors autonomous.go's NodeID{Repo,Number} epic linkage.
	epicByKey := make(map[string]*depgraph.Node)
	subsByEpic := make(map[string][]*depgraph.Node)
	for _, n := range graph.Nodes {
		if isEpicNode(n) {
			epicByKey[n.ID().String()] = n
		}
		if n.EpicNumber != 0 {
			ek := depgraph.NodeID{Repo: n.Repo, Number: n.EpicNumber}.String()
			subsByEpic[ek] = append(subsByEpic[ek], n)
		}
	}

	var stuck []StuckEpic
	for epicKey, epic := range epicByKey {
		if epic == nil || !strings.EqualFold(epic.State, "OPEN") {
			continue
		}
		subs := subsByEpic[epicKey]
		openSubs := make([]*depgraph.Node, 0, len(subs))
		for _, s := range subs {
			if strings.EqualFold(s.State, "OPEN") {
				openSubs = append(openSubs, s)
			}
		}
		if len(openSubs) == 0 {
			continue // no open work — completion check (not this watchdog) owns it
		}

		// An epic is NOT stuck if any open sub-issue is eligible, running, or
		// actively recovering — that is real or imminent progress.
		progressing := false
		for _, s := range openSubs {
			key := fmt.Sprintf("%s#%d", s.Repo, s.Number)
			if o.runningSet[key] {
				progressing = true
				break
			}
			if o.isRecovering != nil && o.isRecovering(s.Repo, s.Number) {
				progressing = true
				break
			}
			if dispatchable(s, adj, graph, o.pickupBacklog, o.disableEpicCascade) {
				progressing = true
				break
			}
		}
		if progressing {
			continue
		}

		// Stalled: explain each open sub-issue.
		sort.Slice(openSubs, func(i, j int) bool { return openSubs[i].Number < openSubs[j].Number })
		blockers := make([]StuckBlocker, 0, len(openSubs))
		for _, s := range openSubs {
			blockers = append(blockers, StuckBlocker{
				Number: s.Number,
				Title:  s.Title,
				Reason: blockerReasonFor(s, adj, graph, o),
			})
		}
		ts := ""
		if !o.now.IsZero() {
			ts = o.now.UTC().Format(time.RFC3339)
		}
		stuck = append(stuck, StuckEpic{
			Repo: epic.Repo, Number: epic.Number, Title: epic.Title,
			DetectedAt: ts, Blockers: blockers,
		})
	}

	sort.Slice(stuck, func(i, j int) bool {
		if stuck[i].Repo != stuck[j].Repo {
			return stuck[i].Repo < stuck[j].Repo
		}
		return stuck[i].Number < stuck[j].Number
	})
	return stuck
}

func isEpicNode(n *depgraph.Node) bool {
	if n == nil {
		return false
	}
	for _, l := range n.Labels {
		if strings.EqualFold(l, "type:epic") {
			return true
		}
	}
	return false
}

// dispatchable mirrors the REAL autonomous dispatch gate so the watchdog cannot
// drift from it (#4073 review). It reuses the package-local helpers
// isDispatchableStatus (Ready/Todo/To Do, or Backlog under pickup) and
// isWorkCompleteStatus (an "In review" dep — PR up, awaiting merge — does NOT
// block downstream work), and applies the same parent-epic blockedBy cascade the
// dispatcher does (unless disabled).
func dispatchable(n *depgraph.Node, adj map[string][]string, graph *depgraph.Graph, pickupBacklog, disableEpicCascade bool) bool {
	if n == nil || !strings.EqualFold(n.State, "OPEN") {
		return false
	}
	if !isDispatchableStatus(n.BoardStatus, pickupBacklog) {
		return false
	}
	if hasOpenBlocker(adj[n.ID().String()], graph) {
		return false
	}
	// Epic-level cascade: a sub is not dispatchable when its parent epic is OPEN
	// with an open, non-work-complete blocker — matching prioritize() in
	// autonomous.go (gated by the same DisableEpicBlockedByCascade config).
	if !disableEpicCascade && n.EpicNumber != 0 {
		epicKey := depgraph.NodeID{Repo: n.Repo, Number: n.EpicNumber}.String()
		if epic, ok := graph.Nodes[epicKey]; ok && strings.EqualFold(epic.State, "OPEN") {
			if hasOpenBlocker(adj[epicKey], graph) {
				return false
			}
		}
	}
	return true
}

// hasOpenBlocker reports whether any dependency in depKeys is OPEN and not
// work-complete ("In review" deps don't block downstream, matching the
// dispatcher's isWorkCompleteStatus skip).
func hasOpenBlocker(depKeys []string, graph *depgraph.Graph) bool {
	for _, depKey := range depKeys {
		dep, ok := graph.Nodes[depKey]
		if !ok || !strings.EqualFold(dep.State, "OPEN") {
			continue
		}
		if isWorkCompleteStatus(dep.BoardStatus) {
			continue
		}
		return true
	}
	return false
}

// openBlockerRefs returns the sorted "#N" refs of the OPEN, non-work-complete
// dependencies in depKeys (the same set hasOpenBlocker counts), for use in a
// human reason string.
func openBlockerRefs(depKeys []string, graph *depgraph.Graph) []string {
	var nums []int
	for _, depKey := range depKeys {
		dep, ok := graph.Nodes[depKey]
		if !ok || !strings.EqualFold(dep.State, "OPEN") || isWorkCompleteStatus(dep.BoardStatus) {
			continue
		}
		nums = append(nums, dep.Number)
	}
	if len(nums) == 0 {
		return nil
	}
	sort.Ints(nums)
	refs := make([]string, len(nums))
	for i, b := range nums {
		refs[i] = fmt.Sprintf("#%d", b)
	}
	return refs
}

// blockerReasonFor explains why an open sub-issue is not making progress. It
// prefers the concrete graph blocker (open dependency), then the last-run failure
// from history, then the board status. Takes the whole scan opts so the
// "In review" branch can consult PR evidence and the approval gate (#265)
// instead of inferring a cause from the overloaded status string.
func blockerReasonFor(n *depgraph.Node, adj map[string][]string, graph *depgraph.Graph, o stuckEpicScanOpts) string {
	failureReason := o.failureReason
	disableEpicCascade := o.disableEpicCascade
	// Direct blocker takes precedence (matches the dispatcher's ordering: own-dep
	// before epic cascade). Only OPEN, non-work-complete deps are real blockers —
	// an "In review" dep (PR up) does not block downstream work (#4073 review).
	if refs := openBlockerRefs(adj[n.ID().String()], graph); len(refs) > 0 {
		return "blocked by " + strings.Join(refs, ", ") + " (open)"
	}

	// Held back solely by the parent epic's blockedBy cascade — surface that
	// "(via epic #N)" reason instead of mislabeling the sub as merely
	// "ready but undispatched" (matches autonomous.go's blocked-by-epic-dep).
	if !disableEpicCascade && n.EpicNumber != 0 {
		epicKey := depgraph.NodeID{Repo: n.Repo, Number: n.EpicNumber}.String()
		if epic, ok := graph.Nodes[epicKey]; ok && strings.EqualFold(epic.State, "OPEN") {
			if refs := openBlockerRefs(adj[epicKey], graph); len(refs) > 0 {
				return fmt.Sprintf("(via epic #%d) blocked by %s (open)", n.EpicNumber, strings.Join(refs, ", "))
			}
		}
	}

	var hist string
	if failureReason != nil {
		hist = strings.TrimSpace(failureReason(n.Repo, n.Number))
	}

	status := n.BoardStatus
	switch {
	case strings.EqualFold(status, "In review"):
		return inReviewReason(n, o, hist)
	case strings.EqualFold(status, "In progress"):
		if hist != "" {
			return fmt.Sprintf("in %s with no active run — last run: %s", status, hist)
		}
		return fmt.Sprintf("in %s with no active run (likely a silently-failed run)", status)
	case isReadyStatus(status):
		if hist != "" {
			return "ready but undispatched — last run: " + hist
		}
		return "ready but undispatched"
	default:
		if hist != "" {
			return fmt.Sprintf("in %q, not promoted to ready — last run: %s", status, hist)
		}
		return fmt.Sprintf("in %q, not promoted to ready", status)
	}
}

// withHist appends the last-run reason to a stall reason when there is one.
func withHist(reason, hist string) string {
	if hist == "" {
		return reason
	}
	return reason + " — last run: " + hist
}

// inReviewReason explains an "In review" sub-issue WITHOUT inferring a cause
// from the status string (#265).
//
// "In review" is overloaded: the PR-review phase parks an issue there, and so
// does the architecture-approval gate (see TerminalKindArchitectureApprovalRequired,
// which moves the board to "In review" on purpose). The two states need
// opposite operator actions — merge a PR, versus approve a gate so work can
// START — so the old unconditional "PR open, awaiting merge" was not merely
// imprecise: it named the wrong action, confidently, and sent the operator to
// a PR list that had nothing in it.
//
// Each branch below states only what was observed. When nothing was observed,
// it says that too rather than picking the likelier story.
func inReviewReason(n *depgraph.Node, o stuckEpicScanOpts, hist string) string {
	// The approval gate is checked first: when it is holding the issue, that
	// is the actionable blocker regardless of PR state. The last-run reason is
	// deliberately dropped here — it would read "architecture approval
	// required", restating the sentence it is appended to.
	if o.awaitingArchApproval != nil && o.awaitingArchApproval(n.Repo, n.Number) {
		return "in review, awaiting architecture approval (no PR to merge — approve the gate so work can start)"
	}

	verdict := prUnverified
	if o.prEvidence != nil {
		verdict = o.prEvidence(n.Repo, n.Number)
	}
	switch verdict {
	case prOpen:
		return withHist("in review (PR open, awaiting merge)", hist)
	case prAbsent:
		return withHist("in review, but no open PR exists — the run never produced one", hist)
	default:
		// Fail-loud: naming the uncertainty is what lets an operator decide
		// whether to trust the card, and is cheaper than a wrong instruction.
		return withHist("in review (PR state unverified — the PR lookup did not succeed for this repo)", hist)
	}
}

// --- scheduler wiring -------------------------------------------------------

// detectStuckEpics runs the pure detector against the cycle's graph using the
// scheduler's live running-set, recovery maps, and history reader. Called from
// the idle path of runCycle (#4073).
func (as *AutonomousScheduler) detectStuckEpics(graph *depgraph.Graph) []StuckEpic {
	as.mu.Lock()
	runningSet := make(map[string]bool, len(as.state.Running))
	for _, r := range as.state.Running {
		runningSet[fmt.Sprintf("%s#%d", r.Repo, r.Number)] = true
	}
	pickup := as.config.PickupBacklog
	disableCascade := as.config.DisableEpicBlockedByCascade
	// Snapshot the PR-evidence maps under the same lock so the verdict a card
	// reports is internally consistent for the whole scan.
	prBacked := as.inReviewPRBacked
	queryOK := as.prQueryOKRepos
	as.mu.Unlock()

	return stuckEpicsFromGraph(graph, stuckEpicScanOpts{
		now:                  time.Now(),
		runningSet:           runningSet,
		pickupBacklog:        pickup,
		disableEpicCascade:   disableCascade,
		isRecovering:         as.isIssueActivelyRecovering,
		failureReason:        as.issueLastFailureReason,
		prEvidence:           prEvidenceFrom(prBacked, queryOK),
		awaitingArchApproval: as.isAwaitingArchitectureApproval,
	})
}

// prEvidenceFrom builds the three-state PR lookup from the reconcile sweep's
// two maps. Membership in prBacked is a confirmed OPEN PR; otherwise the answer
// depends on whether that repo's PR listing succeeded at all (#265) — which is
// the difference between "there is no PR" and "nobody looked".
func prEvidenceFrom(prBacked, queryOK map[string]bool) func(string, int) prVerdict {
	return func(repo string, number int) prVerdict {
		if prBacked[fmt.Sprintf("%s#%d", repo, number)] {
			return prOpen
		}
		if queryOK[repo] {
			return prAbsent
		}
		return prUnverified
	}
}

// isAwaitingArchitectureApproval reports whether the issue's most recent run
// halted on the architecture-approval gate. That gate parks the issue at "In
// review" deliberately (see TerminalKindArchitectureApprovalRequired), which is
// the second, PR-less meaning of that board status (#265). Best-effort: no
// record reads as false, and the PR-evidence branch then does the talking.
func (as *AutonomousScheduler) isAwaitingArchitectureApproval(repo string, number int) bool {
	rec, ok := as.latestRunRecord(repo, number)
	if !ok || rec == nil {
		return false
	}
	return rec.TerminalFailureKind == TerminalKindArchitectureApprovalRequired
}

// isIssueActivelyRecovering reports whether the scheduler is currently working
// the issue (scheduled retry, in-review recovery, or conflict restart in flight)
// OR its most recent run was a conflict-recovery / branch-out-of-date recovery —
// in which case it is mid-recovery, not silently stalled (#4073).
func (as *AutonomousScheduler) isIssueActivelyRecovering(repo string, number int) bool {
	key := fmt.Sprintf("%s#%d", repo, number)
	as.mu.Lock()
	if plan, ok := as.retryBackoff[key]; ok && plan.Until.After(time.Now()) {
		as.mu.Unlock()
		return true
	}
	if as.inReviewRecoveryAttempts[key] > 0 || as.conflictRestartCount[key] > 0 {
		as.mu.Unlock()
		return true
	}
	as.mu.Unlock()

	rec, ok := as.latestRunRecord(repo, number)
	return ok && runRecordIsRecovering(rec)
}

// recoveryRunWindow bounds how recent a recovery run must be to count as "still
// recovering" — beyond this the run is treated as settled (and the epic may be
// genuinely stuck). 30 minutes comfortably covers a CI re-validation cycle.
const recoveryRunWindow = 30 * time.Minute

func runRecordIsRecovering(rec *state.V2RunRecord) bool {
	if rec == nil {
		return false
	}
	// Only consider recent runs — a week-old recovery is not "in flight".
	if rec.CompletedAt != "" {
		if t, err := time.Parse(time.RFC3339, rec.CompletedAt); err == nil {
			if time.Since(t) > recoveryRunWindow {
				return false
			}
		}
	}
	if rec.IsRecovery {
		return true
	}
	for _, stage := range rec.Stages {
		for _, a := range stage.RecoveryAttempts {
			if a.Action == "conflict-recovery-loop" || a.Action == "branch-out-of-date" {
				return true
			}
		}
	}
	return false
}

// issueLastFailureReason returns a short, human reason for the issue's most
// recent failed run, read from the history JSONL (best-effort; "" when no record
// or the last run succeeded).
func (as *AutonomousScheduler) issueLastFailureReason(repo string, number int) string {
	rec, ok := as.latestRunRecord(repo, number)
	if !ok || rec == nil {
		return ""
	}
	if strings.EqualFold(rec.Outcome, "success") {
		return ""
	}
	if rec.TerminalFailureKind != "" {
		return strings.ReplaceAll(rec.TerminalFailureKind, "_", " ")
	}
	if rec.Outcome != "" {
		return rec.Outcome
	}
	return "run did not complete"
}

// latestRunRecord reads the most recent V2 run record for an issue from the
// primary workspace's history JSONL. Overridable in tests via
// stuckEpicHistoryFn. Best-effort: returns (nil,false) on any read/parse miss.
func (as *AutonomousScheduler) latestRunRecord(repo string, number int) (*state.V2RunRecord, bool) {
	if as.stuckEpicHistoryFn != nil {
		return as.stuckEpicHistoryFn(repo, number)
	}
	return latestRunRecordFromDir(filepath.Join(as.workspaceRoot, ".nightgauge", "pipeline", "history"), repo, number)
}

// parseRecordTime prefers completed_at, falling back to recorded_at. Returns
// ok=false when neither is a valid RFC3339 timestamp, so callers can skip
// records that cannot be ordered (a zero-time tie would otherwise pick an
// arbitrary os.ReadDir/line-order winner).
func parseRecordTime(completedAt, recordedAt string) (time.Time, bool) {
	if t, err := time.Parse(time.RFC3339, completedAt); err == nil {
		return t, true
	}
	if t, err := time.Parse(time.RFC3339, recordedAt); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// latestRunRecordFromDir scans the daily history files for the newest record
// matching the issue (and repo, when the record carries one).
func latestRunRecordFromDir(historyDir, repo string, number int) (*state.V2RunRecord, bool) {
	entries, err := os.ReadDir(historyDir)
	if err != nil {
		return nil, false
	}
	var best *state.V2RunRecord
	var bestAt time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		f, err := os.Open(filepath.Join(historyDir, e.Name()))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
		for sc.Scan() {
			line := sc.Bytes()
			if len(line) == 0 {
				continue
			}
			var rec state.V2RunRecord
			if json.Unmarshal(line, &rec) != nil {
				continue
			}
			if rec.IssueNumber != number {
				continue
			}
			if rec.Repo != "" && repo != "" && !strings.EqualFold(rec.Repo, repo) {
				continue
			}
			at, ok := parseRecordTime(rec.CompletedAt, rec.RecordedAt)
			if !ok {
				continue // no orderable timestamp — skip (avoids zero-time ties)
			}
			if best == nil || at.After(bestAt) {
				cp := rec
				best, bestAt = &cp, at
			}
		}
		f.Close()
	}
	return best, best != nil
}

// surfaceStuckEpics runs detection on the idle cycle, stores the result for the
// IPC/CLI snapshot, and emits a de-duplicated Discord alert per newly-stalled
// epic. Wired into runCycle's idle branch (#4073).
func (as *AutonomousScheduler) surfaceStuckEpics(ctx context.Context, graph *depgraph.Graph) {
	if !as.config.StuckEpicDetectionEnabled || graph == nil {
		// Nothing looked, so nothing may be retracted below (#108): detection
		// being off, or the graph being absent, is "I could not observe" — never
		// a positive assertion that no epic is stalled.
		return
	}
	epics := as.detectStuckEpics(graph)

	as.mu.Lock()
	as.state.StuckEpics = epics
	as.mu.Unlock()

	// Detection ran over the whole graph, so an epic missing from the result is
	// a stall that cleared, not a stall we failed to look for. Retract its card
	// before the early return below — an empty result is the case that most
	// needs retracting (#108).
	observed := make([]string, 0, len(epics))
	for _, e := range epics {
		observed = append(observed, keyStuckEpic(e.Repo, e.Number))
	}
	as.autoResolveAttention(producerStuckEpic, observed)

	if len(epics) == 0 {
		return
	}

	for _, e := range epics {
		log.Printf("autonomous: %s", e.Summary())
		// Action Center watchdog producer (ADR 015 §F #8): surface each stalled
		// epic as a two-way card (requeue / wait) alongside the one-way Discord
		// alert, so the operator can act from any surface.
		as.raiseStuckEpic(e.Repo, e.Number, e.Title, e.Summary())
	}
	as.alertStuckEpics(ctx, epics)
}

// alertStuckEpics posts an alert for each stalled epic not alerted within the
// re-alert cooldown, to every configured sink (Discord and/or Slack, #1072).
// Best-effort: a webhook failure is logged, not fatal.
func (as *AutonomousScheduler) alertStuckEpics(ctx context.Context, epics []StuckEpic) {
	webhook := strings.TrimSpace(as.config.StuckEpicWebhookURL)
	cfg, cfgErr := config.Load(as.workspaceRoot)
	if cfgErr != nil {
		// A malformed config.yaml must not silence the Discord alert that is
		// already configured — resolve Slack from defaults instead (cfg == nil).
		log.Printf("autonomous: stuck-epic alert: config load failed, Slack sink unavailable: %v", cfgErr)
		cfg = nil
	}
	sink := notify.Sinks(webhook, cfg, &http.Client{Timeout: 10 * time.Second})
	if sink == nil {
		return // no destination configured — detection still surfaces via state/CLI
	}
	cooldown := as.config.StuckEpicReAlertAfter
	if cooldown <= 0 {
		cooldown = 6 * time.Hour
	}
	now := time.Now()

	var msgs []notify.Message
	var sentKeys []string
	as.mu.Lock()
	if as.alertedStuckEpics == nil {
		as.alertedStuckEpics = make(map[string]time.Time)
	}
	for _, e := range epics {
		key := fmt.Sprintf("%s#%d", e.Repo, e.Number)
		if last, ok := as.alertedStuckEpics[key]; ok && now.Sub(last) < cooldown {
			continue // still within cooldown — don't re-spam
		}
		sentKeys = append(sentKeys, key)
		msgs = append(msgs, stuckEpicMessage(e))
	}
	as.mu.Unlock()

	if len(msgs) == 0 {
		return
	}
	delivered, err := sink.Post(ctx, msgs)
	if err != nil {
		// A failed batch must NOT arm the cooldown for the epics it dropped, or a
		// transient outage would suppress them for the full cooldown (default 6h).
		// But embeds in earlier batches that DID land must be armed, or they
		// re-spam every cycle. `delivered` is the count a sink actually received
		// (#4073 review, rounds 1-2); with several sinks it is the best any one
		// of them managed, so a message that reached Slack but not Discord still
		// arms its cooldown rather than re-spamming Slack every cycle.
		log.Printf("autonomous: stuck-epic alert partially failed (%d/%d delivered via %s): %s",
			delivered, len(msgs), sink.Name(), sink.Redact(err.Error()))
	}
	if delivered > len(sentKeys) {
		delivered = len(sentKeys)
	}
	if delivered == 0 {
		return
	}
	// Arm the cooldown only for the epics whose embed actually landed.
	as.mu.Lock()
	for _, key := range sentKeys[:delivered] {
		as.alertedStuckEpics[key] = now
	}
	as.mu.Unlock()
}

func stuckEpicMessage(e StuckEpic) notify.Message {
	fields := make([]notify.Field, 0, len(e.Blockers))
	for _, b := range e.Blockers {
		fields = append(fields, notify.Field{
			Name:  fmt.Sprintf("#%d · %s", b.Number, notify.ClampField(b.Title, 120)),
			Value: notify.ClampField(b.Reason, 400),
		})
	}
	title := fmt.Sprintf("🛑 Stalled epic: %s#%d", e.Repo, e.Number)
	desc := fmt.Sprintf("%s\n\nNo eligible work, no active run, and no recovery in flight — this epic is open but stalled, not done.", notify.ClampField(e.Title, 200))
	return notify.Message{
		Title:       notify.ClampField(title, 240),
		Description: desc,
		Color:       notify.ColorHigh,
		Fields:      fields,
		Footer:      "nightgauge stuck-epic watchdog",
		Timestamp:   e.DetectedAt,
	}
}

// StuckEpicsSnapshot returns the stalled epics detected on the most recent idle
// scan (empty when none). Exposed via IPC + the CLI for visibility (#4073).
func (as *AutonomousScheduler) StuckEpicsSnapshot() []StuckEpic {
	as.mu.Lock()
	defer as.mu.Unlock()
	out := make([]StuckEpic, len(as.state.StuckEpics))
	copy(out, as.state.StuckEpics)
	return out
}

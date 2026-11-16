package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/depgraph"
)

// blockedFindingRecord is the subset of the extension's BlockedFinding
// (packages/nightgauge/src/utils/blockedFinding.ts) the scheduler needs to
// skip an issue and to recognise the same finding across cycles (#2914).
type blockedFindingRecord struct {
	SchemaVersion string `json:"schema_version"`
	IssueNumber   int    `json:"issue_number"`
	SignalType    string `json:"signal_type"`
	Reason        string `json:"reason"`
	RecordedAt    string `json:"recorded_at"`
	Stage         string `json:"stage"`
	RunID         string `json:"run_id"`
}

// identity distinguishes one recorded finding from a later re-recording, so
// the skip is logged once per finding rather than once per cycle.
func (r blockedFindingRecord) identity() string {
	return r.SignalType + "|" + r.RecordedAt
}

// readBlockedFinding reads the issue's recorded out-of-scope finding. It
// accepts a file only when it parses and passes the same checks as the
// extension's reader: schema_version "1.0", a non-empty signal_type, and an
// issue_number matching the issue. Anything else reads as no finding, so an
// unreadable file can never wedge an issue out of dispatch.
func readBlockedFinding(repoRoot string, issue int) (blockedFindingRecord, bool) {
	var rec blockedFindingRecord
	path := BlockedFindingPath(repoRoot, issue)
	if path == "" {
		return rec, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return rec, false
	}
	if json.Unmarshal(b, &rec) != nil {
		return blockedFindingRecord{}, false
	}
	if rec.SchemaVersion != "1.0" || rec.SignalType == "" || rec.IssueNumber != issue {
		return blockedFindingRecord{}, false
	}
	return rec, true
}

// blockedFindingFor resolves the root the run itself would use (as
// architectureApprovalFileGranted does) and reads the finding there. The file
// is re-read every cycle, so blocked.clearFinding — which deletes it — makes
// the issue a candidate again on the very next pass.
func (as *AutonomousScheduler) blockedFindingFor(repo string, issue int) (blockedFindingRecord, bool) {
	root, _ := as.blockedFindingRoot(repo)
	if root == "" {
		return blockedFindingRecord{}, false
	}
	return readBlockedFinding(root, issue)
}

// blockedFindingRoot is the root the run for repo would use, where its
// findings live. With no scheduler wired it is the workspace root. A resolve
// error returns the workspace root with the error: a reader may fall back to
// it, a writer must not act on it.
func (as *AutonomousScheduler) blockedFindingRoot(repo string) (string, error) {
	if as.scheduler != nil {
		resolved, err := as.scheduler.resolveRunRoot(repo)
		if err != nil {
			return as.workspaceRoot, err
		}
		if resolved != "" {
			return resolved, nil
		}
	}
	return as.workspaceRoot, nil
}

// noteBlockedFindingSkip logs the skip only when this finding has not been
// logged before. Caller must NOT hold as.mu.
func (as *AutonomousScheduler) noteBlockedFindingSkip(key string, rec blockedFindingRecord) {
	as.mu.Lock()
	defer as.mu.Unlock()
	if as.blockedFindingLogged == nil {
		as.blockedFindingLogged = make(map[string]string)
	}
	id := rec.identity()
	if as.blockedFindingLogged[key] == id {
		return
	}
	as.blockedFindingLogged[key] = id
	log.Printf("autonomous: skipping %s — unresolved blocked finding (%s: %s); "+
		"clear it with blocked.clearFinding to re-admit", key, rec.SignalType, rec.Reason)
}

// forgetClearedBlockedFindings drops de-dup entries for issues that had no
// finding this pass, so a finding recorded again later is logged again.
func (as *AutonomousScheduler) forgetClearedBlockedFindings(seen map[string]bool) {
	as.mu.Lock()
	defer as.mu.Unlock()
	for key := range as.blockedFindingLogged {
		if !seen[key] {
			delete(as.blockedFindingLogged, key)
		}
	}
}

// nativeBlockersAllClosed returns the issue's native `blockedBy` blockers
// (graph edges sourced from the forge's own relation, not from issue-body
// prose) and whether there is at least one and every one is CLOSED (#3081). A
// blocker on the board is read from its node; one off every board from
// resolved, the same per-pass lookup evaluateDeps reads. A blocker of unknown
// state is not closed.
func nativeBlockersAllClosed(g *depgraph.Graph, key string, resolved map[string]string) ([]string, bool) {
	var blockers []string
	allClosed := true
	for _, e := range g.Edges {
		if e.Source != depgraph.EdgeSourceGraphQL || g.NodeKey(e.From) != key {
			continue
		}
		to := g.NodeKey(e.To)
		blockers = append(blockers, to)
		state := resolved[to]
		if node, ok := g.Nodes[to]; ok {
			state = node.State
		}
		if !strings.EqualFold(state, "CLOSED") {
			allClosed = false
		}
	}
	sort.Strings(blockers)
	return blockers, len(blockers) > 0 && allClosed
}

// autoClearBlockedFinding deletes the issue's recorded finding once every
// native blockedBy edge is closed (#3081), and then does what the "Blocker
// resolved" option does (#3170): releases the operator hold a run that
// stopped on the finding may have left, and retracts the card. The finding
// named its blocker in prose; the closed edges are the forge saying the work
// it waited for is done. A finding with no native edge is never cleared here.
func (as *AutonomousScheduler) autoClearBlockedFinding(repo string, issue int, blockers []string, cards *openCardIndex) error {
	root, err := as.blockedFindingRoot(repo)
	if err != nil {
		return fmt.Errorf("resolve the run root for %s: %w", repo, err)
	}
	path := BlockedFindingPath(root, issue)
	if path == "" {
		return fmt.Errorf("no repository root resolves for %s#%d", repo, issue)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	key := fmt.Sprintf("%s#%d", repo, issue)
	as.ClearIssueFailures(key)
	as.retractBlockedFindingCard(repo, issue, cards)
	log.Printf("autonomous: %s — every native blockedBy edge is closed (%s); cleared its blocked finding (#3081)",
		key, strings.Join(blockers, ", "))
	return nil
}

// blockedFindingCardKey is the out-of-scope-blocker card's key for an issue.
func blockedFindingCardKey(repo string, issue int) string {
	return fmt.Sprintf("%s:%s#%d", ProducerOutOfScopeBlocker, repo, issue)
}

// openCardIndex maps the idempotency key of every open (non-terminal) card to
// its id. It lists the store once, on first use, so a prioritize pass reads the
// store once however many findings defer issues. A nil index lists on use.
type openCardIndex struct {
	as     *AutonomousScheduler
	loaded bool
	ok     bool
	byKey  map[string]string
}

// id returns the open card's id for key, or "". ok is false when the store
// could not be read.
func (ix *openCardIndex) id(key string) (string, bool) {
	if !ix.loaded {
		ix.loaded = true
		reqs, err := ix.as.attention.List(attention.ListFilter{})
		if err != nil {
			log.Printf("attention: list open cards failed (fail-open): %v", err)
			return "", false
		}
		ix.ok = true
		ix.byKey = make(map[string]string, len(reqs))
		for _, r := range reqs {
			if !r.Lifecycle.State.IsTerminal() {
				ix.byKey[r.IdempotencyKey] = r.ID
			}
		}
	}
	return ix.byKey[key], ix.ok
}

// raiseBlockedFindingCard keeps the "Blocker resolved" card open while the
// finding defers the issue (#3081). It raises the card only when none is open
// for the issue: an open card is left alone, because re-raising an event card
// re-alerts, and a card that was resolved or expired while the finding still
// holds is raised again rather than leaving the deferral silent. The card is
// not standing on purpose: a standing card a human resolved is suppressed
// until its condition changes, which is exactly the silence this prevents.
func (as *AutonomousScheduler) raiseBlockedFindingCard(repo string, issue int, rec blockedFindingRecord, cards *openCardIndex) {
	if as.attention == nil {
		return
	}
	if cards == nil {
		cards = &openCardIndex{as: as}
	}
	if id, ok := cards.id(blockedFindingCardKey(repo, issue)); !ok || id != "" {
		return
	}
	as.raiseAttention(BuildOutOfScopeBlocker(repo, issue, rec.RunID, rec.Stage))
}

// retractBlockedFindingCard resolves the issue's open out-of-scope-blocker card
// with its "leave" option once the scheduler itself cleared the finding
// (#3081), so the inbox does not offer to clear a hold that is gone.
func (as *AutonomousScheduler) retractBlockedFindingCard(repo string, issue int, cards *openCardIndex) {
	if as.attention == nil {
		return
	}
	if cards == nil {
		cards = &openCardIndex{as: as}
	}
	id, ok := cards.id(blockedFindingCardKey(repo, issue))
	if !ok || id == "" {
		return
	}
	if _, err := as.attention.Resolve(context.Background(), id, "leave", "scheduler:autonomous", "",
		"every native blockedBy edge closed; the scheduler cleared the finding (#3081)", nil); err != nil {
		log.Printf("attention: retract %s failed (fail-open): %v", blockedFindingCardKey(repo, issue), err)
	}
}

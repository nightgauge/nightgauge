package ipc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/runstate"
	"github.com/nightgauge/nightgauge/internal/state"
)

// RED PROBES for ADR-017 step 4 (#370). These are written BEFORE the re-key and
// FAIL ON MAIN BY DESIGN (#166 evidence rule): step 2 put `runId` on the wire
// and the server accepts-and-ignores it, so today every message for an issue
// lands on ONE issue-keyed runtime and no id shape is ever validated.
//
// They compile against today's tree (the param structs already carry RunID) and
// become the step-4 regression guards named in the ADR's Testing Strategy table.

// newRunIdentityTestServer builds the same in-process server the rest of
// internal/ipc's handler tests use: no socket, no transport — the handler
// closures registered by registerMethods are invoked directly with marshalled
// production param structs. workspaceRoot is the launch root, so a repo-carrying
// transition persists under {workspaceRoot}/.nightgauge/pipeline.
func newRunIdentityTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	workspaceRoot := t.TempDir()
	s := &Server{
		writer:         &bytes.Buffer{},
		methods:        make(map[string]Handler),
		activeRuntimes: make(map[string]*runEntry),
		workspaceRoot:  workspaceRoot,
	}
	s.registerMethods()
	return s, filepath.Join(workspaceRoot, ".nightgauge", "pipeline")
}

// mustMarshal renders a production param struct to the wire form the handler
// receives. Wire payloads are NEVER hand-authored JSON in these probes: a
// hand-written shape can drift from what the product actually sends and turn a
// real regression into a green test.
func mustMarshal(t *testing.T, v interface{}) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %T: %v", v, err)
	}
	return data
}

func mustRunID(t *testing.T) string {
	t.Helper()
	id, err := runstate.NewRunID()
	if err != nil {
		t.Fatalf("NewRunID: %v", err)
	}
	return id
}

// TestRunIdentity_ZombieCannotMutateSuccessor covers ADR-017 F2/F4/F10.
//
// Two runs of ONE issue — a zombie that is still emitting and its successor —
// must be two runtimes with two snapshots and no shared accumulators. On main
// the registry key is fmt.Sprintf("%d", p.IssueNumber), so the successor's
// messages mutate the zombie's runtime: one snapshot, one merged TotalCostUSD,
// one merged CompletedStages, and whichever id the server minted first owns the
// file. That is the corruption this step exists to end.
func TestRunIdentity_ZombieCannotMutateSuccessor(t *testing.T) {
	s, stateDir := newRunIdentityTestServer(t)

	const (
		repo  = "acme/platform"
		issue = 370
	)
	runA := mustRunID(t)
	runB := mustRunID(t)

	// Run A (the zombie) is mid-stage.
	if _, err := s.methods["pipeline.notifyStageTransition"](context.Background(), mustMarshal(t,
		PipelineNotifyStageTransitionParams{
			Repo: repo, IssueNumber: issue, Stage: "feature-planning", Status: "running", RunID: runA,
		})); err != nil {
		t.Fatalf("run A running transition: %v", err)
	}

	// Run B (the successor) runs a DIFFERENT stage and books real spend.
	if _, err := s.methods["pipeline.notifyStageTransition"](context.Background(), mustMarshal(t,
		PipelineNotifyStageTransitionParams{
			Repo: repo, IssueNumber: issue, Stage: "feature-dev", Status: "running", RunID: runB,
		})); err != nil {
		t.Fatalf("run B running transition: %v", err)
	}
	if _, err := s.methods["pipeline.notifyStageTransition"](context.Background(), mustMarshal(t,
		PipelineNotifyStageTransitionParams{
			Repo: repo, IssueNumber: issue, Stage: "feature-dev", Status: "complete", RunID: runB,
			InputTokens: 1000, OutputTokens: 250, CostUsd: 4.25,
		})); err != nil {
		t.Fatalf("run B complete transition: %v", err)
	}

	// 1. Two registry entries, keyed by run identity — not one keyed by issue.
	s.runtimesMu.Lock()
	registrySize := len(s.activeRuntimes)
	_, hasA := s.activeRuntimes[runA]
	_, hasB := s.activeRuntimes[runB]
	s.runtimesMu.Unlock()
	if registrySize != 2 || !hasA || !hasB {
		t.Errorf("activeRuntimes holds %d entr(ies) (runA present=%v, runB present=%v); two runs of one issue must be two entries keyed by run id",
			registrySize, hasA, hasB)
	}

	// 2. Two snapshots on disk, each under its own run's canonical filename.
	for _, id := range []string{runA, runB} {
		if _, err := os.Stat(filepath.Join(stateDir, state.SnapshotFilename(issue, id))); err != nil {
			t.Errorf("snapshot %s missing: %v", state.SnapshotFilename(issue, id), err)
		}
	}
	found, err := state.FindPersistedStatesForIssue(stateDir, issue)
	if err != nil {
		t.Fatalf("FindPersistedStatesForIssue: %v", err)
	}
	if len(found) != 2 {
		names, _ := os.ReadDir(stateDir)
		var got []string
		for _, e := range names {
			got = append(got, e.Name())
		}
		t.Fatalf("two runs of one issue must leave two discoverable snapshots; found %d in %s: %v",
			len(found), stateDir, got)
	}

	// 3. Content is UNMERGED: the successor's spend never lands on the zombie,
	//    and the zombie's stage never lands on the successor.
	byID := map[string]*state.RuntimeState{}
	for _, rs := range found {
		byID[rs.RunID] = rs
	}
	zombie, successor := byID[runA], byID[runB]
	if zombie == nil || successor == nil {
		t.Fatalf("snapshots are not keyed by the emitted run ids; got %v", keysOf(byID))
	}
	if zombie.TotalCostUSD != 0 {
		t.Errorf("zombie TotalCostUSD = %v, want 0 — the successor's $4.25 leaked onto a run that never spent it", zombie.TotalCostUSD)
	}
	if len(zombie.CompletedStages) != 0 {
		t.Errorf("zombie CompletedStages = %d, want 0 — the successor's completed stage was booked against the zombie", len(zombie.CompletedStages))
	}
	if successor.TotalCostUSD != 4.25 {
		t.Errorf("successor TotalCostUSD = %v, want 4.25", successor.TotalCostUSD)
	}
	if len(successor.CompletedStages) != 1 || string(successor.CompletedStages[0].Stage) != "feature-dev" {
		t.Errorf("successor CompletedStages = %+v, want exactly one feature-dev result", successor.CompletedStages)
	}
	if string(successor.Stage) == "feature-planning" {
		t.Errorf("successor Stage = %q — the zombie's stage moved the successor", successor.Stage)
	}
	if string(zombie.Stage) != "feature-planning" {
		t.Errorf("zombie Stage = %q, want feature-planning — the successor's stage moved the zombie", zombie.Stage)
	}
}

func keysOf(m map[string]*state.RuntimeState) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestRunIdentity_InvalidRunIdIsRejectedBeforeUse covers ADR-017 Decision 1 +
// Decision 3 lines 1-2: the id is validated BEFORE any use, because it becomes a
// map key and a filename component on an unauthenticated socket (ADR-015 —
// validate at the sink).
//
// Every malformed shape must come back as a JSON-RPC error carrying
// `run_id_invalid`; an EMPTY id must come back as `run_id_required` (the two are
// distinct on purpose: one is hostile input, the other is a version-skewed
// client — see Decision 3). In no case may a file be created, mutated or removed
// under the state dir, and no registry entry may be installed.
//
// On main every one of these is silently accepted: the handler ignores runId,
// mints its own identity and writes a snapshot.
func TestRunIdentity_InvalidRunIdIsRejectedBeforeUse(t *testing.T) {
	cases := []struct {
		name     string
		runID    string
		wantCode string
	}{
		{"empty", "", "run_id_required"},
		{"dot-dot-traversal", "../evil", "run_id_invalid"},
		{"deep-traversal", "../../etc/passwd", "run_id_invalid"},
		{"bare-separator", "/", "run_id_invalid"},
		{"encoded-traversal", "%2e%2e", "run_id_invalid"},
		{"uppercase-uuid", "019A1F2C-3B4D-7E5F-8A9B-0C1D2E3F4A5B", "run_id_invalid"},
		{"uuid-v4", "f47ac10b-58cc-4372-a567-0e02b2c3d479", "run_id_invalid"},
		{"thirty-six-char-non-uuid", strings.Repeat("z", 36), "run_id_invalid"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, stateDir := newRunIdentityTestServer(t)
			const (
				repo  = "acme/platform"
				issue = 4242
			)

			// A legitimate run of the SAME issue already has a snapshot on disk.
			// A rejection must not create, rewrite or remove anything here — the
			// hostile id must never reach a filename composer or a directory
			// read that could act on this file.
			innocent := state.NewRuntimeState(repo, issue, "", mustRunID(t))
			if err := innocent.Persist(stateDir); err != nil {
				t.Fatalf("seed innocent snapshot: %v", err)
			}
			before := dirFingerprint(t, stateDir)

			_, err := s.methods["pipeline.notifyStageTransition"](context.Background(), mustMarshal(t,
				PipelineNotifyStageTransitionParams{
					Repo: repo, IssueNumber: issue, Stage: "feature-dev", Status: "running", RunID: tc.runID,
				}))
			// Errorf, not Fatalf: the "nothing touched disk" assertions below are
			// the second half of the claim and must run even when the call was
			// wrongly accepted.
			if err == nil {
				t.Errorf("runId %q was ACCEPTED; want a JSON-RPC error %s", tc.runID, tc.wantCode)
			} else if !strings.Contains(err.Error(), tc.wantCode) {
				t.Errorf("error %q does not carry the machine-readable code %q", err.Error(), tc.wantCode)
			}

			// Nothing created, nothing rewritten, nothing removed.
			if after := dirFingerprint(t, stateDir); after != before {
				t.Errorf("state dir changed across a rejected call:\n before: %s\n  after: %s", before, after)
			}
			// And no runtime was invented for the rejected id.
			s.runtimesMu.Lock()
			n := len(s.activeRuntimes)
			s.runtimesMu.Unlock()
			if n != 0 {
				t.Errorf("a rejected runId installed %d registry entr(ies); rejection precedes every use", n)
			}
		})
	}
}

// dirFingerprint renders the state dir as "name:sha256(content)" pairs, sorted
// by name — so a created, rewritten or removed file all show up as a diff.
func dirFingerprint(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "<absent>"
		}
		t.Fatalf("ReadDir %s: %v", dir, err)
	}
	var parts []string
	for _, e := range entries {
		if e.IsDir() {
			parts = append(parts, e.Name()+"/")
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("ReadFile %s: %v", e.Name(), err)
		}
		sum := sha256.Sum256(data)
		parts = append(parts, e.Name()+":"+hex.EncodeToString(sum[:8]))
	}
	return strings.Join(parts, " ")
}

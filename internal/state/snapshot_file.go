package state

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"

	"github.com/nightgauge/nightgauge/internal/runstate"
)

// Canonical snapshot layout — ADR-017 Decision 8.
//
//	{repoRoot}/.nightgauge/pipeline/runtime-{issueNumber}-{runId}.json
//
// Everything the old `runtime-{issue}.json` gave us survives — discoverable by
// directory scan with no index, parseable by a process with no registry,
// atomically written, rooted in the run's target repo — and the filename
// additionally becomes the IDENTITY CHECK for destructive writes: a remove
// composed from (issue, runId) cannot take a successor's file even in
// principle, which a bare-issue remove could and did.
//
// THE COMPOSER AND THE DISCOVERY REGEX ARE THE ONLY TWO PLACES IN THE GO TREE
// THAT KNOW THIS SHAPE. Every reader and every remove goes through them; a
// hand-composed `runtime-%d…` anywhere else is the bug this file exists to
// prevent. Both are built from runstate.IdentityPattern, the same constant the
// IPC wire validation uses, so — because the identity is always locally minted
// — there is no id shape that can pass validation and fail discovery. A test
// pins the composer's output against the regex.

// SnapshotFilename composes the canonical on-disk name for a run's snapshot.
// The ONE composer: Persist, PersistExisting, SealAndRemove, LoadPersistedState
// and the IPC server's snapshot removes all route through it.
func SnapshotFilename(issueNumber int, runID string) string {
	return fmt.Sprintf("runtime-%d-%s.json", issueNumber, runID)
}

// snapshotFilePattern matches the NEW scheme only. A legacy `runtime-42.json`
// is deliberately not matched: pre-customer there is no migration, and a file
// that cannot name its run is not a run this tree can reason about.
var snapshotFilePattern = regexp.MustCompile(
	`^runtime-(\d+)-(` + runstate.IdentityPattern + `)\.json$`)

// ParseSnapshotFilename decomposes a canonical snapshot filename into its issue
// number and run identity. ok is false for anything else — including the legacy
// scheme, a claim artifact, and a name whose id does not match the identity
// shape.
func ParseSnapshotFilename(name string) (issueNumber int, runID string, ok bool) {
	m := snapshotFilePattern.FindStringSubmatch(name)
	if m == nil {
		return 0, "", false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, "", false
	}
	return n, m[2], true
}

// LoadPersistedState reads the snapshot for ONE run, addressed by its run
// identity (ADR-017 Decision 8 — this used to take an issue number, which is no
// longer an address because concurrent dispatches of one issue coexist).
//
// The filename carries both components, so the lookup scans stateDir for the
// entry whose parsed identity matches. Returns an error wrapping fs.ErrNotExist
// when no such snapshot exists, so callers can keep distinguishing
// missing-file from parse-error with errors.Is.
func LoadPersistedState(stateDir, runID string) (*RuntimeState, error) {
	if !runstate.IsIdentity(runID) {
		// Refused before the value can become a filename component: this string
		// is interpolated into a path that Persist writes and SealAndRemove
		// removes, on a socket ADR-015 documents as unauthenticated.
		return nil, fmt.Errorf("load runtime snapshot: %q is not a run identity", runID)
	}
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		issueNumber, id, ok := ParseSnapshotFilename(entry.Name())
		if !ok || id != runID {
			continue
		}
		return readSnapshotFile(filepath.Join(stateDir, SnapshotFilename(issueNumber, id)))
	}
	return nil, fmt.Errorf("no runtime snapshot for run %s in %s: %w", runID, stateDir, fs.ErrNotExist)
}

// FindPersistedStatesForIssue returns EVERY snapshot in stateDir belonging to
// the given issue (ADR-017 Decision 8). The issue number is a derived index,
// not an address: concurrent dispatches of one issue coexist, so an
// issue-addressed reader must decide for itself what "more than one" means.
//
// Order is deterministic — newest StartedAt first, ties broken by run id, which
// sorts by mint time because the identity is a UUIDv7. Unreadable and
// unparseable files are skipped rather than failing the whole scan; a
// snapshot dir with one corrupt file still answers for the others.
func FindPersistedStatesForIssue(stateDir string, issueNumber int) ([]*RuntimeState, error) {
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var found []*RuntimeState
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		n, id, ok := ParseSnapshotFilename(entry.Name())
		if !ok || n != issueNumber {
			continue
		}
		rs, err := readSnapshotFile(filepath.Join(stateDir, SnapshotFilename(n, id)))
		if err != nil {
			log.Printf("state: skipping unreadable runtime snapshot %s: %v", entry.Name(), err)
			continue
		}
		found = append(found, rs)
	}
	sort.SliceStable(found, func(i, j int) bool {
		if !found[i].StartedAt.Equal(found[j].StartedAt) {
			return found[i].StartedAt.After(found[j].StartedAt)
		}
		return found[i].RunID > found[j].RunID
	})
	return found, nil
}

// PickPersistedStateForIssue is THE standard pick for issue-addressed readers
// that want a single run: prefer a non-terminal snapshot, then the newest
// StartedAt. It logs whenever more than one candidate existed, because a reader
// silently choosing between two live runs of one issue is exactly the ambiguity
// ADR-017 exists to make visible.
//
// Readers for whom a wrong answer is worse than no answer must NOT use this —
// they call FindPersistedStatesForIssue and refuse. #305's spend corroboration
// is the in-tree example: a guessed spend number is worse than a missing one.
//
// Returns an error wrapping fs.ErrNotExist when the issue has no snapshot.
func PickPersistedStateForIssue(stateDir string, issueNumber int) (*RuntimeState, error) {
	candidates, err := FindPersistedStatesForIssue(stateDir, issueNumber)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no runtime snapshot for #%d in %s: %w", issueNumber, stateDir, fs.ErrNotExist)
	}
	if len(candidates) > 1 {
		ids := make([]string, 0, len(candidates))
		for _, c := range candidates {
			ids = append(ids, c.RunID)
		}
		log.Printf("state: #%d has %d runtime snapshots (%v) — picking by non-terminal, then newest StartedAt",
			issueNumber, len(candidates), ids)
	}
	// candidates is already newest-first, so the first non-terminal entry is
	// both non-terminal and newest among the non-terminal ones.
	for _, c := range candidates {
		if !c.Terminal {
			return c, nil
		}
	}
	return candidates[0], nil
}

func readSnapshotFile(path string) (*RuntimeState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rs RuntimeState
	if err := json.Unmarshal(data, &rs); err != nil {
		return nil, fmt.Errorf("unmarshal state: %w", err)
	}
	return &rs, nil
}

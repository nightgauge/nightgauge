package reclaim

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Pipeline stashes — the other half of "machine state nobody reclaims".
//
// A stage that needs a clean tree to measure against (the CI-gate baseline
// detector, the "preserve unrelated work" paths) stashes first and pops after.
// When the stage is killed the pop never runs, and the stash outlives it with
// no owner and no expiry. A workspace audit on 2026-08-04 found five such
// stashes across three repos, the oldest five months old, one of them holding
// an entire issue's deliverable. Nothing had ever reported them: the operator
// found them by running `git stash list` by hand.
//
// The stashes were unfindable because they were unnamed — `sibling-496-wip-
// preserve`, `temp-stash-unrelated-changes-before-pr-36`, `lint-staged
// automatic backup`. No tool can reclaim state it cannot recognise, and no
// heuristic over free-form stash messages is safe enough to delete on: an
// operator's own `git stash -m "wip before pr 36"` is indistinguishable from
// the pipeline's. So the pipeline writes exactly one name, and the sweep
// touches exactly that name. Anything else is UNOWNED and is reported, never
// acted on — the determined=false contract from #323 applied to stashes.

// StashMarker prefixes every stash the pipeline creates. Chosen to be
// greppable and impossible to type by accident:
//
//	git stash list | grep nightgauge:
const StashMarker = "nightgauge:"

// StashPurpose names why a stash exists. It is part of the reclaim decision:
// a baseline stash holds work that must be restored, so the sweep pops it
// rather than dropping it.
type StashPurpose string

const (
	// StashBaseline is the pre-baseline snapshot a validation stage takes so
	// it can run the test suite against an unmodified tree. ResetPipeline pops
	// it (#289 AC5); a killed stage leaves it behind.
	StashBaseline StashPurpose = "baseline"
	// StashWIPPreserve is the "preserve unrelated work" path: a stage moving
	// work out of the way before touching a sibling repo.
	StashWIPPreserve StashPurpose = "wip-preserve"
)

// StashName builds the canonical stash message. Every field is required and
// every field is load-bearing: purpose decides pop-vs-drop, issue scopes a
// per-run reclaim, and stage is what makes a leak attributable to the code
// path that dropped it.
//
//	nightgauge:baseline:289:feature-validate
func StashName(purpose StashPurpose, issue int, stage string) string {
	if stage == "" {
		stage = "unknown"
	}
	return fmt.Sprintf("%s%s:%d:%s", StashMarker, purpose, issue, stage)
}

// StashEntry is one entry from `git stash list`, classified.
type StashEntry struct {
	// Ref is the stash selector, e.g. "stash@{0}". Note that refs SHIFT as
	// stashes are removed — see Sweep for why it re-lists rather than
	// iterating a captured slice.
	Ref string `json:"ref"`
	// Message is the full reflog subject, e.g. "On main: nightgauge:...".
	Message string `json:"message"`
	// CreatedAt is when the stash commit was made.
	CreatedAt time.Time `json:"createdAt"`
	// Owned is true when Message carries the canonical marker. Only an owned
	// stash may be reclaimed.
	Owned bool `json:"owned"`
	// Purpose, Issue and Stage are populated only when Owned.
	Purpose StashPurpose `json:"purpose,omitempty"`
	Issue   int          `json:"issue,omitempty"`
	Stage   string       `json:"stage,omitempty"`
}

// Age reports how long the stash has existed as of now.
func (e StashEntry) Age(now time.Time) time.Duration {
	if e.CreatedAt.IsZero() {
		return 0
	}
	return now.Sub(e.CreatedAt)
}

// ParseStashMessage extracts the canonical fields from a stash reflog subject.
//
// The marker is searched for rather than anchored at the start because git
// renders a stash as "On <branch>: <message>" (or "WIP on <branch>: …"), and
// the branch name is not ours to predict. ok=false means the pipeline did not
// create this stash — the sweep must leave it alone rather than guess.
func ParseStashMessage(message string) (purpose StashPurpose, issue int, stage string, ok bool) {
	idx := strings.Index(message, StashMarker)
	if idx < 0 {
		return "", 0, "", false
	}
	fields := strings.Split(strings.TrimSpace(message[idx+len(StashMarker):]), ":")
	if len(fields) != 3 {
		return "", 0, "", false
	}
	n, err := strconv.Atoi(fields[1])
	if err != nil || n <= 0 || fields[0] == "" || fields[2] == "" {
		return "", 0, "", false
	}
	return StashPurpose(fields[0]), n, fields[2], true
}

// ListStashes returns every stash in repoRoot, classified by ownership.
//
// The custom format is what makes this deterministic: %gd is the selector,
// %gs the reflog subject, %ct the commit timestamp. Parsing the default human
// output would mean re-deriving the timestamp from a separate call, and the
// selector and message would have to be split on a ": " that also appears
// inside messages.
//
// A repo with no stashes returns an empty slice and no error — nothing to be
// wrong about. A failing `git stash list` returns an error, and callers must
// treat that as "I could not find out" rather than "there are none".
func ListStashes(repoRoot string) ([]StashEntry, error) {
	cmd := exec.Command("git", "stash", "list", "--format=%gd%x1f%gs%x1f%ct")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git stash list in %s: %w", repoRoot, err)
	}

	var entries []StashEntry
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, "\x1f")
		if len(parts) != 3 {
			continue
		}
		e := StashEntry{Ref: parts[0], Message: parts[1]}
		if secs, convErr := strconv.ParseInt(parts[2], 10, 64); convErr == nil {
			e.CreatedAt = time.Unix(secs, 0)
		}
		if purpose, issue, stage, ok := ParseStashMessage(e.Message); ok {
			e.Owned, e.Purpose, e.Issue, e.Stage = true, purpose, issue, stage
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// PipelineStashes filters a listing to the stashes the pipeline owns,
// optionally narrowed to one issue (issue <= 0 means every issue).
func PipelineStashes(entries []StashEntry, issue int) []StashEntry {
	var out []StashEntry
	for _, e := range entries {
		if !e.Owned {
			continue
		}
		if issue > 0 && e.Issue != issue {
			continue
		}
		out = append(out, e)
	}
	return out
}

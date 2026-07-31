package attention

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// streakFileName holds consecutive-occurrence counts for standing conditions
// that escalate with repetition, keyed by the producer's idempotency_key.
//
// The count lives here rather than on the card itself (#243). #242 stored it in
// the card's fingerprint and recovered it by listing open cards, which made
// every path that ends a card's life also a silent reset: an operator
// acknowledging the card, or the card reaching ExpiresAt, both returned the
// streak to 1. Because the card escalates to blocking_run at 3 and its only
// options resolve it, the one action needed to unblock a run was the action
// that erased the evidence — the count could never exceed 3.
//
// A card is a notification; it is allowed to be dismissed. The count is a
// durable fact about the repo, and only a real execution of the tier may clear
// it (ResetStreak, called from the auto-resolve path).
const streakFileName = "streaks.json"

// streakSubdir keeps the counts file OUT of the directory the card scanner
// reads. List/scanLocked walk s.dir and parse every *.json entry as a
// DecisionRequest — a counts file sitting beside the cards unmarshals into a
// zero-valued request and is returned as a phantom card, which is exactly what
// happened the first time this was written here. The scanner skips directory
// entries (`e.IsDir()`), so one level down is structurally safe rather than
// safe-by-naming-convention. Do not flatten this back into s.dir.
const streakSubdir = "state"

// streakFile is the on-disk shape. Counts are keyed by idempotency_key so a
// producer's per-(repo, tier) identity carries over verbatim.
type streakFile struct {
	SchemaVersion int            `json:"schema_version"`
	Counts        map[string]int `json:"counts"`
}

const streakSchemaVersion = 1

func (s *Store) streakDir() string {
	return filepath.Join(s.dir, streakSubdir)
}

func (s *Store) streakPath() string {
	return filepath.Join(s.streakDir(), streakFileName)
}

// loadStreaksLocked reads the counts file. A missing, malformed, or
// unknown-version file yields an empty map rather than an error: losing a
// streak count degrades a signal, while failing the caller would fail the
// pipeline run that was only trying to record one.
func (s *Store) loadStreaksLocked() streakFile {
	empty := streakFile{SchemaVersion: streakSchemaVersion, Counts: map[string]int{}}

	data, err := os.ReadFile(s.streakPath())
	if err != nil {
		return empty
	}
	var f streakFile
	if err := json.Unmarshal(data, &f); err != nil {
		return empty
	}
	if f.SchemaVersion != streakSchemaVersion || f.Counts == nil {
		return empty
	}
	return f
}

func (s *Store) saveStreaksLocked(f streakFile) error {
	if err := os.MkdirAll(s.streakDir(), 0o755); err != nil {
		return fmt.Errorf("attention: create streak dir: %w", err)
	}
	f.SchemaVersion = streakSchemaVersion
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("attention: marshal streaks: %w", err)
	}
	path := s.streakPath()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("attention: write temp streaks: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("attention: rename streaks: %w", err)
	}
	return nil
}

// IncrementStreak raises the consecutive-occurrence count for key by one and
// returns the new value. The first occurrence returns 1.
//
// The count is independent of any card's lifecycle: it survives the card being
// acknowledged, muted, or expired. Only ResetStreak clears it.
func (s *Store) IncrementStreak(key string) (int, error) {
	if strings.TrimSpace(key) == "" {
		return 0, fmt.Errorf("attention: increment streak requires a key")
	}

	mu := lockFor(s.dir)
	mu.Lock()
	defer mu.Unlock()

	f := s.loadStreaksLocked()
	f.Counts[key] = f.Counts[key] + 1
	next := f.Counts[key]
	if err := s.saveStreaksLocked(f); err != nil {
		return next, err
	}
	return next, nil
}

// ResetStreak clears the count for key, so the next occurrence starts at 1.
// This is the only way a streak returns to zero — it belongs to the path that
// observed the underlying condition actually clear, never to a card dismissal.
func (s *Store) ResetStreak(key string) error {
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("attention: reset streak requires a key")
	}

	mu := lockFor(s.dir)
	mu.Lock()
	defer mu.Unlock()

	f := s.loadStreaksLocked()
	if _, ok := f.Counts[key]; !ok {
		return nil
	}
	delete(f.Counts, key)
	return s.saveStreaksLocked(f)
}

// StreakCount reports the current count for key without changing it.
func (s *Store) StreakCount(key string) int {
	mu := lockFor(s.dir)
	mu.Lock()
	defer mu.Unlock()

	return s.loadStreaksLocked().Counts[key]
}

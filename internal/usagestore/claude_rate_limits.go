// Package usagestore persists the last-seen Claude subscription rate-limit
// readings that the VS Code footer and dashboard usage panel render.
//
// # Why a Go writer exists
//
// The reading itself belongs to the extension's usage model
// (docs/decisions/018-adapter-usage-quota-model.md): TypeScript's
// ClaudeRateLimitStore owns the same file and feeds
// ClaudeRateLimitUsageProvider. That store originally had exactly one writer —
// PipelineBridge, fed by the rate_limit_event envelope on nightgauge's own
// `claude -p` stream — which meant a reading existed only while nightgauge was
// itself running a pipeline stage. An operator who uses Claude Code
// interactively and runs the pipeline occasionally had no file at all, so the
// meter fell through to dollar windows that describe pay-per-token billing
// rather than a subscription allowance (Issue #730).
//
// Claude Code's statusLine contract hands its configured command the same
// account-wide figure at rest, on every render of every session. That command
// is a process, not the extension, so the second writer has to live here.
//
// # One shape, two writers
//
// This package deliberately does not define its own format: it reads and
// writes the envelope ClaudeRateLimitStore.ts already persists, field for
// field, including the version number. A fork would give the two surfaces two
// answers to the same question, which is the failure ADR 018 exists to
// prevent.
//
// Merging is by ObservedAt, per bucket — whichever writer saw the newer
// reading wins. Neither writer is authoritative, because neither can see the
// other's channel.
package usagestore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// StoreVersion is the on-disk schema version, and must stay in lockstep with
// STORE_VERSION in
// packages/nightgauge-vscode/src/services/usage/ClaudeRateLimitStore.ts. A
// file carrying any other version is discarded rather than migrated: this is a
// cache of a figure that will be re-observed within minutes, not user data.
const StoreVersion = 1

// relPath is the store's location beneath the account root. It is account
// scoped rather than workspace scoped because the utilization it holds is
// account wide: a statusline render in any repository must reach every
// workspace's footer.
const relPath = ".nightgauge/usage/claude-rate-limits.json"

// Reading is one bucket's last-seen utilization.
//
// The JSON tags are camelCase because the TypeScript store defined this file
// first; matching it exactly is the point.
type Reading struct {
	// RateLimitType is the vendor's own bucket name — "five_hour",
	// "seven_day", "daily". Never translated here: the consumer maps bucket
	// names onto window scopes, and a name this package does not recognise
	// must still round-trip rather than being dropped by the writer.
	RateLimitType string `json:"rateLimitType"`
	// Utilization is the percentage of the bucket consumed, 0-100, as the
	// vendor reported it.
	Utilization float64 `json:"utilization"`
	// ResetsAt is unix epoch *seconds* when the bucket refills, or 0 when the
	// source carried none.
	ResetsAt int64 `json:"resetsAt"`
	// Status is "allowed" / "allowed_warning" / "limited" verbatim from the
	// stream envelope. The statusLine payload carries no equivalent field, so
	// the statusline writer records "unknown" rather than inferring one from
	// the percentage — a threshold this code picked is not a vendor status.
	Status string `json:"status"`
	// ObservedAt is when the reading was seen. It is the merge key, and the
	// "as of" the UI prints for a cached figure.
	ObservedAt time.Time `json:"observedAt"`
}

type persistedStore struct {
	Version int                `json:"version"`
	Buckets map[string]Reading `json:"buckets"`
}

// Store is the account-scoped rate-limit file.
type Store struct {
	path string
}

// New opens the store beneath an explicit account root. Tests pass a temp
// directory; production uses ForAccount.
func New(accountRoot string) *Store {
	return &Store{path: filepath.Join(accountRoot, relPath)}
}

// ForAccount opens the store beneath the current user's home directory.
func ForAccount() (*Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home directory: %w", err)
	}
	return New(home), nil
}

// Path is the absolute path of the backing file.
func (s *Store) Path() string { return s.path }

// load reads the current buckets. A missing file is the normal first-run
// state, and an unreadable, malformed or wrong-version file is treated as "no
// readings" — never as an error worth failing a status line over.
func (s *Store) load() map[string]Reading {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return map[string]Reading{}
	}
	var parsed persistedStore
	if err := json.Unmarshal(data, &parsed); err != nil {
		return map[string]Reading{}
	}
	if parsed.Version != StoreVersion || parsed.Buckets == nil {
		return map[string]Reading{}
	}
	out := make(map[string]Reading, len(parsed.Buckets))
	for name, reading := range parsed.Buckets {
		if !readingValid(reading) {
			continue
		}
		out[name] = reading
	}
	return out
}

// readingValid rejects anything the consumer could not honestly render. A
// coerced percentage is a fabricated one, so a malformed entry is dropped
// rather than repaired.
func readingValid(r Reading) bool {
	return r.RateLimitType != "" && !r.ObservedAt.IsZero()
}

// Expired reports whether a reading's own window has already refilled, making
// its utilization known-wrong rather than merely stale.
//
// A ResetsAt of 0 means no reset time was carried. That cannot expire on a
// clock — there is no clock — so such a reading stays readable and relies on
// its ObservedAt to state its age.
func Expired(r Reading, now time.Time) bool {
	if r.ResetsAt <= 0 {
		return false
	}
	return !time.Unix(r.ResetsAt, 0).After(now)
}

// Record merges readings into the store and writes it back atomically.
//
// Per bucket the newer ObservedAt wins, so this writer cannot clobber a
// fresher reading the extension's stream writer recorded between this
// process's read and write. Expired buckets are pruned on the way out so the
// file does not accumulate windows that can never be served again.
//
// Returns an error only for a genuine I/O failure; callers on a status-line
// path degrade to a stderr warning rather than failing the render.
func (s *Store) Record(readings []Reading, now time.Time) error {
	if len(readings) == 0 {
		return nil
	}
	buckets := s.load()
	for _, incoming := range readings {
		if !readingValid(incoming) {
			continue
		}
		// Normalise to UTC so the file matches what the TypeScript writer's
		// Date.toISOString() produces. Both parsers accept either form, but a
		// store that flips between offset and Z notation on alternate writes
		// looks like a change to anything diffing it.
		incoming.ObservedAt = incoming.ObservedAt.UTC()
		if existing, ok := buckets[incoming.RateLimitType]; ok &&
			existing.ObservedAt.After(incoming.ObservedAt) {
			continue
		}
		buckets[incoming.RateLimitType] = incoming
	}
	for name, reading := range buckets {
		if Expired(reading, now) {
			delete(buckets, name)
		}
	}
	return s.write(persistedStore{Version: StoreVersion, Buckets: buckets})
}

// write serialises the store to a temp file in the target directory and
// renames it into place, so a concurrent reader — the extension polls this
// file — never observes a partially written document.
func (s *Store) write(store persistedStore) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create usage directory: %w", err)
	}
	// Match the TypeScript writer byte for byte: two-space indent, trailing
	// newline. A round-trip that reformats the file would make every write
	// look like a change to anything watching it.
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return fmt.Errorf("encode usage store: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(dir, ".claude-rate-limits-*.json")
	if err != nil {
		return fmt.Errorf("create temp usage file: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write temp usage file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp usage file: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("replace usage file: %w", err)
	}
	return nil
}

// Readings returns every unexpired reading, for diagnostics (`nightgauge
// doctor`) and tests. The extension reads the file directly through its own
// store.
func (s *Store) Readings(now time.Time) []Reading {
	buckets := s.load()
	out := make([]Reading, 0, len(buckets))
	for _, reading := range buckets {
		if Expired(reading, now) {
			continue
		}
		out = append(out, reading)
	}
	return out
}

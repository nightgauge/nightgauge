// Package discoveredwork is the per-run capture substrate for work a stage
// found but must not do in its own run (#478, ADR 027). Stages append records
// to discovered-work-{N}.json in the clone's pipeline state directory; the
// #480 materializer is the only reader that turns them into issues and the
// only deleter. pr-merge cleanup deliberately never matches the file.
//
// The record contract is shared with the SDK's DiscoveredWorkRecordSchema
// (packages/nightgauge-sdk/src/context/schemas/discovered-work.ts); both
// sides pin their key sets against testdata/record-v1.json.
package discoveredwork

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/atomicfile"
	"github.com/nightgauge/nightgauge/internal/filing"
	"github.com/nightgauge/nightgauge/internal/flock"
)

// SchemaVersion is the capture file envelope version.
const SchemaVersion = "1.0"

// DefaultOrphanAge is how old a capture file may grow before it is reported
// as orphaned even while its issue context still exists.
const DefaultOrphanAge = 7 * 24 * time.Hour

// lockTimeout bounds the wait for a concurrent appender.
const lockTimeout = 10 * time.Second

// Record is one discovered-work item: an issue candidate, never an action.
type Record struct {
	ID                   string   `json:"id"`
	Summary              string   `json:"summary"`
	Evidence             []string `json:"evidence"`
	SourceStage          string   `json:"source_stage"`
	IssueNumber          int      `json:"issue_number"`
	RunID                *string  `json:"run_id"`
	SuggestedType        string   `json:"suggested_type"`
	SuggestedPriority    string   `json:"suggested_priority"`
	SuggestedSize        string   `json:"suggested_size"`
	CandidateTargetFiles []string `json:"candidate_target_files"`
	RecordedAt           string   `json:"recorded_at"`
}

// Capture is the discovered-work-{N}.json envelope.
type Capture struct {
	SchemaVersion string   `json:"schema_version"`
	IssueNumber   int      `json:"issue_number"`
	Records       []Record `json:"records"`
}

// Closed vocabularies. Type, priority and size are internal/filing's tables,
// shared with the spike path and the #480 materializer (ADR 027).
var (
	allowedStages = map[string]bool{
		"issue-pickup": true, "feature-planning": true, "feature-dev": true,
		"feature-validate": true, "pr-create": true, "pr-merge": true,
	}
	allowedTypes      = filing.AllowedTypes
	allowedPriorities = filing.AllowedPriorities
	allowedSizes      = filing.AllowedSizes
	producerRe        = regexp.MustCompile(`^producer:[a-z0-9]+(-[a-z0-9]+)*$`)
	idRe              = regexp.MustCompile(`^dw-[0-9a-f]{12}$`)
	captureNameRe     = regexp.MustCompile(`^discovered-work-([0-9]+)\.json$`)
)

// Bounds shared with the SDK schema.
const (
	maxSummary    = 200
	maxEvidence   = 10
	maxEvidenceSz = 2000
	maxFiles      = 20
	maxFileLen    = 256
)

// ID is the deterministic record id: re-recording the same discovery from the
// same producer collapses onto one record.
func ID(sourceStage, summary string) string {
	sum := sha256.Sum256([]byte(sourceStage + "\x00" + summary))
	return "dw-" + hex.EncodeToString(sum[:])[:12]
}

// Validate checks rec against the v1 contract. Errors name the field first
// ("<field>: <reason>") so a caller can surface them verbatim.
func Validate(rec *Record) error {
	if rec == nil {
		return errors.New("record: is nil")
	}
	s := rec.Summary
	if strings.TrimSpace(s) == "" {
		return errors.New("summary: is required")
	}
	if s != strings.TrimSpace(s) {
		return errors.New("summary: must not have leading or trailing whitespace")
	}
	if strings.ContainsAny(s, "\r\n") {
		return errors.New("summary: must be a single line")
	}
	if err := checkPrintable(s, false); err != nil {
		return fmt.Errorf("summary: %w", err)
	}
	if n := len([]rune(s)); n > maxSummary {
		return fmt.Errorf("summary: must be at most %d characters (got %d)", maxSummary, n)
	}
	if len(rec.Evidence) == 0 || len(rec.Evidence) > maxEvidence {
		return fmt.Errorf("evidence: must have 1-%d entries (got %d)", maxEvidence, len(rec.Evidence))
	}
	for i, e := range rec.Evidence {
		if n := len([]rune(e)); n == 0 || n > maxEvidenceSz {
			return fmt.Errorf("evidence[%d]: must be 1-%d characters (got %d)", i, maxEvidenceSz, n)
		}
		if err := checkPrintable(e, true); err != nil {
			return fmt.Errorf("evidence[%d]: %w", i, err)
		}
	}
	if !allowedStages[rec.SourceStage] && !producerRe.MatchString(rec.SourceStage) {
		return fmt.Errorf("source_stage: must be a pipeline stage or producer:<kebab-name> (got %q)", rec.SourceStage)
	}
	if rec.IssueNumber <= 0 {
		return fmt.Errorf("issue_number: must be a positive integer (got %d)", rec.IssueNumber)
	}
	if rec.RunID != nil && strings.TrimSpace(*rec.RunID) == "" {
		return errors.New("run_id: must be null or a non-empty string")
	}
	if !allowedTypes[rec.SuggestedType] {
		return fmt.Errorf("suggested_type: must be one of feature|bug|docs|chore|spike (got %q)", rec.SuggestedType)
	}
	if !allowedPriorities[rec.SuggestedPriority] {
		return fmt.Errorf("suggested_priority: must be one of critical|high|medium|low (got %q)", rec.SuggestedPriority)
	}
	if !allowedSizes[rec.SuggestedSize] {
		return fmt.Errorf("suggested_size: must be one of XS|S|M|L|XL (got %q)", rec.SuggestedSize)
	}
	if rec.CandidateTargetFiles == nil {
		return errors.New("candidate_target_files: is required (use [] for none)")
	}
	if len(rec.CandidateTargetFiles) > maxFiles {
		return fmt.Errorf("candidate_target_files: at most %d entries (got %d)", maxFiles, len(rec.CandidateTargetFiles))
	}
	for i, f := range rec.CandidateTargetFiles {
		if err := validateTargetPath(f); err != nil {
			return fmt.Errorf("candidate_target_files[%d]: %v", i, err)
		}
	}
	if !idRe.MatchString(rec.ID) {
		return fmt.Errorf("id: must match dw-<12 hex> (got %q)", rec.ID)
	}
	if want := ID(rec.SourceStage, rec.Summary); rec.ID != want {
		return fmt.Errorf("id: %q does not match its source_stage and summary (want %q)", rec.ID, want)
	}
	if _, err := time.Parse(time.RFC3339, rec.RecordedAt); err != nil || !strings.HasSuffix(rec.RecordedAt, "Z") {
		return fmt.Errorf("recorded_at: must be an RFC 3339 UTC timestamp (got %q)", rec.RecordedAt)
	}
	return nil
}

// validateTargetPath accepts a repository-relative path. The path is never
// opened, stat'ed or executed here; it is a hint for the materializer.
func validateTargetPath(p string) error {
	switch {
	case p == "":
		return errors.New("must not be empty")
	case len(p) > maxFileLen:
		return fmt.Errorf("must be at most %d characters", maxFileLen)
	case strings.ContainsRune(p, 0):
		return errors.New("must not contain NUL")
	case strings.HasPrefix(p, "-"):
		return errors.New("must not start with '-'")
	case filepath.IsAbs(p) || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) ||
		hasDrivePrefix(p):
		return errors.New("must be repository-relative, not absolute")
	}
	// Normalize both separators on every OS so this agrees with the SDK schema.
	for _, seg := range strings.Split(path.Clean(strings.ReplaceAll(p, `\`, "/")), "/") {
		if seg == ".." {
			return errors.New("must not contain a '..' segment")
		}
	}
	return nil
}

func hasDrivePrefix(p string) bool {
	return len(p) >= 2 && p[1] == ':' && ((p[0] >= 'A' && p[0] <= 'Z') || (p[0] >= 'a' && p[0] <= 'z'))
}

// checkPrintable rejects C0/C1 control characters, DEL and bidi override code
// points, so a record printed to a terminal cannot carry escape sequences.
// Multi-line text may keep \n and \t.
func checkPrintable(s string, multiline bool) error {
	for _, r := range s {
		if multiline && (r == '\n' || r == '\t') {
			continue
		}
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) || (r >= 0x202a && r <= 0x202e) ||
			(r >= 0x2066 && r <= 0x2069) {
			return fmt.Errorf("must not contain control or bidi character %U", r)
		}
	}
	return nil
}

// CapturePath is runDir/discovered-work-{issue}.json. The name is built only
// from the integer issue, and the result is confirmed to stay inside the
// symlink-resolved runDir, which must be an existing directory.
func CapturePath(runDir string, issue int) (string, error) {
	if issue <= 0 {
		return "", fmt.Errorf("issue: must be a positive integer (got %d)", issue)
	}
	root, err := resolveRunDir(runDir)
	if err != nil {
		return "", err
	}
	p := filepath.Join(root, "discovered-work-"+strconv.Itoa(issue)+".json")
	if filepath.Dir(p) != root {
		return "", fmt.Errorf("run dir: capture path %s escapes %s", p, root)
	}
	if fi, err := os.Lstat(p); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("capture file %s: is a symlink; refusing to follow it", p)
	}
	return p, nil
}

func resolveRunDir(runDir string) (string, error) {
	if strings.TrimSpace(runDir) == "" {
		return "", errors.New("run dir: is required")
	}
	abs, err := filepath.Abs(filepath.Clean(runDir))
	if err != nil {
		return "", fmt.Errorf("run dir: %w", err)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("run dir: %w", err)
	}
	fi, err := os.Stat(real)
	if err != nil {
		return "", fmt.Errorf("run dir: %w", err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("run dir: %s is not a directory", real)
	}
	return real, nil
}

// Load reads and validates the capture file for issue. A missing file is an
// empty capture (nil, nil): a run that recorded nothing writes nothing.
func Load(runDir string, issue int) (*Capture, error) {
	p, err := CapturePath(runDir, issue)
	if err != nil {
		return nil, err
	}
	return loadPath(p, issue)
}

func loadPath(p string, issue int) (*Capture, error) {
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", p, err)
	}
	c, err := Decode(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	if c.IssueNumber != issue {
		return nil, fmt.Errorf("%s: issue_number: envelope says %d, file name says %d", p, c.IssueNumber, issue)
	}
	return c, nil
}

// Decode parses and validates a capture envelope, refusing unknown keys.
func Decode(data []byte) (*Capture, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var c Capture
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("decode: trailing data after the envelope")
	}
	if c.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("schema_version: must be %q (got %q)", SchemaVersion, c.SchemaVersion)
	}
	if c.IssueNumber <= 0 {
		return nil, fmt.Errorf("issue_number: must be a positive integer (got %d)", c.IssueNumber)
	}
	if c.Records == nil {
		return nil, errors.New("records: is required")
	}
	seen := map[string]bool{}
	for i := range c.Records {
		r := &c.Records[i]
		if err := Validate(r); err != nil {
			return nil, fmt.Errorf("records[%d]: %w", i, err)
		}
		if r.IssueNumber != c.IssueNumber {
			return nil, fmt.Errorf("records[%d]: issue_number: %d does not match the envelope's %d", i, r.IssueNumber, c.IssueNumber)
		}
		if seen[r.ID] {
			return nil, fmt.Errorf("records[%d]: id: duplicate %q", i, r.ID)
		}
		seen[r.ID] = true
	}
	return &c, nil
}

// Append validates rec, then adds it to the capture file under an exclusive
// lock with an atomic replace. rec.ID and rec.RecordedAt are set here when
// empty. It reports added=false when a record with the same id is already
// present (an idempotent re-record). On any error the file is untouched.
func Append(runDir string, rec Record, now time.Time) (Record, bool, error) {
	if rec.ID == "" {
		rec.ID = ID(rec.SourceStage, rec.Summary)
	}
	if rec.RecordedAt == "" {
		rec.RecordedAt = now.UTC().Format(time.RFC3339)
	}
	if rec.CandidateTargetFiles == nil {
		rec.CandidateTargetFiles = []string{}
	}
	if err := Validate(&rec); err != nil {
		return rec, false, err
	}
	p, err := CapturePath(runDir, rec.IssueNumber)
	if err != nil {
		return rec, false, err
	}

	lf, err := os.OpenFile(p+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return rec, false, fmt.Errorf("open lock: %w", err)
	}
	defer lf.Close()
	if err := flock.Exclusive(lf, lockTimeout); err != nil && !errors.Is(err, flock.ErrUnsupported) {
		return rec, false, fmt.Errorf("lock %s: %w", p, err)
	}
	defer flock.Unlock(lf) //nolint:errcheck // closing the file releases it too

	c, err := loadPath(p, rec.IssueNumber)
	if err != nil {
		return rec, false, err
	}
	if c == nil {
		c = &Capture{SchemaVersion: SchemaVersion, IssueNumber: rec.IssueNumber, Records: []Record{}}
	}
	for _, existing := range c.Records {
		if existing.ID == rec.ID {
			return existing, false, nil
		}
	}
	c.Records = append(c.Records, rec)
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return rec, false, fmt.Errorf("marshal: %w", err)
	}
	if err := atomicfile.Write(p, append(data, '\n'), 0o644); err != nil {
		return rec, false, fmt.Errorf("write %s: %w", p, err)
	}
	return rec, true, nil
}

// Consume rewrites the capture file for issue keeping only the records for
// which keep returns true, under the same lock and atomic replace as Append.
// When nothing is kept the file is deleted. The #480 materializer is the only
// caller: it is the capture's only deleter (ADR 027 §Lifecycle). A missing
// file is a no-op.
func Consume(runDir string, issue int, keep func(Record) bool) error {
	p, err := CapturePath(runDir, issue)
	if err != nil {
		return err
	}
	lf, err := os.OpenFile(p+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("open lock: %w", err)
	}
	defer lf.Close()
	if err := flock.Exclusive(lf, lockTimeout); err != nil && !errors.Is(err, flock.ErrUnsupported) {
		return fmt.Errorf("lock %s: %w", p, err)
	}
	defer flock.Unlock(lf) //nolint:errcheck // closing the file releases it too

	c, err := loadPath(p, issue)
	if err != nil || c == nil {
		return err
	}
	kept := []Record{}
	for _, r := range c.Records {
		if keep != nil && keep(r) {
			kept = append(kept, r)
		}
	}
	if len(kept) == 0 {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", p, err)
		}
		return nil
	}
	c.Records = kept
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if err := atomicfile.Write(p, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", p, err)
	}
	return nil
}

// Orphan is a capture file no run is going to materialize.
type Orphan struct {
	Path        string    `json:"path"`
	IssueNumber int       `json:"issue_number"`
	Reason      string    `json:"reason"`
	ModTime     time.Time `json:"mod_time"`
}

// Orphan reasons.
const (
	ReasonNoLiveRun = "no-live-run" // issue-{N}.json is gone: the run ended unmaterialized
	ReasonTooOld    = "too-old"     // older than maxAge
)

// Orphans lists capture files in runDir with no sibling issue-{N}.json, or
// older than maxAge. It only reports: nothing is deleted (ADR 027).
func Orphans(runDir string, now time.Time, maxAge time.Duration) ([]Orphan, error) {
	root, err := resolveRunDir(runDir)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", root, err)
	}
	var out []Orphan
	for _, e := range entries {
		m := captureNameRe.FindStringSubmatch(e.Name())
		if m == nil || !e.Type().IsRegular() {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil || n <= 0 {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", e.Name(), err)
		}
		o := Orphan{Path: filepath.Join(root, e.Name()), IssueNumber: n, ModTime: info.ModTime().UTC()}
		if _, err := os.Stat(filepath.Join(root, "issue-"+m[1]+".json")); errors.Is(err, os.ErrNotExist) {
			o.Reason = ReasonNoLiveRun
		} else if maxAge > 0 && now.Sub(info.ModTime()) > maxAge {
			o.Reason = ReasonTooOld
		} else {
			continue
		}
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].IssueNumber < out[j].IssueNumber })
	return out, nil
}

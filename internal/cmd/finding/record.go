// Package finding is the substrate for out-of-band findings (#102): learning
// signals an operator records while working beside the pipeline. Records are
// appended to <main-checkout>/.nightgauge/findings.jsonl, one JSON object per
// line, and read by continuous-improvement and feature-planning.
//
// ADR 027 keeps this separate from discovered work: findings are long-lived,
// repository-scoped learning signals; discovered work is run-scoped issue
// candidates consumed once. The record contract is schemas/finding-v1.json.
package finding

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/atomicfile"
	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/flock"
)

// SchemaVersion is the record version.
const SchemaVersion = "1"

// FileName is the findings file, relative to the main checkout.
const FileName = ".nightgauge/findings.jsonl"

const lockTimeout = 10 * time.Second

// Record is one out-of-band finding.
type Record struct {
	SchemaVersion string   `json:"schema_version"`
	ID            string   `json:"id"`
	Kind          string   `json:"kind"`
	Scope         string   `json:"scope"`
	Summary       string   `json:"summary"`
	Remedy        string   `json:"remedy"`
	Evidence      []string `json:"evidence"`
	Paths         []string `json:"paths"`
	RecordedAt    string   `json:"recorded_at"`
}

var (
	allowedKinds  = map[string]bool{"environment": true, "precondition": true, "api-shape": true, "gotcha": true}
	allowedScopes = map[string]bool{"project": true, "pipeline": true}
	idRe          = regexp.MustCompile(`^fd-[0-9a-f]{12}$`)
)

// Bounds shared with schemas/finding-v1.json.
const (
	maxSummary    = 200
	maxRemedy     = 2000
	maxEvidence   = 10
	maxEvidenceSz = 2000
	maxPaths      = 20
	maxPathLen    = 256
)

// ID is the deterministic record id: recording the same finding again
// collapses onto one record.
func ID(kind, scope, summary string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + scope + "\x00" + summary))
	return "fd-" + hex.EncodeToString(sum[:])[:12]
}

// Validate checks rec against the v1 contract. Errors name the field first
// ("<field>: <reason>") so a caller can surface them verbatim.
func Validate(rec *Record) error {
	if rec == nil {
		return errors.New("record: is nil")
	}
	if rec.SchemaVersion != SchemaVersion {
		return fmt.Errorf("schema_version: must be %q (got %q)", SchemaVersion, rec.SchemaVersion)
	}
	if !allowedKinds[rec.Kind] {
		return fmt.Errorf("kind: must be one of environment|precondition|api-shape|gotcha (got %q)", rec.Kind)
	}
	if !allowedScopes[rec.Scope] {
		return fmt.Errorf("scope: must be one of project|pipeline (got %q)", rec.Scope)
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
	if n := len([]rune(rec.Remedy)); n > maxRemedy {
		return fmt.Errorf("remedy: must be at most %d characters (got %d)", maxRemedy, n)
	}
	if err := checkPrintable(rec.Remedy, true); err != nil {
		return fmt.Errorf("remedy: %w", err)
	}
	if rec.Evidence == nil {
		return errors.New("evidence: is required (use [] for none)")
	}
	if len(rec.Evidence) > maxEvidence {
		return fmt.Errorf("evidence: at most %d entries (got %d)", maxEvidence, len(rec.Evidence))
	}
	for i, e := range rec.Evidence {
		if n := len([]rune(e)); n == 0 || n > maxEvidenceSz {
			return fmt.Errorf("evidence[%d]: must be 1-%d characters (got %d)", i, maxEvidenceSz, n)
		}
		if err := checkPrintable(e, true); err != nil {
			return fmt.Errorf("evidence[%d]: %w", i, err)
		}
	}
	if rec.Paths == nil {
		return errors.New("paths: is required (use [] for none)")
	}
	if len(rec.Paths) > maxPaths {
		return fmt.Errorf("paths: at most %d entries (got %d)", maxPaths, len(rec.Paths))
	}
	for i, p := range rec.Paths {
		if err := validatePath(p); err != nil {
			return fmt.Errorf("paths[%d]: %v", i, err)
		}
	}
	if !idRe.MatchString(rec.ID) {
		return fmt.Errorf("id: must match fd-<12 hex> (got %q)", rec.ID)
	}
	if want := ID(rec.Kind, rec.Scope, rec.Summary); rec.ID != want {
		return fmt.Errorf("id: %q does not match its kind, scope and summary (want %q)", rec.ID, want)
	}
	if _, err := time.Parse(time.RFC3339, rec.RecordedAt); err != nil || !strings.HasSuffix(rec.RecordedAt, "Z") {
		return fmt.Errorf("recorded_at: must be an RFC 3339 UTC timestamp (got %q)", rec.RecordedAt)
	}
	return nil
}

// validatePath accepts a repository-relative path or path.Match glob. The
// path is never opened; it is a matching hint.
func validatePath(p string) error {
	switch {
	case p == "":
		return errors.New("must not be empty")
	case len(p) > maxPathLen:
		return fmt.Errorf("must be at most %d characters", maxPathLen)
	case strings.ContainsRune(p, 0):
		return errors.New("must not contain NUL")
	case strings.HasPrefix(p, "-"):
		return errors.New("must not start with '-'")
	case filepath.IsAbs(p) || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) || hasDrivePrefix(p):
		return errors.New("must be repository-relative, not absolute")
	}
	for _, seg := range strings.Split(path.Clean(strings.ReplaceAll(p, `\`, "/")), "/") {
		if seg == ".." {
			return errors.New("must not contain a '..' segment")
		}
	}
	if _, err := path.Match(p, ""); err != nil {
		return fmt.Errorf("is not a valid glob: %v", err)
	}
	return checkPrintable(p, false)
}

func hasDrivePrefix(p string) bool {
	return len(p) >= 2 && p[1] == ':' && ((p[0] >= 'A' && p[0] <= 'Z') || (p[0] >= 'a' && p[0] <= 'z'))
}

// checkPrintable rejects C0/C1 control characters, DEL and bidi override code
// points, so a record printed to a terminal cannot carry escape sequences.
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

// Path is the findings file in the main checkout that owns cwd. Resolving to
// the main checkout keeps a record written from a pipeline worktree alive
// after the worktree is removed (#2535).
func Path(cwd string) (string, error) {
	root := config.MainCheckoutRoot(cwd)
	if root == "" {
		return "", fmt.Errorf("findings file: %s is not inside a git checkout", cwd)
	}
	return filepath.Join(root, filepath.FromSlash(FileName)), nil
}

// List reads and validates every record in file. An absent file is an empty
// list, and nothing is created.
func List(file string) ([]Record, error) {
	data, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return []Record{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", file, err)
	}
	return decode(file, data)
}

func decode(file string, data []byte) ([]Record, error) {
	out := []Record{}
	seen := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for n := 1; sc.Scan(); n++ {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.DisallowUnknownFields()
		var r Record
		if err := dec.Decode(&r); err != nil {
			return nil, fmt.Errorf("%s:%d: decode: %w", file, n, err)
		}
		if dec.More() {
			return nil, fmt.Errorf("%s:%d: decode: trailing data after the record", file, n)
		}
		if err := Validate(&r); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", file, n, err)
		}
		if seen[r.ID] {
			return nil, fmt.Errorf("%s:%d: id: duplicate %q", file, n, r.ID)
		}
		seen[r.ID] = true
		out = append(out, r)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", file, err)
	}
	return out, nil
}

// Append validates rec and adds it to file under an exclusive lock with an
// atomic replace. SchemaVersion, ID and RecordedAt are set when empty, and nil
// slices become []. It reports added=false when a record with the same id is
// already present. On any error the file is untouched.
func Append(file string, rec Record, now time.Time) (Record, bool, error) {
	if rec.SchemaVersion == "" {
		rec.SchemaVersion = SchemaVersion
	}
	if rec.ID == "" {
		rec.ID = ID(rec.Kind, rec.Scope, rec.Summary)
	}
	if rec.RecordedAt == "" {
		rec.RecordedAt = now.UTC().Format(time.RFC3339)
	}
	if rec.Evidence == nil {
		rec.Evidence = []string{}
	}
	if rec.Paths == nil {
		rec.Paths = []string{}
	}
	if err := Validate(&rec); err != nil {
		return rec, false, err
	}
	if fi, err := os.Lstat(file); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return rec, false, fmt.Errorf("findings file %s: is a symlink; refusing to follow it", file)
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return rec, false, fmt.Errorf("create %s: %w", filepath.Dir(file), err)
	}

	lf, err := os.OpenFile(file+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return rec, false, fmt.Errorf("open lock: %w", err)
	}
	defer lf.Close()
	if err := flock.Exclusive(lf, lockTimeout); err != nil && !errors.Is(err, flock.ErrUnsupported) {
		return rec, false, fmt.Errorf("lock %s: %w", file, err)
	}
	defer flock.Unlock(lf) //nolint:errcheck // closing the file releases it too

	existing, err := List(file)
	if err != nil {
		return rec, false, err
	}
	for _, e := range existing {
		if e.ID == rec.ID {
			return e, false, nil
		}
	}
	var buf bytes.Buffer
	for _, r := range append(existing, rec) {
		line, err := json.Marshal(r)
		if err != nil {
			return rec, false, fmt.Errorf("marshal: %w", err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if err := atomicfile.Write(file, buf.Bytes(), 0o644); err != nil {
		return rec, false, fmt.Errorf("write %s: %w", file, err)
	}
	return rec, true, nil
}

// Filter keeps records matching kind and scope (empty means any) and, when
// matchPaths is non-empty, at least one target path (see Matches).
func Filter(recs []Record, kind, scope string, matchPaths []string) []Record {
	out := []Record{}
	for _, r := range recs {
		if kind != "" && r.Kind != kind {
			continue
		}
		if scope != "" && r.Scope != scope {
			continue
		}
		if len(matchPaths) > 0 && !Matches(r, matchPaths) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// Matches reports whether r concerns any target path t: an entry p in Paths
// equals t, is a directory prefix of t, or globs it; or t appears verbatim in
// the summary, remedy or an evidence entry.
func Matches(r Record, targets []string) bool {
	for _, t := range targets {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		for _, p := range r.Paths {
			if t == p || strings.HasPrefix(t, strings.TrimSuffix(p, "/")+"/") {
				return true
			}
			if ok, _ := path.Match(p, t); ok {
				return true
			}
		}
		if strings.Contains(r.Summary, t) || strings.Contains(r.Remedy, t) {
			return true
		}
		for _, e := range r.Evidence {
			if strings.Contains(e, t) {
				return true
			}
		}
	}
	return false
}

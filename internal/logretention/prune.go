// Package logretention bounds Nightgauge's log directories by size and by age
// (ADR-024 § 11, #2029).
//
// One function, Prune, enforces the policy on one directory: files older than
// the age cap go first, then the oldest remaining files until the directory is
// under its size cap. It deletes whole files only, regular files only, and only
// entries of the resolved directory itself; it never deletes a file that a
// long-lived writer holds open, a file written within the last hour, or a file
// that belongs to a run that is not terminal. When only such files remain over
// the cap, it stops and reports the directory as over its cap, which
// `nightgauge doctor` surfaces.
package logretention

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/github"
)

// Defaults recorded by ADR-024 § 11.
const (
	DefaultMaxSizeMB  = 200
	DefaultMaxAgeDays = 30
)

// RecentWindow is how recently a file may have been written and still be
// deleted. A file written inside it is treated as possibly open: several
// writers (the extension's session log, the hooks' sanitization log) open,
// append and close on every event, so "open right now" is not observable from
// another process, and "written in the last hour" is the portable stand-in.
const RecentWindow = time.Hour

// LiveFiles are the logs in a pruned directory that a long-lived process holds
// open at now: the current UTC day's GitHub request ledger segment,
// github-api-YYYY-MM-DD.jsonl. Retention never deletes it. Earlier segments,
// every size backup (*.1) and a pre-segment github-api.jsonl are ordinary
// prunable files: the ledger is written in dated segments precisely so
// whole-file pruning can bound it. `serve`'s go-backend.log is not here: it
// lives in the per-checkout directory (ADR-024 § 7), which retention does not
// prune, and is bounded by its own 5 MB rotation on serve start.
func LiveFiles(now time.Time) []string {
	return []string{github.LedgerSegmentName(now)}
}

// Policy is the pair of caps applied to each log directory.
type Policy struct {
	// MaxBytes is the total size cap. Zero or negative disables it.
	MaxBytes int64
	// MaxAge is the maximum file age. Zero or negative disables it.
	MaxAge time.Duration
}

// DefaultPolicy is the ADR-024 § 11 policy: 200 MB and 30 days.
func DefaultPolicy() Policy {
	return Policy{
		MaxBytes: DefaultMaxSizeMB << 20,
		MaxAge:   DefaultMaxAgeDays * 24 * time.Hour,
	}
}

// Options controls one Prune call.
type Options struct {
	Policy Policy
	// Now is the clock the age cap and the recency guard read. Zero means
	// time.Now().
	Now time.Time
	// Open names files (base names) that a writer holds open. They are never
	// deleted. Nil means LiveFiles(Now).
	Open []string
	// Recent is the recency guard: files modified within it are kept. Zero
	// means RecentWindow; a negative value disables the guard (tests only).
	Recent time.Duration
	// InFlight reports whether an issue has a run that is not terminal. Nil
	// means the in-flight set could not be determined, and then every
	// issue-keyed file is kept: a guess that deletes a paused run's logs is
	// not recoverable, a guess that keeps them is.
	InFlight func(issue int) bool
	// DryRun computes the result without deleting anything.
	DryRun bool
}

// File is one regular file Prune considered.
type File struct {
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
	// Kept names why the file may not be deleted: "open", "recent",
	// "in-flight run #N" or "run state undetermined". Empty when prunable.
	Kept string `json:"kept,omitempty"`
}

// Result describes one Prune call.
type Result struct {
	// Dir is the resolved directory that was pruned.
	Dir string `json:"dir"`
	// Files is the number of regular files present before pruning.
	Files int `json:"files"`
	// SizeBefore and SizeAfter are the total bytes of those regular files.
	SizeBefore int64 `json:"size_before"`
	SizeAfter  int64 `json:"size_after"`
	// Deleted lists the files removed (or, with DryRun, that would be).
	Deleted []File `json:"deleted"`
	// Kept lists every file a guard protected.
	Kept []File `json:"kept"`
	// Refused names entries that are not regular files (symlinks,
	// directories, devices). They are never followed or deleted.
	Refused []string `json:"refused,omitempty"`
	// OverCap is true when the directory is still above its size cap after
	// pruning because only kept files remain.
	OverCap bool `json:"over_cap"`
	// Errors records files that could not be removed. They still count in
	// SizeAfter.
	Errors []string `json:"errors,omitempty"`
}

// ErrSymlinkDir reports a log directory whose final component is a symlink.
// Following it would let a link aim retention at any directory the user can
// write to, so it is refused.
var ErrSymlinkDir = errors.New("log directory is a symlink")

// ErrNotAbsolute reports a relative log directory, which would resolve
// against whichever working directory the process happens to have.
var ErrNotAbsolute = errors.New("log directory must be an absolute path")

// issueKeyRE matches the issue-keyed session log names the extension writes:
// YYYY-MM-DD_NNN_session.log and YYYY-MM-DD_NNN_<extra>_session.log.
var issueKeyRE = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}_(\d+)_`)

// IssueOf returns the issue number a log file name is keyed by.
func IssueOf(name string) (int, bool) {
	m := issueKeyRE.FindStringSubmatch(name)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// Prune applies opts.Policy to the log directory dir. An absent directory is
// an empty result, not an error.
//
// Deletion is bounded and confined: dir must be absolute and its final
// component must not be a symlink; only its direct entries are considered,
// never recursively; an entry is deleted only when it is a regular file both
// when listed and immediately before removal, and is the same file both times.
// Dot files (.gitkeep, the prune marker) are neither counted nor deleted.
func Prune(dir string, opts Options) (Result, error) {
	res := Result{Deleted: []File{}, Kept: []File{}}
	resolved, err := resolveDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return res, nil
		}
		return res, err
	}
	res.Dir = resolved

	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	recent := opts.Recent
	if recent == 0 {
		recent = RecentWindow
	}
	open := opts.Open
	if open == nil {
		open = LiveFiles(now)
	}
	openSet := make(map[string]bool, len(open))
	for _, n := range open {
		openSet[n] = true
	}

	entries, err := os.ReadDir(resolved)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return res, nil
		}
		return res, fmt.Errorf("logretention: read %s: %w", resolved, err)
	}

	type candidate struct {
		File
		info fs.FileInfo
	}
	var prunable []candidate
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || !confinedName(name) {
			continue
		}
		info, err := os.Lstat(filepath.Join(resolved, name))
		if err != nil {
			continue // vanished between the listing and the stat
		}
		if !info.Mode().IsRegular() {
			res.Refused = append(res.Refused, name)
			continue
		}
		f := File{Name: name, Size: info.Size(), ModTime: info.ModTime()}
		res.Files++
		res.SizeBefore += f.Size
		f.Kept = keepReason(name, info.ModTime(), now, recent, openSet, opts.InFlight)
		if f.Kept != "" {
			res.Kept = append(res.Kept, f)
			continue
		}
		prunable = append(prunable, candidate{File: f, info: info})
	}
	res.SizeAfter = res.SizeBefore

	// Oldest first; the name breaks mtime ties so the order is stable.
	sort.Slice(prunable, func(i, j int) bool {
		if !prunable[i].ModTime.Equal(prunable[j].ModTime) {
			return prunable[i].ModTime.Before(prunable[j].ModTime)
		}
		return prunable[i].Name < prunable[j].Name
	})

	remove := func(c candidate) {
		if !opts.DryRun {
			if err := removeConfined(resolved, c.Name, c.info); err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", c.Name, err))
				return
			}
		}
		res.Deleted = append(res.Deleted, c.File)
		res.SizeAfter -= c.Size
	}

	var rest []candidate
	for _, c := range prunable {
		if opts.Policy.MaxAge > 0 && now.Sub(c.ModTime) > opts.Policy.MaxAge {
			remove(c)
			continue
		}
		rest = append(rest, c)
	}
	if opts.Policy.MaxBytes > 0 {
		for _, c := range rest {
			if res.SizeAfter <= opts.Policy.MaxBytes {
				break
			}
			remove(c)
		}
		res.OverCap = res.SizeAfter > opts.Policy.MaxBytes
	}
	return res, nil
}

// keepReason returns why a file must survive, or "" when it may be pruned.
func keepReason(name string, mod, now time.Time, recent time.Duration, open map[string]bool, inFlight func(int) bool) string {
	if open[name] {
		return "open"
	}
	if recent > 0 && now.Sub(mod) < recent {
		return "recent"
	}
	if issue, ok := IssueOf(name); ok {
		if inFlight == nil {
			return "run state undetermined"
		}
		if inFlight(issue) {
			return "in-flight run #" + strconv.Itoa(issue)
		}
	}
	return ""
}

// resolveDir validates dir and returns it with every symlink in its parents
// resolved. The final component itself must be a real directory.
func resolveDir(dir string) (string, error) {
	if dir == "" || !filepath.IsAbs(dir) {
		return "", fmt.Errorf("logretention: %w: %q", ErrNotAbsolute, dir)
	}
	clean := filepath.Clean(dir)
	info, err := os.Lstat(clean)
	if err != nil {
		return "", err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return "", fmt.Errorf("logretention: %w: %s", ErrSymlinkDir, clean)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("logretention: %s is not a directory", clean)
	}
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return "", err
	}
	return resolved, nil
}

// confinedName reports whether name is a plain entry of its directory: no
// separator and no parent reference, so joining it cannot leave the directory.
func confinedName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	return filepath.Base(name) == name && !strings.ContainsAny(name, `/\`)
}

// removeConfined deletes dir/name only if it is still the regular file that
// was listed. A swap to a symlink or to another file between the listing and
// the removal is refused, and the path must stay inside dir.
func removeConfined(dir, name string, listed fs.FileInfo) error {
	if !confinedName(name) {
		return fmt.Errorf("refused: %q is not a plain directory entry", name)
	}
	path := filepath.Join(dir, name)
	if filepath.Dir(path) != dir {
		return fmt.Errorf("refused: %s resolves outside %s", path, dir)
	}
	now, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil // already gone: the goal state
		}
		return err
	}
	if !now.Mode().IsRegular() {
		return fmt.Errorf("refused: %s is no longer a regular file", path)
	}
	// Device and inode alone are not identity: Linux hands a freed inode to
	// the next file created, so a delete-and-recreate at the same name can
	// match. Size and modification time must match too.
	if !os.SameFile(now, listed) || now.Size() != listed.Size() || !now.ModTime().Equal(listed.ModTime()) {
		return fmt.Errorf("refused: %s was replaced after it was listed", path)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

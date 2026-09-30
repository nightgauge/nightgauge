package logretention

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/nightgauge/nightgauge/internal/atomicfile"
	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/layout"
	"github.com/nightgauge/nightgauge/internal/state"
)

// Machine-tier keys for the caps (ADR-024 § 11).
const (
	KeyMaxSizeMB  = "pipeline.logs.max_size_mb"
	KeyMaxAgeDays = "pipeline.logs.max_age_days"
)

// Interval is how often retention runs: at `serve` start and then daily while
// it runs, and at CLI start only when the last prune is older than this.
const Interval = 24 * time.Hour

// MarkerName is the file in each log directory whose mtime records the last
// prune. It is a dot file, so Prune never counts or deletes it, and the
// .nightgauge/.gitignore `logs/*` rule keeps it out of git.
const MarkerName = ".last-prune"

// ConfiguredPolicy is the policy in effect with where each cap came from.
type ConfiguredPolicy struct {
	Policy
	// SizeSource and AgeSource are "default" or the machine config path.
	SizeSource string
	AgeSource  string
	// Warnings names machine-tier values that were present but unusable; the
	// default applies in their place.
	Warnings []string
}

// LoadPolicy reads the caps from the machine tier, falling back to the
// ADR-024 defaults. A missing, non-integer or non-positive value leaves the
// default in effect; an unusable one is named in Warnings.
func LoadPolicy() ConfiguredPolicy {
	cp := ConfiguredPolicy{Policy: DefaultPolicy(), SizeSource: "default", AgeSource: "default"}
	if mb, src, warn := machineInt(KeyMaxSizeMB); mb > 0 {
		cp.MaxBytes, cp.SizeSource = int64(mb)<<20, src
	} else if warn != "" {
		cp.Warnings = append(cp.Warnings, warn)
	}
	if days, src, warn := machineInt(KeyMaxAgeDays); days > 0 {
		cp.MaxAge, cp.AgeSource = time.Duration(days)*24*time.Hour, src
	} else if warn != "" {
		cp.Warnings = append(cp.Warnings, warn)
	}
	return cp
}

func machineInt(key string) (int, string, string) {
	v, path, err := config.ReadMachineString(key)
	if err != nil {
		return 0, "", fmt.Sprintf("%s: %v (default applies)", key, err)
	}
	if v == "" {
		return 0, "", ""
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, "", fmt.Sprintf("%s in %s is %q, not a positive integer (default applies)", key, path, v)
	}
	return n, path, ""
}

// Target is one log directory under retention.
type Target struct {
	// Label is "clone" (CLONE/logs) or "machine" (STATE/logs).
	Label string
	Dir   string
	// root is the repository root whose run state guards the clone
	// directory's issue-keyed files. Empty for the machine directory, whose
	// in-flight set is undetermined, so its issue-keyed files are all kept.
	root string
}

// Targets returns the log directories retention covers for workspaceRoot:
// CLONE/logs of that root (when the root is absolute and in a git repository)
// and STATE/logs. Neither log directory is created; resolving CLONE/logs
// creates the empty CLONE root (mode 0700) as every per-clone resolution does.
func Targets(workspaceRoot string) []Target {
	var out []Target
	if workspaceRoot != "" {
		if dir, err := layout.CloneLogsDir(workspaceRoot); err == nil {
			out = append(out, Target{Label: "clone", Dir: dir, root: workspaceRoot})
		}
	}
	if home, err := layout.StateHomePath(); err == nil {
		out = append(out, Target{Label: "machine", Dir: filepath.Join(home, "logs")})
	}
	return out
}

// InFlight returns the in-flight predicate for t, or nil when it cannot be
// determined. It reads the run snapshots of t's repository (ADR-024 § 15: a
// run that is running, queued, paused or parked is in flight).
func (t Target) InFlight() func(int) bool {
	if t.root == "" {
		return nil
	}
	main := config.MainCheckoutRoot(t.root)
	if main == "" {
		main = t.root
	}
	dir, err := state.PipelineStateDir(main)
	if err != nil {
		return nil
	}
	active, err := state.ActiveIssuesFromSnapshots(dir)
	if err != nil {
		return nil
	}
	issues := active.Issues
	return func(n int) bool { return issues[n] }
}

// LastPrune returns when dir was last pruned, or the zero time.
func LastPrune(dir string) time.Time {
	info, err := os.Lstat(filepath.Join(dir, MarkerName))
	if err != nil || !info.Mode().IsRegular() {
		return time.Time{}
	}
	return info.ModTime()
}

// Due reports whether dir exists and was last pruned more than Interval ago.
// It costs two stats, so it is safe on every command's startup path.
func Due(dir string, now time.Time) bool {
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() {
		return false
	}
	last := LastPrune(dir)
	return last.IsZero() || now.Sub(last) > Interval
}

// markPruned records a prune. The marker is replaced by rename, so a symlink
// planted at its name is replaced rather than followed.
func markPruned(dir string, now time.Time) error {
	path := filepath.Join(dir, MarkerName)
	if err := atomicfile.Write(path, []byte(now.UTC().Format(time.RFC3339)+"\n"), 0o600); err != nil {
		return err
	}
	return os.Chtimes(path, now, now)
}

// PruneTarget prunes one target with the machine-tier policy and records the
// prune. An absent directory is skipped.
func PruneTarget(t Target, now time.Time, dryRun bool) (Result, error) {
	cp := LoadPolicy()
	res, err := Prune(t.Dir, Options{Policy: cp.Policy, Now: now, InFlight: t.InFlight(), DryRun: dryRun})
	if err != nil || res.Dir == "" || dryRun {
		return res, err
	}
	if merr := markPruned(res.Dir, now); merr != nil {
		return res, fmt.Errorf("logretention: record prune in %s: %w", res.Dir, merr)
	}
	return res, nil
}

// Logf is the logging callback the runners take (log.Printf fits).
type Logf func(format string, args ...any)

// PruneAll prunes every target of workspaceRoot now. force=false skips a
// directory pruned within Interval: that is the CLI-start path.
func PruneAll(workspaceRoot string, now time.Time, force bool, logf Logf) {
	for _, t := range Targets(workspaceRoot) {
		if !force && !Due(t.Dir, now) {
			continue
		}
		res, err := PruneTarget(t, now, false)
		report(t, res, err, logf)
	}
}

// RunDaemon prunes at once and then every interval until ctx ends. `serve`
// runs it in a goroutine so startup never waits on a directory walk.
func RunDaemon(ctx context.Context, workspaceRoot string, interval time.Duration, logf Logf) {
	PruneAll(workspaceRoot, time.Now(), true, logf)
	if interval <= 0 {
		return
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			PruneAll(workspaceRoot, now, true, logf)
		}
	}
}

func report(t Target, res Result, err error, logf Logf) {
	if logf == nil {
		return
	}
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			logf("logretention: %s logs %s: %v", t.Label, t.Dir, err)
		}
		return
	}
	if len(res.Deleted) > 0 {
		logf("logretention: %s logs %s: pruned %d file(s), %s -> %s",
			t.Label, res.Dir, len(res.Deleted), FormatBytes(res.SizeBefore), FormatBytes(res.SizeAfter))
	}
	if res.OverCap {
		logf("logretention: %s logs %s: still over the size cap at %s; only open, recent or unfinished-run files remain",
			t.Label, res.Dir, FormatBytes(res.SizeAfter))
	}
	for _, e := range res.Errors {
		logf("logretention: %s logs: %s", t.Label, e)
	}
}

// FormatBytes renders a byte count in binary units, one decimal.
func FormatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

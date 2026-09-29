package doctor

// The `log_retention` check (#2029, ADR-024 § 11).
//
// Retention runs unattended (at `serve` start, daily while it runs, and at CLI
// start once a day), so the operator needs one place that says how big the log
// directories are, which caps apply and where they came from, and whether
// retention is able to keep them under those caps. The detail line carries
// the sizes and caps on every run; findings appear only when a directory is
// over its size cap or cannot be pruned at all.

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/logretention"
)

const codeLogRetention = "NGD043"

func init() {
	builtinChecks = append(builtinChecks, Check{
		ID: "log_retention", Title: "Log retention", Group: "hygiene", Code: codeLogRetention,
		Run: func(ctx context.Context, env *Env) []Finding {
			fs, detail := logRetentionFindings(env.Cwd, env.Now)
			env.SetDetail("log_retention", detail)
			return fs
		},
	})
}

// logRetentionFindings measures each log directory with a dry-run prune under
// the configured policy. It never deletes anything.
func logRetentionFindings(workspaceRoot string, now time.Time) ([]Finding, string) {
	const check = "log_retention"
	if now.IsZero() {
		now = time.Now()
	}
	root := ""
	if workspaceRoot != "" {
		if abs, err := filepath.Abs(workspaceRoot); err == nil {
			root = abs
		}
	}
	cp := logretention.LoadPolicy()
	capBytes := logretention.FormatBytes(cp.MaxBytes)
	capDays := int(cp.MaxAge / (24 * time.Hour))
	caps := fmt.Sprintf("caps %s (%s) and %d days (%s)", capBytes, sourceLabel(cp.SizeSource), capDays, sourceLabel(cp.AgeSource))

	var parts []string
	var out []Finding
	for _, t := range logretention.Targets(root) {
		res, err := logretention.Prune(t.Dir, logretention.Options{
			Policy: cp.Policy, Now: now, InFlight: t.InFlight(), DryRun: true,
		})
		if err != nil {
			parts = append(parts, fmt.Sprintf("%s logs unreadable: %v", t.Label, err))
			if errors.Is(err, logretention.ErrSymlinkDir) {
				out = append(out, newFinding(check, codeLogRetention, SeverityWarning,
					fmt.Sprintf("log-dir-symlink: the %s log directory is a symlink, so retention refuses it", t.Label),
					fmt.Sprintf("%s is a symlink. Retention deletes only inside a real directory, so a link "+
						"cannot aim it elsewhere; until it is a directory, these logs grow without limit", t.Dir),
					map[string]string{"dir": t.Dir, "label": t.Label},
					[]string{t.Dir, "symlink"},
					manualRemedy("replace", "Replace the symlink with a real directory", check,
						"Move the files the link points at into a new directory at "+t.Dir,
						"Remove the symlink and re-run `nightgauge doctor --only "+codeLogRetention+"`")))
			}
			continue
		}
		if res.Dir == "" {
			parts = append(parts, t.Label+" logs absent")
			continue
		}
		part := fmt.Sprintf("%s logs %s in %d file(s)", t.Label, logretention.FormatBytes(res.SizeBefore), res.Files)
		if last := logretention.LastPrune(res.Dir); !last.IsZero() {
			part += ", last pruned " + last.UTC().Format(time.RFC3339)
		}
		parts = append(parts, part)

		ev := map[string]string{
			"dir":        res.Dir,
			"size_bytes": strconv.FormatInt(res.SizeBefore, 10),
			"files":      strconv.Itoa(res.Files),
			"max_bytes":  strconv.FormatInt(cp.MaxBytes, 10),
			"max_days":   strconv.Itoa(capDays),
		}
		switch {
		case res.OverCap:
			ev["kept_bytes"] = strconv.FormatInt(res.SizeAfter, 10)
			ev["kept"] = keptSummary(res.Kept)
			out = append(out, newFinding(check, codeLogRetention, SeverityWarning,
				fmt.Sprintf("log-dir-over-cap: %s logs hold %s against a %s cap, and retention may delete none of the rest",
					t.Label, logretention.FormatBytes(res.SizeAfter), capBytes),
				"every file that would bring the directory under its cap is live, written in the last hour, "+
					"or belongs to a run that is not terminal (running, queued, paused or parked), and "+
					"retention never deletes those. Finish or cancel the runs named in the evidence, "+
					"or raise "+logretention.KeyMaxSizeMB+" in the machine config",
				ev, []string{res.Dir, "over-cap"},
				manualRemedy("review", "Finish or cancel the runs holding the logs", check,
					"Resolve the runs the evidence names (kept files and why)",
					"Run `nightgauge logs prune`, then `nightgauge doctor --only "+codeLogRetention+"`")))
		case cp.MaxBytes > 0 && res.SizeBefore > cp.MaxBytes:
			out = append(out, newFinding(check, codeLogRetention, SeverityHousekeeping,
				fmt.Sprintf("log-dir-prune-due: %s logs hold %s against a %s cap",
					t.Label, logretention.FormatBytes(res.SizeBefore), capBytes),
				fmt.Sprintf("a prune would delete %d file(s) and bring the directory to %s. Retention runs at "+
					"`serve` start, daily while it runs, and at CLI start once a day, so it has not run here since the directory grew",
					len(res.Deleted), logretention.FormatBytes(res.SizeAfter)),
				ev, []string{res.Dir, "prune-due"},
				manualRemedy("prune", "Prune the log directory now", check,
					"Run `nightgauge logs prune` from the repository")))
		}
	}
	for _, w := range cp.Warnings {
		parts = append(parts, "ignored "+w)
	}
	return out, strings.Join(append(parts, caps), "; ")
}

func sourceLabel(src string) string {
	if src == "" || src == "default" {
		return "default"
	}
	return "from " + src
}

// keptSummary names at most five kept files with the guard that kept each.
func keptSummary(kept []logretention.File) string {
	var b strings.Builder
	for i, f := range kept {
		if i == 5 {
			fmt.Fprintf(&b, ", and %d more", len(kept)-5)
			break
		}
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%s (%s, %s)", f.Name, logretention.FormatBytes(f.Size), f.Kept)
	}
	return b.String()
}

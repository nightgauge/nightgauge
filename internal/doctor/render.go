package doctor

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"golang.org/x/term"
)

// RenderOptions controls the human renderer.
type RenderOptions struct {
	// Color enables ANSI color. Use ColorEnabled to decide it.
	Color bool
	// Adapters renders the per-adapter section, when the result carries one.
	Adapters func(w io.Writer, adapters []AdapterHealth)
}

// ColorEnabled reports whether w may receive ANSI color: never when NO_COLOR
// is set, TERM is "dumb", or w is not an interactive terminal.
func ColorEnabled(w io.Writer) bool {
	if _, set := os.LookupEnv("NO_COLOR"); set {
		return false
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// severityStyle pairs every severity with a text label and a symbol, so color
// is never the only signal (ADR-025 § 6).
var severityStyle = map[Severity]struct{ label, symbol, color string }{
	SeverityBlocker:      {"BLOCKER", "✗", "\x1b[31m"},
	SeverityWarning:      {"WARNING", "⚠", "\x1b[33m"},
	SeverityHousekeeping: {"HOUSEKEEPING", "•", "\x1b[36m"},
	SeverityInfo:         {"INFO", "i", "\x1b[2m"},
}

var statusStyle = map[CheckStatus]struct{ label, symbol, color string }{
	StatusPassed:  {"OK", "✓", "\x1b[32m"},
	StatusFailed:  {"FINDING", "✗", "\x1b[31m"},
	StatusSkipped: {"SKIPPED", "-", "\x1b[2m"},
	StatusTimeout: {"INCOMPLETE", "?", "\x1b[33m"},
}

const ansiReset = "\x1b[0m"

func paint(on bool, color, s string) string {
	if !on {
		return s
	}
	return color + s + ansiReset
}

// firstLine returns s up to its first newline.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// RenderHuman writes the severity-grouped human report. It is driven by the
// registry results, so every registered check renders.
func RenderHuman(w io.Writer, res DoctorResult, opts RenderOptions) {
	fmt.Fprintf(w, "nightgauge doctor — schema v%d\n", res.V)
	s := res.Summary
	fmt.Fprintf(w, "%d blocker, %d warning, %d housekeeping, %d info\n",
		s.Blocker, s.Warning, s.Housekeeping, s.Info)

	for _, sev := range Severities {
		var group []Finding
		for _, f := range res.Findings {
			if f.Severity == sev {
				group = append(group, f)
			}
		}
		if len(group) == 0 {
			continue
		}
		st := severityStyle[sev]
		fmt.Fprintf(w, "\n%s (%d)\n", paint(opts.Color, st.color, st.label), len(group))
		for _, f := range group {
			fmt.Fprintf(w, "  %s %s  %s  %s  %s\n", paint(opts.Color, st.color, st.symbol), st.label,
				f.Code, f.Check, firstLine(f.Title))
			if c := firstLine(f.Cause); c != "" && c != firstLine(f.Title) {
				fmt.Fprintf(w, "      cause: %s\n", c)
			}
			keys := make([]string, 0, len(f.Evidence))
			for k := range f.Evidence {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				fmt.Fprintf(w, "      %s: %s\n", k, firstLine(f.Evidence[k]))
			}
			if len(f.Remedies) > 0 {
				fmt.Fprintf(w, "      fix: %s\n", f.Remedies[0].Summary)
			}
		}
	}

	fmt.Fprintln(w, "\nChecks:")
	width := 0
	for _, r := range res.Results {
		if len(r.ID) > width {
			width = len(r.ID)
		}
	}
	for _, r := range res.Results {
		st := statusStyle[r.Status]
		detail := r.Detail
		if r.Status != StatusPassed && len(r.Findings) > 0 {
			detail = r.Findings[0].Code + " " + firstLine(r.Findings[0].Title)
		}
		fmt.Fprintf(w, "  %s %-10s  %-*s  %s\n", paint(opts.Color, st.color, st.symbol), st.label,
			width, r.ID, firstLine(detail))
	}

	if len(res.Adapters) > 0 && opts.Adapters != nil {
		fmt.Fprintln(w, "\nAdapters:")
		opts.Adapters(w, res.Adapters)
	}
	if res.InstallInstructions != "" {
		fmt.Fprintf(w, "\n%s\n", res.InstallInstructions)
	}

	fmt.Fprintln(w)
	switch res.ExitCode {
	case 0:
		fmt.Fprintln(w, "Status: healthy — environment ready for pipeline operations")
	case 1:
		// No usable adapter means no stage can run at all (#862).
		if hasFinding(res, "ai_adapter", SeverityWarning) {
			fmt.Fprintln(w, "Status: degraded — no AI coding agent, so no pipeline stage can run; other checks passed")
		} else {
			fmt.Fprintln(w, "Status: degraded — pipeline will run but some features may be limited")
		}
	default:
		fmt.Fprintln(w, "Status: broken — fix the blockers above before running pipeline skills")
	}
}

func hasFinding(res DoctorResult, check string, sev Severity) bool {
	for _, f := range res.Findings {
		if f.Check == check && f.Severity == sev {
			return true
		}
	}
	return false
}

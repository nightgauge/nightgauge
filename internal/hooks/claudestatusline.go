package hooks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/usagestore"
)

// ClaudeStatusLine implements the `nightgauge hook claude-statusline` verb: a
// Claude Code statusLine command that records the operator's subscription
// rate-limit utilization on the way past, then renders a status line.
//
// # Why the status line is the feed
//
// Nightgauge's footer meter can only show a Claude Max allowance if something
// tells it what that allowance is. The rate_limit_event envelope on
// nightgauge's own `claude -p` stream (Issue #709) says so, but only while a
// pipeline stage is streaming — which is a small and unpredictable fraction of
// the time the operator is looking at the footer. Between runs the meter falls
// back to locally-derived dollar windows, which describe pay-per-token billing
// and answer a question a subscriber did not ask (Issue #730).
//
// Claude Code's statusLine contract carries the same account-wide figure:
//
//	"rate_limits": {
//	  "five_hour": { "used_percentage": number, "resets_at": number },
//	  "seven_day": { "used_percentage": number, "resets_at": number }
//	}
//
// It is delivered on every statusline render of every session, in any
// repository, whether or not nightgauge is running anything. Unlike
// rate_limit_event it is a documented input contract rather than a
// reverse-engineered stream detail. Wiring this verb in as the statusLine
// command therefore turns an occasional reading into a continuous one.
//
// # What it must never do
//
// A statusLine command runs on the operator's every render. Nothing here is
// permitted to fail one: malformed input, an absent rate_limits block (the
// normal case for a non-subscriber, and for a subscriber before the session's
// first API response), an unwritable store, or a delegate that exits non-zero
// all degrade to a stderr note and a best-effort line on stdout.

// statusLineInput is the subset of Claude Code's statusLine payload this verb
// reads. Every field is optional: the payload has grown over time and will
// keep growing, and a field this verb has not heard of must not stop it from
// using the ones it has.
type statusLineInput struct {
	Model struct {
		DisplayName string `json:"display_name"`
	} `json:"model"`
	Workspace struct {
		CurrentDir string `json:"current_dir"`
		ProjectDir string `json:"project_dir"`
	} `json:"workspace"`
	// RateLimits is absent for non-subscribers, and for a subscriber until the
	// session's first API response. Absent is a normal state, not an error.
	RateLimits *struct {
		FiveHour *statusLineBucket `json:"five_hour"`
		SevenDay *statusLineBucket `json:"seven_day"`
	} `json:"rate_limits"`
}

type statusLineBucket struct {
	// UsedPercentage is 0-100 as the vendor reported it. A pointer so an
	// explicit 0 ("you have used none of this window") is distinguishable from
	// a field that was not sent — the first is a real measured zero, the
	// second is nothing to say.
	UsedPercentage *float64 `json:"used_percentage"`
	// ResetsAt is unix epoch seconds.
	ResetsAt int64 `json:"resets_at"`
}

// bucketName maps the payload's field onto the vendor bucket name the store
// and ClaudeRateLimitUsageProvider already key on. Using the same names as the
// rate_limit_event channel is what lets one store serve both writers.
const (
	bucketFiveHour = "five_hour"
	bucketSevenDay = "seven_day"
)

// statusLineStatus is recorded in place of the stream envelope's status field,
// which the statusLine payload has no equivalent for. Deriving one from the
// percentage would put a threshold this code chose behind a field the UI reads
// as the vendor's own verdict.
const statusLineStatus = "unknown"

// ClaudeStatusLineOptions configures one invocation.
type ClaudeStatusLineOptions struct {
	// Delegate is the operator's pre-existing statusLine command, if any. When
	// set it is run with the same stdin payload and its stdout becomes this
	// verb's stdout, so adopting the usage feed never costs an operator the
	// status line they already had.
	Delegate string
	// AccountRoot overrides the store's account root. Empty means the current
	// user's home directory. Tests set it; production does not.
	AccountRoot string
}

// ClaudeStatusLine parses the payload, records any rate-limit readings, and
// returns the line to print.
//
// The returned error is always nil in practice — it exists so the cobra
// wrapper has a conventional signature — because every failure mode here is
// recoverable into "record nothing, print something".
func ClaudeStatusLine(input []byte, opts ClaudeStatusLineOptions, now time.Time) (string, error) {
	var parsed statusLineInput
	// A parse failure means every field is unavailable, including the ones the
	// fallback line would use. It is still not a reason to print nothing: an
	// empty status line looks to the operator like Claude Code broke.
	if err := json.Unmarshal(input, &parsed); err != nil {
		fmt.Fprintf(os.Stderr, "warn: claude-statusline: unparseable payload: %v\n", err)
	}

	readings := readingsFrom(parsed, now)
	if len(readings) > 0 {
		if err := recordReadings(readings, opts.AccountRoot, now); err != nil {
			// The store is a cache of a figure that will be re-observed on the
			// next render. Losing one write costs the footer nothing lasting.
			fmt.Fprintf(os.Stderr, "warn: claude-statusline: %v\n", err)
		}
	}

	if opts.Delegate != "" {
		if out, err := runDelegate(opts.Delegate, input); err == nil {
			return out, nil
		} else {
			// Falling through to nightgauge's own line rather than printing the
			// delegate's error keeps a broken delegate from blanking the status
			// line entirely.
			fmt.Fprintf(os.Stderr, "warn: claude-statusline: delegate failed: %v\n", err)
		}
	}

	return renderStatusLine(parsed, readings, now), nil
}

// readingsFrom extracts the buckets the payload actually carried. A bucket
// without a used_percentage produces no reading: there is nothing to record,
// and a zero placeholder would be indistinguishable from a genuine "you have
// used none of this window".
func readingsFrom(in statusLineInput, now time.Time) []usagestore.Reading {
	if in.RateLimits == nil {
		return nil
	}
	var readings []usagestore.Reading
	for _, candidate := range []struct {
		name   string
		bucket *statusLineBucket
	}{
		{bucketFiveHour, in.RateLimits.FiveHour},
		{bucketSevenDay, in.RateLimits.SevenDay},
	} {
		if candidate.bucket == nil || candidate.bucket.UsedPercentage == nil {
			continue
		}
		pct := *candidate.bucket.UsedPercentage
		if math.IsNaN(pct) || math.IsInf(pct, 0) {
			continue
		}
		readings = append(readings, usagestore.Reading{
			RateLimitType: candidate.name,
			Utilization:   pct,
			ResetsAt:      candidate.bucket.ResetsAt,
			Status:        statusLineStatus,
			ObservedAt:    now,
		})
	}
	return readings
}

func recordReadings(readings []usagestore.Reading, accountRoot string, now time.Time) error {
	var store *usagestore.Store
	if accountRoot != "" {
		store = usagestore.New(accountRoot)
	} else {
		resolved, err := usagestore.ForAccount()
		if err != nil {
			return err
		}
		store = resolved
	}
	return store.Record(readings, now)
}

// runDelegate executes the operator's existing statusLine command with the
// same stdin payload Claude Code supplied, and returns its stdout.
//
// Run through the platform shell because that is how Claude Code itself
// invokes a statusLine command: the configured value is a command line, not an
// argv, and operators write pipelines and $(...) into it.
func runDelegate(command string, input []byte) (string, error) {
	shell, flag := "/bin/sh", "-c"
	if runtime.GOOS == "windows" {
		shell, flag = "cmd.exe", "/C"
	}
	cmd := exec.Command(shell, flag, command)
	cmd.Stdin = bytes.NewReader(input)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return strings.TrimRight(stdout.String(), "\r\n"), nil
}

// renderStatusLine builds nightgauge's own line: the model, the directory, and
// whichever rate-limit windows the payload carried.
//
// Deliberately terse. This runs in a status bar the operator reads at a
// glance, and it is competing with whatever else they wanted there.
func renderStatusLine(in statusLineInput, readings []usagestore.Reading, now time.Time) string {
	parts := make([]string, 0, 3)
	if name := strings.TrimSpace(in.Model.DisplayName); name != "" {
		parts = append(parts, name)
	}
	dir := in.Workspace.CurrentDir
	if dir == "" {
		dir = in.Workspace.ProjectDir
	}
	if dir != "" {
		parts = append(parts, filepath.Base(dir))
	}
	if usage := renderUsageSegment(readings, now); usage != "" {
		parts = append(parts, usage)
	}
	return strings.Join(parts, " · ")
}

// renderUsageSegment formats the windows as "5h 44% · 7d 61% (resets 2d 3h)".
//
// The reset shown is the *weekly* one when both are present: the five-hour
// window refills often enough that its countdown is noise, while the weekly
// one is the number that decides whether an operator can finish what they
// started.
func renderUsageSegment(readings []usagestore.Reading, now time.Time) string {
	var segments []string
	var weekly *usagestore.Reading
	for i := range readings {
		reading := readings[i]
		switch reading.RateLimitType {
		case bucketFiveHour:
			segments = append(segments, fmt.Sprintf("5h %.0f%%", reading.Utilization))
		case bucketSevenDay:
			segments = append(segments, fmt.Sprintf("7d %.0f%%", reading.Utilization))
			weekly = &readings[i]
		}
	}
	if len(segments) == 0 {
		return ""
	}
	line := strings.Join(segments, " · ")
	if weekly != nil && weekly.ResetsAt > 0 {
		if remaining := time.Unix(weekly.ResetsAt, 0).Sub(now); remaining > 0 {
			line += fmt.Sprintf(" (resets %s)", formatRemaining(remaining))
		}
	}
	return line
}

// formatRemaining renders a duration as "2d 3h" / "3h 14m" / "12m", matching
// the coarse-to-fine style the extension's own reset countdown uses.
func formatRemaining(d time.Duration) string {
	if d >= 24*time.Hour {
		days := int(d / (24 * time.Hour))
		hours := int((d % (24 * time.Hour)) / time.Hour)
		if hours == 0 {
			return fmt.Sprintf("%dd", days)
		}
		return fmt.Sprintf("%dd %dh", days, hours)
	}
	if d >= time.Hour {
		hours := int(d / time.Hour)
		minutes := int((d % time.Hour) / time.Minute)
		if minutes == 0 {
			return fmt.Sprintf("%dh", hours)
		}
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}
	minutes := int(d / time.Minute)
	if minutes < 1 {
		return "<1m"
	}
	return fmt.Sprintf("%dm", minutes)
}

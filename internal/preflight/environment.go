package preflight

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
)

// EnvironmentOutputCap is the most stdout (and stderr) bytes kept per check.
// Anything beyond it is discarded so a chatty command cannot flood the JSON.
const EnvironmentOutputCap = 4096

// environmentWaitDelay bounds how long Wait blocks on pipes held open by
// descendants after the check itself has been killed.
const environmentWaitDelay = 2 * time.Second

// Environment check failure reasons. The set is closed and stable.
const (
	EnvReasonExitCode         = "exit_code"
	EnvReasonStdoutMismatch   = "stdout_mismatch"
	EnvReasonNumberOutOfRange = "stdout_number_out_of_range"
	EnvReasonNumberNotFound   = "stdout_number_not_found"
	EnvReasonTimeout          = "timeout"
	EnvReasonStartFailed      = "start_failed"
)

// EnvironmentResult is the output of `nightgauge preflight environment`.
type EnvironmentResult struct {
	OK     bool                     `json:"ok"`
	Checks []EnvironmentCheckResult `json:"checks"`
}

// EnvironmentCheckResult is the outcome of one declared check.
type EnvironmentCheckResult struct {
	Name            string   `json:"name"`
	Status          string   `json:"status"` // "ok" | "failed"
	Reason          string   `json:"reason,omitempty"`
	Message         string   `json:"message,omitempty"`
	Remedy          string   `json:"remedy,omitempty"`
	Stdout          string   `json:"stdout,omitempty"`
	StdoutTruncated bool     `json:"stdout_truncated,omitempty"`
	ExitCode        int      `json:"exit_code"`
	Observed        *float64 `json:"observed,omitempty"`
}

// RunEnvironmentChecks runs every declared check, in order, and reports all
// of them so the operator sees every failure at once. Commands run as argv
// with no shell; remedies are reported, never executed.
func RunEnvironmentChecks(ctx context.Context, checks []config.EnvironmentCheck, workdir string) EnvironmentResult {
	res := EnvironmentResult{OK: true, Checks: []EnvironmentCheckResult{}}
	for _, c := range checks {
		r := runEnvironmentCheck(ctx, c, workdir)
		if r.Status != "ok" {
			res.OK = false
		}
		res.Checks = append(res.Checks, r)
	}
	return res
}

func runEnvironmentCheck(parent context.Context, c config.EnvironmentCheck, workdir string) EnvironmentCheckResult {
	r := EnvironmentCheckResult{Name: c.Name, Status: "failed", Remedy: c.Remedy, ExitCode: -1}
	timeout := c.EffectiveTimeout()
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, c.Run[0], c.Run[1:]...)
	cmd.Dir = workdir
	cmd.Stdin = nil
	stdout := &cappedBuffer{limit: EnvironmentOutputCap}
	stderr := &cappedBuffer{limit: EnvironmentOutputCap}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = environmentWaitDelay
	isolateProcessGroup(cmd)

	err := cmd.Run()
	r.Stdout = string(stdout.buf)
	r.StdoutTruncated = stdout.truncated

	if ctx.Err() == context.DeadlineExceeded {
		r.Reason = EnvReasonTimeout
		r.Message = fmt.Sprintf("timed out after %s", timeout)
		return r
	}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		r.ExitCode = 0
	case errors.As(err, &exitErr):
		r.ExitCode = exitErr.ExitCode()
	default:
		r.Reason = EnvReasonStartFailed
		r.Message = fmt.Sprintf("could not run %q: %v", c.Run[0], err)
		if msg := strings.TrimSpace(string(stderr.buf)); msg != "" {
			r.Message += ": " + msg
		}
		return r
	}

	if want := c.Expect.WantExitCode(); r.ExitCode != want {
		r.Reason = EnvReasonExitCode
		r.Message = fmt.Sprintf("exit code %d, want %d", r.ExitCode, want)
		if msg := strings.TrimSpace(string(stderr.buf)); msg != "" {
			r.Message += ": " + firstLine(msg)
		}
		return r
	}
	if pat := c.Expect.StdoutMatches; pat != "" {
		re, err := regexp.Compile(pat)
		if err != nil || !re.MatchString(r.Stdout) {
			r.Reason = EnvReasonStdoutMismatch
			r.Message = fmt.Sprintf("stdout does not match %q", pat)
			return r
		}
	}
	if n := c.Expect.StdoutNumber; n != nil {
		reason, msg, observed := checkStdoutNumber(r.Stdout, n)
		r.Observed = observed
		if reason != "" {
			r.Reason, r.Message = reason, msg
			return r
		}
	}
	r.Status = "ok"
	return r
}

func checkStdoutNumber(stdout string, n *config.StdoutNumberExpect) (reason, msg string, observed *float64) {
	re, err := regexp.Compile(n.Capture)
	if err != nil {
		return EnvReasonNumberNotFound, fmt.Sprintf("invalid capture %q: %v", n.Capture, err), nil
	}
	m := re.FindStringSubmatch(stdout)
	if len(m) < 2 {
		return EnvReasonNumberNotFound, fmt.Sprintf("no number captured by %q in stdout", n.Capture), nil
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(m[1]), 64)
	if err != nil {
		return EnvReasonNumberNotFound, fmt.Sprintf("captured %q is not a number", m[1]), nil
	}
	if n.Min != nil && v < *n.Min {
		return EnvReasonNumberOutOfRange, fmt.Sprintf("observed %s, want >= %s", fmtNum(v), fmtNum(*n.Min)), &v
	}
	if n.Max != nil && v > *n.Max {
		return EnvReasonNumberOutOfRange, fmt.Sprintf("observed %s, want <= %s", fmtNum(v), fmtNum(*n.Max)), &v
	}
	return "", "", &v
}

func fmtNum(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// cappedBuffer keeps the first limit bytes written and discards the rest,
// while still reporting full writes so the child never blocks or errors.
type cappedBuffer struct {
	buf       []byte
	limit     int
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	room := b.limit - len(b.buf)
	if room <= 0 {
		if len(p) > 0 {
			b.truncated = true
		}
		return len(p), nil
	}
	if len(p) > room {
		b.buf = append(b.buf, p[:room]...)
		b.truncated = true
		return len(p), nil
	}
	b.buf = append(b.buf, p...)
	return len(p), nil
}

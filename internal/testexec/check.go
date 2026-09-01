package testexec

import (
	"fmt"
	"sort"
	"strings"

	"github.com/nightgauge/nightgauge/internal/deliverable"
)

// Options configures one check.
type Options struct {
	// Workspace is the repo root — both the git checkout being validated and
	// the root of `.nightgauge/`.
	Workspace string
	// IssueNumber scopes the execution record.
	IssueNumber int
	// ChangedFiles is the change's own file set, from git. Never a stage's
	// self-report of what it wrote: the whole point is to stand outside the
	// loop where a plan declares a deliverable and a validator reads the same
	// plan back.
	ChangedFiles []string
	// ConfiguredCommand overrides command resolution
	// (pipeline.test_execution.command). Empty means resolve from the repo.
	ConfiguredCommand string
	// Registry defaults to DefaultRegistry when nil.
	Registry Registry
}

// Result is the outcome of one check.
type Result struct {
	// Command is the resolved test command the check reasoned about.
	Command ResolvedCommand
	// Excluded is every changed test file the command does not execute.
	Excluded []Exclusion
	// Satisfied are the excluded files that DO carry a passing execution
	// record. They are kept, not discarded, so the passing case is visible
	// evidence rather than an absence.
	Satisfied []Exclusion
	// Unsatisfied are the excluded files with no passing execution record.
	// Non-empty means the gate blocks.
	Unsatisfied []Exclusion
	// Warnings carries non-blocking observations from resolution and detection.
	Warnings []string
}

// Blocked reports whether this result must fail the stage.
func (r Result) Blocked() bool { return len(r.Unsatisfied) > 0 }

// Quiet reports whether the check found nothing to say at all — the common
// case, and the one that must produce no new output whatsoever.
func (r Result) Quiet() bool { return len(r.Excluded) == 0 }

// Reason is the one-line gate reason.
func (r Result) Reason() string {
	noun := "files"
	if len(r.Unsatisfied) == 1 {
		noun = "file"
	}
	return fmt.Sprintf("%d test %s the configured test command does not execute, and no execution record proves they were ever run",
		len(r.Unsatisfied), noun)
}

// Evidence renders the blocking detail: which files, by what mechanism, and the
// exact command that would run each one.
//
// The remediation is per-file and runnable on purpose. "Add tests" and "run the
// suite" are the two messages this gate must never emit; the operator already
// knows the suite exists — what they do not know is that their invocation
// cannot reach it.
func (r Result) Evidence() []string {
	out := []string{
		fmt.Sprintf("test command: %s (source: %s)", r.Command.Command, r.Command.Source),
	}
	for _, e := range r.Unsatisfied {
		out = append(out, e.String())
	}
	out = append(out,
		"record an execution with: nightgauge gate record-test-execution --issue <N> --file <path> --outcome pass --command '<the command you ran>'")
	out = append(out, r.Warnings...)
	return out
}

// Summary is the machine-readable block written into the validate artifact.
func (r Result) Summary() map[string]any {
	toList := func(in []Exclusion) []any {
		out := make([]any, 0, len(in))
		for _, e := range in {
			out = append(out, map[string]any{
				"file":        e.Path,
				"detector":    e.Detector,
				"mechanism":   string(e.Mechanism),
				"evidence":    e.Evidence,
				"remediation": e.Remediation,
			})
		}
		return out
	}
	return map[string]any{
		"test_command":        r.Command.Command,
		"test_command_source": r.Command.Source,
		"excluded":            toList(r.Excluded),
		"satisfied":           toList(r.Satisfied),
		"unsatisfied":         toList(r.Unsatisfied),
		"warnings":            r.Warnings,
	}
}

// Check answers, for one run: does this change add test files the configured
// test command cannot execute, and if so, did anything actually execute them?
//
// It is quiet by construction. No changed test files, no resolvable command, no
// detector for the ecosystem, or a command that excludes nothing — every one of
// those returns an empty Result with no warnings and no output. The common case
// must be byte-identical to a run from before this check existed, because a
// gate that speaks up on ordinary work is a gate people learn to ignore.
func Check(opts Options) (Result, error) {
	var res Result
	if len(opts.ChangedFiles) == 0 {
		return res, nil
	}
	cmd := ResolveTestCommand(opts.Workspace, opts.ConfiguredCommand)
	res.Command = cmd
	if !cmd.Resolved() {
		return res, nil
	}

	reg := opts.Registry
	if reg == nil {
		reg = DefaultRegistry()
	}
	excluded, warnings := reg.Detect(opts.Workspace, cmd, opts.ChangedFiles)
	if len(excluded) == 0 {
		// Deliberately drops warnings too. A detector that noticed a malformed
		// annotation in a repo with nothing excluded has nothing to report —
		// surfacing it would add output to the common case.
		return res, nil
	}
	res.Excluded = excluded
	res.Warnings = append(res.Warnings, cmd.Warnings...)
	res.Warnings = append(res.Warnings, warnings...)

	records, err := ReadRecords(opts.Workspace, opts.IssueNumber)
	if err != nil {
		return res, fmt.Errorf("read execution records: %w", err)
	}
	passed := PassedFiles(records)
	for _, e := range excluded {
		if _, ok := passed[e.Path]; ok {
			res.Satisfied = append(res.Satisfied, e)
			continue
		}
		res.Unsatisfied = append(res.Unsatisfied, e)
	}
	sort.Strings(res.Warnings)
	res.Warnings = dedupe(res.Warnings)
	return res, nil
}

// ApplyToValidateContext records the check's outcome in the validate artifact
// so a consumer that sees the artifact later — pr-create, the attention sweep,
// a retro — reads the same facts the gate did rather than re-deriving them from
// a diff it may be running too late to obtain.
//
// This is also the single place a second sink belongs when epic #12's artifact
// manifest lands: one writer, two destinations, not two writers.
func ApplyToValidateContext(workspace string, issueNumber int, res Result) error {
	if res.Quiet() {
		return nil
	}
	doc, err := deliverable.ReadValidateContext(workspace, issueNumber)
	if err != nil {
		return err
	}
	doc["test_execution"] = res.Summary()
	return deliverable.WriteValidateContext(workspace, issueNumber, doc)
}

func dedupe(in []string) []string {
	var out []string
	for _, s := range in {
		if s = strings.TrimSpace(s); s == "" {
			continue
		}
		if len(out) > 0 && out[len(out)-1] == s {
			continue
		}
		out = append(out, s)
	}
	return out
}

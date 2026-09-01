// Package testexec answers a narrower question than package deliverable does,
// and answers it before the merge rather than after: of the test files this
// change introduced, which ones does the repo's own test command structurally
// never execute?
//
// The distinction matters because the two failures look identical from inside
// the pipeline and have completely different causes. `deliverable` catches a
// tier the stage *chose* not to run and reports it in the verdict (#152). This
// package catches a file the configured command *cannot* run — a Dart suite
// carrying `@Tags(['app-e2e'])` against an invocation that passes
// `--exclude-tags=app-e2e`, or a suite outside every path the command names.
// No amount of running the configured command will ever exercise it, so a green
// run says nothing at all about the code the change added.
//
// That is not a verdict nuance; it is a gate. In a downstream Flutter app,
// three sibling issues each merged a suite under `integration_test/app_e2e/`
// tagged `app-e2e`, each was marked validated by a stage whose test command
// excluded that tag, and each then failed on every nightly sweep for five
// weeks. Two of the defects later found were structurally impossible
// assertions — a running total asserted before the inputs that determine it
// existed, and a widget finder that could never match — either of which would
// have failed on the very first honest execution.
//
// So the rule this package enforces is: if the change adds a test file the
// configured command excludes, the run must produce an execution record naming
// that file with a passing outcome. Not "add tests" — the file exists. Not "run
// everything" — the exclusion is usually deliberate and correct. Just: someone
// has to have watched this code run, once, before it is called validated.
//
// Everything here is fail-open by construction. An unresolvable test command,
// an ecosystem with no detector, a repo whose command excludes nothing: all
// produce zero findings and no output. A gate that fires on ordinary work gets
// routed around, and the routing-around is how the pipeline acquired the
// self-granted exemption in the first place.
package testexec

import (
	"fmt"
	"sort"
	"strings"
)

// Mechanism names how a file came to be outside the configured command's reach.
// It is part of the failure message because "your test never runs" is not
// actionable, and "your test never runs because the command passes
// --exclude-tags=app-e2e and this file declares @Tags(['app-e2e'])" is.
type Mechanism string

const (
	// MechanismExcludedTag — the file declares a tag the command excludes.
	MechanismExcludedTag Mechanism = "excluded-tag"
	// MechanismTagFilter — the command restricts to a tag set (`--tags=`) that
	// the file's tags (or its lack of any) do not satisfy.
	MechanismTagFilter Mechanism = "tag-filter"
	// MechanismOutsidePaths — the command names explicit path targets and the
	// file is under none of them.
	MechanismOutsidePaths Mechanism = "outside-target-paths"
)

// Exclusion is one file the configured test command does not execute, with the
// evidence for that claim and the command that would execute it.
type Exclusion struct {
	// Path is the repo-relative path as git reported it.
	Path string
	// Detector is the ecosystem detector that produced this finding.
	Detector string
	// Mechanism is how the exclusion happens.
	Mechanism Mechanism
	// Evidence is the concrete fragment of the resolved command (and, where
	// relevant, of the file) that proves it — e.g. `--exclude-tags=app-e2e`.
	Evidence string
	// Remediation is a runnable command that WOULD execute this file. A
	// remediation the operator has to invent is not a remediation.
	Remediation string
}

// String renders one exclusion for a gate evidence line.
func (e Exclusion) String() string {
	return fmt.Sprintf("%s (%s: %s) — run: %s", e.Path, e.Mechanism, e.Evidence, e.Remediation)
}

// Detector recognises, for one ecosystem, which of a change's test files the
// resolved test command does not execute.
//
// The interface deliberately takes the resolved command rather than a repo
// path plus a guess: a repo that changes its exclusions must be re-evaluated
// on the next run, never grandfathered by a value cached at first sight.
type Detector interface {
	// Name is the ecosystem identifier used in findings and tests.
	Name() string
	// Detect returns the subset of files this detector claims are excluded.
	// A detector that does not recognise the ecosystem, or cannot decide,
	// returns nil — silence is the only safe default here.
	//
	// warnings carries anything the detector noticed but refused to act on
	// (an unparseable annotation, a command shape it does not model). They are
	// surfaced, never escalated: a parse failure must not be able to fail a
	// stage, or the first malformed file in a repo turns the gate into an
	// obstacle and the gate gets disabled.
	Detect(repoRoot string, cmd ResolvedCommand, files []string) (exclusions []Exclusion, warnings []string)
}

// Registry is the ordered detector set. Order is stable so a file claimed by
// two detectors reports the same one every run.
type Registry []Detector

// DefaultRegistry is the shipped detector set. Dart/Flutter is the only member
// today; the interface exists so the next ecosystem is an addition here rather
// than a rewrite of the gate.
func DefaultRegistry() Registry { return Registry{DartDetector{}} }

// Detect runs every detector and merges their findings, first claim winning
// per path.
func (r Registry) Detect(repoRoot string, cmd ResolvedCommand, files []string) ([]Exclusion, []string) {
	var (
		out      []Exclusion
		warnings []string
		seen     = map[string]bool{}
	)
	for _, d := range r {
		found, warns := d.Detect(repoRoot, cmd, files)
		warnings = append(warnings, warns...)
		for _, e := range found {
			if seen[e.Path] {
				continue
			}
			seen[e.Path] = true
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, warnings
}

// splitTagExpression splits a Dart/Flutter tag selector into its literal tag
// names. The runners accept a small boolean expression (`app-e2e || slow`,
// `a,b`); anything beyond a disjunction of bare names is deliberately NOT
// modelled — an expression this function cannot read yields no names, which
// makes the file un-excluded rather than falsely excluded.
func splitTagExpression(expr string) []string {
	expr = strings.TrimSpace(strings.Trim(strings.TrimSpace(expr), `"'`))
	if expr == "" {
		return nil
	}
	repl := strings.NewReplacer(",", " ", "||", " ", "|", " ")
	var out []string
	for _, f := range strings.Fields(repl.Replace(expr)) {
		f = strings.TrimSpace(f)
		// A negation or conjunction means the selector is doing something this
		// function does not model. Bail entirely rather than half-read it.
		if f == "" || strings.ContainsAny(f, "!&()") {
			return nil
		}
		out = append(out, f)
	}
	return out
}

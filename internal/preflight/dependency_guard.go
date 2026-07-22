// Package preflight — dependency_guard.go is the slopsquatting / hallucinated-
// dependency pre-merge gate (#4095).
//
// `npm audit` / `pip-audit` / `govulncheck` only find CVEs in packages that
// REALLY EXIST and are published. They say nothing about a package name an
// agent INVENTED (a hallucinated dependency that 404s on the registry) or a
// fresh typosquat of a popular package (slopsquatting) — exactly the
// supply-chain risk the "New SDLC" whitepaper names. This gate closes that gap
// for newly-added dependencies.
//
// It is deliberately NOT a deterministic StageGate (those forbid network — see
// docs/STAGE_GATES.md and internal/.../gate.go): registry existence is an
// inherently network check, so it runs as a CLI/skill preflight (the same class
// as version-downgrade / scope-drift). The non-deterministic part (the registry
// lookup) is isolated behind RegistryChecker; everything else — extracting the
// newly-added deps from the diff and the typosquat heuristic — is pure and
// fully unit-tested.
//
// Exit policy lives in the CLI: a missing (hallucinated) package or a typosquat
// is BLOCKING; a network-inconclusive lookup is a non-blocking warning, so a
// flaky registry never blocks a merge.
//
// @see Issue #4095 - Slopsquatting / hallucinated-dependency pre-merge guard
package preflight

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Ecosystem identifies a package registry.
type Ecosystem string

const (
	EcoNPM Ecosystem = "npm"
	EcoGo  Ecosystem = "go"
	EcoPip Ecosystem = "pip"
)

// manifestFiles maps each ecosystem to the manifest this gate diffs.
var manifestFiles = map[Ecosystem]string{
	EcoNPM: "package.json",
	EcoGo:  "go.mod",
	EcoPip: "requirements.txt",
}

// RegistryStatus is the outcome of a registry-existence lookup.
type RegistryStatus int

const (
	RegistryExists RegistryStatus = iota
	RegistryMissing
	RegistryInconclusive // network error / rate-limited — never blocks a merge
)

// RegistryChecker resolves whether a package name exists on its registry. The
// real implementation hits the network; tests inject a fake. The only
// non-deterministic dependency of this gate.
type RegistryChecker interface {
	Exists(ctx context.Context, eco Ecosystem, name string) RegistryStatus
}

// AddedDep is a dependency introduced by the working tree relative to baseline.
type AddedDep struct {
	Ecosystem Ecosystem `json:"ecosystem"`
	Name      string    `json:"name"`
	File      string    `json:"file"`
}

// DepFinding is a blocking-or-not problem with an added dependency.
type DepFinding struct {
	Ecosystem Ecosystem `json:"ecosystem"`
	Name      string    `json:"name"`
	File      string    `json:"file"`
	Kind      string    `json:"kind"`   // "missing" | "typosquat"
	Detail    string    `json:"detail"` // human explanation
	Blocking  bool      `json:"blocking"`
}

// DependencyGuardResult is the gate verdict.
type DependencyGuardResult struct {
	V            int          `json:"v"` // schema version, always 1
	Root         string       `json:"root"`
	Baseline     string       `json:"baseline"`
	AddedCount   int          `json:"added_count"`
	Findings     []DepFinding `json:"findings"`               // blocking problems
	Inconclusive []DepFinding `json:"inconclusive,omitempty"` // network-inconclusive (warn only)
}

// HasBlocking reports whether any blocking finding was recorded.
func (r *DependencyGuardResult) HasBlocking() bool {
	for _, f := range r.Findings {
		if f.Blocking {
			return true
		}
	}
	return false
}

// DependencyGuardOptions configures a run.
type DependencyGuardOptions struct {
	Root     string // repo root; defaults to CWD
	Baseline string // baseline git ref; defaults to "main"
	Registry RegistryChecker
}

// RunDependencyGuardCheck diffs each manifest against the baseline ref, extracts
// the newly-added dependencies, and flags missing/typosquatted ones.
func RunDependencyGuardCheck(ctx context.Context, opts DependencyGuardOptions) (*DependencyGuardResult, error) {
	root := opts.Root
	if root == "" {
		if wd, err := os.Getwd(); err == nil {
			root = wd
		}
	}
	baseline := opts.Baseline
	if baseline == "" {
		baseline = "main"
	}
	registry := opts.Registry
	if registry == nil {
		registry = NewHTTPRegistryChecker()
	}

	result := &DependencyGuardResult{V: 1, Root: root, Baseline: baseline, Findings: []DepFinding{}}

	var added []AddedDep
	for eco, file := range manifestFiles {
		current, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			continue // manifest absent for this ecosystem
		}
		base, _ := gitShow(root, baseline, file) // missing baseline → whole file is "added"
		added = append(added, addedDeps(eco, file, base, current)...)
	}
	result.AddedCount = len(added)

	findings, inconclusive := evaluateDeps(ctx, added, registry)
	result.Findings = append(result.Findings, findings...)
	result.Inconclusive = inconclusive

	return result, nil
}

// evaluateDeps applies the typosquat heuristic and the registry-existence check
// to each added dependency, returning the blocking findings and the
// network-inconclusive (warn-only) entries. Pure given a RegistryChecker — the
// unit-testable core of the gate.
func evaluateDeps(ctx context.Context, added []AddedDep, registry RegistryChecker) (findings, inconclusive []DepFinding) {
	for _, dep := range added {
		if pop, ok := typosquatMatch(dep.Ecosystem, dep.Name); ok {
			findings = append(findings, DepFinding{
				Ecosystem: dep.Ecosystem, Name: dep.Name, File: dep.File,
				Kind: "typosquat", Blocking: true,
				Detail: "name is within one edit of the popular package \"" + pop + "\" — possible slopsquat",
			})
		}
		switch registry.Exists(ctx, dep.Ecosystem, dep.Name) {
		case RegistryMissing:
			findings = append(findings, DepFinding{
				Ecosystem: dep.Ecosystem, Name: dep.Name, File: dep.File,
				Kind: "missing", Blocking: true,
				Detail: "package does not exist on the " + string(dep.Ecosystem) + " registry (hallucinated dependency?)",
			})
		case RegistryInconclusive:
			inconclusive = append(inconclusive, DepFinding{
				Ecosystem: dep.Ecosystem, Name: dep.Name, File: dep.File,
				Kind: "missing", Blocking: false,
				Detail: "registry existence could not be verified (network/rate-limit) — not blocking",
			})
		}
	}
	return findings, inconclusive
}

// addedDeps returns the dependencies present in current but not in baseline.
func addedDeps(eco Ecosystem, file string, baseline, current []byte) []AddedDep {
	parse := depParserFor(eco)
	if parse == nil {
		return nil
	}
	baseNames := parse(baseline)
	curNames := parse(current)

	var added []AddedDep
	for name := range curNames {
		if _, existed := baseNames[name]; !existed {
			added = append(added, AddedDep{Ecosystem: eco, Name: name, File: file})
		}
	}
	return added
}

func depParserFor(eco Ecosystem) func([]byte) map[string]struct{} {
	switch eco {
	case EcoNPM:
		return parseNpmDeps
	case EcoGo:
		return parseGoModDeps
	case EcoPip:
		return parsePipDeps
	}
	return nil
}

// parseNpmDeps collects names from every dependency block in package.json.
func parseNpmDeps(b []byte) map[string]struct{} {
	out := map[string]struct{}{}
	if len(b) == 0 {
		return out
	}
	var pkg map[string]json.RawMessage
	if err := json.Unmarshal(b, &pkg); err != nil {
		return out
	}
	for _, block := range []string{"dependencies", "devDependencies", "optionalDependencies", "peerDependencies"} {
		raw, ok := pkg[block]
		if !ok {
			continue
		}
		var deps map[string]string
		if err := json.Unmarshal(raw, &deps); err != nil {
			continue
		}
		for name := range deps {
			out[name] = struct{}{}
		}
	}
	return out
}

// parseGoModDeps collects module paths from require directives in go.mod.
func parseGoModDeps(b []byte) map[string]struct{} {
	out := map[string]struct{}{}
	inBlock := false
	for _, line := range strings.Split(string(b), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}
		if strings.HasPrefix(trimmed, "require (") {
			inBlock = true
			continue
		}
		if inBlock {
			if trimmed == ")" {
				inBlock = false
				continue
			}
			if mod := firstField(trimmed); mod != "" {
				out[mod] = struct{}{}
			}
			continue
		}
		if strings.HasPrefix(trimmed, "require ") {
			if mod := firstField(strings.TrimPrefix(trimmed, "require ")); mod != "" {
				out[mod] = struct{}{}
			}
		}
	}
	return out
}

// parsePipDeps collects package names from a requirements.txt body.
func parsePipDeps(b []byte) map[string]struct{} {
	out := map[string]struct{}{}
	for _, line := range strings.Split(string(b), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "-") {
			continue // comments and pip flags (-r, -e, --hash) are not packages
		}
		// Name ends at the first version specifier, extra, marker, or space.
		name := strings.TrimSpace(strings.FieldsFunc(trimmed, func(r rune) bool {
			return strings.ContainsRune("<>=!~;[ \t", r)
		})[0])
		if name != "" {
			out[strings.ToLower(name)] = struct{}{}
		}
	}
	return out
}

func firstField(s string) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// gitShow returns the bytes of `git show {ref}:{path}` from inside root, or
// (nil, false) when the file is absent on that ref (a brand-new manifest).
func gitShow(root, ref, path string) ([]byte, bool) {
	out, err := exec.Command("git", "-C", root, "show", ref+":"+path).Output()
	if err != nil {
		return nil, false
	}
	return out, true
}

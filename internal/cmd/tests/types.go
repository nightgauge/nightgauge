// Package tests implements deterministic test inventory and risk scoring
// behind the `nightgauge test inventory` and `nightgauge test
// risk-score` verbs. It absorbs the inline Glob + grep + git log shell from
// skills/nightgauge-test-scaffold/SKILL.md Phases 1 (Steps 1.1–1.4) and
// Phase 3 (Steps 3.1–3.5).
//
// Audit reference: docs/SKILL_DETERMINISM_AUDIT.md row B39.
//
// JSON contract — both verbs emit `v: 1`. Field names are pinned at the first
// merge; bumps are signaled by a new `v` value with additive evolution only.
// The risk-score scoring tables (criticality boosts, branch-count buckets,
// commit-frequency buckets, importer buckets, priority thresholds) reproduce
// the SKILL.md prose verbatim — they are part of the v1 contract.
package tests

// SchemaVersion is the JSON schema version emitted by every result struct in
// this package. Constant lives at the package level to keep the inventory
// and risk verbs in lockstep.
const SchemaVersion = 1

// InventoryOptions controls a single inventory run.
type InventoryOptions struct {
	// Workdir is the directory to walk. Empty falls back to the caller's CWD.
	Workdir string
}

// InventoryCounts is the lightweight roll-up of an inventory walk. Mirrors
// the count surface of `scan tests` so consumers that only need the totals
// don't have to read the file lists.
type InventoryCounts struct {
	SourceFiles   int `json:"source_files"`
	TestFiles     int `json:"test_files"`
	UntestedFiles int `json:"untested_files"`
}

// InventoryResult is the JSON document emitted by `test inventory --json`.
// Paths in SourceFiles, TestFiles, and UntestedFiles are workdir-relative
// (POSIX-style separators) so consumers can pipe the list directly into
// `risk-score --files`.
type InventoryResult struct {
	V                   int               `json:"v"`
	Workdir             string            `json:"workdir"`
	Counts              InventoryCounts   `json:"counts"`
	SourceFiles         []string          `json:"source_files"`
	TestFiles           []string          `json:"test_files"`
	TestToSourceMapping map[string]string `json:"test_to_source_mapping"`
	UntestedFiles       []string          `json:"untested_files"`
	Warnings            []string          `json:"warnings"`
}

// RiskOptions controls a single risk-score run.
type RiskOptions struct {
	// Workdir is the directory used for git-log change-frequency lookups and
	// importer counts. Empty falls back to the caller's CWD.
	Workdir string
	// Files is the list of paths (workdir-relative or absolute) to score.
	Files []string
}

// RiskScoreEntry is the per-file scoring breakdown plus composite + bucket.
// All sub-score fields are exposed so consumers can rebuild the priority
// without re-running the verb (debug + audit).
type RiskScoreEntry struct {
	File                string `json:"file"`
	BusinessCriticality int    `json:"business_criticality"`
	Complexity          int    `json:"complexity"`
	ChangeFrequency     int    `json:"change_frequency"`
	DependencyDepth     int    `json:"dependency_depth"`
	Score               int    `json:"score"`
	Priority            string `json:"priority"`
}

// RiskScoreResult is the JSON document emitted by `test risk-score --json`.
// Entries are sorted by score descending, then by file path ascending, for
// stable ordering across runs.
type RiskScoreResult struct {
	V        int              `json:"v"`
	Workdir  string           `json:"workdir"`
	Entries  []RiskScoreEntry `json:"entries"`
	Warnings []string         `json:"warnings"`
}

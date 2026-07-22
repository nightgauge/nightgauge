// Package release implements deterministic GitHub-release fetch and changelog
// classification behind the `nightgauge release fetch` and
// `nightgauge release classify-changes` verbs. It absorbs the inline
// `gh api` + Python from skills/nightgauge-release-watch/SKILL.md
// Phases 2–4 (lines 146–326).
//
// Audit reference: docs/SKILL_DETERMINISM_AUDIT.md row B33.
//
// JSON contract — the classified output is a top-level array of
// ClassifiedRelease values. Field names (`version`, `published_at`,
// `changes[].type`, `.description`, `.tags`) are pinned byte-for-byte to the
// pre-migration `/tmp/release-watch-classified.json` shape so the
// release-watch skill's Phase 5+ scoring code consumes the new output without
// changes (see ADR-002 in the issue knowledge directory).
package release

// SchemaVersion is the JSON schema version emitted by Fetch results. The
// classified output is a bare array (preserving consumer compatibility) so it
// has no embedded version field; bumps to the classifier semantics will be
// signaled by a new `cv` field added to the wrapper if it ever exists.
const SchemaVersion = 1

// DefaultBaseURL is the GitHub REST API root used by Fetch when
// Options.BaseURL is empty.
const DefaultBaseURL = "https://api.github.com"

// DefaultLimit mirrors the `--jq '.[0:10]'` slice used by the pre-migration
// release-watch skill.
const DefaultLimit = 10

// Release captures the GitHub releases-API fields downstream consumers
// actually use. Field names mirror the GitHub REST shape so a fetched
// document can also be written verbatim to /tmp/release-watch-new.json
// for skills that still read it.
type Release struct {
	TagName     string `json:"tag_name"`
	Name        string `json:"name"`
	PublishedAt string `json:"published_at"`
	Body        string `json:"body"`
	HTMLURL     string `json:"html_url"`
	Prerelease  bool   `json:"prerelease"`
	Draft       bool   `json:"draft"`
}

// FetchResult is the JSON document emitted by `release fetch --json`.
// Consumers may pipe this directly into `release classify-changes --input`.
type FetchResult struct {
	V         int       `json:"v"`
	Source    string    `json:"source"`
	Since     string    `json:"since,omitempty"`
	Limit     int       `json:"limit"`
	FetchedAt string    `json:"fetched_at"`
	Filtered  int       `json:"filtered"` // releases dropped by --since semver compare
	Releases  []Release `json:"releases"`
}

// ClassifiedChange is a single bullet from a release body, classified into
// one of five buckets: feature, fix, breaking, deprecation, improvement.
//
// Field-name stability is the contract: release-watch SKILL Phase 5 reads
// `type`, `description`, and `tags` directly when scoring relevance.
type ClassifiedChange struct {
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
}

// ClassifiedRelease groups classified changes for a single release. Top-level
// JSON output of `release classify-changes` is a `[]ClassifiedRelease` (a
// bare array — preserving the pre-migration `/tmp/release-watch-classified.json`
// shape so Phase 5+ of the release-watch skill needs no changes).
type ClassifiedRelease struct {
	Version     string             `json:"version"`
	PublishedAt string             `json:"published_at"`
	Changes     []ClassifiedChange `json:"changes"`
}

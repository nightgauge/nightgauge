// Package knowledge provides deterministic file operations for the
// .nightgauge/knowledge/ directory. It is the Go-layer equivalent of the
// TypeScript KnowledgeService in packages/nightgauge-sdk — same templates,
// same slug algorithm, same prune threshold. No LLM calls, no GitHub API.
package knowledge

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// EventType enumerates structured telemetry events emitted by knowledge operations.
type EventType string

const (
	EventScaffold        EventType = "knowledge.scaffold"
	EventPRDEnriched     EventType = "knowledge.prd_enriched"
	EventDecisionAdded   EventType = "knowledge.decision_added"
	EventOutcomeRecorded EventType = "knowledge.outcome_recorded"
	EventPruned          EventType = "knowledge.pruned"
)

// Event is a structured telemetry record emitted by knowledge operations.
// It is written to the provided io.Writer (typically the VSCode output channel
// or stdout) in a single-line format parseable by pipeline history consumers.
type Event struct {
	Type         EventType `json:"type"`
	IssueNumber  int       `json:"issue_number,omitempty"`
	Path         string    `json:"path"`
	BytesWritten int       `json:"bytes_written,omitempty"`
	Stage        string    `json:"stage,omitempty"`
	Timestamp    string    `json:"timestamp"`
}

// emitEvent writes a structured telemetry line to w.
// Format: [knowledge] <type> issue=N path=<p> bytes=N stage=<s> ts=<iso>
// Returns immediately on nil writer — callers may pass nil to suppress telemetry.
func emitEvent(w io.Writer, e Event) {
	if w == nil {
		return
	}
	if e.Timestamp == "" {
		e.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}
	parts := []string{
		"[knowledge]",
		string(e.Type),
	}
	if e.IssueNumber > 0 {
		parts = append(parts, "issue="+strconv.Itoa(e.IssueNumber))
	}
	if e.Path != "" {
		parts = append(parts, "path="+e.Path)
	}
	if e.BytesWritten > 0 {
		parts = append(parts, "bytes="+strconv.Itoa(e.BytesWritten))
	}
	if e.Stage != "" {
		parts = append(parts, "stage="+e.Stage)
	}
	parts = append(parts, "ts="+e.Timestamp)
	fmt.Fprintln(w, strings.Join(parts, " "))
}

// IssueStats holds per-issue knowledge write statistics for the `knowledge stats` command.
type IssueStats struct {
	IssueNumber    int    `json:"issue_number"`
	Path           string `json:"path"`
	PRDBytes       int64  `json:"prd_bytes"`
	DecisionsBytes int64  `json:"decisions_bytes"`
	OutcomesBytes  int64  `json:"outcomes_bytes"`
	LastWrite      string `json:"last_write"`
}

// Stats returns per-issue knowledge write counts for all entries under
// .nightgauge/knowledge/features/. Used by `nightgauge knowledge stats`.
func Stats(workspaceRoot string) ([]IssueStats, error) {
	featuresDir := filepath.Join(workspaceRoot, ".nightgauge", "knowledge", "features")

	entries, err := os.ReadDir(featuresDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []IssueStats{}, nil
		}
		return nil, fmt.Errorf("read features dir: %w", err)
	}

	var results []IssueStats

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}

		parts := strings.SplitN(e.Name(), "-", 2)
		if len(parts) != 2 {
			continue
		}
		issueNum := 0
		fmt.Sscanf(parts[0], "%d", &issueNum)

		dirPath := filepath.Join(featuresDir, e.Name())
		relPath, _ := filepath.Rel(workspaceRoot, dirPath)

		stat := IssueStats{
			IssueNumber: issueNum,
			Path:        relPath,
		}

		var latestMod time.Time

		for _, fname := range []string{"PRD.md", "decisions.md", "outcomes.md"} {
			fpath := filepath.Join(dirPath, fname)
			info, err := os.Stat(fpath)
			if err != nil {
				continue
			}
			size := info.Size()
			if info.ModTime().After(latestMod) {
				latestMod = info.ModTime()
			}
			switch fname {
			case "PRD.md":
				stat.PRDBytes = size
			case "decisions.md":
				stat.DecisionsBytes = size
			case "outcomes.md":
				stat.OutcomesBytes = size
			}
		}

		if !latestMod.IsZero() {
			stat.LastWrite = latestMod.UTC().Format(time.RFC3339)
		}

		results = append(results, stat)
	}

	return results, nil
}

// ScaffoldResult mirrors the TypeScript KnowledgeService.ScaffoldResult.
type ScaffoldResult struct {
	// KnowledgePath is the path relative to workspaceRoot.
	KnowledgePath string `json:"knowledge_path"`
	// PRDPath is the path of PRD.md relative to workspaceRoot (empty when skipped).
	PRDPath string `json:"prd_path"`
	// DecisionsPath is the path of decisions.md relative to workspaceRoot (empty when skipped).
	DecisionsPath string `json:"decisions_path"`
	// Skipped is true when scaffolding was skipped (idempotent rerun OR disabled via config).
	Skipped bool `json:"skipped"`
	// SkipReason provides rationale for skipping (empty when not skipped).
	SkipReason string `json:"skip_reason,omitempty"`
	// FilesCreated lists filenames created (e.g., ["PRD.md", "decisions.md"]).
	FilesCreated []string `json:"files_created"`
}

// Scaffold creates the knowledge directory and template files for an issue.
// Idempotent — safe to call multiple times; returns Skipped=true on repeat calls.
//
// Templates match the TypeScript generatePRD / generateDecisionsTemplate output
// verbatim so both layers produce identical files.
//
// When eventWriter is non-nil, a knowledge.scaffold telemetry event is emitted.
// Pass nil to suppress telemetry (e.g., in tests).
func Scaffold(workspaceRoot string, issueNumber int, title string, acceptanceCriteria []string, eventWriter ...io.Writer) (ScaffoldResult, error) {
	var w io.Writer
	if len(eventWriter) > 0 {
		w = eventWriter[0]
	}
	slug := generateSlug(title)
	dirName := fmt.Sprintf("%d-%s", issueNumber, slug)
	knowledgePath := filepath.Join(workspaceRoot, ".nightgauge", "knowledge", "features", dirName)

	// Idempotent: detect existing directory before creating.
	_, statErr := os.Stat(knowledgePath)
	dirExists := statErr == nil

	if err := os.MkdirAll(knowledgePath, 0o755); err != nil {
		return ScaffoldResult{}, fmt.Errorf("create knowledge directory: %w", err)
	}

	rel, err := filepath.Rel(workspaceRoot, knowledgePath)
	if err != nil {
		rel = knowledgePath
	}

	if dirExists {
		return ScaffoldResult{
			KnowledgePath: rel,
			PRDPath:       filepath.Join(rel, "PRD.md"),
			DecisionsPath: filepath.Join(rel, "decisions.md"),
			Skipped:       true,
		}, nil
	}

	var filesCreated []string

	prdPath := filepath.Join(knowledgePath, "PRD.md")
	prdContent := generatePRD(issueNumber, title, acceptanceCriteria)
	if err := os.WriteFile(prdPath, []byte(prdContent), 0o644); err != nil {
		return ScaffoldResult{}, fmt.Errorf("write PRD.md: %w", err)
	}
	filesCreated = append(filesCreated, "PRD.md")

	decisionsPath := filepath.Join(knowledgePath, "decisions.md")
	decisionsContent := generateDecisionsTemplate(issueNumber, title)
	if err := os.WriteFile(decisionsPath, []byte(decisionsContent), 0o644); err != nil {
		return ScaffoldResult{}, fmt.Errorf("write decisions.md: %w", err)
	}
	filesCreated = append(filesCreated, "decisions.md")

	result := ScaffoldResult{
		KnowledgePath: rel,
		PRDPath:       filepath.Join(rel, "PRD.md"),
		DecisionsPath: filepath.Join(rel, "decisions.md"),
		Skipped:       false,
		FilesCreated:  filesCreated,
	}

	// Compute total bytes written for telemetry.
	totalBytes := 0
	for _, fname := range filesCreated {
		if info, err := os.Stat(filepath.Join(knowledgePath, fname)); err == nil {
			totalBytes += int(info.Size())
		}
	}
	emitEvent(w, Event{
		Type:         EventScaffold,
		IssueNumber:  issueNumber,
		Path:         rel,
		BytesWritten: totalBytes,
	})

	return result, nil
}

// ScaffoldWithConfig creates the knowledge directory and template files for an issue,
// respecting knowledge.enabled and knowledge.workspace_scoped config flags.
// When knowledgeEnabled is false, returns Skipped=true with SkipReason set.
// Otherwise behaves identically to Scaffold.
func ScaffoldWithConfig(workspaceRoot string, issueNumber int, title string, acceptanceCriteria []string, knowledgeEnabled bool, workspaceScoped bool, eventWriter ...io.Writer) (ScaffoldResult, error) {
	if !knowledgeEnabled {
		return ScaffoldResult{
			Skipped:    true,
			SkipReason: "knowledge.enabled=false in config",
		}, nil
	}
	return Scaffold(workspaceRoot, issueNumber, title, acceptanceCriteria, eventWriter...)
}

// PruneEmpty removes knowledge directories whose .md files contain only
// boilerplate content. Matches the TypeScript pruneEmpty() + contentIsSubstantive() logic:
// a file is substantive when ≥30 non-whitespace chars remain after stripping
// HTML comments, headings, table rows, and status checkboxes.
//
// Returns paths of removed directories (relative to workspaceRoot).
// When dryRun is true, directories are listed but not deleted.
func PruneEmpty(workspaceRoot string, dryRun bool) ([]string, error) {
	knowledgeRoot := filepath.Join(workspaceRoot, ".nightgauge", "knowledge")

	categoryEntries, err := os.ReadDir(knowledgeRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read knowledge root: %w", err)
	}

	var pruned []string

	for _, cat := range categoryEntries {
		if !cat.IsDir() {
			continue
		}
		categoryPath := filepath.Join(knowledgeRoot, cat.Name())

		issueEntries, err := os.ReadDir(categoryPath)
		if err != nil {
			continue
		}

		for _, issueDir := range issueEntries {
			if !issueDir.IsDir() {
				continue
			}
			issueDirPath := filepath.Join(categoryPath, issueDir.Name())
			relPath, _ := filepath.Rel(workspaceRoot, issueDirPath)

			mdFiles, err := listMDFiles(issueDirPath)
			if err != nil || len(mdFiles) == 0 {
				continue
			}

			anySubstantive := false
			for _, f := range mdFiles {
				content, err := os.ReadFile(filepath.Join(issueDirPath, f))
				if err != nil {
					continue
				}
				if contentIsSubstantive(string(content)) {
					anySubstantive = true
					break
				}
			}

			if !anySubstantive {
				if !dryRun {
					if err := os.RemoveAll(issueDirPath); err != nil {
						return pruned, fmt.Errorf("remove %s: %w", issueDirPath, err)
					}
				}
				pruned = append(pruned, relPath)
			}
		}
	}

	return pruned, nil
}

// GenerateIndex writes .nightgauge/knowledge/README.md listing all
// knowledge entries grouped by category.
//
// Returns the path of the written README.md (relative to workspaceRoot).
func GenerateIndex(workspaceRoot string) (string, error) {
	knowledgeRoot := filepath.Join(workspaceRoot, ".nightgauge", "knowledge")

	if err := os.MkdirAll(knowledgeRoot, 0o755); err != nil {
		return "", fmt.Errorf("create knowledge root: %w", err)
	}

	categoryEntries, err := os.ReadDir(knowledgeRoot)
	if err != nil {
		return "", fmt.Errorf("read knowledge root: %w", err)
	}

	type entry struct {
		IssueNumber int
		Slug        string
		Files       []string
	}
	categories := map[string][]entry{}
	totalEntries := 0

	for _, cat := range categoryEntries {
		if !cat.IsDir() {
			continue
		}
		categoryPath := filepath.Join(knowledgeRoot, cat.Name())

		issueDirs, err := os.ReadDir(categoryPath)
		if err != nil {
			continue
		}

		for _, issueDir := range issueDirs {
			if !issueDir.IsDir() {
				continue
			}

			// Parse {N}-{slug} directory name.
			parts := strings.SplitN(issueDir.Name(), "-", 2)
			if len(parts) != 2 {
				continue
			}
			issueNum := 0
			fmt.Sscanf(parts[0], "%d", &issueNum)
			slug := parts[1]

			mdFiles, _ := listMDFiles(filepath.Join(categoryPath, issueDir.Name()))

			categories[cat.Name()] = append(categories[cat.Name()], entry{
				IssueNumber: issueNum,
				Slug:        slug,
				Files:       mdFiles,
			})
			totalEntries++
		}
	}

	var sb strings.Builder
	sb.WriteString("# Knowledge Base Index\n\n")
	sb.WriteString("> Auto-generated by `nightgauge knowledge index`\n\n")
	fmt.Fprintf(&sb, "**Total entries:** %d\n\n", totalEntries)

	for catName, entries := range categories {
		fmt.Fprintf(&sb, "## %s\n\n", catName)
		sb.WriteString("| Issue | Slug | Files |\n")
		sb.WriteString("| ----- | ---- | ----- |\n")
		for _, e := range entries {
			fmt.Fprintf(&sb, "| #%d | %s | %s |\n", e.IssueNumber, e.Slug, strings.Join(e.Files, ", "))
		}
		sb.WriteString("\n")
	}

	readmePath := filepath.Join(knowledgeRoot, "README.md")
	if err := os.WriteFile(readmePath, []byte(sb.String()), 0o644); err != nil {
		return "", fmt.Errorf("write README.md: %w", err)
	}

	relPath, _ := filepath.Rel(workspaceRoot, readmePath)
	return relPath, nil
}

// RepoTopicType enumerates valid repo-topic knowledge categories.
type RepoTopicType string

const (
	RepoTopicArchitecture RepoTopicType = "architecture"
	RepoTopicGlossary     RepoTopicType = "glossary"
	RepoTopicRunbook      RepoTopicType = "runbook"
	RepoTopicPostMortem   RepoTopicType = "post-mortem"
)

// ValidRepoTopicTypes is the exhaustive list of repo-topic knowledge types.
var ValidRepoTopicTypes = []RepoTopicType{
	RepoTopicArchitecture, RepoTopicGlossary,
	RepoTopicRunbook, RepoTopicPostMortem,
}

// IsValidRepoTopicType reports whether t is in ValidRepoTopicTypes.
func IsValidRepoTopicType(t RepoTopicType) bool {
	for _, v := range ValidRepoTopicTypes {
		if v == t {
			return true
		}
	}
	return false
}

// RepoTopicResult is returned by ScaffoldRepoTopic.
type RepoTopicResult struct {
	// KnowledgePath is the category directory path relative to workspaceRoot.
	KnowledgePath string `json:"knowledge_path"`
	// FilePath is the created/existing entry file path relative to workspaceRoot.
	FilePath string `json:"file_path"`
	// FilesCreated lists filenames created during this call (empty when Skipped=true).
	FilesCreated []string `json:"files_created"`
	// Skipped is true when the entry file already existed (idempotent call).
	Skipped bool `json:"skipped"`
}

// ScaffoldRepoTopic creates (idempotently) a repo-topic KB entry.
//
// Creates .nightgauge/knowledge/{type}/{slug}.md. When the category
// directory is new, also creates README.md and _template.md. The entry file
// is never clobbered — a second call returns Skipped=true.
func ScaffoldRepoTopic(workspaceRoot string, topicType RepoTopicType, slug string) (RepoTopicResult, error) {
	categoryDir := filepath.Join(workspaceRoot, ".nightgauge", "knowledge", string(topicType))
	relCategoryDir, _ := filepath.Rel(workspaceRoot, categoryDir)

	// Detect whether category dir is new before creating it.
	_, catStatErr := os.Stat(categoryDir)
	categoryIsNew := os.IsNotExist(catStatErr)

	if err := os.MkdirAll(categoryDir, 0o755); err != nil {
		return RepoTopicResult{}, fmt.Errorf("create category directory: %w", err)
	}

	entryPath := filepath.Join(categoryDir, slug+".md")
	relEntryPath, _ := filepath.Rel(workspaceRoot, entryPath)

	result := RepoTopicResult{
		KnowledgePath: relCategoryDir,
		FilePath:      relEntryPath,
		FilesCreated:  []string{},
	}

	// Idempotent: if the entry already exists, return early.
	if _, err := os.Stat(entryPath); err == nil {
		result.Skipped = true
		return result, nil
	}

	// Create README.md and _template.md when the category dir is brand new.
	if categoryIsNew {
		readmePath := filepath.Join(categoryDir, "README.md")
		if err := os.WriteFile(readmePath, []byte(generateRepoTopicREADME(topicType)), 0o644); err != nil {
			return RepoTopicResult{}, fmt.Errorf("write README.md: %w", err)
		}
		result.FilesCreated = append(result.FilesCreated, "README.md")

		templatePath := filepath.Join(categoryDir, "_template.md")
		if err := os.WriteFile(templatePath, []byte(generateRepoTopicTemplate(topicType, "slug")), 0o644); err != nil {
			return RepoTopicResult{}, fmt.Errorf("write _template.md: %w", err)
		}
		result.FilesCreated = append(result.FilesCreated, "_template.md")
	}

	// Create the entry file.
	entryContent := generateRepoTopicTemplate(topicType, slug)
	if err := os.WriteFile(entryPath, []byte(entryContent), 0o644); err != nil {
		return RepoTopicResult{}, fmt.Errorf("write entry file: %w", err)
	}
	result.FilesCreated = append(result.FilesCreated, slug+".md")

	return result, nil
}

// generateRepoTopicREADME produces the README.md for a repo-topic category directory.
func generateRepoTopicREADME(topicType RepoTopicType) string {
	switch topicType {
	case RepoTopicArchitecture:
		return `# Knowledge Base — architecture/

Stores cross-issue architectural principles, layer diagrams, and pattern docs.

**What belongs here**: Repo-wide architectural patterns that AI agents should
consult when planning or implementing features. These are agent-facing working
notes, not public documentation (` + "`docs/ARCHITECTURE.md`" + ` is the human-facing counterpart).

**What does NOT belong here**: Per-issue implementation decisions (use
` + "`features/{N}-{slug}/decisions.md`" + `) or content stable enough for ` + "`docs/`" + `.

**When to add an entry**: When an agent discovers or codifies a pattern that
will be relevant to future pipeline runs.

**Author**: Pipeline / AI agents. Humans may edit for clarity.

## Entries

See ` + "`_template.md`" + ` for the file structure to follow when adding entries.
`
	case RepoTopicGlossary:
		return `# Knowledge Base — glossary/

Stores one-file-per-term definitions of domain vocabulary used across issues.

**What belongs here**: Domain terms, concepts, and jargon that recur across
issues and that agents should look up rather than re-derive.

**What does NOT belong here**: Per-issue acronyms or transient terminology.
Stable terms that belong in developer docs should graduate to ` + "`docs/`" + `.

**When to add an entry**: When a term appears in multiple issues or requires
more than one sentence to explain correctly.

**Author**: Pipeline / AI agents. Humans may edit for clarity.

## Entries

See ` + "`_template.md`" + ` for the file structure to follow when adding entries.
`
	case RepoTopicRunbook:
		return `# Knowledge Base — runbooks/

Stores operational procedures for recurring maintenance tasks and recovery workflows.

**What belongs here**: Step-by-step procedures that agents or operators follow
in response to specific situations (stuck pipelines, stale indices, crashed
processes).

**What does NOT belong here**: One-off fixes or post-mortems (use
` + "`post-mortems/`" + `). Stable runbooks should graduate to ` + "`docs/HEALTH_MONITORING.md`" + ` or
a dedicated docs page.

**When to add an entry**: When you resolve an operational problem and want to
preserve the steps for next time.

**Author**: Pipeline / AI agents. Humans may edit for clarity.

## Entries

See ` + "`_template.md`" + ` for the file structure to follow when adding entries.
`
	case RepoTopicPostMortem:
		return `# Knowledge Base — post-mortems/

Stores incident write-ups and retrospective analyses.

**What belongs here**: Factual accounts of what went wrong, the root cause,
and action items that resulted from an incident.

**What does NOT belong here**: Recurring operational procedures (use
` + "`runbooks/`" + `). Systemic fixes that affect architecture should also be recorded in
` + "`architecture/`" + ` after the post-mortem.

**When to add an entry**: After any pipeline failure, data loss event, or
unexpected outage that took more than 30 minutes to resolve.

**Author**: Pipeline / AI agents. Humans may edit for clarity.

## Entries

See ` + "`_template.md`" + ` for the file structure to follow when adding entries.
`
	default:
		return fmt.Sprintf("# Knowledge Base — %s/\n\nSee `_template.md` for the file structure to follow.\n", topicType)
	}
}

// generateRepoTopicTemplate produces a content template for a repo-topic entry.
// The slug parameter is substituted into the heading.
func generateRepoTopicTemplate(topicType RepoTopicType, slug string) string {
	now := time.Now().UTC().Format(time.RFC3339)
	switch topicType {
	case RepoTopicArchitecture:
		return fmt.Sprintf(`---
type: architecture
created: "%s"
tags: [architecture, pattern, layer]
status: draft
---

# %s

## Overview

<!-- TODO: Describe the architectural principle, pattern, or design decision. -->

## Context

<!-- TODO: Why does this exist? What problem does it solve? -->

## Details

<!-- TODO: Technical details, diagrams, file references. -->

## Related

<!-- TODO: Links to docs/, related issues, related architecture entries. -->
`, now, slug)
	case RepoTopicGlossary:
		return fmt.Sprintf(`---
type: glossary
created: "%s"
tags: [domain-term]
status: draft
---

# %s

## Definition

<!-- TODO: One-sentence definition of the term. -->

## Context

<!-- TODO: Where is this term used? What is the broader context? -->

## Examples

<!-- TODO: Concrete examples of the term in use. -->
`, now, slug)
	case RepoTopicRunbook:
		return fmt.Sprintf(`---
type: runbook
created: "%s"
tags: [operational, procedure]
status: draft
---

# %s

## Purpose

<!-- TODO: What situation does this runbook address? -->

## Prerequisites

<!-- TODO: What must be true before following this runbook? -->

## Steps

<!-- TODO: Step-by-step procedure. -->

1. Step 1

## Verification

<!-- TODO: How do you know the procedure succeeded? -->

## Rollback

<!-- TODO: How to undo the steps if something goes wrong. -->
`, now, slug)
	case RepoTopicPostMortem:
		return fmt.Sprintf(`---
type: post-mortem
created: "%s"
tags: [incident, post-mortem]
status: draft
---

# %s

## Summary

<!-- TODO: One-paragraph description of what happened. -->

## Timeline

<!-- TODO: Chronological list of events. -->

## Root Cause

<!-- TODO: The underlying cause of the incident. -->

## Impact

<!-- TODO: Who was affected? For how long? -->

## Action Items

<!-- TODO: What changes will prevent this from recurring? -->

- [ ] Action item 1

## Lessons Learned

<!-- TODO: Key takeaways for the team. -->
`, now, slug)
	default:
		return fmt.Sprintf("# %s\n\n<!-- TODO: Add content. -->\n", slug)
	}
}

// generateSlug converts a title to a URL-safe kebab-case slug ≤50 chars.
// Matches KnowledgeService.generateSlug() exactly.
var nonAlphanumRe = regexp.MustCompile(`[^a-z0-9]+`)

func generateSlug(title string) string {
	s := strings.ToLower(title)
	s = nonAlphanumRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 50 {
		s = s[:50]
	}
	s = strings.TrimRight(s, "-")
	return s
}

// generatePRD produces PRD.md content matching KnowledgeService.renderPrdBody().
//
// The PRD is the single source of truth for an issue's requirements. Technical
// requirements (the embedded "TRD") live in ## Technical Approach; quality and
// non-functional requirements (the embedded "QRD") live in ## Quality &
// Non-Functional Requirements. These are sections, not separate files — see
// docs/KNOWLEDGE_BASE.md#information-architecture. The H2 section set and order
// here MUST stay in parity with the TypeScript SDK.
func generatePRD(issueNumber int, title string, acceptanceCriteria []string) string {
	acContent := "<!-- TODO: Testable checkboxes — each one a behavior feature-validate can verify\n- [ ] Criterion 1\n- [ ] Criterion 2 -->"
	if len(acceptanceCriteria) > 0 {
		var lines []string
		for _, c := range acceptanceCriteria {
			lines = append(lines, "- [ ] "+c)
		}
		acContent = strings.Join(lines, "\n")
	}

	return fmt.Sprintf(`# PRD: #%d — %s

## Summary

<!-- TODO: 1-2 sentence problem statement — what is missing/broken and why it matters -->

## User Story

<!-- TODO: As a <role>, I want <capability> so that <benefit>. Omit for pure infra/chore work. -->

## Acceptance Criteria

%s

## Technical Approach

<!-- TODO (embedded TRD): design, key components/files, data flow, and implementation constraints.
     This IS the technical requirements doc — keep it here, do not split into a separate TRD file. -->

## Quality & Non-Functional Requirements

<!-- TODO (embedded QRD): test strategy (unit/integration/e2e) plus any performance, security,
     accessibility, or reliability budgets. "None beyond the acceptance criteria" is a valid answer. -->

## Out of Scope

<!-- TODO: What this issue explicitly will NOT do — names the boundary to prevent scope creep. -->

## Status

- [ ] Draft
- [ ] Reviewed
- [ ] Approved
`, issueNumber, title, acContent)
}

// generateDecisionsTemplate produces decisions.md content matching
// KnowledgeService.generateDecisionsTemplate().
func generateDecisionsTemplate(issueNumber int, title string) string {
	return fmt.Sprintf(`# Decisions: #%d — %s

## Architecture Decisions

<!-- Record key architectural decisions made during implementation.
     Add one ADR block per decision. -->

## ADR-001: [Decision Title]

**Status**: Proposed
**Context**: [Background and constraints that led to this decision]
**Decision**: [What was decided and why]
**Consequences**: [Expected impact, trade-offs, and follow-up actions]
`, issueNumber, title)
}

// contentIsSubstantive returns true when content has ≥30 chars of real text
// after stripping HTML comments, headings, table rows, checkboxes, and hr lines.
// Mirrors KnowledgeService.contentIsSubstantive() exactly.
var (
	htmlCommentRe    = regexp.MustCompile(`(?s)<!--.*?-->`)
	headingRe        = regexp.MustCompile(`(?m)^#+\s.*$`)
	tableRowRe       = regexp.MustCompile(`(?m)^\|.*\|$`)
	checkboxRe       = regexp.MustCompile(`(?mi)^-\s*\[[ x]\]\s*\w+$`)
	hrRe             = regexp.MustCompile(`(?m)^---+$`)
	adrPlaceholderRe = regexp.MustCompile(`(?m)^\*\*[\w ]+\*\*:\s*\[.+\]$`)
	whitespaceRe     = regexp.MustCompile(`\s+`)
)

func contentIsSubstantive(content string) bool {
	s := htmlCommentRe.ReplaceAllString(content, "")
	s = headingRe.ReplaceAllString(s, "")
	s = tableRowRe.ReplaceAllString(s, "")
	s = checkboxRe.ReplaceAllString(s, "")
	s = hrRe.ReplaceAllString(s, "")
	s = adrPlaceholderRe.ReplaceAllString(s, "")
	s = whitespaceRe.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)
	return len(s) >= 30
}

// CrossRepoEntry holds knowledge directory metadata for a single sibling repository.
type CrossRepoEntry struct {
	Repo    string   `json:"repo"`
	Path    string   `json:"path"`
	Entries []string `json:"entries"`
}

// WorkspaceKBEntry holds workspace-level knowledge metadata for a single category.
type WorkspaceKBEntry struct {
	Namespace string   `json:"namespace"`
	Path      string   `json:"path"`
	Entries   []string `json:"entries"`
}

// workspaceRepo is the minimal subset of a workspace YAML repository entry.
type workspaceRepo struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// workspaceConfig is the minimal subset of .vscode/nightgauge-workspace.yaml.
type workspaceConfig struct {
	Repositories []workspaceRepo `json:"repositories"`
}

// ScanCrossRepoKnowledge reads .vscode/nightgauge-workspace.yaml from
// workspaceRoot and enumerates .md files (excluding README.md) under each
// sibling repo's .nightgauge/knowledge/ directory. At most limit entries
// are included per repository. Repositories whose knowledge directory is absent
// are silently skipped. Returns an empty slice when the workspace config file
// is missing.
func ScanCrossRepoKnowledge(workspaceRoot string, limit int) ([]CrossRepoEntry, error) {
	if limit <= 0 {
		limit = 20
	}

	configPath := filepath.Join(workspaceRoot, ".vscode", "nightgauge-workspace.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return []CrossRepoEntry{}, nil
		}
		return nil, fmt.Errorf("read workspace config: %w", err)
	}

	cfg, err := parseWorkspaceConfig(data)
	if err != nil {
		return nil, fmt.Errorf("parse workspace config: %w", err)
	}

	var results []CrossRepoEntry

	for _, repo := range cfg.Repositories {
		repoAbs := filepath.Join(workspaceRoot, repo.Path)
		knowledgeDir := filepath.Join(repoAbs, ".nightgauge", "knowledge")

		if _, err := os.Stat(knowledgeDir); err != nil {
			continue
		}

		var entries []string
		_ = filepath.Walk(knowledgeDir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			if strings.HasSuffix(info.Name(), ".md") && info.Name() != "README.md" {
				rel, _ := filepath.Rel(knowledgeDir, path)
				entries = append(entries, rel)
			}
			if len(entries) >= limit {
				return filepath.SkipAll
			}
			return nil
		})

		if len(entries) == 0 {
			continue
		}

		if len(entries) > limit {
			entries = entries[:limit]
		}

		relPath, _ := filepath.Rel(workspaceRoot, knowledgeDir)
		results = append(results, CrossRepoEntry{
			Repo:    repo.Name,
			Path:    relPath,
			Entries: entries,
		})
	}

	return results, nil
}

// ScanWorkspaceKB enumerates top-level .md files (excluding README.md) under
// .nightgauge/knowledge/{product,cross-repo,architecture}/ relative to
// workspaceRoot. At most limit entries are returned in total across all
// categories. Categories with no qualifying files are omitted from the result.
func ScanWorkspaceKB(workspaceRoot string, limit int) ([]WorkspaceKBEntry, error) {
	if limit <= 0 {
		limit = 20
	}

	categories := []string{"product", "cross-repo", "architecture"}
	knowledgeRoot := filepath.Join(workspaceRoot, ".nightgauge", "knowledge")

	var results []WorkspaceKBEntry
	total := 0

	for _, cat := range categories {
		if total >= limit {
			break
		}
		catDir := filepath.Join(knowledgeRoot, cat)
		dirEntries, err := os.ReadDir(catDir)
		if err != nil {
			continue
		}

		var entries []string
		for _, e := range dirEntries {
			if total >= limit {
				break
			}
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") && e.Name() != "README.md" {
				entries = append(entries, e.Name())
				total++
			}
		}

		if len(entries) == 0 {
			continue
		}

		relPath, _ := filepath.Rel(workspaceRoot, catDir)
		results = append(results, WorkspaceKBEntry{
			Namespace: cat,
			Path:      relPath,
			Entries:   entries,
		})
	}

	return results, nil
}

// parseWorkspaceConfig parses a minimal workspace YAML config. It supports both
// the YAML "repositories" key format and gracefully returns an empty config for
// unknown structures. Uses a simple line-by-line parser to avoid importing a
// YAML library — the config subset needed is shallow enough for a basic scanner.
func parseWorkspaceConfig(data []byte) (workspaceConfig, error) {
	// Use encoding/json-compatible struct tags via a minimal YAML subset parser.
	// The nightgauge workspace YAML uses simple key: value and list items.
	// Rather than import gopkg.in/yaml.v3, parse the required fields manually.
	var cfg workspaceConfig
	lines := strings.Split(string(data), "\n")

	inRepos := false
	var currentRepo *workspaceRepo

	for _, raw := range lines {
		line := strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(line)

		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		// Detect "repositories:" section header (top-level key).
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") && trimmed == "repositories:" {
			inRepos = true
			continue
		}

		// Any other top-level key ends the repositories section.
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") && strings.HasSuffix(trimmed, ":") {
			if inRepos && currentRepo != nil {
				cfg.Repositories = append(cfg.Repositories, *currentRepo)
				currentRepo = nil
			}
			inRepos = false
			continue
		}

		if !inRepos {
			continue
		}

		// List item start: "  - name: foo" or "  - name: ..."
		if strings.HasPrefix(trimmed, "- ") {
			if currentRepo != nil {
				cfg.Repositories = append(cfg.Repositories, *currentRepo)
			}
			currentRepo = &workspaceRepo{}
			trimmed = strings.TrimPrefix(trimmed, "- ")
		}

		if currentRepo == nil {
			continue
		}

		// Parse key: value pairs inside a list item.
		if kv, ok := parseYAMLKV(trimmed); ok {
			switch kv[0] {
			case "name":
				currentRepo.Name = kv[1]
			case "path":
				currentRepo.Path = kv[1]
			}
		}
	}

	if inRepos && currentRepo != nil {
		cfg.Repositories = append(cfg.Repositories, *currentRepo)
	}

	return cfg, nil
}

// parseYAMLKV splits "key: value" into [key, value]. Returns ok=false when
// the line is not a key: value pair or the value is empty.
func parseYAMLKV(line string) ([2]string, bool) {
	idx := strings.Index(line, ":")
	if idx < 0 {
		return [2]string{}, false
	}
	key := strings.TrimSpace(line[:idx])
	val := strings.TrimSpace(line[idx+1:])
	// Strip optional inline YAML quotes.
	val = strings.Trim(val, `"'`)
	if key == "" {
		return [2]string{}, false
	}
	return [2]string{key, val}, true
}

// listMDFiles returns .md filenames in a directory, excluding README.md.
func listMDFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") && e.Name() != "README.md" {
			files = append(files, e.Name())
		}
	}
	return files, nil
}

package knowledge

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/knowledge/okf"
)

// RecordOutcomeInput holds the inputs for RecordOutcome.
type RecordOutcomeInput struct {
	IssueNumber    int
	Status         string // complete, partial, failed
	DurationMins   int
	Tokens         int
	CostUSD        float64
	WhatWentWell   string // optional narrative
	WhatDidnt      string // optional narrative
	LessonsLearned string // optional narrative
	PRURL          string // optional; recorded as a source on the entry
}

// RecordOutcomeResult holds the result of RecordOutcome.
type RecordOutcomeResult struct {
	IssueNumber   int     `json:"issue_number"`
	KnowledgePath string  `json:"knowledge_path"`
	TargetFile    string  `json:"target_file"`
	Appended      bool    `json:"appended"`
	FileCreated   bool    `json:"file_created"`
	DateRecorded  string  `json:"date_recorded"`
	Status        string  `json:"status"`
	DurationMins  int     `json:"duration_mins"`
	Tokens        int     `json:"tokens"`
	CostUSD       float64 `json:"cost_usd"`
}

// validOutcomeStatuses is the set of accepted status values.
var validOutcomeStatuses = map[string]bool{
	"complete": true,
	"partial":  true,
	"failed":   true,
}

// RecordOutcome appends a structured ## Outcome Markdown block to the issue's
// decisions.md, then stamps a `verified` event from process:retro and records
// the merged PR as a source.
//
// decisions.md is the only target. The former outcomes.md fallback split the
// retro learning loop across two filenames while decisions.md was the only one
// anything read back; it never produced a file in this tree and is gone.
//
// A missing decisions.md is created rather than reported: PruneEmpty deletes a
// whole issue directory when nothing in it is substantive, and
// knowledge.enabled=false means it was never scaffolded. Failing there would
// lose the outcome — the one durable artifact of the run — over a file this
// function is perfectly able to create correctly.
//
// The verified event is what makes an entry machine-confirmed: retro runs
// after the PR merged, so the decisions it records survived a real merge
// rather than being a model's unreviewed first draft.
//
// The operation is idempotent: if an outcome block for this issue already
// exists in the target file (detected by the "## Outcome" + "**Issue**: #N"
// marker), the function returns Appended=false without modifying the file.
//
// When no knowledge directory exists for the issue, one is created under
// .nightgauge/knowledge/features/{N}-outcome/.
func RecordOutcome(workspaceRoot string, input RecordOutcomeInput) (RecordOutcomeResult, error) {
	if input.IssueNumber <= 0 {
		return RecordOutcomeResult{}, fmt.Errorf("issue number must be positive")
	}
	if !validOutcomeStatuses[input.Status] {
		return RecordOutcomeResult{}, fmt.Errorf("status %q is not valid; must be one of: complete, partial, failed", input.Status)
	}

	knowledgePath, err := findKnowledgePath(workspaceRoot, input.IssueNumber)
	if err != nil {
		// No existing knowledge directory — create a minimal one.
		knowledgePath = filepath.Join(workspaceRoot, ".nightgauge", "knowledge", "features",
			fmt.Sprintf("%d-outcome", input.IssueNumber))
		if mkErr := os.MkdirAll(knowledgePath, 0o755); mkErr != nil {
			return RecordOutcomeResult{}, fmt.Errorf("create knowledge directory: %w", mkErr)
		}
	}

	relPath, _ := filepath.Rel(workspaceRoot, knowledgePath)

	targetPath := filepath.Join(knowledgePath, "decisions.md")
	fileCreated := false

	relTarget, _ := filepath.Rel(workspaceRoot, targetPath)

	// Idempotency: check if outcome block for this issue already exists.
	marker := fmt.Sprintf("**Issue**: #%d", input.IssueNumber)
	if existing, err := os.ReadFile(targetPath); err == nil {
		if strings.Contains(string(existing), marker) {
			return RecordOutcomeResult{
				IssueNumber:   input.IssueNumber,
				KnowledgePath: relPath,
				TargetFile:    relTarget,
				Appended:      false,
				FileCreated:   false,
				DateRecorded:  time.Now().UTC().Format("2006-01-02"),
				Status:        input.Status,
				DurationMins:  input.DurationMins,
				Tokens:        input.Tokens,
				CostUSD:       input.CostUSD,
			}, nil
		}
	} else if os.IsNotExist(err) {
		fileCreated = true
	}

	// Seed a conformant decisions.md when the directory has none, so the
	// outcome lands in a file that carries the frontmatter contract rather
	// than one the conformance check will reject.
	if fileCreated {
		seed, seedErr := seedDecisions(input.IssueNumber)
		if seedErr != nil {
			return RecordOutcomeResult{}, seedErr
		}
		if err := os.WriteFile(targetPath, []byte(seed), 0o644); err != nil {
			return RecordOutcomeResult{}, fmt.Errorf("create %s: %w", relTarget, err)
		}
	}

	block := formatOutcomeBlock(input)

	f, err := os.OpenFile(targetPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return RecordOutcomeResult{}, fmt.Errorf("open %s: %w", relTarget, err)
	}
	if _, err := fmt.Fprint(f, block); err != nil {
		_ = f.Close()
		return RecordOutcomeResult{}, fmt.Errorf("write outcome block: %w", err)
	}
	if err := f.Close(); err != nil {
		return RecordOutcomeResult{}, fmt.Errorf("close %s: %w", relTarget, err)
	}

	// Stamp after the append, so the provenance describes the final file.
	if err := stampRetroVerification(targetPath, input); err != nil {
		return RecordOutcomeResult{}, err
	}

	return RecordOutcomeResult{
		IssueNumber:   input.IssueNumber,
		KnowledgePath: relPath,
		TargetFile:    relTarget,
		Appended:      true,
		FileCreated:   fileCreated,
		DateRecorded:  time.Now().UTC().Format("2006-01-02"),
		Status:        input.Status,
		DurationMins:  input.DurationMins,
		Tokens:        input.Tokens,
		CostUSD:       input.CostUSD,
	}, nil
}

// seedDecisions renders a minimal, contract-conformant decisions.md for an
// issue whose knowledge directory was pruned or never scaffolded.
func seedDecisions(issueNumber int) (string, error) {
	fm, err := okf.ScaffoldFrontmatter(
		okf.TypeDecisions,
		okf.WithTitle(fmt.Sprintf("Decisions: #%d", issueNumber)),
	)
	if err != nil {
		return "", fmt.Errorf("render decisions frontmatter: %w", err)
	}
	return fmt.Sprintf("%s\n# Decisions: #%d\n\n## Architecture Decisions\n", fm, issueNumber), nil
}

// stampRetroVerification records that retro confirmed this entry, and cites the
// merged PR as the source. The actor is constructed, never a literal, so it
// cannot drift from the contract's convention.
func stampRetroVerification(targetPath string, input RecordOutcomeInput) error {
	actor, err := okf.ProcessActor("retro")
	if err != nil {
		return err
	}
	in := okf.StampInput{VerifiedBy: actor}
	if input.PRURL != "" {
		in.Sources = []okf.Source{{
			Resource: input.PRURL,
			Title:    fmt.Sprintf("PR for #%d", input.IssueNumber),
		}}
	}
	if _, _, err := okf.Stamp(targetPath, in); err != nil {
		return fmt.Errorf("stamp retro verification: %w", err)
	}
	return nil
}

// findKnowledgePath locates the knowledge directory for the given issue by
// scanning .nightgauge/knowledge/{features,epics}/ for a directory
// whose name starts with "{issueNumber}-".
func findKnowledgePath(workspaceRoot string, issueNumber int) (string, error) {
	root := filepath.Join(workspaceRoot, ".nightgauge", "knowledge")
	prefix := fmt.Sprintf("%d-", issueNumber)
	for _, category := range []string{"features", "epics"} {
		catDir := filepath.Join(root, category)
		entries, err := os.ReadDir(catDir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", fmt.Errorf("read %s: %w", catDir, err)
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			if strings.HasPrefix(e.Name(), prefix) {
				return filepath.Join(catDir, e.Name()), nil
			}
		}
	}
	return "", fmt.Errorf("no knowledge directory found for issue #%d", issueNumber)
}

// formatOutcomeBlock produces the ## Outcome Markdown section.
func formatOutcomeBlock(input RecordOutcomeInput) string {
	var sb strings.Builder

	sb.WriteString("\n## Outcome\n\n")
	fmt.Fprintf(&sb, "**Issue**: #%d\n", input.IssueNumber)
	fmt.Fprintf(&sb, "**Date**: %s\n", time.Now().UTC().Format("2006-01-02"))
	fmt.Fprintf(&sb, "**Status**: %s\n", input.Status)

	if input.DurationMins > 0 {
		fmt.Fprintf(&sb, "**Pipeline Duration**: %d min\n", input.DurationMins)
	}

	if input.Tokens > 0 {
		if input.CostUSD > 0 {
			fmt.Fprintf(&sb, "**Token Usage**: %d tokens (~$%.2f)\n", input.Tokens, input.CostUSD)
		} else {
			fmt.Fprintf(&sb, "**Token Usage**: %d tokens\n", input.Tokens)
		}
	}

	sb.WriteString("\n### What Went Well\n\n")
	if strings.TrimSpace(input.WhatWentWell) != "" {
		sb.WriteString(input.WhatWentWell)
		if !strings.HasSuffix(input.WhatWentWell, "\n") {
			sb.WriteString("\n")
		}
	} else {
		sb.WriteString("No positive signals recorded.\n")
	}

	sb.WriteString("\n### What Didn't Go Well\n\n")
	if strings.TrimSpace(input.WhatDidnt) != "" {
		sb.WriteString(input.WhatDidnt)
		if !strings.HasSuffix(input.WhatDidnt, "\n") {
			sb.WriteString("\n")
		}
	} else {
		sb.WriteString("No failure signals recorded.\n")
	}

	sb.WriteString("\n### Lessons Learned\n\n")
	if strings.TrimSpace(input.LessonsLearned) != "" {
		sb.WriteString(input.LessonsLearned)
		if !strings.HasSuffix(input.LessonsLearned, "\n") {
			sb.WriteString("\n")
		}
	} else {
		sb.WriteString("No lessons recorded.\n")
	}

	sb.WriteString("\n---\n")

	return sb.String()
}

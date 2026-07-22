// Package context handles context file I/O between pipeline stages.
// Each stage reads context from the previous stage and writes context
// for the next stage. Context files are JSON with a defined schema.
package context

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// StageContext is the JSON structure passed between pipeline stages.
type StageContext struct {
	IssueNumber   int                    `json:"issueNumber"`
	Repo          string                 `json:"repo"`
	Branch        string                 `json:"branch,omitempty"`
	Stage         string                 `json:"stage"`
	PreviousStage string                 `json:"previousStage,omitempty"`
	Data          map[string]interface{} `json:"data,omitempty"`
}

// ReadContext reads a stage context file.
func ReadContext(path string) (*StageContext, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // No context from previous stage
		}
		return nil, fmt.Errorf("read context: %w", err)
	}

	var ctx StageContext
	if err := json.Unmarshal(data, &ctx); err != nil {
		return nil, fmt.Errorf("parse context: %w", err)
	}

	return &ctx, nil
}

// WriteContext writes a stage context file for the next stage.
func WriteContext(path string, ctx *StageContext) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create context dir: %w", err)
	}

	data, err := json.MarshalIndent(ctx, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal context: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write context: %w", err)
	}

	return nil
}

// ContextPath returns the standard path for a stage context file:
// .nightgauge/pipeline/<stage>-<N>.json. This is the flat convention the
// skills actually write and that the gates (registry.contextFilePath) and the
// SDK (cli/commands/stage.ts getContextPath) read — e.g. issue-pickup writes
// issue-<N>.json, pr-create writes pr-<N>.json. (It previously used a nested
// issue-<N>/<stage>-context.json that nothing wrote, so the Go scheduler's
// prerequisite + output validation read phantom paths and failed every
// worktree-isolated `nightgauge run`.)
func ContextPath(workspaceRoot string, issueNumber int, stage string) string {
	return filepath.Join(workspaceRoot, ".nightgauge", "pipeline",
		fmt.Sprintf("%s-%d.json", stage, issueNumber))
}

// Validate checks that a stage context has required fields.
func Validate(ctx *StageContext) error {
	if ctx.IssueNumber <= 0 {
		return fmt.Errorf("invalid issue number: %d", ctx.IssueNumber)
	}
	if ctx.Repo == "" {
		return fmt.Errorf("repo is required")
	}
	if ctx.Stage == "" {
		return fmt.Errorf("stage is required")
	}
	return nil
}

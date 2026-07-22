package context

import (
	"path/filepath"
	"testing"
)

func TestWriteAndReadContext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test-context.json")

	ctx := &StageContext{
		IssueNumber:   1311,
		Repo:          "nightgauge/nightgauge",
		Branch:        "feat/1311-feature",
		Stage:         "feature-dev",
		PreviousStage: "feature-planning",
		Data: map[string]interface{}{
			"planFile": "PLAN.md",
		},
	}

	if err := WriteContext(path, ctx); err != nil {
		t.Fatalf("WriteContext: %v", err)
	}

	loaded, err := ReadContext(path)
	if err != nil {
		t.Fatalf("ReadContext: %v", err)
	}
	if loaded == nil {
		t.Fatal("loaded is nil")
	}
	if loaded.IssueNumber != 1311 {
		t.Errorf("IssueNumber = %d", loaded.IssueNumber)
	}
	if loaded.Stage != "feature-dev" {
		t.Errorf("Stage = %q", loaded.Stage)
	}
	if loaded.Data["planFile"] != "PLAN.md" {
		t.Errorf("Data[planFile] = %v", loaded.Data["planFile"])
	}
}

func TestReadContextMissing(t *testing.T) {
	ctx, err := ReadContext("/nonexistent/path.json")
	if err != nil {
		t.Fatalf("ReadContext: %v", err)
	}
	if ctx != nil {
		t.Error("should return nil for missing file")
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		ctx     *StageContext
		wantErr bool
	}{
		{
			"valid",
			&StageContext{IssueNumber: 1, Repo: "org/repo", Stage: "dev"},
			false,
		},
		{
			"missing issue",
			&StageContext{IssueNumber: 0, Repo: "org/repo", Stage: "dev"},
			true,
		},
		{
			"missing repo",
			&StageContext{IssueNumber: 1, Repo: "", Stage: "dev"},
			true,
		},
		{
			"missing stage",
			&StageContext{IssueNumber: 1, Repo: "org/repo", Stage: ""},
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.ctx)
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestContextPath(t *testing.T) {
	// Flat convention shared with the gates (registry.contextFilePath) and the
	// SDK (stage.ts getContextPath): .nightgauge/pipeline/<stage>-<N>.json.
	path := ContextPath("/workspace", 1311, "dev")
	expected := filepath.Join("/workspace", ".nightgauge", "pipeline", "dev-1311.json")
	if path != expected {
		t.Errorf("ContextPath = %q, want %q", path, expected)
	}
}

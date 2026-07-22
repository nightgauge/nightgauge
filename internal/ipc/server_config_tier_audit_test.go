package ipc

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestConfigTierAudit_HandlerRegistered verifies the config.tierAudit handler
// is present after NewServer.
func TestConfigTierAudit_HandlerRegistered(t *testing.T) {
	srv := NewServer(nil)
	if _, ok := srv.methods["config.tierAudit"]; !ok {
		t.Fatal("config.tierAudit handler not registered")
	}
}

// TestConfigTierAudit_NoWorkspaceRoot returns an error when no workspace root
// is configured and no root is supplied in params.
func TestConfigTierAudit_NoWorkspaceRoot(t *testing.T) {
	srv := NewServer(nil)
	handler := srv.methods["config.tierAudit"]

	params, _ := json.Marshal(ConfigTierAuditParams{})
	_, err := handler(context.Background(), params)
	if err == nil {
		t.Fatal("expected error with no workspace root, got nil")
	}
	if !strings.Contains(err.Error(), "workspace root") {
		t.Errorf("error = %v, want message mentioning workspace root", err)
	}
}

// TestConfigTierAudit_EmptyWorkspace returns a result with no drift when the
// workspace has no config files at all.
func TestConfigTierAudit_EmptyWorkspace(t *testing.T) {
	dir := t.TempDir()
	srv := NewServer(nil)
	srv.workspaceRoot = dir

	handler := srv.methods["config.tierAudit"]
	params, _ := json.Marshal(ConfigTierAuditParams{})
	got, err := handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	res, ok := got.(*ConfigTierAuditResult)
	if !ok {
		t.Fatalf("result type = %T, want *ConfigTierAuditResult", got)
	}
	if res.HasDrift {
		t.Error("HasDrift = true for empty workspace, want false")
	}
}

// TestConfigTierAudit_ParamRootOverridesServerRoot verifies that a non-empty
// Root in params takes precedence over the server's workspaceRoot.
func TestConfigTierAudit_ParamRootOverridesServerRoot(t *testing.T) {
	paramDir := t.TempDir()
	// Server root points to a non-existent path; param root is clean.
	srv := NewServer(nil)
	srv.workspaceRoot = "/nonexistent-path-that-should-not-be-used"

	handler := srv.methods["config.tierAudit"]
	params, _ := json.Marshal(ConfigTierAuditParams{Root: paramDir})
	got, err := handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler with param root: %v", err)
	}
	res := got.(*ConfigTierAuditResult)
	// Clean dir → no drift
	if res.HasDrift {
		t.Error("HasDrift = true for clean param root, want false")
	}
}

// TestConfigTierAudit_DetectsDrift writes a project-tier config with a
// machine-tier key and verifies HasDrift=true and a DRIFT entry is present.
func TestConfigTierAudit_DetectsDrift(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".nightgauge")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// lm_studio.model is a machine-tier key; placing it in project config (config.yaml)
	// causes it to be effective at the project tier, which is not its target — drift.
	projectCfg := []byte("lm_studio:\n  model: llama-test\n")
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), projectCfg, 0o644); err != nil {
		t.Fatal(err)
	}

	srv := NewServer(nil)
	srv.workspaceRoot = dir
	handler := srv.methods["config.tierAudit"]

	params, _ := json.Marshal(ConfigTierAuditParams{})
	got, err := handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	res := got.(*ConfigTierAuditResult)
	if !res.HasDrift {
		t.Error("HasDrift = false, want true when machine-tier key in project config")
	}
	if len(res.Entries) == 0 {
		t.Fatal("Entries is empty, expected at least one drift entry")
	}
	found := false
	for _, e := range res.Entries {
		if e.Key == "lm_studio.model" && strings.HasPrefix(e.Status, "DRIFT") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("no DRIFT entry for lm_studio.model; entries = %+v", res.Entries)
	}
}

// TestConfigTierAudit_NoDriftForCorrectPlacement verifies HasDrift=false when
// every key is placed in its target tier.
func TestConfigTierAudit_NoDriftForCorrectPlacement(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".nightgauge")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// pipeline.model is a project-tier key; placing it in config.yaml is correct.
	projectCfg := []byte("pipeline:\n  model: claude-opus-4-5\n")
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), projectCfg, 0o644); err != nil {
		t.Fatal(err)
	}

	srv := NewServer(nil)
	srv.workspaceRoot = dir
	handler := srv.methods["config.tierAudit"]

	params, _ := json.Marshal(ConfigTierAuditParams{})
	got, err := handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	res := got.(*ConfigTierAuditResult)
	for _, e := range res.Entries {
		if strings.HasPrefix(e.Status, "DRIFT") {
			t.Errorf("unexpected DRIFT entry: %+v", e)
		}
	}
	if res.HasDrift {
		t.Error("HasDrift = true, want false when no drift present")
	}
}

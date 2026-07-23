package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRender_FullYAML(t *testing.T) {
	cfg := &Config{
		Owner:         "nightgauge",
		ProjectNumber: 42,
		LogLevel:      "info",
	}

	out, err := Render(cfg, "", false, false)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(out, "owner: nightgauge") {
		t.Errorf("expected `owner: nightgauge` in output, got:\n%s", out)
	}
	if !strings.Contains(out, "number: 42") {
		t.Errorf("expected `number: 42` in output, got:\n%s", out)
	}
	if !strings.Contains(out, "project:") {
		t.Errorf("expected canonical `project:` block, got:\n%s", out)
	}
}

func TestRenderRedactsCredentialValues(t *testing.T) {
	cfg := DefaultConfig()
	cfg.GitHubAuth = &GitHubAuthConfig{
		Token:  "credential-value-one",
		Tokens: map[string]string{"nightgauge": "credential-value-two", "safe": "env:SAFE_TOKEN"},
	}
	cfg.APIKey = "credential-value-three"
	cfg.LicenseKey = "credential-value-four"

	for _, tc := range []struct {
		key  string
		json bool
		raw  bool
	}{{}, {json: true}, {key: "github_auth.token", raw: true}, {key: "platform.license_key", raw: true}} {
		out, err := Render(cfg, tc.key, tc.json, tc.raw)
		if err != nil {
			t.Fatalf("Render(%q): %v", tc.key, err)
		}
		for _, forbidden := range []string{"credential-value-one", "credential-value-two", "credential-value-three", "credential-value-four"} {
			if strings.Contains(out, forbidden) {
				t.Fatalf("Render(%q) exposed a credential", tc.key)
			}
		}
	}
}

func TestRender_FullJSON(t *testing.T) {
	cfg := &Config{
		Owner:         "nightgauge",
		ProjectNumber: 7,
	}

	out, err := Render(cfg, "", true, false)
	if err != nil {
		t.Fatalf("Render --json: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput:\n%s", err, out)
	}
	project, ok := decoded["project"].(map[string]any)
	if !ok {
		t.Fatalf("expected `project` object in JSON, got %T", decoded["project"])
	}
	if project["owner"] != "nightgauge" {
		t.Errorf("expected project.owner=nightgauge, got %v", project["owner"])
	}
	if project["number"] != float64(7) {
		t.Errorf("expected project.number=7, got %v", project["number"])
	}
}

func TestRender_ScalarKey_ProjectOwner(t *testing.T) {
	cfg := &Config{
		Owner:         "nightgauge",
		ProjectNumber: 42,
	}

	out, err := Render(cfg, "project.owner", false, false)
	if err != nil {
		t.Fatalf("Render --key project.owner: %v", err)
	}
	if strings.TrimRight(out, "\n") != "nightgauge" {
		t.Errorf("expected `nightgauge`, got %q", out)
	}
}

func TestRender_ScalarKey_ProjectNumber(t *testing.T) {
	cfg := &Config{
		Owner:         "nightgauge",
		ProjectNumber: 42,
	}

	out, err := Render(cfg, "project.number", false, true)
	if err != nil {
		t.Fatalf("Render --key project.number --raw: %v", err)
	}
	if out != "42" {
		t.Errorf("expected `42`, got %q", out)
	}
}

func TestRender_NestedSubDocument(t *testing.T) {
	autoActionable := false
	cfg := &Config{
		Owner: "nightgauge",
		Autonomous: &AutonomousConfig{
			StallDetectionMinutes: 5,
			AutoActionable:        &autoActionable,
		},
	}

	out, err := Render(cfg, "autonomous", false, false)
	if err != nil {
		t.Fatalf("Render --key autonomous: %v", err)
	}
	if !strings.Contains(out, "stall_detection_minutes: 5") {
		t.Errorf("expected `stall_detection_minutes: 5` in sub-document, got:\n%s", out)
	}
}

func TestRender_NestedSubDocumentJSON(t *testing.T) {
	cfg := &Config{
		Owner: "nightgauge",
		Autonomous: &AutonomousConfig{
			StallDetectionMinutes: 3,
		},
	}

	out, err := Render(cfg, "autonomous", true, false)
	if err != nil {
		t.Fatalf("Render --key autonomous --json: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("sub-doc JSON invalid: %v\n%s", err, out)
	}
	if decoded["stall_detection_minutes"] != float64(3) {
		t.Errorf("expected stall_detection_minutes=3, got %v", decoded["stall_detection_minutes"])
	}
}

func TestRender_MissingKey(t *testing.T) {
	cfg := &Config{Owner: "nightgauge"}

	_, err := Render(cfg, "does.not.exist", false, false)
	if err == nil {
		t.Fatal("expected error for missing key, got nil")
	}
	if !errors.Is(err, ErrKeyNotFound) {
		t.Errorf("expected ErrKeyNotFound, got %v", err)
	}
	if !strings.Contains(err.Error(), "does.not.exist") {
		t.Errorf("error should include the missing path; got %q", err.Error())
	}
}

func TestRender_RawOnNonScalar(t *testing.T) {
	cfg := &Config{
		Owner: "nightgauge",
		Autonomous: &AutonomousConfig{
			StallDetectionMinutes: 1,
		},
	}

	_, err := Render(cfg, "autonomous", false, true)
	if err == nil {
		t.Fatal("--raw on mapping should error")
	}
	if !errors.Is(err, ErrRawNotScalar) {
		t.Errorf("expected ErrRawNotScalar, got %v", err)
	}
}

func TestRender_RawOnFullConfig(t *testing.T) {
	cfg := &Config{Owner: "nightgauge"}

	_, err := Render(cfg, "", false, true)
	if !errors.Is(err, ErrRawNotScalar) {
		t.Errorf("--raw without --key should error with ErrRawNotScalar, got %v", err)
	}
}

func TestRender_DefaultsOnly(t *testing.T) {
	cfg := DefaultConfig()
	out, err := Render(cfg, "project.owner", false, true)
	if err != nil {
		t.Fatalf("Render defaults: %v", err)
	}
	if out != "nightgauge" {
		t.Errorf("expected default owner nightgauge, got %q", out)
	}
}

func TestRender_NilConfig(t *testing.T) {
	_, err := Render(nil, "", false, false)
	if err == nil {
		t.Fatal("nil config should error")
	}
}

func TestRender_RedactsSecretsInEveryOutputMode(t *testing.T) {
	const token = "github_pat_complete_representative_123456789"
	const partial = "representative_123456789"
	const apiKey = "provider-api-key-secret-987654321"
	const license = "license-key-secret-246813579"
	cfg := &Config{
		GitHubAuth: &GitHubAuthConfig{
			Token:  token,
			Tokens: map[string]string{"nightgauge": token},
		},
		APIKey:     apiKey,
		LicenseKey: license,
	}

	cases := []struct {
		name string
		key  string
		json bool
		raw  bool
	}{
		{name: "full yaml"},
		{name: "full json", json: true},
		{name: "github auth subtree", key: "github_auth"},
		{name: "direct token", key: "github_auth.token"},
		{name: "direct token raw", key: "github_auth.token", raw: true},
		{name: "direct api key", key: "api_key", raw: true},
		{name: "direct license json", key: "platform.license_key", json: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := Render(cfg, tc.key, tc.json, tc.raw)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			for _, forbidden := range []string{token, partial, apiKey, license} {
				if strings.Contains(out, forbidden) {
					t.Fatalf("output contains credential fragment %q: %s", forbidden, out)
				}
			}
			if !strings.Contains(out, RedactedValue) {
				t.Fatalf("output does not contain redaction marker: %s", out)
			}
		})
	}
}

func TestRedactYAML_LeavesEnvironmentVariableReferencesVisible(t *testing.T) {
	in := []byte("forges:\n  github:\n    token_env: GITHUB_TOKEN\ngitlab_inbound:\n  secret_env_var: NIGHTGAUGE_WEBHOOK_SECRET\n")
	out, err := RedactYAMLBytes(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, envName := range []string{"GITHUB_TOKEN", "NIGHTGAUGE_WEBHOOK_SECRET"} {
		if !strings.Contains(string(out), envName) {
			t.Fatalf("environment variable reference %q was hidden: %s", envName, out)
		}
	}
}

// TestRender_RoundtripFromDisk_Nested validates that values written through the
// real Load() path render correctly — guards against tag drift between the
// disk schema (project.number) and the in-memory Config layout.
func TestRender_RoundtripFromDisk_Nested(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, ".nightgauge")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `project:
  owner: TestOrg
  number: 123
  repo: test-repo
`
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	out, err := Render(cfg, "project.number", false, true)
	if err != nil {
		t.Fatalf("Render project.number: %v", err)
	}
	if out != "123" {
		t.Errorf("project.number roundtrip: got %q, want %q", out, "123")
	}
}

// TestRender_RoundtripFromDisk_LegacyFlat ensures the legacy flat YAML format
// still resolves through Render via the same Config struct (the rendered view
// always emits the canonical nested schema regardless of disk format).
func TestRender_RoundtripFromDisk_LegacyFlat(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, ".nightgauge")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `owner: LegacyOrg
project: 99
defaultRepo: legacy-repo
`
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	out, err := Render(cfg, "project.owner", false, true)
	if err != nil {
		t.Fatalf("Render project.owner: %v", err)
	}
	if out != "LegacyOrg" {
		t.Errorf("legacy owner: got %q, want %q", out, "LegacyOrg")
	}

	numOut, err := Render(cfg, "project.number", false, true)
	if err != nil {
		t.Fatalf("Render project.number: %v", err)
	}
	if numOut != "99" {
		t.Errorf("legacy project.number: got %q, want %q", numOut, "99")
	}
}

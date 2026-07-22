package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildTemplate_Placeholders(t *testing.T) {
	out, err := BuildTemplate(InitOptions{Owner: "nightgauge"})
	if err != nil {
		t.Fatalf("BuildTemplate: %v", err)
	}

	for _, token := range []string{
		"<REPO_NAME>",
		"<PROJECT_NUMBER>",
		"<PROJECT_ID>",
		"<STATUS_FIELD_ID>",
		"<PRIORITY_FIELD_ID>",
		"<SIZE_FIELD_ID>",
		"<BACKLOG_OPTION_ID>",
		"<READY_OPTION_ID>",
		"<IN_PROGRESS_OPTION_ID>",
		"<IN_REVIEW_OPTION_ID>",
		"<DONE_OPTION_ID>",
		"<P0_OPTION_ID>",
		"<P1_OPTION_ID>",
		"<P2_OPTION_ID>",
		"<P3_OPTION_ID>",
		"<XS_OPTION_ID>",
		"<S_OPTION_ID>",
		"<M_OPTION_ID>",
		"<L_OPTION_ID>",
		"<XL_OPTION_ID>",
	} {
		if !strings.Contains(out, token) {
			t.Errorf("placeholder template missing %s", token)
		}
	}

	if !strings.Contains(out, "owner: nightgauge") {
		t.Errorf("owner not rendered: %s", out)
	}
	if !strings.Contains(out, "owner_type: org") {
		t.Errorf("default owner_type not rendered as org")
	}
}

func TestBuildTemplate_WithFields(t *testing.T) {
	out, err := BuildTemplate(fullOptions())
	if err != nil {
		t.Fatalf("BuildTemplate: %v", err)
	}

	// No placeholders should remain
	for _, token := range []string{
		"<REPO_NAME>",
		"<PROJECT_NUMBER>",
		"<PROJECT_ID>",
		"<STATUS_FIELD_ID>",
		"<PRIORITY_FIELD_ID>",
		"<SIZE_FIELD_ID>",
		"<BACKLOG_OPTION_ID>",
		"<P0_OPTION_ID>",
		"<XS_OPTION_ID>",
	} {
		if strings.Contains(out, token) {
			t.Errorf("populated template still contains %s", token)
		}
	}

	for _, want := range []string{
		"number: 1",
		`id: "PVT_kwDOAslWDM4BPD3-"`,
		`backlog: "f75ad846"`,
		`p0: "8f87a3ca"`,
		`xs: "6c6483d2"`,
		"repo: nightgauge",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("populated template missing %q", want)
		}
	}
}

func TestBuildTemplate_PartialFields(t *testing.T) {
	opts := fullOptions()
	opts.PriorityFieldID = ""
	opts.PriorityOptions = nil

	out, err := BuildTemplate(opts)
	if err != nil {
		t.Fatalf("BuildTemplate: %v", err)
	}

	// Status and Size should be populated
	if !strings.Contains(out, `backlog: "f75ad846"`) {
		t.Errorf("status options should remain populated")
	}
	if !strings.Contains(out, `xs: "6c6483d2"`) {
		t.Errorf("size options should remain populated")
	}

	// Priority should fall back to placeholders
	for _, token := range []string{
		"<PRIORITY_FIELD_ID>",
		"<P0_OPTION_ID>",
		"<P1_OPTION_ID>",
		"<P2_OPTION_ID>",
		"<P3_OPTION_ID>",
	} {
		if !strings.Contains(out, token) {
			t.Errorf("partial template missing priority placeholder %s", token)
		}
	}
}

func TestBuildTemplate_OwnerTypeUser(t *testing.T) {
	out, err := BuildTemplate(InitOptions{
		Owner:     "alice",
		OwnerType: "user",
	})
	if err != nil {
		t.Fatalf("BuildTemplate: %v", err)
	}

	if !strings.Contains(out, "owner: alice\n") {
		t.Errorf("user owner not rendered")
	}
	if !strings.Contains(out, "owner_type: user") {
		t.Errorf("user owner_type not rendered")
	}
}

func TestBuildTemplate_RejectsMissingOwner(t *testing.T) {
	_, err := BuildTemplate(InitOptions{})
	if err == nil {
		t.Fatal("expected error when owner is empty")
	}
	if !strings.Contains(err.Error(), "owner is required") {
		t.Errorf("error should mention owner; got %v", err)
	}
}

func TestBuildTemplate_RejectsBadOwnerType(t *testing.T) {
	_, err := BuildTemplate(InitOptions{Owner: "nightgauge", OwnerType: "team"})
	if err == nil {
		t.Fatal("expected error for invalid owner_type")
	}
	if !strings.Contains(err.Error(), "owner_type") {
		t.Errorf("error should mention owner_type; got %v", err)
	}
}

// TestBuildTemplate_GoldenPlaceholders compares the placeholder template
// output byte-for-byte against the checked-in fixture so the canonical token
// set never drifts silently.
func TestBuildTemplate_GoldenPlaceholders(t *testing.T) {
	out, err := BuildTemplate(InitOptions{Owner: "nightgauge"})
	if err != nil {
		t.Fatalf("BuildTemplate: %v", err)
	}
	want := readFixture(t, "placeholders.yaml")
	if out != want {
		t.Errorf("placeholder output drift\n--- got ---\n%s\n--- want ---\n%s", out, want)
	}
}

// TestBuildTemplate_GoldenWithFields locks the fully-populated rendering
// against a checked-in fixture so a future field-shape change is caught here
// rather than at the consuming-skill layer.
func TestBuildTemplate_GoldenWithFields(t *testing.T) {
	out, err := BuildTemplate(fullOptions())
	if err != nil {
		t.Fatalf("BuildTemplate: %v", err)
	}
	want := readFixture(t, "with-fields.yaml")
	if out != want {
		t.Errorf("populated output drift\n--- got ---\n%s\n--- want ---\n%s", out, want)
	}
}

// TestBuildTemplate_RoundTripsThroughLoader writes a populated template to a
// temp .nightgauge/config.yaml and asserts the Go parser reads back the
// owner / project number cleanly. Guards against template drift from the
// schema understood by parseYAMLNested.
func TestBuildTemplate_RoundTripsThroughLoader(t *testing.T) {
	out, err := BuildTemplate(fullOptions())
	if err != nil {
		t.Fatalf("BuildTemplate: %v", err)
	}

	dir := t.TempDir()
	configDir := filepath.Join(dir, ".nightgauge")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load template: %v", err)
	}
	if cfg.Owner != "nightgauge" {
		t.Errorf("Owner = %q, want nightgauge", cfg.Owner)
	}
	if cfg.ProjectNumber != 1 {
		t.Errorf("ProjectNumber = %d, want 1", cfg.ProjectNumber)
	}
	if cfg.DefaultRepo != "nightgauge" {
		t.Errorf("DefaultRepo = %q, want nightgauge", cfg.DefaultRepo)
	}
}

func fullOptions() InitOptions {
	return InitOptions{
		Owner:         "nightgauge",
		OwnerType:     "org",
		Repo:          "nightgauge",
		ProjectNumber: 1,
		ProjectID:     "PVT_kwDOAslWDM4BPD3-",
		StatusFieldID: "PVTSSF_lADOAslWDM4BPD3-zg9lKDw",
		StatusOptions: map[string]string{
			"backlog":     "f75ad846",
			"ready":       "61e4505c",
			"in-progress": "47fc9ee4",
			"in-review":   "df73e18b",
			"done":        "98236657",
		},
		PriorityFieldID: "PVTSSF_lADOAslWDM4BPD3-zg9lKEU",
		PriorityOptions: map[string]string{
			"p0": "8f87a3ca",
			"p1": "fb4682e2",
			"p2": "98303a66",
			"p3": "82f1ea49",
		},
		SizeFieldID: "PVTSSF_lADOAslWDM4BPD3-zg9lKEY",
		SizeOptions: map[string]string{
			"xs": "6c6483d2",
			"s":  "f784b110",
			"m":  "7515a9f1",
			"l":  "817d0097",
			"xl": "db339eb2",
		},
	}
}

func readFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "init", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(data)
}

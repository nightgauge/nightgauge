package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/doctor"
	"github.com/nightgauge/nightgauge/internal/execution/adapters"
)

// TestDoctorRender: the human output is driven by the registry, so every
// registered check renders — including github_identity, project_mapping and
// board_population, which the old hand-kept order list never printed (#2088).
// With NO_COLOR set the output carries no ANSI escape.
func TestDoctorRender(t *testing.T) {
	cfg := &config.Config{Owner: "acme", DefaultRepo: "widgets", ProjectNumber: 7}
	result := doctor.RunDoctor(context.Background(), cfg, nil, nil)

	var buf bytes.Buffer
	renderDoctorHuman(&buf, result, true)
	out := buf.String()
	for _, id := range doctor.DefaultRegistry().IDs() {
		if !strings.Contains(out, " "+id+" ") && !strings.Contains(out, " "+id+"\n") {
			t.Errorf("registered check %q does not render:\n%s", id, out)
		}
	}
	for _, label := range []string{"BLOCKER", "NGD004", "Status: broken"} {
		if !strings.Contains(out, label) {
			t.Errorf("output lacks %q:\n%s", label, out)
		}
	}
	if !strings.Contains(out, "\x1b[") {
		t.Error("color requested but no ANSI emitted")
	}

	t.Setenv("NO_COLOR", "1")
	buf.Reset()
	renderDoctorHuman(&buf, result, doctor.ColorEnabled(os.Stdout))
	if strings.Contains(buf.String(), "\x1b[") {
		t.Errorf("NO_COLOR output contains ANSI:\n%s", buf.String())
	}
}

// TestDoctorJSONPreflightFields runs PREFLIGHT's exact jq expressions against
// JSON v2 for a blocker fixture and a healthy fixture; they yield what the v1
// output yielded.
func TestDoctorJSONPreflightFields(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not installed")
	}
	jq := func(t *testing.T, res doctor.DoctorResult, expr string) string {
		t.Helper()
		js, err := json.Marshal(res)
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("jq", "-r", expr)
		cmd.Stdin = bytes.NewReader(js)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("jq %s: %v", expr, err)
		}
		return strings.TrimRight(string(out), "\n")
	}
	authMsg := "GitHub authentication failed — set GITHUB_TOKEN or run `gh auth login`"
	blocker := doctor.BuildResult([]doctor.CheckResult{
		{ID: "github_auth", Status: doctor.StatusFailed, Findings: []doctor.Finding{{
			Code: "NGD004", Check: "github_auth", Severity: doctor.SeverityBlocker, Title: authMsg}}},
		{ID: "gh", Status: doctor.StatusFailed, Findings: []doctor.Finding{{
			Code: "NGD003", Check: "gh", Severity: doctor.SeverityWarning, Title: "gh CLI not found in PATH"}}},
		{ID: "worktree_leaks", Status: doctor.StatusFailed, Findings: []doctor.Finding{{
			Code: "NGD017", Check: "worktree_leaks", Severity: doctor.SeverityHousekeeping, Title: "1 leaked worktree"}}},
	})
	blocker.InstallInstructions = "install me"
	healthy := doctor.BuildResult([]doctor.CheckResult{{ID: "binary", Status: doctor.StatusPassed}})

	cases := []struct {
		name string
		res  doctor.DoctorResult
		want map[string]string
	}{
		{"blocker", blocker, map[string]string{
			".failed_checks[]? // empty":     "github_auth",
			".errors[]":                      authMsg,
			".warnings[]":                    "gh CLI not found in PATH",
			".install_instructions // empty": "install me",
			".v, .exit_code, .healthy":       "2\n2\nfalse",
		}},
		{"healthy", healthy, map[string]string{
			".failed_checks[]? // empty":     "",
			".errors[]":                      "",
			".warnings[]":                    "",
			".install_instructions // empty": "",
			".v, .exit_code, .healthy":       "2\n0\ntrue",
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for expr, want := range c.want {
				if got := jq(t, c.res, expr); got != want {
					t.Errorf("jq %q = %q, want %q", expr, got, want)
				}
			}
		})
	}
}

// TestWriteAdapterRows_WarnFloorIsAWarnRow: a CLI below a warn-policy floor is
// usable (ok true) but below its floor (version_ok false). Its row carries the
// ⚠ mark, names the floor, and prints the remediation, which a ✓ row never
// does; a CLI that is not usable keeps the ✗ mark.
func TestWriteAdapterRows_WarnFloorIsAWarnRow(t *testing.T) {
	warn := doctor.AdapterHealth{
		Adapter: "claude-headless", Kind: "cli", Binary: "claude", Installed: true,
		Version: "2.1.100", MinVersion: "2.1.223", VersionOK: false, OK: true,
		Remediation: "Update claude to >= 2.1.223 (current 2.1.100).",
	}
	healthy := doctor.AdapterHealth{
		Adapter: "codex", Kind: "cli", Binary: "codex", Installed: true,
		Version: "0.145.0", MinVersion: "0.111.0", VersionOK: true, OK: true,
	}
	missing := doctor.AdapterHealth{
		Adapter: "gemini", Kind: "cli", Binary: "gemini", MinVersion: "0.29.0",
		Remediation: "Install the gemini CLI and ensure it is on PATH.",
	}

	var buf bytes.Buffer
	writeAdapterRows(&buf, []doctor.AdapterHealth{warn, healthy, missing})
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	want := []string{
		"  ⚠  claude-headless  claude 2.1.100 (below min 2.1.223)",
		"        → Update claude to >= 2.1.223 (current 2.1.100).",
		"  ✓  codex           codex 0.145.0",
		"  ✗  gemini          gemini CLI not found on PATH",
		"        → Install the gemini CLI and ensure it is on PATH.",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("rows:\n%s\nwant:\n%s", buf.String(), strings.Join(want, "\n"))
	}
}

// TestWriteAdapterRows_OpenCodeEndpointRows (#1678, AC6): one readiness row
// per declared endpoint, naming id, kind, reachable, whether the model is
// loaded and its declared slots; a problem or a warning prints beneath it.
// No base_url or host appears — the row never receives one.
func TestWriteAdapterRows_OpenCodeEndpointRows(t *testing.T) {
	loadedTrue, loadedFalse := true, false
	one := 1
	oc := &doctor.OpenCodeHealth{
		Enabled: true,
		Endpoints: []adapters.OpenCodeEndpointReadiness{
			{Endpoint: "mtplx", Kind: "openai-compatible", Reachable: true, Loaded: &loadedTrue, Ready: true, Slots: 2, SlotsInUse: &one},
			{Endpoint: "mtplx-remote", Kind: "openai-compatible", Reachable: false, Loaded: &loadedFalse, Problem: "endpoint mtplx-remote is not answering (connection refused)"},
		},
	}
	row := doctor.AdapterHealth{Adapter: "opencode", Kind: "cli", Binary: "opencode", OK: true, OpenCode: oc}

	var buf bytes.Buffer
	writeAdapterRows(&buf, []doctor.AdapterHealth{row})
	out := buf.String()
	for _, want := range []string{
		"endpoint mtplx (openai-compatible): reachable=true model_loaded=yes slots=2 in_use=1",
		"endpoint mtplx-remote (openai-compatible): reachable=false model_loaded=no slots=not declared",
		"endpoint mtplx-remote is not answering (connection refused)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not contain %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "10.") || strings.Contains(out, "127.0.0.1") || strings.Contains(out, "http://") {
		t.Errorf("output holds an address: %s", out)
	}
}

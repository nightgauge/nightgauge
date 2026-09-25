package main

import (
	"bytes"
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/doctor"
	"github.com/nightgauge/nightgauge/internal/execution/adapters"
)

// TestDoctorCheckOrder_CoversEveryEmittedCheck pins the render list against
// what RunDoctor actually produces (#912).
//
// The failure this catches is silent by construction: a new arm added to
// RunDoctor lands in result.Checks and in --json, its own unit tests pass, and
// the human `nightgauge doctor` output — the surface an operator actually
// reads — never mentions it. That reads as "the product does not check for
// that", which is exactly the invisibility the stranded-branch arm was added
// to end. Adding the arm and forgetting this list would have reproduced the
// bug one layer up.
//
// Driven off a real RunDoctor call rather than a hand-listed set, so a future
// arm is covered without anyone remembering to extend the test.
func TestDoctorCheckOrder_CoversEveryEmittedCheck(t *testing.T) {
	// nil client/config is enough: every environment-independent arm still
	// writes its row, which is all this test reads.
	result := doctor.RunDoctor(context.Background(), nil, nil, nil)

	rendered := make(map[string]bool, len(doctorCheckOrder))
	for _, key := range doctorCheckOrder {
		rendered[key] = true
	}

	var missing []string
	for key := range result.Checks {
		if !rendered[key] {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("doctor emits %v but the human output never renders them — add them to doctorCheckOrder", missing)
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

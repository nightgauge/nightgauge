package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
)

func runPhaseReconcile(t *testing.T, args ...string) ([]orchestrator.PhaseTransition, error) {
	t.Helper()
	var out bytes.Buffer
	cmd := phaseCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(append([]string{"reconcile"}, args...))
	if err := cmd.Execute(); err != nil {
		return nil, err
	}
	var got []orchestrator.PhaseTransition
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not a JSON array: %v\n%s", err, out.String())
	}
	return got, nil
}

func TestPhaseReconcileVerb(t *testing.T) {
	root := layouttest.Repo(t)
	dir := layouttest.MkPipelineDir(t, root)
	data, err := os.ReadFile(filepath.Join("..", "..", "internal", "orchestrator", "testdata", "agentic_phases", "planning-success.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "planning-4242.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	since := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	base := []string{"--stage", "feature-planning", "--issue", "4242", "--since", since, "--workdir", root, "--json"}

	tests := []struct {
		name      string
		extra     []string
		wantClaim bool
	}{
		{"success", []string{"--exit-code", "0", "--gate-passed"}, true},
		{"gate not passed", []string{"--exit-code", "0"}, false},
		{"nonzero exit", []string{"--exit-code", "1", "--gate-passed"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := runPhaseReconcile(t, append(base, tc.extra...)...)
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantClaim != (len(got) > 0) {
				t.Fatalf("claimed %d transitions, want claim=%v", len(got), tc.wantClaim)
			}
			for _, tr := range got {
				if tr.Stage != "feature-planning" || tr.Total != 13 || (tr.Status != "complete" && tr.Status != "skipped") {
					t.Errorf("bad transition %+v", tr)
				}
			}
		})
	}
	if _, err := runPhaseReconcile(t, "--stage", "feature-dev", "--issue", "4242", "--since", "yesterday"); err == nil {
		t.Error("non-RFC3339 --since accepted")
	}
}

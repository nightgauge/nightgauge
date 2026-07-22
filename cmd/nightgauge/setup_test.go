package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSetupScaffoldToolingCmd_Smoke(t *testing.T) {
	dir := t.TempDir()
	cmd := setupCmd()
	cmd.SetContext(context.Background())

	var stdout bytes.Buffer
	// Replace stdout for this test — printJSON writes to fmt.Println.
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w

	cmd.SetArgs([]string{"scaffold-tooling", "--workdir", dir, "--select", "ci", "--json"})
	execErr := cmd.Execute()

	w.Close()
	os.Stdout = origStdout
	_, _ = stdout.ReadFrom(r)

	if execErr != nil {
		t.Fatalf("Execute returned error: %v", execErr)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("output not valid JSON: %v\n%s", err, stdout.String())
	}
	outcomes, ok := result["outcomes"].([]interface{})
	if !ok || len(outcomes) != 1 {
		t.Fatalf("expected exactly 1 outcome, got %v", result["outcomes"])
	}
	first := outcomes[0].(map[string]interface{})
	if first["key"] != "ci" {
		t.Errorf("outcomes[0].key = %v, want ci", first["key"])
	}
	if first["outcome"] != "created" {
		t.Errorf("outcomes[0].outcome = %v, want created", first["outcome"])
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".github/workflows/ci.yml")); statErr != nil {
		t.Errorf("ci.yml not written: %v", statErr)
	}
}

func TestParseSelect(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{"tsconfig", []string{"tsconfig"}},
		{"tsconfig, vitest ,ci", []string{"tsconfig", "vitest", "ci"}},
		{",,tsconfig,,", []string{"tsconfig"}},
	}
	for _, tc := range cases {
		got := parseSelect(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("parseSelect(%q) = %v, want %v", tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("parseSelect(%q)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
			}
		}
	}
}

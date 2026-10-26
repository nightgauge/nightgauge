package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestIssueCheckOwnershipCmd_BodyFileJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "present.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	body := "- [ ] The flag is documented\n\n<!-- nightgauge:dependency-metadata\nfile_ownership:\n" +
		"  - present.go\n  - gone.md\n-->\n"
	bodyFile := filepath.Join(dir, "body.md")
	if err := os.WriteFile(bodyFile, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := issueCheckOwnershipCmd()
	cmd.SetArgs([]string{"--body-file", bodyFile, "--workdir", dir, "--json"})
	out := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("execute: %v", err)
		}
	})

	var got struct {
		Source   string   `json:"source"`
		Declared []string `json:"declared"`
		Count    int      `json:"count"`
		Findings []struct {
			Type string `json:"type"`
			Rule string `json:"rule"`
			Path string `json:"path"`
		} `json:"findings"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if got.Source != "declared" || len(got.Declared) != 2 || got.Count != 1 {
		t.Fatalf("got %+v", got)
	}
	f := got.Findings[0]
	if f.Type != "FILE_OWNERSHIP_UNSUBSTANTIATED" || f.Rule != "path-missing" || f.Path != "gone.md" {
		t.Errorf("finding = %+v", f)
	}
}

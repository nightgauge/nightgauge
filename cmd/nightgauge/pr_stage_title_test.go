package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
)

func runPrStageTitle(t *testing.T, root, issue string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := prStageTitleCmd()
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs([]string{issue, "--workdir", root})
	err = cmd.Execute()
	return out.String(), errb.String(), err
}

func TestPrStageTitle(t *testing.T) {
	root := layouttest.Repo(t)
	dir := layouttest.MkPipelineDir(t, root)
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if _, _, err := runPrStageTitle(t, root, "42"); err == nil {
		t.Fatal("no issue context must fail so the caller does not open a bare-scope PR")
	}

	write("issue-42.json", `{"title":"the bug as reported","type":"bug"}`)
	stdout, stderr, err := runPrStageTitle(t, root, "42")
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "fix(#42): the bug as reported\n" {
		t.Errorf("stdout = %q: it must carry only the title", stdout)
	}
	if !strings.Contains(stderr, "title falls back to the issue title") {
		t.Errorf("stderr = %q", stderr)
	}

	write("dev-42.json", `{"pr_summary":"state the change"}`)
	stdout, stderr, err = runPrStageTitle(t, root, "42")
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "fix(#42): state the change\n" {
		t.Errorf("stdout = %q", stdout)
	}
	if stderr != "" {
		t.Errorf("a summary needs no note, stderr = %q", stderr)
	}
}

package contractrollout

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
)

func TestMain(m *testing.M) {
	gittest.IsolateProcess()
	os.Exit(m.Run())
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// --- contract manifest ---

func TestParseDefaultsAndValidation(t *testing.T) {
	c, err := Parse(t.TempDir(), []byte("name: demo\nfiles:\n  - path: a.sh\ntargets:\n  - repo: o/r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Branch != "contract/demo" || c.Base != "main" || c.CommitMessage != "chore: adopt the demo contract" || c.PR.Title != c.CommitMessage {
		t.Errorf("defaults = %+v", c)
	}

	for name, src := range map[string]string{
		"unknown key":       "name: demo\nfile:\n  - path: a\n",
		"escaping path":     "name: demo\nfiles:\n  - path: ../secret\n",
		"absolute path":     "name: demo\nfiles:\n  - path: /etc/passwd\n",
		"path inside .git":  "name: demo\nfiles:\n  - path: .git/config\n",
		"bad label color":   "name: demo\nlabels:\n  - name: x\n    color: red\n",
		"workflow location": "name: demo\nci_job:\n  workflow: ci.yml\n  id: x\n  job: 'runs-on: x'\n",
		"bad job id":        "name: demo\nci_job:\n  workflow: .github/workflows/a.yml\n  id: 'a b'\n  job: 'runs-on: x'\n",
		"empty job":         "name: demo\nci_job:\n  workflow: .github/workflows/a.yml\n  id: a\n  job: ''\n",
		"duplicate target":  "name: demo\nfiles:\n  - path: a\ntargets:\n  - repo: o/r\n  - repo: O/R\n",
		"bad repo":          "name: demo\nfiles:\n  - path: a\ntargets:\n  - repo: r\n",
		"changes nothing":   "name: demo\n",
		"bad name":          "name: Demo Contract\nfiles:\n  - path: a\n",
		"option branch":     "name: demo\nbranch: --force\nfiles:\n  - path: a\n",
		"replaces the gate": "name: demo\nfiles:\n  - path: x.sh\n    target: scripts/ci-local.sh\n",
		"manifest gate":     "name: demo\nfiles:\n  - path: a\ntargets:\n  - repo: o/r\n    gate: [sh, -c, id]\n",
		"manifest path":     "name: demo\nfiles:\n  - path: a\ntargets:\n  - repo: o/r\n    path: /etc\n",
	} {
		if _, err := Parse(t.TempDir(), []byte(src)); err == nil {
			t.Errorf("%s: Parse accepted %q", name, src)
		}
	}
}

func TestRenderBody(t *testing.T) {
	c, err := Parse(t.TempDir(), []byte("name: demo\nfiles:\n  - path: a\npr:\n  body: 'adopt {{.Contract}} in {{.Repo}}'\n"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.RenderBody("o/r")
	if err != nil || got != "adopt demo in o/r" {
		t.Fatalf("RenderBody = %q, %v", got, err)
	}
}

// --- byte-identical copy, one target repository ---

func TestCopyFilesByteIdentical(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	script := "#!/bin/sh\r\necho \"\xe2\x9c\x93 no trailing newline\""
	writeFile(t, filepath.Join(src, "scripts", "check.sh"), script, 0o755)
	writeFile(t, filepath.Join(src, "docs", "same.md"), "same\n", 0o644)
	writeFile(t, filepath.Join(dst, "docs", "same.md"), "same\n", 0o644)
	writeFile(t, filepath.Join(src, "a.txt"), "new", 0o644)
	files := []File{{Path: "scripts/check.sh"}, {Path: "docs/same.md"}, {Path: "a.txt", Target: "nested/b.txt"}}

	plan, err := CopyFiles(src, dst, files, false)
	if err != nil {
		t.Fatal(err)
	}
	if !plan[0].Changed || plan[1].Changed || !plan[2].Changed {
		t.Errorf("plan = %+v", plan)
	}
	if _, err := os.Stat(filepath.Join(dst, "scripts", "check.sh")); !os.IsNotExist(err) {
		t.Fatal("planning wrote a file")
	}

	res, err := CopyFiles(src, dst, files, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(dst, "scripts", "check.sh")); got != script {
		t.Errorf("copy is not byte-identical: %q", got)
	}
	if info, _ := os.Stat(filepath.Join(dst, "scripts", "check.sh")); info.Mode()&0o111 == 0 {
		t.Error("the executable bit was not kept")
	}
	if readFile(t, filepath.Join(dst, "nested", "b.txt")) != "new" {
		t.Error("a renamed target was not written")
	}
	if res[0].SHA256 == "" || res[0].SHA256 == res[2].SHA256 {
		t.Errorf("digests = %+v", res)
	}

	again, err := CopyFiles(src, dst, files, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range again {
		if r.Changed {
			t.Errorf("a second copy changed %s", r.Path)
		}
	}

	// A mode-only difference is a change too.
	if err := os.Chmod(filepath.Join(dst, "scripts", "check.sh"), 0o644); err != nil {
		t.Fatal(err)
	}
	if r, _ := CopyFiles(src, dst, files[:1], false); !r[0].Changed {
		t.Error("a lost executable bit was not a change")
	}
}

func TestCopyFilesRefusesSymlinkedTarget(t *testing.T) {
	src, dst, outside := t.TempDir(), t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "scripts", "x.sh"), "x", 0o644)
	if err := os.Symlink(outside, filepath.Join(dst, "scripts")); err != nil {
		t.Fatal(err)
	}
	if _, err := CopyFiles(src, dst, []File{{Path: "scripts/x.sh"}}, true); err == nil {
		t.Fatal("a copy through a symlinked directory was allowed")
	}
	if _, err := os.Stat(filepath.Join(outside, "x.sh")); !os.IsNotExist(err) {
		t.Fatal("the copy wrote outside the repository")
	}
}

// --- label provisioning, one target repository ---

type fakeLabels struct {
	have    []Label
	created []Label
	listErr error
}

func (f *fakeLabels) List(context.Context) ([]Label, error) { return f.have, f.listErr }
func (f *fakeLabels) Create(_ context.Context, l Label) error {
	f.created = append(f.created, l)
	f.have = append(f.have, l)
	return nil
}

func TestProvisionLabels(t *testing.T) {
	fl := &fakeLabels{have: []Label{
		{Name: "Present", Color: "AABBCC", Description: "d"},
		{Name: "drifted", Color: "000000", Description: "old"},
	}}
	want := []Label{
		{Name: "present", Color: "aabbcc", Description: "d"},
		{Name: "drifted", Color: "ffffff", Description: "new"},
		{Name: "missing", Color: "123456", Description: "m"},
	}

	plan, err := ProvisionLabels(context.Background(), fl, want, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(fl.created) != 0 || len(plan.Created) != 1 {
		t.Fatalf("planning created %v, plan %+v", fl.created, plan)
	}

	res, err := ProvisionLabels(context.Background(), fl, want, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(fl.created) != 1 || fl.created[0].Name != "missing" {
		t.Errorf("created = %+v", fl.created)
	}
	if strings.Join(res.Present, ",") != "present" || len(res.Drift) != 1 || !strings.HasPrefix(res.Drift[0], "drifted") {
		t.Errorf("result = %+v", res)
	}

	again, _ := ProvisionLabels(context.Background(), fl, want, true)
	if len(again.Created) != 0 || len(fl.created) != 1 {
		t.Errorf("a second run created labels again: %+v", again)
	}

	fl.listErr = errors.New("boom")
	if _, err := ProvisionLabels(context.Background(), fl, want, true); err == nil {
		t.Error("a list failure was not returned")
	}
}

// --- CI job insertion, one target repository ---

const testJob = `runs-on: ubuntu-latest
steps:
  - run: bash scripts/check.sh
`

func TestInsertCIJobCreatesWorkflow(t *testing.T) {
	root := t.TempDir()
	j := CIJob{Workflow: ".github/workflows/check.yml", WorkflowName: "Check", ID: "check", Job: testJob,
		On: "pull_request:\npush:\n  branches: [main]\n"}
	r, err := InsertCIJob(root, j, true)
	if err != nil || r.Action != "created" {
		t.Fatalf("InsertCIJob = %+v, %v", r, err)
	}
	if err := verifyJob([]byte(readFile(t, filepath.Join(root, j.Workflow))), "check", mustJob(t)); err != nil {
		t.Fatal(err)
	}
	again, err := InsertCIJob(root, j, true)
	if err != nil || again.Action != "present" {
		t.Fatalf("second insert = %+v, %v", again, err)
	}
}

func mustJob(t *testing.T) map[string]any {
	t.Helper()
	j, err := parseJob(testJob)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func TestInsertCIJobSplicesIntoExistingWorkflow(t *testing.T) {
	for name, existing := range map[string]string{
		"jobs last": `# Lint workflow, comments survive.
name: Lint
on: [pull_request]
jobs:
    lint:            # four-space indent is kept
        runs-on: ubuntu-latest
        steps:
            - run: make lint

# trailing comment
`,
		"key after jobs": `name: Lint
on: [pull_request]
jobs:
  lint:
    runs-on: ubuntu-latest
    steps:
      - run: make lint
# belongs to env
env:
  FOO: bar
`,
		"no trailing newline": "name: Lint\non: [pull_request]\njobs:\n  lint:\n    runs-on: x\n    steps:\n      - run: y",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			wf := filepath.Join(root, ".github", "workflows", "lint.yml")
			writeFile(t, wf, existing, 0o644)
			j := CIJob{Workflow: ".github/workflows/lint.yml", ID: "check", Job: testJob}

			plan, err := InsertCIJob(root, j, false)
			if err != nil || plan.Action != "inserted" || readFile(t, wf) != existing {
				t.Fatalf("plan = %+v, %v; or planning wrote", plan, err)
			}
			if _, err := InsertCIJob(root, j, true); err != nil {
				t.Fatal(err)
			}
			got := readFile(t, wf)
			if err := verifyJob([]byte(got), "check", mustJob(t)); err != nil {
				t.Fatal(err)
			}
			// Every original line is still there, in order.
			rest := got
			for _, line := range strings.Split(strings.TrimRight(existing, "\n"), "\n") {
				i := strings.Index(rest, line)
				if i < 0 {
					t.Fatalf("original line %q lost or reordered:\n%s", line, got)
				}
				rest = rest[i+len(line):]
			}
			if r, err := InsertCIJob(root, j, true); err != nil || r.Action != "present" {
				t.Fatalf("second insert = %+v, %v", r, err)
			}
		})
	}
}

func TestInsertCIJobRefusesConflictAndFlowMapping(t *testing.T) {
	root := t.TempDir()
	wf := filepath.Join(root, ".github", "workflows", "a.yml")
	writeFile(t, wf, "on: push\njobs:\n  check:\n    runs-on: other\n    steps: [{run: x}]\n", 0o644)
	_, err := InsertCIJob(root, CIJob{Workflow: ".github/workflows/a.yml", ID: "check", Job: testJob}, true)
	if !errors.Is(err, ErrJobConflict) {
		t.Fatalf("err = %v, want ErrJobConflict", err)
	}

	writeFile(t, wf, "on: push\njobs: {a: {runs-on: x, steps: [{run: y}]}}\n", 0o644)
	if _, err := InsertCIJob(root, CIJob{Workflow: ".github/workflows/a.yml", ID: "check", Job: testJob}, true); err == nil {
		t.Fatal("a flow-style jobs mapping was edited")
	}
}

// TestCopyFilesStaysInsideBothRoots: no source path, symlinked or not, reads
// outside the contract's source root, and no target path writes outside the
// repository.
func TestCopyFilesStaysInsideBothRoots(t *testing.T) {
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "secret"), "secret", 0o644)

	for name, tc := range map[string]struct {
		setup func(src, dst string)
		file  File
	}{
		"dot-dot source":  {func(src, dst string) {}, File{Path: "../secret"}},
		"absolute source": {func(src, dst string) {}, File{Path: filepath.Join(outside, "secret")}},
		"dot-dot target":  {func(src, dst string) { writeFile(t, filepath.Join(src, "a"), "a", 0o644) }, File{Path: "a", Target: "../a"}},
		"symlinked source file": {func(src, dst string) {
			if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(src, "link")); err != nil {
				t.Fatal(err)
			}
		}, File{Path: "link"}},
		"symlinked source directory": {func(src, dst string) {
			if err := os.Symlink(outside, filepath.Join(src, "dir")); err != nil {
				t.Fatal(err)
			}
		}, File{Path: "dir/secret"}},
		"symlinked target file": {func(src, dst string) {
			writeFile(t, filepath.Join(src, "a"), "a", 0o644)
			if err := os.Symlink(filepath.Join(outside, "victim"), filepath.Join(dst, "a")); err != nil {
				t.Fatal(err)
			}
		}, File{Path: "a"}},
	} {
		t.Run(name, func(t *testing.T) {
			src, dst := t.TempDir(), t.TempDir()
			tc.setup(src, dst)
			res, err := CopyFiles(src, dst, []File{tc.file}, true)
			if err == nil {
				t.Fatalf("CopyFiles allowed %+v: %+v", tc.file, res)
			}
			if _, err := os.Stat(filepath.Join(outside, "victim")); !os.IsNotExist(err) {
				t.Fatal("the copy wrote outside the repository")
			}
			entries, _ := os.ReadDir(dst)
			for _, e := range entries {
				if e.Name() == "secret" || e.Name() == "link" {
					t.Fatalf("the copy read outside the source root into %s", e.Name())
				}
			}
		})
	}

	// An in-root symlink to another in-root file is fine.
	src, dst := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "real"), "r", 0o644)
	if err := os.Symlink("real", filepath.Join(src, "alias")); err != nil {
		t.Fatal(err)
	}
	if _, err := CopyFiles(src, dst, []File{{Path: "alias"}}, true); err != nil || readFile(t, filepath.Join(dst, "alias")) != "r" {
		t.Fatalf("an in-root symlink source failed: %v", err)
	}
}

// TestInsertCIJobRefusesSymlinkedWorkflow: a workflow file or directory
// that is a symlink is never written through.
func TestInsertCIJobRefusesSymlinkedWorkflow(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".github"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, ".github", "workflows")); err != nil {
		t.Fatal(err)
	}
	if _, err := InsertCIJob(root, CIJob{Workflow: ".github/workflows/a.yml", ID: "a", Job: testJob, On: "push:\n"}, true); err == nil {
		t.Fatal("a symlinked workflows directory was written through")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatal("the job was written outside the repository")
	}
}

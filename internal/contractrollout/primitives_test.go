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

// changelogYAML is the changelog entry a contract that changes files or CI
// must declare (#2433).
const changelogYAML = "changelog:\n  section: Changed\n  entry: Adopts the demo contract.\n"

func TestParseDefaultsAndValidation(t *testing.T) {
	c, err := Parse(t.TempDir(), []byte("name: demo\nfiles:\n  - path: a.sh\ntargets:\n  - repo: o/r\n"+changelogYAML))
	if err != nil {
		t.Fatal(err)
	}
	if c.Branch != "contract/demo" || c.Base != "main" || c.CommitMessage != "chore: adopt the demo contract" || c.PR.Title != c.CommitMessage {
		t.Errorf("defaults = %+v", c)
	}

	for name, src := range map[string]string{
		"unknown key":               "name: demo\nfile:\n  - path: a\n",
		"escaping path":             "name: demo\nfiles:\n  - path: ../secret\n",
		"absolute path":             "name: demo\nfiles:\n  - path: /etc/passwd\n",
		"path inside .git":          "name: demo\nfiles:\n  - path: .git/config\n",
		"bad label color":           "name: demo\nlabels:\n  - name: x\n    color: red\n",
		"workflow location":         "name: demo\nci_job:\n  workflow: ci.yml\n  id: x\n  job: 'runs-on: x'\n",
		"bad job id":                "name: demo\nci_job:\n  workflow: .github/workflows/a.yml\n  id: 'a b'\n  job: 'runs-on: x'\n",
		"empty job":                 "name: demo\nci_job:\n  workflow: .github/workflows/a.yml\n  id: a\n  job: ''\n",
		"duplicate target":          "name: demo\nfiles:\n  - path: a\ntargets:\n  - repo: o/r\n  - repo: O/R\n",
		"bad repo":                  "name: demo\nfiles:\n  - path: a\ntargets:\n  - repo: r\n",
		"changes nothing":           "name: demo\n",
		"bad name":                  "name: Demo Contract\nfiles:\n  - path: a\n",
		"option branch":             "name: demo\nbranch: --force\nfiles:\n  - path: a\n",
		"option-like repo owner":    "name: demo\nfiles:\n  - path: a\ntargets:\n  - repo: -o/r\n",
		"option-like repo name":     "name: demo\nfiles:\n  - path: a\ntargets:\n  - repo: o/-r\n",
		"option-like base":          "name: demo\nbase: -main\nfiles:\n  - path: a\n",
		"option-like target base":   "name: demo\nfiles:\n  - path: a\ntargets:\n  - repo: o/r\n    base: --upload-pack=x\n",
		"branch segment dash":       "name: demo\nbranch: chore/-x\nfiles:\n  - path: a\n",
		"branch with space":         "name: demo\nbranch: 'a b'\nfiles:\n  - path: a\n",
		"branch lock suffix":        "name: demo\nbranch: a.lock\nfiles:\n  - path: a\n",
		"branch shell chars":        "name: demo\nbranch: 'a;rm'\nfiles:\n  - path: a\n",
		"option-like path":          "name: demo\nfiles:\n  - path: -rf\n",
		"option-like segment":       "name: demo\nfiles:\n  - path: scripts/--exec\n",
		"path with space":           "name: demo\nfiles:\n  - path: 'a b'\n",
		"path with shell chars":     "name: demo\nfiles:\n  - path: 'a$(id)'\n",
		"option-like label":         "name: demo\nlabels:\n  - name: -x\n    color: aabbcc\n",
		"label newline":             "name: demo\nlabels:\n  - name: \"a\\nb\"\n    color: aabbcc\n",
		"label description newline": "name: demo\nlabels:\n  - name: a\n    color: aabbcc\n    description: \"x\\ny\"\n",
		"padded repo":               "name: demo\nfiles:\n  - path: a\ntargets:\n  - repo: ' o/r'\n",
		"replaces the gate":         "name: demo\nfiles:\n  - path: x.sh\n    target: scripts/ci-local.sh\n",
		"manifest gate":             "name: demo\nfiles:\n  - path: a\ntargets:\n  - repo: o/r\n    gate: [sh, -c, id]\n",
		"manifest path":             "name: demo\nfiles:\n  - path: a\ntargets:\n  - repo: o/r\n    path: /etc\n",
		"replaces the gate config":  "name: demo\nfiles:\n  - path: x.yaml\n    target: .nightgauge/config.yaml\n",
		"replaces the changelog":    "name: demo\nfiles:\n  - path: CHANGELOG.md\n",
	} {
		// Every case carries a valid changelog entry, so it fails for its
		// own reason.
		src += changelogYAML
		if _, err := Parse(t.TempDir(), []byte(src)); err == nil {
			t.Errorf("%s: Parse accepted %q", name, src)
		}
	}
}

// TestChangelogEntryValidation: a contract that changes files or CI must
// declare its changelog entry, in a Keep a Changelog section, as prose that
// cannot break the target's changelog structure (#2433).
func TestChangelogEntryValidation(t *testing.T) {
	base := "name: demo\nfiles:\n  - path: a\n"
	for name, src := range map[string]string{
		"missing for files":    base,
		"missing for ci_job":   "name: demo\nci_job:\n  workflow: .github/workflows/a.yml\n  id: a\n  job: 'runs-on: x'\n",
		"unknown section":      base + "changelog:\n  section: Misc\n  entry: x\n",
		"lower-case section":   base + "changelog:\n  section: added\n  entry: x\n",
		"empty entry":          base + "changelog:\n  section: Added\n  entry: '  '\n",
		"heading":              base + "changelog:\n  section: Added\n  entry: \"x\\n#### #1234\"\n",
		"blank line":           base + "changelog:\n  section: Added\n  entry: \"x\\n\\ny\"\n",
		"own bullet":           base + "changelog:\n  section: Added\n  entry: '- x'\n",
		"second item":          base + "changelog:\n  section: Added\n  entry: \"x\\n* y\"\n",
		"ordered item":         base + "changelog:\n  section: Added\n  entry: \"x\\n2. y\"\n",
		"control character":    base + "changelog:\n  section: Added\n  entry: \"x\\ty\"\n",
		"unknown template key": base + "changelog:\n  section: Added\n  entry: '{{.Owner}}'\n",
		"unknown entry key":    base + "changelog:\n  section: Added\n  entry: x\n  file: NEWS.md\n",
	} {
		if _, err := Parse(t.TempDir(), []byte(src)); err == nil {
			t.Errorf("%s: Parse accepted %q", name, src)
		}
	}
	// A labels-only contract commits nothing, so it needs no entry.
	if _, err := Parse(t.TempDir(), []byte("name: demo\nlabels:\n  - name: x\n    color: aabbcc\n")); err != nil {
		t.Errorf("labels-only contract: %v", err)
	}
	c, err := Parse(t.TempDir(), []byte(base+"changelog:\n  section: Fixed\n  entry: |\n    Re-copies a.sh into {{.Repo}}\n    (2. of {{.Contract}}).\n"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.RenderChangelogEntry("o/r")
	if err != nil || strings.Join(got, "\n") != "- Re-copies a.sh into o/r\n  (2. of demo)." {
		t.Fatalf("RenderChangelogEntry = %q, %v", got, err)
	}
}

// TestValidValuesAreAccepted: the allowlists still admit ordinary names.
func TestValidValuesAreAccepted(t *testing.T) {
	src := "name: demo\nbranch: chore/changelog-contract\nbase: release/1.x\nfiles:\n" +
		"  - path: scripts/check-changelog.sh\n  - path: .github/workflows/x.yml\n" +
		"labels:\n  - name: 'type: chore (contract)'\n    color: aabbcc\n" +
		"targets:\n  - repo: Edibu_LLC/my.repo-2\n    base: main\n" + changelogYAML
	if _, err := Parse(t.TempDir(), []byte(src)); err != nil {
		t.Fatal(err)
	}
}

func TestRenderBody(t *testing.T) {
	c, err := Parse(t.TempDir(), []byte("name: demo\nfiles:\n  - path: a\npr:\n  body: 'adopt {{.Contract}} in {{.Repo}}'\n"+changelogYAML))
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

// --- changelog entry, one target repository (#2433) ---

func TestAddChangelogEntry(t *testing.T) {
	entry := []string{"- New thing (#9).", "  More of it."}
	const head = "# Changelog\n\n## [Unreleased]\n\n"
	const rel = "\n## [1.0.0] - 2026-01-01\n\n### Added\n\n- Old.\n"
	for name, tc := range map[string]struct {
		section, before, after string
	}{
		"tight list, first item": {"Added",
			head + "### Added\n\n- A.\n- B.\n" + rel,
			head + "### Added\n\n- New thing (#9).\n  More of it.\n- A.\n- B.\n" + rel},
		"loose list keeps its spacing": {"Added",
			head + "### Added\n\n- A.\n\n- B.\n" + rel,
			head + "### Added\n\n- New thing (#9).\n  More of it.\n\n- A.\n\n- B.\n" + rel},
		"subsection created in order": {"Changed",
			head + "### Added\n\n- A.\n\n### Fixed\n\n- F.\n" + rel,
			head + "### Added\n\n- A.\n\n### Changed\n\n- New thing (#9).\n  More of it.\n\n### Fixed\n\n- F.\n" + rel},
		"subsection appended last": {"Security",
			head + "### Added\n\n- A.\n" + rel,
			head + "### Added\n\n- A.\n\n### Security\n\n- New thing (#9).\n  More of it.\n" + rel},
		"empty unreleased": {"Fixed",
			head + rel[1:],
			head + "### Fixed\n\n- New thing (#9).\n  More of it.\n" + rel},
		"empty unreleased at the end": {"Fixed",
			"# Changelog\n\n## [Unreleased]\n",
			"# Changelog\n\n## [Unreleased]\n\n### Fixed\n\n- New thing (#9).\n  More of it.\n"},
		"empty subsection": {"Added",
			head + "### Added\n\n### Fixed\n\n- F.\n",
			head + "### Added\n\n- New thing (#9).\n  More of it.\n\n### Fixed\n\n- F.\n"},
		"only the unreleased section changes": {"Added",
			head + "### Fixed\n\n- F.\n" + rel,
			head + "### Added\n\n- New thing (#9).\n  More of it.\n\n### Fixed\n\n- F.\n" + rel},
	} {
		root := t.TempDir()
		writeFile(t, filepath.Join(root, "CHANGELOG.md"), tc.before, 0o644)
		plan, err := AddChangelogEntry(root, tc.section, entry, false)
		if err != nil || plan.Action != "added" || readFile(t, filepath.Join(root, "CHANGELOG.md")) != tc.before {
			t.Errorf("%s: planning = %+v, %v, or it wrote", name, plan, err)
		}
		res, err := AddChangelogEntry(root, tc.section, entry, true)
		if err != nil || res.Action != "added" {
			t.Fatalf("%s: %+v, %v", name, res, err)
		}
		if got := readFile(t, filepath.Join(root, "CHANGELOG.md")); got != tc.after {
			t.Errorf("%s:\n got %q\nwant %q", name, got, tc.after)
		}
		// Adding it again finds it present and changes nothing.
		if again, err := AddChangelogEntry(root, tc.section, entry, true); err != nil || again.Action != "present" ||
			readFile(t, filepath.Join(root, "CHANGELOG.md")) != tc.after {
			t.Errorf("%s: second add = %+v, %v", name, again, err)
		}
	}
}

func TestAddChangelogEntryRefusals(t *testing.T) {
	entry := []string{"- x"}
	root := t.TempDir()
	if r, err := AddChangelogEntry(root, "Added", entry, true); err != nil || r.Action != "absent" {
		t.Errorf("no changelog = %+v, %v", r, err)
	}
	for name, content := range map[string]string{
		"no unreleased":  "# Changelog\n\n## [1.0.0] - 2026-01-01\n",
		"two unreleased": "# Changelog\n\n## [Unreleased]\n\n## [Unreleased]\n",
	} {
		writeFile(t, filepath.Join(root, "CHANGELOG.md"), content, 0o644)
		if _, err := AddChangelogEntry(root, "Added", entry, true); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if readFile(t, filepath.Join(root, "CHANGELOG.md")) != content {
			t.Errorf("%s: the changelog was written", name)
		}
	}
	writeFile(t, filepath.Join(root, "CHANGELOG.md"), "# Changelog\n\n## [Unreleased]\n", 0o644)
	if _, err := AddChangelogEntry(root, "Misc", entry, true); err == nil {
		t.Error("an unknown section was accepted")
	}
	outside := filepath.Join(t.TempDir(), "elsewhere.md")
	writeFile(t, outside, "## [Unreleased]\n", 0o644)
	linked := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(linked, "CHANGELOG.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := AddChangelogEntry(linked, "Added", entry, true); err == nil || readFile(t, outside) != "## [Unreleased]\n" {
		t.Errorf("a symlinked changelog was written through: %v", err)
	}
}

// --- the target's declared gate (#2434) ---

func TestResolveGate(t *testing.T) {
	newRoot := func(config string, files ...string) string {
		root := t.TempDir()
		for _, f := range files {
			writeFile(t, filepath.Join(root, f), "#!/bin/sh\n", 0o755)
		}
		if config != "" {
			writeFile(t, filepath.Join(root, ".nightgauge", "config.yaml"), config, 0o644)
		}
		return root
	}
	decl := func(steps string) string { return "owner: o\nlocal_gate:\n  steps:\n" + steps }

	// No declaration: scripts/ci-local.sh when it exists, else nothing.
	if g, err := ResolveGate(newRoot("owner: o\n", "scripts/ci-local.sh")); err != nil || g.Declared || g.String() != "bash scripts/ci-local.sh" {
		t.Errorf("default gate = %+v, %v", g, err)
	}
	if g, err := ResolveGate(newRoot("")); err != nil || !g.Missing() {
		t.Errorf("no gate = %+v, %v", g, err)
	}

	ok := decl("    - [npm, ci]\n    - [npx, prettier, --check, .]\n    - [bash, scripts/test.sh, --extension, none]\n" +
		"    - [go, test, ./...]\n    - [flutter, analyze]\n    - [make, lint]\n")
	g, err := ResolveGate(newRoot(ok, "scripts/ci-local.sh", "scripts/test.sh"))
	if err != nil || !g.Declared || len(g.Steps) != 6 || g.Scripts()[0] != "scripts/test.sh" {
		t.Fatalf("declared gate = %+v, %v", g, err)
	}
	if g.String() != "npm ci && npx prettier --check . && bash scripts/test.sh --extension none && go test ./... && flutter analyze && make lint" {
		t.Errorf("String() = %q", g.String())
	}

	for name, config := range map[string]string{
		"no steps":            "local_gate:\n  steps: []\n",
		"empty step":          decl("    - []\n"),
		"unknown key":         "local_gate:\n  steps:\n    - [npm, ci]\n  shell: bash\n",
		"free-form string":    "local_gate: npm ci && npm test\n",
		"program not allowed": decl("    - [curl, -fsSL, https://example.invalid]\n"),
		"absolute program":    decl("    - [/bin/sh, scripts/test.sh]\n"),
		"bash -c":             decl("    - [bash, -c, id]\n"),
		"sh without a script": decl("    - [sh]\n"),
		"script outside":      decl("    - [bash, ../x.sh]\n"),
		"script missing":      decl("    - [bash, scripts/missing.sh]\n"),
		"node eval":           decl("    - [node, --eval, x]\n"),
		"python -c":           decl("    - [python3, -c, x]\n"),
		"npx -c":              decl("    - [npx, -c, id]\n"),
		"npm exec --call":     decl("    - [npm, exec, --call=id]\n"),
		"pnpm shell mode":     decl("    - [pnpm, exec, --shell-mode, id]\n"),
		"make eval":           decl("    - [make, --eval=x:;id, x]\n"),
		"make outside file":   decl("    - [make, -f, /tmp/Makefile]\n"),
		"newline in argument": decl("    - [npm, \"run\\nid\"]\n"),
		"empty argument":      decl("    - [npm, '']\n"),
		"not yaml":            "local_gate: [\n",
	} {
		if g, err := ResolveGate(newRoot(config, "scripts/ci-local.sh", "scripts/test.sh")); err == nil {
			t.Errorf("%s: accepted %+v", name, g)
		}
	}

	// A script reached through a symlink is not the repository's own file.
	root := newRoot(decl("    - [bash, scripts/test.sh]\n"))
	outside := filepath.Join(t.TempDir(), "x.sh")
	writeFile(t, outside, "#!/bin/sh\n", 0o755)
	if err := os.MkdirAll(filepath.Join(root, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "scripts", "test.sh")); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveGate(root); err == nil {
		t.Error("a symlinked gate script was accepted")
	}
}

func TestCheckGateUntouched(t *testing.T) {
	g := Gate{Steps: [][]string{{"npm", "ci"}, {"bash", "scripts/gate.sh"}}, Declared: true}
	if err := checkGateUntouched(g, []File{{Path: "scripts/other.sh"}}); err != nil {
		t.Error(err)
	}
	for _, f := range []File{{Path: "scripts/gate.sh"}, {Path: "x", Target: ".nightgauge/config.yaml"}} {
		if err := checkGateUntouched(g, []File{f}); err == nil {
			t.Errorf("%s: a contract file replacing the gate was accepted", f.TargetPath())
		}
	}
}

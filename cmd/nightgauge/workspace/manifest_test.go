package workspacecmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// realisticManifest mirrors the shape of this repository's own manifest: head
// comment, a comment block owned by the FIRST repositories entry, blank-line
// separators, a trailing NOTE block, and a routing section. Every formatting
// feature here is one a marshal-based writer would destroy.
const realisticManifest = `# Workspace Configuration
#
# Paths are relative to this file's location.

workspace:
  name: "Test Workspace"
  description: "fixture"

repositories:
  # ` + "`project_number`" + ` (#3232) — explicit repo→project mapping. Without it,
  # defaults caused silent cross-repo misroutes.
  - name: alpha
    path: .
    role: primary
    project_number: 3

  - name: beta
    path: ../beta
    role: primary
    project_number: 4

  - name: gamma
    path: ../gamma
    role: secondary
    project_number: 5

# NOTE: delta is deliberately NOT listed — it carries no project board, and
# project_number has no zero-value guard.

routing:
  default_repository: alpha
  patterns:
    - id: web
      keywords: [angular, web]
      preferred_repo: gamma
`

func writeFixture(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	vscode := filepath.Join(dir, ".vscode")
	if err := os.MkdirAll(vscode, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(vscode, "nightgauge-workspace.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseManifest_StructureAndLayout(t *testing.T) {
	m, err := parseManifest("test.yaml", []byte(realisticManifest))
	if err != nil {
		t.Fatalf("parseManifest: %v", err)
	}
	if len(m.entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(m.entries))
	}
	if m.entries[0].Name != "alpha" || m.entries[0].ProjectNumber != 3 {
		t.Errorf("entry[0] = %+v", m.entries[0])
	}
	if m.entries[2].Role != "secondary" {
		t.Errorf("entry[2].Role = %q, want secondary", m.entries[2].Role)
	}
	if !m.blankSeparated {
		t.Error("blankSeparated = false, want true — fixture separates entries with blank lines")
	}
	if m.dashIndent != 2 || m.contentIndent != 4 {
		t.Errorf("indents = dash %d content %d, want 2/4", m.dashIndent, m.contentIndent)
	}
	// The comment block belongs to the FIRST entry, which is the whole reason
	// removal must not delete an entry's leading comment.
	if m.entries[0].commentStart == 0 {
		t.Error("entry[0].commentStart = 0, want the project_number comment block")
	}
	if m.entries[1].commentStart != 0 {
		t.Errorf("entry[1].commentStart = %d, want 0", m.entries[1].commentStart)
	}
	if m.routingDefault != "alpha" {
		t.Errorf("routingDefault = %q, want alpha", m.routingDefault)
	}
	if got := m.routingPreferred["gamma"]; len(got) != 1 || got[0] != "web" {
		t.Errorf("routingPreferred[gamma] = %v, want [web]", got)
	}
}

// TestAddRemoveRoundTripIsByteIdentical is the acceptance criterion that forced
// a line splicer instead of yaml.Marshal. A marshal-based writer fails this on
// the first write by dropping every blank line.
func TestAddRemoveRoundTripIsByteIdentical(t *testing.T) {
	original := []byte(realisticManifest)

	m, err := parseManifest("test.yaml", original)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.addEntry(manifestEntry{Name: "delta", Path: "../delta", Role: "primary", ProjectNumber: 9}); err != nil {
		t.Fatal(err)
	}
	added := m.render()
	if bytesEqual(added, original) {
		t.Fatal("add produced an identical file — nothing was written")
	}
	if err := validateManifestBytes(added); err != nil {
		t.Fatalf("added document does not validate: %v", err)
	}

	m2, err := parseManifest("test.yaml", added)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m2.removeEntry("delta"); err != nil {
		t.Fatal(err)
	}
	back := m2.render()

	if !bytesEqual(back, original) {
		t.Errorf("add-then-remove is not byte-identical.\n--- want ---\n%s\n--- got ---\n%s", original, back)
	}
}

func TestAddPreservesCommentsAndFormatting(t *testing.T) {
	m, err := parseManifest("test.yaml", []byte(realisticManifest))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.addEntry(manifestEntry{Name: "delta", Path: "../delta", ProjectNumber: 9}); err != nil {
		t.Fatal(err)
	}
	out := string(m.render())

	for _, want := range []string{
		"# Workspace Configuration",
		"# `project_number` (#3232) — explicit repo→project mapping. Without it,",
		"# NOTE: delta is deliberately NOT listed",
		`  default_repository: alpha`,
		`      preferred_repo: gamma`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("add dropped %q", want)
		}
	}
	if !strings.Contains(out, "  - name: delta\n    path: ../delta\n    project_number: 9") {
		t.Errorf("new entry not rendered at the file's indentation:\n%s", out)
	}
	// role is optional and must be omitted rather than written empty.
	if strings.Contains(out, "role: \"\"") || strings.Contains(out, "role: \n") {
		t.Error("empty role was written")
	}
	// The new entry must land inside repositories[], above the NOTE block.
	if strings.Index(out, "- name: delta") > strings.Index(out, "# NOTE: delta is deliberately") {
		t.Error("new entry was appended after the repositories block")
	}
}

func TestAddRejectsDuplicateName(t *testing.T) {
	m, _ := parseManifest("test.yaml", []byte(realisticManifest))
	err := m.addEntry(manifestEntry{Name: "beta", Path: "../beta2", ProjectNumber: 7})
	if err == nil {
		t.Fatal("adding a duplicate name succeeded")
	}
	if !strings.Contains(err.Error(), "already in the manifest") {
		t.Errorf("unhelpful duplicate error: %v", err)
	}
}

// TestRemoveRetainsOwnedCommentBlock pins the comment-ownership decision: the
// four-line block belongs to entry[0] but documents the whole list, so removing
// entry[0] must not delete it.
func TestRemoveRetainsOwnedCommentBlock(t *testing.T) {
	m, _ := parseManifest("test.yaml", []byte(realisticManifest))
	kept, err := m.removeEntry("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if !kept {
		t.Error("removeEntry reported no retained comment, want true")
	}
	out := string(m.render())
	if strings.Contains(out, "- name: alpha") {
		t.Error("entry alpha was not removed")
	}
	if !strings.Contains(out, "# `project_number` (#3232)") {
		t.Errorf("the comment block documenting the list was deleted with the entry:\n%s", out)
	}
	if err := validateManifestBytes(m.render()); err != nil {
		t.Errorf("result does not validate: %v", err)
	}
}

func TestRemoveMiddleAndLastEntry(t *testing.T) {
	for _, name := range []string{"beta", "gamma"} {
		t.Run(name, func(t *testing.T) {
			m, _ := parseManifest("test.yaml", []byte(realisticManifest))
			if _, err := m.removeEntry(name); err != nil {
				t.Fatal(err)
			}
			out := string(m.render())
			if strings.Contains(out, "- name: "+name) {
				t.Errorf("%s still present:\n%s", name, out)
			}
			if !strings.Contains(out, "# NOTE: delta is deliberately NOT listed") {
				t.Error("trailing NOTE block was damaged")
			}
			if !strings.Contains(out, "routing:") {
				t.Error("routing section was damaged")
			}
			if err := validateManifestBytes([]byte(out)); err != nil {
				t.Errorf("result does not validate: %v\n%s", err, out)
			}
			m2, err := parseManifest("t", []byte(out))
			if err != nil {
				t.Fatalf("result does not re-parse: %v", err)
			}
			if len(m2.entries) != 2 {
				t.Errorf("entries after removal = %d, want 2", len(m2.entries))
			}
		})
	}
}

func TestRemoveUnknownName(t *testing.T) {
	m, _ := parseManifest("test.yaml", []byte(realisticManifest))
	if _, err := m.removeEntry("nope"); err == nil {
		t.Fatal("removing an absent entry succeeded")
	}
}

func TestValidateManifestBytes(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name:    "zero project number",
			yaml:    "workspace:\n  name: w\nrepositories:\n  - name: a\n    path: .\n    project_number: 0\n",
			wantErr: "project_number: must be a positive integer",
		},
		{
			name:    "negative project number",
			yaml:    "workspace:\n  name: w\nrepositories:\n  - name: a\n    path: .\n    project_number: -1\n",
			wantErr: "project_number: must be a positive integer",
		},
		{
			name:    "duplicate name",
			yaml:    "workspace:\n  name: w\nrepositories:\n  - name: a\n    path: .\n  - name: a\n    path: ../a\n",
			wantErr: "duplicate repository name",
		},
		{
			name:    "missing path",
			yaml:    "workspace:\n  name: w\nrepositories:\n  - name: a\n",
			wantErr: "path: required field is missing",
		},
		{
			name:    "missing name",
			yaml:    "workspace:\n  name: w\nrepositories:\n  - path: .\n",
			wantErr: "name: required field is missing",
		},
		{
			name:    "bad role",
			yaml:    "workspace:\n  name: w\nrepositories:\n  - name: a\n    path: .\n    role: overlord\n",
			wantErr: "role: must be one of",
		},
		{
			name:    "missing workspace name",
			yaml:    "workspace:\n  description: d\nrepositories:\n  - name: a\n    path: .\n",
			wantErr: "workspace.name",
		},
		{
			name:    "empty repositories without shared project",
			yaml:    "workspace:\n  name: w\nrepositories: []\n",
			wantErr: "cannot be empty",
		},
		{
			name: "empty repositories WITH shared project is allowed",
			yaml: "workspace:\n  name: w\n  shared_project_number: 4\nrepositories: []\n",
		},
		{
			name: "omitted project_number is allowed",
			yaml: "workspace:\n  name: w\nrepositories:\n  - name: a\n    path: .\n",
		},
		{
			name: "valid",
			yaml: realisticManifest,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateManifestBytes([]byte(tc.yaml))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

// TestWriteAtomicRefusesInvalidResult proves the original file survives when
// the would-be result fails validation.
func TestWriteAtomicRefusesInvalidResult(t *testing.T) {
	path := writeFixture(t, realisticManifest)
	m, err := loadManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	// A duplicate name bypassing addEntry's own guard — the writer must still
	// refuse, because validation is the last line of defence for every caller.
	m.lines = append(m.lines, "  - name: alpha", "    path: ../alpha-dup")

	if err := m.writeAtomic(); err == nil {
		t.Fatal("writeAtomic accepted an invalid document")
	}
	on, _ := os.ReadFile(path)
	if string(on) != realisticManifest {
		t.Errorf("original file was modified despite the refusal:\n%s", on)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".nightgauge-workspace-") {
			t.Errorf("temp file %s was left behind", e.Name())
		}
	}
}

// TestWriteAtomicLeavesNoTempOnFailure simulates an unwritable destination:
// the rename fails, and neither a truncated manifest nor a stray temp file may
// remain.
func TestWriteAtomicLeavesNoTempOnFailure(t *testing.T) {
	path := writeFixture(t, realisticManifest)
	m, err := loadManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.addEntry(manifestEntry{Name: "delta", Path: "../delta", ProjectNumber: 9}); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Dir(path)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Skipf("cannot chmod temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	if os.Geteuid() == 0 {
		t.Skip("running as root — a read-only directory does not block writes")
	}

	if err := m.writeAtomic(); err == nil {
		t.Fatal("writeAtomic succeeded against a read-only directory")
	}
	_ = os.Chmod(dir, 0o755)

	on, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("manifest is gone after a failed write: %v", readErr)
	}
	if string(on) != realisticManifest {
		t.Error("manifest was modified by a failed write")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".nightgauge-workspace-") {
			t.Errorf("temp file %s was left behind", e.Name())
		}
	}
}

func TestWriteAtomicPreservesFileMode(t *testing.T) {
	path := writeFixture(t, realisticManifest)
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := loadManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.addEntry(manifestEntry{Name: "delta", Path: "../delta", ProjectNumber: 9}); err != nil {
		t.Fatal(err)
	}
	if err := m.writeAtomic(); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600 — the rename dropped the original permissions", st.Mode().Perm())
	}
}

// TestAddWithoutBlankSeparators covers a compact manifest, where an added entry
// must NOT invent a blank line the file's own style does not use.
func TestAddWithoutBlankSeparators(t *testing.T) {
	compact := "workspace:\n  name: w\nrepositories:\n  - name: a\n    path: .\n    project_number: 1\n  - name: b\n    path: ../b\n    project_number: 2\n"
	m, err := parseManifest("t", []byte(compact))
	if err != nil {
		t.Fatal(err)
	}
	if m.blankSeparated {
		t.Fatal("blankSeparated = true for a compact manifest")
	}
	if err := m.addEntry(manifestEntry{Name: "c", Path: "../c", ProjectNumber: 3}); err != nil {
		t.Fatal(err)
	}
	want := compact + "  - name: c\n    path: ../c\n    project_number: 3\n"
	if got := string(m.render()); got != want {
		t.Errorf("compact add:\n--- want ---\n%s\n--- got ---\n%s", want, got)
	}
}

// TestManifestWithoutTrailingNewline pins that a splice neither adds nor
// removes the file's final newline.
func TestManifestWithoutTrailingNewline(t *testing.T) {
	src := "workspace:\n  name: w\nrepositories:\n  - name: a\n    path: .\n    project_number: 1"
	m, err := parseManifest("t", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if m.trailingNewline {
		t.Fatal("trailingNewline = true for a file that has none")
	}
	if err := m.addEntry(manifestEntry{Name: "b", Path: "../b", ProjectNumber: 2}); err != nil {
		t.Fatal(err)
	}
	if strings.HasSuffix(string(m.render()), "\n") {
		t.Error("splice added a trailing newline the original did not have")
	}
}

func TestQuotedScalarsSurvive(t *testing.T) {
	m, _ := parseManifest("t", []byte(realisticManifest))
	// A name needing quotes must be emitted quoted, or the file stops parsing.
	if err := m.addEntry(manifestEntry{Name: "yes", Path: "./a b", ProjectNumber: 2}); err != nil {
		t.Fatal(err)
	}
	out := m.render()
	if err := validateManifestBytes(out); err != nil {
		t.Fatalf("document with quote-needing scalars does not validate: %v\n%s", err, out)
	}
	m2, err := parseManifest("t", out)
	if err != nil {
		t.Fatalf("re-parse failed: %v", err)
	}
	e, ok := m2.find("yes")
	if !ok {
		t.Fatalf("entry %q not found after round trip; got %+v", "yes", m2.entries)
	}
	if e.Path != "./a b" {
		t.Errorf("path = %q, want %q", e.Path, "./a b")
	}
}

// TestRoleEnumMatchesExtension keeps the Go validator and the extension's
// validateWorkspaceConfig in agreement. The issue calls a divergence a defect,
// and nothing else in CI compares them.
func TestRoleEnumMatchesExtension(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "..",
		"packages", "nightgauge-vscode", "src", "utils", "workspaceDetection.ts"))
	if err != nil {
		t.Skipf("extension source unavailable: %v", err)
	}
	for _, role := range validRoles {
		if !strings.Contains(string(src), `"`+role+`"`) {
			t.Errorf("role %q is accepted by Go but not present in the extension validator", role)
		}
	}
	if !strings.Contains(string(src), `const validRoles = ["primary", "secondary", "shared"]`) {
		t.Error("extension's validRoles literal changed shape — re-check that the two validators still agree")
	}
}

// TestRealRepositoryManifestRoundTrip runs the splicer against THIS repository's
// own manifest rather than a fixture. That file is the concrete artifact the
// acceptance criterion names: it carries a comment block owned by its first
// entry, a NOTE block explaining why a repo is deliberately absent, and blank
// separators — the exact combination a marshal-based writer destroys.
func TestRealRepositoryManifestRoundTrip(t *testing.T) {
	real := filepath.Join("..", "..", "..", ".vscode", "nightgauge-workspace.yaml")
	original, err := os.ReadFile(real)
	if err != nil {
		t.Skipf("repository manifest unavailable: %v", err)
	}

	m, err := parseManifest(real, original)
	if err != nil {
		t.Fatalf("this repository's own manifest does not parse: %v", err)
	}
	if len(m.entries) == 0 {
		t.Fatal("no repositories parsed from the real manifest")
	}
	if err := validateManifestBytes(original); err != nil {
		t.Fatalf("this repository's own manifest fails our validator: %v", err)
	}

	if err := m.addEntry(manifestEntry{
		Name: "nightgauge-roundtrip-probe", Path: "../probe", Role: "secondary", ProjectNumber: 99,
	}); err != nil {
		t.Fatal(err)
	}
	added := m.render()
	if err := validateManifestBytes(added); err != nil {
		t.Fatalf("real manifest + entry does not validate: %v", err)
	}

	m2, err := parseManifest(real, added)
	if err != nil {
		t.Fatalf("real manifest + entry does not re-parse: %v", err)
	}
	if _, err := m2.removeEntry("nightgauge-roundtrip-probe"); err != nil {
		t.Fatal(err)
	}
	if back := m2.render(); !bytesEqual(back, original) {
		t.Errorf("round trip over the real manifest is not byte-identical.\n--- want ---\n%s\n--- got ---\n%s", original, back)
	}
}

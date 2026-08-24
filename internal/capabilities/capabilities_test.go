package capabilities

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeReg writes a registry file into a temp dir and returns its path.
func writeReg(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "capabilities.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const validBody = `
schema_version: 1
capabilities:
  - id: alpha
    title: Alpha
    status: ga
    disposition: core
    surfaces: [cli]
    docs: [docs/A.md]
    owns: [internal/a/**]
    depends_on: []
  - id: beta
    title: Beta
    status: beta
    disposition: both
    surfaces: [cli, sdk]
    docs: [docs/B.md]
    owns: [internal/b/**]
    depends_on: [alpha]
`

func TestLoad_Valid(t *testing.T) {
	reg, err := Load(writeReg(t, validBody))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(reg.Capabilities) != 2 {
		t.Fatalf("want 2 capabilities, got %d", len(reg.Capabilities))
	}
	c, ok := reg.ByID("beta")
	if !ok {
		t.Fatal("beta not found")
	}
	if !c.HasSurface(SurfaceSDK) || c.HasSurface(SurfaceVSCode) {
		t.Errorf("beta surfaces wrong: %v", c.Surfaces)
	}
}

// Each case is a single-field mutation of validBody that must be refused with a
// NAMED error. A closed vocabulary that silently accepts an unknown value is
// the failure mode this table exists to prevent.
func TestLoad_RefusesInvalid(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "unknown status",
			body: strings.Replace(validBody, "status: ga", "status: shipped", 1),
			want: "unknown status",
		},
		{
			name: "unknown disposition",
			body: strings.Replace(validBody, "disposition: core", "disposition: oss", 1),
			want: "unknown disposition",
		},
		{
			name: "unknown surface",
			body: strings.Replace(validBody, "surfaces: [cli]", "surfaces: [tui]", 1),
			want: "unknown surface",
		},
		{
			name: "wrong schema version",
			body: strings.Replace(validBody, "schema_version: 1", "schema_version: 2", 1),
			want: "schema_version 2 is not supported",
		},
		{
			name: "duplicate id",
			body: strings.Replace(validBody, "id: beta", "id: alpha", 1),
			want: "duplicate id",
		},
		{
			name: "unresolved depends_on",
			body: strings.Replace(validBody, "depends_on: [alpha]", "depends_on: [gamma]", 1),
			want: `depends_on "gamma", which is not defined`,
		},
		{
			name: "self dependency",
			body: strings.Replace(validBody, "depends_on: [alpha]", "depends_on: [beta]", 1),
			want: "depends_on itself",
		},
		{
			name: "no surfaces",
			body: strings.Replace(validBody, "surfaces: [cli]", "surfaces: []", 1),
			want: "declares no surfaces",
		},
		{
			name: "no docs",
			body: strings.Replace(validBody, "docs: [docs/A.md]", "docs: []", 1),
			want: "declares no docs",
		},
		{
			name: "no owns and not planned",
			body: strings.Replace(validBody, "owns: [internal/a/**]", "owns: []", 1),
			want: "declares no owns globs",
		},
		{
			name: "misspelled key is not silently ignored",
			body: strings.Replace(validBody, "disposition: core", "dispositon: core", 1),
			want: "parse",
		},
		{
			name: "empty registry",
			body: "schema_version: 1\ncapabilities: []\n",
			want: "declares no capabilities",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeReg(t, tc.body))
			if err == nil {
				t.Fatalf("want error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want error containing %q, got %q", tc.want, err)
			}
		})
	}
}

// A status:planned capability is the ONLY one allowed to own nothing — it names
// work that does not exist yet.
func TestLoad_PlannedMayOwnNothing(t *testing.T) {
	body := `
schema_version: 1
capabilities:
  - id: future
    title: Future
    status: planned
    disposition: core
    surfaces: [cli]
    docs: [docs/F.md]
    owns: []
    depends_on: []
`
	if _, err := Load(writeReg(t, body)); err != nil {
		t.Fatalf("planned capability with no owns should load: %v", err)
	}
}

// The anti-rot mechanism: a glob matching nothing is a VIOLATION, not an empty
// set. This is what makes a deleted or relocated implementation fail the build.
func TestValidateAgainstTree(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "docs"))
	mustMkdir(t, filepath.Join(root, "internal", "a"))
	mustWrite(t, filepath.Join(root, "docs", "A.md"), "# A")
	mustWrite(t, filepath.Join(root, "internal", "a", "a.go"), "package a")
	// docs/B.md and internal/b/ are deliberately absent.

	reg, err := Load(writeReg(t, validBody))
	if err != nil {
		t.Fatal(err)
	}
	v, err := reg.ValidateAgainstTree(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != 2 {
		t.Fatalf("want 2 violations, got %d: %v", len(v), v)
	}

	var gotDoc, gotGlob bool
	for _, x := range v {
		if x.Capability != "beta" {
			t.Errorf("violation on wrong capability: %v", x)
		}
		switch x.Kind {
		case "missing-doc":
			gotDoc = true
			if !strings.Contains(x.String(), "does not exist") {
				t.Errorf("bad message: %s", x)
			}
		case "empty-glob":
			gotGlob = true
			if !strings.Contains(x.String(), "matches nothing") {
				t.Errorf("bad message: %s", x)
			}
		}
	}
	if !gotDoc || !gotGlob {
		t.Errorf("want both a missing-doc and an empty-glob violation, got %v", v)
	}
}

// A clean tree produces zero violations — the positive case, which #723 is a
// standing reminder to actually exercise.
func TestValidateAgainstTree_Clean(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "docs"))
	mustMkdir(t, filepath.Join(root, "internal", "a"))
	mustMkdir(t, filepath.Join(root, "internal", "b"))
	mustWrite(t, filepath.Join(root, "docs", "A.md"), "# A")
	mustWrite(t, filepath.Join(root, "docs", "B.md"), "# B")
	mustWrite(t, filepath.Join(root, "internal", "a", "a.go"), "package a")
	mustWrite(t, filepath.Join(root, "internal", "b", "b.go"), "package b")

	reg, err := Load(writeReg(t, validBody))
	if err != nil {
		t.Fatal(err)
	}
	v, err := reg.ValidateAgainstTree(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != 0 {
		t.Fatalf("clean tree should have no violations, got %v", v)
	}
}

// A wildcard directory segment followed by /** must expand. The first
// implementation os.Stat'd the raw pattern and reported every such glob as
// empty; the real registry caught it immediately.
func TestCountMatches_WildcardDirWithDoubleStar(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "skills", "ng-feature-dev"))
	mustMkdir(t, filepath.Join(root, "skills", "ng-feature-validate"))
	mustWrite(t, filepath.Join(root, "skills", "ng-feature-dev", "SKILL.md"), "x")
	mustWrite(t, filepath.Join(root, "skills", "ng-feature-validate", "SKILL.md"), "x")

	n, err := countMatches(root, "skills/ng-feature-*/**")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("want 2 matched entries across both skill dirs, got %d", n)
	}

	if n, err := countMatches(root, "skills/ng-nonexistent-*/**"); err != nil || n != 0 {
		t.Errorf("want 0 matches for a pattern with no directories, got %d (%v)", n, err)
	}
}

func TestSurfacesWithoutCapability(t *testing.T) {
	reg, err := Load(writeReg(t, validBody))
	if err != nil {
		t.Fatal(err)
	}
	got := reg.SurfacesWithoutCapability()
	for _, s := range got {
		if s == SurfaceCLI || s == SurfaceSDK {
			t.Errorf("%q is claimed by a capability and must not be reported as a hole", s)
		}
	}
	if len(got) != len(AllSurfaces)-2 {
		t.Errorf("want %d unclaimed surfaces, got %d (%v)", len(AllSurfaces)-2, len(got), got)
	}
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, p, body string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

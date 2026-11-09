package main

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// scoreSensitivePins are the modules whose versions moved Microsoft Defender's
// machine-learning verdict on the Linux binary (#3104). With modernc.org/sqlite
// 1.61.x and libc 1.79.0 it flagged 6 of 12 Linux builds; with these versions,
// 0 of 11. A version changes here only after the four-version-string Linux scan
// described in docs/RELEASE_BINARIES.md comes back clean for the new set.
var scoreSensitivePins = map[string]string{
	"modernc.org/sqlite":              "v1.60.1",
	"modernc.org/libc":                "v1.77.1",
	"github.com/dustin/go-humanize":   "v1.0.1",
	"github.com/oapi-codegen/runtime": "v1.7.0",
}

// requireLine matches one `module version` requirement, inside or outside a
// require block, with an optional `// indirect` comment.
var requireLine = regexp.MustCompile(`(?m)^\s*(?:require\s+)?(\S+)\s+(v\S+)(?:\s*//.*)?$`)

func goModRequirements(t *testing.T, path string) map[string]string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("HARNESS ERROR: read %s: %v", path, err)
	}
	reqs := map[string]string{}
	for _, m := range requireLine.FindAllStringSubmatch(string(b), -1) {
		reqs[m[1]] = m[2]
	}
	return reqs
}

func TestScoreSensitiveModulesStayPinned(t *testing.T) {
	reqs := goModRequirements(t, filepath.Join(repoRoot, "go.mod"))
	for mod, want := range scoreSensitivePins {
		got, ok := reqs[mod]
		if !ok {
			t.Errorf("go.mod no longer requires %s; the pin guard would check nothing for it. "+
				"If the module was dropped, remove it from scoreSensitivePins and from %s", mod, record)
			continue
		}
		if got != want {
			t.Errorf("go.mod requires %s %s, but it is pinned at %s: newer versions put the Linux binary "+
				"at Microsoft Defender's ML threshold (#3104). Raise it only after the four-version-string "+
				"Linux scan in %s is clean, then update this pin", mod, got, want, record)
		}
	}
}

func TestGoModRequirementParser(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "go.mod")
	src := "module x\n\ngo 1.26\n\nrequire example.com/single v1.2.3\n\nrequire (\n" +
		"\tmodernc.org/sqlite v1.61.1\n\tgithub.com/dustin/go-humanize v1.1.0 // indirect\n)\n"
	if err := os.WriteFile(p, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	got := goModRequirements(t, p)
	for mod, want := range map[string]string{
		"example.com/single":            "v1.2.3",
		"modernc.org/sqlite":            "v1.61.1",
		"github.com/dustin/go-humanize": "v1.1.0",
	} {
		if got[mod] != want {
			t.Errorf("parser read %s as %q, want %q", mod, got[mod], want)
		}
	}
}

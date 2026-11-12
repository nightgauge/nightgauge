package main

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// unlinkedModulePrefixes are import-path prefixes the release CLI must not
// link (#3192). go-git brought an SSH stack, OpenPGP and a second SHA-1, and
// modernc.org/sqlite an embedded C-to-Go SQLite engine; together they were
// most of what Microsoft's model flagged on the Linux amd64 binary. The
// modernc modules stay in go.mod for a test-only import, which this does not
// see, because `go list -deps` without -test lists the binary's link set.
var unlinkedModulePrefixes = []string{
	"github.com/go-git/",
	"modernc.org/",
	"github.com/ProtonMail/go-crypto",
	"github.com/cloudflare/circl",
}

// TestReleaseCLIDoesNotLinkGoGitOrSQLite lists the linux/amd64 release CLI's
// package dependencies and fails, naming the importer, if any of them comes
// from a module the release must not link.
func TestReleaseCLIDoesNotLinkGoGitOrSQLite(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Fatalf("HARNESS ERROR: go not on PATH; the link-set guard checked nothing: %v", err)
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}\t{{join .Imports \" \"}}", "./cmd/nightgauge")
	cmd.Dir = root
	cmd.Env = append(cmd.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		stderr := ""
		if errors.As(err, &ee) {
			stderr = string(ee.Stderr)
		}
		t.Fatalf("HARNESS ERROR: go list -deps ./cmd/nightgauge: %v\n%s", err, stderr)
	}
	if !strings.Contains(string(out), "github.com/nightgauge/nightgauge/internal/git\t") {
		t.Fatalf("HARNESS ERROR: go list output does not list internal/git; the guard parsed nothing:\n%.500s", out)
	}
	if offenders := linkedOffenders(string(out)); len(offenders) > 0 {
		t.Errorf("the release CLI links modules it must not (see %s):\n%s", record, strings.Join(offenders, "\n"))
	}
}

// linkedOffenders parses `go list -deps -f '{{.ImportPath}}\t{{join .Imports " "}}'`
// output and returns one "importer -> package" line per import of a package
// under unlinkedModulePrefixes.
func linkedOffenders(listing string) []string {
	var offenders []string
	for _, line := range strings.Split(listing, "\n") {
		pkg, imports, _ := strings.Cut(line, "\t")
		for _, imp := range strings.Fields(imports) {
			for _, prefix := range unlinkedModulePrefixes {
				if strings.HasPrefix(imp, prefix) {
					offenders = append(offenders, pkg+" -> "+imp)
				}
			}
		}
	}
	return offenders
}

func TestLinkedOffendersNamesTheImporter(t *testing.T) {
	listing := "example.com/a\tfmt github.com/go-git/go-git/v5\n" +
		"github.com/go-git/go-git/v5\tgithub.com/ProtonMail/go-crypto/openpgp\n" +
		"example.com/b\tmodernc.org/sqlite os\n" +
		"example.com/c\tstrings\n"
	got := strings.Join(linkedOffenders(listing), "|")
	want := "example.com/a -> github.com/go-git/go-git/v5|" +
		"github.com/go-git/go-git/v5 -> github.com/ProtonMail/go-crypto/openpgp|" +
		"example.com/b -> modernc.org/sqlite"
	if got != want {
		t.Errorf("linkedOffenders = %q, want %q", got, want)
	}
}

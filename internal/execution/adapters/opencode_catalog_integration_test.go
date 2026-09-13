//go:build opencode_integration

package adapters

// The catalog snapshot (opencode_catalog_env.go) read again from the installed
// opencode binary, pinned to the version it was read from:
//
//	go test -tags opencode_integration ./internal/execution/adapters/ -run OpenCodeCatalogEnvMatchesTheBinary -count=1

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

// openCodeCatalogVersion is the version openCodeCatalogEnv was read from.
const openCodeCatalogVersion = "1.18.30"

// openCodeCatalogEntryRE matches one provider entry of the catalog bundled in
// the binary, as its source is minified: id:"<key>",env:["<VAR>",...].
var openCodeCatalogEntryRE = regexp.MustCompile(`id:"([a-z0-9._-]+)",env:\[([^\]]*)\]`)

// openCodeQuotedRE matches one quoted variable name in an entry's env list.
var openCodeQuotedRE = regexp.MustCompile(`"([^"]+)"`)

// TestOpenCodeCatalogEnvMatchesTheBinary: the credential policy withholds what
// the bundled catalog binds (ADR-022 § 8), so the snapshot must be the
// binary's catalog exactly. A different binary fails rather than skips, and a
// mismatch prints the entries to paste into opencode_catalog_env.go.
func TestOpenCodeCatalogEnvMatchesTheBinary(t *testing.T) {
	path, err := exec.LookPath("opencode")
	if err != nil {
		t.Skip("opencode is not on PATH")
	}
	cmd := exec.Command(path, "--version")
	cmd.Env = []string{"HOME=" + t.TempDir(), "PATH=/usr/bin:/bin"}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("opencode --version: %v", err)
	}
	if v := strings.TrimSpace(string(out)); v != openCodeCatalogVersion {
		t.Fatalf("opencode %s is installed; the catalog snapshot was read from %s. Re-read it before changing the pin", v, openCodeCatalogVersion)
	}
	// An npm install puts a link to the platform binary on PATH.
	binary, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}

	got := map[string][]string{}
	for _, m := range openCodeCatalogEntryRE.FindAllSubmatch(raw, -1) {
		var vars []string
		for _, v := range openCodeQuotedRE.FindAllSubmatch(m[2], -1) {
			vars = append(vars, string(v[1]))
		}
		key := string(m[1])
		if prev, ok := got[key]; ok && !slices.Equal(prev, vars) {
			t.Fatalf("the binary holds two catalog entries for %q with different variables: %q and %q", key, prev, vars)
		}
		got[key] = vars
	}
	if len(got) == 0 {
		t.Fatalf("no catalog entry found in %s; the pattern no longer matches its catalog", binary)
	}

	var diff []string
	for key, vars := range got {
		if want, ok := openCodeCatalogEnv[key]; !ok || !slices.Equal(want, vars) {
			diff = append(diff, fmt.Sprintf("binary %q = %q, snapshot = %q", key, vars, openCodeCatalogEnv[key]))
		}
	}
	for key := range openCodeCatalogEnv {
		if _, ok := got[key]; !ok {
			diff = append(diff, fmt.Sprintf("snapshot %q is not in the binary's catalog", key))
		}
	}
	if len(diff) == 0 {
		return
	}
	sort.Strings(diff)
	keys := make([]string, 0, len(got))
	for key := range got {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var entries strings.Builder
	for _, key := range keys {
		quoted := make([]string, len(got[key]))
		for i, v := range got[key] {
			quoted[i] = fmt.Sprintf("%q", v)
		}
		fmt.Fprintf(&entries, "\t%q: {%s},\n", key, strings.Join(quoted, ", "))
	}
	t.Errorf("the catalog snapshot differs from the binary's catalog:\n  %s\nopenCodeCatalogEnv entries read from the binary (gofmt after pasting):\n%s",
		strings.Join(diff, "\n  "), entries.String())
}

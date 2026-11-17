package adapters

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestOpenCodeCatalogEnvMatchesThePinnedCatalog holds openCodeCatalogEnv to
// the pinned models catalog it is derived from, so neither moves without the
// other and a hand edit fails without the opencode_integration build tag.
func TestOpenCodeCatalogEnvMatchesThePinnedCatalog(t *testing.T) {
	env, _, err := openCodePinnedCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if maps.EqualFunc(env, openCodeCatalogEnv, slices.Equal[[]string]) {
		return
	}
	var diff, entries strings.Builder
	for _, key := range slices.Sorted(maps.Keys(env)) {
		if got, ok := openCodeCatalogEnv[key]; !ok || !slices.Equal(got, env[key]) {
			fmt.Fprintf(&diff, "  pinned %q = %q, snapshot = %q\n", key, env[key], got)
		}
		fmt.Fprintf(&entries, "\t%q: {%s},\n", key, quoteJoin(env[key]))
	}
	for key := range openCodeCatalogEnv {
		if _, ok := env[key]; !ok {
			fmt.Fprintf(&diff, "  snapshot %q is not in the pinned catalog\n", key)
		}
	}
	t.Errorf("openCodeCatalogEnv differs from the pinned catalog:\n%sentries derived from it (gofmt after pasting):\n%s", diff.String(), entries.String())
}

// TestOpenCodeAnthropicModelsMatchThePinnedCatalog holds
// openCodeAnthropicModels to the pinned catalog: each anthropic model under
// its id, and each experimental mode's entry under its base model's id
// (ADR-022 § 17).
func TestOpenCodeAnthropicModelsMatchThePinnedCatalog(t *testing.T) {
	_, anthropic, err := openCodePinnedCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(anthropic, openCodeAnthropicModels) {
		var entries strings.Builder
		for _, id := range slices.Sorted(maps.Keys(anthropic)) {
			fmt.Fprintf(&entries, "\t%q: %q,\n", id, anthropic[id])
		}
		t.Errorf("openCodeAnthropicModels differs from the pinned catalog; entries derived from it (gofmt after pasting):\n%s", entries.String())
	}
	// The fast-mode entries 2.2.7 lists, each sent under its base model.
	for entry, base := range map[string]string{
		"claude-opus-5-5-fast":  "claude-opus-5-5",
		"claude-fable-5-2-fast": "claude-fable-5-2",
	} {
		if got := anthropic[entry]; got != base {
			t.Errorf("pinned catalog: %s is sent as %q, want %q", entry, got, base)
		}
	}
}

// TestWriteOpenCodeModelsCatalog: the pinned catalog lands at the path
// OPENCODE_MODELS_PATH names, mode 0600, holding exactly the embedded bytes,
// and a link planted at that path is replaced, never written through.
func TestWriteOpenCodeModelsCatalog(t *testing.T) {
	want, err := openCodeModelsCatalogJSON()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("operator file"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := OpenCodeModelsCatalogPath(dir)
	if err := os.Symlink(victim, path); err != nil {
		t.Fatal(err)
	}
	if err := writeOpenCodeModelsCatalog(dir); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
		t.Errorf("%s has mode %s, want a regular file of mode 0600", path, fi.Mode())
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, want) {
		t.Errorf("%s does not hold the pinned catalog (%d bytes, want %d)", path, len(got), len(want))
	}
	if got, _ := os.ReadFile(victim); string(got) != "operator file" {
		t.Errorf("the link target was written through: %q", got)
	}
	// A stage's edit is undone on the next write.
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeOpenCodeModelsCatalog(dir); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, want) {
		t.Error("a changed catalog was not replaced with the pinned one")
	}
}

// TestOpenCodeRunRootPinsTheModelsCatalog: the run root holds the pinned
// catalog at the path the isolation env exports as OPENCODE_MODELS_PATH,
// beside OPENCODE_DISABLE_MODELS_FETCH=1, and an inherited
// OPENCODE_MODELS_PATH is withheld so only the adapter's value reaches a run.
func TestOpenCodeRunRootPinsTheModelsCatalog(t *testing.T) {
	state, home := t.TempDir(), t.TempDir()
	root, _, err := EnsureOpenCodeRunRoot(state, home, testRunID, envLookup(nil))
	if err != nil {
		t.Fatal(err)
	}
	env, err := OpenCodeIsolationEnv(OpenCodeIsolation{Root: root, Home: home, Lookup: envLookup(nil), GOOS: "linux", MachineConfigDir: "/m"})
	if err != nil {
		t.Fatal(err)
	}
	if env["OPENCODE_DISABLE_MODELS_FETCH"] != "1" {
		t.Errorf("OPENCODE_DISABLE_MODELS_FETCH = %q, want \"1\"", env["OPENCODE_DISABLE_MODELS_FETCH"])
	}
	want, _ := openCodeModelsCatalogJSON()
	if got, err := os.ReadFile(env["OPENCODE_MODELS_PATH"]); err != nil || !bytes.Equal(got, want) {
		t.Errorf("OPENCODE_MODELS_PATH %q does not hold the pinned catalog: %v", env["OPENCODE_MODELS_PATH"], err)
	}
	if !OpenCodeWithholdsEnv("anthropic/claude-opus-5-5", "OPENCODE_MODELS_PATH") {
		t.Error("an inherited OPENCODE_MODELS_PATH reaches the run")
	}
	for _, pattern := range openCodeCatalogCacheFiles {
		if ok, _ := filepath.Match(pattern, openCodeModelsCatalogFile); ok {
			t.Errorf("the cache sweep's pattern %q matches the pinned catalog's name %q", pattern, openCodeModelsCatalogFile)
		}
	}
	// The next stage's dispatch restores the pinned bytes, whatever this one
	// left at the path.
	if err := os.Remove(env["OPENCODE_MODELS_PATH"]); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, "elsewhere.json"), env["OPENCODE_MODELS_PATH"]); err != nil {
		t.Fatal(err)
	}
	if _, _, err := EnsureOpenCodeRunRoot(state, home, testRunID, envLookup(nil)); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(env["OPENCODE_MODELS_PATH"]); err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("a second dispatch left the planted link in place: %v", err)
	}
	if got, _ := os.ReadFile(env["OPENCODE_MODELS_PATH"]); !bytes.Equal(got, want) {
		t.Error("a second dispatch did not restore the pinned catalog")
	}
}

// TestOpenCodeParseCatalog covers the derivation on a fixture: a code binding
// is appended once, a model of a type 2.2.7 does not list is dropped, each
// experimental mode adds an entry sent under its base id, and a provider whose
// id differs from its key or a code binding for a provider the catalog lacks
// is an error.
func TestOpenCodeParseCatalog(t *testing.T) {
	const fixture = `{
		"anthropic": {"id": "anthropic", "env": ["ANTHROPIC_API_KEY"], "models": {
			"m1": {"id": "m1", "type": "chat", "experimental": {"modes": {"fast": {}, "slow": {}}}},
			"m2": {"id": "m2", "type": "embedding"},
			"m3": {"id": "m3", "type": "image"}
		}},
		"cf": {"id": "cf", "env": ["CF_KEY", "CF_TOKEN"], "models": {}}
	}`
	env, anthropic, err := openCodeParseCatalog([]byte(fixture), map[string][]string{"cf": {"CF_TOKEN", "CF_EXTRA"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string][]string{"anthropic": {"ANTHROPIC_API_KEY"}, "cf": {"CF_KEY", "CF_TOKEN", "CF_EXTRA"}}; !maps.EqualFunc(env, want, slices.Equal[[]string]) {
		t.Errorf("env = %q, want %q", env, want)
	}
	if want := map[string]string{"m1": "m1", "m1-fast": "m1", "m1-slow": "m1", "m3": "m3"}; !maps.Equal(anthropic, want) {
		t.Errorf("anthropic = %q, want %q", anthropic, want)
	}
	for name, tc := range map[string]struct {
		catalog   string
		codeBound map[string][]string
		want      string
	}{
		"id differs from key":   {`{"a": {"id": "b", "env": [], "models": {}}}`, nil, `provider "a" has id "b"`},
		"code binding unlisted": {`{"a": {"id": "a", "env": [], "models": {}}}`, map[string][]string{"z": {"Z"}}, `names "z"`},
		"not JSON":              {`[`, nil, "opencode models catalog"},
	} {
		if _, _, err := openCodeParseCatalog([]byte(tc.catalog), tc.codeBound); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want one naming %q", name, err, tc.want)
		}
	}
}

// TestWriteOpenCodeModelsCatalogFailsCleanly: a directory the catalog cannot
// be written to is an error, and leaves no temporary file behind.
func TestWriteOpenCodeModelsCatalogFailsCleanly(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if err := writeOpenCodeModelsCatalog(missing); err == nil {
		t.Error("writing into a missing directory succeeded")
	}
	dir := t.TempDir()
	// A directory at the catalog's name makes the rename fail.
	if err := os.Mkdir(OpenCodeModelsCatalogPath(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(OpenCodeModelsCatalogPath(dir), "x"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeOpenCodeModelsCatalog(dir); err == nil {
		t.Error("replacing a non-empty directory succeeded")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".models-catalog-*")); len(left) > 0 {
		t.Errorf("a failed write left %q", left)
	}
}

// TestOpenCodeProbePinsTheModelsCatalog: every probe but `--version` hands
// OpenCode the pinned catalog in its own directory, with the fetch off. The
// probe runs a shell script that prints the two variables and the file's size.
func TestOpenCodeProbePinsTheModelsCatalog(t *testing.T) {
	probe, err := NewOpenCodeProbe()
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	bin := filepath.Join(t.TempDir(), "opencode")
	script := "#!/bin/sh\nprintf '%s|%s|' \"$OPENCODE_MODELS_PATH\" \"$OPENCODE_DISABLE_MODELS_FETCH\"\nwc -c < \"$OPENCODE_MODELS_PATH\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	res, err := probe.Run(context.Background(), bin, []string{"models"}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := openCodeModelsCatalogJSON()
	if _, err := os.Stat(OpenCodeModelsCatalogPath(probe.Root())); err != nil {
		t.Fatalf("the probe did not write the pinned catalog: %v", err)
	}
	fields := strings.Split(strings.TrimSpace(string(res.Stdout)), "|")
	if len(fields) != 3 {
		t.Fatalf("probe printed %q", res.Stdout)
	}
	if fields[0] != OpenCodeModelsCatalogPath(probe.Root()) || fields[1] != "1" || strings.TrimSpace(fields[2]) != fmt.Sprint(len(want)) {
		t.Errorf("probe env: OPENCODE_MODELS_PATH=%q OPENCODE_DISABLE_MODELS_FETCH=%q size=%s; want %q, \"1\", %d",
			fields[0], fields[1], fields[2], OpenCodeModelsCatalogPath(probe.Root()), len(want))
	}

	// `--version` reads no catalog: a fresh probe writes none and sets no path.
	bare, err := NewOpenCodeProbe()
	if err != nil {
		t.Fatal(err)
	}
	defer bare.Close()
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf '%s' \"${OPENCODE_MODELS_PATH-unset}\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	res, err = bare.Run(context.Background(), bin, []string{"--version"}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Stdout) != "unset" {
		t.Errorf("a --version probe set OPENCODE_MODELS_PATH=%q", res.Stdout)
	}
	if _, err := os.Stat(OpenCodeModelsCatalogPath(bare.Root())); err == nil {
		t.Error("a --version probe wrote the pinned catalog")
	}
}

func quoteJoin(vs []string) string {
	q := make([]string, len(vs))
	for i, v := range vs {
		q[i] = fmt.Sprintf("%q", v)
	}
	return strings.Join(q, ", ")
}

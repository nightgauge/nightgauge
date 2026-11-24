//go:build opencode_integration

package adapters

// The catalog snapshots (opencode_catalog_env.go, opencode_catalog_anthropic.go)
// and the pinned models catalog they are derived from
// (opencode-models-catalog.json.gz), checked against the installed opencode
// binary of the version they were captured from:
//
//	go test -tags opencode_integration ./internal/execution/adapters/ -run 'OpenCodeCatalogEnvMatchesTheBinary|OpenCodeReadsThePinnedCatalog|OpenCodeAnthropicModelsMatchTheBinary' -count=1

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
)

// openCodeCatalogBinary resolves the installed opencode binary. On CI a
// missing binary fails the test rather than skipping it: CI installs the
// pinned version to run it.
func openCodeCatalogBinary(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("opencode")
	if err != nil {
		if os.Getenv("CI") == "true" {
			t.Fatalf("opencode is not on PATH, and CI must run this test: scripts/opencode-integration.sh installs it")
		}
		t.Skip("opencode is not on PATH")
	}
	return path
}

// openCodeCatalogBinaryVersion fails unless the installed binary is the
// version the pinned catalog was captured from.
func openCodeCatalogBinaryVersion(t *testing.T, path string) {
	t.Helper()
	cmd := exec.Command(path, "--version")
	cmd.Env = []string{"HOME=" + t.TempDir(), "PATH=/usr/bin:/bin"}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("opencode --version: %v", err)
	}
	if v, _ := ParseOpenCodeVersion(string(out)); v != openCodeCatalogVersion {
		t.Fatalf("opencode %s is installed; the pinned catalog was captured from %s. Re-capture it before changing the pin (testdata/opencode-cli/README.md)", v, openCodeCatalogVersion)
	}
}

// openCodeModels runs the installed opencode's `models --standalone`, the
// listing 2.2.7 accepts (no provider argument, no --verbose), in throwaway
// HOME and XDG directories with the model fetch off, as in a run. With pinned,
// OPENCODE_MODELS_PATH names a written copy of the pinned catalog, as in a
// run; without it, the binary reads the catalog it bundles. content, when set,
// is OPENCODE_CONFIG_CONTENT, and vars are set to a placeholder. It returns
// the "<provider>/<model>" lines it prints.
func openCodeModels(t *testing.T, path string, pinned bool, content string, vars ...string) []string {
	t.Helper()
	if !pinned {
		return openCodeModelsFrom(t, path, nil, content, vars...)
	}
	data, err := openCodeModelsCatalogJSON()
	if err != nil {
		t.Fatal(err)
	}
	return openCodeModelsFrom(t, path, data, content, vars...)
}

// openCodeModelsFrom is openCodeModels with catalog, when not nil, written as
// the file OPENCODE_MODELS_PATH names.
func openCodeModelsFrom(t *testing.T, path string, catalog []byte, content string, vars ...string) []string {
	t.Helper()
	dir := t.TempDir()
	env := []string{"HOME=" + filepath.Join(dir, "home"), "PATH=/usr/bin:/bin", "TMPDIR=" + filepath.Join(dir, "tmp")}
	for _, x := range openCodeXDGDirs {
		env = append(env, x.env+"="+filepath.Join(dir, x.dir))
	}
	for _, sub := range []string{"home", "tmp"} {
		if err := os.Mkdir(filepath.Join(dir, sub), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, flag := range openCodeDisableFlags {
		env = append(env, flag+"=1")
	}
	env = append(env, openCodeDisableProjectConfig+"=1")
	if catalog != nil {
		if err := os.WriteFile(OpenCodeModelsCatalogPath(dir), catalog, 0o600); err != nil {
			t.Fatal(err)
		}
		env = append(env, openCodeModelsPathEnv+"="+OpenCodeModelsCatalogPath(dir))
	}
	if content != "" {
		env = append(env, openCodeConfigContentEnvVar+"="+content)
	}
	for _, v := range vars {
		env = append(env, v+"="+openCodeProbePlaceholder)
	}
	cmd := exec.Command(path, "models", openCodeStandaloneFlag, "--print-logs")
	cmd.Dir = dir
	cmd.Env = env
	var stderr strings.Builder
	cmd.Stderr = &stderr
	raw, err := cmd.Output()
	if err != nil {
		t.Fatalf("opencode models --standalone: %v\n%s", err, stderr.String())
	}
	if strings.Contains(stderr.String(), "failed to parse") {
		t.Fatalf("opencode skipped part of the catalog:\n%s", stderr.String())
	}
	var lines []string
	for _, l := range strings.Split(string(raw), "\n") {
		if l = strings.TrimSpace(l); strings.Contains(l, "/") {
			lines = append(lines, l)
		}
	}
	return lines
}

// openCodeProbeConcurrency bounds how many opencode processes the probes run
// at once, independent of -parallel and GOMAXPROCS: each listing is ~10 s of
// wall time under load, so the serial probes cost most of the step's timeout
// (#3563).
const openCodeProbeConcurrency = 4

// openCodeProbeEach runs probe once per name, each in its own parallel subtest
// of a "probes" group, at most openCodeProbeConcurrency at a time. It returns
// once every probe has finished, so the caller can aggregate their results.
// Subtests, not goroutines: openCodeModelsFrom calls t.Fatalf and t.TempDir,
// which belong to the goroutine of their own t.
func openCodeProbeEach(t *testing.T, names []string, probe func(t *testing.T, name string)) {
	t.Helper()
	sem := make(chan struct{}, openCodeProbeConcurrency)
	t.Run("probes", func(t *testing.T) {
		for _, name := range names {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				sem <- struct{}{}
				defer func() { <-sem }()
				probe(t, name)
			})
		}
	})
}

// openCodeListedProviders is the set of provider keys a listing names.
func openCodeListedProviders(lines []string) map[string]bool {
	keys := map[string]bool{}
	for _, l := range lines {
		key, _, _ := strings.Cut(l, "/")
		keys[key] = true
	}
	return keys
}

// TestOpenCodeCatalogEnvMatchesTheBinary: the credential policy withholds what
// the catalog a run reads binds (ADR-022 § 8), so openCodeCatalogEnv must be
// what the installed binary does with that catalog. Three checks:
//
//   - the pinned catalog is the one the binary bundles: with every variable
//     set, it lists the same models from the pinned file as from its own;
//   - the binary reads the pinned file: a model removed from a copy of it is
//     no longer listed (the known-bad case);
//   - set alone, each variable loads only providers openCodeCatalogEnv binds
//     it to, so no variable makes a provider reachable that the policy does
//     not know (a binding 2.2.7 makes in code included, openCodeCodeBoundEnv).
func TestOpenCodeCatalogEnvMatchesTheBinary(t *testing.T) {
	path := openCodeCatalogBinary(t)
	openCodeCatalogBinaryVersion(t, path)
	all := slices.Sorted(maps.Keys(openCodeCatalogEnvNames))

	bundled := openCodeModels(t, path, false, "", all...)
	pinned := openCodeModels(t, path, true, "", all...)
	if !slices.Equal(bundled, pinned) {
		t.Fatalf("the pinned catalog is not the one opencode %s bundles: it lists %d models from its own and %d from the pinned file; re-capture it (testdata/opencode-cli/README.md)",
			openCodeCatalogVersion, len(bundled), len(pinned))
	}
	for key := range openCodeListedProviders(pinned) {
		if _, ok := openCodeCatalogEnv[key]; !ok {
			t.Errorf("opencode lists provider %q, which openCodeCatalogEnv does not hold", key)
		}
	}

	// Without a variable set, only providers that need none are listed.
	baseline := openCodeListedProviders(openCodeModels(t, path, true, ""))

	var (
		mu   sync.Mutex
		diff []string
	)
	openCodeProbeEach(t, all, func(t *testing.T, v string) {
		for key := range openCodeListedProviders(openCodeModels(t, path, true, "", v)) {
			if !baseline[key] && !slices.Contains(openCodeCatalogEnv[key], v) {
				mu.Lock()
				diff = append(diff, fmt.Sprintf("%s alone loads %q, and openCodeCatalogEnv[%q] = %q", v, key, key, openCodeCatalogEnv[key]))
				mu.Unlock()
			}
		}
	})
	if len(diff) > 0 {
		sort.Strings(diff)
		t.Errorf("opencode %s binds variables the policy does not; add each to openCodeCodeBoundEnv and re-derive openCodeCatalogEnv:\n  %s",
			openCodeCatalogVersion, strings.Join(diff, "\n  "))
	}
}

// TestOpenCodeReadsThePinnedCatalog is the known-bad case for the check
// above: a model removed from the pinned file disappears from the listing, so
// a run reads OPENCODE_MODELS_PATH rather than the bundled catalog.
func TestOpenCodeReadsThePinnedCatalog(t *testing.T) {
	path := openCodeCatalogBinary(t)
	openCodeCatalogBinaryVersion(t, path)
	data, err := openCodeModelsCatalogJSON()
	if err != nil {
		t.Fatal(err)
	}
	var catalog map[string]map[string]json.RawMessage
	if err := json.Unmarshal(data, &catalog); err != nil {
		t.Fatal(err)
	}
	var models map[string]json.RawMessage
	if err := json.Unmarshal(catalog[openCodeAnthropicKey]["models"], &models); err != nil {
		t.Fatal(err)
	}
	const dropped = "claude-sonnet-5-5"
	delete(models, dropped)
	if catalog[openCodeAnthropicKey]["models"], err = json.Marshal(models); err != nil {
		t.Fatal(err)
	}
	changed, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	listed := openCodeModelsFrom(t, path, changed, "", "ANTHROPIC_API_KEY")
	if slices.Contains(listed, openCodeAnthropicKey+"/"+dropped) {
		t.Errorf("%s/%s is listed although the file OPENCODE_MODELS_PATH names drops it: opencode did not read the file", openCodeAnthropicKey, dropped)
	}
	if !slices.Contains(listed, openCodeAnthropicKey+"/claude-opus-5-5") {
		t.Errorf("the changed catalog lists no other anthropic model: %q", listed)
	}
}

// TestOpenCodeAnthropicModelsMatchTheBinary: the per-run config pins the id
// OpenCode sends for an anthropic model, and refuses one it cannot pin
// (ADR-022 § 17), from openCodeAnthropicModels, so its keys must be exactly
// the anthropic models the binary lists from the pinned catalog. Each model
// the pin accepts must also stay listed under the per-run config the builder
// makes for it.
//
// 2.2.7's `models` prints no served id and no limits (it has no --verbose), so
// the ids each entry is sent under, a fast-mode entry's base id included, are
// read from the pinned catalog the binary was shown to read
// (openCodeParseCatalog, TestOpenCodeReadsThePinnedCatalog), not from a
// listing.
func TestOpenCodeAnthropicModelsMatchTheBinary(t *testing.T) {
	path := openCodeCatalogBinary(t)
	openCodeCatalogBinaryVersion(t, path)

	var got []string
	for _, l := range openCodeModels(t, path, true, "", "ANTHROPIC_API_KEY") {
		if id, ok := strings.CutPrefix(l, openCodeAnthropicKey+"/"); ok {
			got = append(got, id)
		}
	}
	slices.Sort(got)
	if want := slices.Sorted(maps.Keys(openCodeAnthropicModels)); !slices.Equal(got, want) {
		t.Errorf("opencode lists the anthropic models %q from the pinned catalog; openCodeAnthropicModels holds %q", got, want)
	}

	openCodeProbeEach(t, slices.Sorted(maps.Keys(openCodeAnthropicModels)), func(t *testing.T, id string) {
		if openCodeAnthropicModels[id] != id {
			return // refused: see TestOpenCodeConfigRefusesAnAnthropicModelItCannotPin
		}
		model := openCodeAnthropicKey + "/" + id
		built, err := BuildOpenCodeConfig(OpenCodeConfigInput{
			Run:     RunOptions{Model: model},
			RunRoot: t.TempDir(),
			Lookup:  envLookup(map[string]string{"ANTHROPIC_API_KEY": "fake-anthropic-credential-1625"}),
		})
		if err != nil {
			t.Errorf("%s: %v", model, err)
			return
		}
		if !slices.Contains(openCodeModels(t, path, true, built.Content, "ANTHROPIC_API_KEY"), model) {
			t.Errorf("%s is not listed under its per-run config", model)
		}
	})
}

//go:build opencode_integration

package adapters

// The catalog snapshots (opencode_catalog_env.go, opencode_catalog_anthropic.go)
// read again from the installed opencode binary, pinned to the version they
// were read from:
//
//	go test -tags opencode_integration ./internal/execution/adapters/ -run 'OpenCodeCatalogEnvMatchesTheBinary|OpenCodeAnthropicModelsMatchTheBinary' -count=1

import (
	"encoding/json"
	"fmt"
	"maps"
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

// openCodeCatalogBinary resolves the installed opencode binary. On CI a
// missing binary fails the test rather than skipping it: CI installs the
// pinned version to run it.
func openCodeCatalogBinary(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("opencode")
	if err != nil {
		if os.Getenv("CI") == "true" {
			t.Fatalf("opencode is not on PATH, and CI must run this test: install opencode-ai@%s", openCodeCatalogVersion)
		}
		t.Skip("opencode is not on PATH")
	}
	return path
}

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
	path := openCodeCatalogBinary(t)
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

// openCodeModelsVerbose runs the installed opencode's `models <provider>
// --verbose` with the model fetch off, as in a run, under a throwaway HOME,
// with content as OPENCODE_CONFIG_CONTENT when it is not empty, and returns
// each model it prints, keyed by its "<provider>/<model>" line.
func openCodeModelsVerbose(t *testing.T, provider, content string) map[string]map[string]any {
	t.Helper()
	path := openCodeCatalogBinary(t)
	cmd := exec.Command(path, "models", provider, "--verbose")
	cmd.Dir = t.TempDir()
	cmd.Env = []string{"HOME=" + t.TempDir(), "PATH=/usr/bin:/bin", "OPENCODE_DISABLE_MODELS_FETCH=1",
		"OPENCODE_DISABLE_AUTOUPDATE=1", "ANTHROPIC_API_KEY=fake-anthropic-credential-1625"}
	if content != "" {
		cmd.Env = append(cmd.Env, "OPENCODE_CONFIG_CONTENT="+content)
	}
	raw, err := cmd.Output()
	if err != nil {
		t.Fatalf("opencode models %s --verbose: %v\n%s", provider, err, raw)
	}
	models := map[string]map[string]any{}
	lines := strings.Split(string(raw), "\n")
	for i := 0; i+1 < len(lines); i++ {
		if lines[i+1] != "{" {
			continue
		}
		name := strings.TrimSpace(lines[i])
		end := i + 1
		for end < len(lines) && lines[end] != "}" {
			end++
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(strings.Join(lines[i+1:end+1], "\n")), &m); err != nil {
			t.Fatalf("`opencode models %s --verbose` printed %s in a form this parser does not know: %v", provider, name, err)
		}
		models[name] = m
		i = end
	}
	if len(models) == 0 {
		t.Fatalf("`opencode models %s --verbose` printed no model:\n%s", provider, raw)
	}
	return models
}

// TestOpenCodeAnthropicModelsMatchTheBinary: the per-run config pins the id
// OpenCode sends for an anthropic model, and refuses one it cannot pin
// (ADR-022 § 17), from openCodeAnthropicModels, so the snapshot must be the
// binary's anthropic models and the ids it sends for them exactly. A mismatch
// prints the entries to paste into opencode_catalog_anthropic.go. The pin must
// also change nothing about a model it accepts: each one resolves, under the
// per-run config the builder makes for it, exactly as it does with none.
func TestOpenCodeAnthropicModelsMatchTheBinary(t *testing.T) {
	path := openCodeCatalogBinary(t)
	cmd := exec.Command(path, "--version")
	cmd.Env = []string{"HOME=" + t.TempDir(), "PATH=/usr/bin:/bin"}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("opencode --version: %v", err)
	}
	if v := strings.TrimSpace(string(out)); v != openCodeCatalogVersion {
		t.Fatalf("opencode %s is installed; the anthropic snapshot was read from %s. Re-read it before changing the pin", v, openCodeCatalogVersion)
	}

	catalog := openCodeModelsVerbose(t, openCodeAnthropicKey, "")
	got := map[string]string{}
	for name, m := range catalog {
		id, ok := strings.CutPrefix(name, openCodeAnthropicKey+"/")
		api, _ := m["api"].(map[string]any)
		served, _ := api["id"].(string)
		if !ok || served == "" {
			t.Fatalf("`opencode models anthropic --verbose` printed %s with api %v", name, m["api"])
		}
		got[id] = served
	}
	if !maps.Equal(got, openCodeAnthropicModels) {
		var entries strings.Builder
		for _, id := range slices.Sorted(maps.Keys(got)) {
			fmt.Fprintf(&entries, "\t%q: %q,\n", id, got[id])
		}
		t.Errorf("the anthropic snapshot differs from the binary's catalog\nopenCodeAnthropicModels entries read from the binary (gofmt after pasting):\n%s", entries.String())
	}

	for _, id := range slices.Sorted(maps.Keys(openCodeAnthropicModels)) {
		if openCodeAnthropicModels[id] != id {
			continue // refused: see TestOpenCodeConfigRefusesAnAnthropicModelItCannotPin
		}
		model := openCodeAnthropicKey + "/" + id
		built, err := BuildOpenCodeConfig(OpenCodeConfigInput{
			Run:     RunOptions{Model: model},
			RunRoot: t.TempDir(),
			Lookup:  envLookup(map[string]string{"ANTHROPIC_API_KEY": "fake-anthropic-credential-1625"}),
		})
		if err != nil {
			t.Errorf("%s: %v", model, err)
			continue
		}
		pinned := openCodeModelsVerbose(t, openCodeAnthropicKey, built.Content)[model]
		want, _ := json.Marshal(catalog[model])
		have, _ := json.Marshal(pinned)
		if string(want) != string(have) {
			t.Errorf("%s resolves differently under its per-run config:\n  catalog: %s\n  pinned:  %s", model, want, have)
		}
	}
}

package adaptercompat

import (
	"bufio"
	"encoding/json"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

// repoRoot is the repository root, relative to this package's directory.
const repoRoot = "../.."

// cliAdapters is every CLI adapter: the ones that spawn a binary of their own.
var cliAdapters = []string{"claude-headless", "codex", "copilot", "gemini", "grok", "opencode"}

func TestLoad_EmbeddedSetIsTheSixCLIAdapters(t *testing.T) {
	ms, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var got []string
	for _, m := range ms {
		got = append(got, m.Adapter)
	}
	if !reflect.DeepEqual(got, cliAdapters) {
		t.Fatalf("Load returned %v, want exactly %v", got, cliAdapters)
	}
}

// TestLoadDir_EmbeddedSetHoldsAgainstTheTree runs the tree-aware loader over
// the repository. A shipped binary has no tree, so this is what holds the
// embedded fixtures to "exists in the tree".
func TestLoadDir_EmbeddedSetHoldsAgainstTheTree(t *testing.T) {
	fromTree, err := LoadDir(os.DirFS(repoRoot))
	if err != nil {
		t.Fatalf("LoadDir over the repository: %v", err)
	}
	embeddedSet, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(fromTree, embeddedSet) {
		t.Fatal("the manifests read from the tree differ from the embedded set")
	}
}

func TestGet_OpenCodeIsPinnedAndFailsClosed(t *testing.T) {
	m, ok := Get("opencode")
	if !ok {
		t.Fatal(`Get("opencode") found no manifest`)
	}
	if m.MinVersion != "1.18.30" || m.MaxTested != "1.18.30" {
		t.Errorf("opencode min_version=%q max_tested=%q, want both 1.18.30", m.MinVersion, m.MaxTested)
	}
	if m.FloorPolicy != FloorFailClosed {
		t.Errorf("opencode floor_policy=%q, want %q", m.FloorPolicy, FloorFailClosed)
	}
	if _, ok := Get("claude-sdk"); ok {
		t.Error(`Get("claude-sdk") found a manifest; SDK adapters spawn no binary and have none`)
	}
}

// TestManifests_ExistingFloorsAndStartingValues pins the starting values to
// the evidence they were taken from (see each manifest's _reason fields).
func TestManifests_ExistingFloorsAndStartingValues(t *testing.T) {
	want := map[string]struct{ min, max, policy string }{
		"claude-headless": {"2.1.223", "2.1.258", FloorWarn},
		"codex":           {"0.111.0", "0.145.0", FloorWarn},
		"gemini":          {"0.29.0", "", FloorWarn},
		"copilot":         {"", "", FloorWarn},
		"grok":            {"1.0.0", "1.0.4", FloorWarn},
		"opencode":        {"1.18.30", "1.18.30", FloorFailClosed},
	}
	for adapter, w := range want {
		m, ok := Get(adapter)
		if !ok {
			t.Errorf("no manifest for %s", adapter)
			continue
		}
		if m.MinVersion != w.min || m.MaxTested != w.max || m.FloorPolicy != w.policy {
			t.Errorf("%s: min=%q max=%q policy=%q, want %q %q %q",
				adapter, m.MinVersion, m.MaxTested, m.FloorPolicy, w.min, w.max, w.policy)
		}
	}
}

// validManifest is a manifest LoadDir accepts, as a JSON object the refusal
// cases below each break in exactly one place.
func validManifest() map[string]any {
	return map[string]any{
		"adapter":              "codex",
		"binary":               "codex",
		"min_version":          "0.111.0",
		"floor_policy":         "warn",
		"max_tested":           "0.145.0",
		"feeds":                []any{map[string]any{"kind": "npm", "ref": "@openai/codex"}},
		"companion_feeds":      []any{},
		"install":              map[string]any{"npm": "@openai/codex"},
		"managed_install":      "",
		"required_flags":       []any{},
		"catalog":              map[string]any{"skip_reason": "no listing command"},
		"fixtures":             []any{"internal/execution/testdata/present.jsonl"},
		"config_schema_sha256": "",
		"plugin_api_version":   "",
		"experimental_hooks":   []any{},
	}
}

// treeWith is a repository-rooted filesystem holding one manifest and the
// fixture validManifest names.
func treeWith(t *testing.T, manifest map[string]any) fstest.MapFS {
	t.Helper()
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return fstest.MapFS{
		ManifestDir + "/codex.json":                 {Data: data},
		"internal/execution/testdata/present.jsonl": {Data: []byte("{}\n")},
	}
}

func TestLoadDir_AcceptsTheValidBase(t *testing.T) {
	ms, err := LoadDir(treeWith(t, validManifest()))
	if err != nil {
		t.Fatalf("the base manifest every refusal case starts from must load: %v", err)
	}
	if len(ms) != 1 || ms[0].Adapter != "codex" {
		t.Fatalf("LoadDir = %+v, want the one codex manifest", ms)
	}
}

func TestLoadDir_RefusesWithAdapterAndField(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(m map[string]any)
		field  string
	}{
		{"unknown key", func(m map[string]any) { m["release_notes"] = "x" }, "release_notes"},
		{"unknown nested key", func(m map[string]any) { m["install"] = map[string]any{"npm": "@openai/codex", "brew": "codex"} }, "brew"},
		{"non-semver min_version", func(m map[string]any) { m["min_version"] = "1.x" }, "min_version"},
		{"non-semver max_tested", func(m map[string]any) { m["max_tested"] = "v0.145.0" }, "max_tested"},
		{"min_version above max_tested", func(m map[string]any) { m["min_version"] = "2.0.0"; m["max_tested"] = "1.0.0" }, "min_version"},
		{"empty feeds", func(m map[string]any) { m["feeds"] = []any{} }, "feeds"},
		{"missing feeds", func(m map[string]any) { delete(m, "feeds") }, "feeds"},
		{"empty min_version, no reason", func(m map[string]any) { m["min_version"] = "" }, "min_version"},
		{"empty max_tested, no reason", func(m map[string]any) { m["max_tested"] = "" }, "max_tested"},
		{"unknown floor_policy", func(m map[string]any) { m["floor_policy"] = "block" }, "floor_policy"},
		{"unknown feed kind", func(m map[string]any) { m["feeds"] = []any{map[string]any{"kind": "brew", "ref": "codex"}} }, "feeds[0].kind"},
		{"tag_pattern on npm feed", func(m map[string]any) {
			m["feeds"] = []any{map[string]any{"kind": "npm", "ref": "@openai/codex", "tag_pattern": "^v.*$"}}
		}, "feeds[0].tag_pattern"},
		{"unanchored tag_pattern", func(m map[string]any) {
			m["feeds"] = []any{map[string]any{"kind": "github", "ref": "openai/codex", "tag_pattern": "v[0-9]"}}
		}, "feeds[0].tag_pattern"},
		{"shell text as installer", func(m map[string]any) {
			m["install"] = map[string]any{"installer": "https://example.com/i.sh | sh"}
		}, "install.installer"},
		{"both install kinds", func(m map[string]any) {
			m["install"] = map[string]any{"npm": "@openai/codex", "installer": "https://example.com/i.sh"}
		}, "install"},
		{"catalog with neither", func(m map[string]any) { m["catalog"] = map[string]any{} }, "catalog"},
		{"adapter not the file name", func(m map[string]any) { m["adapter"] = "gemini" }, "adapter"},
		{"fixture climbs out", func(m map[string]any) { m["fixtures"] = []any{"../x"} }, "fixtures[0]"},
		{"fixture climbs out mid-path", func(m map[string]any) { m["fixtures"] = []any{"internal/../../x"} }, "fixtures[0]"},
		{"absolute fixture", func(m map[string]any) { m["fixtures"] = []any{"/etc/passwd"} }, "fixtures[0]"},
		{"missing fixture", func(m map[string]any) {
			m["fixtures"] = []any{"internal/execution/testdata/missing.jsonl"}
		}, "fixtures[0]"},
		{"directory as fixture", func(m map[string]any) { m["fixtures"] = []any{"internal/execution/testdata"} }, "fixtures[0]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := validManifest()
			c.mutate(m)
			ms, err := LoadDir(treeWith(t, m))
			if err == nil {
				t.Fatalf("LoadDir accepted the manifest: %+v", ms)
			}
			msg := err.Error()
			t.Log(msg)
			if !strings.Contains(msg, `"codex"`) {
				t.Errorf("error does not name the adapter: %s", msg)
			}
			if !strings.Contains(msg, c.field) {
				t.Errorf("error does not name the field %s: %s", c.field, msg)
			}
		})
	}
}

// treeWithRaw is treeWith for a manifest already serialized to bytes, so a
// case above what map[string]any can express (a duplicate exact key) can be
// built by hand.
func treeWithRaw(t *testing.T, data []byte) fstest.MapFS {
	t.Helper()
	return fstest.MapFS{
		ManifestDir + "/codex.json":                 {Data: data},
		"internal/execution/testdata/present.jsonl": {Data: []byte("{}\n")},
	}
}

// TestLoadDir_RefusesCaseVariantAndDuplicateKeys pins #1712's first finding:
// encoding/json matches a struct field's key case-insensitively when there is
// no exact match, and a later duplicate key silently overwrites an earlier
// one, so DisallowUnknownFields alone accepted "Min_Version", "MIN_VERSION"
// and a repeated "min_version" — none of them the exact key LoadDir's schema
// names.
func TestLoadDir_RefusesCaseVariantAndDuplicateKeys(t *testing.T) {
	t.Run("case variant", func(t *testing.T) {
		m := validManifest()
		delete(m, "min_version")
		m["Min_Version"] = "0.111.0"
		data, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		ms, err := LoadDir(treeWithRaw(t, data))
		if err == nil {
			t.Fatalf("LoadDir accepted Min_Version as min_version: %+v", ms)
		}
		msg := err.Error()
		t.Log(msg)
		if !strings.Contains(msg, `"codex"`) || !strings.Contains(msg, "Min_Version") {
			t.Errorf("error does not name the adapter and the offending key: %s", msg)
		}
	})
	t.Run("uppercase variant", func(t *testing.T) {
		m := validManifest()
		delete(m, "min_version")
		m["MIN_VERSION"] = "0.111.0"
		data, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		ms, err := LoadDir(treeWithRaw(t, data))
		if err == nil {
			t.Fatalf("LoadDir accepted MIN_VERSION as min_version: %+v", ms)
		}
		if msg := err.Error(); !strings.Contains(msg, `"codex"`) || !strings.Contains(msg, "MIN_VERSION") {
			t.Errorf("error does not name the adapter and the offending key: %s", msg)
		}
	})
	t.Run("duplicate exact key", func(t *testing.T) {
		m := validManifest()
		data, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		// Compact json.Marshal output always ends in '}'; splice a second,
		// byte-identical "min_version" key in ahead of it.
		if data[len(data)-1] != '}' {
			t.Fatalf("test setup: marshaled manifest does not end in '}': %s", data)
		}
		data = append(data[:len(data)-1], []byte(`,"min_version":"0.111.0"}`)...)
		ms, err := LoadDir(treeWithRaw(t, data))
		if err == nil {
			t.Fatalf("LoadDir accepted a repeated min_version key: %+v", ms)
		}
		msg := err.Error()
		t.Log(msg)
		if !strings.Contains(msg, `"codex"`) || !strings.Contains(msg, "min_version") {
			t.Errorf("error does not name the adapter and the offending key: %s", msg)
		}
	})
}

// TestLoadDir_ReasonMakesAnEmptyVersionValid is the other half of the empty
// min_version case: the reason is what the loader asks for, not a version.
func TestLoadDir_ReasonMakesAnEmptyVersionValid(t *testing.T) {
	m := validManifest()
	m["min_version"], m["min_version_reason"] = "", "no floor yet"
	m["max_tested"], m["max_tested_reason"] = "", "nothing captured yet"
	if _, err := LoadDir(treeWith(t, m)); err != nil {
		t.Fatalf("an empty version with its _reason must load: %v", err)
	}
}

func TestLoad_RefusesTrailingDataAndStrayFiles(t *testing.T) {
	data, err := json.Marshal(validManifest())
	if err != nil {
		t.Fatal(err)
	}
	trailing := fstest.MapFS{
		ManifestDir + "/codex.json":                 {Data: append(data, []byte(` {"adapter":"x"}`)...)},
		"internal/execution/testdata/present.jsonl": {Data: []byte("{}\n")},
	}
	if _, err := LoadDir(trailing); err == nil || !strings.Contains(err.Error(), "trailing data") {
		t.Errorf("trailing object: err = %v, want a trailing-data refusal", err)
	}
	stray := treeWith(t, validManifest())
	stray[ManifestDir+"/codex.yaml"] = &fstest.MapFile{Data: []byte("adapter: codex\n")}
	if _, err := LoadDir(stray); err == nil || !strings.Contains(err.Error(), "codex.yaml") {
		t.Errorf("stray file: err = %v, want a refusal naming codex.yaml", err)
	}
}

// TestOpenCodeTagPatternAgainstCapturedTags checks the opencode github feed's
// tag filter against the real tag and release names of the repository
// (testdata/opencode-feed). The repository tags editor-extension, CI, beta and
// asset builds as well as releases, and several of those start with "v".
func TestOpenCodeTagPatternAgainstCapturedTags(t *testing.T) {
	m, ok := Get("opencode")
	if !ok {
		t.Fatal("no opencode manifest")
	}
	var pattern string
	for _, f := range m.Feeds {
		if f.Kind == FeedGitHub && f.Ref == "anomalyco/opencode" {
			pattern = f.TagPattern
		}
	}
	if pattern == "" {
		t.Fatal("the opencode github feed has no tag_pattern")
	}
	re := regexp.MustCompile(pattern)
	release := regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

	releases := readLines(t, "testdata/opencode-feed/releases.txt")
	tags := readLines(t, "testdata/opencode-feed/tags.txt")

	var rejectedV []string
	for _, tag := range tags {
		if got, want := re.MatchString(tag), release.MatchString(tag); got != want {
			t.Errorf("tag %q: pattern match = %v, want %v", tag, got, want)
		}
		if strings.HasPrefix(tag, "v") && !re.MatchString(tag) {
			rejectedV = append(rejectedV, tag)
		}
	}
	// The reason the pattern is anchored: a v* glob would take these.
	for _, want := range []string{"vscode-v0.0.13", "v0.1.0-beta1", "v0.0.1-feature-bench"} {
		if i := sort.SearchStrings(rejectedV, want); i >= len(rejectedV) || rejectedV[i] != want {
			t.Errorf("captured tag %q is not among the v-prefixed tags the pattern rejects", want)
		}
	}
	newest := ""
	for _, r := range releases {
		if re.MatchString(r) && (newest == "" || compareSemver(r[1:], newest[1:]) > 0) {
			newest = r
		}
	}
	if newest != "v"+m.MaxTested {
		t.Errorf("newest captured release is %q; max_tested %q was captured alongside it", newest, m.MaxTested)
	}
}

func readLines(t *testing.T, name string) []string {
	t.Helper()
	f, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []string
	s := bufio.NewScanner(f)
	for s.Scan() {
		out = append(out, s.Text())
	}
	if err := s.Err(); err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 {
		t.Fatalf("%s is empty", name)
	}
	return out
}

// TestPackageStartsNoProcess keeps the loader a reader of data. Manifest
// strings reach CI shell steps and remediation text later, so this package
// must not be able to run one: its imports are an allowlist, and adding
// anything that can start a process has to be a deliberate edit here.
func TestPackageStartsNoProcess(t *testing.T) {
	allowed := map[string]bool{
		"bytes": true, "embed": true, "encoding/json": true, "errors": true, "fmt": true,
		"io": true, "io/fs": true, "path": true, "path/filepath": true, "reflect": true, "regexp": true,
		"sort": true, "strconv": true, "strings": true, "sync": true, "unicode": true,
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, file, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if !allowed[p] {
				t.Errorf("%s imports %q, which is not on the allowlist", file, p)
			}
		}
	}
}

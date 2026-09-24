package adapters

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The configs/opencode operator templates (#1666) are validated against the
// same pinned OpenCode config schema as the per-run configs
// (schema_contract_test.go), so a misspelled or retired key in a template is
// a red test rather than a key OpenCode silently drops.

// templatesDir is configs/opencode, relative to this package.
const templatesDir = "../../../configs/opencode"

var templateFiles = []string{"opencode.lmstudio.jsonc", "opencode.ollama.jsonc"}

// templateCredential matches a credential-shaped value.
var templateCredential = regexp.MustCompile(`sk-|xai-|Bearer`)

// templateURL finds every http(s) URL in a string value.
var templateURL = regexp.MustCompile(`https?://[^\s"']+`)

// stripJSONC removes // and /* */ comments outside strings, and trailing
// commas before } or ].
func stripJSONC(src string) string {
	var b strings.Builder
	inStr, esc := false, false
	for i := 0; i < len(src); i++ {
		c := src[i]
		if inStr {
			b.WriteByte(c)
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		if c == '"' {
			inStr = true
			b.WriteByte(c)
			continue
		}
		if c == '/' && i+1 < len(src) && src[i+1] == '/' {
			for i < len(src) && src[i] != '\n' {
				i++
			}
			b.WriteByte('\n')
			continue
		}
		if c == '/' && i+1 < len(src) && src[i+1] == '*' {
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				return b.String()
			}
			i += end + 3
			continue
		}
		b.WriteByte(c)
	}
	return regexp.MustCompile(`,(\s*[}\]])`).ReplaceAllString(b.String(), "$1")
}

func readTemplate(t *testing.T, name string) (string, map[string]any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(templatesDir, name))
	if err != nil {
		t.Fatalf("read template %s: %v", name, err)
	}
	content := stripJSONC(string(raw))
	var doc map[string]any
	if err := json.Unmarshal([]byte(content), &doc); err != nil {
		t.Fatalf("%s is not JSONC: %v", name, err)
	}
	return content, doc
}

// walkKeys calls fn for every object key in v, at any depth.
func walkKeys(v any, fn func(string)) {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			fn(k)
			walkKeys(e, fn)
		}
	case []any:
		for _, e := range x {
			walkKeys(e, fn)
		}
	}
}

// walkStrings calls fn for every string value in v.
func walkStrings(v any, fn func(string)) {
	switch x := v.(type) {
	case string:
		fn(x)
	case map[string]any:
		for _, e := range x {
			walkStrings(e, fn)
		}
	case []any:
		for _, e := range x {
			walkStrings(e, fn)
		}
	}
}

func TestOpenCodeTemplatesContract(t *testing.T) {
	sch := compileSchemaContract(t, readPinnedSchema(t))
	for _, name := range templateFiles {
		t.Run(name, func(t *testing.T) {
			content, doc := readTemplate(t, name)
			if err := validateContent(t, sch, content); err != nil {
				t.Fatalf("does not validate against the pinned schema: %v", err)
			}
			if doc["share"] != "disabled" {
				t.Errorf(`share = %v, want "disabled"`, doc["share"])
			}
			if doc["autoupdate"] != false {
				t.Errorf("autoupdate = %v, want false", doc["autoupdate"])
			}
			providers, _ := doc["provider"].(map[string]any)
			nModels := 0
			for pid, p := range providers {
				models, _ := p.(map[string]any)["models"].(map[string]any)
				for mid, m := range models {
					nModels++
					limit, _ := m.(map[string]any)["limit"].(map[string]any)
					for _, k := range []string{"context", "output"} {
						if n, _ := limit[k].(float64); n <= 0 {
							t.Errorf("%s/%s: limit.%s = %v, want > 0", pid, mid, k, limit[k])
						}
					}
				}
			}
			if nModels == 0 {
				t.Error("no provider model declares a limit")
			}
			walkKeys(doc, func(k string) {
				if k == "apiKey" || k == "headers" {
					t.Errorf("credential-bearing key %q; templates hold no credentials", k)
				}
			})
			walkStrings(doc, func(s string) {
				if templateCredential.MatchString(s) {
					t.Errorf("credential-shaped value %q", s)
				}
				for _, raw := range templateURL.FindAllString(s, -1) {
					if raw == schemaContractSchemaURL {
						continue
					}
					u, err := url.Parse(raw)
					if err != nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") {
						t.Errorf("non-loopback URL %q", raw)
					}
				}
			})
		})
	}
}

// TestOpenCodeTemplatesRejectUnknownKey proves the validation above is not
// vacuous: the same template with one unknown top-level key fails it.
func TestOpenCodeTemplatesRejectUnknownKey(t *testing.T) {
	sch := compileSchemaContract(t, readPinnedSchema(t))
	for _, name := range templateFiles {
		_, doc := readTemplate(t, name)
		doc["nightgauge_unknown_key"] = true
		b, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		if validateContent(t, sch, string(b)) == nil {
			t.Errorf("%s with an unknown top-level key still validates", name)
		}
	}
}

func TestStripJSONCKeepsStrings(t *testing.T) {
	got := stripJSONC(`{"a": "http://x//y", /* c */ "b": 1, // d
}`)
	var m map[string]any
	if err := json.Unmarshal([]byte(got), &m); err != nil || m["a"] != "http://x//y" {
		t.Fatalf("stripJSONC = %q (%v)", got, err)
	}
}

package adapters

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The configs/opencode operator templates (#1666) use OpenCode 2.x's native
// keys. 2.x drops a 1.x key it does not translate, and a key of the wrong
// type, without a word, so a template holding one is a red test rather than
// a setting OpenCode silently ignores. Each template was read back with
// `opencode debug config --standalone` on 2.2.7 with every key intact.

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

// openCodeTemplateKeys are the top-level keys a template may set: 2.x
// native keys only.
var openCodeTemplateKeys = map[string]bool{
	"$schema": true, "model": true, "agents": true, "share": true, "update": true,
	"compaction": true, "tool_output": true, "providers": true,
}

func TestOpenCodeTemplatesContract(t *testing.T) {
	for _, name := range templateFiles {
		t.Run(name, func(t *testing.T) {
			_, doc := readTemplate(t, name)
			if err := openCodeTemplateKeysCheck(doc); err != nil {
				t.Fatal(err)
			}
			if doc["share"] != "disabled" {
				t.Errorf(`share = %v, want "disabled"`, doc["share"])
			}
			if doc["update"] != "disable" {
				t.Errorf(`update = %v, want "disable"`, doc["update"])
			}
			if ref, _ := doc["model"].(map[string]any); ref["providerID"] == nil || ref["model"] == nil {
				t.Errorf("model = %v, want {providerID, model}", doc["model"])
			}
			providers, _ := doc["providers"].(map[string]any)
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
					if raw == "https://opencode.ai/config.json" {
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

// openCodeTemplateProviderKeys and openCodeTemplateModelKeys are the keys a
// template's provider block and model entry may set: 2.x names, each read
// back intact on 2.2.7 (1.x's npm, options and tool_call are not among them).
var (
	openCodeTemplateProviderKeys = map[string]bool{"package": true, "name": true, "settings": true, "models": true}
	openCodeTemplateModelKeys    = map[string]bool{"modelID": true, "name": true, "capabilities": true, "limit": true}
)

// openCodeTemplateKeysCheck refuses a top-level, provider or model key that
// is not a 2.x key a template sets, naming a 1.x top-level key as such.
func openCodeTemplateKeysCheck(doc map[string]any) error {
	for key := range doc {
		if slices.Contains(openCodeLegacyKeys, key) {
			return fmt.Errorf("1.x key %q, which 2.x translates or drops without a word", key)
		}
		if !openCodeTemplateKeys[key] {
			return fmt.Errorf("unknown top-level key %q", key)
		}
	}
	providers, _ := doc["providers"].(map[string]any)
	for pid, p := range providers {
		block, _ := p.(map[string]any)
		for key := range block {
			if !openCodeTemplateProviderKeys[key] {
				return fmt.Errorf("providers.%s: unknown key %q", pid, key)
			}
		}
		models, _ := block["models"].(map[string]any)
		for mid, m := range models {
			entry, _ := m.(map[string]any)
			for key := range entry {
				if !openCodeTemplateModelKeys[key] {
					return fmt.Errorf("providers.%s.models.%s: unknown key %q", pid, mid, key)
				}
			}
		}
	}
	return nil
}

// TestOpenCodeTemplatesRejectUnknownKey proves the key check above is not
// vacuous: the same template with an unknown or a 1.x key, at the top, in a
// provider block or in a model entry, fails it.
func TestOpenCodeTemplatesRejectUnknownKey(t *testing.T) {
	for _, name := range templateFiles {
		for _, key := range []string{"nightgauge_unknown_key", "autoupdate", "provider"} {
			_, doc := readTemplate(t, name)
			doc[key] = true
			if openCodeTemplateKeysCheck(doc) == nil {
				t.Errorf("%s with the key %q still passes", name, key)
			}
		}
		for _, key := range []string{"npm", "options"} {
			_, doc := readTemplate(t, name)
			for _, p := range doc["providers"].(map[string]any) {
				p.(map[string]any)[key] = true
			}
			if openCodeTemplateKeysCheck(doc) == nil {
				t.Errorf("%s with the provider key %q still passes", name, key)
			}
		}
		_, doc := readTemplate(t, name)
		for _, p := range doc["providers"].(map[string]any) {
			for _, m := range p.(map[string]any)["models"].(map[string]any) {
				m.(map[string]any)["tool_call"] = true
			}
		}
		if openCodeTemplateKeysCheck(doc) == nil {
			t.Errorf("%s with the model key tool_call still passes", name)
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

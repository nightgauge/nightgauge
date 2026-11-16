package adapters

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
)

// validOpenCodeDocument is a document the builder wrote, decoded generically
// so a test can break one key of it.
func validOpenCodeDocument(t *testing.T) map[string]any {
	t.Helper()
	built, err := buildOpenCodeConfigFor(t, lmStudioSettings(), RunOptions{Model: "lmstudio/qwen/qwen3.8-27b", MaxTurns: 40, AllowedTools: []string{"Read", "Bash", "Task"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return decodeOpenCodeConfig(t, built.Content)
}

func encodeOpenCodeDocument(t *testing.T, doc map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// TestValidateOpenCodeConfigDocumentAcceptsTheBuildersOutput: every document
// the builder writes passes, for an endpoint, anthropic and a hosted catalog
// provider (which has no block of its own), and after the plugins key is
// added.
func TestValidateOpenCodeConfigDocumentAcceptsTheBuildersOutput(t *testing.T) {
	for _, tc := range []struct {
		model string
		env   map[string]string
	}{
		{"lmstudio/qwen/qwen3.8-27b", nil},
		{"anthropic/claude-sonnet-5", map[string]string{"ANTHROPIC_API_KEY": "fake-anthropic-credential-3301"}},
		{"openai/gpt-5.5", nil},
	} {
		built, err := buildOpenCodeConfigFor(t, lmStudioSettings(), RunOptions{Model: tc.model}, tc.env)
		if err != nil {
			t.Fatalf("%s: %v", tc.model, err)
		}
		if err := validateOpenCodeConfigDocument(built.Content); err != nil {
			t.Errorf("%s: the builder's own document is refused: %v", tc.model, err)
		}
		withPlugin, err := addNightgaugePluginToConfig(built.Content, filepath.Join(t.TempDir(), "plugin"))
		if err != nil {
			t.Errorf("%s: the document with its plugins key is refused: %v", tc.model, err)
		}
		if !strings.Contains(withPlugin, `"plugins":[`) {
			t.Errorf("%s: addNightgaugePluginToConfig wrote no plugins list:\n%s", tc.model, withPlugin)
		}
	}
}

// TestValidateOpenCodeConfigDocumentRefuses covers every refusal: 2.2.7 loads
// each of these documents with exit 0 and a control missing, so the
// validator is the only thing that can stop it.
func TestValidateOpenCodeConfigDocumentRefuses(t *testing.T) {
	for _, tc := range []struct {
		name  string
		edit  func(doc map[string]any)
		raw   string // used instead of edit when set
		error string
	}{
		{name: "not JSON", raw: `{"model":`, error: "not a JSON object"},
		{name: "trailing data", raw: `{"share":"disabled"} {}`, error: "not a JSON object"}, // json.Unmarshal refuses it
		{name: "wrong-typed model", raw: `{"model":5}`, error: "cannot unmarshal number"},
		{name: "wrong-typed steps", edit: func(d map[string]any) { setPath(d, "x", "agents", "build", "steps") }, error: "cannot unmarshal string"},
		{name: "unknown top-level key", edit: func(d map[string]any) { d["nightgauge_unknown"] = true }, error: `unknown field "nightgauge_unknown"`},
		{name: "unknown nested key", edit: func(d map[string]any) { setPath(d, true, "agents", "build", "mode") }, error: `unknown field "mode"`},
		{name: "1.x subagent_depth", edit: func(d map[string]any) { d["subagent_depth"] = 1 }, error: `1.x key "subagent_depth"`},
		{name: "1.x small_model", edit: func(d map[string]any) { d["small_model"] = "lmstudio/x" }, error: `1.x key "small_model"`},
		{name: "1.x permission map", edit: func(d map[string]any) { d["permission"] = map[string]any{"bash": "allow"} }, error: `1.x key "permission"`},
		{name: "1.x plugin", edit: func(d map[string]any) { d["plugin"] = []string{"x"} }, error: `1.x key "plugin"`},
		{name: "null share", edit: func(d map[string]any) { d["share"] = nil }, error: `"share" is null`},
		{name: "share not disabled", edit: func(d map[string]any) { d["share"] = "auto" }, error: "share is \"auto\""},
		{name: "update missing", edit: func(d map[string]any) { delete(d, "update") }, error: "update is \"\""},
		{name: "default agent", edit: func(d map[string]any) { d["default_agent"] = "plan" }, error: "default_agent"},
		{name: "ask permission", edit: func(d map[string]any) { appendRule(d, []string{"permissions"}, "shell", "*", "ask") }, error: `effect "ask"`},
		{name: "ask on an agent", edit: func(d map[string]any) { appendRule(d, []string{"agents", "build", "permissions"}, "shell", "*", "ask") }, error: `effect "ask"`},
		{name: "empty permissions", edit: func(d map[string]any) { d["permissions"] = []any{} }, error: "permissions is empty"},
		{name: "policies not deny-first", edit: func(d map[string]any) {
			d["policies"] = []any{map[string]any{"action": "provider.use", "resource": "lmstudio", "effect": "allow"}}
		}, error: `policies must open with provider.use denied`},
		{name: "dispatched provider not allowed", edit: func(d map[string]any) {
			d["policies"] = []any{map[string]any{"action": "provider.use", "resource": "*", "effect": "deny"}}
		}, error: "allow the dispatched provider \"lmstudio\" alone"},
		{name: "subagent deny missing", edit: func(d map[string]any) { setPath(d, nil, "agents", "general", "permissions") }, error: "agents.general does not deny subagent"},
		{name: "second provider allowed", edit: func(d map[string]any) { appendRule(d, []string{"policies"}, "provider.use", "openai", "allow") }, error: "allow the dispatched provider \"lmstudio\" alone"},
		{name: "provider allow-list reopened", edit: func(d map[string]any) { appendRule(d, []string{"policies"}, "provider.use", "*", "allow") }, error: "alone, nothing else"},
		{name: "subagent reopened for one agent", edit: func(d map[string]any) {
			appendRule(d, []string{"agents", "general", "permissions"}, "subagent", "explore", "allow")
		}, error: "agents.general does not deny subagent"},
		{name: "subagent deny reopened", edit: func(d map[string]any) { appendRule(d, []string{"agents", "explore", "permissions"}, "*", "*", "allow") }, error: "agents.explore does not deny subagent"},
		{name: "agent missing", edit: func(d map[string]any) { delete(d["agents"].(map[string]any), "title") }, error: "agents.title is missing"},
		{name: "agent on another model", edit: func(d map[string]any) {
			setPath(d, map[string]any{"providerID": "opencode", "model": "free"}, "agents", "compaction", "model")
		}, error: "agents.compaction.model"},
		{name: "model unpinned", edit: func(d map[string]any) { d["model"] = map[string]any{"providerID": "lmstudio"} }, error: "model must name"},
		{name: "empty plugin entry", edit: func(d map[string]any) { d["plugins"] = []string{""} }, error: "plugins[0] is empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			content := tc.raw
			if content == "" {
				doc := validOpenCodeDocument(t)
				tc.edit(doc)
				content = encodeOpenCodeDocument(t, doc)
			}
			err := validateOpenCodeConfigDocument(content)
			if err == nil {
				t.Fatalf("the document was accepted:\n%s", content)
			}
			if !strings.Contains(err.Error(), tc.error) {
				t.Errorf("error = %q, want it to contain %q", err, tc.error)
			}
		})
	}
}

// TestOpenCodeConfigSafetyKeys carries over what the retired 1.x schema
// contract's TestSecurityKeysPresentAndKnown asserted, on the 2.x keys and
// across the dispatches the builder supports: share "disabled", update
// "disable", the dispatched provider alone, instructions only absolute paths,
// and mcp an object.
func TestOpenCodeConfigSafetyKeys(t *testing.T) {
	url := regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)
	for _, tc := range []struct {
		model, key string
		settings   config.OpenCodeConfig
		env        map[string]string
	}{
		{"lmstudio/qwen/qwen3.8-27b", "lmstudio", lmStudioSettings(), nil},
		{"anthropic/claude-sonnet-5", "anthropic", config.OpenCodeConfig{}, map[string]string{"ANTHROPIC_API_KEY": "fake-anthropic-credential-3301"}},
	} {
		in, err := OpenCodeConfigInputFor(tc.settings, RunOptions{Stage: "feature-dev", Model: tc.model, WorktreeDir: goldenWorktree}, goldenRunRoot, envLookup(tc.env))
		if err != nil {
			t.Fatal(err)
		}
		in.Repository = goldenRepository(t)
		built, err := BuildOpenCodeConfig(in)
		if err != nil {
			t.Fatalf("%s: %v", tc.model, err)
		}
		doc := decodeOpenCodeConfig(t, built.Content)
		if doc["share"] != "disabled" || doc["update"] != "disable" {
			t.Errorf("%s: share %v, update %v; want disabled, disable", tc.model, doc["share"], doc["update"])
		}
		policies, _ := doc["policies"].([]any)
		var allowed []string
		for _, p := range policies {
			r, _ := p.(map[string]any)
			if r["effect"] == "allow" {
				allowed = append(allowed, r["resource"].(string))
			}
		}
		if len(allowed) != 1 || allowed[0] != tc.key {
			t.Errorf("%s: provider.use allows %v, want [%s], the dispatched provider alone", tc.model, allowed, tc.key)
		}
		entries, ok := doc["instructions"].([]any)
		if !ok || len(entries) == 0 {
			t.Errorf("%s: instructions = %v, want a list of the repository's steering files", tc.model, doc["instructions"])
		}
		for _, e := range entries {
			path, _ := e.(string)
			if !filepath.IsAbs(path) || url.MatchString(path) {
				t.Errorf("%s: instructions entry %v is not an absolute path", tc.model, e)
			}
		}
		if servers, ok := jsonPath(doc, "mcp", "servers").(map[string]any); !ok || len(servers) == 0 {
			t.Errorf("%s: mcp.servers = %v, want the repository's servers", tc.model, jsonPath(doc, "mcp", "servers"))
		}
	}
}

// TestOpenCodePermissionRulesTranslatesInOrder: the permission map becomes a
// rule list in field and pattern-insertion order, every action already a 2.x
// name (#3304), so no 1.x-only action reaches the rules. Order
// carries meaning (the last matching rule wins), so the expected rules are
// literal and must appear in this order.
func TestOpenCodePermissionRulesTranslatesInOrder(t *testing.T) {
	p := openCodePermissionMap(RunOptions{AllowedTools: []string{"Read", "Bash(git *)"}, WorktreeDir: "/work/w"}, "")
	rules, err := openCodePermissionRules(p)
	if err != nil {
		t.Fatal(err)
	}
	inOrder := []openCodeRuleJSON{
		{"*", "*", "deny"},
		{"read", "*", "allow"},
		{"read", "*.env", "deny"},
		{"read", "**/gh/hosts.yml", "deny"},
		{"edit", "*", "deny"},
		{"edit", ".opencode/**", "deny"},
		{"write", "*", "deny"},
		{"write", ".opencode/**", "deny"},
		{"shell", "*", "deny"},
		{"shell", "git *", "allow"},
		{"shell", "rm -rf *", "deny"},
		{"subagent", "*", "deny"},
		{"execute", "*", "deny"},
		{"question", "*", "deny"},
		{"doom_loop", "*", "deny"},
		{"external_directory", "*", "deny"},
		{"external_directory", "/work/w/**", "allow"},
	}
	next := 0
	for _, r := range rules {
		if next < len(inOrder) && r == inOrder[next] {
			next++
		}
		switch r.Action {
		case "bash", "task", "list", "todowrite":
			t.Errorf("rule %+v keeps a 1.x tool name", r)
		}
		if r.Effect != "allow" && r.Effect != "deny" {
			t.Errorf("rule %+v is neither allow nor deny", r)
		}
	}
	if next != len(inOrder) {
		t.Errorf("rules do not hold %+v in order (matched %d of %d):\n%+v", inOrder[next], next, len(inOrder), rules)
	}
}

// setPath sets doc[keys...] = v, creating no intermediate object.
func setPath(doc map[string]any, v any, keys ...string) {
	m := doc
	for _, k := range keys[:len(keys)-1] {
		m = m[k].(map[string]any)
	}
	if v == nil {
		delete(m, keys[len(keys)-1])
		return
	}
	m[keys[len(keys)-1]] = v
}

// appendRule appends a rule to the list at keys, creating the list.
func appendRule(doc map[string]any, keys []string, action, resource, effect string) {
	m := doc
	for _, k := range keys[:len(keys)-1] {
		m = m[k].(map[string]any)
	}
	last := keys[len(keys)-1]
	list, _ := m[last].([]any)
	m[last] = append(list, map[string]any{"action": action, "resource": resource, "effect": effect})
}

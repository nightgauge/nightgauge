package adapters

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/nightgauge/nightgauge/internal/adaptercompat"
	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/models"
)

// The OpenCode config-schema contract (#1634).
//
// Every OPENCODE_CONFIG_CONTENT Nightgauge generates is validated against a
// pinned copy of the schema OpenCode publishes at https://opencode.ai/config.json,
// captured for the compat manifest's max-tested version (see
// testdata/opencode-config-schema/README.md). OpenCode 1.18.30 itself drops an
// unknown key without a word (ADR-022 § 20), so a misspelled, renamed or
// retired key in the per-run config would silently do nothing at run time;
// this suite is what turns that into a red test.
//
// The configs under test are the ones PrepareOpenCodeRun, the preparation the
// adapter and `nightgauge opencode config` share, builds for a matrix of
// dispatches (schemaContractConfigs). The validator is imported only here, in
// a _test.go file, so it never reaches the nightgauge binary.

// schemaContractSchemaPath is the pinned schema, byte for byte as fetched.
const schemaContractSchemaPath = "testdata/opencode-config-schema/opencode-config.schema.json"

// schemaContractSchemaURL is where OpenCode publishes the schema. The pinned
// schema has no $id of its own, so it is registered under this URL.
const schemaContractSchemaURL = "https://opencode.ai/config.json"

// schemaContractModelsDevURL is the one external document the pinned schema
// refers to: model and small_model, and an agent's model, are a string that
// also $refs the Model definition of models.dev's live model catalog.
//
// That definition is an enum of every model id the catalog lists today. It is
// not part of OpenCode's config contract: it moves with the catalog, not with
// an OpenCode release, and OpenCode 1.18.30 runs a model it does not list, as
// every model a declared endpoint serves is. The pinned schema already
// declares the property as a string, so the stand-in document the loader gives
// for this URL says only that. Nothing is fetched.
const schemaContractModelsDevURL = "https://models.dev/model-schema.json"

// schemaContractLoader serves the compiler every external document it asks
// for, so validation never reaches the network: the models.dev stand-in, and
// an error for any other URL.
type schemaContractLoader struct{}

func (schemaContractLoader) Load(url string) (any, error) {
	if url == schemaContractModelsDevURL {
		return map[string]any{"$defs": map[string]any{"Model": map[string]any{"type": "string"}}}, nil
	}
	return nil, fmt.Errorf("the schema contract fetches nothing, and the pinned schema refers to %s, which it does not know: pin it or give it a stand-in", url)
}

// readPinnedSchema reads the pinned schema's bytes.
func readPinnedSchema(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(schemaContractSchemaPath)
	if err != nil {
		t.Fatalf("read the pinned OpenCode config schema: %v", err)
	}
	return raw
}

// schemaContractSchemaPathEnv points TestGeneratedConfigsValidate,
// TestValidatorRejectsUnknownKey, TestNoDeprecatedKeys and
// TestSecurityKeysPresentAndKnown at a schema other than the pinned one, so
// the release canary's schema-diff leg (#1639) can re-run this suite against
// a freshly fetched live schema when it differs from the manifest's
// config_schema_sha256, with no code change. TestManifestSchemaHash ignores
// it: its whole point is comparing the PINNED schema's own hash against the
// manifest, which an override would defeat.
const schemaContractSchemaPathEnv = "NIGHTGAUGE_OPENCODE_SCHEMA_PATH"

// schemaContractSchema reads the schema the four suites above validate the
// generated configs against: the override when schemaContractSchemaPathEnv is
// set, else the pinned schema (readPinnedSchema).
func schemaContractSchema(t *testing.T) []byte {
	t.Helper()
	path := os.Getenv(schemaContractSchemaPathEnv)
	if path == "" {
		return readPinnedSchema(t)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the schema %s names (%s): %v", schemaContractSchemaPathEnv, path, err)
	}
	return raw
}

// compileSchemaContract compiles schema, a JSON Schema document, as the
// document at schemaContractSchemaURL, under draft 2020-12, the draft the
// pinned schema declares.
func compileSchemaContract(t *testing.T, schema []byte) *jsonschema.Schema {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schema))
	if err != nil {
		t.Fatalf("the schema is not JSON: %v", err)
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.UseLoader(schemaContractLoader{})
	if err := c.AddResource(schemaContractSchemaURL, doc); err != nil {
		t.Fatal(err)
	}
	sch, err := c.Compile(schemaContractSchemaURL)
	if err != nil {
		t.Fatalf("compile the schema: %v", err)
	}
	return sch
}

// validateContent validates content, a config document, against sch.
func validateContent(t *testing.T, sch *jsonschema.Schema, content string) error {
	t.Helper()
	inst, err := jsonschema.UnmarshalJSON(strings.NewReader(content))
	if err != nil {
		t.Fatalf("the config is not JSON: %v\n%s", err, content)
	}
	return sch.Validate(inst)
}

// schemaContractConfig is one per-run config the matrix built.
type schemaContractConfig struct {
	// name says which dispatch it is.
	name string
	// provider is the dispatched provider key.
	provider string
	// content is OPENCODE_CONFIG_CONTENT.
	content string
}

// The model servers of the matrix. The ollama endpoint is on an RFC 5737
// documentation address, so the matrix includes an endpoint that is not on
// this machine; nothing is ever sent to either.
var schemaContractEndpoints = []struct {
	key, kind, baseURL, model string
}{
	{"lmstudio", "lm-studio", "http://127.0.0.1:1234/v1", "lmstudio/qwen/qwen3.8-27b"},
	{"ollama", "ollama", "http://192.0.2.10:11434/v1", "ollama/qwen3:32b"},
}

// schemaContractMcpJSON is the pipeline MCP config the forge serves when a
// dispatch has MCP servers: a local server with a variable, and a remote one
// with a credential header.
const schemaContractMcpJSON = `{"mcpServers": {
  "fs": {"command": "npx", "args": ["-y", "srv"], "env": {"LEVEL": "debug", "TOKEN": "${MCP_FIXTURE_TOKEN}"}},
  "remote": {"type": "http", "url": "https://mcp.example.test/mcp", "headers": {"Authorization": "Bearer ${MCP_FIXTURE_TOKEN}"}}
}}`

// schemaContractConfigs builds, through PrepareOpenCodeRun, the per-run config
// of every dispatch in the matrix:
//
//   - the provider: a declared LM Studio or Ollama endpoint, or anthropic, the
//     one hosted provider a run can hold a credential for (ADR-022 § 17);
//   - MCP servers on the repository's default branch, or none;
//   - for an endpoint, its limits from the machine-tier override or discovered
//     from the server (a fake discovery), which also decides the compaction
//     values; a hosted model's come from OpenCode's catalog;
//   - inherit_user_config off or on;
//   - the pipeline defaults, or the machine tier's snapshot, lsp and formatter
//     overrides with a stage turn cap and token cap.
//
// Compaction is never off: the builder always sets compaction.auto, so the
// axis is where the limits it is computed from come from. The stage's allowed
// tools are not an axis: the builder maps none yet (BuildCommand's doc says
// so), so no profile changes the content until #1638 adds the permission map.
func schemaContractConfigs(t *testing.T) []schemaContractConfig {
	t.Helper()
	t.Cleanup(SwapOpenCodeLocalDiscoveryForTest(func(ep OpenCodeEndpoint, model string) (models.LocalDescriptor, error) {
		for _, e := range schemaContractEndpoints {
			if ep.ID == e.key && ep.BaseURL == e.baseURL && model == e.model {
				_, id, _ := strings.Cut(model, "/")
				return models.LocalDescriptor{Endpoint: ep.ID, Provider: ep.Provider, Model: id, ContextWindow: 262144, ToolCall: true}, nil
			}
		}
		t.Errorf("discovery was asked for %s on endpoint %s, which the matrix does not declare", model, ep.ID)
		return models.LocalDescriptor{}, errors.New("not a matrix endpoint")
	}))
	wt := openCodeFixtureRepo(t, map[string]string{
		"AGENTS.md":     "# Rules\n\nEvery change carries a changelog entry. See @docs/rules.md\n",
		"docs/rules.md": "# More rules\n",
	})
	env := map[string]string{
		"ANTHROPIC_API_KEY": "fake-anthropic-credential-1634",
		"MCP_FIXTURE_TOKEN": "fake-mcp-credential-1634",
	}
	on, off := true, false

	type dispatch struct {
		provider, model, limits string
		settings                config.OpenCodeConfig
	}
	var dispatches []dispatch
	for _, e := range schemaContractEndpoints {
		for _, limits := range []string{"override", "discovered"} {
			s := config.OpenCodeConfig{Provider: e.kind, BaseURL: e.baseURL}
			if limits == "override" {
				s.Limit = config.OpenCodeLimit{Context: 131072, Output: 8192}
			}
			dispatches = append(dispatches, dispatch{e.key, e.model, limits, s})
		}
	}
	dispatches = append(dispatches, dispatch{"anthropic", "anthropic/claude-sonnet-5", "catalog", config.OpenCodeConfig{}})

	var out []schemaContractConfig
	for _, d := range dispatches {
		for _, mcp := range []bool{false, true} {
			for _, inherit := range []bool{false, true} {
				for _, tuned := range []bool{false, true} {
					name := fmt.Sprintf("%s limits=%s mcp=%t inherit_user_config=%t tuned=%t", d.provider, d.limits, mcp, inherit, tuned)
					settings := d.settings
					settings.InheritUserConfig = inherit
					run := RunOptions{Stage: "feature-dev", Model: d.model, WorktreeDir: wt}
					if tuned {
						settings.Snapshot, settings.LSP, settings.Formatter = &on, &off, &off
						run.MaxTurns, run.MaxTokens = 40, 4096
					}
					var forgeFiles map[string]string
					if mcp {
						forgeFiles = map[string]string{".mcp.json": schemaContractMcpJSON}
					}
					home := t.TempDir()
					var prepared *OpenCodeRun
					var err error
					captureAdapterStderr(t, func() {
						prepared, err = PrepareOpenCodeRun(withMcpForge(OpenCodeRunRequest{
							Home:               home,
							ID:                 testRunID,
							MachineConfigDir:   filepath.Join(home, ".nightgauge"),
							Run:                run,
							Settings:           settings,
							Lookup:             envLookup(env),
							GOOS:               "linux",
							ManagedConfigFiles: []string{},
						}, forgeFiles))
					})
					if err != nil {
						t.Fatalf("%s: PrepareOpenCodeRun: %v", name, err)
					}
					if servers, _ := decodeOpenCodeConfig(t, prepared.ConfigContent)["mcp"].(map[string]any); (len(servers) > 0) != mcp {
						t.Fatalf("%s: the config's mcp holds %d server(s); the matrix asked for servers: %t", name, len(servers), mcp)
					}
					out = append(out, schemaContractConfig{name: name, provider: d.provider, content: prepared.ConfigContent})
				}
			}
		}
	}
	return out
}

// TestGeneratedConfigsValidate: every per-run config the matrix builds is a
// valid document under the pinned schema, as a whole, with zero errors.
func TestGeneratedConfigsValidate(t *testing.T) {
	sch := compileSchemaContract(t, schemaContractSchema(t))
	configs := schemaContractConfigs(t)
	if len(configs) != 40 {
		t.Fatalf("the matrix built %d configs, want 40", len(configs))
	}
	for _, c := range configs {
		if err := validateContent(t, sch, c.content); err != nil {
			t.Errorf("%s: the per-run config does not validate against the pinned OpenCode schema:\n%v\n%s", c.name, err, c.content)
		}
	}
}

// TestValidatorRejectsUnknownKey is the negative control: a generated config
// with an unknown top-level key fails validation, naming the key, so the
// suite enforces the schema rather than accepting everything. The same
// documents pass a permissive {} schema compiled the same way, so the
// rejection is the pinned schema's, not the harness's.
func TestValidatorRejectsUnknownKey(t *testing.T) {
	pinned := compileSchemaContract(t, schemaContractSchema(t))
	permissive := compileSchemaContract(t, []byte(`{}`))
	for _, c := range schemaContractConfigs(t) {
		doc := decodeOpenCodeConfig(t, c.content)
		doc["nightgaugeBogus"] = 1
		raw, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		err = validateContent(t, pinned, string(raw))
		if err == nil {
			t.Errorf("%s: a config with the unknown top-level key nightgaugeBogus validated against the pinned schema", c.name)
		} else if !strings.Contains(err.Error(), "nightgaugeBogus") {
			t.Errorf("%s: the config with nightgaugeBogus failed, but not over that key:\n%v", c.name, err)
		}
		if err := validateContent(t, permissive, string(raw)); err != nil {
			t.Errorf("%s: a permissive {} schema rejected the document, so the harness, not the schema, is failing it: %v", c.name, err)
		}
	}
}

// schemaContractAllowedDeprecated are the deprecated properties the builder
// sets on purpose, each with why. An entry is refused once no generated
// config uses it or the pinned schema stops marking it deprecated, so the
// list cannot outlive its reason.
var schemaContractAllowedDeprecated = map[string]string{
	// The pinned schema marks mode "@deprecated Use `agent` field instead.",
	// but opencode 1.18.30 still merges every mode.<agent> over agent.<agent>
	// after every config layer, so the content sets the mode entry of each
	// built-in primary agent, or a lower layer's would win over its agent
	// entry (ADR-022 § 8). Were OpenCode to drop mode, the schema would lose
	// the property and TestGeneratedConfigsValidate would fail.
	"/mode": "ADR-022 § 8: mode.<agent> is merged over agent.<agent> after every layer",
}

// TestNoDeprecatedKeys: no generated config uses a property the pinned schema
// marks deprecated, found by walking each config against the schema, so a
// future deprecation is caught without a hand-kept list. The renames are
// asserted by name as well: steps and share, never maxSteps or autoshare.
// Each finding is printed as the JSON pointer of the property.
func TestNoDeprecatedKeys(t *testing.T) {
	schema := decodeSchemaDoc(t, schemaContractSchema(t))

	// The walker is not vacuous: it finds each deprecated property the
	// renames left behind, and not their replacements.
	probe := map[string]any{
		"autoshare": false, "share": "disabled",
		"reference": map[string]any{}, "references": map[string]any{},
		"agent": map[string]any{"build": map[string]any{"maxSteps": 20, "steps": 20}},
	}
	want := []string{"/agent/build/maxSteps", "/autoshare", "/reference"}
	if got := schema.deprecatedIn(probe); !slices.Equal(got, want) {
		t.Fatalf("the walker found deprecated properties %q in the probe, want %q: it no longer sees the pinned schema's renames", got, want)
	}

	used := map[string]bool{}
	for _, c := range schemaContractConfigs(t) {
		doc := decodeOpenCodeConfig(t, c.content)
		for _, ptr := range schema.deprecatedIn(doc) {
			if _, ok := schemaContractAllowedDeprecated[ptr]; ok {
				used[ptr] = true
				continue
			}
			t.Errorf("%s: the config sets %s, a property the pinned OpenCode schema marks deprecated", c.name, ptr)
		}
		if _, ok := doc["share"]; !ok {
			t.Errorf("%s: the config does not set share", c.name)
		}
		if _, ok := doc["autoshare"]; ok {
			t.Errorf("%s: the config sets autoshare, which share replaced", c.name)
		}
		if steps := jsonPath(doc, "agent", "build", "steps"); steps == nil {
			t.Errorf("%s: the build agent has no steps cap", c.name)
		}
		if ptrs := keyPointers(doc, "", "maxSteps"); len(ptrs) > 0 {
			t.Errorf("%s: the config sets maxSteps, which steps replaced, at %q", c.name, ptrs)
		}
	}
	for ptr, why := range schemaContractAllowedDeprecated {
		if !used[ptr] {
			t.Errorf("%s is allowed as deprecated (%s), but no generated config sets it as a deprecated property: remove the exception", ptr, why)
		}
	}
}

// schemaContractSecurityKeys are the top-level keys that carry a safety
// setting. A renamed one would be ignored at run time, so each must be a
// property the pinned schema defines. permission is here before the builder
// sets it (#1638): once it does, TestGeneratedConfigsValidate covers its map.
var schemaContractSecurityKeys = []string{"share", "autoupdate", "enabled_providers", "instructions", "mcp", "permission"}

// TestSecurityKeysPresentAndKnown: every generated config sets the keys the
// builder locks, with their locked values: share "disabled", autoupdate
// false, enabled_providers the dispatched provider alone, instructions a list
// of absolute paths (the repository's steering and the run's own steering
// file, never a URL) and mcp. Each security key is a property the pinned
// schema defines at the top level.
func TestSecurityKeysPresentAndKnown(t *testing.T) {
	schema := decodeSchemaDoc(t, schemaContractSchema(t))
	defined := schema.topLevelProperties()
	for _, key := range schemaContractSecurityKeys {
		if !defined[key] {
			t.Errorf("the pinned OpenCode schema does not define the top-level property %q, so the config's %s would be an ignored key", key, key)
		}
	}

	url := regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)
	for _, c := range schemaContractConfigs(t) {
		doc := decodeOpenCodeConfig(t, c.content)
		if got, ok := doc["share"]; !ok || got != "disabled" {
			t.Errorf("%s: share = %v (set: %t), want \"disabled\"", c.name, got, ok)
		}
		if got, ok := doc["autoupdate"]; !ok || got != false {
			t.Errorf("%s: autoupdate = %v (set: %t), want false", c.name, got, ok)
		}
		if got, _ := doc["enabled_providers"].([]any); len(got) != 1 || got[0] != c.provider {
			t.Errorf("%s: enabled_providers = %v, want [%q], the dispatched provider alone", c.name, doc["enabled_providers"], c.provider)
		}
		entries, ok := doc["instructions"].([]any)
		if !ok {
			t.Errorf("%s: instructions = %v, want a list", c.name, doc["instructions"])
		}
		for _, e := range entries {
			path, _ := e.(string)
			if !filepath.IsAbs(path) || url.MatchString(path) {
				t.Errorf("%s: instructions entry %v is not an absolute path", c.name, e)
			}
		}
		if _, ok := doc["mcp"].(map[string]any); !ok {
			t.Errorf("%s: mcp = %v, want an object", c.name, doc["mcp"])
		}
	}
}

// TestManifestSchemaHash: the OpenCode compat manifest's config_schema_sha256
// is the pinned schema's SHA-256, the identity the release canary (#1639)
// compares the live schema against.
func TestManifestSchemaHash(t *testing.T) {
	sum := sha256.Sum256(readPinnedSchema(t))
	got := hex.EncodeToString(sum[:])
	m, ok := adaptercompat.Get("opencode")
	if !ok {
		t.Fatal("no opencode compat manifest")
	}
	if m.ConfigSchemaSHA256 != got {
		t.Errorf("internal/adaptercompat/manifests/opencode.json config_schema_sha256 = %q, but %s hashes to %s: re-pin both together (testdata/opencode-config-schema/README.md)",
			m.ConfigSchemaSHA256, schemaContractSchemaPath, got)
	}
}

// schemaDoc is the pinned schema as generic JSON, for the questions a
// validator does not answer: which properties are deprecated, and which are
// defined.
type schemaDoc struct {
	root map[string]any
}

func decodeSchemaDoc(t *testing.T, raw []byte) schemaDoc {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatalf("the pinned schema is not a JSON object: %v", err)
	}
	return schemaDoc{root: root}
}

// deref is the definition a local $ref ("#/$defs/Name") names, or nil. An
// external $ref, such as models.dev's, defines no property.
func (d schemaDoc) deref(ref string) map[string]any {
	name, ok := strings.CutPrefix(ref, "#/$defs/")
	if !ok {
		return nil
	}
	defs, _ := d.root["$defs"].(map[string]any)
	def, _ := defs[name].(map[string]any)
	return def
}

// expand is s with every schema it applies through a local $ref or an
// allOf, anyOf or oneOf branch, recursively: each one that can define a
// property of the same instance.
func (d schemaDoc) expand(s map[string]any, seen map[string]bool) []map[string]any {
	if s == nil {
		return nil
	}
	out := []map[string]any{s}
	if ref, _ := s["$ref"].(string); ref != "" && !seen[ref] {
		seen[ref] = true
		out = append(out, d.expand(d.deref(ref), seen)...)
	}
	for _, kw := range []string{"allOf", "anyOf", "oneOf"} {
		branches, _ := s[kw].([]any)
		for _, b := range branches {
			bm, _ := b.(map[string]any)
			out = append(out, d.expand(bm, seen)...)
		}
	}
	return out
}

func (d schemaDoc) expandAll(schemas []map[string]any) []map[string]any {
	var out []map[string]any
	for _, s := range schemas {
		out = append(out, d.expand(s, map[string]bool{})...)
	}
	return out
}

// propertySchemas are the subschemas schemas give an object's key: the
// property of that name, or else the additionalProperties schema.
func propertySchemas(schemas []map[string]any, key string) []map[string]any {
	var out []map[string]any
	for _, s := range schemas {
		props, _ := s["properties"].(map[string]any)
		if p, ok := props[key].(map[string]any); ok {
			out = append(out, p)
			continue
		}
		if p, ok := s["additionalProperties"].(map[string]any); ok {
			out = append(out, p)
		}
	}
	return out
}

// schemaDeprecated reports whether a property's schema marks it deprecated:
// the draft 2020-12 deprecated keyword, or the "@deprecated" description the
// pinned schema carries over from its source's doc comments, on the property
// itself or on the definition it $refs.
func (d schemaDoc) schemaDeprecated(s map[string]any) bool {
	for _, c := range []map[string]any{s, d.deref(stringField(s, "$ref"))} {
		if c == nil {
			continue
		}
		if dep, _ := c["deprecated"].(bool); dep {
			return true
		}
		if strings.HasPrefix(strings.TrimSpace(stringField(c, "description")), "@deprecated") {
			return true
		}
	}
	return false
}

func stringField(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

// deprecatedIn is the sorted JSON pointers of every property in doc that the
// schema marks deprecated, walking doc against the root schema.
func (d schemaDoc) deprecatedIn(doc any) []string {
	var found []string
	d.walk(doc, []map[string]any{d.root}, "", &found)
	slices.Sort(found)
	return slices.Compact(found)
}

func (d schemaDoc) walk(v any, schemas []map[string]any, ptr string, found *[]string) {
	schemas = d.expandAll(schemas)
	switch v := v.(type) {
	case map[string]any:
		for key, child := range v {
			p := ptr + "/" + jsonPointerEscape(key)
			props := propertySchemas(schemas, key)
			for _, s := range props {
				if d.schemaDeprecated(s) {
					*found = append(*found, p)
					break
				}
			}
			d.walk(child, props, p, found)
		}
	case []any:
		for i, child := range v {
			var items []map[string]any
			for _, s := range schemas {
				if prefix, _ := s["prefixItems"].([]any); i < len(prefix) {
					if p, ok := prefix[i].(map[string]any); ok {
						items = append(items, p)
					}
				} else if p, ok := s["items"].(map[string]any); ok {
					items = append(items, p)
				}
			}
			d.walk(child, items, fmt.Sprintf("%s/%d", ptr, i), found)
		}
	}
}

// topLevelProperties are the properties the schema's root document, through
// its $ref, defines for a config.
func (d schemaDoc) topLevelProperties() map[string]bool {
	out := map[string]bool{}
	for _, s := range d.expand(d.root, map[string]bool{}) {
		props, _ := s["properties"].(map[string]any)
		for k := range props {
			out[k] = true
		}
	}
	return out
}

// keyPointers is the JSON pointer of every object key named key in v, at
// any depth.
func keyPointers(v any, ptr, key string) []string {
	var out []string
	switch v := v.(type) {
	case map[string]any:
		for k, child := range v {
			p := ptr + "/" + jsonPointerEscape(k)
			if k == key {
				out = append(out, p)
			}
			out = append(out, keyPointers(child, p, key)...)
		}
	case []any:
		for i, child := range v {
			out = append(out, keyPointers(child, fmt.Sprintf("%s/%d", ptr, i), key)...)
		}
	}
	return out
}

func jsonPointerEscape(key string) string {
	return strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
}

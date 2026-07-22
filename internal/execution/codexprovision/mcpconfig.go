// Package codexprovision provisions Codex provider context on the Go-direct
// spawn path (ExecutionManagerRunner), at parity with the TypeScript
// StageExecutor / skillRunner paths:
//
//   - MCP servers: translate the pipeline's .mcp.json into Codex
//     `[mcp_servers.*]` tables inside a managed block in $CODEX_HOME/config.toml
//     (mirrors packages/nightgauge-sdk/src/context/codexMcpConfig.ts, #4025).
//   - AGENTS.md steering: a provider-neutral baseline-steering managed block
//     (mirrors CodexContextGenerator/steeringSources.ts, #4028) — see steering.go.
//
// The TypeScript modules are the reference behavior to mirror: managed-block
// markers, user-wins-on-collision, control-char escaping, and the idempotent /
// non-destructive merge semantics are kept byte-for-byte compatible. #4041
package codexprovision

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Managed-block markers. TOML uses `#` line comments (no HTML comments).
const (
	mcpManagedBegin = "# >>> BEGIN NIGHTGAUGE MANAGED MCP >>>"
	mcpManagedEnd   = "# <<< END NIGHTGAUGE MANAGED MCP <<<"
)

const mcpManagedNotice = "# Managed by the Nightgauge pipeline (issue #4025). Servers inside these\n" +
	"# markers are regenerated from the project's .mcp.json on every Codex stage —\n" +
	"# edits here are overwritten. Define your own [mcp_servers.*] OUTSIDE the block."

// PipelineMcpServer mirrors the Claude-native .mcp.json server shape.
type PipelineMcpServer struct {
	Type    string            `json:"type,omitempty"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Cwd     string            `json:"cwd,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// codexMcpServer is a server normalized to Codex's TOML field names.
type codexMcpServer struct {
	command           string
	args              []string
	env               map[string]string
	cwd               string
	url               string
	bearerTokenEnvVar string
	httpHeaders       map[string]string
}

// ReadPipelineMcpServers reads the pipeline's MCP servers from the same
// Claude-native sources a Claude stage sees: `.mcp.json` (primary) and
// `.claude/settings.json` `mcpServers` (secondary). `.mcp.json` wins on a name
// clash. Malformed/non-string env+header values are coerced (never crash).
func ReadPipelineMcpServers(workspaceRoot string) map[string]PipelineMcpServer {
	merged := map[string]PipelineMcpServer{}
	for k, v := range extractServers(filepath.Join(workspaceRoot, ".claude", "settings.json")) {
		merged[k] = v
	}
	for k, v := range extractServers(filepath.Join(workspaceRoot, ".mcp.json")) {
		merged[k] = v // .mcp.json takes precedence
	}
	return merged
}

// extractServers reads a JSON file and returns its `mcpServers` map, tolerating
// missing files, malformed JSON, and non-string env/header values.
func extractServers(filePath string) map[string]PipelineMcpServer {
	raw, err := os.ReadFile(filePath)
	if err != nil {
		return nil
	}
	// Decode loosely so non-string env/header values (JSON numbers/booleans) are
	// coerced rather than failing the whole parse (#4025 review #2/#8).
	var root struct {
		McpServers map[string]struct {
			Type    string                     `json:"type"`
			Command string                     `json:"command"`
			Args    []json.RawMessage          `json:"args"`
			Env     map[string]json.RawMessage `json:"env"`
			Cwd     string                     `json:"cwd"`
			URL     string                     `json:"url"`
			Headers map[string]json.RawMessage `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil
	}
	out := map[string]PipelineMcpServer{}
	for name, s := range root.McpServers {
		srv := PipelineMcpServer{
			Type:    s.Type,
			Command: s.Command,
			Cwd:     s.Cwd,
			URL:     s.URL,
			Env:     coerceStringMap(s.Env),
			Headers: coerceStringMap(s.Headers),
		}
		for _, a := range s.Args {
			if str, ok := jsonAsString(a); ok {
				srv.Args = append(srv.Args, str)
			}
		}
		out[name] = srv
	}
	return out
}

// jsonAsString returns the string value of a raw JSON token only when it is a
// JSON string (args are string-typed; non-strings are dropped). JSON `null` is
// rejected — `json.Unmarshal(null, &string)` succeeds with "", which would leak
// an empty arg/env value, so it is filtered out explicitly (matches the TS
// `typeof a === "string"` guard).
func jsonAsString(raw json.RawMessage) (string, bool) {
	if strings.TrimSpace(string(raw)) == "null" {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, true
	}
	return "", false
}

// coerceStringMap converts a raw JSON map to string→string, coercing JSON
// numbers/booleans to their string form and dropping nulls.
func coerceStringMap(m map[string]json.RawMessage) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := map[string]string{}
	for k, raw := range m {
		trimmed := strings.TrimSpace(string(raw))
		if trimmed == "null" || trimmed == "" {
			continue
		}
		if s, ok := jsonAsString(raw); ok {
			out[k] = s
			continue
		}
		// Non-string (number/boolean) — emit its raw JSON text (matches String(v)).
		out[k] = trimmed
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

var bearerRe = regexp.MustCompile(`^Bearer\s+\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?$`)

// bearerEnvVar extracts `VAR` from `Bearer ${VAR}` / `Bearer $VAR`, else "".
func bearerEnvVar(authValue string) string {
	if m := bearerRe.FindStringSubmatch(authValue); m != nil {
		return m[1]
	}
	return ""
}

// toCodexMcpServer normalizes one pipeline server into Codex's field names.
// Returns (_, false) when it carries neither a url nor a command.
func toCodexMcpServer(s PipelineMcpServer) (codexMcpServer, bool) {
	isHTTP := s.URL != "" &&
		(s.Type == "" || s.Type == "http" || s.Type == "sse" || s.Command == "")
	if isHTTP {
		out := codexMcpServer{url: s.URL}
		rest := map[string]string{}
		// Deterministic header order so collision/diff output is stable.
		for _, key := range sortedKeys(s.Headers) {
			value := s.Headers[key]
			if strings.EqualFold(key, "authorization") {
				if env := bearerEnvVar(value); env != "" {
					out.bearerTokenEnvVar = env
					continue
				}
			}
			rest[key] = value
		}
		if len(rest) > 0 {
			out.httpHeaders = rest
		}
		return out, true
	}
	if s.Command != "" {
		out := codexMcpServer{command: s.Command}
		if len(s.Args) > 0 {
			out.args = append(out.args, s.Args...)
		}
		if len(s.Env) > 0 {
			out.env = s.Env
		}
		if s.Cwd != "" {
			out.cwd = s.Cwd
		}
		return out, true
	}
	return codexMcpServer{}, false
}

// --- TOML emission (minimal, scoped to the [mcp_servers.*] shapes) ---

// tomlString escapes a string for a TOML basic (double-quoted) string. Raw
// control chars (U+0000–U+001F except the named escapes, plus U+007F) are
// forbidden inside basic strings — a single raw control byte makes config.toml
// unparseable for every Codex stage — so each is emitted as an escape. (#4025 review #1)
func tomlString(value string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range value {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			if r <= 0x1f || r == 0x7f {
				b.WriteString(fmt.Sprintf(`\u%04X`, r))
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

var bareKeyRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// tomlKey emits a bare key for a simple identifier, else a quoted key.
func tomlKey(key string) string {
	if bareKeyRe.MatchString(key) {
		return key
	}
	return tomlString(key)
}

// tomlInlineTable emits `{ K = "v", K2 = "v2" }` with deterministic key order.
func tomlInlineTable(m map[string]string) string {
	keys := sortedKeys(m)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s = %s", tomlKey(k), tomlString(m[k])))
	}
	return "{ " + strings.Join(parts, ", ") + " }"
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// emitServerTable emits the `[mcp_servers.<name>]` table body for one server.
func emitServerTable(name string, s codexMcpServer) string {
	lines := []string{fmt.Sprintf("[mcp_servers.%s]", tomlKey(name))}
	if s.command != "" {
		lines = append(lines, "command = "+tomlString(s.command))
	}
	if len(s.args) > 0 {
		quoted := make([]string, len(s.args))
		for i, a := range s.args {
			quoted[i] = tomlString(a)
		}
		lines = append(lines, "args = ["+strings.Join(quoted, ", ")+"]")
	}
	if len(s.env) > 0 {
		lines = append(lines, "env = "+tomlInlineTable(s.env))
	}
	if s.cwd != "" {
		lines = append(lines, "cwd = "+tomlString(s.cwd))
	}
	if s.url != "" {
		lines = append(lines, "url = "+tomlString(s.url))
	}
	if s.bearerTokenEnvVar != "" {
		lines = append(lines, "bearer_token_env_var = "+tomlString(s.bearerTokenEnvVar))
	}
	if len(s.httpHeaders) > 0 {
		lines = append(lines, "http_headers = "+tomlInlineTable(s.httpHeaders))
	}
	return strings.Join(lines, "\n")
}

// buildManagedMcpBlockInner builds the inner block content (notice + every
// server table), with deterministic ordering → idempotent bytes. Empty servers
// → "".
func buildManagedMcpBlockInner(servers map[string]codexMcpServer) string {
	names := make([]string, 0, len(servers))
	for n := range servers {
		names = append(names, n)
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	parts := []string{mcpManagedNotice}
	for _, n := range names {
		parts = append(parts, emitServerTable(n, servers[n]))
	}
	return strings.Join(parts, "\n\n")
}

// --- Managed-region location — line-anchored & BEGIN-authoritative. ---

func escapeRegexp(s string) string {
	return regexp.QuoteMeta(s)
}

// lineAnchoredIndex returns the byte index of the first occurrence of marker
// that begins a line (after optional indentation) at or after fromIdx, or -1.
func lineAnchoredIndex(text, marker string, fromIdx int) int {
	re := regexp.MustCompile(`(?m)^[ \t]*` + escapeRegexp(marker))
	for _, loc := range re.FindAllStringIndex(text, -1) {
		// loc[0] is the line start (incl. indentation); the marker starts after it.
		markerIdx := loc[1] - len(marker)
		if markerIdx >= fromIdx {
			return markerIdx
		}
	}
	return -1
}

type managedRegion struct {
	start        int
	endExclusive int
}

// locateManagedRegion finds [start, endExclusive). A present BEGIN is
// authoritative: a missing END (truncated/hand-edited block) extends the region
// to EOF so the next write HEALS it.
func locateManagedRegion(text string) (managedRegion, bool) {
	start := lineAnchoredIndex(text, mcpManagedBegin, 0)
	if start == -1 {
		return managedRegion{}, false
	}
	endMarkerIdx := lineAnchoredIndex(text, mcpManagedEnd, start+len(mcpManagedBegin))
	endExclusive := len(text)
	if endMarkerIdx != -1 {
		endExclusive = endMarkerIdx + len(mcpManagedEnd)
	}
	return managedRegion{start: start, endExclusive: endExclusive}, true
}

// withoutManagedBlock returns text with the managed region removed (for scanning
// user content only).
func withoutManagedBlock(existing string) string {
	region, ok := locateManagedRegion(existing)
	if !ok {
		return existing
	}
	return existing[:region.start] + existing[region.endExclusive:]
}

// --- Collision detection: user-defined [mcp_servers.X] OUTSIDE the block. ---

var (
	trailNL = regexp.MustCompile(`\n+$`)
	leadNL  = regexp.MustCompile(`^\n+`)
	// Bracketed table header: `[ mcp_servers . "a\"b" ]` (whitespace-tolerant).
	mcpTableRe = regexp.MustCompile(`(?m)^[ \t]*\[[ \t]*mcp_servers[ \t]*\.[ \t]*(?:"((?:[^"\\]|\\.)*)"|([A-Za-z0-9_-]+))[ \t]*\]`)
	// Dotted-key inline form: `mcp_servers.foo = { ... }`.
	mcpDottedRe = regexp.MustCompile(`(?m)^[ \t]*mcp_servers[ \t]*\.[ \t]*(?:"((?:[^"\\]|\\.)*)"|([A-Za-z0-9_-]+))[ \t]*=`)
	basicEscRe  = regexp.MustCompile(`\\(u[0-9A-Fa-f]{4}|.)`)
)

// unescapeTomlBasic reverses the basic-string escapes tomlString produces.
func unescapeTomlBasic(s string) string {
	return basicEscRe.ReplaceAllStringFunc(s, func(full string) string {
		esc := full[1:]
		if esc[0] == 'u' {
			var code int
			fmt.Sscanf(esc[1:], "%x", &code)
			return string(rune(code))
		}
		switch esc {
		case "n":
			return "\n"
		case "r":
			return "\r"
		case "t":
			return "\t"
		case "b":
			return "\b"
		case "f":
			return "\f"
		case `"`:
			return `"`
		case `\`:
			return `\`
		default:
			return esc
		}
	})
}

// findUserDefinedServerNames returns the names of `[mcp_servers.<name>]` servers
// the user defined OUTSIDE the managed block (table-header or dotted-key form).
// Commented-out lines are ignored (a `#` precedes the bracket/key).
func findUserDefinedServerNames(existing string) map[string]bool {
	outside := withoutManagedBlock(existing)
	names := map[string]bool{}
	for _, re := range []*regexp.Regexp{mcpTableRe, mcpDottedRe} {
		for _, m := range re.FindAllStringSubmatch(outside, -1) {
			var name string
			if m[1] != "" {
				name = unescapeTomlBasic(m[1])
			} else {
				name = m[2]
			}
			if name != "" {
				names[name] = true
			}
		}
	}
	return names
}

// --- Managed-block upsert / strip. ---

// upsertManagedMcpBlock inserts or replaces the managed MCP block, preserving
// everything outside the markers (modulo boundary newline normalization). An
// empty blockInner removes any existing block.
func upsertManagedMcpBlock(existing string, hasExisting bool, blockInner string) string {
	if strings.TrimSpace(blockInner) == "" {
		if !hasExisting {
			return ""
		}
		return stripManagedMcpBlock(existing)
	}

	wrapped := mcpManagedBegin + "\n" + blockInner + "\n" + mcpManagedEnd

	if !hasExisting || strings.TrimSpace(existing) == "" {
		return wrapped + "\n"
	}

	if region, ok := locateManagedRegion(existing); ok {
		before := trailNL.ReplaceAllString(existing[:region.start], "")
		after := leadNL.ReplaceAllString(existing[region.endExclusive:], "")
		switch {
		case before == "" && after == "":
			return wrapped + "\n"
		case before == "":
			return wrapped + "\n\n" + after
		case after == "":
			return before + "\n\n" + wrapped + "\n"
		default:
			return before + "\n\n" + wrapped + "\n\n" + after
		}
	}

	// No block yet — append below the user's content.
	return trailNL.ReplaceAllString(existing, "") + "\n\n" + wrapped + "\n"
}

// stripManagedMcpBlock removes the managed block, preserving user content.
func stripManagedMcpBlock(existing string) string {
	region, ok := locateManagedRegion(existing)
	if !ok {
		return existing
	}
	before := trailNL.ReplaceAllString(existing[:region.start], "")
	after := leadNL.ReplaceAllString(existing[region.endExclusive:], "")
	switch {
	case before == "" && after == "":
		return ""
	case before == "":
		return after
	case after == "":
		return before + "\n"
	default:
		return before + "\n\n" + after
	}
}

// ComputeNextCodexConfig is the pure end-to-end transform: given the existing
// config text (hasExisting=false ≈ no file) and the pipeline servers, return the
// next config text. Servers whose name collides with a user-defined table
// outside the block are skipped (user wins); skipped/provisioned names are
// reported (sorted) for logging.
func ComputeNextCodexConfig(existing string, hasExisting bool, pipelineServers map[string]PipelineMcpServer) (next string, provisioned, skippedCollisions []string) {
	userDefined := map[string]bool{}
	if hasExisting {
		userDefined = findUserDefinedServerNames(existing)
	}

	codexServers := map[string]codexMcpServer{}
	for _, name := range sortedServerNames(pipelineServers) {
		if userDefined[name] {
			skippedCollisions = append(skippedCollisions, name)
			continue
		}
		if normalized, ok := toCodexMcpServer(pipelineServers[name]); ok {
			codexServers[name] = normalized
			provisioned = append(provisioned, name)
		}
	}

	inner := buildManagedMcpBlockInner(codexServers)
	next = upsertManagedMcpBlock(existing, hasExisting, inner)
	sort.Strings(provisioned)
	sort.Strings(skippedCollisions)
	return next, provisioned, skippedCollisions
}

func sortedServerNames(m map[string]PipelineMcpServer) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

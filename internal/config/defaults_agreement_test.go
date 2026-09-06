package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	yaml "gopkg.in/yaml.v3"
)

// A shipped default lives in up to four places: the Go structs and resolvers in
// this package, the extension's DEFAULT_CONFIG (schema.ts), the
// `nightgauge config init` template (init.go), and the reference tables in
// docs/CONFIGURATION.md. Nothing reconciled them, so a default that exists in
// four places was four defaults — the audit of 2026-09-06 found nineteen keys
// where at least two of the four disagreed, including `platform.telemetry`
// documented as opt-out and shipped off, and `pipeline.ci_timeout` shipped as
// 10 in one unit and 300 in another.
//
// TestDefaultsAgree is the thing that was missing. Each row names ONE shipped
// value and every source that is supposed to state it; a source the key
// legitimately does not appear in is left empty and is not checked. Adding a
// default without adding a row here is allowed — this test cannot know about a
// key it was not told about — but changing one of the four sources for a key
// that IS listed fails CI at the moment of the change rather than at the moment
// somebody notices the behaviour.
//
// The three non-Go sources are read as text on purpose. A Go test cannot
// evaluate TypeScript or render the docs, and a generated snapshot of either
// would be a fifth place to drift.

// defaultCase is one shipped default and the sources that must state it.
type defaultCase struct {
	// key is the dotted config key, used only in failure messages.
	key string
	// ship is the canonical shipped value, written the way a human would.
	ship string

	// goVal returns the value this package resolves for an EMPTY config.
	// nil when the key has no Go home (an extension- or skill-only key).
	goVal func() string

	// ts is the dotted path into DEFAULT_CONFIG in schema.ts.
	// Empty when the key is deliberately absent from the extension defaults.
	ts string

	// docsAnchor is the exact heading line in docs/CONFIGURATION.md above the
	// table that documents this key; docsRow is the key column of the row.
	// Empty anchor means the key is deliberately undocumented.
	docsAnchor string
	docsRow    string
	// docsWant overrides `ship` when the docs cell is prose rather than a
	// literal (a value that follows another key, for instance).
	docsWant string

	// initPath is the dotted path into the rendered `config init` template.
	// Empty when the template deliberately does not carry the key.
	initPath string
}

func defaultCases() []defaultCase {
	return []defaultCase{
		{
			key:        "knowledge.enabled",
			ship:       "true",
			goVal:      func() string { return fmt.Sprint((*KnowledgeConfig)(nil).IsEnabled()) },
			ts:         "knowledge.enabled",
			docsAnchor: "## Knowledge Base Configuration", docsRow: "enabled",
			initPath: "knowledge.enabled",
		},
		{
			key:        "knowledge.auto_scaffold",
			ship:       "true",
			goVal:      func() string { return fmt.Sprint((&KnowledgeConfig{}).IsAutoScaffold()) },
			ts:         "knowledge.auto_scaffold",
			docsAnchor: "## Knowledge Base Configuration", docsRow: "auto_scaffold",
			initPath: "knowledge.auto_scaffold",
		},
		{
			key: "knowledge.require_decisions", ship: "true",
			goVal:      func() string { return fmt.Sprint((*KnowledgeConfig)(nil).ResolveRequireDecisions()) },
			ts:         "knowledge.require_decisions",
			docsAnchor: "## Knowledge Base Configuration", docsRow: "require_decisions",
		},
		{
			key: "knowledge.workspace_scoped", ship: "true",
			goVal:      func() string { return fmt.Sprint((*KnowledgeConfig)(nil).IsWorkspaceScoped()) },
			ts:         "knowledge.workspace_scoped",
			docsAnchor: "## Knowledge Base Configuration", docsRow: "workspace_scoped",
		},
		{
			// Follows knowledge.enabled (ADR-005): there is no static value to
			// put in DEFAULT_CONFIG, so the extension carries none and the docs
			// say so in prose. TestKnowledgeTelemetryFollowsEnabled below is
			// what actually pins the behaviour.
			key: "knowledge.telemetry.enabled", ship: "follows knowledge.enabled",
			docsAnchor: "## Knowledge Base Configuration", docsRow: "telemetry.enabled",
			docsWant: "_follows `enabled`_",
		},
		{
			key: "knowledge.auto_index", ship: "true",
			ts:         "knowledge.auto_index",
			docsAnchor: "## Knowledge Base Configuration", docsRow: "auto_index",
		},
		{
			key: "knowledge.auto_prune_on_merge", ship: "true",
			ts:         "knowledge.auto_prune_on_merge",
			docsAnchor: "## Knowledge Base Configuration", docsRow: "auto_prune_on_merge",
		},
		{
			key: "knowledge.aggregate", ship: "false",
			ts:         "knowledge.aggregate",
			docsAnchor: "## Knowledge Base Configuration", docsRow: "aggregate",
		},
		{
			key: "platform.enabled", ship: "false",
			ts:         "platform.enabled",
			docsAnchor: "## Platform Configuration", docsRow: "enabled",
		},
		{
			key: "platform.telemetry.enabled", ship: "true",
			goVal:      func() string { return fmt.Sprint((*TelemetryConfig)(nil).IsEnabled()) },
			ts:         "platform.telemetry.enabled",
			docsAnchor: "## Platform Configuration", docsRow: "telemetry.enabled",
		},
		{
			key: "pull_request.auto_merge", ship: "false",
			ts:         "pull_request.auto_merge",
			docsAnchor: "### pr", docsRow: "auto_merge",
		},
		{
			key: "project.auto_dates", ship: "true",
			ts:         "project.auto_dates",
			docsAnchor: "### project", docsRow: "auto_dates",
		},
		{
			key: "project.sprint.auto_assign", ship: "false",
			ts:         "",
			docsAnchor: "#### project.sprint", docsRow: "auto_assign",
			initPath: "project.sprint.auto_assign",
		},
		{
			key: "issue.auto_assign", ship: "true",
			ts:         "issue.auto_assign",
			docsAnchor: "### issue", docsRow: "auto_assign",
		},
		{
			key: "ui.project_board.default_epic_collapsed", ship: "true",
			ts:         "ui.project_board.default_epic_collapsed",
			docsAnchor: "### ui.project_board", docsRow: "default_epic_collapsed",
		},
		{
			key: "pipeline.ci_timeout", ship: "300",
			ts:         "pipeline.ci_timeout",
			docsAnchor: "### pipeline", docsRow: "ci_timeout",
		},
		{
			key: "pipeline.auto_fix", ship: "true",
			ts:         "pipeline.auto_fix",
			docsAnchor: "### pipeline", docsRow: "auto_fix",
			initPath: "pipeline.auto_fix",
		},
		{
			key: "pipeline.adaptive_budget", ship: "true",
			ts:         "pipeline.adaptive_budget",
			docsAnchor: "### pipeline", docsRow: "adaptive_budget",
		},
		{
			key: "pipeline.max_concurrent", ship: "3",
			goVal:      func() string { return fmt.Sprint(DefaultPipelineMaxConcurrent) },
			ts:         "pipeline.max_concurrent",
			docsAnchor: "#### pipeline.max_concurrent", docsRow: "max_concurrent",
		},
		{
			key: "pipeline.token_budget_ceiling.ceiling_usd", ship: "75",
			goVal:      func() string { return fmt.Sprint((*PipelineConfig)(nil).ResolveTokenBudgetCeilingUSD()) },
			ts:         "pipeline.token_budget_ceiling.ceiling_usd",
			docsAnchor: "#### pipeline.token_budget_ceiling", docsRow: "ceiling_usd",
		},
		{
			key: "model_routing.mode", ship: "automatic",
			goVal:      func() string { return (*ModelRoutingConfig)(nil).ResolveMode() },
			ts:         "model_routing.mode",
			docsAnchor: "### model_routing", docsRow: "mode",
		},
		{
			key: "autonomous.debounce_repos", ship: "true",
			goVal:      func() string { return fmt.Sprint((*AutonomousConfig)(nil).ResolveDebounceRepos()) },
			docsAnchor: "#### autonomous scheduler options", docsRow: "debounce_repos",
		},
		{
			key: "autonomous.safety_rails.budget_ceiling", ship: "500000",
			goVal:      func() string { return fmt.Sprint(ResolveSafetyBudgetCeiling(nil)) },
			docsAnchor: "#### autonomous.safety_rails", docsRow: "budget_ceiling",
		},
		{
			key: "autonomous.safety_rails.health_gate_min", ship: "30",
			goVal:      func() string { return fmt.Sprint(ResolveHealthGateMin(nil)) },
			docsAnchor: "#### autonomous.safety_rails", docsRow: "health_gate_min",
		},
		{
			key: "autonomous.safety_rails.epic_checkpoint", ship: "true",
			goVal:      func() string { return fmt.Sprint(ResolveEpicCheckpoint(nil)) },
			docsAnchor: "#### autonomous.safety_rails", docsRow: "epic_checkpoint",
		},
		{
			key: "sanitization.mode", ship: "warn",
			goVal:    func() string { return string((*SanitizationConfig)(nil).ResolvedMode()) },
			ts:       "sanitization.mode",
			initPath: "sanitization.mode",
		},
		{
			key: "human_in_the_loop.auto_accept_stages", ship: "false",
			ts:       "human_in_the_loop.auto_accept_stages",
			initPath: "human_in_the_loop.auto_accept_stages",
		},
		{
			key: "human_in_the_loop.auto_accept_permissions", ship: "false",
			ts:       "human_in_the_loop.auto_accept_permissions",
			initPath: "human_in_the_loop.auto_accept_permissions",
		},
		{
			key: "pipeline.adversarial_review.enabled", ship: "true",
			goVal:    func() string { return fmt.Sprint(DefaultAdversarialReviewEnabled) },
			initPath: "pipeline.adversarial_review.enabled",
		},
		{
			key: "pipeline.grounding_gate.enabled", ship: "true",
			goVal:    func() string { return fmt.Sprint(DefaultGroundingGateEnabled) },
			initPath: "pipeline.grounding_gate.enabled",
		},
		{
			key: "pipeline.architecture_approval.enabled", ship: "true",
			goVal:    func() string { return fmt.Sprint(DefaultArchitectureApprovalEnabled) },
			initPath: "pipeline.architecture_approval.enabled",
		},
		{
			key: "pipeline.feedback_loop.health_warning_threshold", ship: "70",
			goVal: func() string { return fmt.Sprint((*FeedbackLoopConfig)(nil).ResolveWarningThreshold()) },
			ts:    "pipeline.feedback_loop.health_warning_threshold",
		},
		{
			key: "pipeline.feedback_loop.health_critical_threshold", ship: "50",
			goVal: func() string { return fmt.Sprint((*FeedbackLoopConfig)(nil).ResolveCriticalThreshold()) },
			ts:    "pipeline.feedback_loop.health_critical_threshold",
		},
		{
			key: "pipeline.feedback_loop.health_emergency_threshold", ship: "30",
			goVal: func() string { return fmt.Sprint((*FeedbackLoopConfig)(nil).ResolveEmergencyThreshold()) },
			ts:    "pipeline.feedback_loop.health_emergency_threshold",
		},
		{
			key: "pipeline.feedback_loop.health_actions_enabled", ship: "true",
			goVal: func() string { return fmt.Sprint((*FeedbackLoopConfig)(nil).ResolveActionsEnabled()) },
			ts:    "pipeline.feedback_loop.health_actions_enabled",
		},
		{
			key: "pipeline.feedback_loop.health_policies_enabled", ship: "true",
			goVal: func() string { return fmt.Sprint((*FeedbackLoopConfig)(nil).ResolvePoliciesEnabled()) },
			ts:    "pipeline.feedback_loop.health_policies_enabled",
		},
	}
}

func TestDefaultsAgree(t *testing.T) {
	root := repoRootForDefaults(t)

	tsDefaults := loadTSDefaultConfig(t, root)
	docs := readRepoFile(t, root, filepath.Join("docs", "CONFIGURATION.md"))
	initTemplate := renderInitTemplate(t)

	for _, tc := range defaultCases() {
		t.Run(tc.key, func(t *testing.T) {
			if tc.goVal != nil {
				if got := normalizeDefault(tc.goVal()); got != normalizeDefault(tc.ship) {
					t.Errorf("Go resolves %s to %q, ship value is %q\n"+
						"Fix the resolver in internal/config, or this row if the ship value moved.",
						tc.key, got, tc.ship)
				}
			}
			if tc.ts != "" {
				got, ok := lookupJSPath(tsDefaults, tc.ts)
				if !ok {
					t.Errorf("%s is missing from DEFAULT_CONFIG in schema.ts (expected %q). "+
						"A key the extension validates but never defaults is a key with no default at all.",
						tc.ts, tc.ship)
				} else if normalizeDefault(got) != normalizeDefault(tc.ship) {
					t.Errorf("schema.ts DEFAULT_CONFIG.%s = %q, ship value is %q", tc.ts, got, tc.ship)
				}
			}
			if tc.docsAnchor != "" {
				want := tc.ship
				if tc.docsWant != "" {
					want = tc.docsWant
				}
				got, err := docsDefault(docs, tc.docsAnchor, tc.docsRow)
				if err != nil {
					t.Errorf("docs/CONFIGURATION.md: %v (key %s)", err, tc.key)
				} else if normalizeDefault(got) != normalizeDefault(want) {
					t.Errorf("docs/CONFIGURATION.md table under %q says %s = %q, ship value is %q",
						tc.docsAnchor, tc.docsRow, got, want)
				}
			}
			if tc.initPath != "" {
				got, ok := lookupInitPath(initTemplate, tc.initPath)
				if !ok {
					t.Errorf("`nightgauge config init` template does not emit %s (expected %q)",
						tc.initPath, tc.ship)
				} else if normalizeDefault(got) != normalizeDefault(tc.ship) {
					t.Errorf("`nightgauge config init` template writes %s: %q, ship value is %q",
						tc.initPath, got, tc.ship)
				}
			}
		})
	}
}

// TestKnowledgeTelemetryFollowsEnabled pins the one default in the table above
// that is not a value: knowledge.telemetry.enabled has no static default
// because it follows its parent in both directions.
func TestKnowledgeTelemetryFollowsEnabled(t *testing.T) {
	on, off := true, false

	if got := (&KnowledgeConfig{Enabled: &on}).IsTelemetryEnabled(); !got {
		t.Error("KB on, telemetry unset: want telemetry on (it follows the parent)")
	}
	if got := (&KnowledgeConfig{Enabled: &off}).IsTelemetryEnabled(); got {
		t.Error("KB off, telemetry unset: want telemetry off")
	}
	if got := (&KnowledgeConfig{
		Enabled:   &off,
		Telemetry: &KnowledgeTelemetryConfig{Enabled: &on},
	}).IsTelemetryEnabled(); got {
		t.Error("KB off with telemetry explicitly on: want off — ADR-005 makes the parent authoritative")
	}
}

// ---------------------------------------------------------------------------
// Source readers
// ---------------------------------------------------------------------------

func repoRootForDefaults(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Skipf("could not resolve repo root: %v", err)
	}
	return root
}

func readRepoFile(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Skipf("%s unreadable (%v) — skipping agreement check", rel, err)
	}
	return string(b)
}

func renderInitTemplate(t *testing.T) map[string]any {
	t.Helper()
	out, err := BuildTemplate(InitOptions{Owner: "nightgauge", Repo: "nightgauge"})
	if err != nil {
		t.Fatalf("BuildTemplate: %v", err)
	}
	var m map[string]any
	if err := yaml.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("the `config init` template does not parse as YAML: %v", err)
	}
	return m
}

func lookupInitPath(m map[string]any, path string) (string, bool) {
	var cur any = m
	for _, seg := range strings.Split(path, ".") {
		node, ok := cur.(map[string]any)
		if !ok {
			return "", false
		}
		cur, ok = node[seg]
		if !ok {
			return "", false
		}
	}
	return fmt.Sprint(cur), true
}

// ---------------------------------------------------------------------------
// docs/CONFIGURATION.md
// ---------------------------------------------------------------------------

// docsDefault returns the Default cell of the row whose key column is `row`,
// in the FIRST markdown table below the exact heading line `anchor`.
//
// "First table below the heading" rather than "anywhere in the section",
// because leaf key names repeat: `max_concurrent` documents three unrelated
// keys and `enabled` documents dozens. The anchor is what makes the lookup
// unambiguous, and a moved heading fails loudly here instead of silently
// matching some other section's row.
func docsDefault(docs, anchor, row string) (string, error) {
	lines := strings.Split(docs, "\n")
	start := -1
	for i, ln := range lines {
		if strings.TrimRight(ln, " \t") == anchor {
			if start >= 0 {
				return "", fmt.Errorf("heading %q appears more than once; it cannot anchor a table", anchor)
			}
			start = i
		}
	}
	if start < 0 {
		return "", fmt.Errorf("heading %q not found", anchor)
	}

	inTable := false
	for i := start + 1; i < len(lines); i++ {
		ln := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(ln, "|") {
			if inTable {
				break // the first table below the anchor has ended
			}
			continue
		}
		inTable = true
		cells := splitTableRow(ln)
		if len(cells) < 3 {
			continue
		}
		if normalizeKey(cells[0]) == row {
			return cells[2], nil
		}
	}
	if inTable {
		return "", fmt.Errorf("no row %q in the first table below %q", row, anchor)
	}
	return "", fmt.Errorf("no table below heading %q", anchor)
}

func splitTableRow(ln string) []string {
	ln = strings.TrimPrefix(ln, "|")
	ln = strings.TrimSuffix(ln, "|")
	parts := strings.Split(ln, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// normalizeKey strips markdown decoration from a table's key column.
func normalizeKey(v string) string {
	return strings.Trim(strings.TrimSpace(v), "`")
}

// normalizeDefault strips the decoration each source applies to the same value
// — markdown backticks, TS/YAML quoting, Go's float formatting — so `"true"`,
// `true` and true compare equal.
func normalizeDefault(v string) string {
	v = strings.TrimSpace(v)
	v = strings.Trim(v, "`")
	v = strings.TrimSpace(v)
	v = strings.Trim(v, `"'`)
	v = strings.TrimSpace(v)
	if isNumericLiteral(v) {
		v = strings.ReplaceAll(v, "_", "") // TS numeric separators: 500_000
	}
	if strings.Contains(v, ".") {
		// 75 == 75.0 == 75.00
		trimmed := strings.TrimRight(v, "0")
		trimmed = strings.TrimSuffix(trimmed, ".")
		if trimmed != "" && !strings.ContainsAny(trimmed, " ") {
			allNum := true
			for _, r := range trimmed {
				if (r < '0' || r > '9') && r != '.' && r != '-' {
					allNum = false
					break
				}
			}
			if allNum {
				v = trimmed
			}
		}
	}
	return v
}

// ---------------------------------------------------------------------------
// schema.ts DEFAULT_CONFIG
// ---------------------------------------------------------------------------

// jsNode is one node of the parsed DEFAULT_CONFIG literal: either a nested
// object or a raw leaf (the source text of the value, comments stripped).
type jsNode struct {
	raw string
	obj map[string]*jsNode
}

func loadTSDefaultConfig(t *testing.T, root string) map[string]*jsNode {
	t.Helper()
	rel := filepath.Join("packages", "nightgauge-vscode", "src", "config", "schema.ts")
	src := readRepoFile(t, root, rel)

	const marker = "export const DEFAULT_CONFIG"
	idx := strings.Index(src, marker)
	if idx < 0 {
		t.Skipf("DEFAULT_CONFIG not found in %s — this pin is path-coupled; if the constant moved, move the pin", rel)
	}
	brace := strings.Index(src[idx:], "{")
	if brace < 0 {
		t.Fatalf("DEFAULT_CONFIG in %s has no object literal", rel)
	}
	obj, _, err := parseJSObject(src, idx+brace)
	if err != nil {
		t.Fatalf("parsing DEFAULT_CONFIG in %s: %v", rel, err)
	}
	return obj
}

func lookupJSPath(root map[string]*jsNode, path string) (string, bool) {
	cur := root
	segs := strings.Split(path, ".")
	for i, seg := range segs {
		node, ok := cur[seg]
		if !ok {
			return "", false
		}
		if i == len(segs)-1 {
			if node.obj != nil {
				return "", false // an object where a scalar was expected
			}
			return node.raw, true
		}
		if node.obj == nil {
			return "", false
		}
		cur = node.obj
	}
	return "", false
}

// parseJSObject reads the object literal starting at src[i] == '{' and returns
// its members plus the index just past the closing brace. It understands only
// as much JavaScript as DEFAULT_CONFIG uses: nested object literals, quoted and
// bare keys, line and block comments, string literals, and arrays (kept raw).
func parseJSObject(src string, i int) (map[string]*jsNode, int, error) {
	if i >= len(src) || src[i] != '{' {
		return nil, 0, fmt.Errorf("expected '{' at offset %d", i)
	}
	out := map[string]*jsNode{}
	i++
	for {
		var err error
		i, err = skipJSFiller(src, i)
		if err != nil {
			return nil, 0, err
		}
		if i >= len(src) {
			return nil, 0, fmt.Errorf("unterminated object literal")
		}
		if src[i] == '}' {
			return out, i + 1, nil
		}
		if src[i] == ',' {
			i++
			continue
		}

		key, next, err := readJSKey(src, i)
		if err != nil {
			return nil, 0, err
		}
		i = next
		if i, err = skipJSFiller(src, i); err != nil {
			return nil, 0, err
		}
		if i >= len(src) || src[i] != ':' {
			return nil, 0, fmt.Errorf("expected ':' after key %q", key)
		}
		i++
		if i, err = skipJSFiller(src, i); err != nil {
			return nil, 0, err
		}
		if i >= len(src) {
			return nil, 0, fmt.Errorf("value missing for key %q", key)
		}
		if src[i] == '{' {
			child, next, err := parseJSObject(src, i)
			if err != nil {
				return nil, 0, err
			}
			out[key] = &jsNode{obj: child}
			i = next
			continue
		}
		raw, next, err := readJSValue(src, i)
		if err != nil {
			return nil, 0, err
		}
		out[key] = &jsNode{raw: raw}
		i = next
	}
}

func readJSKey(src string, i int) (string, int, error) {
	if src[i] == '"' || src[i] == '\'' {
		quote := src[i]
		j := i + 1
		for j < len(src) && src[j] != quote {
			if src[j] == '\\' {
				j++
			}
			j++
		}
		if j >= len(src) {
			return "", 0, fmt.Errorf("unterminated key string at offset %d", i)
		}
		return src[i+1 : j], j + 1, nil
	}
	j := i
	for j < len(src) && (isIdentRune(src[j])) {
		j++
	}
	if j == i {
		return "", 0, fmt.Errorf("expected an object key at offset %d, saw %q", i, src[i])
	}
	return src[i:j], j, nil
}

func isIdentRune(c byte) bool {
	return c == '_' || c == '$' ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// readJSValue captures a scalar or array value verbatim, stopping at the comma
// or closing brace that ends it at this nesting level.
func readJSValue(src string, i int) (string, int, error) {
	depth := 0
	start := i
	for i < len(src) {
		c := src[i]
		switch {
		case c == '"' || c == '\'' || c == '`':
			j := i + 1
			for j < len(src) && src[j] != c {
				if src[j] == '\\' {
					j++
				}
				j++
			}
			if j >= len(src) {
				return "", 0, fmt.Errorf("unterminated string at offset %d", i)
			}
			i = j + 1
			continue
		case c == '[' || c == '{' || c == '(':
			depth++
		case c == ']' || c == ')':
			depth--
		case c == '}':
			if depth == 0 {
				return strings.TrimSpace(src[start:i]), i, nil
			}
			depth--
		case c == ',':
			if depth == 0 {
				return strings.TrimSpace(src[start:i]), i + 1, nil
			}
		case c == '/' && i+1 < len(src) && (src[i+1] == '/' || src[i+1] == '*'):
			// A comment can only follow a complete value here.
			return strings.TrimSpace(src[start:i]), i, nil
		}
		i++
	}
	return "", 0, fmt.Errorf("unterminated value starting at offset %d", start)
}

// skipJSFiller advances past whitespace and both comment forms.
func skipJSFiller(src string, i int) (int, error) {
	for i < len(src) {
		c := src[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			i++
			continue
		}
		if c == '/' && i+1 < len(src) {
			if src[i+1] == '/' {
				for i < len(src) && src[i] != '\n' {
					i++
				}
				continue
			}
			if src[i+1] == '*' {
				end := strings.Index(src[i+2:], "*/")
				if end < 0 {
					return 0, fmt.Errorf("unterminated block comment at offset %d", i)
				}
				i += 2 + end + 2
				continue
			}
		}
		return i, nil
	}
	return i, nil
}

// isNumericLiteral reports whether v is a number, allowing the TypeScript
// digit separator. Guarding the separator strip on this matters: applied
// unconditionally it also flattens every snake_case key name.
func isNumericLiteral(v string) bool {
	if v == "" {
		return false
	}
	seen := false
	for _, r := range v {
		switch {
		case r >= '0' && r <= '9':
			seen = true
		case r == '_' || r == '.' || r == '-' || r == '+':
		default:
			return false
		}
	}
	return seen
}

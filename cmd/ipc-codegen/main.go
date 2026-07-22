// cmd/ipc-codegen generates IpcClient.generated.ts from Go IPC annotations.
//
// It reads //ipc:method annotations from server.go and struct definitions from
// protocol.go, then produces a TypeScript class extending IpcClientBase with
// typed methods for each annotated IPC handler.
//
// Usage:
//
//	go run ./cmd/ipc-codegen \
//	  --server internal/ipc/server.go \
//	  --protocol internal/ipc/protocol.go \
//	  --out packages/nightgauge-vscode/src/services/IpcClient.generated.ts
package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log"
	"os"
	"regexp"
	"sort"
	"strings"
	"text/template"
	"unicode"
)

// ProtocolVersion is embedded in the generated TypeScript file header.
// Bump this when the IPC contract changes incompatibly.
const ProtocolVersion = 1

// MethodDef represents a parsed //ipc:method annotation.
type MethodDef struct {
	TSName     string  // camelCase TypeScript method name
	GoMethod   string  // dot-separated Go method name (e.g., "board.list")
	ParamsType string  // Go struct name or "none"
	ResultType string  // TypeScript result type string
	Nullable   bool    // result ?? [] coercion
	UnwrapKey  string  // unwrap a specific field from the result object
	Skip       bool    // skip code generation (manual implementation)
	Fields     []Field // populated from protocol.go struct parsing
}

// Field represents a struct field from protocol.go.
type Field struct {
	GoName   string
	JSONName string
	GoType   string
	TSType   string
	Optional bool // from omitempty tag
}

var (
	serverPath   = flag.String("server", "internal/ipc/server.go", "Path to server.go")
	protocolPath = flag.String("protocol", "internal/ipc/protocol.go", "Path to protocol.go")
	outPath      = flag.String("out", "packages/nightgauge-vscode/src/services/IpcClient.generated.ts", "Output TypeScript file path")
)

func main() {
	flag.Parse()

	// 1. Parse annotations from server.go
	methods, err := parseAnnotations(*serverPath)
	if err != nil {
		log.Fatalf("parse annotations: %v", err)
	}

	// 2. Parse struct definitions from protocol.go
	structs, err := parseStructs(*protocolPath)
	if err != nil {
		log.Fatalf("parse structs: %v", err)
	}

	// 3. Resolve fields for each method
	for i := range methods {
		if methods[i].ParamsType != "none" && methods[i].ParamsType != "" {
			fields, ok := structs[methods[i].ParamsType]
			if ok {
				methods[i].Fields = fields
			}
		}
	}

	// 4. Generate TypeScript
	output, err := generateTypeScript(methods)
	if err != nil {
		log.Fatalf("generate TypeScript: %v", err)
	}

	// 5. Write output
	if err := os.WriteFile(*outPath, output, 0644); err != nil {
		log.Fatalf("write output: %v", err)
	}

	fmt.Printf("Generated %s (%d methods, %d skipped)\n", *outPath,
		countGenerated(methods), countSkipped(methods))
}

// annotationRe matches //ipc:method lines.
// Format: //ipc:method <tsName> params:<GoType> result:<TSType> [nullable] [unwrap:<field>] [skip]
var annotationRe = regexp.MustCompile(`^//ipc:method\s+(\S+)\s+params:(\S+)\s+result:(\S+)(.*)$`)

// methodRe extracts the Go method name from s.methods["name"] lines.
var methodRe = regexp.MustCompile(`s\.methods\["([^"]+)"\]`)

func parseAnnotations(path string) ([]MethodDef, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var methods []MethodDef
	var pending *MethodDef

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Check for annotation
		if strings.HasPrefix(line, "//ipc:method") {
			m := annotationRe.FindStringSubmatch(line)
			if m == nil {
				// Simple skip annotation: //ipc:method skip
				if strings.Contains(line, "skip") {
					pending = &MethodDef{Skip: true}
					continue
				}
				log.Printf("WARNING: malformed annotation: %s", line)
				continue
			}

			md := MethodDef{
				TSName:     m[1],
				ParamsType: m[2],
				ResultType: m[3],
			}
			modifiers := m[4]
			md.Nullable = strings.Contains(modifiers, "nullable")
			md.Skip = strings.Contains(modifiers, "skip")
			if idx := strings.Index(modifiers, "unwrap:"); idx >= 0 {
				rest := modifiers[idx+7:]
				md.UnwrapKey = strings.Fields(rest)[0]
			}
			pending = &md
			continue
		}

		// Check for method registration after an annotation
		if pending != nil {
			if mm := methodRe.FindStringSubmatch(line); mm != nil {
				pending.GoMethod = mm[1]
				methods = append(methods, *pending)
				pending = nil
			}
		}
	}

	return methods, scanner.Err()
}

func parseStructs(path string) (map[string][]Field, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return nil, err
	}

	result := make(map[string][]Field)

	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				continue
			}

			var fields []Field
			for _, f := range st.Fields.List {
				if len(f.Names) == 0 {
					continue // embedded field
				}

				goType := exprToString(f.Type)
				jsonName, optional := parseJSONTag(f.Tag)
				if jsonName == "" {
					jsonName = f.Names[0].Name
				}

				fields = append(fields, Field{
					GoName:   f.Names[0].Name,
					JSONName: jsonName,
					GoType:   goType,
					TSType:   goTypeToTS(goType),
					Optional: optional,
				})
			}
			result[ts.Name.Name] = fields
		}
	}

	return result, nil
}

func parseJSONTag(tag *ast.BasicLit) (string, bool) {
	if tag == nil {
		return "", false
	}
	raw := tag.Value
	// Remove backticks
	raw = strings.Trim(raw, "`")
	// Find json:"..."
	idx := strings.Index(raw, `json:"`)
	if idx == -1 {
		return "", false
	}
	rest := raw[idx+6:]
	end := strings.Index(rest, `"`)
	if end == -1 {
		return "", false
	}
	value := rest[:end]
	if value == "-" {
		return "", false
	}
	parts := strings.Split(value, ",")
	name := parts[0]
	optional := false
	for _, p := range parts[1:] {
		if p == "omitempty" {
			optional = true
		}
	}
	return name, optional
}

func exprToString(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.ArrayType:
		return "[]" + exprToString(t.Elt)
	case *ast.StarExpr:
		return "*" + exprToString(t.X)
	case *ast.SelectorExpr:
		return exprToString(t.X) + "." + t.Sel.Name
	case *ast.StructType:
		return "struct{}"
	case *ast.InterfaceType:
		return "interface{}"
	case *ast.MapType:
		return "map[" + exprToString(t.Key) + "]" + exprToString(t.Value)
	default:
		return "unknown"
	}
}

func goTypeToTS(goType string) string {
	switch goType {
	case "string":
		return "string"
	case "int", "int32", "int64", "float64", "float32":
		return "number"
	case "bool":
		return "boolean"
	case "[]string":
		return "string[]"
	case "[]int":
		return "number[]"
	case "interface{}":
		return "unknown"
	default:
		if strings.HasPrefix(goType, "[]") {
			inner := goTypeToTS(goType[2:])
			return inner + "[]"
		}
		return "unknown"
	}
}

// toCamelCase converts a JSON field name to camelCase (it usually already is).
func toCamelCase(s string) string {
	if s == "" {
		return s
	}
	runes := []rune(s)
	runes[0] = unicode.ToLower(runes[0])
	return string(runes)
}

func countGenerated(methods []MethodDef) int {
	n := 0
	for _, m := range methods {
		if !m.Skip {
			n++
		}
	}
	return n
}

func countSkipped(methods []MethodDef) int {
	n := 0
	for _, m := range methods {
		if m.Skip {
			n++
		}
	}
	return n
}

// collectImportTypes extracts all type names referenced in result types and
// fields that need to be imported from IpcClientBase.
func collectImportTypes(methods []MethodDef) []string {
	typeSet := make(map[string]bool)
	knownImports := map[string]bool{
		"AuthTokenResponse": true, "AuthDeviceCodeResult": true, "AuthDeviceFlowPollResult": true,
		"BoardItem": true, "IssueDetail": true, "EpicProgress": true,
		"PipelineStatus": true, "ExecutionInfo": true, "ComplexityResult": true,
		"ModelRouteResult": true, "FailureClassification": true, "CostEstimate": true,
		"BatchAssessment": true, "PlatformStatus": true, "LicenseInfo": true,
		"TrialResult":   true, // Issue #1138 — Start Free Trial
		"IpcQueueState": true, "IpcQueueItem": true, "RunPipelineResult": true,
		"HealthAnalysis": true, "PullRequestDetail": true, "GitStatusResult": true,
		"GitLogEntry": true, "StatusCounts": true,
		"ConfigGetProjectResult": true, "ConfigGetHealthThresholdsResult": true,
		"CachedSkill": true, "StatusOK": true, "UsageSummaryResult": true,
		"TeamMemberResult": true, "PortalSessionResult": true, "HealthResponse": true,
		"WorkspaceSetRootResult": true, "WorkspaceRegisterRepoResult": true,
		"ConfigureForgeInstanceResult":    true, // Issue #3365
		"ForgeListResult":                 true, // Issue #3364
		"ForgeConnectionTestResult":       true, // Issue #3364
		"NotificationsReloadTokensResult": true,
		"PlatformSyncTelemetryResult":     true,
		"RemoteGetCommandHistoryResult":   true, "RemotePollingStatus": true,
		"AutonomousStatusResult":             true,
		"AutonomousClearIssueFailuresResult": true,
		"AutonomousClearQuotaCooldownResult": true,
		"StuckEpicsResult":                   true, // Issue #4073
		"EpicContextResult":                  true,
		"FocusShowResult":                    true,
		"FocusListResult":                    true,
		"PipelineMaxConcurrentResult":        true,
		"RateLimitInfo":                      true,
		"GitCleanupMergedBranchesResult":     true,
		"GitHubAuthCheckResult":              true,
		"CancelActiveForNetworkOutageResult": true, // Issue #3296
		"CostAnalyticsResult":                true, // Issue #3317
		"AnalyticsHealthResult":              true, // Issue #3318
		"AnalyticsRunsResult":                true, // Issue #3319
		"AnalyticsTrendsResult":              true, // Issue #3320
		"ComplianceReportResult":             true, // Issue #3322
		"ComplianceReportsPage":              true, // Issue #3322
		"ComplianceReportDetail":             true, // Issue #3322
		"CheckAuthorizationResult":           true, // Issue #3377
		"RetentionConfig":                    true, // Issue #3323
		"IntegrityResult":                    true, // Issue #3323
		"KnowledgeMetricsResult":             true, // Issue #3600
		"KnowledgeSearchResult":              true, // Issue #2964
		"KnowledgeBacklinksResult":           true, // Issue #2964
		"KnowledgeRelatedToIssueResult":      true, // Issue #2964
		"RecordStageExitResult":              true, // Issue #3619
		"AgentAcknowledgeCommandResult":      true, // Issue #3551
		"ConfigTierAuditResult":              true, // Issue #3645
		"WorkflowQuotaStateResult":           true, // Issue #3909
		"AttentionListResult":                true, // ADR 015 — Action Center
		"AttentionResolveResult":             true, // ADR 015
		"AttentionAcknowledgeResult":         true, // ADR 015
	}

	for _, m := range methods {
		if m.Skip {
			continue
		}
		// Extract type names from result type
		rt := m.ResultType
		rt = strings.TrimSuffix(rt, "[]")
		rt = strings.TrimSuffix(rt, " | null")
		if knownImports[rt] {
			typeSet[rt] = true
		}
	}

	var types []string
	for t := range typeSet {
		types = append(types, t)
	}
	sort.Strings(types)
	return types
}

// groupMethodsByCategory returns methods grouped by category prefix.
func groupMethodsByCategory(methods []MethodDef) map[string][]MethodDef {
	groups := make(map[string][]MethodDef)
	for _, m := range methods {
		if m.Skip {
			continue
		}
		parts := strings.SplitN(m.GoMethod, ".", 2)
		category := parts[0]
		groups[category] = append(groups[category], m)
	}
	return groups
}

func generateTypeScript(methods []MethodDef) ([]byte, error) {
	imports := collectImportTypes(methods)
	groups := groupMethodsByCategory(methods)

	// Stable category order
	var categories []string
	categoryOrder := []string{"config", "board", "issue", "pr", "epic", "pipeline", "execution", "queue", "intelligence", "platform", "project", "git"}
	for _, c := range categoryOrder {
		if _, ok := groups[c]; ok {
			categories = append(categories, c)
		}
	}
	// Any remaining categories not in the predefined order (sorted for determinism)
	var remaining []string
	for c := range groups {
		found := false
		for _, co := range categoryOrder {
			if c == co {
				found = true
				break
			}
		}
		if !found {
			remaining = append(remaining, c)
		}
	}
	sort.Strings(remaining)
	categories = append(categories, remaining...)

	var buf bytes.Buffer
	data := struct {
		ProtocolVersion int
		Imports         []string
		Categories      []string
		Groups          map[string][]MethodDef
	}{
		ProtocolVersion: ProtocolVersion,
		Imports:         imports,
		Categories:      categories,
		Groups:          groups,
	}

	funcMap := template.FuncMap{
		"renderMethod": renderMethod,
		"titleCase": func(s string) string {
			if s == "" {
				return s
			}
			return strings.ToUpper(s[:1]) + s[1:]
		},
		"repeatDash": func(n int) string {
			return strings.Repeat("-", n)
		},
	}

	tmpl, err := template.New("generated").Funcs(funcMap).Parse(tsTemplate)
	if err != nil {
		return nil, fmt.Errorf("parse template: %w", err)
	}
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("execute template: %w", err)
	}

	return buf.Bytes(), nil
}

func renderMethod(m MethodDef) string {
	var buf bytes.Buffer

	// Build parameter list
	var params []string
	if m.ParamsType != "none" && len(m.Fields) > 0 {
		for _, f := range m.Fields {
			opt := ""
			if f.Optional {
				opt = "?"
			}
			params = append(params, fmt.Sprintf("%s%s: %s", f.JSONName, opt, f.TSType))
		}
	}
	paramStr := strings.Join(params, ", ")

	// Build return type
	isVoid := m.ResultType == "void"
	returnType := m.ResultType
	if m.UnwrapKey != "" {
		// The actual call type is the wrapper object, but we return the unwrapped field type
		returnType = m.ResultType
	}

	// Determine the call type (what this.call<T> uses)
	callType := returnType
	if m.Nullable {
		callType = returnType + " | null"
	}
	if m.UnwrapKey != "" {
		// We call with the wrapper type and unwrap
		callType = fmt.Sprintf("{ %s: %s }", m.UnwrapKey, m.ResultType)
	}

	// Promise return type
	promiseType := returnType
	if isVoid {
		promiseType = "void"
	}

	// Method signature
	fmt.Fprintf(&buf, "  async %s(%s): Promise<%s> {\n", m.TSName, paramStr, promiseType)

	// Build the params object for the call
	hasParams := m.ParamsType != "none" && len(m.Fields) > 0
	callParams := ""
	if hasParams {
		var fieldNames []string
		for _, f := range m.Fields {
			fieldNames = append(fieldNames, f.JSONName)
		}
		callParams = fmt.Sprintf(", { %s }", strings.Join(fieldNames, ", "))
	}

	// Method body
	if isVoid {
		fmt.Fprintf(&buf, "    await this.call<void>('%s'%s);\n", m.GoMethod, callParams)
	} else if m.UnwrapKey != "" {
		if m.Nullable {
			fmt.Fprintf(&buf, "    const result = await this.call<%s>('%s'%s);\n", callType, m.GoMethod, callParams)
			fmt.Fprintf(&buf, "    return result?.%s ?? [];\n", m.UnwrapKey)
		} else {
			fmt.Fprintf(&buf, "    const result = await this.call<%s>('%s'%s);\n", callType, m.GoMethod, callParams)
			fmt.Fprintf(&buf, "    return result.%s;\n", m.UnwrapKey)
		}
	} else if m.Nullable {
		fmt.Fprintf(&buf, "    const result = await this.call<%s>('%s'%s);\n", callType, m.GoMethod, callParams)
		fmt.Fprintf(&buf, "    return result ?? [];\n")
	} else {
		fmt.Fprintf(&buf, "    return this.call<%s>('%s'%s);\n", callType, m.GoMethod, callParams)
	}

	fmt.Fprintf(&buf, "  }")
	return buf.String()
}

const tsTemplate = `/**
 * IpcClient.generated.ts — Auto-generated typed API methods for the Go IPC backend.
 *
 * DO NOT EDIT THIS FILE MANUALLY.
 * Generated by: go run ./cmd/ipc-codegen
 * Source files:  internal/ipc/server.go + internal/ipc/protocol.go
 * Protocol version: {{ .ProtocolVersion }}
 *
 * Regenerate: make generate-ipc-client
 *
 * @see internal/ipc/server.go   — Go handler registrations (source of truth)
 * @see internal/ipc/protocol.go — Go param struct definitions
 * @see IpcClientBase.ts          — Base class with lifecycle and transport
 * @see IpcClient.ts              — Final class with singleton and manual wrappers
 */

import { IpcClientBase } from './IpcClientBase';
{{- if .Imports }}
import type {
{{- range .Imports }}
  {{ . }},
{{- end }}
} from './IpcClientBase';
{{- end }}

/** Protocol version matching the Go binary. Bump when IPC contract changes. */
export const IPC_PROTOCOL_VERSION = {{ .ProtocolVersion }};

export class IpcClientGenerated extends IpcClientBase {
{{- range $i, $cat := .Categories }}
{{- $methods := index $.Groups $cat }}

  // {{ repeatDash 73 }}
  // {{ titleCase $cat }}
  // {{ repeatDash 73 }}
{{ range $j, $m := $methods }}
{{ renderMethod $m }}
{{ end }}
{{- end }}
}
`

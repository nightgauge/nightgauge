// Platform raw-HTTP linter (#750).
//
// internal/platform must reach the platform through exactly two doors: the
// oapi-codegen client in api/generated/go/platform, or Client.newRequest,
// which builds every request from the operation contract in
// api/platform-operations.yaml and enforces the credential requirement the
// contract declares.
//
// A hand-rolled `http.NewRequestWithContext(ctx, m, s.client.base+"/v1/...")`
// is a third door, and it is how the auth mismatch in nightgauge/nightgauge#741
// survived: twenty-odd endpoints each re-implemented URL construction and the
// Authorization header, and nothing could check any of them against a
// contract. This gate fails the build when that shape reappears.
//
// Schema version 1 — field names (v, root, files_checked, findings, warnings)
// are stable and consumed by callers via fixed jq paths.
package preflight

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// PlatformRawHTTPResult is the stable JSON output schema for
// `nightgauge preflight platform-raw-http`.
type PlatformRawHTTPResult struct {
	V            int                      `json:"v"`             // schema version, always 1
	Root         string                   `json:"root"`          // absolute path
	Dir          string                   `json:"dir"`           // scanned directory, relative to Root
	FilesChecked int                      `json:"files_checked"` // non-test .go files inspected
	Findings     []PlatformRawHTTPFinding `json:"findings"`      // one entry per offending expression
	Warnings     []string                 `json:"warnings"`      // non-fatal issues (parse errors, etc.)
}

// PlatformRawHTTPFinding describes a single offending expression.
type PlatformRawHTTPFinding struct {
	File   string `json:"file"`   // path relative to Root
	Line   int    `json:"line"`   // 1-based line number
	Kind   string `json:"kind"`   // "raw_request" | "base_url"
	Match  string `json:"match"`  // the offending expression, rendered
	Detail string `json:"detail"` // what to do instead
}

// Finding kinds.
const (
	// PlatformRawHTTPKindRawRequest: an http.NewRequest / http.NewRequestWithContext
	// call outside the sanctioned constructor.
	PlatformRawHTTPKindRawRequest = "raw_request"

	// PlatformRawHTTPKindBaseURL: a read of the platform Client's private base
	// field outside the sanctioned constructor. Concatenating a path onto it is
	// the other half of a hand-rolled call, and catching it stops the gate from
	// being sidestepped by building the URL in one function and the request in
	// another.
	PlatformRawHTTPKindBaseURL = "base_url"
)

// platformRawHTTPDefaultDir is the package this gate governs.
const platformRawHTTPDefaultDir = "internal/platform"

// platformRawHTTPSanctionedFile is the one file allowed to construct a request
// against the platform base URL. Everything else in the package must call
// Client.newRequest, which lives here.
const platformRawHTTPSanctionedFile = "request.go"

// PlatformRawHTTPOptions controls a single linter run.
type PlatformRawHTTPOptions struct {
	// Root is the repository root. When empty, the caller's CWD is used.
	Root string
	// Dir is the directory to scan, relative to Root. Defaults to
	// internal/platform.
	Dir string
}

// RunPlatformRawHTTPCheck parses every non-test .go file under Dir and emits a
// finding for each raw platform request construction.
//
// Returns a non-error result even when findings exist — the caller inspects
// len(result.Findings) to decide the gate exit code. A directory that does not
// exist is a hard error: a silently-empty scan is indistinguishable from a
// clean one, and this gate is load-bearing.
func RunPlatformRawHTTPCheck(_ context.Context, opts PlatformRawHTTPOptions) (*PlatformRawHTTPResult, error) {
	root := opts.Root
	if root == "" {
		var err error
		root, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("resolve root: %w", err)
		}
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve root: %w", err)
	}
	if info, statErr := os.Stat(abs); statErr != nil || !info.IsDir() {
		return nil, fmt.Errorf("root %q is not a readable directory", root)
	}
	root = abs

	dir := opts.Dir
	if dir == "" {
		dir = platformRawHTTPDefaultDir
	}
	scanDir := filepath.Join(root, filepath.FromSlash(dir))
	info, err := os.Stat(scanDir)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("scan directory %q is not a readable directory", dir)
	}

	result := &PlatformRawHTTPResult{
		V:        1,
		Root:     root,
		Dir:      filepath.ToSlash(dir),
		Findings: []PlatformRawHTTPFinding{},
		Warnings: []string{},
	}

	entries, err := os.ReadDir(scanDir)
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", dir, err)
	}

	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(scanDir, name)
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("%s: parse: %v", name, parseErr))
			continue
		}
		result.FilesChecked++
		if name == platformRawHTTPSanctionedFile {
			continue
		}
		rel := filepath.ToSlash(filepath.Join(result.Dir, name))
		result.Findings = append(result.Findings, inspectPlatformFile(fset, file, rel)...)
	}

	sort.Slice(result.Findings, func(i, j int) bool {
		if result.Findings[i].File != result.Findings[j].File {
			return result.Findings[i].File < result.Findings[j].File
		}
		return result.Findings[i].Line < result.Findings[j].Line
	})
	return result, nil
}

// inspectPlatformFile walks one parsed file for the two offending shapes.
func inspectPlatformFile(fset *token.FileSet, file *ast.File, rel string) []PlatformRawHTTPFinding {
	var findings []PlatformRawHTTPFinding

	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pos := fset.Position(sel.Pos())

		// http.NewRequest / http.NewRequestWithContext
		if pkg, isIdent := sel.X.(*ast.Ident); isIdent && pkg.Name == "http" &&
			strings.HasPrefix(sel.Sel.Name, "NewRequest") {
			findings = append(findings, PlatformRawHTTPFinding{
				File:   rel,
				Line:   pos.Line,
				Kind:   PlatformRawHTTPKindRawRequest,
				Match:  "http." + sel.Sel.Name,
				Detail: "build the request with Client.newRequest and an operation from api/platform-operations.yaml",
			})
			return true
		}

		// <expr>.base — reading the platform Client's base URL.
		if sel.Sel.Name == "base" {
			findings = append(findings, PlatformRawHTTPFinding{
				File:   rel,
				Line:   pos.Line,
				Kind:   PlatformRawHTTPKindBaseURL,
				Match:  renderSelector(sel),
				Detail: "the base URL is owned by Client.newRequest; declare the path in api/platform-operations.yaml instead",
			})
		}
		return true
	})

	return findings
}

// renderSelector renders a selector chain like `s.client.base` for the report.
// Anything it cannot render falls back to ".base", which is still enough to
// find the line.
func renderSelector(sel *ast.SelectorExpr) string {
	var parts []string
	var walk func(ast.Expr) bool
	walk = func(e ast.Expr) bool {
		switch v := e.(type) {
		case *ast.Ident:
			parts = append(parts, v.Name)
			return true
		case *ast.SelectorExpr:
			if !walk(v.X) {
				return false
			}
			parts = append(parts, v.Sel.Name)
			return true
		default:
			return false
		}
	}
	if !walk(sel) {
		return ".base"
	}
	return strings.Join(parts, ".")
}

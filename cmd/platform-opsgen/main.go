// Command platform-opsgen compiles api/platform-operations.yaml into
// api/generated/go/platform/operations.gen.go (#750).
//
// The generated artifact is what internal/platform builds every request from,
// and — because each entry carries the operation's credential requirement —
// what the contract conformance test in internal/platform checks call sites
// against. Regeneration is deterministic: `make generate-platform-operations`
// followed by `git diff --exit-code` is the drift gate, mirroring the
// generated-IPC-client check in scripts/ci-local.sh.
//
// Usage:
//
//	platform-opsgen --in api/platform-operations.yaml \
//	                --out api/generated/go/platform/operations.gen.go
package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/format"
	"os"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// manifest mirrors api/platform-operations.yaml.
type manifest struct {
	Version    int                 `yaml:"version"`
	Operations []manifestOperation `yaml:"operations"`
}

type manifestOperation struct {
	ID       string `yaml:"id"`
	GoName   string `yaml:"go_name"`
	Method   string `yaml:"method"`
	Path     string `yaml:"path"`
	Security string `yaml:"security"`
	Upstream string `yaml:"upstream"`
}

var (
	goNameRE      = regexp.MustCompile(`^Op[A-Z][A-Za-z0-9]*$`)
	pathParamRE   = regexp.MustCompile(`\{([A-Za-z][A-Za-z0-9]*)\}`)
	validMethods  = map[string]bool{"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true}
	validSecurity = map[string]string{
		"user_jwt": "SecurityUserJWT",
		"pipeline": "SecurityPipeline",
	}
	validUpstream = map[string]bool{"declared": true, "undeclared": true}
)

func main() {
	in := flag.String("in", "api/platform-operations.yaml", "path to the operation manifest")
	out := flag.String("out", "api/generated/go/platform/operations.gen.go", "path to the generated Go file")
	flag.Parse()

	src, err := os.ReadFile(*in)
	if err != nil {
		fail("read manifest: %v", err)
	}

	var m manifest
	dec := yaml.NewDecoder(bytes.NewReader(src))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		fail("parse %s: %v", *in, err)
	}
	if m.Version != 1 {
		fail("parse %s: unsupported version %d (want 1)", *in, m.Version)
	}
	if len(m.Operations) == 0 {
		fail("parse %s: no operations declared", *in)
	}
	if err := validate(m.Operations); err != nil {
		fail("validate %s: %v", *in, err)
	}

	rendered, err := render(*in, sortForRender(m.Operations))
	if err != nil {
		fail("render: %v", err)
	}
	if err := os.WriteFile(*out, rendered, 0o644); err != nil {
		fail("write %s: %v", *out, err)
	}
}

// sortForRender orders entries by Go name so the generated file is a pure
// function of the manifest's content and not of the order its entries happen
// to be written in. The CI drift check compares bytes.
func sortForRender(ops []manifestOperation) []manifestOperation {
	out := append([]manifestOperation(nil), ops...)
	sort.Slice(out, func(i, j int) bool { return out[i].GoName < out[j].GoName })
	return out
}

func validate(ops []manifestOperation) error {
	seenID := map[string]bool{}
	seenGo := map[string]bool{}
	seenRoute := map[string]string{}
	for i, op := range ops {
		where := fmt.Sprintf("operations[%d]", i)
		if op.ID == "" {
			return fmt.Errorf("%s: id is required", where)
		}
		if seenID[op.ID] {
			return fmt.Errorf("%s: duplicate id %q", where, op.ID)
		}
		seenID[op.ID] = true

		if !goNameRE.MatchString(op.GoName) {
			return fmt.Errorf("%s (%s): go_name %q must match %s", where, op.ID, op.GoName, goNameRE)
		}
		if seenGo[op.GoName] {
			return fmt.Errorf("%s (%s): duplicate go_name %q", where, op.ID, op.GoName)
		}
		seenGo[op.GoName] = true

		if !validMethods[op.Method] {
			return fmt.Errorf("%s (%s): method %q is not a supported HTTP method", where, op.ID, op.Method)
		}
		if !strings.HasPrefix(op.Path, "/") {
			return fmt.Errorf("%s (%s): path %q must be absolute", where, op.ID, op.Path)
		}
		if strings.Contains(op.Path, "?") {
			return fmt.Errorf("%s (%s): path %q must not carry a query string; query params are supplied at the call site", where, op.ID, op.Path)
		}
		// A stray `{` or `}` outside a well-formed placeholder means the
		// substitution count in newRequest would silently disagree with the
		// call site, so reject it here instead.
		if got, want := strings.Count(op.Path, "{"), len(pathParamRE.FindAllString(op.Path, -1)); got != want {
			return fmt.Errorf("%s (%s): path %q has a malformed placeholder", where, op.ID, op.Path)
		}
		if strings.Count(op.Path, "}") != strings.Count(op.Path, "{") {
			return fmt.Errorf("%s (%s): path %q has unbalanced braces", where, op.ID, op.Path)
		}

		route := op.Method + " " + op.Path
		if prev, dup := seenRoute[route]; dup {
			return fmt.Errorf("%s (%s): route %q already declared by %q", where, op.ID, route, prev)
		}
		seenRoute[route] = op.ID

		if _, ok := validSecurity[op.Security]; !ok {
			return fmt.Errorf("%s (%s): security %q must be one of user_jwt, pipeline", where, op.ID, op.Security)
		}
		if !validUpstream[op.Upstream] {
			return fmt.Errorf("%s (%s): upstream %q must be one of declared, undeclared", where, op.ID, op.Upstream)
		}
	}
	return nil
}

func render(source string, ops []manifestOperation) ([]byte, error) {
	var b strings.Builder

	fmt.Fprintf(&b, `// Code generated by cmd/platform-opsgen from %s. DO NOT EDIT.

package platform

// SecurityRequirement is the credential class a platform operation demands.
//
// The platform OpenAPI document cannot express this: it declares a single
// bearerAuth scheme ("License key or JWT session token") for nearly every
// operation, so its own security requirement is satisfied by any bearer. The
// values here are transcribed from the platform's route middleware, which is
// where the distinction actually lives. See %s for the full rationale.
type SecurityRequirement string

const (
	// SecurityUserJWT marks an operation behind the platform's jwtMiddleware:
	// it authorises a *user*, and answers 401 to an account-scoped license key
	// no matter how valid that key is.
	SecurityUserJWT SecurityRequirement = "user_jwt"

	// SecurityPipeline marks an operation behind the platform's pipelineAuth:
	// either a user JWT or a license key is accepted.
	SecurityPipeline SecurityRequirement = "pipeline"
)

// UpstreamStatus records whether the platform OpenAPI document declares the
// operation's path and method.
type UpstreamStatus string

const (
	// UpstreamDeclared: the document declares this path+method.
	UpstreamDeclared UpstreamStatus = "declared"

	// UpstreamUndeclared: this binary calls the route but the document does
	// not declare it. Every such entry is a live drift finding.
	UpstreamUndeclared UpstreamStatus = "undeclared"
)

// Operation is one platform HTTP operation this binary invokes.
type Operation struct {
	// ID matches the platform OpenAPI operationId where one exists, so the
	// two artifacts can be reconciled mechanically.
	ID string

	// Method is the HTTP method.
	Method string

	// Path is a template rooted at the platform base URL. %s placeholders
	// are substituted positionally and percent-escaped by the caller.
	Path string

	// Security is the credential class the operation demands.
	Security SecurityRequirement

	// Upstream records whether the OpenAPI document declares this route.
	Upstream UpstreamStatus
}

// PathParams returns the number of {placeholder} segments in Path.
func (o Operation) PathParams() int {
	n := 0
	for i := 0; i < len(o.Path); i++ {
		if o.Path[i] == '{' {
			n++
		}
	}
	return n
}

// String renders the operation as "METHOD /path (id)" for error messages.
func (o Operation) String() string { return o.Method + " " + o.Path + " (" + o.ID + ")" }

`, source, source, "`{name}`")

	for _, op := range ops {
		fmt.Fprintf(&b, "// %s is %s %s.\nvar %s = Operation{\n\tID:       %q,\n\tMethod:   %q,\n\tPath:     %q,\n\tSecurity: %s,\n\tUpstream: %s,\n}\n\n",
			op.GoName, op.Method, op.Path, op.GoName,
			op.ID, op.Method, op.Path,
			validSecurity[op.Security], upstreamConst(op.Upstream))
	}

	b.WriteString("// Operations is every declared platform operation, ordered by Go name.\n" +
		"// The contract conformance test in internal/platform iterates this slice, so\n" +
		"// an operation added to the manifest cannot escape the credential check.\n" +
		"var Operations = []Operation{\n")
	for _, op := range ops {
		fmt.Fprintf(&b, "\t%s,\n", op.GoName)
	}
	b.WriteString("}\n\n")

	b.WriteString(`// OperationByID looks an operation up by its contract id.
func OperationByID(id string) (Operation, bool) {
	for _, op := range Operations {
		if op.ID == id {
			return op, true
		}
	}
	return Operation{}, false
}
`)

	formatted, err := format.Source([]byte(b.String()))
	if err != nil {
		return nil, fmt.Errorf("gofmt generated source: %w", err)
	}
	return formatted, nil
}

func upstreamConst(v string) string {
	if v == "declared" {
		return "UpstreamDeclared"
	}
	return "UpstreamUndeclared"
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "platform-opsgen: "+format+"\n", args...)
	os.Exit(1)
}

package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	api "github.com/nightgauge/nightgauge/api/generated/go/platform"
)

// ErrCredentialInsufficient is returned before a request leaves the process
// when the credential the client holds cannot satisfy the operation's declared
// security requirement (#750).
//
// It exists for exactly one shape of failure, the one that motivated the
// contract work: an account-scoped license key presented to a user-scoped
// route. The platform answers those 401, and the 401 used to surface far from
// the cause — as an empty analytics panel, or as a confident sentence in a
// webview describing numbers nobody ever fetched. Failing here names the cause.
//
// Note the deliberate narrowness in credentialKindOf: only a credential
// recognisable *as* a license key trips this. An opaque or absent credential
// is passed through and the platform decides, because guessing would turn a
// diagnosable 401 into a false local refusal.
var ErrCredentialInsufficient = errors.New("credential insufficient for operation")

// credentialKind classifies the bearer the client would present.
type credentialKind string

const (
	// credentialNone: no credential configured at all.
	credentialNone credentialKind = "none"

	// credentialUserJWT: a user-scoped session JWT. Satisfies everything.
	credentialUserJWT credentialKind = "user_jwt"

	// credentialLicenseKey: an account-scoped license key (ib_live_*, ib_ci_*).
	// Satisfies pipelineAuth routes only.
	credentialLicenseKey credentialKind = "license_key"

	// credentialOpaque: non-empty but of no recognised shape. Treated as
	// possibly-sufficient; the platform is the judge.
	credentialOpaque credentialKind = "opaque"
)

// licenseKeyPrefixes are the account-scoped key shapes the platform issues.
// The security scheme description in the platform's OpenAPI document names
// exactly these two ("License key (ib_live_*, ib_ci_*) or JWT session token").
var licenseKeyPrefixes = []string{"ib_live_", "ib_ci_"}

// credentialKindOf classifies a bearer string by shape.
//
// A JWT is three base64url segments separated by dots; a license key carries
// no dots and starts with a known prefix. Anything else — a test fixture, a
// future key shape, an opaque token — is credentialOpaque and is never
// refused locally.
func credentialKindOf(bearer string) credentialKind {
	bearer = strings.TrimSpace(bearer)
	if bearer == "" {
		return credentialNone
	}
	for _, p := range licenseKeyPrefixes {
		if strings.HasPrefix(bearer, p) {
			return credentialLicenseKey
		}
	}
	if strings.Count(bearer, ".") == 2 {
		return credentialUserJWT
	}
	return credentialOpaque
}

// satisfies reports whether a credential of this kind can satisfy req.
//
// Only the license-key/user-JWT pair is a definite mismatch. `none` and
// `opaque` pass through so behaviour is unchanged for every credential shape
// this binary cannot positively identify.
func (k credentialKind) satisfies(req api.SecurityRequirement) bool {
	if req != api.SecurityUserJWT {
		return true
	}
	return k != credentialLicenseKey
}

// CredentialSatisfies reports whether the credential currently installed on
// the client can satisfy op's declared security requirement. Callers that want
// to explain a gap to a user (rather than surface an error) can consult this
// before issuing the request.
func (c *Client) CredentialSatisfies(op api.Operation) bool {
	return credentialKindOf(c.bearer()).satisfies(op.Security)
}

// requestSpec describes one platform call in terms of the generated contract.
//
// Nothing here has a default: Headers is written out verbatim by each call
// site so migrating a hand-rolled request onto newRequest cannot quietly add
// or drop an Accept or Content-Type header.
type requestSpec struct {
	// Op is the generated contract entry. Supplies method, path and the
	// security requirement enforced below.
	Op api.Operation

	// PathArgs fills Op.Path's {placeholder} segments, in order. Each value
	// is percent-escaped as a path segment.
	PathArgs []string

	// Query is appended as the URL query string when non-empty.
	Query url.Values

	// Body is the request body, or nil for no body. A nil Body produces a
	// request with a nil io.Reader, matching http.NewRequestWithContext's
	// no-body form exactly.
	Body []byte

	// Headers are set verbatim on the request, before Authorization.
	Headers map[string]string
}

// newRequest is the only way this package builds a request against the
// platform base URL.
//
// It owns three things that were previously re-implemented at every call site:
// URL construction from the contract's path template, the Authorization header
// (always from bearer(), the single credential source established in #742),
// and the credential/security check above.
//
// internal/preflight's platform-raw-http check fails the build when a raw
// http.NewRequest* targeting the platform base URL reappears, so this function
// stays the only door.
func (c *Client) newRequest(ctx context.Context, spec requestSpec) (*http.Request, error) {
	op := spec.Op
	if op.Method == "" || op.Path == "" {
		return nil, fmt.Errorf("platform request: operation is not from the generated contract")
	}
	if want, got := op.PathParams(), len(spec.PathArgs); want != got {
		return nil, fmt.Errorf("platform request %s: path expects %d argument(s), got %d", op, want, got)
	}

	bearer := c.bearer()
	if kind := credentialKindOf(bearer); !kind.satisfies(op.Security) {
		return nil, fmt.Errorf("platform request %s: %w: holding a %s, operation requires a user-scoped session token",
			op, ErrCredentialInsufficient, kind)
	}

	target := c.base + expandPath(op.Path, spec.PathArgs)
	if len(spec.Query) > 0 {
		target += "?" + spec.Query.Encode()
	}

	var body io.Reader
	if spec.Body != nil {
		body = bytes.NewReader(spec.Body)
	}

	req, err := http.NewRequestWithContext(ctx, op.Method, target, body)
	if err != nil {
		return nil, err
	}
	for k, v := range spec.Headers {
		req.Header.Set(k, v)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	return req, nil
}

// expandPath substitutes {placeholder} segments positionally, percent-escaping
// each value as a path segment. The count is validated by the caller.
func expandPath(tmpl string, args []string) string {
	if len(args) == 0 {
		return tmpl
	}
	var out strings.Builder
	i, next := 0, 0
	for i < len(tmpl) {
		open := strings.IndexByte(tmpl[i:], '{')
		if open < 0 {
			out.WriteString(tmpl[i:])
			break
		}
		out.WriteString(tmpl[i : i+open])
		closeAt := strings.IndexByte(tmpl[i+open:], '}')
		if closeAt < 0 {
			// Validated by the generator; unreachable in practice.
			out.WriteString(tmpl[i+open:])
			break
		}
		out.WriteString(url.PathEscape(args[next]))
		next++
		i += open + closeAt + 1
	}
	return out.String()
}

// platformErrorBody is the envelope every 4xx/5xx from the platform API
// carries — `makeErrorBody(code, message, requestId, details)` in
// `packages/api/src/utils/api-error.ts`. `details.fields` is populated for a
// VALIDATION_ERROR with the Zod issues, each naming the path it rejected.
type platformErrorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Details struct {
			Fields []struct {
				Path    []json.RawMessage `json:"path"`
				Message string            `json:"message"`
			} `json:"fields"`
		} `json:"details"`
	} `json:"error"`
}

// errorResponseBodyLimit caps how much of an error body is read. Enough for
// the envelope and its field list; a runaway body cannot be pulled into an
// error string.
const errorResponseBodyLimit = 8 << 10

// describeErrorResponse renders a failed platform response as the text of a Go
// error: always the status, plus whatever the platform said about WHY.
//
// The status alone is what this package used to report, and #821 is what that
// costs. Generating a compliance report sent a lowercase reportType and bare
// calendar dates into a route validating `z.enum(['SOC2','ISO27001'])` and
// `z.string().datetime()`, so every attempt answered 422 — and the dashboard
// rendered "The platform rejected this request (HTTP 422)" with no hint of
// which of the four fields was wrong. The route had already named them all in
// `details.fields`; nothing read it.
//
// The response body is consumed by this call. A body that is not the platform
// envelope (an empty 502 from a proxy, HTML from a load balancer) degrades to
// the bare status rather than quoting bytes of unknown provenance.
func describeErrorResponse(resp *http.Response) string {
	status := fmt.Sprintf("server returned %d", resp.StatusCode)

	raw, err := io.ReadAll(io.LimitReader(resp.Body, errorResponseBodyLimit))
	if err != nil || len(raw) == 0 {
		return status
	}
	var parsed platformErrorBody
	if err := json.Unmarshal(raw, &parsed); err != nil || parsed.Error.Code == "" {
		return status
	}

	detail := parsed.Error.Code
	if parsed.Error.Message != "" {
		detail += ": " + parsed.Error.Message
	}
	if fields := describeErrorFields(parsed); fields != "" {
		detail += " (" + fields + ")"
	}
	return status + ": " + detail
}

// describeErrorFields flattens the Zod issue list into "path: message" pairs.
// Path segments are raw JSON because Zod mixes property names and array
// indices in one array; both render the way they appear on the wire, minus
// the quotes on strings.
func describeErrorFields(parsed platformErrorBody) string {
	var pairs []string
	for _, f := range parsed.Error.Details.Fields {
		var segments []string
		for _, seg := range f.Path {
			var name string
			if err := json.Unmarshal(seg, &name); err == nil {
				segments = append(segments, name)
				continue
			}
			segments = append(segments, string(seg))
		}
		path := strings.Join(segments, ".")
		switch {
		case path != "" && f.Message != "":
			pairs = append(pairs, path+": "+f.Message)
		case f.Message != "":
			pairs = append(pairs, f.Message)
		case path != "":
			pairs = append(pairs, path)
		}
	}
	return strings.Join(pairs, "; ")
}

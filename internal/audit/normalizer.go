package audit

import (
	"regexp"
	"strings"
)

var (
	// paramColon matches :param style (Express/Hono)
	paramColon = regexp.MustCompile(`:[a-zA-Z_][a-zA-Z0-9_]*`)
	// paramBrace matches {param} style (OpenAPI/Flutter)
	paramBrace = regexp.MustCompile(`\{[a-zA-Z_][a-zA-Z0-9_]*\}`)
	// versionPrefix matches /v1, /v2, /v3 prefix segments
	versionPrefix = regexp.MustCompile(`^(/api)/v\d+(/|$)`)
)

// NormalizePath converts a path to a comparable form:
//   - Converts :id → {id}
//   - Lowercases the path
//   - Ensures a leading slash
//   - Removes trailing slash
//
// NOTE: Version prefixes (/v1, /v2) are intentionally NOT stripped here.
// Version differences are detected as PATH_MISMATCH via PathSimilarity.
func NormalizePath(path string) string {
	if path == "" {
		return path
	}

	// Ensure leading slash
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	// Lowercase
	path = strings.ToLower(path)

	// Normalize :param → {param}
	path = paramColon.ReplaceAllStringFunc(path, func(m string) string {
		return "{" + m[1:] + "}"
	})

	// Remove trailing slash unless root
	if len(path) > 1 && strings.HasSuffix(path, "/") {
		path = path[:len(path)-1]
	}

	return path
}

// stripVersionPrefix removes a /v1, /v2, etc. segment after /api.
func stripVersionPrefix(path string) string {
	return versionPrefix.ReplaceAllStringFunc(path, func(m string) string {
		after := versionPrefix.FindStringSubmatch(path)
		if after == nil {
			return m
		}
		suffix := after[2] // "/" or ""
		return "/api" + suffix
	})
}

// NormalizeMethod uppercases an HTTP method.
func NormalizeMethod(method string) string {
	return strings.ToUpper(method)
}

// PathsMatch returns true if two normalized paths are equivalent.
// It compares with all params replaced by a wildcard placeholder.
func PathsMatch(a, b string) bool {
	return wildcardPath(NormalizePath(a)) == wildcardPath(NormalizePath(b))
}

// wildcardPath replaces all {param} placeholders with a fixed token for comparison.
func wildcardPath(path string) string {
	return paramBrace.ReplaceAllString(path, "{*}")
}

// PathSimilarity returns true if the paths refer to the same resource but with
// a detectable variation (e.g., singular/plural, version prefix).
func PathSimilarity(client, canonical string) (isSimilar bool, detail string) {
	nc := NormalizePath(client)
	ncan := NormalizePath(canonical)

	if wildcardPath(nc) == wildcardPath(ncan) {
		return true, ""
	}

	// Check singular/plural variation: /team vs /teams
	wcClient := wildcardPath(nc)
	wcCanon := wildcardPath(ncan)
	if strings.Replace(wcClient, "/teams", "/team", -1) == wcCanon ||
		strings.Replace(wcCanon, "/teams", "/team", -1) == wcClient {
		return true, "singular/plural variation"
	}

	// Check version-only difference (client had /v1 prefix, canonical didn't)
	if versionPrefix.MatchString(client) && !versionPrefix.MatchString(canonical) {
		stripped := stripVersionPrefix(NormalizePath(client))
		if wildcardPath(stripped) == wildcardPath(ncan) {
			return true, "version prefix mismatch"
		}
	}

	return false, ""
}

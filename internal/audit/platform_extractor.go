package audit

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	// platformRouteRe matches .get/.post/.put/.delete/.patch("path") in TypeScript
	platformRouteRe = regexp.MustCompile(`\.(get|post|put|delete|patch)\(\s*['"]([^'"]+)['"]`)
	// authRequiredRe matches common auth middleware patterns
	authRequiredRe = regexp.MustCompile(`authRequired|authMiddleware|requireAuth|@Auth\(|bearerAuth|jwtAuth`)
	// apiKeyRe matches API key auth patterns
	apiKeyRe = regexp.MustCompile(`apiKey|api_key|x-api-key`)
)

// ExtractPlatformRoutes scans platform route TypeScript files and returns
// a map of normalized_path+method → CanonicalEndpoint.
func ExtractPlatformRoutes(platformRepo string) (map[string]CanonicalEndpoint, bool) {
	routesDir := filepath.Join(platformRepo, "packages", "api", "src", "routes")
	if _, err := os.Stat(routesDir); err != nil {
		return nil, false
	}

	result := make(map[string]CanonicalEndpoint)

	err := filepath.Walk(routesDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".ts") {
			return nil
		}
		endpoints := parsePlatformFile(path)
		for _, ep := range endpoints {
			key := NormalizeMethod(ep.Method) + ":" + NormalizePath(ep.Path)
			result[key] = ep
		}
		return nil
	})
	if err != nil {
		return nil, false
	}

	return result, true
}

func parsePlatformFile(path string) []CanonicalEndpoint {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var endpoints []CanonicalEndpoint
	scanner := bufio.NewScanner(f)
	lineNum := 0
	// Track auth context: scan up to 5 lines before/after for auth middleware
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}

	for i, line := range lines {
		lineNum = i + 1
		matches := platformRouteRe.FindStringSubmatch(line)
		if matches == nil {
			continue
		}
		method := strings.ToUpper(matches[1])
		rawPath := matches[2]
		if !isValidPath(rawPath) {
			continue
		}

		// Determine auth type by scanning surrounding context (±5 lines)
		authType := detectAuthContext(lines, i, 5)

		endpoints = append(endpoints, CanonicalEndpoint{
			Path:       rawPath,
			Method:     method,
			AuthType:   authType,
			SourceFile: path,
			LineNumber: lineNum,
		})
	}

	return endpoints
}

func detectAuthContext(lines []string, idx, window int) string {
	start := idx - window
	if start < 0 {
		start = 0
	}
	end := idx + window
	if end >= len(lines) {
		end = len(lines) - 1
	}

	for i := start; i <= end; i++ {
		l := lines[i]
		if authRequiredRe.MatchString(l) {
			return "bearer"
		}
		if apiKeyRe.MatchString(l) {
			return "api_key"
		}
	}
	return "none"
}

func isValidPath(path string) bool {
	if path == "" {
		return false
	}
	if !strings.HasPrefix(path, "/") {
		return false
	}
	// Reject obviously non-path strings
	if strings.Contains(path, " ") || strings.Contains(path, "\n") {
		return false
	}
	return true
}

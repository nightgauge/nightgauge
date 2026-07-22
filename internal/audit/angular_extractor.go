package audit

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	// angularHttpRe matches this.http.get/post/put/delete/patch<...>('url' or "url" or `url`)
	angularHttpRe = regexp.MustCompile("this\\.http\\.(get|post|put|delete|patch)(?:<[^>]*)?>?\\(\\s*[`'\"]([^'\"` \t\n]+)[`'\"]")
	// angularAuthHeaderRe detects Authorization header in options
	angularAuthHeaderRe = regexp.MustCompile(`(?i)Authorization|Bearer|getToken|authHeader|httpHeaders`)
	// angularApiKeyRe detects API key usage
	angularApiKeyRe = regexp.MustCompile(`(?i)x-api-key|apiKey|api_key`)
	// templateLiteralRe detects template literal interpolation like ${...}
	templateLiteralRe = regexp.MustCompile(`\$\{[^}]+\}`)
	// angularBaseURLRe matches environment.apiUrl or similar base URL variable
	angularBaseURLVarRe = regexp.MustCompile(`(?:environment|this\.api|apiUrl|baseUrl|BASE_URL)[^'"` + "`" + `]*`)
)

// ExtractAngularEndpoints scans Angular service TypeScript files and returns
// a slice of ClientEndpoints.
func ExtractAngularEndpoints(angularRepo string) ([]ClientEndpoint, bool) {
	apiDir := filepath.Join(angularRepo, "src", "app", "core", "api")
	if _, err := os.Stat(apiDir); err != nil {
		return nil, false
	}

	var endpoints []ClientEndpoint
	err := filepath.Walk(apiDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".service.ts") {
			return nil
		}
		eps := parseAngularFile(path)
		endpoints = append(endpoints, eps...)
		return nil
	})
	if err != nil {
		return nil, false
	}

	return endpoints, true
}

func parseAngularFile(path string) []ClientEndpoint {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}

	var endpoints []ClientEndpoint
	client := "angular"

	for i, line := range lines {
		lineNum := i + 1

		matches := angularHttpRe.FindStringSubmatch(line)
		if matches == nil {
			continue
		}

		method := strings.ToUpper(matches[1])
		rawURL := matches[2]

		// Strip base URL variable prefix (e.g., `${environment.apiUrl}`)
		rawURL = angularBaseURLVarRe.ReplaceAllString(rawURL, "")
		rawURL = templateLiteralRe.ReplaceAllString(rawURL, "{param}")

		// Must look like an API path
		if !looksLikeAPIPath(rawURL) {
			continue
		}

		approximate := strings.Contains(rawURL, "{param}")

		authType := detectAngularAuth(lines, i, 5)

		ln := lineNum
		endpoints = append(endpoints, ClientEndpoint{
			Client:      client,
			Path:        rawURL,
			Method:      method,
			AuthType:    authType,
			SourceFile:  path,
			LineNumber:  ln,
			Approximate: approximate,
		})
	}

	return endpoints
}

func detectAngularAuth(lines []string, idx, window int) string {
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
		if angularAuthHeaderRe.MatchString(l) {
			return "bearer"
		}
		if angularApiKeyRe.MatchString(l) {
			return "api_key"
		}
	}
	return "none"
}

func looksLikeAPIPath(url string) bool {
	if url == "" {
		return false
	}
	// Must start with / or contain /api/
	if strings.HasPrefix(url, "/") {
		return true
	}
	if strings.Contains(url, "/api/") {
		// Strip up to /api/
		idx := strings.Index(url, "/api/")
		_ = url[idx:]
		return true
	}
	return false
}

// stripBaseURL removes scheme+host prefix, returning just the path.
func stripBaseURL(url string) string {
	// Remove http(s)://host part
	if idx := strings.Index(url, "://"); idx >= 0 {
		rest := url[idx+3:]
		slashIdx := strings.Index(rest, "/")
		if slashIdx >= 0 {
			return rest[slashIdx:]
		}
	}
	return url
}

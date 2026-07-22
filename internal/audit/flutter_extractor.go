package audit

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	// flutterDioRe matches _dio.get/post/put/delete/patch('url') in Dart
	flutterDioRe = regexp.MustCompile(`_dio\.(get|post|put|delete|patch)\(\s*['"]([^'"]+)['"]`)
	// flutterAuthRe detects auth header patterns in Dart
	flutterAuthRe = regexp.MustCompile(`(?i)Authorization|Bearer|getAuthHeaders?|authToken|accessToken`)
	// flutterApiKeyRe detects API key patterns in Dart
	flutterApiKeyRe = regexp.MustCompile(`(?i)x-api-key|apiKey|api_key`)
	// dartTemplateRe detects Dart string interpolation $var or ${expr}
	dartTemplateRe = regexp.MustCompile(`\$\{?[a-zA-Z_][a-zA-Z0-9_.]*\}?`)
)

// ExtractFlutterEndpoints scans Flutter Dio API client files and returns
// a slice of ClientEndpoints.
func ExtractFlutterEndpoints(flutterRepo string) ([]ClientEndpoint, bool) {
	apiDir := filepath.Join(flutterRepo, "lib", "core", "network", "api")
	if _, err := os.Stat(apiDir); err != nil {
		return nil, false
	}

	var endpoints []ClientEndpoint
	err := filepath.Walk(apiDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, "_api.dart") {
			return nil
		}
		eps := parseFlutterDioFile(path)
		endpoints = append(endpoints, eps...)
		return nil
	})
	if err != nil {
		return nil, false
	}

	return endpoints, true
}

// ExtractFlutterDirectGitHubCalls scans Flutter remote data source files
// for direct api.github.com calls and returns findings.
func ExtractFlutterDirectGitHubCalls(flutterRepo string) ([]Finding, bool) {
	networkDir := filepath.Join(flutterRepo, "lib", "core", "network")
	if _, err := os.Stat(networkDir); err != nil {
		return nil, false
	}

	var findings []Finding
	err := filepath.Walk(networkDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, "_remote_data_source.dart") {
			return nil
		}
		fds := parseGitHubCallsFile(path)
		findings = append(findings, fds...)
		return nil
	})
	if err != nil {
		return nil, false
	}

	return findings, true
}

func parseFlutterDioFile(path string) []ClientEndpoint {
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
	for i, line := range lines {
		lineNum := i + 1

		matches := flutterDioRe.FindStringSubmatch(line)
		if matches == nil {
			continue
		}

		method := strings.ToUpper(matches[1])
		rawPath := matches[2]

		// Strip Dart interpolation
		approximate := dartTemplateRe.MatchString(rawPath)
		rawPath = dartTemplateRe.ReplaceAllString(rawPath, "{param}")

		if !isValidPath(rawPath) {
			continue
		}

		authType := detectFlutterAuth(lines, i, 10)

		ln := lineNum
		endpoints = append(endpoints, ClientEndpoint{
			Client:      "flutter",
			Path:        rawPath,
			Method:      method,
			AuthType:    authType,
			SourceFile:  path,
			LineNumber:  ln,
			Approximate: approximate,
		})
	}

	return endpoints
}

func parseGitHubCallsFile(path string) []Finding {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var findings []Finding
	scanner := bufio.NewScanner(f)
	lineNum := 0
	client := "flutter"

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		urlMatch := ""
		for _, candidate := range []string{"https://api.github.com", "http://api.github.com", "api.github.com"} {
			if strings.Contains(line, candidate) {
				urlMatch = candidate
				break
			}
		}
		if urlMatch == "" {
			continue
		}

		// Extract the URL fragment for detail
		ln := lineNum
		findings = append(findings, Finding{
			Category:         "DIRECT_GITHUB_CALL",
			Severity:         "info",
			Client:           &client,
			DetectedEndpoint: urlMatch,
			DetectedMethod:   "unknown",
			SourceFile:       path,
			LineNumber:       &ln,
			Detail:           "Flutter makes a direct call to api.github.com instead of routing through the platform API",
			Suggestion:       "Consider proxying GitHub API calls through the platform to centralize auth and rate limiting",
		})
	}

	return findings
}

func detectFlutterAuth(lines []string, idx, window int) string {
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
		if flutterAuthRe.MatchString(l) {
			return "bearer"
		}
		if flutterApiKeyRe.MatchString(l) {
			return "api_key"
		}
	}
	return "none"
}

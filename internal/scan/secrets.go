// Secret-pattern scan verb. Replicates the six fixed regex passes plus
// false-positive filter from skills/nightgauge-security-audit/SKILL.md
// Phase 2.2 (audit row B41). The verb counts matching *lines* (not occurrences)
// to preserve drop-in behavioral compatibility with the grep -rn ... | wc -l
// pipeline being replaced — see ADR-001 in
// .nightgauge/knowledge/features/3099-scan-leakcheck/decisions.md.
package scan

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// SecretsScanResult is the stable JSON output schema for
// `nightgauge scan secrets`. Schema version 1 — do not rename or remove
// fields after first merge.
type SecretsScanResult struct {
	V        int            `json:"v"`        // schema version, always 1
	Workdir  string         `json:"workdir"`  // absolute path that was scanned
	Patterns map[string]int `json:"patterns"` // per-pattern matching-line count; six fixed keys, always populated
	Total    int            `json:"total"`    // sum of all pattern counts
	Warnings []string       `json:"warnings"` // non-fatal scan warnings (unreadable files, oversize-skips)
}

// SecretsOptions controls a single secret-scan run.
type SecretsOptions struct {
	// Workdir is the directory to scan. When empty, the caller's CWD is used.
	Workdir string
}

// Pattern key names used by skill consumers via jq paths. Stable after first
// merge — adding a new pattern requires bumping the schema V field.
const (
	patternGenericKV        = "generic_kv"
	patternPEMPrivateKey    = "pem_private_key"
	patternAWSAccessKey     = "aws_access_key"
	patternJWTBearer        = "jwt_bearer"
	patternConnectionString = "connection_string"
	patternDotenvFiles      = "dotenv_files"
)

// secretPatternKeys is the canonical, stable order of pattern keys. The
// JSON Patterns map is initialized with all six keys set to zero so skill-side
// jq paths never resolve to null.
var secretPatternKeys = []string{
	patternGenericKV,
	patternPEMPrivateKey,
	patternAWSAccessKey,
	patternJWTBearer,
	patternConnectionString,
	patternDotenvFiles,
}

// secretsExcludedDirs are pruned at the WalkDir level — matching the
// security-audit skill's --exclude-dir grep flags. Pruning here avoids reading
// thousands of files that we'd discard anyway.
var secretsExcludedDirs = map[string]struct{}{
	".git":         {},
	"node_modules": {},
	"vendor":       {},
	"dist":         {},
	"build":        {},
	"coverage":     {},
}

// secretsMaxFileBytes is the largest file the scanner will inspect line-by-line.
// Files larger than this are recorded as warnings (matches grep's de-facto
// behavior on giant blobs — it works, but the value of the result diminishes).
const secretsMaxFileBytes int64 = 5 * 1024 * 1024

// Compiled regex set. (?i) is used for the patterns where the SKILL.md uses
// `grep -iE`. The narrower connection-string FP filter is intentionally
// case-sensitive to match the SKILL.md's `grep -vE` (no -i) exactly.
var (
	secretsGenericRE     = regexp.MustCompile(`(?i)(api[_-]?key|secret|password|passwd|token|auth[_-]?token|access[_-]?key|private[_-]?key)\s*[:=]\s*['"][^'"]{8,}['"]`)
	secretsPEMRE         = regexp.MustCompile(`BEGIN (RSA |EC |DSA |OPENSSH )?PRIVATE KEY`)
	secretsAWSRE         = regexp.MustCompile(`AKIA[0-9A-Z]{16}`)
	secretsJWTRE         = regexp.MustCompile(`(?i)(jwt[_-]?secret|bearer)\s*[:=]\s*['"][^'"]{16,}['"]`)
	secretsConnstrRE     = regexp.MustCompile(`(?i)(mysql|postgres|postgresql|mongodb|redis|amqp)://[^:@\s]+:[^@\s]+@`)
	secretsGenericFPFilt = regexp.MustCompile(`(?i)(example|placeholder|your[_-]?|<|>|REPLACE|TODO|test|mock|fake|dummy)`)
	secretsConnstrFPFilt = regexp.MustCompile(`(example|localhost|127\.0\.0\.1|REPLACE|TODO)`)
)

// Per-pattern file-extension allowlists — replicate the SKILL.md `--include`
// lists exactly. Keys must be lowercase including the leading dot. The
// AWS-key pass intentionally has no allowlist (the original `grep -rn` had no
// `--include` flag) so it scans every file.
var (
	secretsGenericExts = stringSet(
		".ts", ".tsx", ".js", ".jsx", ".py", ".go",
		".rs", ".java", ".kt",
		".yaml", ".yml", ".json", ".toml",
		".env", ".cfg", ".conf", ".ini",
	)
	secretsPEMExts = stringSet(
		".ts", ".tsx", ".js", ".jsx", ".py", ".go",
		".rs", ".java", ".pem", ".key", ".txt",
	)
	secretsJWTExts = stringSet(
		".ts", ".tsx", ".js", ".jsx", ".py", ".go", ".rs",
		".yaml", ".yml", ".json", ".env",
	)
	secretsConnstrExts = stringSet(
		".ts", ".tsx", ".js", ".jsx", ".py", ".go",
		".yaml", ".yml", ".json", ".toml", ".env",
	)
)

// dotenvExcludedNames are .env-prefixed filenames that should NOT count as a
// committed secret file (they're convention-driven templates).
var dotenvExcludedNames = map[string]struct{}{
	".env" + ".example":  {},
	".env" + ".sample":   {},
	".env" + ".template": {},
}

// RunSecretsScan executes the secret-pattern scan and returns the structured
// result. Non-fatal by design — unreadable files and oversize skips are
// recorded in Warnings rather than returned as errors. err is reserved for
// hard input errors (invalid workdir).
func RunSecretsScan(ctx context.Context, opts SecretsOptions) (*SecretsScanResult, error) {
	workdir := opts.Workdir
	if workdir == "" {
		var err error
		workdir, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("resolve workdir: %w", err)
		}
	}
	abs, err := filepath.Abs(workdir)
	if err != nil {
		return nil, fmt.Errorf("resolve workdir: %w", err)
	}
	workdir = abs

	result := &SecretsScanResult{
		V:        1,
		Workdir:  workdir,
		Patterns: make(map[string]int, len(secretPatternKeys)),
		Warnings: []string{},
	}
	for _, k := range secretPatternKeys {
		result.Patterns[k] = 0
	}

	walkErr := filepath.WalkDir(workdir, func(path string, d fs.DirEntry, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			rel := relOrAbs(workdir, path)
			result.Warnings = append(result.Warnings, fmt.Sprintf("walk %s: %v", rel, err))
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			// Don't prune the workdir root itself even if its basename happens
			// to match an excluded dir name.
			if path == workdir {
				return nil
			}
			if _, skip := secretsExcludedDirs[d.Name()]; skip {
				return fs.SkipDir
			}
			return nil
		}

		base := d.Name()
		ext := strings.ToLower(filepath.Ext(base))

		// Path-based pass: bare .env files (not .env.example/.sample/.template).
		// Broaden the outer match to any .env-prefixed file so the exclusion
		// map is actually consulted — guards against a future refactor that
		// widens the outer condition without revisiting the exclusion logic.
		if strings.HasPrefix(base, ".env") {
			if _, skip := dotenvExcludedNames[base]; !skip {
				result.Patterns[patternDotenvFiles]++
			}
		}

		// Every file is in scope for the AWS-key pass (no allowlist), so we
		// always read regular files. Skip oversize blobs.
		info, infoErr := d.Info()
		if infoErr != nil {
			rel := relOrAbs(workdir, path)
			result.Warnings = append(result.Warnings, fmt.Sprintf("stat %s: %v", rel, infoErr))
			return nil
		}
		if info.Size() > secretsMaxFileBytes {
			rel := relOrAbs(workdir, path)
			result.Warnings = append(result.Warnings, fmt.Sprintf("skip oversize %s (%d bytes > %d)", rel, info.Size(), secretsMaxFileBytes))
			return nil
		}

		f, openErr := os.Open(path)
		if openErr != nil {
			rel := relOrAbs(workdir, path)
			result.Warnings = append(result.Warnings, fmt.Sprintf("open %s: %v", rel, openErr))
			return nil
		}
		if scanErr := scanFileForSecrets(f, ext, result.Patterns); scanErr != nil {
			rel := relOrAbs(workdir, path)
			result.Warnings = append(result.Warnings, fmt.Sprintf("scan %s truncated: %v", rel, scanErr))
		}
		_ = f.Close()
		return nil
	})

	if walkErr != nil {
		// ctx cancellation or unexpected fatal walk error — record but still
		// return whatever we collected so callers can inspect partial data.
		result.Warnings = append(result.Warnings, fmt.Sprintf("walk aborted: %v", walkErr))
	}

	total := 0
	for _, k := range secretPatternKeys {
		total += result.Patterns[k]
	}
	result.Total = total

	return result, nil
}

// scanFileForSecrets reads the file line-by-line and increments the
// appropriate pattern counters. Each pattern increments at most once per line
// (line-count semantics, matching grep's `wc -l` behavior).
func scanFileForSecrets(r io.Reader, ext string, counts map[string]int) error {
	scanner := bufio.NewScanner(r)
	// Default Scanner buffer is 64K — large enough for source-file lines but
	// some minified JS or generated JSON can blow past it. Bump to 1 MiB.
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()

		if contains(secretsGenericExts, ext) && secretsGenericRE.MatchString(line) {
			if !secretsGenericFPFilt.MatchString(line) {
				counts[patternGenericKV]++
			}
		}
		if contains(secretsPEMExts, ext) && secretsPEMRE.MatchString(line) {
			counts[patternPEMPrivateKey]++
		}
		// AWS pass: no extension allowlist, no FP filter.
		if secretsAWSRE.MatchString(line) {
			counts[patternAWSAccessKey]++
		}
		if contains(secretsJWTExts, ext) && secretsJWTRE.MatchString(line) {
			if !secretsGenericFPFilt.MatchString(line) {
				counts[patternJWTBearer]++
			}
		}
		if contains(secretsConnstrExts, ext) && secretsConnstrRE.MatchString(line) {
			if !secretsConnstrFPFilt.MatchString(line) {
				counts[patternConnectionString]++
			}
		}
	}
	// scanner errors (e.g. line exceeds the 1 MiB buffer) are non-fatal but
	// observable — return so the caller can record a warning. A silent miss
	// would let a single oversized line hide every secret beneath it.
	return scanner.Err()
}

func stringSet(s ...string) map[string]struct{} {
	out := make(map[string]struct{}, len(s))
	for _, v := range s {
		out[strings.ToLower(v)] = struct{}{}
	}
	return out
}

func contains(set map[string]struct{}, key string) bool {
	_, ok := set[key]
	return ok
}

// relOrAbs returns path relative to workdir for warning messages, falling
// back to the absolute path if the rel computation fails.
func relOrAbs(workdir, path string) string {
	if rel, err := filepath.Rel(workdir, path); err == nil {
		return rel
	}
	return path
}

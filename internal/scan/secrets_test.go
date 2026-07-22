package scan

import (
	"context"
	"strings"
	"testing"
)

func runSecretsScan(t *testing.T, dir string) *SecretsScanResult {
	t.Helper()
	res, err := RunSecretsScan(context.Background(), SecretsOptions{Workdir: dir})
	if err != nil {
		t.Fatalf("RunSecretsScan: %v", err)
	}
	if res.V != 1 {
		t.Errorf("V = %d, want 1", res.V)
	}
	return res
}

// TestRunSecretsScan_AllPatternKeysAlwaysPopulated asserts the schema contract:
// every consumer relies on `jq -r '.patterns.<name>'` returning a number, never
// null. An empty workdir must still emit the six fixed keys with zero counts.
func TestRunSecretsScan_AllPatternKeysAlwaysPopulated(t *testing.T) {
	dir := t.TempDir()
	res := runSecretsScan(t, dir)
	for _, key := range secretPatternKeys {
		if _, ok := res.Patterns[key]; !ok {
			t.Errorf("Patterns missing key %q (skill consumers require fixed shape)", key)
		}
	}
	if res.Total != 0 {
		t.Errorf("Total = %d on empty workdir, want 0", res.Total)
	}
}

func TestRunSecretsScan_GenericKeyValuePositive(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "config.ts", `const apiKey = "abcdef1234567890"`)
	res := runSecretsScan(t, dir)
	if res.Patterns[patternGenericKV] != 1 {
		t.Errorf("generic_kv = %d, want 1", res.Patterns[patternGenericKV])
	}
	if res.Total != 1 {
		t.Errorf("total = %d, want 1", res.Total)
	}
}

// FP filter should suppress matches that include the literal "example",
// matching the SKILL.md `grep -vE '(example|placeholder|...)'` step.
func TestRunSecretsScan_GenericKeyValueFalsePositiveFiltered(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "config.ts", `const apiKey = "example_placeholder_12345"`)
	res := runSecretsScan(t, dir)
	if res.Patterns[patternGenericKV] != 0 {
		t.Errorf("generic_kv = %d, want 0 (example should be FP-filtered)", res.Patterns[patternGenericKV])
	}
}

func TestRunSecretsScan_PEMPositive(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "leaked.pem",
		"-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQ\n-----END RSA PRIVATE KEY-----\n")
	res := runSecretsScan(t, dir)
	if res.Patterns[patternPEMPrivateKey] != 1 {
		t.Errorf("pem_private_key = %d, want 1", res.Patterns[patternPEMPrivateKey])
	}
}

// PEM pass intentionally has no FP filter — even an example block counts.
// This documents the SKILL.md behavior: PEM headers are treated as a
// hard-block signal regardless of surrounding text.
func TestRunSecretsScan_PEMNoFalsePositiveFilter(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "leaked.pem",
		"# example test fixture\n-----BEGIN PRIVATE KEY-----\n")
	res := runSecretsScan(t, dir)
	if res.Patterns[patternPEMPrivateKey] != 1 {
		t.Errorf("pem_private_key = %d, want 1 (no FP filter on PEM)", res.Patterns[patternPEMPrivateKey])
	}
}

func TestRunSecretsScan_AWSAccessKeyPositive(t *testing.T) {
	dir := t.TempDir()
	// AWS pass has no extension allowlist — verifying with a non-source file.
	writeFile(t, dir, "notes.md", "leaked: AKIAIOSFODNN7EXAMPLE")
	res := runSecretsScan(t, dir)
	if res.Patterns[patternAWSAccessKey] != 1 {
		t.Errorf("aws_access_key = %d, want 1", res.Patterns[patternAWSAccessKey])
	}
}

func TestRunSecretsScan_AWSAccessKeyNoFilter(t *testing.T) {
	dir := t.TempDir()
	// SKILL.md AWS pass has no FP filter — even an example token counts.
	// EXAMPLE is uppercase and AKIA pattern requires uppercase, so this matches.
	writeFile(t, dir, "test_fixture.go", `var awsKey = "AKIAIOSFODNN7EXAMPLE"`)
	res := runSecretsScan(t, dir)
	if res.Patterns[patternAWSAccessKey] != 1 {
		t.Errorf("aws_access_key = %d, want 1 (no FP filter on AWS pass)", res.Patterns[patternAWSAccessKey])
	}
}

func TestRunSecretsScan_JWTBearerPositive(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "auth.ts", `const bearer = "abcdefghijklmnopqrstuvwxyz"`)
	res := runSecretsScan(t, dir)
	if res.Patterns[patternJWTBearer] != 1 {
		t.Errorf("jwt_bearer = %d, want 1", res.Patterns[patternJWTBearer])
	}
}

func TestRunSecretsScan_JWTBearerFalsePositiveFiltered(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "auth.ts", `const bearer = "your_test_token_goes_here"`)
	res := runSecretsScan(t, dir)
	if res.Patterns[patternJWTBearer] != 0 {
		t.Errorf("jwt_bearer = %d, want 0 (FP filter)", res.Patterns[patternJWTBearer])
	}
}

func TestRunSecretsScan_ConnectionStringPositive(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "db.go", `dsn := "postgres://admin:hunter2@db.prod.io:5432/app"`)
	res := runSecretsScan(t, dir)
	if res.Patterns[patternConnectionString] != 1 {
		t.Errorf("connection_string = %d, want 1", res.Patterns[patternConnectionString])
	}
}

func TestRunSecretsScan_ConnectionStringLocalhostFiltered(t *testing.T) {
	dir := t.TempDir()
	// SKILL.md narrower FP filter excludes localhost / 127.0.0.1 — local dev
	// connection strings are noise, not committed leaks.
	writeFile(t, dir, "db.go", `dsn := "postgres://admin:hunter2@localhost:5432/app"`)
	res := runSecretsScan(t, dir)
	if res.Patterns[patternConnectionString] != 0 {
		t.Errorf("connection_string = %d, want 0 (localhost FP-filtered)", res.Patterns[patternConnectionString])
	}
}

// SKILL.md FP filter for connection strings is case-sensitive (no -i on the
// outer grep -vE). "Localhost" with a capital L should NOT match the filter
// and the secret pattern should count.
func TestRunSecretsScan_ConnectionStringFilterCaseSensitive(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "db.go", `dsn := "postgres://admin:hunter2@Localhost-prod:5432/app"`)
	res := runSecretsScan(t, dir)
	if res.Patterns[patternConnectionString] != 1 {
		t.Errorf("connection_string = %d, want 1 (Localhost capitalized not filtered)", res.Patterns[patternConnectionString])
	}
}

// Walk pruning must skip excluded directories (.git, node_modules, vendor,
// dist, build, coverage). A leaked secret inside node_modules must not count.
func TestRunSecretsScan_ExcludedDirsPruned(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "node_modules/leak.ts", `const apiKey = "abcdef1234567890"`)
	writeFile(t, dir, "vendor/leak.go", `var token = "abcdef1234567890"`)
	writeFile(t, dir, ".git/HEAD", `ref: refs/heads/main`)
	res := runSecretsScan(t, dir)
	if res.Patterns[patternGenericKV] != 0 {
		t.Errorf("generic_kv = %d, want 0 (excluded dirs should be pruned)", res.Patterns[patternGenericKV])
	}
}

// Bare `.env` counts as a committed secret file; `.env.example` does not.
func TestRunSecretsScan_DotenvCountsBareNotTemplate(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".env", "API_KEY=hunter2")
	writeFile(t, dir, ".env"+".example", "API_KEY=changeme")
	writeFile(t, dir, ".env"+".sample", "")
	writeFile(t, dir, ".env"+".template", "")
	res := runSecretsScan(t, dir)
	if res.Patterns[patternDotenvFiles] != 1 {
		t.Errorf("dotenv_files = %d, want 1 (only bare .env counts)", res.Patterns[patternDotenvFiles])
	}
}

// Multiple matches on the SAME line count as one (line-count semantics, per
// ADR-001). This guards against drift to occurrence-count semantics.
func TestRunSecretsScan_LineCountSemantics(t *testing.T) {
	dir := t.TempDir()
	// Two AWS-pattern matches on a single line — must count as 1.
	writeFile(t, dir, "two.go", "AKIAIOSFODNN7EXAMPL1 AKIAIOSFODNN7EXAMPL2")
	res := runSecretsScan(t, dir)
	if res.Patterns[patternAWSAccessKey] != 1 {
		t.Errorf("aws_access_key = %d, want 1 (line-count, not occurrence-count)", res.Patterns[patternAWSAccessKey])
	}
}

// Files outside a pattern's --include allowlist must not count for that
// pattern. The generic-kv pass has no `.md` in its allowlist (the SKILL.md
// --include list is fixed), so a key=value line in markdown should not count.
func TestRunSecretsScan_ExtensionAllowlistEnforced(t *testing.T) {
	dir := t.TempDir()
	// .md is NOT in the generic-kv allowlist.
	writeFile(t, dir, "notes.md", `apiKey: "abcdef1234567890"`)
	res := runSecretsScan(t, dir)
	if res.Patterns[patternGenericKV] != 0 {
		t.Errorf("generic_kv = %d in .md file, want 0 (.md not in allowlist)", res.Patterns[patternGenericKV])
	}
}

// AWS pass deliberately has NO extension allowlist (matches SKILL.md grep
// invocation that omits --include). A key in a .md file must count.
func TestRunSecretsScan_AWSPassNoAllowlist(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "README.md", "do not commit AKIAIOSFODNN7EXAMPLE")
	res := runSecretsScan(t, dir)
	if res.Patterns[patternAWSAccessKey] != 1 {
		t.Errorf("aws_access_key in .md = %d, want 1 (AWS pass has no extension allowlist)", res.Patterns[patternAWSAccessKey])
	}
}

func TestRunSecretsScan_TotalSumsAllPatterns(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "config.ts", `const apiKey = "abcdef1234567890"`)
	writeFile(t, dir, "leaked.pem", "-----BEGIN PRIVATE KEY-----\n")
	writeFile(t, dir, "main.go", "AKIAIOSFODNN7EXAMPLE")
	writeFile(t, dir, ".env", "API_KEY=x")
	res := runSecretsScan(t, dir)
	want := res.Patterns[patternGenericKV] +
		res.Patterns[patternPEMPrivateKey] +
		res.Patterns[patternAWSAccessKey] +
		res.Patterns[patternJWTBearer] +
		res.Patterns[patternConnectionString] +
		res.Patterns[patternDotenvFiles]
	if res.Total != want {
		t.Errorf("Total = %d, want %d (sum of patterns)", res.Total, want)
	}
	if res.Total < 4 {
		t.Errorf("Total = %d, want >= 4 (each fixture should match at least one pass)", res.Total)
	}
}

// The verb is non-fatal — record errors as warnings, never return them.
// An empty workdir falls back to CWD (matches sibling RunEcosystemScan).
func TestRunSecretsScan_EmptyWorkdirFallsToCWD(t *testing.T) {
	_, err := RunSecretsScan(context.Background(), SecretsOptions{Workdir: ""})
	if err != nil {
		t.Fatalf("RunSecretsScan with empty workdir should fall back to CWD, got error: %v", err)
	}
}

// scanFileForSecrets is callable directly with an io.Reader — verifies the
// line-by-line scanning works against arbitrary content sources.
func TestScanFileForSecrets_DirectReaderInterface(t *testing.T) {
	counts := map[string]int{
		patternGenericKV: 0, patternPEMPrivateKey: 0, patternAWSAccessKey: 0,
		patternJWTBearer: 0, patternConnectionString: 0, patternDotenvFiles: 0,
	}
	scanFileForSecrets(strings.NewReader(`const apiKey = "abcdef1234567890"`), ".ts", counts)
	if counts[patternGenericKV] != 1 {
		t.Errorf("scanFileForSecrets generic_kv = %d, want 1", counts[patternGenericKV])
	}
}

package config

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// update regenerates golden output.yaml files from actual MigrateFile output.
// Run: go test ./internal/config/... -run TestMigrateGolden -update
var update = flag.Bool("update", false, "update golden output.yaml files")

// TestMigrateGolden runs all testdata/migrations/* cases.
// Each directory contains input.yaml plus either:
//   - output.yaml (success case: migrated content compared byte-for-byte)
//   - expected-error.txt (error case: error substring matched)
func TestMigrateGolden(t *testing.T) {
	dirs, err := filepath.Glob("testdata/migrations/*")
	if err != nil {
		t.Fatalf("glob testdata/migrations/*: %v", err)
	}
	if len(dirs) == 0 {
		t.Fatal("no testdata/migrations/* directories found")
	}

	for _, dir := range dirs {
		dir := dir
		name := filepath.Base(dir)
		t.Run(name, func(t *testing.T) {
			inputFile := filepath.Join(dir, "input.yaml")
			outputFile := filepath.Join(dir, "output.yaml")
			errFile := filepath.Join(dir, "expected-error.txt")

			inputBytes, err := os.ReadFile(inputFile)
			if err != nil {
				t.Fatalf("read input.yaml: %v", err)
			}

			// Copy input to a temp file so MigrateFile can write to it safely.
			tmp := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(tmp, inputBytes, 0o644); err != nil {
				t.Fatalf("write temp input: %v", err)
			}

			result, migrateErr := MigrateFile(tmp, false)

			// Determine case type by expected-error.txt presence.
			// Error case: expected-error.txt exists in the directory.
			// Success case: expected-error.txt absent (output.yaml may or may not exist yet).
			_, errFileStat := os.Stat(errFile)
			isErrorCase := errFileStat == nil

			if !isErrorCase {
				// Success case.
				if migrateErr != nil && !errors.Is(migrateErr, ErrAlreadyMigrated) {
					t.Fatalf("MigrateFile returned unexpected error: %v", migrateErr)
				}

				got, err := os.ReadFile(tmp)
				if err != nil {
					t.Fatalf("read migrated temp file: %v", err)
				}

				if *update {
					if err := os.WriteFile(outputFile, got, 0o644); err != nil {
						t.Fatalf("update golden file: %v", err)
					}
					t.Logf("updated %s", outputFile)
				}

				want, err := os.ReadFile(outputFile)
				if err != nil {
					t.Fatalf("read output.yaml (run with -update to create): %v", err)
				}

				if !bytes.Equal(want, got) {
					t.Errorf("output mismatch for %s:\nwant:\n%s\ngot:\n%s", name, want, got)
				}
				_ = result
			} else {
				// Error case.
				wantErrBytes, err := os.ReadFile(errFile)
				if err != nil {
					t.Fatalf("read expected-error.txt: %v", err)
				}
				if migrateErr == nil {
					t.Fatalf("expected an error, got nil (result: %+v)", result)
				}
				wantSubstr := strings.TrimSpace(string(wantErrBytes))
				gotErrStr := migrateErr.Error()
				if !strings.Contains(gotErrStr, wantSubstr) {
					t.Errorf("error mismatch:\nwant substring: %q\ngot:            %q", wantSubstr, gotErrStr)
				}
			}
		})
	}
}

// TestMigrateIdempotent verifies that calling MigrateFile twice on a v1 config
// returns ErrAlreadyMigrated on the second call (the first call wrote v2).
func TestMigrateIdempotent(t *testing.T) {
	dirs, err := filepath.Glob("testdata/migrations/*")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}

	for _, dir := range dirs {
		dir := dir
		name := filepath.Base(dir)
		// Only success cases (those with output.yaml).
		outputFile := filepath.Join(dir, "output.yaml")
		if _, err := os.Stat(outputFile); err != nil {
			continue
		}

		t.Run(name, func(t *testing.T) {
			inputBytes, err := os.ReadFile(filepath.Join(dir, "input.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			tmp := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(tmp, inputBytes, 0o644); err != nil {
				t.Fatal(err)
			}

			// First call: may return ErrAlreadyMigrated (if input is already v2) or succeed.
			_, firstErr := MigrateFile(tmp, false)
			if firstErr != nil && !errors.Is(firstErr, ErrAlreadyMigrated) {
				t.Fatalf("first MigrateFile: %v", firstErr)
			}

			// Second call: must always return ErrAlreadyMigrated.
			_, secondErr := MigrateFile(tmp, false)
			if !errors.Is(secondErr, ErrAlreadyMigrated) {
				t.Errorf("second call: want ErrAlreadyMigrated, got %v", secondErr)
			}
		})
	}
}

// TestMigratePreservesComments verifies that inline YAML comments survive the
// v1→v2 round-trip.
func TestMigratePreservesComments(t *testing.T) {
	dir := "testdata/migrations/v1-with-comments"
	inputBytes, err := os.ReadFile(filepath.Join(dir, "input.yaml"))
	if err != nil {
		t.Fatalf("read input.yaml: %v", err)
	}

	tmp := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(tmp, inputBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := MigrateFile(tmp, false); err != nil {
		t.Fatalf("MigrateFile: %v", err)
	}

	got, err := os.ReadFile(tmp)
	if err != nil {
		t.Fatal(err)
	}
	gotStr := string(got)

	for _, comment := range []string{"# org login", "# project board number"} {
		if !strings.Contains(gotStr, comment) {
			t.Errorf("comment %q not found in migrated output:\n%s", comment, gotStr)
		}
	}

	// Also verify schema_version and forges.github were added.
	if !strings.Contains(gotStr, `schema_version: "2"`) {
		t.Errorf("schema_version not added:\n%s", gotStr)
	}
	if !strings.Contains(gotStr, "github:") {
		t.Errorf("forges.github not added:\n%s", gotStr)
	}
}

// TestMigrateDryRun verifies that --dry-run populates Diff but does not write
// the file.
func TestMigrateDryRun(t *testing.T) {
	inputBytes, err := os.ReadFile("testdata/migrations/v1-default-no-forges/input.yaml")
	if err != nil {
		t.Fatal(err)
	}

	tmp := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(tmp, inputBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := MigrateFile(tmp, true /* dryRun */)
	if err != nil {
		t.Fatalf("MigrateFile dry-run: %v", err)
	}

	if !result.Changed {
		t.Error("dry-run: expected Changed=true for v1 input")
	}
	if result.Diff == "" {
		t.Error("dry-run: expected non-empty Diff")
	}

	// File must be unchanged.
	got, err := os.ReadFile(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(inputBytes, got) {
		t.Errorf("dry-run modified the file:\noriginal:\n%s\nafter dry-run:\n%s", inputBytes, got)
	}
}

func TestMigratePreviewRedactsSecretsButPreservesOnDiskValues(t *testing.T) {
	const token = "github_pat_complete_representative_123456789"
	const apiKey = "provider-api-key-secret-987654321"
	const license = "license-key-secret-246813579"
	input := []byte(`owner: nightgauge
github_auth:
  token: ` + token + `
  tokens:
    nightgauge: ` + token + `
gemini:
  api_key: ` + apiKey + `
platform:
  license_key: ` + license + `
forges:
  custom:
    kind: github
    base_url: https://github.com
    token_env: GITHUB_TOKEN
`)

	for _, dryRun := range []bool{true, false} {
		t.Run(fmt.Sprintf("dryRun=%v", dryRun), func(t *testing.T) {
			tmp := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(tmp, input, 0o644); err != nil {
				t.Fatal(err)
			}
			result, err := MigrateFile(tmp, dryRun)
			if err != nil {
				t.Fatalf("MigrateFile: %v", err)
			}
			for _, forbidden := range []string{token, "representative_123456789", apiKey, license} {
				if strings.Contains(result.Diff, forbidden) {
					t.Fatalf("preview contains credential fragment %q:\n%s", forbidden, result.Diff)
				}
			}
			if !strings.Contains(result.Diff, RedactedValue) {
				t.Fatalf("preview does not contain redaction marker:\n%s", result.Diff)
			}
			if !strings.Contains(result.Diff, "GITHUB_TOKEN") {
				t.Fatalf("preview should retain environment variable name:\n%s", result.Diff)
			}
			if !dryRun {
				persisted, readErr := os.ReadFile(tmp)
				if readErr != nil {
					t.Fatal(readErr)
				}
				for _, expected := range []string{token, apiKey, license} {
					if !strings.Contains(string(persisted), expected) {
						t.Fatalf("on-disk migration changed secret %q", expected)
					}
				}
			}
		})
	}
}

// TestMigrateWarningLog verifies that a successful migration emits a log line
// containing the file path and version info.
func TestMigrateWarningLog(t *testing.T) {
	inputBytes, err := os.ReadFile("testdata/migrations/v1-default-no-forges/input.yaml")
	if err != nil {
		t.Fatal(err)
	}

	tmp := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(tmp, inputBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	var logBuf strings.Builder
	orig := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(orig)

	if _, err := MigrateFile(tmp, false); err != nil {
		t.Fatalf("MigrateFile: %v", err)
	}

	logOut := logBuf.String()
	if !strings.Contains(logOut, "migrated config") {
		t.Errorf("expected migration log line, got: %q", logOut)
	}
	if !strings.Contains(logOut, tmp) {
		t.Errorf("log line does not contain file path %q: %q", tmp, logOut)
	}
}

// TestMigrateValidationLineColumn verifies that validation errors include a
// YAML line number reference when the migrated config is invalid.
func TestMigrateValidationLineColumn(t *testing.T) {
	// missing-base-url-gitlab has a gitlab forge with no base_url — will fail
	// ValidateForgeConfig after migration inserts schema_version + forges.github.
	inputBytes, err := os.ReadFile("testdata/migrations/missing-base-url-gitlab/input.yaml")
	if err != nil {
		t.Fatal(err)
	}

	tmp := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(tmp, inputBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = MigrateFile(tmp, false)
	if err == nil {
		t.Fatal("expected validation error, got nil")
	}

	errStr := err.Error()
	// The error must name the file path and include a line indicator or the forge ID.
	if !strings.Contains(errStr, "post-migration validation failed") {
		t.Errorf("error does not mention post-migration validation: %q", errStr)
	}
	// Line number annotation: either ":<line>:" pattern or the forge key.
	if !strings.Contains(errStr, "corp-gitlab") && !strings.Contains(errStr, ":") {
		t.Errorf("error does not contain line reference or forge key: %q", errStr)
	}
}

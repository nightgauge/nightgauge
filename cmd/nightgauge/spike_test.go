package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// validSpikeBody returns a spike issue body that passes ValidateBody for the
// given artifact path.
func validSpikeBody(path string) string {
	return `## Spike Contract (Path A)

**Artifact**: ` + "`" + path + "`" + `

Some description of the spike.

` + "```" + `yaml recommendations
spike: 1
recommendations:
  - id: implement-feature
    action: adopt
    title: "Implement the feature"
    type: feature
    priority: high
    size: M
` + "```" + `
`
}

func TestRunSpikeValidate_ValidBodyOK(t *testing.T) {
	ok, msg := runSpikeValidate(validSpikeBody("docs/spikes/1-some-spike.md"))
	if !ok {
		t.Fatalf("expected valid body to pass, got message: %s", msg)
	}
	if msg != "spike validate: OK" {
		t.Errorf("unexpected success message: %q", msg)
	}
}

func TestRunSpikeValidate_EmptyBodyReportsNoBodySupplied(t *testing.T) {
	ok, msg := runSpikeValidate("")
	if ok {
		t.Fatal("expected empty body to fail validation")
	}
	if !strings.Contains(msg, "no body supplied") {
		t.Errorf("expected 'no body supplied' in message, got: %q", msg)
	}
	if strings.Contains(msg, "fenced") {
		t.Errorf("empty body should not be reported as a contract violation, got: %q", msg)
	}
}

func TestRunSpikeValidate_WhitespaceOnlyBodyReportsNoBodySupplied(t *testing.T) {
	ok, msg := runSpikeValidate("   \n\t  \n")
	if ok {
		t.Fatal("expected whitespace-only body to fail validation")
	}
	if !strings.Contains(msg, "no body supplied") {
		t.Errorf("expected 'no body supplied' in message, got: %q", msg)
	}
}

func TestRunSpikeValidate_InvalidBodyReportsContractError(t *testing.T) {
	ok, msg := runSpikeValidate("## Spike Contract (Path A)\n\nSome text without a yaml block.\n")
	if ok {
		t.Fatal("expected malformed body to fail validation")
	}
	if !strings.Contains(msg, "missing a fenced") {
		t.Errorf("expected a contract error naming the missing block, got: %q", msg)
	}
}

// TestSpikeValidateCmd_PositionalPathAliasesBodyFile exercises the fix for
// #1981: a positional path argument to `spike validate` must be read as the
// body, exactly as --body-file would be, instead of being silently ignored
// while the command reads (empty) stdin.
func TestSpikeValidateCmd_PositionalPathAliasesBodyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "1-some-spike.md")
	body := validSpikeBody("docs/spikes/1-some-spike.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}

	cmd := spikeValidateCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetIn(strings.NewReader("")) // stdin must not be consulted when a path is given
	cmd.SetArgs([]string{path})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("expected positional path to validate successfully, got error: %v (stderr: %s)", err, stderr.String())
	}
}

// TestSpikeValidateCmd_PositionalPathAndBodyFileConflict ensures the two
// forms of specifying the body are mutually exclusive rather than one
// silently winning.
func TestSpikeValidateCmd_PositionalPathAndBodyFileConflict(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "1-some-spike.md")
	body := validSpikeBody("docs/spikes/1-some-spike.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}

	cmd := spikeValidateCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{path, "--body-file", path})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected an error when both a positional path and --body-file are given")
	}
	if !strings.Contains(err.Error(), "positional path") {
		t.Errorf("expected error naming the positional/--body-file conflict, got: %v", err)
	}
}

// TestSpikeValidateCmd_MultiplePositionalArgsRejected guards the Args
// constraint added for #1981: unlike before, cobra must reject more than one
// positional argument instead of silently discarding it.
func TestSpikeValidateCmd_MultiplePositionalArgsRejected(t *testing.T) {
	cmd := spikeValidateCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"one", "two"})

	if err := cmd.Execute(); err == nil {
		t.Fatal("expected an error for more than one positional argument")
	}
}

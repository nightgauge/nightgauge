package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureToken is shaped like a classic GitHub PAT (prefix + 36 base62). It is
// not a real credential.
const fixtureToken = "ghp_" + "0123456789abcdef0123456789abcdef0123"

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func newCredentialRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q")
	if err := os.MkdirAll(filepath.Join(dir, ".nightgauge"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeFixture(t *testing.T, dir, rel, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestTrackedSecretsCheck pins #2024: a committed token under .nightgauge/ is a
// failing finding at path:line with the value redacted everywhere, including
// the JSON; the same file untracked is not scanned.
func TestTrackedSecretsCheck(t *testing.T) {
	body := "project:\n  owner: acme\ngithub_auth:\n  token: " + fixtureToken + "\n"

	t.Run("tracked", func(t *testing.T) {
		dir := newCredentialRepo(t)
		writeFixture(t, dir, ".nightgauge/config.yaml", body)
		gitIn(t, dir, "add", ".nightgauge/config.yaml")
		gitIn(t, dir, "commit", "-q", "-m", "fixture")

		item, warning := checkTrackedCredentials(dir)
		if item.OK {
			t.Fatalf("check passed with a committed token: %+v", item)
		}
		if warning == "" {
			t.Error("no warning for a committed token")
		}
		if len(item.Findings) != 1 {
			t.Fatalf("findings = %+v, want exactly one", item.Findings)
		}
		f := item.Findings[0]
		if f.Path != ".nightgauge/config.yaml" || f.Line != 4 || f.Pattern != "github-token" || f.Redacted != "ghp_…" {
			t.Errorf("finding = %+v, want .nightgauge/config.yaml:4 github-token ghp_…", f)
		}
		if !strings.Contains(item.Error, ".nightgauge/config.yaml:4") {
			t.Errorf("error does not name path:line: %s", item.Error)
		}
		if !strings.Contains(item.Error, "rotate") || !strings.Contains(item.Error, "history") {
			t.Errorf("remediation does not name both steps: %s", item.Error)
		}

		result := DoctorResult{V: 1, Checks: map[string]CheckItem{trackedCredentialsCheck: item}, Warnings: []string{warning}}
		raw, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), fixtureToken) || strings.Contains(string(raw), "0123456789abcdef") {
			t.Errorf("JSON carries the token: %s", raw)
		}
		if !strings.Contains(string(raw), `"line":4`) || !strings.Contains(string(raw), `"path":".nightgauge/config.yaml"`) {
			t.Errorf("JSON lacks the structured finding: %s", raw)
		}
	})

	t.Run("untracked", func(t *testing.T) {
		dir := newCredentialRepo(t)
		writeFixture(t, dir, ".nightgauge/config.yaml", body)

		item, warning := checkTrackedCredentials(dir)
		if !item.OK || warning != "" || len(item.Findings) != 0 {
			t.Fatalf("untracked file produced a finding: %+v %q", item, warning)
		}
		if !strings.Contains(item.Detail, "no tracked credentials") {
			t.Errorf("detail = %q, want the explicit clean line", item.Detail)
		}
	})

	// Proves the tracked-only filter is load-bearing: a lister that returns
	// every file on disk (what dropping `git ls-files` would do) turns the
	// untracked case into a finding.
	t.Run("filter-is-load-bearing", func(t *testing.T) {
		dir := newCredentialRepo(t)
		writeFixture(t, dir, ".nightgauge/config.yaml", body)
		prev := trackedFileLister
		trackedFileLister = func(string) (string, []string, bool, error) {
			return dir, []string{".nightgauge/config.yaml"}, true, nil
		}
		t.Cleanup(func() { trackedFileLister = prev })
		if item, _ := checkTrackedCredentials(dir); item.OK {
			t.Fatal("an all-files lister found nothing; the fixture does not exercise the filter")
		}
	})
}

// TestTrackedSecretsCheckPatterns covers every GitHub prefix, the license-key
// prefixes from the platform's parser, and prose that must not match.
func TestTrackedSecretsCheckPatterns(t *testing.T) {
	dir := newCredentialRepo(t)
	lines := []string{
		"a: gho_" + strings.Repeat("a", 36),
		"b: ghu_" + strings.Repeat("b", 36),
		"c: ghs_" + strings.Repeat("c", 36),
		"d: ghr_" + strings.Repeat("d", 36),
		"e: github_pat_" + strings.Repeat("E", 82),
		"f: ib_live_" + strings.Repeat("f", 32),
		"g: ib_ci_" + strings.Repeat("g", 32),
		"h: ghp_example",
		"i: env:GITHUB_TOKEN",
	}
	writeFixture(t, dir, ".nightgauge/notes.md", strings.Join(lines, "\n")+"\n")
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "fixture")

	item, _ := checkTrackedCredentials(dir)
	if len(item.Findings) != 7 {
		t.Fatalf("findings = %d (%+v), want 7", len(item.Findings), item.Findings)
	}
	wantRedacted := []string{"gho_…", "ghu_…", "ghs_…", "ghr_…", "github_pat_…", "ib_live_…", "ib_ci_…"}
	for i, f := range item.Findings {
		if f.Line != i+1 || f.Redacted != wantRedacted[i] {
			t.Errorf("finding %d = %+v, want line %d %s", i, f, i+1, wantRedacted[i])
		}
	}
	if strings.Contains(item.Error, strings.Repeat("a", 36)) {
		t.Errorf("error carries a matched value: %s", item.Error)
	}
}

// TestTrackedSecretsCheckSkips: a binary file and one over 1 MiB are skipped
// with a note, and a directory outside any git work tree skips the check.
func TestTrackedSecretsCheckSkips(t *testing.T) {
	dir := newCredentialRepo(t)
	writeFixture(t, dir, ".nightgauge/blob.bin", "x\x00"+fixtureToken)
	big := strings.Repeat("x", maxScannedFileBytes) + "\n" + fixtureToken + "\n"
	writeFixture(t, dir, ".nightgauge/big.log", big)
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "fixture")

	item, warning := checkTrackedCredentials(dir)
	if !item.OK || warning != "" {
		t.Fatalf("skipped files produced a finding: %+v", item)
	}
	for _, want := range []string{".nightgauge/blob.bin (binary)", ".nightgauge/big.log (over 1 MiB)"} {
		if !strings.Contains(item.Detail, want) {
			t.Errorf("detail lacks %q: %s", want, item.Detail)
		}
	}

	outside := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(outside))
	item, warning = checkTrackedCredentials(outside)
	if !item.OK || warning != "" || !strings.Contains(item.Detail, "skipped") {
		t.Errorf("outside a work tree: %+v %q, want a skip", item, warning)
	}
}

// TestTrackedSecretsCheckGluedAndEscaping: a token glued to an identifier is
// still found, and a tracked path that resolves outside the repository
// through a symlinked directory is never opened.
func TestTrackedSecretsCheckGluedAndEscaping(t *testing.T) {
	dir := newCredentialRepo(t)
	writeFixture(t, dir, ".nightgauge/env.sh", "export GH_TOKEN_"+fixtureToken+"\n")

	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "leak.yaml"), []byte("token: "+fixtureToken+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, ".nightgauge", "linked")); err != nil {
		t.Skip("symlinks unavailable")
	}
	gitIn(t, dir, "add", ".nightgauge/env.sh")
	// Track the file as if the directory were real, then swap the directory
	// for the symlink: git records the path, the work tree leads outside.
	if err := os.Remove(filepath.Join(dir, ".nightgauge", "linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".nightgauge", "linked"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, dir, ".nightgauge/linked/leak.yaml", "clean: true\n")
	gitIn(t, dir, "add", ".nightgauge/linked/leak.yaml")
	gitIn(t, dir, "commit", "-q", "-m", "fixture")
	if err := os.RemoveAll(filepath.Join(dir, ".nightgauge", "linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, ".nightgauge", "linked")); err != nil {
		t.Fatal(err)
	}

	item, _ := checkTrackedCredentials(dir)
	if len(item.Findings) != 1 || item.Findings[0].Path != ".nightgauge/env.sh" {
		t.Fatalf("findings = %+v, want only the glued token in env.sh", item.Findings)
	}
	if !strings.Contains(item.Error, ".nightgauge/linked/leak.yaml (resolves outside the repository)") {
		t.Errorf("escaping path not noted: %s", item.Error)
	}
}

// TestRunDoctorRefusedConfigIsRequiredFailure: a config that exists and was
// refused is a failed config check (exit 2), not a fresh repository.
func TestRunDoctorRefusedConfigIsRequiredFailure(t *testing.T) {
	result := RunDoctorWithConfigError(context.Background(), nil, errors.New("config: refused (value redacted)"), nil, nil)
	if result.ExitCode != 2 {
		t.Errorf("ExitCode = %d, want 2", result.ExitCode)
	}
	cfgCheck := result.Checks["config"]
	if cfgCheck.OK || !strings.Contains(cfgCheck.Error, "refused") {
		t.Errorf("config check = %+v, want a failure carrying the load error", cfgCheck)
	}
	found := false
	for _, name := range result.FailedChecks {
		if name == "config" {
			found = true
		}
	}
	if !found {
		t.Errorf("FailedChecks = %v, want config", result.FailedChecks)
	}
}

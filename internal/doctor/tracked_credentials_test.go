package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/gittest"
)

// fixtureToken is shaped like a classic GitHub PAT (prefix + 36 base62). It is
// not a real credential.
const fixtureToken = "ghp_" + "0123456789abcdef0123456789abcdef0123"

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	gittest.Run(t, dir, args...)
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

		fs, _ := trackedCredentialFindings(dir)
		if len(fs) != 1 {
			t.Fatalf("findings = %s, want exactly one", findingsText(fs))
		}
		f := fs[0]
		if f.Severity != SeverityBlocker {
			t.Errorf("severity = %s, want blocker", f.Severity)
		}
		ev := f.Evidence
		if ev["path"] != ".nightgauge/config.yaml" || ev["line"] != "4" || ev["pattern"] != "github-token" || ev["redacted"] != "ghp_…" {
			t.Errorf("evidence = %+v, want .nightgauge/config.yaml:4 github-token ghp_…", ev)
		}
		if !strings.Contains(f.Title, ".nightgauge/config.yaml:4") {
			t.Errorf("title does not name path:line: %s", f.Title)
		}
		text := findingsText(fs)
		if !strings.Contains(text, "rotate") || !strings.Contains(text, "history") {
			t.Errorf("remediation does not name both steps: %s", text)
		}

		result := BuildResult([]CheckResult{{ID: trackedCredentialsCheck, Status: StatusFailed, Findings: fs}})
		raw, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), fixtureToken) || strings.Contains(string(raw), "0123456789abcdef") {
			t.Errorf("JSON carries the token: %s", raw)
		}
		for _, want := range []string{
			`.nightgauge/config.yaml:4`, `"path":".nightgauge/config.yaml"`, `"line":"4"`,
			`"pattern":"github-token"`, `"redacted":"ghp_…"`,
		} {
			if !strings.Contains(string(raw), want) {
				t.Errorf("JSON lacks the structured finding's %s: %s", want, raw)
			}
		}
	})

	t.Run("untracked", func(t *testing.T) {
		dir := newCredentialRepo(t)
		writeFixture(t, dir, ".nightgauge/config.yaml", body)

		fs, detail := trackedCredentialFindings(dir)
		if len(fs) != 0 {
			t.Fatalf("untracked file produced a finding: %s", findingsText(fs))
		}
		if !strings.Contains(detail, "no tracked credentials") {
			t.Errorf("detail = %q, want the explicit clean line", detail)
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
		if fs, _ := trackedCredentialFindings(dir); len(fs) == 0 {
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

	fs, _ := trackedCredentialFindings(dir)
	text := findingsText(fs)
	if len(fs) != 7 {
		t.Fatalf("findings = %d (%s), want 7", len(fs), text)
	}
	wantRedacted := []string{"gho_…", "ghu_…", "ghs_…", "ghr_…", "github_pat_…", "ib_live_…", "ib_ci_…"}
	for i, f := range fs {
		if f.Evidence["line"] != strconv.Itoa(i+1) || f.Evidence["redacted"] != wantRedacted[i] {
			t.Errorf("finding %d evidence = %+v, want line %d %s", i, f.Evidence, i+1, wantRedacted[i])
		}
	}
	if strings.Contains(text, strings.Repeat("a", 36)) {
		t.Errorf("findings carry a matched value: %s", text)
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

	fs, detail := trackedCredentialFindings(dir)
	if len(fs) != 0 {
		t.Fatalf("skipped files produced a finding: %s", findingsText(fs))
	}
	for _, want := range []string{".nightgauge/blob.bin (binary)", ".nightgauge/big.log (over 1 MiB)"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail lacks %q: %s", want, detail)
		}
	}

	outside := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(outside))
	fs, detail = trackedCredentialFindings(outside)
	if len(fs) != 0 || !strings.Contains(detail, "skipped") {
		t.Errorf("outside a work tree: %s / %q, want a skip", findingsText(fs), detail)
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

	fs, detail := trackedCredentialFindings(dir)
	if len(fs) != 1 || fs[0].Evidence["path"] != ".nightgauge/env.sh" {
		t.Fatalf("findings = %s, want only the glued token in env.sh", findingsText(fs))
	}
	if !strings.Contains(detail, ".nightgauge/linked/leak.yaml (resolves outside the repository)") {
		t.Errorf("escaping path not noted: %s", detail)
	}
}

// TestRunDoctorRefusedConfigIsRequiredFailure: a config that exists and was
// refused is a failed config check (exit 2), not a fresh repository.
func TestRunDoctorRefusedConfigIsRequiredFailure(t *testing.T) {
	result := RunDoctorWithConfigError(context.Background(), nil, errors.New("config: refused (value redacted)"), nil, nil)
	if result.ExitCode != 2 {
		t.Errorf("ExitCode = %d, want 2", result.ExitCode)
	}
	cfgCheck, _ := checkResult(result, "config")
	if checkPassed(cfgCheck) || !strings.Contains(findingsText(cfgCheck.Findings), "refused") {
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

// TestCredentialFindings pins #2091: a committed ghp_ token and a CI machine
// credential are blockers whose JSON carries only the redacted prefix and key
// names, with manual rotation remedies.
func TestCredentialFindings(t *testing.T) {
	dir := newCredentialRepo(t)
	writeFixture(t, dir, ".nightgauge/config.yaml", "github_auth:\n  token: "+fixtureToken+"\n")
	gitIn(t, dir, "add", ".nightgauge/config.yaml")
	gitIn(t, dir, "commit", "-q", "-m", "fixture")

	fs, _ := trackedCredentialFindings(dir)
	if len(fs) != 1 || fs[0].Code != "NGD024" || fs[0].Severity != SeverityBlocker {
		t.Fatalf("want one NGD024 blocker, got %s", findingsText(fs))
	}
	if fs[0].Evidence["redacted"] != "ghp_…" {
		t.Errorf("evidence = %v, want the redacted prefix", fs[0].Evidence)
	}
	r := fs[0].Remedies[0]
	steps := strings.Join(r.Steps, " ")
	if r.Kind != RemedyManual || r.Verb != "" || !strings.Contains(steps, "git rm --cached") ||
		!strings.Contains(steps, "history") || !strings.Contains(steps, "Rotate") {
		t.Errorf("want a manual rotate + git rm --cached + history remedy, got %+v", r)
	}
	raw, err := json.Marshal(BuildResult([]CheckResult{{ID: trackedCredentialsCheck, Findings: fs}}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), fixtureToken) || !strings.Contains(string(raw), "ghp_…") {
		t.Errorf("JSON must carry only the redacted prefix: %s", raw)
	}

	stubMachineCredentials(t, []string{"license_key", "github_auth.token"}, nil)
	ci, _ := ciMachineCredentialFindings(envOf(map[string]string{"CI": "true"}))
	if len(ci) != 1 || ci[0].Code != "NGD025" || ci[0].Severity != SeverityBlocker ||
		ci[0].Evidence["keys"] != "license_key, github_auth.token" || ci[0].Remedies[0].Kind != RemedyManual {
		t.Errorf("want one NGD025 blocker naming the keys, got %s", findingsText(ci))
	}
}

// TestCredentialFindingsNeverCarryTokens feeds a token-shaped fixture into every
// input of this group's checks that could echo one — errors, config errors,
// board and rate-limit failures — and asserts no finding, evidence value or
// remedy preview carries it after the render boundary.
func TestCredentialFindingsNeverCarryTokens(t *testing.T) {
	leak := errors.New("request failed: Authorization: token " + fixtureToken)
	cfg := &config.Config{Owner: "o", DefaultRepo: "r", ProjectNumber: 3}
	var all []CheckResult
	add := func(fs []Finding, _ string) {
		if len(fs) == 0 {
			t.Fatal("fixture produced no finding")
		}
		all = append(all, CheckResult{ID: fs[0].Check, Findings: fs})
	}
	add(githubAuthFindings(nil, leak))
	add(rateLimitFindings(nil, leak))
	add(configFindings(nil, leak, "/w"))
	add(boardPopulationFindings(cfg, boardPopulation{}, leak))
	add(scopeFindings(&gh.TokenScopeInfo{Valid: false, MissingScopes: []string{"repo"}}))
	add(apiUserFindings(nil))
	add(ghFindings("", leak))
	add(projectFindings(&config.Config{}, "/w"))
	add(githubIdentityFindings(nil, nil))
	stubMachineCredentials(t, nil, leak)
	add(ciMachineCredentialFindings(envOf(map[string]string{"CI": "true"})))

	raw, err := json.Marshal(BuildResult(all))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), fixtureToken) {
		t.Errorf("a finding carries the token: %s", raw)
	}
	for _, r := range all {
		for _, f := range r.Findings {
			for _, rem := range f.Remedies {
				if strings.Contains(rem.Preview+strings.Join(rem.Steps, " "), fixtureToken) {
					t.Errorf("%s remedy carries the token", f.Code)
				}
			}
		}
	}
}

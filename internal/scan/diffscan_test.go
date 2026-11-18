package scan

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
)

// Pinned verbatim from pr-create Step 2.5.3 as it stood before #2702 moved the
// loop into this package. A change to the scanner's set must edit both.
var (
	pinnedCritical = []string{
		`-----BEGIN (RSA |DSA |EC |OPENSSH )?PRIVATE KEY`,
		`aws_secret_access_key\s*[=:]\s*['"][A-Za-z0-9/+=]{20,}['"]`,
		`aws_access_key_id\s*[=:]\s*['"]AKIA[0-9A-Z]{16}['"]`,
		`(jdbc|postgresql|mysql|mongodb):\/\/[^:]+:[^@]+@`,
	}
	pinnedWarning = []string{
		`(api[_-]?key|apikey)\s*[=:]\s*['"][^'"]{16,}['"]`,
		`secret\s*[=:]\s*['"][^'"]{10,}['"]`,
		`password\s*[=:]\s*['"][^'"]{6,}['"]`,
		`token\s*[=:]\s*['"][^'"]{20,}['"]`,
		`authorization\s*[=:]\s*['"]Bearer [^'"]{20,}['"]`,
	}
	pinnedExclude = `example|placeholder|YOUR_|xxx|test|mock|REDACTED|\*\*\*|TODO|fake|dummy|sample|changeme|<.*>`
)

// scanDiffFile reads a fixture from disk and scans it.
func scanDiffFile(abs, rel string) (DiffSecretsFile, bool, error) {
	data, err := os.ReadFile(abs)
	if err != nil {
		return DiffSecretsFile{}, false, err
	}
	return scanDiffContent(data, rel)
}

func TestDiffSecretsPatternsPinned(t *testing.T) {
	if !reflect.DeepEqual(DiffSecretsCriticalPatterns, pinnedCritical) {
		t.Errorf("critical patterns drifted: %q", DiffSecretsCriticalPatterns)
	}
	if !reflect.DeepEqual(DiffSecretsWarningPatterns, pinnedWarning) {
		t.Errorf("warning patterns drifted: %q", DiffSecretsWarningPatterns)
	}
	if DiffSecretsExcludePattern != pinnedExclude {
		t.Errorf("exclude pattern drifted: %q", DiffSecretsExcludePattern)
	}
}

// Fixture values are assembled so this file carries no literal secret shape.
var (
	awsSecret = "wJalrXUtnFEMI" + "K7MDENGbPxRfiCYzzzzKEY"
	awsID     = "AKIA" + "ABCDEFGHIJKLMNOP"
)

func TestScanDiffFile_EachPattern(t *testing.T) {
	cases := []struct {
		name, line, severity string
	}{
		{"pem", "-----BEGIN RSA PRIV" + "ATE KEY-----", "critical"},
		{"aws_secret", `aws_secret_access_key = "` + awsSecret + `"`, "critical"},
		{"aws_id", `AWS_ACCESS_KEY_ID: '` + awsID + `'`, "critical"},
		{"conn", "postgresql://admin:hunter2@db.internal/app", "critical"},
		{"apikey", `api_key = "abcdefghijklmnopqrstu"`, "warning"},
		{"secret", `secret: "abcdefghijkl"`, "warning"},
		{"password", `password = "hunter22"`, "warning"},
		{"token", `token = "abcdefghijklmnopqrstuvwxyz"`, "warning"},
		{"bearer", `authorization: "Bearer abcdefghijklmnopqrstuvwxyz"`, "warning"},
	}
	dir := t.TempDir()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := filepath.Join(dir, c.name+".txt")
			if err := os.WriteFile(p, []byte("clean\n"+c.line+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			f, ok, err := scanDiffFile(p, c.name+".txt")
			if err != nil || !ok {
				t.Fatalf("file skipped: %v", err)
			}
			if len(f.Findings) != 1 {
				t.Fatalf("want one finding, got %+v", f.Findings)
			}
			crit, warn := 0, 0
			if c.severity == "critical" {
				crit = 1
			} else {
				warn = 1
			}
			if f.Critical != crit || f.Warnings != warn {
				t.Fatalf("got critical=%d warnings=%d, want %d/%d (%+v)", f.Critical, f.Warnings, crit, warn, f.Findings)
			}
			if f.Findings[0].Lines[0] != 2 {
				t.Errorf("line = %v, want [2]", f.Findings[0].Lines)
			}
		})
	}
}

func TestScanDiffFile_ExcludeFilter(t *testing.T) {
	dir := t.TempDir()
	body := `password = "changeme123"` + "\n" +
		`password = "<your-password>"` + "\n" +
		`password = "hunter22" // example` + "\n" +
		`password = "hunter22"` + "\n"
	p := filepath.Join(dir, "conf.txt")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	f, ok, err := scanDiffFile(p, "conf.txt")
	if err != nil || !ok {
		t.Fatalf("skipped: %v", err)
	}
	if f.Warnings != 1 || f.Findings[0].Lines[0] != 4 {
		t.Fatalf("want only line 4, got %+v", f)
	}
	// The filter sees "path:n:content", so a path containing "test" hides all hits.
	f, ok, _ = scanDiffFile(p, "pkg/test_conf.txt")
	if !ok || f.Warnings != 0 {
		t.Fatalf("path filter not applied: %+v", f)
	}
}

func TestScanDiffFile_CountsAllLinesSamplesFive(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.txt")
	line := `password = "hunter22" secret = "abcdefghijkl"` + "\n"
	body := ""
	for i := 0; i < 7; i++ {
		body += line
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	f, ok, err := scanDiffFile(p, "c.txt")
	if err != nil || !ok {
		t.Fatalf("skipped: %v", err)
	}
	if f.Warnings != 14 || len(f.Findings) != 2 {
		t.Fatalf("want 14 warnings over 2 patterns, got %+v", f)
	}
	for _, fd := range f.Findings {
		if fd.Count != 7 || !reflect.DeepEqual(fd.Lines, []int{1, 2, 3, 4, 5}) {
			t.Errorf("finding %+v: want count 7, lines 1-5", fd)
		}
	}
}

func TestRunDiffSecretsScan_Repo(t *testing.T) {
	repo := gittest.InitRepo(t, t.TempDir(), "-q", "-b", "main")
	write := func(rel, s string) {
		p := filepath.Join(repo, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	commit := func(msg string) {
		gittest.Run(t, repo, "add", "-A")
		gittest.Run(t, repo, "-c", "user.email=t@e.com", "-c", "user.name=T",
			"-c", "commit.gpgsign=false", "commit", "-q", "-m", msg)
	}
	write("gone.txt", "x\n")
	write("old.txt", `password = "hunter22"`+"\n")
	commit("base")
	gittest.Run(t, repo, "checkout", "-q", "-b", "feat")
	// Many changed files: the scan must not put the diff on argv.
	for i := 0; i < 3000; i++ {
		write(fmt.Sprintf("many/%04d-%s.txt", i, strings.Repeat("x", 60)), "ok\n")
	}
	write("b/crit.go", "-----BEGIN PRIV"+"ATE KEY-----\n")
	write("a/warn.go", `password = "hunter22"`+"\n")
	write("clean.go", "package x\n")
	write(".nightgauge/x.txt", "-----BEGIN PRIV"+"ATE KEY-----\n")
	write("node_modules/x.js", "-----BEGIN PRIV"+"ATE KEY-----\n")
	write("pkg/node_modules/x.js", "-----BEGIN PRIV"+"ATE KEY-----\n")
	write("dist/x.js", "-----BEGIN PRIV"+"ATE KEY-----\n")
	write("bin.dat", "\x00-----BEGIN PRIV"+"ATE KEY-----\n")
	if err := os.Remove(filepath.Join(repo, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	commit("feat")
	// A secret landing on main after the branch point is outside base...HEAD.
	gittest.Run(t, repo, "checkout", "-q", "main")
	write("later.go", "-----BEGIN PRIV"+"ATE KEY-----\n")
	commit("main moves")
	gittest.Run(t, repo, "checkout", "-q", "feat")

	ctx := context.Background()
	res, err := RunDiffSecretsScan(ctx, DiffSecretsOptions{Workdir: repo, Base: "main"})
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, f := range res.Files {
		paths = append(paths, f.Path)
	}
	if len(paths) != 3003 {
		t.Fatalf("scanned %d files, want 3003", len(paths))
	}
	paths = paths[:3]
	if want := []string{"a/warn.go", "b/crit.go", "clean.go"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
	if res.Critical != 1 || res.Warnings != 1 || res.FilesScanned != 3003 || res.V != 1 {
		t.Fatalf("totals: %+v", res)
	}
	if res.HeadSHA != gittest.Run(t, repo, "rev-parse", "HEAD") {
		t.Errorf("head_sha = %s", res.HeadSHA)
	}

	a, _ := json.Marshal(res)
	res2, err := RunDiffSecretsScan(ctx, DiffSecretsOptions{Workdir: repo, Base: "main"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(res2)
	if string(a) != string(b) {
		t.Errorf("output not deterministic:\n%s\n%s", a, b)
	}

	// The scan reads HEAD, not the checkout: an uncommitted fix does not
	// clear the committed secret, and an uncommitted secret is not HEAD's.
	write("b/crit.go", "fixed\n")
	write("clean.go", "-----BEGIN PRIV"+"ATE KEY-----\n")
	if err := os.Remove(filepath.Join(repo, "a", "warn.go")); err != nil {
		t.Fatal(err)
	}
	res3, err := RunDiffSecretsScan(ctx, DiffSecretsOptions{Workdir: repo, Base: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := json.Marshal(res3); string(c) != string(a) {
		t.Errorf("dirty working tree changed the result:\n%s\n%s", a, c)
	}

	for bad, want := range map[string]string{
		"":            "--base is required",
		"no-such-ref": "does not resolve",
		"--output=x":  "invalid base ref",
	} {
		_, err := RunDiffSecretsScan(ctx, DiffSecretsOptions{Workdir: repo, Base: bad})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("base %q: err = %v, want %q", bad, err, want)
		}
	}
	if _, err := RunDiffSecretsScan(ctx, DiffSecretsOptions{Workdir: t.TempDir(), Base: "main"}); err == nil ||
		!strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("non-repo workdir: err = %v", err)
	}
}

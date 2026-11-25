package format

import (
	"context"
	"github.com/nightgauge/nightgauge/internal/gittest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// dartRepo builds a temp git repo with a pubspec on main, then a feature
// branch, and puts a fake `dart` on PATH that logs each argv line. A fake that
// sees "tool/bad.dart" exits 65, mimicking a parse error in an excluded fixture.
func dartRepo(t *testing.T) (dir, log string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake dart is a shell script")
	}
	dir = t.TempDir()
	bin := t.TempDir()
	log = filepath.Join(bin, "dart.log")
	script := "#!/bin/sh\necho \"$*\" >> " + log + "\ncase \"$*\" in *tool/bad.dart*) exit 65;; esac\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "dart"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	write(t, dir, "pubspec.yaml", "name: app\n")
	write(t, dir, "analysis_options.yaml",
		"analyzer:\n  exclude:\n    - \"lib/**/*.g.dart\"\n    - \"tool/**\"\n")
	write(t, dir, "lib/old.dart", "void a() {}\n")
	write(t, dir, "lib/model.g.dart", "void g() {}\n")
	gitT(t, dir, "init", "-q", "-b", "main")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-q", "-m", "init")
	gitT(t, dir, "checkout", "-q", "-b", "feat")
	return dir, log
}

func write(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com",
		"-c", "commit.gpgsign=false"}, args...)
	gittest.Run(t, dir, full...)
}

func readLog(t *testing.T, log string) string {
	t.Helper()
	b, err := os.ReadFile(log)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRunDart_ScopesToChangedFilesOutsideExcludes(t *testing.T) {
	dir, log := dartRepo(t)
	write(t, dir, "lib/committed.dart", "void c() {}\n")
	write(t, dir, "lib/model.g.dart", "void g2() {}\n") // changed but excluded (AC 1)
	write(t, dir, "tool/bad.dart", "not dart(\n")       // excluded fixture that fails dart (AC 3)
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-q", "-m", "feat")
	write(t, dir, "lib/untracked.dart", "void u() {}\n") // untracked edit counts

	res, err := RunFormat(context.Background(), dir, Options{Base: "main"})
	if err != nil {
		t.Fatalf("RunFormat: %v (output %q)", err, res.Output)
	}
	if !res.Ran || res.Formatter != "dart format" {
		t.Fatalf("result = %+v", res)
	}
	got := readLog(t, log)
	want := "fix --apply -- lib/committed.dart\nfix --apply -- lib/untracked.dart\n" +
		"format -- lib/committed.dart lib/untracked.dart\n"
	if got != want {
		t.Fatalf("dart invocations:\n%s\nwant:\n%s", got, want)
	}
	for _, never := range []string{"model.g.dart", "tool/bad.dart", "old.dart", " ."} {
		if strings.Contains(got, never) {
			t.Errorf("dart was given %q:\n%s", never, got)
		}
	}
}

func TestRunDart_NoChangedDartFiles_SkipsWithoutInvokingDart(t *testing.T) {
	dir, log := dartRepo(t)
	write(t, dir, "lib/model.g.dart", "void g2() {}\n") // only an excluded change
	write(t, dir, "README.md", "x\n")

	res, err := RunFormat(context.Background(), dir, Options{})
	if err != nil {
		t.Fatalf("RunFormat: %v", err)
	}
	if res.Ran {
		t.Fatalf("Ran = true, want false: %+v", res)
	}
	if got := readLog(t, log); got != "" {
		t.Fatalf("dart invoked: %q", got)
	}
}

func TestRunDart_UnstagedEditIncluded(t *testing.T) {
	dir, log := dartRepo(t)
	write(t, dir, "lib/old.dart", "void a2() {}\n")

	if _, err := RunFormat(context.Background(), dir, Options{}); err != nil {
		t.Fatalf("RunFormat: %v", err)
	}
	if got := readLog(t, log); !strings.Contains(got, "format -- lib/old.dart\n") {
		t.Fatalf("unstaged edit not formatted: %q", got)
	}
}

func TestRunDart_UnresolvableBaseErrors(t *testing.T) {
	dir, log := dartRepo(t)
	write(t, dir, "lib/x.dart", "void x() {}\n")
	_, err := RunFormat(context.Background(), dir, Options{Base: "nope"})
	if err == nil || !strings.Contains(err.Error(), `base branch "nope" does not resolve`) {
		t.Fatalf("err = %v, want unresolvable-base error", err)
	}
	if got := readLog(t, log); got != "" {
		t.Fatalf("dart invoked despite error: %q", got)
	}
}

func TestRunDart_MalformedAnalysisOptionsErrors(t *testing.T) {
	dir, log := dartRepo(t)
	write(t, dir, "analysis_options.yaml", "analyzer: [\n")
	write(t, dir, "lib/x.dart", "void x() {}\n")
	_, err := RunFormat(context.Background(), dir, Options{})
	if err == nil || !strings.Contains(err.Error(), "parse analysis_options.yaml") {
		t.Fatalf("err = %v, want parse error", err)
	}
	if got := readLog(t, log); got != "" {
		t.Fatalf("dart invoked despite malformed options: %q", got)
	}
}

func TestRunDart_DeletedFileNeverPassed(t *testing.T) {
	dir, log := dartRepo(t)
	gitT(t, dir, "rm", "-q", "lib/old.dart")
	gitT(t, dir, "commit", "-q", "-m", "rm")
	write(t, dir, "lib/new.dart", "void n() {}\n")
	if _, err := RunFormat(context.Background(), dir, Options{}); err != nil {
		t.Fatalf("RunFormat: %v", err)
	}
	got := readLog(t, log)
	if strings.Contains(got, "old.dart") || !strings.Contains(got, "format -- lib/new.dart\n") {
		t.Fatalf("dart invocations: %q", got)
	}
}

func TestRunDart_PackageSubdirectoryUsesPackageRelativePaths(t *testing.T) {
	dir, log := dartRepo(t)
	write(t, dir, "app/pubspec.yaml", "name: sub\n")
	write(t, dir, "app/analysis_options.yaml", "analyzer:\n  exclude:\n    - gen/**\n")
	write(t, dir, "app/lib/a.dart", "void a() {}\n")
	write(t, dir, "app/gen/b.dart", "void b() {}\n")
	write(t, dir, "lib/outside.dart", "void o() {}\n")
	if _, err := RunFormat(context.Background(), filepath.Join(dir, "app"), Options{}); err != nil {
		t.Fatalf("RunFormat: %v", err)
	}
	want := "fix --apply -- lib/a.dart\nformat -- lib/a.dart\n"
	if got := readLog(t, log); got != want {
		t.Fatalf("dart invocations:\n%s\nwant:\n%s", got, want)
	}
}

func TestRunDart_NoAnalysisOptionsMeansNoExcludes(t *testing.T) {
	dir, log := dartRepo(t)
	if err := os.Remove(filepath.Join(dir, "analysis_options.yaml")); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "lib/model.g.dart", "void g2() {}\n")
	if _, err := RunFormat(context.Background(), dir, Options{}); err != nil {
		t.Fatalf("RunFormat: %v", err)
	}
	if got := readLog(t, log); !strings.Contains(got, "format -- lib/model.g.dart\n") {
		t.Fatalf("dart invocations: %q", got)
	}
}

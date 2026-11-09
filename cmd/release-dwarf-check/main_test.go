package main

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fixture is one cross-compiled Go binary built for the tests.
type fixture struct {
	name     string
	goos     string
	goarch   string
	stripped bool
}

var fixtures = []fixture{
	{"linux-stripped", "linux", "amd64", true},
	{"linux-unstripped", "linux", "amd64", false},
	{"darwin-stripped", "darwin", "arm64", true},
	{"darwin-unstripped", "darwin", "arm64", false},
}

var (
	buildOnce sync.Once
	builtDir  string
	buildErr  error
	buildOut  []byte
)

// buildFixtures compiles a tiny program once per test binary, with and without
// the linker strip flags, the same way the release builds it (-trimpath,
// CGO_ENABLED=0).
func buildFixtures(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "release-dwarf-check-fixtures-*")
		if err != nil {
			buildErr = err
			return
		}
		builtDir = dir
		src := filepath.Join(dir, "src")
		if err := os.MkdirAll(src, 0o755); err != nil {
			buildErr = err
			return
		}
		files := map[string]string{
			"go.mod":  "module fixture\n\ngo 1.21\n",
			"main.go": "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"fixture\") }\n",
		}
		for name, body := range files {
			if err := os.WriteFile(filepath.Join(src, name), []byte(body), 0o644); err != nil {
				buildErr = err
				return
			}
		}
		for _, fx := range fixtures {
			args := []string{"build", "-trimpath", "-o", filepath.Join(dir, fx.name)}
			if fx.stripped {
				args = append(args, "-ldflags=-s -w")
			}
			args = append(args, ".")
			cmd := exec.Command("go", args...)
			cmd.Dir = src
			cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+fx.goos, "GOARCH="+fx.goarch,
				"GOFLAGS=", "GOWORK=off")
			if out, err := cmd.CombinedOutput(); err != nil {
				buildErr, buildOut = err, out
				return
			}
		}
	})
	if buildErr != nil {
		t.Fatalf("HARNESS ERROR: building fixtures: %v\n%s", buildErr, buildOut)
	}
	return builtDir
}

func TestMain(m *testing.M) {
	code := m.Run()
	if builtDir != "" {
		os.RemoveAll(builtDir)
	}
	os.Exit(code)
}

func runCheck(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return code, stdout.String() + stderr.String()
}

func wantCode(fx fixture) int {
	if fx.stripped {
		return 1
	}
	return 0
}

func TestRawBinaries(t *testing.T) {
	dir := buildFixtures(t)
	for _, fx := range fixtures {
		t.Run(fx.name, func(t *testing.T) {
			code, out := runCheck(t, filepath.Join(dir, fx.name))
			if code != wantCode(fx) {
				t.Fatalf("exit %d, want %d; output:\n%s", code, wantCode(fx), out)
			}
			if fx.stripped && !strings.Contains(out, record) {
				t.Errorf("finding does not name %s:\n%s", record, out)
			}
		})
	}
}

func writeTarGz(t *testing.T, path string, entries map[string][]byte) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)),
			Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeZip(t *testing.T, path string, entries map[string][]byte) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, body := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPackagedBinaries(t *testing.T) {
	dir := buildFixtures(t)
	for _, fx := range fixtures {
		bin, err := os.ReadFile(filepath.Join(dir, fx.name))
		if err != nil {
			t.Fatal(err)
		}
		t.Run(fx.name+"/tar.gz", func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), "nightgauge_0.0.0_"+fx.goos+"_"+fx.goarch+".tar.gz")
			writeTarGz(t, archive, map[string][]byte{
				"nightgauge": bin,
				"LICENSE":    []byte("Apache-2.0\n"),
			})
			if code, out := runCheck(t, archive); code != wantCode(fx) {
				t.Fatalf("exit %d, want %d; output:\n%s", code, wantCode(fx), out)
			}
		})
		t.Run(fx.name+"/vsix", func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), "nightgauge-"+fx.goos+".vsix")
			writeZip(t, archive, map[string][]byte{
				"extension/dist/bin/nightgauge": bin,
				"extension/package.json":        []byte("{}\n"),
			})
			if code, out := runCheck(t, archive); code != wantCode(fx) {
				t.Fatalf("exit %d, want %d; output:\n%s", code, wantCode(fx), out)
			}
		})
	}
}

// One stripped binary among clean ones still fails the whole run.
func TestMixedRunFails(t *testing.T) {
	dir := buildFixtures(t)
	code, out := runCheck(t, filepath.Join(dir, "linux-unstripped"), filepath.Join(dir, "darwin-stripped"))
	if code != 1 {
		t.Fatalf("exit %d, want 1; output:\n%s", code, out)
	}
}

func TestNothingAssertedExitsTwo(t *testing.T) {
	tmp := t.TempDir()
	empty := filepath.Join(tmp, "empty.tar.gz")
	writeTarGz(t, empty, map[string][]byte{"LICENSE": []byte("x\n")})
	emptyVsix := filepath.Join(tmp, "empty.vsix")
	writeZip(t, emptyVsix, map[string][]byte{"extension/package.json": []byte("{}\n")})
	notBinary := filepath.Join(tmp, "README")
	if err := os.WriteFile(notBinary, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := map[string][]string{
		"no args":            nil,
		"missing path":       {filepath.Join(tmp, "absent.tar.gz")},
		"unmatched glob":     {"dist/*.tar.gz"},
		"tar.gz with no bin": {empty},
		"vsix with no bin":   {emptyVsix},
		"raw non-binary":     {notBinary},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			code, out := runCheck(t, args...)
			if code != 2 {
				t.Fatalf("exit %d, want 2; output:\n%s", code, out)
			}
			if !strings.Contains(out, "HARNESS ERROR") {
				t.Errorf("no HARNESS ERROR line:\n%s", out)
			}
		})
	}
}

// --- Flag guard: the linker strip flags must never return. ---

const repoRoot = "../.."

func stripFlagIn(line string) string {
	fields := strings.FieldsFunc(line, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '"' || r == '\'' || r == '='
	})
	for _, f := range fields {
		if f == "-s" || f == "-w" {
			return f
		}
	}
	return ""
}

// ldflagsLines returns the non-comment lines of path that feed the Go linker:
// in the Makefile every line naming ldflags/LDFLAGS, in .goreleaser.yml every
// list item under an `ldflags:` key and any inline `-ldflags`.
func ldflagsLines(t *testing.T, path string, yaml bool) map[int]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("HARNESS ERROR: %v", err)
	}
	defer f.Close()
	got := map[int]string{}
	sc := bufio.NewScanner(f)
	inList, listIndent := false, 0
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		if yaml {
			if inList && (indent < listIndent || (indent == listIndent && !strings.HasPrefix(trimmed, "-"))) {
				inList = false
			}
			if strings.HasPrefix(trimmed, "ldflags:") {
				inList, listIndent = true, indent
				if rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "ldflags:")); rest != "" {
					got[n] = rest
				}
				continue
			}
			if inList && strings.HasPrefix(trimmed, "-") {
				got[n] = trimmed[1:]
				continue
			}
		}
		if strings.Contains(strings.ToLower(line), "ldflags") {
			got[n] = line
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("HARNESS ERROR: %v", err)
	}
	return got
}

func TestNoLinkerStripFlags(t *testing.T) {
	for _, tc := range []struct {
		file string
		yaml bool
	}{{"Makefile", false}, {".goreleaser.yml", true}} {
		t.Run(tc.file, func(t *testing.T) {
			lines := ldflagsLines(t, filepath.Join(repoRoot, tc.file), tc.yaml)
			if len(lines) == 0 {
				t.Fatalf("HARNESS ERROR: no ldflags line found in %s; the guard would check nothing", tc.file)
			}
			sawVersion := false
			for n, line := range lines {
				if f := stripFlagIn(line); f != "" {
					t.Errorf("Go linker strip flag %q in %s:%d; release binaries ship unstripped — see %s",
						f, tc.file, n, record)
				}
				if strings.Contains(line, "main.version") {
					sawVersion = true
				}
			}
			if !sawVersion {
				t.Errorf("%s ldflags no longer set main.version; the guard may be reading the wrong lines", tc.file)
			}
		})
	}
}

func TestStripFlagDetector(t *testing.T) {
	bad := []string{
		`LDFLAGS := -ldflags "-s -w -X main.version=1"`,
		`      - -w -X main.version={{.Version}}`,
		`go build -ldflags=-s ./cmd/nightgauge`,
	}
	for _, l := range bad {
		if stripFlagIn(l) == "" {
			t.Errorf("detector missed a strip flag in %q", l)
		}
	}
	good := []string{
		`LDFLAGS := -ldflags "-X main.version=$(VERSION) -X 'main.vendor=$(VENDOR)'"`,
		`- -X main.version={{.Version}} -X "main.vendor=Edibu, LLC"`,
	}
	for _, l := range good {
		if f := stripFlagIn(l); f != "" {
			t.Errorf("detector flagged %q in clean line %q", f, l)
		}
	}
}

// --- Workflow guard: the DWARF check runs before every VirusTotal scan and fails closed. ---

const dwarfStepName = "Verify every shipped binary carries DWARF"

// stepBlock returns the text of the workflow step whose `- name:` line holds
// name, and the byte offset where it starts.
func stepBlock(t *testing.T, wf, name string) (string, int) {
	t.Helper()
	start := strings.Index(wf, "- name: "+name)
	if start < 0 {
		t.Fatalf("no %q step; the release must check DWARF before scanning (see %s)", name, record)
	}
	rest := wf[start+1:]
	end := strings.Index(rest, "\n      - ")
	if end < 0 {
		end = len(rest)
	}
	return wf[start : start+1+end], start
}

func assertFailsClosed(t *testing.T, file, block string) {
	t.Helper()
	for _, bad := range []string{"continue-on-error", "|| true", "|| :", "exit 0", "if: "} {
		if strings.Contains(block, bad) {
			t.Errorf("%s DWARF step contains %q; it must fail on exit 1 and exit 2 alike (see %s)",
				file, bad, record)
		}
	}
	if !strings.Contains(block, "./cmd/release-dwarf-check") ||
		!strings.Contains(block, "dist/*.tar.gz") || !strings.Contains(block, "packages/nightgauge/*.vsix") {
		t.Errorf("%s DWARF step does not run the tool over every archive and VSIX:\n%s", file, block)
	}
}

func readWorkflow(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot, ".github", "workflows", name))
	if err != nil {
		t.Fatalf("HARNESS ERROR: %v", err)
	}
	return string(b)
}

func TestReleaseWorkflowChecksDWARFBeforeDraftScan(t *testing.T) {
	wf := readWorkflow(t, "release.yml")
	block, at := stepBlock(t, wf, dwarfStepName)
	assertFailsClosed(t, "release.yml", block)
	scan := strings.Index(wf, "release-scan-gate.sh scan")
	if scan < 0 {
		t.Fatal("HARNESS ERROR: release.yml has no release-scan-gate.sh scan step")
	}
	if at > scan {
		t.Errorf("release.yml runs the DWARF check after the draft scan; it must run first (see %s)", record)
	}
	if verify := strings.Index(wf, "verify-archive-binaries.sh dist bin"); verify < 0 || at < verify {
		t.Errorf("release.yml DWARF check must run after the archives are verified against bin/")
	}
}

func TestStagingWorkflowChecksDWARFBeforeRCScan(t *testing.T) {
	wf := readWorkflow(t, "staging.yml")
	block, at := stepBlock(t, wf, dwarfStepName)
	assertFailsClosed(t, "staging.yml", block)
	dryRun := strings.Index(wf, "\n  dry-run:\n")
	rcScan := strings.Index(wf, "\n  rc-scan:\n")
	if dryRun < 0 || rcScan < 0 || rcScan < dryRun {
		t.Fatal("HARNESS ERROR: staging.yml jobs dry-run and rc-scan not found in that order")
	}
	if at < dryRun || at > rcScan {
		t.Errorf("staging.yml DWARF check is not in the dry-run job, which rc-scan needs (see %s)", record)
	}
	if !strings.Contains(wf[rcScan:], "needs: dry-run") {
		t.Errorf("staging.yml rc-scan no longer needs dry-run, so the DWARF check no longer precedes the scan")
	}
}

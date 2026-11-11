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
	// compressed builds with the linker's default compressed DWARF; the
	// release builds use -compressdwarf=false (#3192).
	compressed bool
}

var fixtures = []fixture{
	{"linux-stripped", "linux", "amd64", true, false},
	{"linux-unstripped", "linux", "amd64", false, false},
	{"linux-compressed", "linux", "amd64", false, true},
	{"darwin-stripped", "darwin", "arm64", true, false},
	{"darwin-unstripped", "darwin", "arm64", false, false},
	{"darwin-compressed", "darwin", "arm64", false, true},
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
			switch {
			case fx.stripped:
				args = append(args, "-ldflags=-s -w")
			case !fx.compressed:
				args = append(args, "-ldflags=-compressdwarf=false")
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
	if fx.stripped || fx.compressed {
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
			if wantCode(fx) != 0 && !strings.Contains(out, record) {
				t.Errorf("finding does not name %s:\n%s", record, out)
			}
			if fx.compressed && !strings.Contains(out, "compressed DWARF") {
				t.Errorf("compressed fixture not reported as compressed DWARF:\n%s", out)
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

// releaseBuildTargets are the Makefile's release binaries: every one must be
// linked with uncompressed DWARF (#3192). build-cli, the native dev build, is
// not shipped and is not checked.
var releaseBuildTargets = []string{
	"nightgauge-darwin-arm64",
	"nightgauge-darwin-amd64",
	"nightgauge-linux-amd64",
}

const noCompressFlag = "-compressdwarf=false"

// makefileReleaseLDFlags returns, for each release target, the expanded
// ldflags its `go build` line uses: the line's own text plus the definition of
// every $(VAR) it names.
func makefileReleaseLDFlags(t *testing.T, mk string) map[string]string {
	t.Helper()
	defs := map[string]string{}
	for _, line := range strings.Split(mk, "\n") {
		if name, val, ok := strings.Cut(line, ":="); ok {
			if name = strings.TrimSpace(name); name != "" && !strings.ContainsAny(name, " \t#") {
				defs[name] = val
			}
		}
	}
	got := map[string]string{}
	for _, line := range strings.Split(mk, "\n") {
		if !strings.Contains(line, "go build") {
			continue
		}
		for _, target := range releaseBuildTargets {
			if !strings.Contains(line, "$(BIN_DIR)/"+target+" ") {
				continue
			}
			expanded := line
			for rest := line; ; {
				i := strings.Index(rest, "$(")
				if i < 0 {
					break
				}
				j := strings.Index(rest[i:], ")")
				if j < 0 {
					break
				}
				expanded += " " + defs[rest[i+2:i+j]]
				rest = rest[i+j:]
			}
			got[target] = expanded
		}
	}
	return got
}

func TestReleaseBuildsKeepDWARFUncompressed(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot, "Makefile"))
	if err != nil {
		t.Fatalf("HARNESS ERROR: %v", err)
	}
	builds := makefileReleaseLDFlags(t, string(b))
	for _, target := range releaseBuildTargets {
		line, ok := builds[target]
		if !ok {
			t.Fatalf("HARNESS ERROR: no go build line for %s in the Makefile; the guard would check nothing", target)
		}
		if !strings.Contains(line, noCompressFlag) {
			t.Errorf("Makefile builds %s without %s; release binaries keep DWARF uncompressed (see %s)",
				target, noCompressFlag, record)
		}
	}
	lines := ldflagsLines(t, filepath.Join(repoRoot, ".goreleaser.yml"), true)
	if len(lines) == 0 {
		t.Fatal("HARNESS ERROR: no ldflags item in .goreleaser.yml; the guard would check nothing")
	}
	for n, line := range lines {
		if !strings.Contains(line, noCompressFlag) {
			t.Errorf(".goreleaser.yml:%d ldflags lack %s; it must build the way the Makefile does (see %s)",
				n, noCompressFlag, record)
		}
	}
}

// The guard must go red when a release target loses the flag.
func TestReleaseLDFlagsDetector(t *testing.T) {
	mk := `RELEASE_LDFLAGS := -ldflags "-compressdwarf=false -X main.version=$(VERSION)"
LDFLAGS := -ldflags "-X main.version=$(VERSION)"
x:
	GOOS=darwin GOARCH=arm64 go build $(RELEASE_LDFLAGS) -o $(BIN_DIR)/nightgauge-darwin-arm64 ./cmd/nightgauge
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o $(BIN_DIR)/nightgauge-linux-amd64 ./cmd/nightgauge
`
	got := makefileReleaseLDFlags(t, mk)
	if !strings.Contains(got["nightgauge-darwin-arm64"], noCompressFlag) {
		t.Errorf("detector missed the flag through $(RELEASE_LDFLAGS): %q", got["nightgauge-darwin-arm64"])
	}
	if line, ok := got["nightgauge-linux-amd64"]; !ok || strings.Contains(line, noCompressFlag) {
		t.Errorf("detector did not report the linux build as lacking the flag: %q", line)
	}
	if _, ok := got["nightgauge-darwin-amd64"]; ok {
		t.Errorf("detector invented a build line for a target the Makefile does not build")
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
		t.Fatalf("no %q step; the release workflows must keep it (see %s)", name, record)
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

// --- Margin scan guard (#3192): every RC builds and scans the shipped binaries
// at three neighbouring version strings, and a release needs a margin-clean RC. ---

const (
	marginStepName     = "Build the margin scan binaries"
	marginUploadName   = "Upload margin scan inputs"
	marginDownloadName = "Download margin scan inputs"
	marginRecordName   = "Upload the margin scan record"
	marginGateName     = "Require an RC on this commit with a clean margin scan"
	rcScanStepName     = "Scan every archive and VSIX"
	rcScanCondition    = "if: startsWith(github.ref, 'refs/tags/v') && inputs.version == ''"
)

// assertNoSwallow fails when a step can pass while its command fails.
func assertNoSwallow(t *testing.T, file, name, block string) {
	t.Helper()
	for _, bad := range []string{"continue-on-error", "|| true", "|| :", "exit 0"} {
		if strings.Contains(block, bad) {
			t.Errorf("%s step %q contains %q; it must fail closed (see %s)", file, name, bad, record)
		}
	}
}

// jobSpan returns the byte range of the job whose header is `  name:`.
func jobSpan(t *testing.T, wf, file, name string) (int, int) {
	t.Helper()
	start := strings.Index(wf, "\n  "+name+":\n")
	if start < 0 {
		t.Fatalf("HARNESS ERROR: %s has no %s job", file, name)
	}
	end := len(wf)
	for i := start + 1; i < len(wf); {
		j := strings.Index(wf[i:], "\n  ")
		if j < 0 {
			break
		}
		i += j + 1
		if i+2 < len(wf) && wf[i+2] != ' ' && wf[i+2] != '#' && wf[i+2] != '\n' {
			end = i
			break
		}
	}
	return start, end
}

func TestStagingWorkflowBuildsMarginBinaries(t *testing.T) {
	wf := readWorkflow(t, "staging.yml")
	dryStart, dryEnd := jobSpan(t, wf, "staging.yml", "dry-run")
	block, at := stepBlock(t, wf, marginStepName)
	if at < dryStart || at > dryEnd {
		t.Fatalf("staging.yml %q is not in the dry-run job", marginStepName)
	}
	assertNoSwallow(t, "staging.yml", marginStepName, block)
	if !strings.Contains(block, "scripts/release-margin-builds.sh \"$VERSION\"") {
		t.Errorf("staging.yml %q does not run scripts/release-margin-builds.sh on the RC version:\n%s",
			marginStepName, block)
	}
	if !strings.Contains(block, "./cmd/release-dwarf-check") || !strings.Contains(block, "nightgauge-margin_*.tar.gz") {
		t.Errorf("staging.yml %q does not DWARF-check the margin archives:\n%s", marginStepName, block)
	}
	// The margin builds are scan inputs only. Notarizing them cannot change a
	// bare Mach-O and can stall dry-run on Apple's queue (#3192).
	for _, secret := range []string{"APPLE_ID:", "APPLE_TEAM_ID:", "APPLE_APP_PASSWORD:"} {
		if strings.Contains(block, secret) {
			t.Errorf("staging.yml %q carries %s; margin builds must not be notarized:\n%s",
				marginStepName, strings.TrimSuffix(secret, ":"), block)
		}
	}
	if !strings.Contains(block, rcScanCondition) {
		t.Errorf("staging.yml %q must run exactly when rc-scan does (%s):\n%s", marginStepName, rcScanCondition, block)
	}
	_, sign := stepBlock(t, wf, "Sign the macOS binaries")
	if at < sign {
		t.Errorf("staging.yml builds the margin binaries before the RC's own are signed")
	}
	upload, up := stepBlock(t, wf, marginUploadName)
	if up < at || up > dryEnd {
		t.Errorf("staging.yml %q must follow the margin build inside dry-run", marginUploadName)
	}
	for _, want := range []string{"name: rc-margin-${{ steps.version.outputs.tag }}", "if-no-files-found: error",
		"nightgauge-margin_*.tar.gz", "margin-manifest.txt", rcScanCondition} {
		if !strings.Contains(upload, want) {
			t.Errorf("staging.yml %q lacks %q:\n%s", marginUploadName, want, upload)
		}
	}
	rc, _ := stepBlock(t, wf, "Upload RC artifacts for manual verification")
	if strings.Contains(rc, "margin") {
		t.Errorf("staging.yml uploads margin builds in rc-artifacts; they are scan inputs only and never published")
	}
}

func TestRCScanScansMarginBinaries(t *testing.T) {
	wf := readWorkflow(t, "staging.yml")
	scanStart, scanEnd := jobSpan(t, wf, "staging.yml", "rc-scan")
	if !strings.Contains(wf[scanStart:scanEnd], "needs: dry-run") {
		t.Fatalf("staging.yml rc-scan no longer needs dry-run, so it cannot read the margin builds")
	}
	dl, dlAt := stepBlock(t, wf, marginDownloadName)
	if dlAt < scanStart || dlAt > scanEnd || !strings.Contains(dl, "name: rc-margin-${{ needs.dry-run.outputs.tag }}") {
		t.Errorf("staging.yml rc-scan does not download rc-margin-<tag>:\n%s", dl)
	}
	scan, scanAt := stepBlock(t, wf, rcScanStepName)
	if scanAt < dlAt || scanAt > scanEnd {
		t.Fatalf("staging.yml %q must follow the margin download inside rc-scan", rcScanStepName)
	}
	assertNoSwallow(t, "staging.yml", rcScanStepName, scan)
	for _, want := range []string{"-eq 6", "margin-manifest.txt", `"${MARGIN[@]}"`, "release-scan-gate.sh local"} {
		if !strings.Contains(scan, want) {
			t.Errorf("staging.yml %q lacks %q; it must count the margin archives and scan them:\n%s",
				rcScanStepName, want, scan)
		}
	}
	rec, recAt := stepBlock(t, wf, marginRecordName)
	if recAt < scanAt || recAt > scanEnd {
		t.Errorf("staging.yml %q must follow the scan inside rc-scan", marginRecordName)
	}
	for _, want := range []string{"name: rc-margin-scan-${{ needs.dry-run.outputs.tag }}", "margin-manifest.txt",
		"retention-days: 90", "if-no-files-found: error"} {
		if !strings.Contains(rec, want) {
			t.Errorf("staging.yml %q lacks %q:\n%s", marginRecordName, want, rec)
		}
	}
	if strings.Contains(rec, "if:") {
		t.Errorf("staging.yml %q has a condition; it must upload only after a clean scan:\n%s", marginRecordName, rec)
	}
	// upload-artifact roots the artifact at the paths' common ancestor, and
	// scripts/release-rc-margin-check.sh reads margin-manifest.txt and
	// vt-scan-report-rc.json from its top level, so both must be uploaded from
	// the same directory (#3192).
	for _, want := range []string{"${{ runner.temp }}/margin-manifest.txt", "${{ runner.temp }}/vt-scan-report-rc.json"} {
		if !strings.Contains(rec, want) {
			t.Errorf("staging.yml %q must upload %s so the release gate finds it at the artifact's top level:\n%s",
				marginRecordName, want, rec)
		}
	}
	if !strings.Contains(scan, `cp "$RUNNER_TEMP/margin/margin-manifest.txt" "$RUNNER_TEMP/margin-manifest.txt"`) {
		t.Errorf("staging.yml %q must copy the manifest beside the scan report before the record upload:\n%s",
			rcScanStepName, scan)
	}
}

func TestReleaseWorkflowRequiresMarginScannedRC(t *testing.T) {
	wf := readWorkflow(t, "release.yml")
	relStart, relEnd := jobSpan(t, wf, "release.yml", "release")
	block, at := stepBlock(t, wf, marginGateName)
	if at < relStart || at > relEnd {
		t.Fatalf("release.yml %q is not in the release job", marginGateName)
	}
	assertNoSwallow(t, "release.yml", marginGateName, block)
	if strings.Contains(block, "if:") {
		t.Errorf("release.yml %q has a condition; there is no override (see %s)", marginGateName, record)
	}
	if !strings.Contains(block, `scripts/release-rc-margin-check.sh "$TAG" "$GITHUB_SHA" "$REPO"`) {
		t.Errorf("release.yml %q does not run the margin gate on the tag's commit:\n%s", marginGateName, block)
	}
	build := strings.Index(wf, "- name: Build all Go binaries")
	if build < 0 {
		t.Fatal("HARNESS ERROR: release.yml has no Build all Go binaries step")
	}
	if at > build {
		t.Errorf("release.yml runs the margin gate after the build; it must refuse before anything is built")
	}
	if !strings.Contains(wf[relStart:at], "actions: read") {
		t.Errorf("release.yml release job lacks `actions: read`; the gate cannot read the RC's run and artifact")
	}
}

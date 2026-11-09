package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const repoRoot = "../.."

var seeded = filepath.Join(repoRoot, "configs", "release-text-patterns.yaml")

// The bundle line that held v0.8.4, and the text the fix produces.
const (
	brokenBundle = "class a{constructor(){this.displayState = \"hidden\";this.elapsedMs=0}}\n"
	fixedBundle  = "class a{constructor(){this.stalled=!1}getDisplayState(){return this.stalled?\"stalled\":\"hidden\"}}\n"
)

func runCheck(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return code, stdout.String() + stderr.String()
}

func writeVSIX(t *testing.T, files map[string][]byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nightgauge.vsix")
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
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
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeTarGz(t *testing.T, files map[string][]byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nightgauge_linux_amd64.tar.gz")
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
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
	return path
}

func writeFile(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

var elfBlob = append([]byte{0x7f, 'E', 'L', 'F', 0, 0}, []byte(`displayState = "hidden"`)...)

func TestBrokenBundleFailsAndFixedBundlePasses(t *testing.T) {
	cases := []struct {
		name  string
		write func(*testing.T, map[string][]byte) string
	}{{"vsix", writeVSIX}, {"tar.gz", writeTarGz}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			broken := tc.write(t, map[string][]byte{
				"extension/dist/extension.cjs": []byte(brokenBundle),
				"extension/package.json":       []byte(`{"name":"nightgauge"}`),
			})
			code, out := runCheck(t, "--patterns", seeded, broken)
			if code != 1 {
				t.Fatalf("broken bundle: exit %d, want 1\n%s", code, out)
			}
			for _, want := range []string{"extension/dist/extension.cjs:1", "Microsoft_Excel_Hidden_Macrosheet", "v0.8.4"} {
				if !strings.Contains(out, want) {
					t.Errorf("broken bundle output lacks %q:\n%s", want, out)
				}
			}
			fixed := tc.write(t, map[string][]byte{"extension/dist/extension.cjs": []byte(fixedBundle)})
			if code, out := runCheck(t, "--patterns", seeded, fixed); code != 0 {
				t.Fatalf("fixed bundle: exit %d, want 0\n%s", code, out)
			}
		})
	}
}

func TestMatchesEachAlternative(t *testing.T) {
	for _, body := range []string{`<sheet hidden = '1'>`, `x.HIDDEN="2"`, `state="veryHidden"`, `sheetState = 'hidden'`, "displayState =\n  \"hidden\"", "hidden\r\n=\t'1'"} {
		if code, out := runCheck(t, "--patterns", seeded, writeFile(t, "a.txt", body)); code != 1 {
			t.Errorf("%q: exit %d, want 1\n%s", body, code, out)
		}
	}
	for _, body := range []string{`hidden: true`, `state = "visible"`, `return s ? "stalled" : "hidden"`} {
		if code, out := runCheck(t, "--patterns", seeded, writeFile(t, "a.txt", body)); code != 0 {
			t.Errorf("%q: exit %d, want 0\n%s", body, code, out)
		}
	}
}

func TestBinariesAreSkipped(t *testing.T) {
	nul := append([]byte(`displayState = "hidden"`), 0)
	path := writeVSIX(t, map[string][]byte{"bin/nightgauge": elfBlob, "data.bin": nul})
	code, out := runCheck(t, "--patterns", seeded, path)
	if code != 2 || !strings.Contains(out, "no text file was checked") {
		t.Fatalf("archive of binaries only: exit %d, want 2 with no-text HARNESS ERROR\n%s", code, out)
	}
}

func TestHarnessErrors(t *testing.T) {
	text := writeFile(t, "a.txt", "ok\n")
	bad := writeFile(t, "bad.yaml", "patterns:\n  - {rule: r, source: s, release: v1, pattern: '(['}\n")
	empty := writeFile(t, "empty.yaml", "patterns: []\n")
	partial := writeFile(t, "partial.yaml", "patterns:\n  - {rule: r, pattern: x}\n")
	for name, args := range map[string][]string{
		"no args":          nil,
		"no paths":         {"--patterns", seeded},
		"no patterns flag": {text},
		"missing patterns": {"--patterns", "/nonexistent.yaml", text},
		"bad regex":        {"--patterns", bad, text},
		"empty patterns":   {"--patterns", empty, text},
		"partial entry":    {"--patterns", partial, text},
		"missing path":     {"--patterns", seeded, "/nonexistent.vsix"},
	} {
		code, out := runCheck(t, args...)
		if code != 2 || !strings.Contains(out, "HARNESS ERROR") {
			t.Errorf("%s: exit %d, want 2 with HARNESS ERROR\n%s", name, code, out)
		}
	}
}

func TestSeededPatterns(t *testing.T) {
	pats, err := loadPatterns(seeded)
	if err != nil {
		t.Fatalf("HARNESS ERROR: %v", err)
	}
	if len(pats) < 2 {
		t.Fatalf("seeded data file holds %d patterns, want the rule's two regexes", len(pats))
	}
}

// The fixed source must not carry the text that held v0.8.4.
func TestStallStatusBarItemSourcePasses(t *testing.T) {
	src := filepath.Join(repoRoot, "packages", "nightgauge", "src", "services", "StallStatusBarItem.ts")
	if code, out := runCheck(t, "--patterns", seeded, src); code != 0 {
		t.Fatalf("StallStatusBarItem.ts: exit %d, want 0\n%s", code, out)
	}
}

// --- Workflow guard: the step runs beside the DWARF check, before every scan, and fails closed. ---

const (
	stepName      = "Verify no shipped text file matches a known YARA text pattern"
	dwarfStepName = "Verify every shipped binary carries DWARF"
)

func readWorkflow(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot, ".github", "workflows", name))
	if err != nil {
		t.Fatalf("HARNESS ERROR: %v", err)
	}
	return string(b)
}

func stepBlock(t *testing.T, wf, file string) (string, int) {
	t.Helper()
	start := strings.Index(wf, "- name: "+stepName)
	if start < 0 {
		t.Fatalf("%s has no %q step (see %s)", file, stepName, record)
	}
	rest := wf[start+1:]
	end := strings.Index(rest, "\n      - ")
	if end < 0 {
		end = len(rest)
	}
	block := wf[start : start+1+end]
	for _, bad := range []string{"continue-on-error", "|| true", "|| :", "exit 0", "if: "} {
		if strings.Contains(block, bad) {
			t.Errorf("%s text-pattern step contains %q; it must fail on exit 1 and 2 alike", file, bad)
		}
	}
	for _, want := range []string{"./cmd/release-text-pattern-check", "--patterns configs/release-text-patterns.yaml",
		"dist/*.tar.gz", "packages/nightgauge/*.vsix"} {
		if !strings.Contains(block, want) {
			t.Errorf("%s text-pattern step lacks %q:\n%s", file, want, block)
		}
	}
	if dwarf := strings.Index(wf, "- name: "+dwarfStepName); dwarf < 0 || start < dwarf {
		t.Errorf("%s text-pattern step must run after the DWARF check", file)
	}
	return block, start
}

func TestReleaseWorkflowChecksTextPatternsBeforeDraftScan(t *testing.T) {
	wf := readWorkflow(t, "release.yml")
	_, at := stepBlock(t, wf, "release.yml")
	scan := strings.Index(wf, "release-scan-gate.sh scan")
	if scan < 0 {
		t.Fatal("HARNESS ERROR: release.yml has no release-scan-gate.sh scan step")
	}
	if at > scan {
		t.Errorf("release.yml runs the text-pattern check after the draft scan; it must run first")
	}
}

func TestStagingWorkflowChecksTextPatternsBeforeRCScan(t *testing.T) {
	wf := readWorkflow(t, "staging.yml")
	_, at := stepBlock(t, wf, "staging.yml")
	dryRun := strings.Index(wf, "\n  dry-run:\n")
	rcScan := strings.Index(wf, "\n  rc-scan:\n")
	if dryRun < 0 || rcScan < 0 || rcScan < dryRun {
		t.Fatal("HARNESS ERROR: staging.yml jobs dry-run and rc-scan not found in that order")
	}
	if at < dryRun || at > rcScan {
		t.Errorf("staging.yml text-pattern check is not in the dry-run job, which rc-scan needs")
	}
}

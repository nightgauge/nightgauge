package doctor

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// gitBinPath and bashBinPath are resolved once at package init, before any
// test clears PATH via isolateCascade — tests that shell out to git/bash as
// *helpers* (not as the resolution target under test) must not be broken by
// the very PATH-clearing they're testing.
//
// bash is pinned to /bin/bash when it exists rather than taken from PATH:
// Claude Code hooks run under the SYSTEM bash, which on macOS is 3.2.57, while
// PATH on a dev machine points at Homebrew's bash 5.x. Testing the 5.x binary
// would let a bash-4ism in guard.sh (arrays, `mapfile`, `${var,,}`) pass CI and
// break every macOS hook silently.
var (
	gitBinPath  string
	bashBinPath string
	// testdataDir is absolute: every test here chdirs into a scratch
	// directory via isolateCascade, so a relative "testdata/..." path stops
	// resolving the moment the cascade is isolated.
	testdataDir string
)

func init() {
	if wd, err := os.Getwd(); err == nil {
		testdataDir = filepath.Join(wd, "testdata")
	}
	gitBinPath, _ = exec.LookPath("git")
	if _, err := os.Stat("/bin/bash"); err == nil {
		bashBinPath = "/bin/bash"
	} else {
		bashBinPath, _ = exec.LookPath("bash")
	}
}

// realPath resolves symlinks (e.g. macOS's /tmp -> /private/tmp) so paths
// built from t.TempDir() compare equal to the physical path `git
// rev-parse` reports. Falls back to the original path if resolution fails.
func realPath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path
	}
	return resolved
}

// writeFakeBinary creates an executable file at path with placeholder content.
func writeFakeBinary(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("failed to create parent dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho fake\n"), 0o755); err != nil {
		t.Fatalf("failed to write fake binary: %v", err)
	}
}

// installBundle materializes ~/.vscode/extensions/<dir>/dist/bin/nightgauge
// and returns its path. `dir` is the FULL directory name so a test can express
// the shapes that actually occur: dev (`…-0.1.<epoch>`), release
// (`…-0.2.1-darwin-arm64`), RC (`…-0.2.0-rc.23-darwin-arm64`) and partial
// installs (`….vsctmp`).
func installBundle(t *testing.T, home, dir string, executable bool) string {
	t.Helper()
	binary := filepath.Join(home, ".vscode", "extensions", dir, "dist", "bin", "nightgauge")
	writeFakeBinary(t, binary)
	if !executable {
		if err := os.Chmod(binary, 0o644); err != nil {
			t.Fatalf("chmod %s: %v", binary, err)
		}
	}
	return binary
}

// writeExtensionsIndex writes a VS Code-shaped extensions.json naming the
// given bundle directories as installed. A foreign publisher is always
// included so the nightgauge entry is never the whole document — a parser that
// only works on a one-entry file is not a parser.
func writeExtensionsIndex(t *testing.T, home string, recordedDirs ...string) string {
	t.Helper()
	type ident struct {
		ID string `json:"id"`
	}
	type loc struct {
		Mid    int    `json:"$mid"`
		Path   string `json:"path"`
		Scheme string `json:"scheme"`
	}
	type entry struct {
		Identifier       ident  `json:"identifier"`
		Version          string `json:"version"`
		Location         loc    `json:"location"`
		RelativeLocation string `json:"relativeLocation"`
	}
	extDir := filepath.Join(home, ".vscode", "extensions")
	entries := []entry{{
		Identifier:       ident{ID: "publisher1.extension1"},
		Version:          "1.0.0",
		Location:         loc{Mid: 1, Path: filepath.Join(extDir, "publisher1.extension1-1.0.0"), Scheme: "file"},
		RelativeLocation: "publisher1.extension1-1.0.0",
	}}
	for _, dir := range recordedDirs {
		entries = append(entries, entry{
			Identifier:       ident{ID: "nightgauge.nightgauge-vscode"},
			Version:          strings.TrimPrefix(dir, bundleDirPrefix),
			Location:         loc{Mid: 1, Path: filepath.Join(extDir, dir), Scheme: "file"},
			RelativeLocation: dir,
		})
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("marshal extensions index: %v", err)
	}
	return writeExtensionsIndexRaw(t, home, string(raw))
}

// writeExtensionsIndexRaw writes arbitrary bytes as extensions.json, for the
// malformed/whitespace cases a marshaller cannot express.
func writeExtensionsIndexRaw(t *testing.T, home, content string) string {
	t.Helper()
	path := filepath.Join(home, ".vscode", "extensions", "extensions.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create extensions dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write extensions index: %v", err)
	}
	return path
}

// isolateCascade clears every cascade input so only the step under test can
// resolve, then restores the environment/cwd on cleanup.
func isolateCascade(t *testing.T) {
	t.Helper()
	t.Setenv("NIGHTGAUGE_BIN", "")
	// PATH is narrowed to just git's directory (never "nightgauge" itself) —
	// both ResolveBinary's own git-based cascade steps and this test file's
	// git helper calls need `git` resolvable, while step 1 (PATH lookup of
	// "nightgauge") must still fail unless a test explicitly overrides PATH.
	safePath := ""
	if gitBinPath != "" {
		safePath = filepath.Dir(gitBinPath)
	}
	t.Setenv("PATH", safePath)
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
}

func TestResolveBinary_EnvOverride(t *testing.T) {
	isolateCascade(t)

	fake := filepath.Join(t.TempDir(), "nightgauge")
	writeFakeBinary(t, fake)
	t.Setenv("NIGHTGAUGE_BIN", fake)

	resolved := ResolveBinary()
	if resolved.Path != fake {
		t.Errorf("expected path %q, got %q", fake, resolved.Path)
	}
	if resolved.Step != StepEnvOverride {
		t.Errorf("expected step %q, got %q", StepEnvOverride, resolved.Step)
	}
}

func TestResolveBinary_EnvOverride_StaleFallsThrough(t *testing.T) {
	isolateCascade(t)

	// Points at a non-existent file — must fall through to "unresolved"
	// since none of the other cascade steps can resolve either.
	t.Setenv("NIGHTGAUGE_BIN", filepath.Join(t.TempDir(), "does-not-exist"))

	resolved := ResolveBinary()
	if resolved.Path != "" {
		t.Errorf("expected stale NIGHTGAUGE_BIN to fall through, got path %q via step %q", resolved.Path, resolved.Step)
	}
}

func TestResolveBinary_Path(t *testing.T) {
	isolateCascade(t)

	dir := t.TempDir()
	fake := filepath.Join(dir, "nightgauge")
	writeFakeBinary(t, fake)
	t.Setenv("PATH", dir)

	resolved := ResolveBinary()
	if resolved.Step != StepPath {
		t.Errorf("expected step %q, got %q (path=%q)", StepPath, resolved.Step, resolved.Path)
	}
}

func TestResolveBinary_RepoBin(t *testing.T) {
	if gitBinPath == "" {
		t.Skip("git not available")
	}
	isolateCascade(t)

	repo := t.TempDir()
	runGit(t, repo, "init")
	writeFakeBinary(t, filepath.Join(repo, "bin", "nightgauge"))
	t.Chdir(repo)

	resolved := ResolveBinary()
	if resolved.Step != StepRepoBin {
		t.Errorf("expected step %q, got %q (path=%q)", StepRepoBin, resolved.Step, resolved.Path)
	}
}

func TestResolveBinary_CanonicalRepoBin(t *testing.T) {
	if gitBinPath == "" {
		t.Skip("git not available")
	}
	isolateCascade(t)

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test")
	runGit(t, repo, "commit", "--allow-empty", "-m", "init")

	worktreeDir := filepath.Join(t.TempDir(), "worktree")
	if out, err := exec.Command(gitBinPath, "-C", repo, "worktree", "add", "--detach", worktreeDir).CombinedOutput(); err != nil {
		t.Skipf("git worktree add failed (environment limitation): %v: %s", err, out)
	}

	writeFakeBinary(t, filepath.Join(repo, "bin", "nightgauge"))
	t.Chdir(worktreeDir)

	resolved := ResolveBinary()
	if resolved.Step != StepCanonicalRepoBin {
		t.Errorf("expected step %q, got %q (path=%q)", StepCanonicalRepoBin, resolved.Step, resolved.Path)
	}
}

func TestResolveBinary_VSCodeExtensionBundle(t *testing.T) {
	isolateCascade(t)

	home := os.Getenv("HOME")
	installBundle(t, home, bundleDirPrefix+"0.1.0", true)

	resolved := ResolveBinary()
	if resolved.Step != StepVSCodeExtension {
		t.Errorf("expected step %q, got %q (path=%q)", StepVSCodeExtension, resolved.Step, resolved.Path)
	}
}

func TestResolveBinary_GoBin(t *testing.T) {
	isolateCascade(t)

	home := os.Getenv("HOME")
	writeFakeBinary(t, filepath.Join(home, "go", "bin", "nightgauge"))

	resolved := ResolveBinary()
	if resolved.Step != StepGoBin {
		t.Errorf("expected step %q, got %q (path=%q)", StepGoBin, resolved.Step, resolved.Path)
	}
}

func TestResolveBinary_Unresolved(t *testing.T) {
	isolateCascade(t)

	resolved := ResolveBinary()
	if resolved.Path != "" || resolved.Step != "" {
		t.Errorf("expected unresolved result, got path=%q step=%q", resolved.Path, resolved.Step)
	}
}

// --- #356: the install record is the selection authority -------------------

// TestScanVSCodeBundles_RecordBeatsGlobOrder is THE #356 regression test.
//
// Two bundles are installed and VS Code records the SECOND one in glob order.
// Pre-#356 resolution took the first executable glob match — both bash globbing
// and filepath.Glob are collation-sorted — so it ran the bundle VS Code had
// already superseded, silently, in every repo outside a nightgauge checkout.
//
// Note what this test does NOT do: it never compares the two version strings.
// The recorded bundle wins because it is recorded, which is the only property
// that survives downgrades, RC ties and `.vsctmp` orphans.
func TestScanVSCodeBundles_RecordBeatsGlobOrder(t *testing.T) {
	isolateCascade(t)
	home := os.Getenv("HOME")

	first := installBundle(t, home, bundleDirPrefix+"0.1.1785906439", true)
	recorded := installBundle(t, home, bundleDirPrefix+"0.1.1785982325", true)
	writeExtensionsIndex(t, home, bundleDirPrefix+"0.1.1785982325")

	scan := ScanVSCodeBundles(home)
	if scan.SelectedPath == first {
		t.Fatalf("selected the first glob match %q; VS Code records %q as installed", first, recorded)
	}
	if scan.SelectedPath != recorded {
		t.Fatalf("expected the recorded bundle %q, got %q", recorded, scan.SelectedPath)
	}
	if !scan.RecordedUsed {
		t.Error("RecordedUsed must be true when the selection came from extensions.json")
	}
	if scan.Divergence != DivergenceNone {
		t.Errorf("a confirmed resolution must not diverge, got %q", scan.Divergence)
	}
	if scan.RecordedVersion != "0.1.1785982325" {
		t.Errorf("RecordedVersion = %q, want %q", scan.RecordedVersion, "0.1.1785982325")
	}
}

// TestScanVSCodeBundles_RecordedDowngradeWinsSilently pins the property that
// version ordering cannot express: a recorded bundle that is NEITHER the first
// glob match NOR the highest version number.
//
// This is the live dogfood shape. dev-install.sh derives its version from
// package.json (`${ORIG_VERSION%.*}.$(date +%s)` over a 0.1.0 that main never
// bumps), so a maintainer dev-install is permanently `0.1.<epoch>` and loses
// numerically to any leftover 0.2.x release directory — i.e. version ranking
// silently discards the build the maintainer just installed to test a fix,
// which is #356's own stated failure mode. And it must be SILENT: a recorded
// downgrade is a healthy machine, not a diagnostic.
func TestScanVSCodeBundles_RecordedDowngradeWinsSilently(t *testing.T) {
	isolateCascade(t)
	home := os.Getenv("HOME")

	installBundle(t, home, bundleDirPrefix+"0.1.0", true)             // first in glob order
	recorded := installBundle(t, home, bundleDirPrefix+"0.2.0", true) // recorded
	installBundle(t, home, bundleDirPrefix+"0.3.0", true)             // highest version

	writeExtensionsIndex(t, home, bundleDirPrefix+"0.2.0")

	scan := ScanVSCodeBundles(home)
	if scan.SelectedPath != recorded {
		t.Fatalf("expected the recorded bundle %q, got %q (first-glob-match and highest-version are both wrong answers here)", recorded, scan.SelectedPath)
	}
	if scan.Divergence != DivergenceNone {
		t.Errorf("a recorded downgrade is a healthy install and must stay silent, got divergence %q", scan.Divergence)
	}
}

// TestScanVSCodeBundles_RecordedRCBundleWins covers the channel this repo
// actually ships. staging.yml runs `npm version 0.2.0-rc.23` and then
// `vsce package --target <t>`, so bundle directories really are
// `…-0.2.0-rc.23-darwin-arm64`. Every dotted-numeric comparator collapses
// rc.22 and rc.23 to `0.2.0`, ties, and falls back to the first glob match —
// #356 verbatim.
func TestScanVSCodeBundles_RecordedRCBundleWins(t *testing.T) {
	isolateCascade(t)
	home := os.Getenv("HOME")

	rc22 := installBundle(t, home, bundleDirPrefix+"0.2.0-rc.22-darwin-arm64", true)
	rc23 := installBundle(t, home, bundleDirPrefix+"0.2.0-rc.23-darwin-arm64", true)
	writeExtensionsIndex(t, home, bundleDirPrefix+"0.2.0-rc.23-darwin-arm64")

	scan := ScanVSCodeBundles(home)
	if scan.SelectedPath == rc22 {
		t.Fatalf("resolved rc.22 %q while VS Code records rc.23 %q", rc22, rc23)
	}
	if scan.SelectedPath != rc23 {
		t.Fatalf("expected %q, got %q", rc23, scan.SelectedPath)
	}
	if scan.Divergence != DivergenceNone {
		t.Errorf("expected a silent confirmed resolution, got divergence %q", scan.Divergence)
	}
}

// TestScanVSCodeBundles_VsctmpOrphanNeverWins: the glob also matches VS Code's
// partial-install leftovers. They are not installs and must never be selected
// over the recorded one — including when they sort FIRST, which is where the
// fallback would otherwise land.
func TestScanVSCodeBundles_VsctmpOrphanNeverWins(t *testing.T) {
	isolateCascade(t)
	home := os.Getenv("HOME")

	orphan := installBundle(t, home, bundleDirPrefix+"0.2.0-darwin-arm64.vsctmp", true)
	recorded := installBundle(t, home, bundleDirPrefix+"0.2.1-darwin-arm64", true)
	writeExtensionsIndex(t, home, bundleDirPrefix+"0.2.1-darwin-arm64")

	scan := ScanVSCodeBundles(home)
	if scan.SelectedPath == orphan {
		t.Fatalf("a .vsctmp partial-install orphan was selected over the recorded install %q", recorded)
	}
	if scan.SelectedPath != recorded {
		t.Fatalf("expected %q, got %q", recorded, scan.SelectedPath)
	}
}

// TestScanVSCodeBundles_RecordUnusableFallsBackAndDiverges: the recorded
// bundle's binary is present but not executable (partial install, lost exec
// bit). Resolution must still yield a runnable binary AND say so — this is the
// case where the hooks genuinely run something other than the install.
func TestScanVSCodeBundles_RecordUnusableFallsBackAndDiverges(t *testing.T) {
	isolateCascade(t)
	home := os.Getenv("HOME")

	fallback := installBundle(t, home, bundleDirPrefix+"0.2.0-darwin-arm64", true)
	installBundle(t, home, bundleDirPrefix+"0.2.1-darwin-arm64", false)
	writeExtensionsIndex(t, home, bundleDirPrefix+"0.2.1-darwin-arm64")

	scan := ScanVSCodeBundles(home)
	if scan.SelectedPath != fallback {
		t.Fatalf("expected fallback to the runnable bundle %q, got %q", fallback, scan.SelectedPath)
	}
	if scan.RecordedUsed {
		t.Error("RecordedUsed must be false when the recorded bundle could not be run")
	}
	if scan.Divergence != DivergenceRecordUnusable {
		t.Errorf("Divergence = %q, want %q", scan.Divergence, DivergenceRecordUnusable)
	}
	if scan.RecordedVersion != "0.2.1-darwin-arm64" {
		t.Errorf("the signal needs the recorded version; got %q", scan.RecordedVersion)
	}
}

// TestScanVSCodeBundles_RecordNamesMissingDirectory: extensions.json can point
// at a directory that is no longer on disk (a half-finished uninstall). Same
// treatment as an unrunnable record.
func TestScanVSCodeBundles_RecordNamesMissingDirectory(t *testing.T) {
	isolateCascade(t)
	home := os.Getenv("HOME")

	onDisk := installBundle(t, home, bundleDirPrefix+"0.2.0-darwin-arm64", true)
	writeExtensionsIndex(t, home, bundleDirPrefix+"0.9.9-darwin-arm64")

	scan := ScanVSCodeBundles(home)
	if scan.SelectedPath != onDisk {
		t.Fatalf("expected %q, got %q", onDisk, scan.SelectedPath)
	}
	if scan.Divergence != DivergenceRecordUnusable {
		t.Errorf("Divergence = %q, want %q", scan.Divergence, DivergenceRecordUnusable)
	}
	for _, b := range scan.Bundles {
		if b.Recorded {
			t.Errorf("bundle %q must not be marked Recorded — the record names a different directory", b.Dir)
		}
	}
}

// TestScanVSCodeBundles_NoRecordSingleBundleIsSilent: one bundle and no record
// is not ambiguous — there is nothing else it could be. Signalling here would
// write a line on every single tool call for a perfectly ordinary machine.
func TestScanVSCodeBundles_NoRecordSingleBundleIsSilent(t *testing.T) {
	isolateCascade(t)
	home := os.Getenv("HOME")

	only := installBundle(t, home, bundleDirPrefix+"0.2.1-darwin-arm64", true)

	scan := ScanVSCodeBundles(home)
	if scan.SelectedPath != only {
		t.Fatalf("expected %q, got %q", only, scan.SelectedPath)
	}
	if scan.Divergence != DivergenceNone {
		t.Errorf("a single unrecorded bundle must stay silent, got divergence %q", scan.Divergence)
	}
}

// TestScanVSCodeBundles_NoRecordMultipleBundlesDiverge: with no record and
// several candidates the selection is a guess. It restores the pre-#356
// first-glob-match answer — deterministic, no ordering — and says so.
func TestScanVSCodeBundles_NoRecordMultipleBundlesDiverge(t *testing.T) {
	isolateCascade(t)
	home := os.Getenv("HOME")

	first := installBundle(t, home, bundleDirPrefix+"0.2.0-darwin-arm64", true)
	installBundle(t, home, bundleDirPrefix+"0.2.1-darwin-arm64", true)

	scan := ScanVSCodeBundles(home)
	if scan.SelectedPath != first {
		t.Fatalf("expected the first glob match %q, got %q", first, scan.SelectedPath)
	}
	if scan.Divergence != DivergenceUnrecorded {
		t.Errorf("Divergence = %q, want %q", scan.Divergence, DivergenceUnrecorded)
	}
	if scan.RecordedDir != "" {
		t.Errorf("RecordedDir must be empty with no record, got %q", scan.RecordedDir)
	}
}

// TestReadRecordedBundleDir covers every way the record can be unusable. Each
// case must degrade to "no record" rather than to a guess, because the record
// steers which binary the hooks execute.
func TestReadRecordedBundleDir(t *testing.T) {
	cases := []struct {
		name    string
		content string // "" = do not write the file at all
		want    string
	}{
		{
			name:    "single nightgauge entry",
			content: `[{"identifier":{"id":"a.b"},"relativeLocation":"a.b-1.0.0"},{"identifier":{"id":"nightgauge.nightgauge-vscode"},"relativeLocation":"nightgauge.nightgauge-vscode-0.1.5"}]`,
			want:    "nightgauge.nightgauge-vscode-0.1.5",
		},
		{
			name:    "whitespace around the colon",
			content: "[{\"relativeLocation\"\t:   \"nightgauge.nightgauge-vscode-0.2.0-rc.23-darwin-arm64\"}]",
			want:    "nightgauge.nightgauge-vscode-0.2.0-rc.23-darwin-arm64",
		},
		{
			name:    "pretty printed",
			content: "[\n  {\n    \"identifier\": { \"id\": \"nightgauge.nightgauge-vscode\" },\n    \"relativeLocation\": \"nightgauge.nightgauge-vscode-0.9.9\"\n  }\n]",
			want:    "nightgauge.nightgauge-vscode-0.9.9",
		},
		{
			name:    "two nightgauge entries",
			content: `[{"relativeLocation":"nightgauge.nightgauge-vscode-0.1.1"},{"relativeLocation":"nightgauge.nightgauge-vscode-0.1.2"}]`,
			want:    "",
		},
		{
			name:    "no nightgauge entry",
			content: `[{"relativeLocation":"other.ext-1.0.0"}]`,
			want:    "",
		},
		{
			name:    "empty array",
			content: `[]`,
			want:    "",
		},
		{
			name:    "not json",
			content: `not json at all`,
			want:    "",
		},
		{
			// A torn index — VS Code rewrites extensions.json in place on
			// install/uninstall — still names the install unambiguously, and
			// the hooks act on it. Answering "" here would make doctor report
			// a divergence that does not exist (#277).
			name:    "truncated but still carries a complete record",
			content: `[{"identifier":{"id":"nightgauge.nightgauge-vscode"},"relativeLocation":"nightgauge.nightgauge-vscode-0.2.0"},{"identi`,
			want:    "nightgauge.nightgauge-vscode-0.2.0",
		},
		{
			// The value exists only after JSON unescaping, so it is not the
			// text of any directory name in the file.
			name:    "escaped value is not a record",
			content: `[{"relativeLocation":"nightgauge.nightgauge-vscode-a\\b"}]`,
			want:    "",
		},
		{
			name:    "path traversal is rejected",
			content: `[{"relativeLocation":"nightgauge.nightgauge-vscode-../../evil"}]`,
			want:    "",
		},
		{
			name:    "separator is rejected",
			content: `[{"relativeLocation":"nightgauge.nightgauge-vscode-x/y"}]`,
			want:    "",
		},
		{
			name:    "file absent",
			content: "",
			want:    "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			if tc.content != "" {
				writeExtensionsIndexRaw(t, home, tc.content)
			}
			if got := readRecordedBundleDir(home); got != tc.want {
				t.Errorf("readRecordedBundleDir = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestScanVSCodeBundles_CapturedRealLayout runs the resolver against the
// REDACTED CAPTURE of a real machine: two bundles on disk, extensions.json
// recording the second one in glob order (and only that one), with 13 foreign
// publishers around it. Pre-#356 this machine silently ran the unrecorded
// leftover — for two days, undetected.
func TestScanVSCodeBundles_CapturedRealLayout(t *testing.T) {
	layout := loadCapturedBundleLayout(t)
	isolateCascade(t)
	home := os.Getenv("HOME")

	paths := materializeCapturedBundles(t, home, layout, func(capturedBundle) bool { return true })
	writeCapturedExtensionsIndex(t, home)

	want := paths[layout.RecordedRelativeLocation]
	if want == "" {
		t.Fatalf("captured layout has no on-disk bundle for the recorded install %q", layout.RecordedRelativeLocation)
	}

	scan := ScanVSCodeBundles(home)
	if scan.RecordedDir != layout.RecordedRelativeLocation {
		t.Fatalf("parsed record %q from the captured index, want %q", scan.RecordedDir, layout.RecordedRelativeLocation)
	}
	if scan.SelectedPath != want {
		t.Errorf("captured real layout resolved %q, want the recorded install %q", scan.SelectedPath, want)
	}
	if scan.Divergence != DivergenceNone {
		t.Errorf("the captured machine is healthy; expected no divergence, got %q", scan.Divergence)
	}
	if len(scan.Bundles) < 2 {
		t.Errorf("expected the capture's multi-bundle layout, got %d", len(scan.Bundles))
	}
}

// --- guard.sh parity (#277) ------------------------------------------------

// TestResolveBinary_GuardShParity asserts guard.sh resolves to the same path
// as ResolveBinary() for each of the five filesystem-based cascade steps
// (#277 AC2), including every #356 record-authority case. Skips gracefully
// when bash is unavailable.
func TestResolveBinary_GuardShParity(t *testing.T) {
	if bashBinPath == "" {
		t.Skip("bash not available")
	}
	if gitBinPath == "" {
		t.Skip("git not available")
	}

	guardPath := guardShPath(t)

	cases := []struct {
		name  string
		setup func(t *testing.T) string // returns expected path
	}{
		{
			name: "path",
			setup: func(t *testing.T) string {
				isolateCascade(t)
				dir := t.TempDir()
				fake := filepath.Join(dir, "nightgauge")
				writeFakeBinary(t, fake)
				t.Setenv("PATH", dir)
				return fake
			},
		},
		{
			name: "repo_bin",
			setup: func(t *testing.T) string {
				isolateCascade(t)
				repo := realPath(t, t.TempDir())
				runGit(t, repo, "init")
				fake := filepath.Join(repo, "bin", "nightgauge")
				writeFakeBinary(t, fake)
				t.Chdir(repo)
				return fake
			},
		},
		{
			name: "canonical_repo_bin",
			setup: func(t *testing.T) string {
				isolateCascade(t)
				repo := realPath(t, t.TempDir())
				runGit(t, repo, "init")
				runGit(t, repo, "config", "user.email", "test@example.com")
				runGit(t, repo, "config", "user.name", "Test")
				runGit(t, repo, "commit", "--allow-empty", "-m", "init")
				worktreeDir := filepath.Join(realPath(t, t.TempDir()), "worktree")
				if out, err := exec.Command(gitBinPath, "-C", repo, "worktree", "add", "--detach", worktreeDir).CombinedOutput(); err != nil {
					t.Skipf("git worktree add failed (environment limitation): %v: %s", err, out)
				}
				fake := filepath.Join(repo, "bin", "nightgauge")
				writeFakeBinary(t, fake)
				t.Chdir(worktreeDir)
				return fake
			},
		},
		{
			name: "vscode_extension",
			setup: func(t *testing.T) string {
				isolateCascade(t)
				return installBundle(t, os.Getenv("HOME"), bundleDirPrefix+"0.1.0", true)
			},
		},
		{
			name: "go_bin",
			setup: func(t *testing.T) string {
				isolateCascade(t)
				home := os.Getenv("HOME")
				fake := filepath.Join(home, "go", "bin", "nightgauge")
				writeFakeBinary(t, fake)
				return fake
			},
		},
		{
			// #356: the record beats glob order, in BOTH implementations.
			name: "vscode_extension_recorded_beats_glob_order",
			setup: func(t *testing.T) string {
				isolateCascade(t)
				home := os.Getenv("HOME")
				installBundle(t, home, bundleDirPrefix+"0.1.1785906439", true)
				recorded := installBundle(t, home, bundleDirPrefix+"0.1.1785982325", true)
				writeExtensionsIndex(t, home, bundleDirPrefix+"0.1.1785982325")
				return recorded
			},
		},
		{
			// A recorded bundle that is neither first in glob order nor the
			// highest version — the shape no version comparator can produce.
			name: "vscode_extension_recorded_downgrade",
			setup: func(t *testing.T) string {
				isolateCascade(t)
				home := os.Getenv("HOME")
				installBundle(t, home, bundleDirPrefix+"0.1.0", true)
				recorded := installBundle(t, home, bundleDirPrefix+"0.2.0", true)
				installBundle(t, home, bundleDirPrefix+"0.3.0", true)
				writeExtensionsIndex(t, home, bundleDirPrefix+"0.2.0")
				return recorded
			},
		},
		{
			// RC bundles: the shipping channel. rc.22 sorts first.
			name: "vscode_extension_recorded_rc",
			setup: func(t *testing.T) string {
				isolateCascade(t)
				home := os.Getenv("HOME")
				installBundle(t, home, bundleDirPrefix+"0.2.0-rc.22-darwin-arm64", true)
				recorded := installBundle(t, home, bundleDirPrefix+"0.2.0-rc.23-darwin-arm64", true)
				writeExtensionsIndex(t, home, bundleDirPrefix+"0.2.0-rc.23-darwin-arm64")
				return recorded
			},
		},
		{
			name: "vscode_extension_vsctmp_orphan_sorts_first",
			setup: func(t *testing.T) string {
				isolateCascade(t)
				home := os.Getenv("HOME")
				installBundle(t, home, bundleDirPrefix+"0.2.0-darwin-arm64.vsctmp", true)
				recorded := installBundle(t, home, bundleDirPrefix+"0.2.1-darwin-arm64", true)
				writeExtensionsIndex(t, home, bundleDirPrefix+"0.2.1-darwin-arm64")
				return recorded
			},
		},
		{
			name: "vscode_extension_record_unusable_falls_back",
			setup: func(t *testing.T) string {
				isolateCascade(t)
				home := os.Getenv("HOME")
				fallback := installBundle(t, home, bundleDirPrefix+"0.2.0-darwin-arm64", true)
				installBundle(t, home, bundleDirPrefix+"0.2.1-darwin-arm64", false)
				writeExtensionsIndex(t, home, bundleDirPrefix+"0.2.1-darwin-arm64")
				return fallback
			},
		},
		{
			name: "vscode_extension_two_records_is_no_record",
			setup: func(t *testing.T) string {
				isolateCascade(t)
				home := os.Getenv("HOME")
				first := installBundle(t, home, bundleDirPrefix+"0.2.0-darwin-arm64", true)
				installBundle(t, home, bundleDirPrefix+"0.2.1-darwin-arm64", true)
				writeExtensionsIndex(t, home,
					bundleDirPrefix+"0.2.0-darwin-arm64",
					bundleDirPrefix+"0.2.1-darwin-arm64")
				return first
			},
		},
		{
			name: "vscode_extension_no_record_falls_back_to_first_glob_match",
			setup: func(t *testing.T) string {
				isolateCascade(t)
				home := os.Getenv("HOME")
				first := installBundle(t, home, bundleDirPrefix+"0.2.0-darwin-arm64", true)
				installBundle(t, home, bundleDirPrefix+"0.2.1-darwin-arm64", true)
				return first
			},
		},
		{
			// The captured real machine, index and all.
			name: "vscode_extension_captured_real_layout",
			setup: func(t *testing.T) string {
				layout := loadCapturedBundleLayout(t)
				isolateCascade(t)
				home := os.Getenv("HOME")
				paths := materializeCapturedBundles(t, home, layout, func(capturedBundle) bool { return true })
				writeCapturedExtensionsIndex(t, home)
				return paths[layout.RecordedRelativeLocation]
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expected := tc.setup(t)
			if expected == "" {
				t.Fatalf("parity case %q produced an empty expected path", tc.name)
			}

			goResolved := ResolveBinary()
			if goResolved.Path != expected {
				t.Fatalf("Go resolver: expected %q, got %q", expected, goResolved.Path)
			}

			shResolved := guardShResolve(t, guardPath)
			if shResolved != expected {
				t.Errorf("guard.sh resolved %q, Go resolver resolved %q (expected %q)", shResolved, goResolved.Path, expected)
			}
		})
	}
}

// TestResolveBinary_GuardShParity_Precedence runs the same adjacent-pair
// precedence matrix as TestResolveBinary_Precedence through guard.sh, so the
// cascade ORDER — not just per-step reachability — is pinned in BOTH
// implementations (#356 AC4, #277 parity contract). Reordering the chain in
// either file fails here.
func TestResolveBinary_GuardShParity_Precedence(t *testing.T) {
	if bashBinPath == "" {
		t.Skip("bash not available")
	}
	if gitBinPath == "" {
		t.Skip("git not available")
	}
	guardPath := guardShPath(t)

	for _, tc := range precedenceCases() {
		t.Run(tc.name, func(t *testing.T) {
			winner, loser := tc.setup(t)
			if winner == "" || loser == "" {
				t.Fatalf("precedence case %q produced an empty path (winner=%q loser=%q)", tc.name, winner, loser)
			}

			goResolved := ResolveBinary()
			if goResolved.Path != winner {
				t.Errorf("Go resolver: %s must beat %s — expected %q, got %q", tc.higher, tc.lower, winner, goResolved.Path)
			}
			shResolved := guardShResolve(t, guardPath)
			if shResolved != winner {
				t.Errorf("guard.sh: %s must beat %s — expected %q, got %q", tc.higher, tc.lower, winner, shResolved)
			}
		})
	}
}

// TestGuardShParity_RecordedBundleDir pins the two record PARSERS against each
// other directly — guard.sh's single `grep -o` extraction and the Go side —
// over the whole malformed-input matrix. Whole-cascade parity would hide a
// parser disagreement whenever both answers happen to reach the same binary.
//
// The matrix deliberately includes the inputs a JSON DECODER cannot read: a
// truncated index (what VS Code transiently leaves while rewriting the file on
// install/uninstall) and escaped values that only exist after unescaping.
// Those are the exact shapes on which the two sides used to disagree, which
// made `doctor` name a binary the hooks were not running — so they are
// asserted here rather than documented as a known asymmetry.
func TestGuardShParity_RecordedBundleDir(t *testing.T) {
	if bashBinPath == "" {
		t.Skip("bash not available")
	}
	guardPath := guardShPath(t)

	cases := []struct {
		name    string
		content string
	}{
		{"single entry", `[{"identifier":{"id":"a.b"},"relativeLocation":"a.b-1.0.0"},{"identifier":{"id":"nightgauge.nightgauge-vscode"},"relativeLocation":"nightgauge.nightgauge-vscode-0.1.5"}]`},
		{"whitespace around colon", "[{\"relativeLocation\"\t:   \"nightgauge.nightgauge-vscode-0.2.0-rc.23-darwin-arm64\"}]"},
		{"pretty printed", "[\n  {\n    \"identifier\": { \"id\": \"nightgauge.nightgauge-vscode\" },\n    \"relativeLocation\": \"nightgauge.nightgauge-vscode-0.9.9\"\n  }\n]"},
		{"no trailing newline", `[{"relativeLocation":"nightgauge.nightgauge-vscode-0.5.0"}]`},
		{"two nightgauge entries", `[{"relativeLocation":"nightgauge.nightgauge-vscode-0.1.1"},{"relativeLocation":"nightgauge.nightgauge-vscode-0.1.2"}]`},
		{"no nightgauge entry", `[{"relativeLocation":"other.ext-1.0.0"}]`},
		{"empty array", `[]`},
		{"traversal rejected", `[{"relativeLocation":"nightgauge.nightgauge-vscode-../../evil"}]`},
		{"separator rejected", `[{"relativeLocation":"nightgauge.nightgauge-vscode-x/y"}]`},
		{"other publisher named similarly", `[{"relativeLocation":"nightgauge.nightgauge-vscode-companion-1.0.0"},{"relativeLocation":"notnightgauge.nightgauge-vscode-2.0.0"}]`},
		// Invalid JSON that still carries a scannable record: the shape VS
		// Code transiently leaves behind while rewriting extensions.json. A
		// decode-only Go side answered "" here while guard.sh honored the
		// record, so doctor reported a false stale-binary warning naming a
		// binary the hooks were not running.
		{"truncated mid-object after a complete entry", `[{"identifier":{"id":"nightgauge.nightgauge-vscode"},"relativeLocation":"nightgauge.nightgauge-vscode-0.2.0"},{"identi`},
		{"truncated with no closing bracket", `[{"relativeLocation":"nightgauge.nightgauge-vscode-0.3.0-darwin-arm64"}`},
		{"truncated before any record", `[{"identifier":{"id":"a.b"},"relativeLoc`},
		// Escapes: the value exists only after JSON unescaping, so guard.sh —
		// which reads the file's text — never sees it. Both sides must reject.
		{"unicode escape in the value", `[{"relativeLocation":"nightgauge.nightgauge-vscode-abc"}]`},
		{"escaped quote in the value", `[{"relativeLocation":"nightgauge.nightgauge-vscode-a\"b"}]`},
		{"escaped backslash in the value", `[{"relativeLocation":"nightgauge.nightgauge-vscode-a\\b"}]`},
		{"newline between the key and its value", "[{\"relativeLocation\":\n\"nightgauge.nightgauge-vscode-0.4.0\"}]"},
		// The other direction: a nested relativeLocation is not an entry to a
		// decoder, but it is one match to a text scan. Whatever the answer,
		// both sides must give it — this is the case that proves the two
		// cannot be reconciled by decoding on one side.
		{"relativeLocation nested inside another object", `[{"metadata":{"relativeLocation":"nightgauge.nightgauge-vscode-9.9"}}]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			writeExtensionsIndexRaw(t, home, tc.content)
			want := readRecordedBundleDir(home)
			got := guardShRecordedDir(t, guardPath, home)
			if got != want {
				t.Errorf("record parser disagreement: guard.sh %q, Go %q", got, want)
			}
		})
	}

	// The large fixture is in the matrix because size is where the two
	// implementations were most likely to drift: the shell reads the file with
	// one bounded `grep -o` and everything about that is O(n), while a 120-entry
	// index is the first input that makes an O(n^2) scan visible (#356 round 4).
	t.Run("large real-shaped index", func(t *testing.T) {
		home := t.TempDir()
		writeExtensionsIndexRaw(t, home, string(loadLargeExtensionsIndex(t)))
		want := readRecordedBundleDir(home)
		if want != largeIndexRecordedDir {
			t.Fatalf("Go parsed %q from the large index, want %q", want, largeIndexRecordedDir)
		}
		got := guardShRecordedDir(t, guardPath, home)
		if got != want {
			t.Errorf("record parser disagreement on the large index: guard.sh %q, Go %q", got, want)
		}
	})

	t.Run("file absent", func(t *testing.T) {
		home := t.TempDir()
		want := readRecordedBundleDir(home)
		got := guardShRecordedDir(t, guardPath, home)
		if got != want {
			t.Errorf("record parser disagreement on a missing index: guard.sh %q, Go %q", got, want)
		}
	})
}

// guardShPath locates guard.sh relative to the repo root, skipping the test
// when it cannot be found (e.g. a vendored-source build).
func guardShPath(t *testing.T) string {
	t.Helper()
	repoRoot, err := gitRepoRootForTest()
	if err != nil {
		t.Skipf("could not locate repo root for guard.sh: %v", err)
	}
	guardPath := filepath.Join(repoRoot, "claude-plugins", "nightgauge", "hooks", "lib", "guard.sh")
	if _, err := os.Stat(guardPath); err != nil {
		t.Skipf("guard.sh not found at %q: %v", guardPath, err)
	}
	return guardPath
}

// guardShResolve sources guard.sh in the test's current environment/cwd and
// returns the path it resolved. guard.sh itself shells out to `git rev-parse`,
// so git's directory is appended to whatever PATH the case configured.
//
// LC_ALL=C pins the shell's glob collation to byte order, which is what
// filepath.Glob's sort does. The two only differ for the unrecorded-fallback
// case, and pinning it here keeps that parity case deterministic instead of
// locale-dependent.
func guardShResolve(t *testing.T, guardPath string) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get cwd: %v", err)
	}
	shPath := filepath.Dir(gitBinPath) + string(os.PathListSeparator) + os.Getenv("PATH")
	cmd := exec.Command(bashBinPath, "-c", `source "$1"; echo "$NIGHTGAUGE_BINARY"`, "guard.sh", guardPath)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(),
		"NIGHTGAUGE_HOOK_BLOCKING=false",
		"HOME="+os.Getenv("HOME"),
		"PATH="+shPath,
		"LC_ALL=C",
		"NIGHTGAUGE_BIN="+os.Getenv("NIGHTGAUGE_BIN"),
		"NIGHTGAUGE_HOOK_LOG="+filepath.Join(t.TempDir(), "hook-warnings.log"),
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("guard.sh invocation failed: %v (stderr: %s)", err, stderr.String())
	}
	return trimTrailingNewline(stdout.String())
}

// guardShRecordedDir calls guard.sh's record parser in isolation against home.
//
// $NIGHTGAUGE_BIN is pointed at a stub so the cascade resolves at step 0:
// guard.sh `exit 0`s on the graceful-skip path, and since it is SOURCED that
// exit takes the whole shell with it — nothing after `source` would run.
func guardShRecordedDir(t *testing.T, guardPath, home string) string {
	t.Helper()
	stub := filepath.Join(t.TempDir(), "nightgauge")
	writeFakeBinary(t, stub)

	script := `source "$1" >/dev/null 2>&1
HOME="$2"
_ng_read_recorded_bundle_dir
printf '%s' "$_ng_recorded_dir"`
	cmd := exec.Command(bashBinPath, "-c", script, "guard.sh", guardPath, home)
	cmd.Dir = t.TempDir()
	cmd.Env = []string{
		"HOME=" + t.TempDir(),
		"PATH=/usr/bin:/bin",
		"LC_ALL=C",
		"NIGHTGAUGE_BIN=" + stub,
		"NIGHTGAUGE_HOOK_BLOCKING=false",
		"NIGHTGAUGE_HOOK_LOG=" + filepath.Join(t.TempDir(), "hook-warnings.log"),
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("guard.sh record parse failed: %v (stderr: %s)", err, stderr.String())
	}
	return stdout.String()
}

// --- captured VSCode fixtures (#356) ---------------------------------------
//
// internal/doctor/testdata/vscode-bundles/ holds two redacted captures of a
// real machine: bundle-layout.json (what is on disk) and
// extensions-index.json (what VS Code records as installed). The tests below
// build their temporary $HOME from those rather than from invented listings,
// so the shape under test is the shape that produced the #356 defect.

type capturedBundle struct {
	Dir               string `json:"dir"`
	BundleVersion     string `json:"bundle_version"`
	RecordedInstalled bool   `json:"recorded_installed"`
	Binary            string `json:"binary"`
	BinaryExists      bool   `json:"binary_exists"`
	BinaryExecutable  bool   `json:"binary_executable"`
	BinaryMtime       string `json:"binary_mtime"`
	VersionOutput     string `json:"version_output"`
}

type capturedBundleLayout struct {
	CapturedAt string `json:"captured_at"`
	CapturedBy string `json:"captured_by"`
	Glob       string `json:"glob"`
	// RecordedRelativeLocation is what extensions.json named at capture time.
	RecordedRelativeLocation string           `json:"recorded_relative_location"`
	Bundles                  []capturedBundle `json:"bundles"`
}

// loadCapturedBundleLayout reads the committed capture and asserts it is
// actually usable evidence: at least two bundles (one bundle cannot exhibit a
// selection defect), a recorded install that is NOT the first in glob order
// (otherwise the fixture cannot distinguish the record from first-glob-match),
// and every path still redacted to the `~` placeholder.
func loadCapturedBundleLayout(t *testing.T) capturedBundleLayout {
	t.Helper()
	path := filepath.Join(testdataDir, "vscode-bundles", "bundle-layout.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read captured bundle layout %s: %v", path, err)
	}
	var layout capturedBundleLayout
	if err := json.Unmarshal(raw, &layout); err != nil {
		t.Fatalf("parse captured bundle layout %s: %v", path, err)
	}
	if len(layout.Bundles) < 2 {
		t.Fatalf("captured bundle layout %s has %d bundle(s); the multi-bundle selection tests need at least 2 to mean anything", path, len(layout.Bundles))
	}
	if layout.RecordedRelativeLocation == "" {
		t.Fatalf("captured bundle layout %s records no installed bundle — re-capture with the current script", path)
	}
	if layout.Bundles[0].Dir == "~/.vscode/extensions/"+layout.RecordedRelativeLocation {
		t.Fatalf("captured bundle layout %s records the FIRST bundle in glob order; it cannot distinguish the install record from first-glob-match", path)
	}
	seen := map[string]bool{}
	recorded := 0
	for i, b := range layout.Bundles {
		if b.BundleVersion == "" {
			t.Fatalf("captured bundle %d has an empty bundle_version — fixture is unusable", i)
		}
		if seen[b.BundleVersion] {
			t.Fatalf("captured bundle layout repeats bundle_version %q", b.BundleVersion)
		}
		seen[b.BundleVersion] = true
		if b.RecordedInstalled {
			recorded++
		}
		if !strings.HasPrefix(b.Dir, "~/") || !strings.HasPrefix(b.Binary, "~/") {
			t.Fatalf("captured bundle %d is not redacted (dir=%q binary=%q); paths must start with the ~ placeholder", i, b.Dir, b.Binary)
		}
		for _, leak := range []string{"/Users/", "/home/", "/root/"} {
			if strings.Contains(b.Dir, leak) || strings.Contains(b.Binary, leak) {
				t.Fatalf("captured bundle %d leaks a home directory (%q) — re-run scripts/capture-vscode-bundle-layout.sh", i, leak)
			}
		}
	}
	if recorded != 1 {
		t.Fatalf("captured layout marks %d bundles as recorded_installed; VS Code records exactly one", recorded)
	}
	return layout
}

// writeCapturedExtensionsIndex installs the captured, redacted extensions.json
// into home with the `~` placeholder expanded. It also re-asserts the redaction
// on the way through, so a careless re-capture cannot land a home path in the
// repository unnoticed.
func writeCapturedExtensionsIndex(t *testing.T, home string) string {
	t.Helper()
	path := filepath.Join(testdataDir, "vscode-bundles", "extensions-index.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read captured extensions index %s: %v", path, err)
	}
	text := string(raw)
	for _, leak := range []string{"/Users/", "/home/", "/root/"} {
		if strings.Contains(text, leak) {
			t.Fatalf("captured extensions index leaks a home directory (%q) — re-run scripts/capture-vscode-bundle-layout.sh --extensions-index", leak)
		}
	}
	var records []vscodeExtensionRecord
	if err := json.Unmarshal(raw, &records); err != nil {
		t.Fatalf("captured extensions index is not valid JSON: %v", err)
	}
	if len(records) < 3 {
		t.Fatalf("captured extensions index has %d entries; the parser must be exercised against a real multi-publisher file", len(records))
	}
	return writeExtensionsIndexRaw(t, home, strings.ReplaceAll(text, "~/", home+"/"))
}

// vscodeExtensionRecord is the minimal shape of one extensions.json entry.
// The RESOLVER does not decode this file at all — see scanRecordedBundleDir
// for why a decoder cannot hold the #277 parity contract — so this type lives
// here, where it is used to assert that the committed fixtures are still
// well-formed VS Code indexes of the expected shape.
type vscodeExtensionRecord struct {
	RelativeLocation string `json:"relativeLocation"`
}

// largeIndexRecordedDir is the ONE nightgauge bundle directory recorded by
// testdata/vscode-bundles/extensions-index-large.json.
const largeIndexRecordedDir = bundleDirPrefix + "0.2.0-rc.23-darwin-arm64"

// loadLargeExtensionsIndex reads the synthetic 120-entry index and asserts it
// is still the thing the tests need it to be: a real-shaped, minified VS Code
// index (the shape VS Code itself writes), 120 entries, with the nightgauge
// entry buried deep in the list rather than sitting first.
//
// It exists because every other fixture here is 1-14 entries, and a 14-entry
// file is small enough to hide a superlinear parser: the builtin-only scan
// this branch shipped in round 3 cost ~23ms at 14 entries and ~958ms at 120,
// which no test could see. The fixture is synthetic and anonymized on purpose —
// a real 120-extension index cannot be published (third-party ids, marketplace
// GUIDs, install timestamps) — but its per-entry shape is copied from the
// captured one.
func loadLargeExtensionsIndex(t *testing.T) []byte {
	t.Helper()
	path := filepath.Join(testdataDir, "vscode-bundles", "extensions-index-large.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read large extensions index %s: %v", path, err)
	}
	for _, leak := range []string{"/Users/", "/home/", "/root/"} {
		if bytes.Contains(raw, []byte(leak)) {
			t.Fatalf("large extensions index leaks a home directory (%q)", leak)
		}
	}
	var records []vscodeExtensionRecord
	if err := json.Unmarshal(raw, &records); err != nil {
		t.Fatalf("large extensions index is not valid JSON: %v", err)
	}
	if len(records) != 120 {
		t.Fatalf("large extensions index has %d entries, want 120 — the point of this fixture is size", len(records))
	}
	hits, at := 0, -1
	for i, rec := range records {
		if strings.HasPrefix(rec.RelativeLocation, bundleDirPrefix) {
			hits++
			at = i
		}
	}
	if hits != 1 || records[at].RelativeLocation != largeIndexRecordedDir {
		t.Fatalf("large extensions index must record exactly one nightgauge bundle (%s); found %d", largeIndexRecordedDir, hits)
	}
	if at < 10 {
		t.Fatalf("the nightgauge entry sits at index %d; it must be deep in the list so a parser cannot pass by reading the head of the file", at)
	}
	if bytes.Contains(raw[:len(raw)-1], []byte("\n")) {
		t.Fatal("the large index must stay minified on ONE line — that is what VS Code writes, and it is the worst case for a text scan")
	}
	return raw
}

// TestReadRecordedBundleDir_LargeIndex asserts extraction is CORRECT at size,
// not merely fast: the recorded entry is the 100th of 120, so a parser that
// stops early, mis-anchors, or trips over the surrounding entries is caught.
func TestReadRecordedBundleDir_LargeIndex(t *testing.T) {
	home := t.TempDir()
	writeExtensionsIndexRaw(t, home, string(loadLargeExtensionsIndex(t)))

	if got := readRecordedBundleDir(home); got != largeIndexRecordedDir {
		t.Errorf("readRecordedBundleDir on the 120-entry index = %q, want %q", got, largeIndexRecordedDir)
	}
}

// TestScanVSCodeBundles_LargeIndexSelectsRecorded runs the whole step-4
// selection against the large index with a decoy bundle sorting FIRST in glob
// order, so the assertion can only pass by honoring the record.
func TestScanVSCodeBundles_LargeIndexSelectsRecorded(t *testing.T) {
	home := t.TempDir()
	writeExtensionsIndexRaw(t, home, string(loadLargeExtensionsIndex(t)))
	installBundle(t, home, bundleDirPrefix+"0.1.0-darwin-arm64", true)
	want := installBundle(t, home, largeIndexRecordedDir, true)

	scan := ScanVSCodeBundles(home)
	if scan.SelectedPath != want {
		t.Errorf("large index resolved %q, want the recorded install %q", scan.SelectedPath, want)
	}
	if scan.Divergence != DivergenceNone {
		t.Errorf("the recorded bundle is runnable; expected no divergence, got %q", scan.Divergence)
	}
}

// materializeCapturedBundles recreates the captured bundle directories under
// home. executable decides, per bundle, whether the binary gets its exec bit —
// letting a test model "both bundles installed" and "the recorded bundle's
// binary is not runnable" from the same capture. Returns bundle DIRECTORY NAME
// -> binary path, keyed to match extensions.json's relativeLocation.
func materializeCapturedBundles(t *testing.T, home string, layout capturedBundleLayout, executable func(capturedBundle) bool) map[string]string {
	t.Helper()
	paths := make(map[string]string, len(layout.Bundles))
	for _, b := range layout.Bundles {
		if !b.BinaryExists {
			continue
		}
		dir := bundleDirPrefix + b.BundleVersion
		binary := filepath.Join(home, ".vscode", "extensions", dir, "dist", "bin", "nightgauge")
		if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
			t.Fatalf("create bundle dir: %v", err)
		}
		mode := os.FileMode(0o644)
		if executable(b) {
			mode = 0o755
		}
		// The stub echoes its own bundle version so a test can tell which
		// bundle actually ran, not merely which path was reported.
		body := "#!/bin/sh\necho " + b.BundleVersion + "\n"
		if err := os.WriteFile(binary, []byte(body), mode); err != nil {
			t.Fatalf("write bundle binary: %v", err)
		}
		paths[dir] = binary
	}
	if len(paths) == 0 {
		t.Fatal("captured layout materialized zero bundle binaries — nothing under test")
	}
	return paths
}

// TestResolveBinary_Precedence pins the cascade ORDER itself (#356 AC4).
//
// Every other test in this file isolates the cascade so exactly one step can
// resolve, which pins per-step reachability but leaves the ORDER completely
// untested: swapping two steps keeps all of them green. Each case here
// satisfies an ADJACENT pair of steps simultaneously and asserts the earlier
// one wins, so a reorder of the chain fails here loudly.
func TestResolveBinary_Precedence(t *testing.T) {
	for _, tc := range precedenceCases() {
		t.Run(tc.name, func(t *testing.T) {
			if tc.needsGit && gitBinPath == "" {
				t.Skip("git not available")
			}
			winner, loser := tc.setup(t)
			if winner == "" || loser == "" {
				t.Fatalf("precedence case %q produced an empty path (winner=%q loser=%q)", tc.name, winner, loser)
			}
			if winner == loser {
				t.Fatalf("precedence case %q set up identical winner and loser paths (%q) — it cannot detect a reorder", tc.name, winner)
			}

			resolved := ResolveBinary()
			if resolved.Path != winner {
				t.Errorf("%s must win over %s: expected %q (step %q), got %q (step %q)", tc.higher, tc.lower, winner, tc.higher, resolved.Path, resolved.Step)
			}
			if resolved.Step != tc.higher {
				t.Errorf("expected resolving step %q, got %q", tc.higher, resolved.Step)
			}
		})
	}
}

type precedenceCase struct {
	name     string
	higher   ResolveBinaryStep
	lower    ResolveBinaryStep
	needsGit bool
	// setup returns (expected winner path, the lower-priority path it must beat).
	setup func(t *testing.T) (string, string)
}

// precedenceCases enumerates every ADJACENT pair in the cascade plus one
// end-to-end case with all six steps satisfiable. Shared by the Go-side
// precedence test and the guard.sh parity test so a reorder in EITHER
// implementation fails CI.
func precedenceCases() []precedenceCase {
	newVSCodeBundle := func(t *testing.T, home, version string) string {
		return installBundle(t, home, bundleDirPrefix+version, true)
	}
	newGoBin := func(t *testing.T, home string) string {
		p := filepath.Join(home, "go", "bin", "nightgauge")
		writeFakeBinary(t, p)
		return p
	}
	newGitRepo := func(t *testing.T) string {
		repo := realPath(t, t.TempDir())
		runGit(t, repo, "init")
		runGit(t, repo, "config", "user.email", "test@example.com")
		runGit(t, repo, "config", "user.name", "Test")
		runGit(t, repo, "commit", "--allow-empty", "-m", "init")
		return repo
	}

	return []precedenceCase{
		{
			name:   "env_override_beats_path",
			higher: StepEnvOverride,
			lower:  StepPath,
			setup: func(t *testing.T) (string, string) {
				isolateCascade(t)
				envBin := filepath.Join(realPath(t, t.TempDir()), "nightgauge")
				writeFakeBinary(t, envBin)
				pathDir := realPath(t, t.TempDir())
				pathBin := filepath.Join(pathDir, "nightgauge")
				writeFakeBinary(t, pathBin)
				t.Setenv("PATH", pathDir)
				t.Setenv("NIGHTGAUGE_BIN", envBin)
				return envBin, pathBin
			},
		},
		{
			name:     "path_beats_repo_bin",
			higher:   StepPath,
			lower:    StepRepoBin,
			needsGit: true,
			setup: func(t *testing.T) (string, string) {
				isolateCascade(t)
				repo := newGitRepo(t)
				repoBin := filepath.Join(repo, "bin", "nightgauge")
				writeFakeBinary(t, repoBin)
				t.Chdir(repo)
				pathDir := realPath(t, t.TempDir())
				pathBin := filepath.Join(pathDir, "nightgauge")
				writeFakeBinary(t, pathBin)
				t.Setenv("PATH", pathDir+string(os.PathListSeparator)+filepath.Dir(gitBinPath))
				return pathBin, repoBin
			},
		},
		{
			name:     "repo_bin_beats_canonical_repo_bin",
			higher:   StepRepoBin,
			lower:    StepCanonicalRepoBin,
			needsGit: true,
			setup: func(t *testing.T) (string, string) {
				isolateCascade(t)
				repo := newGitRepo(t)
				canonicalBin := filepath.Join(repo, "bin", "nightgauge")
				writeFakeBinary(t, canonicalBin)
				worktreeDir := filepath.Join(realPath(t, t.TempDir()), "worktree")
				if out, err := exec.Command(gitBinPath, "-C", repo, "worktree", "add", "--detach", worktreeDir).CombinedOutput(); err != nil {
					t.Skipf("git worktree add failed (environment limitation): %v: %s", err, out)
				}
				// The worktree carries its own bin/nightgauge — step 2 must
				// win over the canonical repo's copy at step 3.
				worktreeBin := filepath.Join(worktreeDir, "bin", "nightgauge")
				writeFakeBinary(t, worktreeBin)
				t.Chdir(worktreeDir)
				return worktreeBin, canonicalBin
			},
		},
		{
			name:     "canonical_repo_bin_beats_vscode_extension",
			higher:   StepCanonicalRepoBin,
			lower:    StepVSCodeExtension,
			needsGit: true,
			setup: func(t *testing.T) (string, string) {
				isolateCascade(t)
				home := os.Getenv("HOME")
				bundleBin := newVSCodeBundle(t, home, "0.1.2000000000")
				writeExtensionsIndex(t, home, bundleDirPrefix+"0.1.2000000000")
				repo := newGitRepo(t)
				canonicalBin := filepath.Join(repo, "bin", "nightgauge")
				writeFakeBinary(t, canonicalBin)
				worktreeDir := filepath.Join(realPath(t, t.TempDir()), "worktree")
				if out, err := exec.Command(gitBinPath, "-C", repo, "worktree", "add", "--detach", worktreeDir).CombinedOutput(); err != nil {
					t.Skipf("git worktree add failed (environment limitation): %v: %s", err, out)
				}
				t.Chdir(worktreeDir)
				return canonicalBin, bundleBin
			},
		},
		{
			name:   "vscode_extension_beats_go_bin",
			higher: StepVSCodeExtension,
			lower:  StepGoBin,
			setup: func(t *testing.T) (string, string) {
				isolateCascade(t)
				home := os.Getenv("HOME")
				goBin := newGoBin(t, home)
				bundleBin := newVSCodeBundle(t, home, "0.1.2000000000")
				writeExtensionsIndex(t, home, bundleDirPrefix+"0.1.2000000000")
				return bundleBin, goBin
			},
		},
		{
			name:     "env_override_beats_every_later_step",
			higher:   StepEnvOverride,
			lower:    StepGoBin,
			needsGit: true,
			setup: func(t *testing.T) (string, string) {
				isolateCascade(t)
				home := os.Getenv("HOME")
				goBin := newGoBin(t, home)
				newVSCodeBundle(t, home, "0.1.2000000000")
				writeExtensionsIndex(t, home, bundleDirPrefix+"0.1.2000000000")
				repo := newGitRepo(t)
				writeFakeBinary(t, filepath.Join(repo, "bin", "nightgauge"))
				t.Chdir(repo)
				pathDir := realPath(t, t.TempDir())
				writeFakeBinary(t, filepath.Join(pathDir, "nightgauge"))
				t.Setenv("PATH", pathDir+string(os.PathListSeparator)+filepath.Dir(gitBinPath))
				envBin := filepath.Join(realPath(t, t.TempDir()), "nightgauge")
				writeFakeBinary(t, envBin)
				t.Setenv("NIGHTGAUGE_BIN", envBin)
				return envBin, goBin
			},
		},
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command(gitBinPath, append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v: %s", args, err, out)
	}
}

func gitRepoRootForTest() (string, error) {
	if runtime.GOOS == "windows" {
		return "", os.ErrNotExist
	}
	cmd := exec.Command(gitBinPath, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return trimTrailingNewline(string(out)), nil
}

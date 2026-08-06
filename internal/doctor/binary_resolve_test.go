package doctor

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// gitBinPath and bashBinPath are resolved once via the real PATH at package
// init, before any test clears PATH via isolateCascade — tests that shell
// out to git/bash as *helpers* (not as the resolution target under test)
// must not be broken by the very PATH-clearing they're testing.
var (
	gitBinPath  string
	bashBinPath string
)

func init() {
	gitBinPath, _ = exec.LookPath("git")
	bashBinPath, _ = exec.LookPath("bash")
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
	extDir := filepath.Join(home, ".vscode", "extensions", "nightgauge.nightgauge-vscode-0.1.0", "dist", "bin")
	writeFakeBinary(t, filepath.Join(extDir, "nightgauge"))

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

// TestResolveBinary_GuardShParity asserts guard.sh resolves to the same path
// as ResolveBinary() for each of the five filesystem-based cascade steps
// (#277 AC2). Skips gracefully when bash is unavailable.
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
				home := os.Getenv("HOME")
				fake := filepath.Join(home, ".vscode", "extensions", "nightgauge.nightgauge-vscode-0.1.0", "dist", "bin", "nightgauge")
				writeFakeBinary(t, fake)
				return fake
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
			// #356: with several bundles installed, both implementations must
			// select the NEWEST — the case that previously existed in neither
			// suite and where the two implementations agreed on the WRONG
			// answer. Built from the captured real layout.
			name: "vscode_extension_multi_bundle",
			setup: func(t *testing.T) string {
				layout := loadCapturedBundleLayout(t)
				isolateCascade(t)
				home := os.Getenv("HOME")
				paths := materializeCapturedBundles(t, home, layout, func(capturedBundle) bool { return true })
				return paths[newestVersion(t, versionsOf(paths))]
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

// --- captured VSCode bundle-layout fixture (#356) -------------------------
//
// internal/doctor/testdata/vscode-bundles/bundle-layout.json is a redacted
// capture of a real machine's extension install (see the README next to it).
// The multi-bundle tests below build their temporary $HOME from that capture
// rather than from an invented directory listing, so the shape under test is
// the shape that actually produced the #356 defect.

type capturedBundle struct {
	Dir              string `json:"dir"`
	BundleVersion    string `json:"bundle_version"`
	Binary           string `json:"binary"`
	BinaryExists     bool   `json:"binary_exists"`
	BinaryExecutable bool   `json:"binary_executable"`
	VersionOutput    string `json:"version_output"`
}

type capturedBundleLayout struct {
	CapturedAt string           `json:"captured_at"`
	CapturedBy string           `json:"captured_by"`
	Glob       string           `json:"glob"`
	Bundles    []capturedBundle `json:"bundles"`
}

// loadCapturedBundleLayout reads the committed capture and asserts it is
// actually usable evidence: at least two bundles (one bundle cannot exhibit a
// selection defect), every bundle version non-empty, and every path still
// redacted to the `~` placeholder. An empty or single-bundle fixture would
// make every assertion below vacuously true, so it is a hard failure rather
// than a skip.
func loadCapturedBundleLayout(t *testing.T) capturedBundleLayout {
	t.Helper()
	path := filepath.Join("testdata", "vscode-bundles", "bundle-layout.json")
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
	seen := map[string]bool{}
	for i, b := range layout.Bundles {
		if b.BundleVersion == "" {
			t.Fatalf("captured bundle %d has an empty bundle_version — fixture is unusable", i)
		}
		if seen[b.BundleVersion] {
			t.Fatalf("captured bundle layout repeats bundle_version %q — versions must be distinct to order them", b.BundleVersion)
		}
		seen[b.BundleVersion] = true
		if !strings.HasPrefix(b.Dir, "~/") || !strings.HasPrefix(b.Binary, "~/") {
			t.Fatalf("captured bundle %d is not redacted (dir=%q binary=%q); paths must start with the ~ placeholder", i, b.Dir, b.Binary)
		}
		for _, leak := range []string{"/Users/", "/home/", "/root/"} {
			if strings.Contains(b.Dir, leak) || strings.Contains(b.Binary, leak) {
				t.Fatalf("captured bundle %d leaks a home directory (%q) — re-run scripts/capture-vscode-bundle-layout.sh", i, leak)
			}
		}
	}
	return layout
}

// materializeCapturedBundles recreates the captured bundle directories under
// home. executable decides, per bundle, whether the binary gets its exec bit —
// letting a test model "both bundles installed" and "the newest bundle's
// binary is not runnable" from the same capture. Returns bundle version ->
// binary path.
func materializeCapturedBundles(t *testing.T, home string, layout capturedBundleLayout, executable func(capturedBundle) bool) map[string]string {
	t.Helper()
	paths := make(map[string]string, len(layout.Bundles))
	for _, b := range layout.Bundles {
		if !b.BinaryExists {
			continue
		}
		binary := filepath.Join(home, ".vscode", "extensions", "nightgauge.nightgauge-vscode-"+b.BundleVersion, "dist", "bin", "nightgauge")
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
		paths[b.BundleVersion] = binary
	}
	if len(paths) == 0 {
		t.Fatal("captured layout materialized zero bundle binaries — nothing under test")
	}
	return paths
}

// versionLess is the test's independent ordering oracle. It is deliberately a
// separate implementation from the production comparator so a bug in the
// production comparator cannot make its own tests agree with it.
func versionLess(a, b string) bool {
	as := strings.Split(a, ".")
	bs := strings.Split(b, ".")
	n := len(as)
	if len(bs) > n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		av, bv := 0, 0
		if i < len(as) {
			av, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			bv, _ = strconv.Atoi(bs[i])
		}
		if av != bv {
			return av < bv
		}
	}
	return false
}

func newestVersion(t *testing.T, versions []string) string {
	t.Helper()
	if len(versions) == 0 {
		t.Fatal("newestVersion called with no versions")
	}
	sorted := append([]string(nil), versions...)
	sort.Slice(sorted, func(i, j int) bool { return versionLess(sorted[i], sorted[j]) })
	return sorted[len(sorted)-1]
}

func versionsOf(paths map[string]string) []string {
	out := make([]string, 0, len(paths))
	for v := range paths {
		out = append(out, v)
	}
	return out
}

// TestResolveBinary_VSCodeExtension_PicksNewestBundle is the #356 regression
// test: with more than one extension bundle installed, step 4 must select the
// NEWEST bundle, not whichever one the glob happens to list first.
//
// Both `filepath.Glob` and bash globbing return collation-sorted matches, so
// pre-fix both implementations took the lexicographically first entry — which
// for the captured layout is the OLDER bundle.
func TestResolveBinary_VSCodeExtension_PicksNewestBundle(t *testing.T) {
	layout := loadCapturedBundleLayout(t)
	isolateCascade(t)

	home := os.Getenv("HOME")
	paths := materializeCapturedBundles(t, home, layout, func(capturedBundle) bool { return true })
	want := paths[newestVersion(t, versionsOf(paths))]
	if want == "" {
		t.Fatal("no binary materialized for the newest captured bundle")
	}

	resolved := ResolveBinary()
	if resolved.Step != StepVSCodeExtension {
		t.Fatalf("expected step %q, got %q (path=%q)", StepVSCodeExtension, resolved.Step, resolved.Path)
	}
	if resolved.Path != want {
		t.Errorf("multi-bundle selection picked the wrong bundle:\n  got  %q\n  want %q (newest of %v)", resolved.Path, want, versionsOf(paths))
	}
}

// TestResolveBinary_VSCodeExtension_NewestBundleOrdersNumerically pins the
// comparison to numeric component ordering rather than string ordering. The
// captured layout's epoch suffixes are all the same digit count, so
// lexicographic and numeric order coincide there and cannot catch this; a
// bundle scheme with differing digit counts can.
func TestResolveBinary_VSCodeExtension_NewestBundleOrdersNumerically(t *testing.T) {
	isolateCascade(t)

	home := os.Getenv("HOME")
	// Lexicographic order of these three is ["0.0.5", "0.1.10", "0.1.9"], so
	// the lexicographic FIRST (0.0.5, the old first-match rule) and the
	// lexicographic LAST (0.1.9, a naive "sort and take the end" fix) are both
	// wrong; only numeric component ordering yields 0.1.10.
	for _, v := range []string{"0.1.9", "0.1.10", "0.0.5"} {
		writeFakeBinary(t, filepath.Join(home, ".vscode", "extensions", "nightgauge.nightgauge-vscode-"+v, "dist", "bin", "nightgauge"))
	}
	want := filepath.Join(home, ".vscode", "extensions", "nightgauge.nightgauge-vscode-0.1.10", "dist", "bin", "nightgauge")

	resolved := ResolveBinary()
	if resolved.Path != want {
		t.Errorf("expected numeric ordering to pick %q, got %q", want, resolved.Path)
	}
}

// TestResolveBinary_VSCodeExtension_SkipsNonExecutableNewestBundle asserts the
// scan still returns a usable binary when the newest bundle's binary cannot be
// run (partial install, lost exec bit). The older executable bundle is the
// correct answer — and it is precisely the case guard.sh must report as stale.
func TestResolveBinary_VSCodeExtension_SkipsNonExecutableNewestBundle(t *testing.T) {
	layout := loadCapturedBundleLayout(t)
	isolateCascade(t)

	home := os.Getenv("HOME")
	allVersions := make([]string, 0, len(layout.Bundles))
	for _, b := range layout.Bundles {
		allVersions = append(allVersions, b.BundleVersion)
	}
	newest := newestVersion(t, allVersions)

	paths := materializeCapturedBundles(t, home, layout, func(b capturedBundle) bool {
		return b.BundleVersion != newest
	})
	remaining := make([]string, 0, len(paths))
	for v := range paths {
		if v != newest {
			remaining = append(remaining, v)
		}
	}
	if len(remaining) == 0 {
		t.Fatal("no executable bundle left after disabling the newest — fixture cannot express this case")
	}
	want := paths[newestVersion(t, remaining)]

	resolved := ResolveBinary()
	if resolved.Step != StepVSCodeExtension {
		t.Fatalf("expected step %q, got %q (path=%q)", StepVSCodeExtension, resolved.Step, resolved.Path)
	}
	if resolved.Path != want {
		t.Errorf("expected the newest EXECUTABLE bundle %q, got %q", want, resolved.Path)
	}
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
		p := filepath.Join(home, ".vscode", "extensions", "nightgauge.nightgauge-vscode-"+version, "dist", "bin", "nightgauge")
		writeFakeBinary(t, p)
		return p
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
	cmd := exec.Command(gitBinPath, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return trimTrailingNewline(string(out)), nil
}

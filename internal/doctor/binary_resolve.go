package doctor

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// ResolveBinaryStep identifies which cascade step resolved the binary.
type ResolveBinaryStep string

const (
	StepEnvOverride      ResolveBinaryStep = "NIGHTGAUGE_BIN"
	StepPath             ResolveBinaryStep = "PATH"
	StepRepoBin          ResolveBinaryStep = "repo_bin"
	StepCanonicalRepoBin ResolveBinaryStep = "canonical_repo_bin"
	StepVSCodeExtension  ResolveBinaryStep = "vscode_extension"
	StepGoBin            ResolveBinaryStep = "go_bin"
)

// ResolvedBinary is the result of walking the cascade.
type ResolvedBinary struct {
	Path string
	Step ResolveBinaryStep // empty when unresolved
	// Bundles is the step-4 VSCode-extension inventory. It is populated on
	// EVERY resolve, not just when step 4 wins, because `nightgauge doctor`
	// reports it either way: inside a nightgauge checkout the hooks use
	// <repo>/bin/nightgauge and the installed bundles are invisible, which is
	// exactly the cwd-dependence that made #356 impossible to notice.
	Bundles VSCodeBundleScan
}

// VSCodeBundle is one nightgauge VSCode extension bundle whose bundled binary
// file exists on disk.
type VSCodeBundle struct {
	Version    string // directory suffix, e.g. "0.1.1785982325"
	Path       string // <bundle-dir>/dist/bin/nightgauge
	Executable bool
}

// VSCodeBundleScan summarizes the step-4 candidate set.
type VSCodeBundleScan struct {
	Bundles         []VSCodeBundle // newest first
	SelectedPath    string         // newest EXECUTABLE bundle binary ("" when none is runnable)
	SelectedVersion string
	NewestVersion   string // newest bundle whose binary file exists, runnable or not
	// Superseded reports that the selected bundle is not the newest one
	// installed — i.e. the hooks would run a stale binary. Post-#356 this can
	// only happen when the newer bundle's binary is present but not
	// executable (partial install, lost exec bit), because selection is by
	// version rather than by glob order.
	Superseded bool
}

// ScanVSCodeBundles enumerates ~/.vscode/extensions/nightgauge.nightgauge-vscode-*
// bundles under home and picks the NEWEST executable one.
//
// #356: this used to be "first executable glob match wins". Both bash globbing
// and filepath.Glob return collation-sorted matches, so on a machine with two
// installed bundles the hooks silently ran whichever version sorted first —
// the OLDER one, for the epoch-suffixed versions the extension build emits.
// The bundle version is the only totally-ordered, exec-free signal available
// here (the binary's own stamp is a `git describe` string and the plugin
// version is hand-maintained; neither is comparable to it), so selection is by
// bundle version, compared component-wise and numerically.
//
// Modification time is deliberately NOT used: on the machine captured in
// internal/doctor/testdata/vscode-bundles/ the older-versioned bundle's binary
// had the later mtime, so an mtime ranking picks the wrong bundle and still
// passes a naive local check.
//
// guard.sh implements this identically; see the parity tests in
// binary_resolve_test.go (#277).
func ScanVSCodeBundles(home string) VSCodeBundleScan {
	var scan VSCodeBundleScan
	if home == "" {
		return scan
	}
	matches, _ := filepath.Glob(filepath.Join(home, ".vscode", "extensions", "nightgauge.nightgauge-vscode-*", "dist", "bin", "nightgauge"))
	for _, candidate := range matches {
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() {
			continue
		}
		version := bundleVersionFromPath(candidate)
		if version == "" {
			continue
		}
		bundle := VSCodeBundle{
			Version:    version,
			Path:       candidate,
			Executable: info.Mode()&0o111 != 0,
		}
		scan.Bundles = append(scan.Bundles, bundle)

		if scan.NewestVersion == "" || compareBundleVersions(version, scan.NewestVersion) > 0 {
			scan.NewestVersion = version
		}
		if bundle.Executable && (scan.SelectedPath == "" || compareBundleVersions(version, scan.SelectedVersion) > 0) {
			scan.SelectedPath = candidate
			scan.SelectedVersion = version
		}
	}
	sort.SliceStable(scan.Bundles, func(i, j int) bool {
		return compareBundleVersions(scan.Bundles[i].Version, scan.Bundles[j].Version) > 0
	})
	scan.Superseded = scan.SelectedPath != "" && scan.SelectedVersion != scan.NewestVersion
	return scan
}

// bundleVersionFromPath extracts "0.1.1785982325" from
// <home>/.vscode/extensions/nightgauge.nightgauge-vscode-0.1.1785982325/dist/bin/nightgauge.
// Returns "" when the path does not carry a version suffix.
func bundleVersionFromPath(binaryPath string) string {
	const prefix = "nightgauge.nightgauge-vscode-"
	// <bundle-dir>/dist/bin/nightgauge → <bundle-dir>
	dir := filepath.Dir(filepath.Dir(filepath.Dir(binaryPath)))
	base := filepath.Base(dir)
	if !strings.HasPrefix(base, prefix) {
		return ""
	}
	return strings.TrimPrefix(base, prefix)
}

// compareBundleVersions orders two dotted bundle versions component-wise and
// numerically: -1 when a < b, 0 when equal, 1 when a > b. A missing or
// non-numeric component counts as 0, matching guard.sh's comparator exactly.
//
// String comparison would work today only by accident — the extension emits a
// fixed-width 10-digit epoch suffix — and would silently invert the moment
// that width changes.
func compareBundleVersions(a, b string) int {
	as := strings.Split(a, ".")
	bs := strings.Split(b, ".")
	n := len(as)
	if len(bs) > n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		av := bundleVersionComponent(as, i)
		bv := bundleVersionComponent(bs, i)
		if av != bv {
			if av < bv {
				return -1
			}
			return 1
		}
	}
	return 0
}

func bundleVersionComponent(parts []string, i int) int {
	if i >= len(parts) {
		return 0
	}
	part := parts[i]
	if part == "" {
		return 0
	}
	for _, r := range part {
		if r < '0' || r > '9' {
			return 0
		}
	}
	v, err := strconv.Atoi(part)
	if err != nil {
		return 0
	}
	return v
}

// ResolveBinary walks the same six-step cascade documented in
// claude-plugins/nightgauge/hooks/lib/guard.sh (#3234, #4029, #277):
//
//  0. $NIGHTGAUGE_BIN
//  1. PATH (exec.LookPath)
//  2. <repo-root>/bin/nightgauge (git rev-parse --show-toplevel)
//  3. <canonical-repo-root>/bin/nightgauge (git rev-parse --git-common-dir)
//  4. ~/.vscode/extensions/nightgauge.nightgauge-vscode-*/dist/bin/nightgauge
//     — the NEWEST installed bundle by version (#356), never the first glob
//     match
//  5. ~/go/bin/nightgauge
//
// This is the single canonical implementation of the cascade on the Go side;
// binary_resolve_test.go asserts guard.sh resolves identically for each of
// the five filesystem-based steps (step 0 is a trivial passthrough in both)
// AND pins the ORDER via an adjacent-pair precedence matrix run through both
// implementations, so a future edit cannot silently reorder the chain.
func ResolveBinary() ResolvedBinary {
	home, homeErr := os.UserHomeDir()
	if homeErr != nil {
		home = ""
	}
	// The bundle inventory is collected up front so it is reported no matter
	// which step wins (see ResolvedBinary.Bundles).
	bundles := ScanVSCodeBundles(home)

	// 0. $NIGHTGAUGE_BIN — only when it points at an executable file.
	if envBin := os.Getenv("NIGHTGAUGE_BIN"); envBin != "" && isExecutable(envBin) {
		return ResolvedBinary{Path: envBin, Step: StepEnvOverride, Bundles: bundles}
	}

	// 1. PATH lookup.
	if path, err := exec.LookPath("nightgauge"); err == nil {
		return ResolvedBinary{Path: path, Step: StepPath, Bundles: bundles}
	}

	// 2. <repo-root>/bin/nightgauge.
	if repoRoot, err := gitRevParse("--show-toplevel"); err == nil && repoRoot != "" {
		candidate := filepath.Join(repoRoot, "bin", "nightgauge")
		if isExecutable(candidate) {
			return ResolvedBinary{Path: candidate, Step: StepRepoBin, Bundles: bundles}
		}
	}

	// 3. <canonical-repo-root>/bin/nightgauge.
	if gitCommonDir, err := gitRevParse("--git-common-dir"); err == nil && gitCommonDir != "" {
		canonicalRepo := filepath.Dir(gitCommonDir)
		candidate := filepath.Join(canonicalRepo, "bin", "nightgauge")
		if isExecutable(candidate) {
			return ResolvedBinary{Path: candidate, Step: StepCanonicalRepoBin, Bundles: bundles}
		}
	}

	// 4. VSCode extension bundle — the NEWEST installed bundle, not the first
	//    glob match (#356).
	if bundles.SelectedPath != "" {
		return ResolvedBinary{Path: bundles.SelectedPath, Step: StepVSCodeExtension, Bundles: bundles}
	}

	// 5. ~/go/bin/nightgauge.
	if home != "" {
		candidate := filepath.Join(home, "go", "bin", "nightgauge")
		if isExecutable(candidate) {
			return ResolvedBinary{Path: candidate, Step: StepGoBin, Bundles: bundles}
		}
	}

	return ResolvedBinary{Bundles: bundles}
}

// isExecutable reports whether path exists, is a regular file, and has at
// least one executable bit set.
func isExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode()&0o111 != 0
}

// gitRevParse runs `git rev-parse <arg>` in the current working directory and
// returns its trimmed output. Errors (no git, not a repo) are returned as-is
// so callers can treat them as "step not applicable", mirroring guard.sh's
// `2>/dev/null || true` fallback.
func gitRevParse(arg string) (string, error) {
	cmd := exec.Command("git", "rev-parse", arg)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return trimTrailingNewline(string(out)), nil
}

func trimTrailingNewline(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

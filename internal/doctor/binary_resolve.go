package doctor

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

// bundleDirPrefix is the VS Code extension directory prefix for this
// extension. A bundle directory is `<prefix><version>[-<targetPlatform>]`,
// and extensions.json records exactly that directory name in
// `relativeLocation`.
const bundleDirPrefix = "nightgauge.nightgauge-vscode-"

// extensionsIndexRelPath is VS Code's own record of what it has installed.
const extensionsIndexRelPath = ".vscode/extensions/extensions.json"

// BundleDivergence explains why a step-4 selection is NOT confirmed by VS
// Code's install record. Empty means the resolution is confirmed (or is the
// only possible answer), which is the silent case.
type BundleDivergence string

const (
	// DivergenceNone — the recorded bundle was used, or exactly one bundle
	// exists and nothing contradicts it.
	DivergenceNone BundleDivergence = ""
	// DivergenceRecordUnusable — extensions.json names an installed bundle
	// that could not be used: its directory is absent, its binary is missing,
	// or the binary is not executable.
	DivergenceRecordUnusable BundleDivergence = "record_unusable"
	// DivergenceUnrecorded — no usable record exists (no extensions.json, or
	// zero/multiple nightgauge entries in it) AND more than one bundle is on
	// disk, so the selection is a guess between real candidates.
	DivergenceUnrecorded BundleDivergence = "unrecorded_ambiguous"
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

// VSCodeBundle is one nightgauge VSCode extension bundle directory whose
// bundled binary file exists on disk.
type VSCodeBundle struct {
	Version    string // directory suffix, e.g. "0.1.1785982325"
	Dir        string // directory base name, comparable to relativeLocation
	Path       string // <bundle-dir>/dist/bin/nightgauge
	Executable bool
	Recorded   bool // this directory is the one extensions.json records
}

// VSCodeBundleScan summarizes the step-4 candidate set and how the selection
// relates to VS Code's own install record.
type VSCodeBundleScan struct {
	// Bundles is in glob order — the same collation order both implementations
	// iterate — not "newest first". Nothing here orders versions (#356).
	Bundles []VSCodeBundle

	SelectedPath    string // "" when no bundle binary is runnable
	SelectedVersion string

	// RecordedDir is extensions.json's `relativeLocation` for
	// nightgauge.nightgauge-vscode, and RecordedVersion its version segment.
	// Both are empty when there is no usable record. They are DISPLAY values:
	// never ordered, never compared for newness.
	RecordedDir     string
	RecordedVersion string
	// RecordedUsed reports that the selection came from the record.
	RecordedUsed bool

	// Divergence is empty when the resolution is confirmed (recorded bundle
	// used) or unambiguous (a single unrecorded bundle). It is meaningful only
	// when SelectedPath != "".
	Divergence BundleDivergence
}

// ScanVSCodeBundles enumerates ~/.vscode/extensions/nightgauge.nightgauge-vscode-*
// bundles under home and picks the one VS Code RECORDS as installed.
//
// # Why the record, and not the highest version number
//
// #356 was "the hooks silently run a superseded bundle". The obvious repair —
// pick the highest-parsing version among the glob matches — is not a repair,
// because "highest version number on disk" is not "the bundle VS Code
// installed". Three verified counter-examples, all reachable on this project:
//
//   - DOWNGRADE. packages/nightgauge-vscode/scripts/dev-install.sh stamps
//     `0.1.<epoch>` (package.json stays 0.1.0 on main forever, and release.yml
//     restores it after `npm version`). The moment v0.2.0 ships, every
//     maintainer dev-install loses numerically to a leftover
//     `…-0.2.0-darwin-arm64` directory — i.e. the dogfood build is silently
//     discarded in favour of the thing it was built to replace.
//   - PRERELEASE. staging.yml packages RC VSIXes with
//     `npm version 0.2.0-rc.23` + `vsce package --target <t>`, so bundle
//     directories really are `…-0.2.0-rc.23-darwin-arm64`. Any comparator that
//     reduces a version to its dotted numeric prefix collapses rc.22 and rc.23
//     to `0.2.0` — equal — and falls back to first-glob-match, i.e. the older
//     one. That is #356 verbatim, on the channel this repo actually ships.
//   - ORPHANS. The glob also matches partial-install leftovers such as
//     `…-0.2.2-darwin-arm64.vsctmp`, which can out-parse the real install.
//
// All three vanish when the authority is the record rather than the parse:
// ~/.vscode/extensions/extensions.json is written by VS Code and names exactly
// one directory per installed extension. A recorded bundle whose binary is
// runnable IS the answer, even when some other directory carries a bigger
// number — a downgrade resolves to the older bundle, silently, because that is
// what is installed.
//
// # Fallback
//
// When there is no usable record (file absent, or zero/multiple nightgauge
// entries in it) or the recorded bundle cannot be used (absent, or its
// binary is missing/non-executable), selection falls back to the FIRST
// executable glob match — the behaviour that shipped before #356. It is
// deterministic and needs no ordering. Divergence then records why, so the
// caller can signal it; a single unrecorded bundle is not ambiguous and stays
// silent.
//
// guard.sh implements this identically; see the parity tests in
// binary_resolve_test.go (#277).
func ScanVSCodeBundles(home string) VSCodeBundleScan {
	var scan VSCodeBundleScan
	if home == "" {
		return scan
	}

	scan.RecordedDir = readRecordedBundleDir(home)
	if scan.RecordedDir != "" {
		scan.RecordedVersion = strings.TrimPrefix(scan.RecordedDir, bundleDirPrefix)
	}

	matches, _ := filepath.Glob(filepath.Join(home, ".vscode", "extensions", bundleDirPrefix+"*", "dist", "bin", "nightgauge"))

	var (
		recordedPath      string
		recordedRunnable  bool
		firstRunnablePath string
		firstRunnableVer  string
	)
	for _, candidate := range matches {
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() {
			continue
		}
		dir := filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(candidate))))
		if !strings.HasPrefix(dir, bundleDirPrefix) {
			continue
		}
		version := strings.TrimPrefix(dir, bundleDirPrefix)
		if version == "" {
			continue
		}
		bundle := VSCodeBundle{
			Version:    version,
			Dir:        dir,
			Path:       candidate,
			Executable: info.Mode()&0o111 != 0,
			Recorded:   scan.RecordedDir != "" && dir == scan.RecordedDir,
		}
		scan.Bundles = append(scan.Bundles, bundle)

		if bundle.Recorded && bundle.Executable {
			recordedPath, recordedRunnable = candidate, true
		}
		if bundle.Executable && firstRunnablePath == "" {
			firstRunnablePath, firstRunnableVer = candidate, version
		}
	}

	switch {
	case recordedRunnable:
		scan.SelectedPath = recordedPath
		scan.SelectedVersion = scan.RecordedVersion
		scan.RecordedUsed = true
	case firstRunnablePath != "":
		scan.SelectedPath = firstRunnablePath
		scan.SelectedVersion = firstRunnableVer
		if scan.RecordedDir != "" {
			scan.Divergence = DivergenceRecordUnusable
		} else if len(scan.Bundles) > 1 {
			scan.Divergence = DivergenceUnrecorded
		}
	}
	return scan
}

// recordedLocationPattern extracts a nightgauge `relativeLocation` value from
// the RAW text of extensions.json. It is the exact expression guard.sh hands
// to `grep -o -E`, deliberately so: whitespace is tolerated around the colon,
// the value is bounded by the closing quote, and — because grep is
// line-oriented — neither the whitespace runs nor the value may cross a
// newline. Any drift between the two spellings is a parity break (#277).
var recordedLocationPattern = regexp.MustCompile(`"relativeLocation"[ \t\f\v]*:[ \t\f\v]*"` + regexp.QuoteMeta(bundleDirPrefix) + `[^"\n]*"`)

// scanRecordedBundleDir applies guard.sh's extraction to raw file bytes: take
// the ONE nightgauge relativeLocation in the text, require it to be a plain
// directory name, and treat zero or several matches as "no record".
//
// This is the single, shared definition of "what the record says" — the whole
// answer on both sides, not a fallback.
//
// # Why not decode the JSON here
//
// The obvious Go implementation decodes extensions.json and reads the field.
// It cannot satisfy the #277 parity contract, because a decoder and a text
// scan disagree in BOTH directions, and each disagreement makes `doctor` name
// a binary the hooks are not running:
//
//   - The decoder sees MORE. A truncated index — the shape VS Code transiently
//     leaves while rewriting the file on install/uninstall — decodes to
//     nothing while still carrying a perfectly readable record, so guard.sh
//     honors the install and Go reports a stale-binary warning that is false.
//     Escaped values (`a`, `\"`) and a newline between the key and its
//     value are the same class: real to a decoder, invisible to a line-oriented
//     scan.
//   - The scan sees MORE. A `relativeLocation` NESTED inside another object is
//     not an entry to the decoder, but it is one match to the scan.
//
// Every attempt to reconcile the two (require both to agree, fall back on
// decode failure) leaves one of those directions broken. So there is one
// algorithm, expressed twice, and TestGuardShParity_RecordedBundleDir asserts
// the two spellings agree on the entire matrix — valid, malformed and
// adversarial alike. Both are conservative the same way: the value must be a
// plain directory name, and zero or several matches mean "no record".
func scanRecordedBundleDir(raw []byte) string {
	matches := recordedLocationPattern.FindAll(raw, 2)
	if len(matches) != 1 {
		return ""
	}
	value := string(matches[0])
	value = strings.TrimSuffix(value, `"`)
	if i := strings.LastIndex(value, `"`); i >= 0 {
		value = value[i+1:]
	}
	if !isPlainDirName(value) {
		return ""
	}
	return value
}

// readRecordedBundleDir returns the `relativeLocation` extensions.json records
// for this extension, or "" when there is no usable record.
//
// Conservative by construction: the record is used ONLY when exactly one entry
// names a nightgauge bundle directory. Zero entries (never installed, or the
// file is missing) and multiple entries both mean "no record", which falls
// back rather than guessing. Multiple entries for one extension id are not
// believed to be a state VS Code produces, but that is an assumption rather
// than a documented guarantee — profiles and pinned installs are plausible
// producers — so the comment states the consequence instead of the premise:
// the fallback is first-executable-glob-match, i.e. the pre-#356 selection,
// now accompanied by a divergence signal.
//
// Selection is by the relativeLocation VALUE rather than by identifier.id so
// that this and guard.sh apply the same rule: guard.sh cannot walk a JSON
// object graph under the bash-3.2/no-jq floor, and a rule the shell cannot
// express is a rule the two implementations cannot share (#277). The value is
// also validated as a plain directory name — no separators, no traversal —
// before it is joined onto a path.
//
// The extraction itself is scanRecordedBundleDir, which is guard.sh's
// algorithm expressed in Go; see there for why this does not decode the JSON.
func readRecordedBundleDir(home string) string {
	raw, err := os.ReadFile(filepath.Join(home, filepath.FromSlash(extensionsIndexRelPath)))
	if err != nil {
		return ""
	}
	return scanRecordedBundleDir(raw)
}

// isPlainDirName rejects anything that is not a single directory name, so a
// hand-edited or hostile extensions.json cannot steer resolution outside
// ~/.vscode/extensions.
func isPlainDirName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	return !strings.ContainsAny(name, `/\`) && !strings.Contains(name, "..")
}

// ResolveBinary walks the same six-step cascade documented in
// claude-plugins/nightgauge/hooks/lib/guard.sh (#3234, #4029, #277):
//
//  0. $NIGHTGAUGE_BIN
//  1. PATH (exec.LookPath)
//  2. <repo-root>/bin/nightgauge (git rev-parse --show-toplevel)
//  3. <canonical-repo-root>/bin/nightgauge (git rev-parse --git-common-dir)
//  4. ~/.vscode/extensions/nightgauge.nightgauge-vscode-*/dist/bin/nightgauge
//     — the bundle VS Code RECORDS as installed (#356), falling back to the
//     first glob match when there is no usable record
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

	// 4. VSCode extension bundle — the RECORDED install (#356).
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

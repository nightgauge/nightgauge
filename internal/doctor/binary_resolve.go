package doctor

import (
	"os"
	"os/exec"
	"path/filepath"
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
}

// ResolveBinary walks the same six-step cascade documented in
// claude-plugins/nightgauge/hooks/lib/guard.sh (#3234, #4029, #277):
//
//  0. $NIGHTGAUGE_BIN
//  1. PATH (exec.LookPath)
//  2. <repo-root>/bin/nightgauge (git rev-parse --show-toplevel)
//  3. <canonical-repo-root>/bin/nightgauge (git rev-parse --git-common-dir)
//  4. ~/.vscode/extensions/nightgauge.nightgauge-vscode-*/dist/bin/nightgauge
//  5. ~/go/bin/nightgauge
//
// This is the single canonical implementation of the cascade on the Go side;
// binary_resolve_test.go asserts guard.sh resolves identically for each of
// the five filesystem-based steps (step 0 is a trivial passthrough in both).
func ResolveBinary() ResolvedBinary {
	// 0. $NIGHTGAUGE_BIN — only when it points at an executable file.
	if envBin := os.Getenv("NIGHTGAUGE_BIN"); envBin != "" && isExecutable(envBin) {
		return ResolvedBinary{Path: envBin, Step: StepEnvOverride}
	}

	// 1. PATH lookup.
	if path, err := exec.LookPath("nightgauge"); err == nil {
		return ResolvedBinary{Path: path, Step: StepPath}
	}

	// 2. <repo-root>/bin/nightgauge.
	if repoRoot, err := gitRevParse("--show-toplevel"); err == nil && repoRoot != "" {
		candidate := filepath.Join(repoRoot, "bin", "nightgauge")
		if isExecutable(candidate) {
			return ResolvedBinary{Path: candidate, Step: StepRepoBin}
		}
	}

	// 3. <canonical-repo-root>/bin/nightgauge.
	if gitCommonDir, err := gitRevParse("--git-common-dir"); err == nil && gitCommonDir != "" {
		canonicalRepo := filepath.Dir(gitCommonDir)
		candidate := filepath.Join(canonicalRepo, "bin", "nightgauge")
		if isExecutable(candidate) {
			return ResolvedBinary{Path: candidate, Step: StepCanonicalRepoBin}
		}
	}

	// 4. VSCode extension bundle.
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		matches, _ := filepath.Glob(filepath.Join(home, ".vscode", "extensions", "nightgauge.nightgauge-vscode-*", "dist", "bin", "nightgauge"))
		for _, candidate := range matches {
			if isExecutable(candidate) {
				return ResolvedBinary{Path: candidate, Step: StepVSCodeExtension}
			}
		}
	}

	// 5. ~/go/bin/nightgauge.
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidate := filepath.Join(home, "go", "bin", "nightgauge")
		if isExecutable(candidate) {
			return ResolvedBinary{Path: candidate, Step: StepGoBin}
		}
	}

	return ResolvedBinary{}
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

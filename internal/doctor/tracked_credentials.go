package doctor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/platform"
)

// Tracked credentials under .nightgauge/ (#2024).
//
// The config loader refuses a plaintext token or license key in the project
// and local tiers (#2023), but that only stops the next one. A credential
// committed before that change, or pasted into some other file under
// .nightgauge/ (a saved query, a report, a log that was once tracked), stays in
// the repository, and nothing told the team. This check reads only what git
// tracks there, so an untracked runtime file never produces a finding.
//
// The scan never prints, logs or returns a matched value: each finding carries
// the pattern's prefix followed by "…", and nothing else of the match.

// trackedCredentialsCheck is the check's key in DoctorResult.Checks.
const trackedCredentialsCheck = "tracked_secrets"

// maxScannedFileBytes bounds the per-file read so a large tracked artefact
// cannot stall doctor. Larger files are skipped with a note.
const maxScannedFileBytes = 1 << 20

// binarySniffBytes is how much of a file is inspected for a NUL byte, the same
// heuristic git uses to call a file binary.
const binarySniffBytes = 8000

// SecretFinding is one tracked line holding something shaped like a
// credential. Redacted is the pattern's prefix plus "…"; the matched value is
// never carried.
type SecretFinding struct {
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Pattern  string `json:"pattern"`
	Redacted string `json:"redacted"`
}

// credentialPattern recognises one credential shape. prefix is both what the
// finding shows and what the regexp is anchored on.
type credentialPattern struct {
	name   string
	prefix string
	re     *regexp.Regexp
}

// credentialPatterns are GitHub's token prefixes and the license-key prefixes
// the platform's own key parser recognises (platform.LicenseKeyPrefixes), so a
// new key shape is picked up here without a second list.
var credentialPatterns = buildCredentialPatterns()

func buildCredentialPatterns() []credentialPattern {
	var out []credentialPattern
	// GitHub classic and OAuth/app tokens: prefix, then at least 20 base62
	// characters (issued tokens carry 36), so prose such as "ghp_example"
	// is not a finding.
	for _, p := range []string{"ghp_", "gho_", "ghu_", "ghs_", "ghr_"} {
		out = append(out, credentialPattern{
			name:   "github-token",
			prefix: p,
			re:     regexp.MustCompile(`\b` + regexp.QuoteMeta(p) + `[A-Za-z0-9]{20,}`),
		})
	}
	out = append(out, credentialPattern{
		name:   "github-fine-grained-token",
		prefix: "github_pat_",
		re:     regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{22,}`),
	})
	for _, p := range platform.LicenseKeyPrefixes() {
		out = append(out, credentialPattern{
			name:   "nightgauge-license-key",
			prefix: p,
			re:     regexp.MustCompile(`\b` + regexp.QuoteMeta(p) + `[A-Za-z0-9_-]{16,}`),
		})
	}
	return out
}

// trackedFileLister lists the tracked files under .nightgauge/ in the work
// tree containing dir, as paths relative to the returned root. ok=false means
// dir is not inside a git work tree. A variable so a test can prove the check
// reads only tracked files.
var trackedFileLister = gitTrackedNightgaugeFiles

func gitTrackedNightgaugeFiles(dir string) (root string, files []string, ok bool, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	top, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", nil, false, nil
	}
	root = strings.TrimSpace(string(top))
	if root == "" {
		return "", nil, false, nil
	}
	out, err := exec.CommandContext(ctx, "git", "-C", root, "ls-files", "-z", "--", ".nightgauge").Output()
	if err != nil {
		return root, nil, true, fmt.Errorf("git ls-files: %w", err)
	}
	for _, f := range strings.Split(string(out), "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	return root, files, true, nil
}

// checkTrackedCredentials scans the tracked files under .nightgauge/ for
// credential shapes. The warning string is empty when nothing was found.
// A finding is a warning, not a required failure: the environment still runs,
// and the remedy (rotation, history rewrite) is the operator's, never doctor's.
func checkTrackedCredentials(dir string) (CheckItem, string) {
	root, files, ok, err := trackedFileLister(dir)
	if !ok {
		return CheckItem{OK: true, Detail: "skipped: not inside a git work tree"}, ""
	}
	if err != nil {
		return CheckItem{OK: false, Error: "could not list tracked files: " + err.Error()},
			"tracked_secrets: could not list tracked files under .nightgauge/"
	}

	var findings []SecretFinding
	var skipped []string
	for _, rel := range files {
		fileFindings, note := scanTrackedFile(filepath.Join(root, filepath.FromSlash(rel)), rel)
		findings = append(findings, fileFindings...)
		if note != "" {
			skipped = append(skipped, rel+" ("+note+")")
		}
	}

	skipNote := ""
	if len(skipped) > 0 {
		skipNote = fmt.Sprintf("; skipped %d file(s): %s", len(skipped), strings.Join(capList(skipped), ", "))
	}
	if len(findings) == 0 {
		return CheckItem{
			OK:     true,
			Detail: fmt.Sprintf("no tracked credentials (%d tracked file(s) under .nightgauge/ scanned%s)", len(files)-len(skipped), skipNote),
		}, ""
	}

	locations := make([]string, 0, len(findings))
	for _, f := range findings {
		locations = append(locations, fmt.Sprintf("%s:%d %s %s", f.Path, f.Line, f.Pattern, f.Redacted))
	}
	msg := fmt.Sprintf("%d tracked credential(s) under .nightgauge/: %s%s. "+
		"Treat each as leaked: (1) rotate it at its issuer now; (2) remove it from the "+
		"repository's history (a history rewrite, coordinated with every clone). "+
		"Deleting the line alone leaves it in history; doctor changes nothing",
		len(findings), strings.Join(capList(locations), ", "), skipNote)
	return CheckItem{OK: false, Error: msg, Findings: findings},
		fmt.Sprintf("tracked_secrets: %d credential(s) committed under .nightgauge/ — rotate them and remove them from history", len(findings))
}

// scanTrackedFile returns the findings in one tracked file, or a note saying
// why it was not scanned. A tracked file deleted from the work tree, or one
// that is not a regular file (a symlink could point outside the repository),
// is passed over without a note.
func scanTrackedFile(abs, rel string) ([]SecretFinding, string) {
	info, err := os.Lstat(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ""
		}
		return nil, "unreadable"
	}
	if !info.Mode().IsRegular() {
		return nil, ""
	}
	if info.Size() > maxScannedFileBytes {
		return nil, "over 1 MiB"
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, "unreadable"
	}
	sniff := data
	if len(sniff) > binarySniffBytes {
		sniff = sniff[:binarySniffBytes]
	}
	if bytes.IndexByte(sniff, 0) >= 0 {
		return nil, "binary"
	}

	var out []SecretFinding
	for i, line := range strings.Split(string(data), "\n") {
		for _, p := range credentialPatterns {
			for range p.re.FindAllStringIndex(line, -1) {
				out = append(out, SecretFinding{
					Path:     rel,
					Line:     i + 1,
					Pattern:  p.name,
					Redacted: p.prefix + "…",
				})
			}
		}
	}
	return out, ""
}

// capList bounds a list printed in a message; the count carries the magnitude.
func capList(items []string) []string {
	if len(items) <= maxLeaksReported {
		return items
	}
	return append(append([]string(nil), items[:maxLeaksReported]...),
		fmt.Sprintf("and %d more", len(items)-maxLeaksReported))
}

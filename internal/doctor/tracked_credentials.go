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
	"strconv"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/credshape"
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
// the pattern's prefix followed by "…", and nothing else of the match. The
// shapes come from internal/credshape, the list the config loader also reads.

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

// trackedScan is what one pass over the tracked files under .nightgauge/ found.
type trackedScan struct {
	inRepo   bool // false: dir is not inside a git work tree
	err      error
	scanned  int
	skipped  []string
	findings []SecretFinding
}

// scanTrackedCredentials scans the tracked files under .nightgauge/ for
// credential shapes. Each SecretFinding carries only the pattern's redacted
// prefix, never the matched value.
func scanTrackedCredentials(dir string) trackedScan {
	root, files, ok, err := trackedFileLister(dir)
	if !ok {
		return trackedScan{}
	}
	if err != nil {
		return trackedScan{inRepo: true, err: err}
	}

	// The real root, so a tracked path that resolves outside it (through a
	// symlinked directory as well as a symlinked file) is never opened.
	realRoot, rerr := filepath.EvalSymlinks(root)
	if rerr != nil {
		realRoot = root
	}

	scan := trackedScan{inRepo: true}
	for _, rel := range files {
		fileFindings, note := scanTrackedFile(realRoot, filepath.Join(root, filepath.FromSlash(rel)), rel)
		scan.findings = append(scan.findings, fileFindings...)
		if note != "" {
			scan.skipped = append(scan.skipped, rel+" ("+note+")")
		}
	}
	scan.scanned = len(files) - len(scan.skipped)
	return scan
}

// trackedCredentialFindings reports one blocker per credential committed under
// .nightgauge/. Evidence carries the path, line, pattern name and redacted
// prefix only. The remedy is manual: doctor never rotates a key or rewrites
// history.
func trackedCredentialFindings(dir string) ([]Finding, string) {
	const check, code = trackedCredentialsCheck, "NGD024"
	scan := scanTrackedCredentials(dir)
	if !scan.inRepo {
		return nil, "skipped: not inside a git work tree"
	}
	if scan.err != nil {
		return []Finding{unverifiableFinding(check, code, SeverityWarning, "tracked_secrets",
			"could not list tracked files under .nightgauge/: "+scan.err.Error())}, "could not list tracked files"
	}
	skipNote := ""
	if len(scan.skipped) > 0 {
		skipNote = fmt.Sprintf("; skipped %d file(s): %s", len(scan.skipped), strings.Join(capList(scan.skipped), ", "))
	}
	if len(scan.findings) == 0 {
		return nil, fmt.Sprintf("no tracked credentials (%d tracked file(s) under .nightgauge/ scanned%s)", scan.scanned, skipNote)
	}
	out := make([]Finding, 0, len(scan.findings))
	for _, f := range scan.findings {
		out = append(out, newFinding(check, code, SeverityBlocker,
			fmt.Sprintf("tracked_secrets: %s credential committed at %s:%d", f.Pattern, f.Path, f.Line),
			"a credential is committed under .nightgauge/. Treat it as leaked: rotate it at its issuer "+
				"and remove it from the repository's history. Deleting the line alone leaves it in history; doctor changes nothing",
			map[string]string{"path": f.Path, "line": strconv.Itoa(f.Line), "pattern": f.Pattern, "redacted": f.Redacted},
			[]string{f.Path, f.Pattern, f.Redacted},
			manualRemedy("rotate", "Rotate the credential, then remove it from the repository and its history", check,
				"Rotate the "+f.Pattern+" credential at its issuer now; assume it is compromised",
				"Stop tracking the file: `git rm --cached "+f.Path+"` (or delete the line), and commit",
				"Remove it from history with a coordinated rewrite (for example `git filter-repo`), "+
					"then have every clone re-fetch; doctor never rewrites history")))
	}
	return out, fmt.Sprintf("%d tracked credential(s) under .nightgauge/%s", len(scan.findings), skipNote)
}

// scanTrackedFile returns the findings in one tracked file, or a note saying
// why it was not scanned. A tracked file deleted from the work tree, or one
// that is not a regular file (a symlink could point outside the repository),
// is passed over without a note.
func scanTrackedFile(realRoot, abs, rel string) ([]SecretFinding, string) {
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
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, "unreadable"
	}
	if r, err := filepath.Rel(realRoot, real); err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return nil, "resolves outside the repository"
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
		for _, p := range credshape.Patterns() {
			for n := p.Count(line); n > 0; n-- {
				out = append(out, SecretFinding{
					Path:     rel,
					Line:     i + 1,
					Pattern:  p.Name,
					Redacted: p.Prefix + "…",
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

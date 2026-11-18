// Diff secret-scan verb (`scan diff-secrets`). A faithful port of pr-create
// Phase 2.5 Step 2.5.3 (the grep loop over the branch's changed files) into a
// deterministic scanner, so the orchestrator can re-run the same check after a
// fix without rendering the skill (#2702).
//
// Semantics preserved from the bash loop:
//   - file selection is Step 2.5.1: `git diff --name-only <base>...HEAD`,
//     minus .nightgauge/, node_modules/ and dist/;
//   - every pattern is case-insensitive (`grep -iE`);
//   - the exclude filter ran as `grep -viE` over grep's output line
//     `path:lineno:content`, so a hit is dropped when the path OR the content
//     matches it.
//
// One deliberate divergence: the loop added 1 per pattern with any hit; the
// verb's totals count matching lines per pattern. A changed file that exists
// but cannot be read fails the run instead of being skipped (no fail-open).
//
// It differs from `scan secrets` (the audit-rubric counter): changed files
// only, a critical/warning severity split, and a gating exit code.
package scan

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
)

// DiffSecretsCriticalPatterns are the critical patterns of Step 2.5.3,
// verbatim. Each is compiled with a (?i) prefix.
var DiffSecretsCriticalPatterns = []string{
	`-----BEGIN (RSA |DSA |EC |OPENSSH )?PRIVATE KEY`,
	`aws_secret_access_key\s*[=:]\s*['"][A-Za-z0-9/+=]{20,}['"]`,
	`aws_access_key_id\s*[=:]\s*['"]AKIA[0-9A-Z]{16}['"]`,
	`(jdbc|postgresql|mysql|mongodb):\/\/[^:]+:[^@]+@`,
}

// DiffSecretsWarningPatterns are the warning patterns of Step 2.5.3, verbatim.
var DiffSecretsWarningPatterns = []string{
	`(api[_-]?key|apikey)\s*[=:]\s*['"][^'"]{16,}['"]`,
	`secret\s*[=:]\s*['"][^'"]{10,}['"]`,
	`password\s*[=:]\s*['"][^'"]{6,}['"]`,
	`token\s*[=:]\s*['"][^'"]{20,}['"]`,
	`authorization\s*[=:]\s*['"]Bearer [^'"]{20,}['"]`,
}

// DiffSecretsExcludePattern is the false-positive filter of Step 2.5.3.
const DiffSecretsExcludePattern = `example|placeholder|YOUR_|xxx|test|mock|REDACTED|\*\*\*|TODO|fake|dummy|sample|changeme|<.*>`

const (
	diffSecretsMaxSampleLines = 5
	diffSecretsBinarySniff    = 8 * 1024
)

type compiledDiffPattern struct {
	severity string
	source   string
	re       *regexp.Regexp
}

var (
	diffSecretsPatterns = compileDiffPatterns()
	diffSecretsExclude  = regexp.MustCompile(`(?i)` + DiffSecretsExcludePattern)
)

func compileDiffPatterns() []compiledDiffPattern {
	var out []compiledDiffPattern
	for _, p := range DiffSecretsCriticalPatterns {
		out = append(out, compiledDiffPattern{"critical", p, regexp.MustCompile(`(?i)` + p)})
	}
	for _, p := range DiffSecretsWarningPatterns {
		out = append(out, compiledDiffPattern{"warning", p, regexp.MustCompile(`(?i)` + p)})
	}
	return out
}

// DiffSecretsOptions controls one `scan diff-secrets` run.
type DiffSecretsOptions struct {
	Workdir string // repository to scan; empty means the CWD
	Base    string // base ref; the scanned set is <Base>...HEAD
}

// DiffSecretsFinding is one pattern's hits in one file. Lines holds at most
// five sample line numbers; matched content is never recorded.
type DiffSecretsFinding struct {
	Severity string `json:"severity"`
	Pattern  string `json:"pattern"`
	Count    int    `json:"count"`
	Lines    []int  `json:"lines"`
}

// DiffSecretsFile is the per-file result. Every scanned file is listed.
type DiffSecretsFile struct {
	Path     string               `json:"path"`
	Critical int                  `json:"critical"`
	Warnings int                  `json:"warnings"`
	Findings []DiffSecretsFinding `json:"findings"`
}

// DiffSecretsResult is the stable JSON schema (v1) of `scan diff-secrets`.
// critical/warnings count matching lines, once per pattern per line.
type DiffSecretsResult struct {
	V            int               `json:"v"`
	Base         string            `json:"base"`
	HeadSHA      string            `json:"head_sha"`
	FilesScanned int               `json:"files_scanned"`
	Critical     int               `json:"critical"`
	Warnings     int               `json:"warnings"`
	Files        []DiffSecretsFile `json:"files"`
}

// RunDiffSecretsScan scans the files changed in <Base>...HEAD.
func RunDiffSecretsScan(ctx context.Context, opts DiffSecretsOptions) (*DiffSecretsResult, error) {
	if strings.TrimSpace(opts.Base) == "" {
		return nil, fmt.Errorf("scan diff-secrets: --base is required")
	}
	if strings.HasPrefix(opts.Base, "-") {
		return nil, fmt.Errorf("scan diff-secrets: invalid base ref %q", opts.Base)
	}
	workdir := opts.Workdir
	if workdir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("scan diff-secrets: resolve workdir: %w", err)
		}
		workdir = wd
	}
	top, err := runDiffGit(ctx, workdir, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("scan diff-secrets: not a git repository: %w", err)
	}
	top = strings.TrimSpace(top)
	if _, err := runDiffGit(ctx, top, "rev-parse", "--verify", "--quiet", opts.Base+"^{commit}"); err != nil {
		return nil, fmt.Errorf("scan diff-secrets: base ref %q does not resolve to a commit", opts.Base)
	}
	head, err := runDiffGit(ctx, top, "rev-parse", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("scan diff-secrets: resolve HEAD: %w", err)
	}
	names, err := runDiffGit(ctx, top, "diff", "--name-only", "-z", opts.Base+"...HEAD", "--")
	if err != nil {
		return nil, fmt.Errorf("scan diff-secrets: git diff: %w", err)
	}

	var paths []string
	for _, p := range strings.Split(names, "\x00") {
		if p == "" || strings.HasPrefix(p, ".nightgauge/") ||
			strings.Contains(p, "node_modules/") || strings.HasPrefix(p, "dist/") {
			continue
		}
		paths = append(paths, p)
	}
	sort.Strings(paths)

	headSHA := strings.TrimSpace(head)
	res := &DiffSecretsResult{V: 1, Base: opts.Base, HeadSHA: headSHA, Files: []DiffSecretsFile{}}
	if len(paths) == 0 {
		return res, nil
	}
	// Contents come from the HEAD commit, never the working tree: the result
	// is a function of head_sha alone, so an uncommitted edit cannot clear a
	// secret the pushed branch still carries.
	blobs, err := diffHeadBlobs(ctx, top, headSHA, paths)
	if err != nil {
		return nil, fmt.Errorf("scan diff-secrets: git ls-tree: %w", err)
	}
	var scanned []string
	var shas []string
	for _, p := range paths {
		if sha, ok := blobs[p]; ok { // absent: deleted at HEAD, symlink or submodule
			scanned = append(scanned, p)
			shas = append(shas, sha)
		}
	}
	contents, err := diffReadBlobs(ctx, top, shas)
	if err != nil {
		return nil, fmt.Errorf("scan diff-secrets: git cat-file: %w", err)
	}
	for i, p := range scanned {
		data := contents[i]
		f, ok, err := scanDiffContent(data, p)
		if err != nil {
			return nil, fmt.Errorf("scan diff-secrets: %s: %w", p, err)
		}
		if !ok {
			continue
		}
		res.Files = append(res.Files, f)
		res.FilesScanned++
		res.Critical += f.Critical
		res.Warnings += f.Warnings
	}
	return res, nil
}

// diffHeadBlobs maps each path that is a regular file in the commit at sha to
// its blob id. Paths absent from the commit (deleted) or of another kind
// (symlink, submodule) are left out, which grep skipped silently.
func diffHeadBlobs(ctx context.Context, top, sha string, paths []string) (map[string]string, error) {
	// One full listing filtered in memory: a pathspec per changed file would
	// put the whole diff on argv and fail with E2BIG on a broad sweep.
	out, err := runDiffGit(ctx, top, "ls-tree", "-r", "-z", sha)
	if err != nil {
		return nil, err
	}
	want := make(map[string]bool, len(paths))
	for _, p := range paths {
		want[p] = true
	}
	blobs := make(map[string]string, len(paths))
	for _, rec := range strings.Split(out, "\x00") {
		meta, path, found := strings.Cut(rec, "\t")
		if !found || !want[path] {
			continue
		}
		fields := strings.Fields(meta) // <mode> <type> <object>
		if len(fields) != 3 || fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755") {
			continue
		}
		blobs[path] = fields[2]
	}
	return blobs, nil
}

// diffReadBlobs reads the blobs in order through one `git cat-file --batch`,
// so the cost does not grow by a process per changed file.
func diffReadBlobs(ctx context.Context, top string, shas []string) ([][]byte, error) {
	if len(shas) == 0 {
		return nil, nil
	}
	cmd := exec.CommandContext(ctx, "git", "cat-file", "--batch")
	cmd.Dir = top
	cmd.Stdin = strings.NewReader(strings.Join(shas, "\n") + "\n")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	r := bufio.NewReader(bytes.NewReader(out))
	blobs := make([][]byte, 0, len(shas))
	for _, sha := range shas {
		header, err := r.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("blob %s: read header: %w", sha, err)
		}
		var gotSHA, kind string
		var size int
		if n, _ := fmt.Sscanf(header, "%s %s %d", &gotSHA, &kind, &size); n != 3 || gotSHA != sha || kind != "blob" {
			return nil, fmt.Errorf("blob %s: unexpected header %q", sha, strings.TrimSpace(header))
		}
		data := make([]byte, size+1) // contents plus the trailing newline
		if _, err := io.ReadFull(r, data); err != nil {
			return nil, fmt.Errorf("blob %s: read contents: %w", sha, err)
		}
		blobs = append(blobs, data[:size])
	}
	return blobs, nil
}

// scanDiffContent scans one file's contents. It reports ok=false for
// binaries, which grep skipped silently.
func scanDiffContent(data []byte, rel string) (DiffSecretsFile, bool, error) {
	sniff := data
	if len(sniff) > diffSecretsBinarySniff {
		sniff = sniff[:diffSecretsBinarySniff]
	}
	if bytes.IndexByte(sniff, 0) >= 0 {
		return DiffSecretsFile{}, false, nil
	}

	findings := make([]DiffSecretsFinding, len(diffSecretsPatterns))
	for i, p := range diffSecretsPatterns {
		findings[i] = DiffSecretsFinding{Severity: p.severity, Pattern: p.source, Lines: []int{}}
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), len(data)+1)
	n := 0
	for sc.Scan() {
		n++
		line := sc.Text()
		excluded := false
		checked := false
		for i, p := range diffSecretsPatterns {
			if !p.re.MatchString(line) {
				continue
			}
			if !checked {
				excluded = diffSecretsExclude.MatchString(fmt.Sprintf("%s:%d:%s", rel, n, line))
				checked = true
			}
			if excluded {
				break
			}
			findings[i].Count++
			if len(findings[i].Lines) < diffSecretsMaxSampleLines {
				findings[i].Lines = append(findings[i].Lines, n)
			}
		}
	}

	if err := sc.Err(); err != nil {
		return DiffSecretsFile{}, false, err
	}

	out := DiffSecretsFile{Path: rel, Findings: []DiffSecretsFinding{}}
	for _, f := range findings {
		if f.Count == 0 {
			continue
		}
		if f.Severity == "critical" {
			out.Critical += f.Count
		} else {
			out.Warnings += f.Count
		}
		out.Findings = append(out.Findings, f)
	}
	return out, true, nil
}

func runDiffGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

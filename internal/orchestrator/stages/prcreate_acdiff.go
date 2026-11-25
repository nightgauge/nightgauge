package stages

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/ci"
	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/preflight"
	"github.com/nightgauge/nightgauge/internal/reclaim"
	"gopkg.in/yaml.v3"
)

// Deterministic diff verification of acceptance criteria (#2665). Many
// criteria are answered by the branch diff alone: a bullet under a heading,
// a file that must not change, a test that must exist and pass. Checking
// those here means the closing keyword no longer depends on the shape of the
// verdicts an LLM stage wrote. A criterion no rule can read stays
// unanswered (ok=false); one that needs a runtime or operator step resolves
// not_verifiable and names the step.

// ACCheck is one source's answer for a criterion.
type ACCheck struct {
	Status   string
	Evidence string
	// Step names the runtime or operator step a not_verifiable criterion
	// needs, e.g. "a physical device".
	Step string
	// Uncommitted lists the uncommitted or untracked paths the evidence
	// cites (#2669).
	Uncommitted []string
}

// ACDiffChecker answers one criterion; ok=false means it cannot. prior holds
// the criteria before it, in order, so a clause naming no file can inherit
// the file the nearest earlier criterion named (#2669).
type ACDiffChecker func(text string, prior []string) (ACCheck, bool)

// ACTestState is the stage's test run as pr-create can see it. A test named in
// PassedTests or FailedTests answers for itself, from the gate log's own
// `--- PASS` / `--- FAIL` line (#2941); any other named test "passed" when the
// run did.
type ACTestState struct {
	Passed bool
	Source string // e.g. "feature-validate unit tests"

	PassedTests map[string]bool
	FailedTests map[string]bool
	LogSource   string // e.g. "the feature-validate gate log `gate-42.log`"
}

// named reports a test's own result: (passed, true) when the gate log has a
// line for it, (_, false) when it does not.
func (t ACTestState) named(name string) (bool, bool) {
	if t.FailedTests[name] {
		return false, true
	}
	if t.PassedTests[name] {
		return true, true
	}
	return false, false
}

func (t ACTestState) logSource() string {
	if t.LogSource != "" {
		return t.LogSource
	}
	return "the gate log"
}

type acDiffLine struct {
	N    int
	Text string
}

// ACFileDiff is one file's changes against the merge base.
type ACFileDiff struct {
	Path    string
	OldPath string // the base path of a renamed file
	Status  string // added | modified | deleted | renamed
	Added   []acDiffLine
	Deleted int
	// Removed is the text of each removed line, so a criterion naming a
	// symbol the change renames or deletes still finds its place (#2941).
	Removed []string
}

// ACDiff is the branch's changes against its merge base, committed or not.
type ACDiff struct {
	Files map[string]*ACFileDiff
	// Workdir reads branch content and greps for test definitions. Empty
	// means only the parsed diff is available.
	Workdir string
	// Uncommitted holds the paths that differ from HEAD or are untracked:
	// work the commit owner has not committed yet.
	Uncommitted map[string]bool
	// Repos maps a workspace repository slug (`owner/repo`) to its checkout,
	// so a criterion comparing a file with another repository's file at a
	// revision can be decided locally (#3042). Empty means none is known.
	Repos map[string]string
	// BaseRev is the revision the diff is taken against: the merge base, or
	// a range diff's first revision. Unlike FromRev it does not mark a range
	// diff (#3501).
	BaseRev string
	// FromRev and ToRev are set by CollectACDiffRange: the diff is
	// FromRev..ToRev, and content is read at ToRev, never from the working
	// tree (#3423).
	FromRev string
	ToRev   string

	content map[string]acContent
	yaml    map[string]*yaml.Node
	gofiles map[string]*acGoFile
	// gofields memoises goFieldValue by path and span (#3424).
	gofields map[string]acGoFieldMatch
	// base memoises the span-independent base listings acTestsPreserved
	// reads, so each scoped span does not list the tree again (#3504).
	base *acBaseListing
}

// acBaseListing is the base revision's file list and the paths git says the
// diff touches; ok=false records that either listing failed.
type acBaseListing struct {
	tree    []string
	touched map[string]bool
	ok      bool
}

// acContent is one file's working-tree lines; ok=false means the file could
// not be read, which is not the same as an empty file (#2669).
type acContent struct {
	lines []string
	ok    bool
}

// acResult is one rule's answer for a clause. paths are the diffed files the
// verdict rests on, so provenance does not depend on rendered evidence.
type acResult struct {
	status   string
	evidence string
	paths    []string
	ok       bool
}

func acAnswer(status, evidence string, paths ...string) acResult {
	return acResult{status: status, evidence: evidence, paths: paths, ok: true}
}

// acDiffTimeout bounds every git call the verifier makes.
const acDiffTimeout = 20 * time.Second

// acReadMaxBytes bounds every file the verifier reads: a larger file is too
// big to be a hand-written deliverable (generated output, fixtures), and is
// skipped by size before it is read.
const acReadMaxBytes = 1 << 20

// acReadBounded reads a regular file, or a symlink to one, no larger than
// acReadMaxBytes; ok=false for anything else.
func acReadBounded(path string) ([]byte, bool) {
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > acReadMaxBytes {
		return nil, false
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) > acReadMaxBytes {
		return nil, false
	}
	return data, true
}

// CollectACDiff diffs the working tree against the merge base of base and
// HEAD, so work the commit owner has not committed yet is included, and adds
// untracked deliverable files as wholly added. origin/<base> is tried before
// the local ref. Pipeline exhaust is excluded, as the commit owner excludes it.
func CollectACDiff(ctx context.Context, workdir, base string) (*ACDiff, error) {
	if base == "" {
		base = defaultBaseBranch
	}
	ctx, cancel := context.WithTimeout(ctx, acDiffTimeout)
	defer cancel()
	git := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.quotePath=false"}, args...)...)
		cmd.Dir = workdir
		out, err := cmd.Output()
		return string(out), err
	}
	var mergeBase string
	var lastErr error
	for _, ref := range []string{"origin/" + base, base} {
		out, err := git("merge-base", ref, "HEAD")
		if err != nil {
			lastErr = err
			continue
		}
		mergeBase = strings.TrimSpace(out)
		break
	}
	if mergeBase == "" {
		return nil, fmt.Errorf("resolve merge base of %q: %w", base, lastErr)
	}
	out, err := git("diff", "--unified=0", "--no-color", "--no-ext-diff",
		"--src-prefix=a/", "--dst-prefix=b/", "-M", mergeBase)
	if err != nil {
		return nil, fmt.Errorf("git diff %s: %w", mergeBase, err)
	}
	d := &ACDiff{Files: parseUnifiedDiff(out), Workdir: workdir, Uncommitted: map[string]bool{}, BaseRev: mergeBase}
	dirty, err := git("diff", "--name-only", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("git diff --name-only HEAD: %w", err)
	}
	for _, p := range strings.Split(strings.TrimSpace(dirty), "\n") {
		if p != "" {
			d.Uncommitted[acUnquotePath(p)] = true
		}
	}

	untracked, err := git("ls-files", "--others", "--exclude-standard")
	if err != nil {
		return nil, fmt.Errorf("list untracked files: %w", err)
	}
	var porcelain strings.Builder
	for _, p := range strings.Split(strings.TrimSpace(untracked), "\n") {
		if p != "" {
			porcelain.WriteString("?? " + p + "\n")
		}
	}
	for _, p := range reclaim.ClassifyStatus(porcelain.String()).Blocking {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		data, ok := acReadBounded(filepath.Join(workdir, p))
		if !ok || bytes.IndexByte(data, 0) >= 0 {
			continue
		}
		f := &ACFileDiff{Path: p, Status: "added"}
		data = bytes.TrimSuffix(data, []byte("\n"))
		for n := 1; ; n++ {
			j := bytes.IndexByte(data, '\n')
			if j < 0 {
				f.Added = append(f.Added, acDiffLine{N: n, Text: string(data)})
				break
			}
			f.Added = append(f.Added, acDiffLine{N: n, Text: string(data[:j])})
			data = data[j+1:]
		}
		d.Files[p] = f
		d.Uncommitted[p] = true
	}
	// The workspace is the main checkout's, not this worktree's siblings.
	if common, err := git("rev-parse", "--path-format=absolute", "--git-common-dir"); err == nil {
		if root := filepath.Dir(strings.TrimSpace(common)); root != "" {
			d.Repos = config.WorkspaceRepoPaths(root)
		}
	}
	return d, nil
}

// blob reads path at rev in the repository checked out at repo, bounded by
// acDiffTimeout and acReadMaxBytes; ok=false when it cannot be read.
func acBlob(repo, rev, path string) ([]byte, bool) {
	if repo == "" || rev == "" || path == "" || strings.HasPrefix(rev, "-") {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), acDiffTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", repo, "cat-file", "blob", rev+":"+path)
	out, err := cmd.Output()
	if err != nil || len(out) > acReadMaxBytes {
		return nil, false
	}
	return out, true
}

// acBlobs reads every path at rev in one `git cat-file --batch` streamed
// within acDiffTimeout; a blob over acReadMaxBytes is discarded unread. A path
// that is missing, not a blob, too large or holds a newline is absent from the
// result; a failed subprocess or output that does not parse leaves every path
// absent (#3504).
func acBlobs(repo, rev string, paths []string) map[string][]byte {
	if repo == "" || rev == "" || strings.HasPrefix(rev, "-") {
		return map[string][]byte{}
	}
	var req []string
	var in strings.Builder
	for _, p := range paths {
		if p == "" || strings.ContainsAny(p, "\n\r") {
			continue
		}
		req = append(req, p)
		in.WriteString(rev + ":" + p + "\n")
	}
	if len(req) == 0 {
		return map[string][]byte{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), acDiffTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", repo, "cat-file", "--batch")
	cmd.Stdin = strings.NewReader(in.String())
	stdout, err := cmd.StdoutPipe()
	if err != nil || cmd.Start() != nil {
		return map[string][]byte{}
	}
	got, ok := acParseBatch(bufio.NewReader(stdout), req)
	if _, err := io.Copy(io.Discard, stdout); err != nil {
		ok = false
	}
	if cmd.Wait() != nil || !ok {
		return map[string][]byte{}
	}
	return got
}

// acParseBatch reads one `cat-file --batch` answer per requested path from r,
// keeping each blob within acReadMaxBytes; ok=false when the output is not
// one well-formed answer per path.
func acParseBatch(r *bufio.Reader, req []string) (map[string][]byte, bool) {
	got := map[string][]byte{}
	for _, p := range req {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, false
		}
		header := strings.Fields(line)
		if len(header) == 2 && header[1] == "missing" {
			continue
		}
		if len(header) != 3 {
			return nil, false
		}
		size, err := strconv.ParseInt(header[2], 10, 64)
		if err != nil || size < 0 {
			return nil, false
		}
		if header[1] == "blob" && size <= acReadMaxBytes {
			body := make([]byte, size+1)
			if _, err := io.ReadFull(r, body); err != nil || body[size] != '\n' {
				return nil, false
			}
			got[p] = body[:size]
			continue
		}
		if _, err := io.CopyN(io.Discard, r, size); err != nil {
			return nil, false
		}
		if b, err := r.ReadByte(); err != nil || b != '\n' {
			return nil, false
		}
	}
	if _, err := r.ReadByte(); err != io.EOF {
		return nil, false
	}
	return got, true
}

// baseListing lists the base tree and the paths the diff touches once per
// ACDiff, memoising a failure too; its revisions never change (#3504).
func (d *ACDiff) baseListing() ([]string, map[string]bool, bool) {
	if d.base != nil {
		return d.base.tree, d.base.touched, d.base.ok
	}
	d.base = &acBaseListing{}
	tree, err := acGitList(d.Workdir, nil, "ls-tree", "-r", "--full-tree", "--name-only", "-z", d.BaseRev)
	if err != nil {
		return nil, nil, false
	}
	// The diff's headers lose a path git quotes, so git lists what was touched.
	args := []string{"diff", "--name-only", "--no-renames", "--no-relative", "-z", d.BaseRev}
	if d.ToRev != "" {
		args = append(args, d.ToRev)
	}
	listed, err := acGitList(d.Workdir, nil, append(args, "--")...)
	if err != nil {
		return nil, nil, false
	}
	touched := map[string]bool{}
	for _, p := range listed {
		touched[p] = true
	}
	d.base = &acBaseListing{tree: tree, touched: touched, ok: true}
	return tree, touched, true
}

// headContent is path's content on the branch: the working tree when the
// path is uncommitted, else its blob at HEAD.
func (d *ACDiff) headContent(path string) ([]byte, bool) {
	if d.Workdir == "" {
		return nil, false
	}
	if d.ToRev != "" {
		return acBlob(d.Workdir, d.ToRev, path)
	}
	if d.Uncommitted[path] {
		return acReadBounded(filepath.Join(d.Workdir, path))
	}
	return acBlob(d.Workdir, "HEAD", path)
}

// acUnquotePath undoes the C quoting git gives a path that holds a quote, a
// backslash or a control byte, which core.quotePath=false does not turn off.
func acUnquotePath(p string) string {
	if !strings.HasPrefix(p, `"`) {
		return p
	}
	// strconv.Unquote turns a raw byte that is not UTF-8 into U+FFFD, which
	// would give two files one path, so each high byte is escaped first.
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		if p[i] >= 0x80 {
			fmt.Fprintf(&b, `\%03o`, p[i])
		} else {
			b.WriteByte(p[i])
		}
	}
	if u, err := strconv.Unquote(b.String()); err == nil {
		return u
	}
	return p
}

var acHunkRE = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// parseUnifiedDiff reads `git diff --unified=0` output into per-file changes.
// It finds paths by the b/ prefix, so callers pass --src-prefix and
// --dst-prefix: diff.noprefix and diff.mnemonicPrefix change the default.
func parseUnifiedDiff(out string) map[string]*ACFileDiff {
	var sections []*ACFileDiff
	var cur *ACFileDiff
	inHunk := false
	next := 0
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			cur = &ACFileDiff{Status: "modified"}
			sections = append(sections, cur)
			inHunk = false
			cur.Path = acHeaderPath(strings.TrimPrefix(line, "diff --git "))
		case cur == nil:
		case !inHunk && strings.HasPrefix(line, "new file mode"):
			cur.Status = "added"
		case !inHunk && strings.HasPrefix(line, "deleted file mode"):
			cur.Status = "deleted"
		case !inHunk && strings.HasPrefix(line, "rename from "):
			cur.OldPath = acUnquotePath(strings.TrimPrefix(line, "rename from "))
		case !inHunk && strings.HasPrefix(line, "rename to "):
			cur.Status = "renamed"
			cur.Path = acUnquotePath(strings.TrimPrefix(line, "rename to "))
		case !inHunk && strings.HasPrefix(line, "+++ "):
			// git ends a header path that holds a space with a tab.
			if p := acUnquotePath(strings.TrimSuffix(strings.TrimPrefix(line, "+++ "), "\t")); strings.HasPrefix(p, "b/") {
				cur.Path = p[2:]
			}
		case strings.HasPrefix(line, "@@"):
			if m := acHunkRE.FindStringSubmatch(line); m != nil {
				next, _ = strconv.Atoi(m[1])
				inHunk = true
			}
		case !inHunk:
		case strings.HasPrefix(line, "+"):
			cur.Added = append(cur.Added, acDiffLine{N: next, Text: line[1:]})
			next++
		case strings.HasPrefix(line, "-"):
			cur.Deleted++
			cur.Removed = append(cur.Removed, line[1:])
		case strings.HasPrefix(line, " "):
			next++
		}
	}
	// Keyed only once every path is final, so no section displaces another.
	files := map[string]*ACFileDiff{}
	for _, f := range sections {
		if f.Path == "" {
			continue
		}
		// A type change is two sections, a delete and an add, of one path.
		if prev := files[f.Path]; prev != nil {
			f.Deleted += prev.Deleted
			f.Removed = append(prev.Removed, f.Removed...)
			f.Added = append(prev.Added, f.Added...)
		}
		files[f.Path] = f
	}
	return files
}

// acHeaderPath reads the destination from what follows "diff --git ". A name
// may itself hold " b/", so an unrenamed one is read as the half it repeats;
// a rename's is replaced by its "rename to" line.
func acHeaderPath(names string) string {
	if n := (len(names) - len("a/ b/")) / 2; n > 0 {
		if p := names[len(names)-n:]; names == "a/"+p+" b/"+p {
			return p
		}
	}
	sep := " b/"
	if strings.HasSuffix(names, `"`) {
		sep = ` "b/` // only a quoted path ends in a quote
	}
	if i := strings.LastIndex(names, sep); i >= 0 {
		return strings.TrimPrefix(acUnquotePath(names[i+1:]), "b/")
	}
	return ""
}

// Checker returns the verifier bound to this diff, or nil when the diff is
// empty: with nothing changed there is nothing a diff can answer.
func (d *ACDiff) Checker(tests ACTestState) ACDiffChecker {
	if d == nil || len(d.Files) == 0 {
		return nil
	}
	return func(text string, prior []string) (ACCheck, bool) { return VerifyACAgainstDiff(text, prior, d, tests) }
}

// lines returns path's working-tree lines; ok=false when there is no Workdir
// or the file could not be read (missing, too large, not a regular file).
func (d *ACDiff) lines(path string) ([]string, bool) {
	if d.content == nil {
		d.content = map[string]acContent{}
	}
	if c, ok := d.content[path]; ok {
		return c.lines, c.ok
	}
	var c acContent
	if d.Workdir != "" && d.ToRev != "" {
		if data, ok := acBlob(d.Workdir, d.ToRev, path); ok {
			c = acContent{lines: strings.Split(string(data), "\n"), ok: true}
		}
	} else if d.Workdir != "" {
		if data, ok := acReadBounded(filepath.Join(d.Workdir, path)); ok {
			c = acContent{lines: strings.Split(string(data), "\n"), ok: true}
		}
	}
	d.content[path] = c
	return c.lines, c.ok
}

func (d *ACDiff) sortedPaths() []string {
	out := make([]string, 0, len(d.Files))
	for p := range d.Files {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// Criterion text
// ---------------------------------------------------------------------------

type acHeading struct {
	Level    int
	Text     string
	Existing bool // qualified by "existing": only its current lines must hold
}

func (h acHeading) String() string { return strings.Repeat("#", h.Level) + " " + h.Text }

type acClause struct {
	Raw      string
	Prose    string // Raw with code spans removed
	Paths    []string
	Headings []acHeading
	Tokens   []string // code spans that are not paths or headings
	// Refs are #NNN issue/PR references in the prose. They name context ("the
	// case PR #3225 hit"), not text the diff must add: one the diff adds is
	// cited as evidence, one it lacks leaves the clause to validate (#3233).
	// A ref written in a code span is a Token, checked literally.
	Refs []string
	// Bounds are code-span values the prose names as a bound ("a version
	// above `1.26.0`"): a comparison, not text the diff must add (#2941).
	Bounds []acBound
	// Globs are path globs (`claude-plugins/nightgauge/**`), decided against
	// the diffed paths (#3042).
	Globs []string
	// Digests are 64-hex sha256 spans, decided by hashing the named file.
	Digests []string
	// Revisions are 7–40 hex spans naming a commit, decided by comparing the
	// named file with the named repository's file at that revision.
	Revisions []string
	// AfterAnd reports that the clause was split off at ", and", so what
	// precedes it may share its predicate (#3501).
	AfterAnd bool
	// Joined reports that another clause of the criterion is more than a
	// command's result, so what this one keeps may continue there (#3501).
	Joined bool
}

// acBound is one "<op> value" comparison a criterion states.
type acBound struct {
	Op    string // ">", ">=", "<" or "<="
	Value string
	Word  string // the phrase as written, for evidence
}

var (
	acHeadingTokRE  = regexp.MustCompile(`^(#{1,6})\s+(\S.*)$`)
	acPathLikeRE    = regexp.MustCompile(`^[A-Za-z0-9_.\-/]+$`)
	acPathExtRE     = regexp.MustCompile(`\.[A-Za-z][A-Za-z0-9]{0,5}$`)
	acProsePathRE   = regexp.MustCompile(`(?:^|[\s(])((?:[A-Za-z0-9_.\-]+/)*[A-Za-z0-9_.\-]+\.(?:md|mdx|ts|tsx|js|jsx|mjs|json|sh|ya?ml|toml|go|py|rs|txt))\b`)
	acIssueRefRE    = regexp.MustCompile(`(?:^|[^\w&])(#\d+)\b`)
	acExistingRE    = regexp.MustCompile(`(?i)\bexisting\s*$`)
	acMdHeadingRE   = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*#*\s*$`)
	acClauseSplitRE = regexp.MustCompile(`^(?:,\s+and\s+|;\s+)`)
	// acBoundPhraseRE is a bound phrase ending immediately before a code
	// span, optionally followed by "version" or "v".
	acBoundPhraseRE = regexp.MustCompile(`(?i)(?:^|[^\w])(no (?:more|less|fewer) than|at (?:least|most)|(?:greater|more|higher|newer|later|less|fewer|lower|older|earlier) than|above|over|below|under|exceed(?:s|ing)?|>=|<=|≥|≤|>|<)\s*(?:version\s+)?$`)
	// acBoundValueRE is a value a bound can compare: an integer or a dotted
	// version, optionally v-prefixed.
	acBoundValueRE = regexp.MustCompile(`^v?\d+(?:\.\d+)*$`)
	acVersionRE    = regexp.MustCompile(`(?:^|[^\w.])v?(\d+\.\d+(?:\.\d+)*)(?:[^\w.]|$)`)
	acIntegerRE    = regexp.MustCompile(`(?:^|[^\w.])(\d+)(?:[^\w.]|$)`)
	// acQualifiedSymRE is a package-qualified Go symbol: `skillrender.BundleExcludes`.
	acQualifiedSymRE = regexp.MustCompile(`^([a-z][a-z0-9_]*)\.([A-Z][A-Za-z0-9_]*)$`)
)

// acBoundOps maps a bound phrase to its comparison.
func acBoundOp(phrase string) string {
	switch p := strings.ToLower(phrase); {
	case p == ">=" || p == "≥" || p == "at least" || p == "no less than" || p == "no fewer than":
		return ">="
	case p == "<=" || p == "≤" || p == "at most" || p == "no more than":
		return "<="
	case p == "<" || p == "below" || p == "under" || strings.HasSuffix(p, " than") &&
		(strings.HasPrefix(p, "less") || strings.HasPrefix(p, "fewer") || strings.HasPrefix(p, "lower") ||
			strings.HasPrefix(p, "older") || strings.HasPrefix(p, "earlier")):
		return "<"
	}
	return ">"
}

// acCompareValues compares two dotted values numerically, component by
// component, a missing component counting as 0.
func acCompareValues(a, b string) int {
	pa := strings.Split(strings.TrimPrefix(a, "v"), ".")
	pb := strings.Split(strings.TrimPrefix(b, "v"), ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

func (b acBound) holds(v string) bool {
	c := acCompareValues(v, b.Value)
	switch b.Op {
	case ">=":
		return c >= 0
	case "<":
		return c < 0
	case "<=":
		return c <= 0
	}
	return c > 0
}

// acIsPathRef reports whether a code span opens with a path followed by more
// text — `docs/GIT_WORKFLOW.md § Changelog` — which names a place to consult,
// not text the change must add (#2941).
func acIsPathRef(tok string) bool {
	fields := strings.Fields(tok)
	return len(fields) > 1 && acIsPath(fields[0]) && strings.ContainsAny(fields[0], "/.")
}

func acIsPath(s string) bool {
	if !acPathLikeRE.MatchString(s) || strings.Contains(s, "..") {
		return false
	}
	if strings.Contains(s, "/") {
		return true
	}
	// A bare `.md` is an extension, not a file.
	return !strings.HasPrefix(s, ".") && acPathExtRE.MatchString(s)
}

// acCodeSpans returns the CommonMark code spans in text as
// [start, end, contentStart, contentEnd]. A run of N backticks opens a span
// that only the next run of exactly N backticks closes; an opener with no
// closer is literal text. Content that both starts and ends with a space, and
// is not all spaces, loses one space at each end (CommonMark 6.1).
func acCodeSpans(text string) [][4]int {
	var out [][4]int
	runEnd := func(i int) int {
		for i < len(text) && text[i] == '`' {
			i++
		}
		return i
	}
	for i := 0; i < len(text); {
		if text[i] != '`' {
			i++
			continue
		}
		openEnd := runEnd(i)
		n := openEnd - i
		closeStart, closeEnd := -1, -1
		for j := openEnd; j < len(text); {
			if text[j] != '`' {
				j++
				continue
			}
			k := runEnd(j)
			if k-j == n {
				closeStart, closeEnd = j, k
				break
			}
			j = k
		}
		if closeStart < 0 {
			i = openEnd
			continue
		}
		cs, ce := openEnd, closeStart
		if c := text[cs:ce]; len(c) >= 2 && c[0] == ' ' && c[len(c)-1] == ' ' &&
			strings.Trim(c, " ") != "" {
			cs, ce = cs+1, ce-1
		}
		out = append(out, [4]int{i, closeEnd, cs, ce})
		i = closeEnd
	}
	return out
}

// acStripCodeSpans replaces each code span with a single space.
func acStripCodeSpans(text string) string {
	spans := acCodeSpans(text)
	if len(spans) == 0 {
		return text
	}
	var b strings.Builder
	prev := 0
	for _, sp := range spans {
		b.WriteString(text[prev:sp[0]])
		b.WriteByte(' ')
		prev = sp[1]
	}
	b.WriteString(text[prev:])
	return b.String()
}

// acSpanContent unwraps s while it is exactly one code span, so the content
// of “ `name()` “ is name().
func acSpanContent(s string) string {
	for {
		spans := acCodeSpans(s)
		if len(spans) != 1 || spans[0][0] != 0 || spans[0][1] != len(s) {
			return s
		}
		s = s[spans[0][2]:spans[0][3]]
	}
}

// splitACClauses splits on ", and" and ";" outside code spans, so each
// predicate of a compound criterion is checked on its own.
func splitACClauses(text string) []string {
	var out []string
	spans := acCodeSpans(text)
	start, si := 0, 0
	for i := 0; i < len(text); i++ {
		if si < len(spans) && i == spans[si][0] {
			i = spans[si][1] - 1
			si++
			continue
		}
		if m := acClauseSplitRE.FindString(text[i:]); m != "" {
			out = append(out, text[start:i])
			start = i + len(m)
			i = start - 1
		}
	}
	out = append(out, text[start:])
	return out
}

// parseACClauses parses each clause of text, marking those that follow a
// ", and" the split consumed and those joined to another clause that is more
// than a command's result.
func parseACClauses(text string) []acClause {
	var out []acClause
	pos, afterAnd := 0, false
	for _, raw := range splitACClauses(text) {
		c := parseACClause(raw)
		c.AfterAnd = afterAnd
		out = append(out, c)
		pos += len(raw)
		sep := acClauseSplitRE.FindString(text[pos:])
		afterAnd = strings.HasPrefix(sep, ",")
		pos += len(sep)
	}
	for i := range out {
		for j, o := range out {
			if j != i && strings.TrimSpace(o.Raw) != "" && !acResultClauseRE.MatchString(o.Raw) {
				out[i].Joined = true
			}
		}
	}
	return out
}

// acRevisionCtx reports whether the revision wording sits directly before
// the hex span ("at `abc1234`", "commit `abc1234`", "@`abc1234`").
func acRevisionCtx(raw, h string) bool {
	for rest := raw; ; {
		i := strings.Index(rest, "`"+h+"`")
		if i < 0 {
			return false
		}
		if acRevisionCtxRE.MatchString(rest[:i]) {
			return true
		}
		rest = rest[i+len(h)+2:]
	}
}

func parseACClause(raw string) acClause {
	c := acClause{Raw: raw, Prose: acStripCodeSpans(raw)}
	var hexes []string
	seen := map[string]bool{}
	add := func(dst *[]string, s string) {
		if !seen[s] {
			seen[s] = true
			*dst = append(*dst, s)
		}
	}
	for _, m := range acCodeSpans(raw) {
		tok := strings.TrimSpace(acSpanContent(raw[m[2]:m[3]]))
		switch {
		case tok == "":
		case acHeadingTokRE.MatchString(tok):
			hm := acHeadingTokRE.FindStringSubmatch(tok)
			c.Headings = append(c.Headings, acHeading{
				Level: len(hm[1]), Text: strings.TrimSpace(hm[2]),
				Existing: acExistingRE.MatchString(raw[:m[0]]),
			})
		case acIsGlobSpan(tok):
			add(&c.Globs, tok)
		case acDigestRE.MatchString(tok):
			add(&c.Digests, strings.ToLower(tok))
		case acRevisionRE.MatchString(tok) && strings.ContainsAny(tok, "0123456789") &&
			strings.ContainsAny(strings.ToLower(tok), "abcdef"):
			hexes = append(hexes, tok)
		case acIsPath(tok):
			add(&c.Paths, tok)
		case acIsPathRef(tok):
		default:
			if acBoundValueRE.MatchString(tok) {
				if pm := acBoundPhraseRE.FindStringSubmatch(raw[:m[0]]); pm != nil {
					c.Bounds = append(c.Bounds, acBound{Op: acBoundOp(pm[1]), Value: tok, Word: strings.ToLower(pm[1])})
					continue
				}
			}
			add(&c.Tokens, tok)
		}
	}
	// A hex span names a revision only when the prose says so; otherwise it
	// is text like any other.
	for _, h := range hexes {
		if acRevisionCtx(raw, h) {
			add(&c.Revisions, h)
		} else {
			add(&c.Tokens, h)
		}
	}
	for _, m := range acProsePathRE.FindAllStringSubmatch(c.Prose, -1) {
		add(&c.Paths, m[1])
	}
	for _, m := range acIssueRefRE.FindAllStringSubmatch(c.Prose, -1) {
		add(&c.Refs, m[1])
	}
	return c
}

// ---------------------------------------------------------------------------
// Rules
// ---------------------------------------------------------------------------

// acRuntimeSteps are criteria a diff can never answer, matched on prose
// (code spans removed, so `manual_checklist` is not a manual step).
var acRuntimeSteps = []struct {
	re   *regexp.Regexp
	step string
}{
	{regexp.MustCompile(`(?i)\b(?:physical|real|test) device\b|\bon (?:a|the) device\b|\b(?:simulator|emulator)\b`), "a physical device"},
	{regexp.MustCompile(`(?i)\b(?:in|on|against|to) (?:the )?(?:production|prod|staging)\b|\bproduction (?:environment|traffic|call|data|database)\b`), "a production check"},
	{regexp.MustCompile(`(?i)\bdeploy(?:s|ed|ing|ment)?\b`), "a deploy"},
	{regexp.MustCompile(`(?i)\b(?:publish(?:ed)?|cut|tag(?:ged)?)\s+(?:a |the )?release\b|\brelease\s+(?:is\s+)?(?:published|cut|tagged)\b|\bmarketplace\b`), "a release"},
	{regexp.MustCompile(`(?i)\bmanually\b|\bmanual (?:step|check|review|test(?:ing)?|verification|QA|run|sign[- ]off|approval)\b|\boperator\b|\bby hand\b|\bhuman (?:review(?:er)?|step|check|approval|operator|in the loop)\b|\bsign[- ]off\b`), "a manual or operator step"},
	{regexp.MustCompile(`(?i)\b(?:rotat(?:e|es|ed|ing)|provision(?:s|ed|ing)?|suppl(?:y|ies|ied|ying)|configur(?:e|es|ed|ing)|set(?:s|ting)? up|issu(?:e|es|ed|ing)\s+(?:a|an|the|new)|generat(?:e|es|ed|ing)|obtain(?:s|ed|ing)?)\b[^.]*\b(?:credentials?|API keys?)\b|\b(?:credentials?|API keys?)\b[^.]*\b(?:rotated|provisioned|supplied|configured|issued|generated|obtained)\b|\brotat(?:e|ed|es|ing|ion)\b[^.]*\b(?:credentials?|keys?|tokens?|secrets?|passwords?)\b|\b(?:credentials?|keys?|tokens?|secrets?)\s+rotation\b`), "a credential"},
	{regexp.MustCompile(`(?i)\b(?:live|real) (?:API|endpoint|service|call|GitHub|run)\b|\bagainst (?:the )?(?:live|real)\b`), "a live API call"},
	{regexp.MustCompile(`(?i)\b(?:after|once)\b[^.]*\bmerged?\b|\bpost-merge\b`), "a post-merge observation"},
	{regexp.MustCompile(`(?i)\b(?:observed|monitored)\b|\b(?:next|following|subsequent) (?:pipeline )?runs?\b`), "observing a live run"},
}

var (
	acScopeRE     = regexp.MustCompile(`(?i)\bno (?:other )?files?\b.*\bother than\b|\bno other files?\b|\bonly(?:\s|,|\band\b|\bor\b)*(?:(?:is|are|gets?)\s+)?(?:changed|modified|touched|edited|changes?)\b|\bthe only (?:files?|changes?)\b`)
	acUnchangedRE = regexp.MustCompile(`(?i)\b(?:unchanged|untouched|not (?:modified|changed|touched|edited))\b`)
	acGoTestRE    = regexp.MustCompile(`\bTest[A-Z0-9_]\w*`)
	acPyTestRE    = regexp.MustCompile(`\btest_\w+`)
	acJSTestRE    = regexp.MustCompile(`^(?:it|test|describe)\((["'])(.+)["']\)?$`)
	// acGoRunRE matches a `go test -run` argument, quoted or not: a regex
	// selecting tests, not the name of one test that must exist.
	acGoRunRE     = regexp.MustCompile(`(?:^|\s)-(?:test\.)?run(?:=|\s+)(?:'([^']*)'|"([^"]*)"|([^\s` + "`" + `]+))`)
	acGoTestDefRE = regexp.MustCompile(`^func (Test\w*)\(`)
	acChangelogRE = regexp.MustCompile(`(?i)\bchange ?log\b`)
	// acChangelogFileRE matches a changelog file's base name.
	acChangelogFileRE = regexp.MustCompile(`(?i)^change_?log(\..*)?$`)
)

// acPRTextRE matches prose about the pull request's own description, body or
// title: "the PR description", "the pull request's body", "the PR title"
// (#3529).
var acPRTextRE = regexp.MustCompile(`(?i)\b(?:PR|pull[- ]request)(?:'s)?\s+(?:own\s+)?(?:description|body|title)\b`)

// acBareDescriptionRE matches "the description says" with no owner named,
// which reads as the PR's description only when the clause names no file.
var acBareDescriptionRE = regexp.MustCompile(`(?i)\b(?:the|its)\s+(?:own\s+)?description\s+(?:says|reads|states|contains|includes|ends|names|cites|carries)\b`)

// acIsDescriptionClause reports whether a clause is about the pull request's
// own description, body or title (#3529).
func acIsDescriptionClause(c acClause) bool {
	if acPRTextRE.MatchString(c.Prose) {
		return true
	}
	return len(c.Paths) == 0 && acBareDescriptionRE.MatchString(c.Prose)
}

// VerifyACAgainstDiff answers one criterion from the diff and the stage's
// test run. A runtime or operator criterion resolves not_verifiable with its
// step named, before any diff rule can claim it. Otherwise each clause goes
// to the first rule that reads it (scope, unchanged, test, section,
// token/file), clauses no rule reads are skipped, and the worst result wins.
// ok=false means no clause was diff-checkable. A clause naming no diffed
// file inherits the diffed files of the nearest prior criterion naming any;
// with none, a bare token is not searched across the whole diff (#2669).
func VerifyACAgainstDiff(text string, prior []string, d *ACDiff, tests ACTestState) (ACCheck, bool) {
	if d == nil || len(d.Files) == 0 {
		return ACCheck{}, false
	}
	prose := acStripCodeSpans(text)
	for _, r := range acRuntimeSteps {
		if r.re.MatchString(prose) {
			return ACCheck{Status: ACNotVerifiable, Step: r.step, Evidence: "needs " + r.step}, true
		}
	}
	inherited := acInheritedPaths(prior, d)
	var met, unmet []acResult
	for _, c := range parseACClauses(text) {
		r := verifyACClause(c, inherited, d, tests)
		switch {
		case !r.ok:
		case r.status == ACUnmet:
			unmet = append(unmet, r)
		default:
			met = append(met, r)
		}
	}
	win, status := unmet, ACUnmet
	if len(win) == 0 {
		win, status = met, ACMet
	}
	if len(win) == 0 {
		return ACCheck{}, false
	}
	r := ACCheck{Status: status}
	var ev []string
	cited := map[string]bool{}
	for _, c := range win {
		ev = append(ev, c.evidence)
		for _, p := range c.paths {
			if d.Uncommitted[p] && !cited[p] {
				cited[p] = true
				r.Uncommitted = append(r.Uncommitted, p)
			}
		}
	}
	r.Evidence = strings.Join(ev, "; ")
	sort.Strings(r.Uncommitted)
	return r, true
}

// acInheritedPaths returns the diffed paths named by the nearest prior
// criterion that names any.
func acInheritedPaths(prior []string, d *ACDiff) []string {
	for i := len(prior) - 1; i >= 0; i-- {
		var out []string
		for _, raw := range splitACClauses(prior[i]) {
			out = append(out, acDiffedPaths(parseACClause(raw), d)...)
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}

// verifyACClause routes a clause to its rule. A verdict drawn from inherited
// files may only add met: the criterion never named those files, so their
// failing to carry its tokens is no evidence against it (#2669).
func verifyACClause(c acClause, inherited []string, d *ACDiff, tests ACTestState) acResult {
	if acIsDescriptionClause(c) {
		// The PR's own description, body or title is not in the diff, so the
		// diff can neither meet nor contradict it (#3529).
		return acResult{}
	}
	switch {
	case acScopeRE.MatchString(c.Prose) && len(c.Paths) > 0:
		return acScope(c, d)
	case acIsPreservation(c) && !preflight.ACAsksAddition(c.Raw):
		// A preservation names a span to keep, so it is judged on the span
		// being present and untouched, never on added content (#3423).
		return acPreserved(c, d)
	case acIsPreservation(c):
		// "keep `X` and add `Y`" is judged on the addition only: no unmet
		// comes from the preserved span (#3423).
		c = acAdditionPart(c)
	}
	switch {
	case acUnchangedAboutTarget(c.Raw):
		return acUnchanged(c, d)
	case acChangelogRE.MatchString(c.Prose) && len(c.Paths) == 0 && len(c.Headings) > 0:
		return acChangelog(c, d)
	case len(c.Revisions) > 0:
		// A comparison clause: its other spans (`cmp`) describe the check,
		// never text the diff must add (#3042).
		return acRevision(c, d)
	case len(c.Digests) > 0:
		return acDigest(c, d)
	case acAmbiguousBare(c, d):
		// A bare name several diffed files share names none of them, so
		// neither they nor inherited files may decide the clause (#3042).
		return acResult{}
	}
	if acIsRemoval(c) {
		if preflight.ACAsksAddition(c.Raw) {
			// A removal check answers only half a mixed clause.
			return acResult{}
		}
		if name, re, ok := acRemovalTarget(c); ok {
			return acRemoved(name, re, d, c.Paths)
		}
		if !acNegContentRE.MatchString(c.Prose) {
			// The presence rules below would answer a removal inverted.
			return acResult{}
		}
	}
	if acNegContentRE.MatchString(c.Prose) && len(c.Tokens) > 0 {
		if _, _, isTest := acTestName(c); isTest || acNegCoverRE.MatchString(c.Prose) {
			// "`TestX` asserts the log does not contain `Y`" asks for a
			// test, not for an absence; the judge decides it.
			return acResult{}
		}
		// A positive rule would answer a negative criterion inverted, so
		// one acAbsent cannot decide goes to the judge.
		return acAbsent(c, d)
	}
	if pattern, ok := acRunPattern(c); ok {
		return acRunTest(pattern, d, tests)
	}
	if name, re, ok := acTestName(c); ok {
		return acTest(name, re, d, tests)
	}
	named := acDiffedPaths(c, d)
	var globRes acResult
	if len(c.Globs) > 0 {
		var matched []string
		globRes, matched = acGlobs(c, d)
		if globRes.ok && globRes.status == ACUnmet {
			return globRes
		}
		for _, p := range matched {
			if !acContains(named, p) {
				named = append(named, p)
			}
		}
	}
	fromInherited := len(named) == 0 && len(inherited) > 0
	var r acResult
	switch {
	case len(named) > 0 && len(c.Headings) > 0:
		r = acSection(c, named, d)
	case fromInherited && len(c.Headings) > 0:
		r = acSection(c, inherited, d)
	default:
		r = acTokens(c, named, inherited, d)
	}
	if fromInherited && r.status == ACUnmet {
		return acResult{}
	}
	if globRes.ok {
		switch {
		case r.ok && r.status == ACUnmet:
			return r
		case r.ok:
			return acAnswer(ACMet, globRes.evidence+"; "+r.evidence, append(globRes.paths, r.paths...)...)
		case len(c.Tokens) == 0 && len(c.Headings) == 0 && len(c.Bounds) == 0:
			return globRes
		}
	}
	return r
}

func acContains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// acBareMatches returns the diffed paths whose base name is p.
func acBareMatches(p string, d *ACDiff) []string {
	var hits []string
	for _, q := range d.sortedPaths() {
		if filepath.Base(q) == p {
			hits = append(hits, q)
		}
	}
	return hits
}

// acAmbiguousBare reports whether the clause names a bare file name that is
// not itself diffed and is the base name of several diffed paths.
func acAmbiguousBare(c acClause, d *ACDiff) bool {
	for _, p := range c.Paths {
		if _, ok := d.Files[p]; ok || strings.Contains(p, "/") {
			continue
		}
		if len(acBareMatches(p, d)) > 1 {
			return true
		}
	}
	return false
}

var (
	acGlobSpanRE    = regexp.MustCompile(`/\*\*|\*\*/|\*\.[A-Za-z0-9]+|/\*(?:$|[/.A-Za-z0-9])`)
	acGlobCharsRE   = regexp.MustCompile(`^[A-Za-z0-9_.\-/*?]+$`)
	acDigestRE      = regexp.MustCompile(`^(?i:[0-9a-f]{64})$`)
	acRevisionRE    = regexp.MustCompile(`^(?i:[0-9a-f]{7,40})$`)
	acRevisionCtxRE = regexp.MustCompile(`(?i)(?:\b(?:at|revision|commit|sha)\s+(?:commit\s+|revision\s+)?|@)$`)
	// acConditionalRE marks a glob that states when something happens ("when
	// `a/**` changes"), not a path the change must touch.
	acConditionalRE = regexp.MustCompile(`(?i)\b(?:when|whenever|if|unless)\b`)
	acCoreRE        = regexp.MustCompile(`(?i)\bthe core\b|\bcore's\b|\bcore repo(?:sitory)?\b`)
	acSlugRE        = regexp.MustCompile(`\b([A-Za-z0-9_.\-]+/[A-Za-z0-9_.\-]+)\b`)
	acRangeRE       = regexp.MustCompile(`(?i)\blines?\b[^.]*\bfrom\b[^.]*\b(?:through|to|until)\b`)
	acEllipsisRE    = regexp.MustCompile(`\.\.\.|…`)
	// acSpreadRE is a JavaScript spread or rest (`...props`): literal text.
	acSpreadRE = regexp.MustCompile(`(?:\.\.\.|…)[A-Za-z_$]`)
	// acGlobChangeRE ties a glob to a change the diff must make.
	acGlobChangeRE = regexp.MustCompile(`(?i)\b(?:chang|updat|touch|add|modif|edit|creat|delet|remov|writ)\w*`)
)

// acIsGlobSpan reports whether a span is a path glob: path characters only,
// with a `**`, `*.ext` or `dir/*` component (#3042).
func acIsGlobSpan(tok string) bool {
	return acGlobCharsRE.MatchString(tok) && acGlobSpanRE.MatchString(tok)
}

// acGlobRegexp converts a path glob to an anchored regexp: `**/` matches
// any directory prefix, `**` anything, `*` and `?` anything but `/`.
func acGlobRegexp(g string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(g); {
		switch {
		case strings.HasPrefix(g[i:], "**/"):
			b.WriteString("(?:.*/)?")
			i += 3
		case strings.HasPrefix(g[i:], "**"):
			b.WriteString(".*")
			i += 2
		case g[i] == '*':
			b.WriteString("[^/]*")
			i++
		case g[i] == '?':
			b.WriteString("[^/]")
			i++
		default:
			b.WriteString(regexp.QuoteMeta(g[i : i+1]))
			i++
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}

// acGlobMatches returns the diffed paths a glob matches.
func acGlobMatches(g string, d *ACDiff) []string {
	re := acGlobRegexp(g)
	var out []string
	for _, p := range d.sortedPaths() {
		if re.MatchString(p) {
			out = append(out, p)
		}
	}
	return out
}

// acGlobs decides a clause's globs against the diffed paths: met when every
// glob matches a diffed path or is itself an added line (a `.gitignore` or
// `files:` entry), unmet when one does neither. A glob in a conditional
// ("when `a/**` changes") states a trigger, not a path the change touches,
// and leaves the clause undecided. matched are the paths the globs match.
func acGlobs(c acClause, d *ACDiff) (r acResult, matched []string) {
	if acConditionalRE.MatchString(c.Prose) {
		return acResult{}, nil
	}
	var ev []string
	for _, g := range c.Globs {
		hits := acGlobMatches(g, d)
		if len(hits) > 0 {
			matched = append(matched, hits...)
			ev = append(ev, fmt.Sprintf("changes %s (matches `%s`)", acList(hits, 3), g))
			continue
		}
		if loc, ok := acAddedToken(g, d.sortedPaths(), d); ok {
			ev = append(ev, fmt.Sprintf("adds `%s` at `%s`", g, loc))
			p, _, _ := strings.Cut(loc, ":")
			matched = append(matched, p)
			continue
		}
		if !acGlobChangeRE.MatchString(c.Prose) {
			// A glob the prose only describes ("the linter skips `x/**`")
			// names no path the change must touch: undecided, not unmet.
			return acResult{}, nil
		}
		return acAnswer(ACUnmet, fmt.Sprintf("no diffed path matches `%s`", g)), nil
	}
	return acAnswer(ACMet, "the diff "+strings.Join(ev, ", "), matched...), matched
}

// acNamedFiles returns the files a clause names: diffed paths, plus named
// paths with a directory that exist on the branch.
func acNamedFiles(c acClause, d *ACDiff) []string {
	out := acDiffedPaths(c, d)
	for _, p := range c.Paths {
		if acContains(out, p) || !strings.Contains(p, "/") {
			continue
		}
		if _, ok := d.headContent(p); ok {
			out = append(out, p)
		}
	}
	return out
}

// acDigest decides a sha256 span by hashing the clause's one named file on
// the branch. No named file, several, or an unreadable one: undecided.
func acDigest(c acClause, d *ACDiff) acResult {
	files := acNamedFiles(c, d)
	if len(files) != 1 {
		return acResult{}
	}
	p := files[0]
	data, ok := d.headContent(p)
	if !ok {
		return acResult{}
	}
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	for _, want := range c.Digests {
		if got != want {
			return acAnswer(ACUnmet, fmt.Sprintf("`%s` at HEAD has sha256 `%s`, not `%s`", p, got, want), p)
		}
	}
	return acAnswer(ACMet, fmt.Sprintf("`%s` at HEAD has sha256 `%s`", p, got), p)
}

// acRevisionRepo resolves the repository a comparison clause names: the
// core, an `owner/repo` slug or a bare repository name among d.Repos. No
// repository named means this one. ok=false when the named repository has
// no known checkout.
func acRevisionRepo(c acClause, d *ACDiff) (path, label string, ok bool) {
	if acCoreRE.MatchString(c.Prose) {
		p, found := d.Repos["nightgauge/nightgauge"]
		return p, "nightgauge/nightgauge", found
	}
	for _, m := range acSlugRE.FindAllStringSubmatch(c.Prose, -1) {
		if p, found := d.Repos[m[1]]; found {
			return p, m[1], true
		}
	}
	slugs := make([]string, 0, len(d.Repos))
	for s := range d.Repos {
		slugs = append(slugs, s)
	}
	sort.Strings(slugs)
	// A bare name matches only as a whole repository name: `nightgauge` is
	// not named by `nightgauge-internal`. The longest name wins; a tie names
	// no repository and leaves the clause undecided.
	best, bestLen, tie := "", 0, false
	for _, s := range slugs {
		_, name, _ := strings.Cut(s, "/")
		if name == "" || !regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_.\-/])`+
			regexp.QuoteMeta(name)+`(?:$|[^A-Za-z0-9_\-/]|\.(?:$|\s))`).MatchString(c.Prose) {
			continue
		}
		switch {
		case len(name) > bestLen:
			best, bestLen, tie = s, len(name), false
		case len(name) == bestLen:
			tie = true
		}
	}
	if tie {
		return "", "", false
	}
	if best != "" {
		return d.Repos[best], best, true
	}
	return d.Workdir, "this repository", d.Workdir != ""
}

// acLineRange returns the lines of data from the first line matching from
// through the next line matching to; ok=false when either is absent.
func acLineRange(data []byte, from, to string) (string, bool) {
	lines := strings.Split(string(data), "\n")
	mFrom, mTo := acSpanMatcher(from), acSpanMatcher(to)
	for i, l := range lines {
		if !mFrom(l) {
			continue
		}
		for j := i; j < len(lines); j++ {
			if mTo(lines[j]) {
				return strings.Join(lines[i:j+1], "\n"), true
			}
		}
		return "", false
	}
	return "", false
}

// acRevision decides a clause comparing a named file with a repository's
// file at a revision: byte-identical is met, different is unmet. "the lines
// from `A` through `B`" narrows the comparison to that range. A checkout,
// revision or file that cannot be read leaves the clause undecided.
func acRevision(c acClause, d *ACDiff) acResult {
	if len(c.Paths) == 0 {
		return acResult{}
	}
	mine, other := c.Paths[0], c.Paths[0]
	if len(c.Paths) > 1 {
		other = c.Paths[1]
	}
	if hits := acBareMatches(mine, d); !strings.Contains(mine, "/") && len(hits) == 1 {
		mine = hits[0]
	}
	repo, label, ok := acRevisionRepo(c, d)
	if !ok {
		return acResult{}
	}
	rev := c.Revisions[0]
	a, okA := d.headContent(mine)
	b, okB := acBlob(repo, rev, other)
	if !okA || !okB {
		return acResult{}
	}
	what := fmt.Sprintf("`%s`", mine)
	if acRangeRE.MatchString(c.Prose) {
		if len(c.Tokens) < 2 {
			return acResult{}
		}
		ra, okA := acLineRange(a, c.Tokens[0], c.Tokens[1])
		rb, okB := acLineRange(b, c.Tokens[0], c.Tokens[1])
		if !okA || !okB {
			return acResult{}
		}
		a, b = []byte(ra), []byte(rb)
		what = fmt.Sprintf("the lines of `%s` from `%s` through `%s`", mine, c.Tokens[0], c.Tokens[1])
	}
	if !bytes.Equal(a, b) {
		return acAnswer(ACUnmet, fmt.Sprintf("%s differ from %s's `%s` at `%s`", what, label, other, rev), mine)
	}
	return acAnswer(ACMet, fmt.Sprintf("%s match %s's `%s` at `%s` byte for byte", what, label, other, rev), mine)
}

// acDiffedPaths returns the clause's paths that are in the diff. A bare file
// name (`bundle-marketplace.sh`) names the one diffed file with that base name,
// so a criterion need not spell out the directory (#2941).
func acDiffedPaths(c acClause, d *ACDiff) []string {
	var out []string
	for _, p := range c.Paths {
		if _, ok := d.Files[p]; ok {
			out = append(out, p)
			continue
		}
		if strings.Contains(p, "/") {
			continue
		}
		if hits := acBareMatches(p, d); len(hits) == 1 {
			out = append(out, hits[0])
		}
	}
	return out
}

func acPathAllowed(path string, allowed []string) bool {
	for _, a := range allowed {
		if path == a || (strings.HasSuffix(a, "/") && strings.HasPrefix(path, a)) {
			return true
		}
	}
	return false
}

// acScope: "no file other than X is modified", "only X changes".
func acScope(c acClause, d *ACDiff) acResult {
	var extra []string
	all := d.sortedPaths()
	for _, p := range all {
		if !acPathAllowed(p, c.Paths) {
			extra = append(extra, p)
		}
	}
	if len(extra) > 0 {
		return acAnswer(ACUnmet, "the diff also changes "+acList(extra, 5), extra...)
	}
	return acAnswer(ACMet, "the diff changes only "+acList(all, 5), all...)
}

// acUnchanged: a named path must be absent from the diff, or its diff must
// delete nothing. Headings narrow the check to the diffed files holding them;
// a heading not qualified by "existing" must also gain no lines. A file that
// must be read and cannot be leaves the clause not diff-checkable.
func acUnchanged(c acClause, d *ACDiff) acResult {
	var globEv []string
	for _, g := range c.Globs {
		if hits := acGlobMatches(g, d); len(hits) > 0 {
			return acAnswer(ACUnmet, fmt.Sprintf("the diff changes %s (matches `%s`)", acList(hits, 3), g), hits...)
		}
		globEv = append(globEv, fmt.Sprintf("no diffed path matches `%s`", g))
	}
	if len(globEv) > 0 && len(c.Paths) == 0 && len(c.Headings) == 0 {
		return acAnswer(ACMet, strings.Join(globEv, "; "))
	}
	if len(c.Paths) > 0 {
		var ev, paths []string
		for _, p := range c.Paths {
			f, ok := d.Files[p]
			switch {
			case !ok:
				ev = append(ev, "`"+p+"` is not in the diff")
			case f.Deleted > 0 || f.Status == "deleted" || f.Status == "renamed":
				return acAnswer(ACUnmet, fmt.Sprintf("`%s` is %s with %d line(s) removed or rewritten", p, f.Status, f.Deleted), p)
			case len(c.Headings) > 0:
				r := acHeadingsUnchanged(c.Headings, f, d)
				if !r.ok || r.status == ACUnmet {
					return r
				}
				ev = append(ev, "`"+p+"` removes no lines and adds none under "+acHeadingList(c.Headings, false))
				paths = append(paths, p)
			default:
				return acAnswer(ACUnmet, fmt.Sprintf("`%s` changes in the diff", p), p)
			}
		}
		return acAnswer(ACMet, strings.Join(ev, "; "), paths...)
	}
	if len(c.Headings) == 0 {
		return acResult{}
	}
	var ev, paths []string
	for _, p := range d.sortedPaths() {
		f := d.Files[p]
		if f.Status == "deleted" {
			continue
		}
		content, ok := d.lines(p)
		if !ok {
			// It may hold the headings; met from the other files would be a guess.
			return acResult{}
		}
		if !acFileHasHeadings(content, c.Headings) {
			continue
		}
		if f.Deleted > 0 {
			return acAnswer(ACUnmet, fmt.Sprintf("`%s` removes or rewrites %d line(s)", p, f.Deleted), p)
		}
		if r := acHeadingsUnchanged(c.Headings, f, d); !r.ok || r.status == ACUnmet {
			return r
		}
		ev = append(ev, fmt.Sprintf("`%s` is a pure insertion that adds nothing under %s", p, acHeadingList(c.Headings, false)))
		paths = append(paths, p)
	}
	if len(ev) == 0 {
		return acResult{}
	}
	return acAnswer(ACMet, strings.Join(ev, "; "), paths...)
}

// acHeadingsUnchanged reports unmet when f adds a line under a heading not
// qualified by "existing", met when it adds none, and ok=false when f cannot
// be read.
func acHeadingsUnchanged(hs []acHeading, f *ACFileDiff, d *ACDiff) acResult {
	content, ok := d.lines(f.Path)
	if !ok {
		return acResult{}
	}
	for _, h := range hs {
		if h.Existing {
			continue
		}
		for _, l := range f.Added {
			if strings.TrimSpace(l.Text) == "" {
				continue
			}
			stack := acHeadingStack(content, l.N)
			if acHeadingEq(stack[h.Level], h.Text) {
				return acAnswer(ACUnmet, fmt.Sprintf("`%s:%d` adds a line under %s", f.Path, l.N, h), f.Path)
			}
		}
	}
	return acAnswer(ACMet, "", f.Path)
}

func acFileHasHeadings(content []string, hs []acHeading) bool {
	for _, h := range hs {
		found := false
		for _, l := range content {
			if m := acMdHeadingRE.FindStringSubmatch(l); m != nil && len(m[1]) == h.Level && acHeadingEq(m[2], h.Text) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return len(hs) > 0
}

// acHeadingStack returns the heading in force at each level for 1-based line
// n of content, ignoring fenced code blocks.
func acHeadingStack(content []string, n int) map[int]string {
	stack := map[int]string{}
	fenced := false
	for i := 0; i < n-1 && i < len(content); i++ {
		l := content[i]
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			fenced = !fenced
			continue
		}
		if fenced {
			continue
		}
		if m := acMdHeadingRE.FindStringSubmatch(l); m != nil {
			lvl := len(m[1])
			stack[lvl] = m[2]
			for k := lvl + 1; k <= 6; k++ {
				delete(stack, k)
			}
		}
	}
	return stack
}

func acHeadingEq(a, b string) bool {
	return strings.EqualFold(preflight.NormalizeACText(a), preflight.NormalizeACText(b))
}

func acHeadingList(hs []acHeading, chain bool) string {
	parts := make([]string, 0, len(hs))
	for _, h := range hs {
		parts = append(parts, h.String())
	}
	if chain {
		return strings.Join(parts, " › ")
	}
	return strings.Join(parts, ", ")
}

// acChangelog: a pathless changelog criterion with a heading means the root
// CHANGELOG.md, which must be in the diff with an added line under it.
func acChangelog(c acClause, d *ACDiff) acResult {
	const path = "CHANGELOG.md"
	if _, ok := d.Files[path]; !ok {
		return acAnswer(ACUnmet, "`"+path+"` is not in the diff")
	}
	return acSection(c, []string{path}, d)
}

// acRunPattern returns the `-run` argument when it is the only place the
// clause names a Go test. A name also given outside `-run` is a required
// definition and stays with acTestName.
func acRunPattern(c acClause) (string, bool) {
	m := acGoRunRE.FindStringSubmatch(c.Raw)
	if m == nil {
		return "", false
	}
	if acGoTestRE.MatchString(acGoRunRE.ReplaceAllString(c.Raw, " ")) {
		return "", false
	}
	return m[1] + m[2] + m[3], true
}

// acRunTest: the `-run` pattern's top-level element must match a Go test
// defined on the branch, and the stage's test run must have passed.
func acRunTest(pattern string, d *ACDiff, tests ACTestState) acResult {
	top, _, _ := strings.Cut(pattern, "/")
	re, err := regexp.Compile(top)
	if err != nil || top == "" {
		return acResult{}
	}
	name, loc, ok, unread := d.findGoTest(re)
	if !ok && unread {
		// A diffed test file could not be read and may define the test.
		return acResult{}
	}
	if !ok {
		return acAnswer(ACUnmet, fmt.Sprintf("`-run` pattern `%s` matches no test defined on the branch", pattern))
	}
	file, _, _ := strings.Cut(loc, ":")
	if passed, known := tests.named(name); known {
		if !passed {
			return acAnswer(ACUnmet, fmt.Sprintf("`-run` pattern `%s` matches `%s` at `%s`, which failed in %s", pattern, name, loc, tests.logSource()), file)
		}
		return acAnswer(ACMet, fmt.Sprintf("`-run` pattern `%s` matches `%s` at `%s`, which passed in %s", pattern, name, loc, tests.logSource()), file)
	}
	if !tests.Passed {
		src := tests.Source
		if src == "" {
			src = "no recorded test run"
		}
		return acAnswer(ACUnmet, fmt.Sprintf("`-run` pattern `%s` matches `%s` at `%s`, but the test run did not pass (%s)", pattern, name, loc, src), file)
	}
	return acAnswer(ACMet, fmt.Sprintf("`-run` pattern `%s` matches `%s` at `%s`, passed in %s", pattern, name, loc, tests.Source), file)
}

// findGoTest finds a Go test definition whose name re matches: in added
// lines, in the branch content of diffed test files, then via git grep.
// unread reports that a diffed test file could not be read, so a miss is
// not evidence the test is absent (#2669).
func (d *ACDiff) findGoTest(re *regexp.Regexp) (name, loc string, found, unread bool) {
	hit := func(text string) (string, bool) {
		m := acGoTestDefRE.FindStringSubmatch(text)
		if m != nil && re.MatchString(m[1]) {
			return m[1], true
		}
		return "", false
	}
	paths := d.sortedPaths()
	for _, p := range paths {
		for _, l := range d.Files[p].Added {
			if n, ok := hit(l.Text); ok {
				return n, fmt.Sprintf("%s:%d", p, l.N), true, false
			}
		}
	}
	for _, p := range paths {
		if !strings.HasSuffix(p, "_test.go") {
			continue
		}
		lines, ok := d.lines(p)
		if !ok {
			unread = true
			continue
		}
		for i, l := range lines {
			if n, ok := hit(l); ok {
				return n, fmt.Sprintf("%s:%d", p, i+1), true, false
			}
		}
	}
	if d.Workdir == "" {
		return "", "", false, unread
	}
	ctx, cancel := context.WithTimeout(context.Background(), acDiffTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-c", "core.quotePath=false", "-c", "submodule.recurse=false", "grep", "-n", "-E", "--untracked", "-e", `^func Test[A-Za-z0-9_]*\(`, "--", "*_test.go")
	cmd.Dir = d.Workdir
	out, err := cmd.Output()
	if err != nil {
		return "", "", false, unread
	}
	for _, line := range strings.Split(string(out), "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) < 3 {
			continue
		}
		if n, ok := hit(parts[2]); ok {
			return n, parts[0] + ":" + parts[1], true, false
		}
	}
	return "", "", false, unread
}

// acTestName finds a named test: Go `TestX`, Python `test_x`, or a quoted
// `it("…")` / `test("…")` title.
func acTestName(c acClause) (string, *regexp.Regexp, bool) {
	if m := acGoTestRE.FindString(c.Raw); m != "" {
		return m, regexp.MustCompile(`func (\([^)]*\) )?` + m + `\(`), true
	}
	if m := acPyTestRE.FindString(c.Raw); m != "" {
		return m, regexp.MustCompile(`def ` + m + `\(`), true
	}
	for _, tok := range c.Tokens {
		if m := acJSTestRE.FindStringSubmatch(tok); m != nil {
			return m[2], regexp.MustCompile(`(it|test|describe)\(["']` + regexp.QuoteMeta(m[2]) + `["']`), true
		}
	}
	return "", nil, false
}

var acGoIdentRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\*?$`)

func acIsRemoval(c acClause) bool {
	return preflight.ACAsksRemoval(c.Raw)
}

// acRemovalTarget names what a removal clause asks to remove: a Go or Python
// test, or a Go function written as a code span. A name written with a
// trailing `*` (`TestCheckParity_*`) is a prefix. The regex is ERE-portable
// because findDefinition also hands it to git grep.
func acRemovalTarget(c acClause) (string, *regexp.Regexp, bool) {
	span := preflight.ACRemovalSpan(c.Raw)
	if span == "" {
		return "", nil, false
	}
	goDef := func(name string, prefix bool) *regexp.Regexp {
		tail := `\(`
		if prefix {
			tail = `[A-Za-z0-9_]*\(`
		}
		// Anchored: gofmt puts top-level funcs at column 0, so a string
		// literal or comment holding the text is not a definition.
		return regexp.MustCompile(`^func (\([^)]*\) )?` + regexp.QuoteMeta(name) + tail)
	}
	if loc := acGoTestRE.FindStringIndex(span); loc != nil {
		name := span[loc[0]:loc[1]]
		prefix := loc[1] < len(span) && span[loc[1]] == '*'
		shown := name
		if prefix {
			shown += "*"
		}
		return shown, goDef(name, prefix), true
	}
	if m := acPyTestRE.FindString(span); m != "" {
		return m, regexp.MustCompile(`^[[:space:]]*def ` + m + `\(`), true
	}
	if len(c.Paths) > 0 || len(c.Headings) > 0 {
		// A path or section names where the removal happens; the
		// token rules decide it.
		return "", nil, false
	}
	if acGoIdentRE.MatchString(span) {
		name := strings.TrimSuffix(span, "*")
		return span, goDef(name, name != span), true
	}
	return "", nil, false
}

// acRemoved answers a removal criterion (#3385). Absence on the branch is the
// success condition, so it never makes the clause unmet: a definition still
// present is unmet; one the diff removed with nothing remaining is met; one
// never in the diff is left to the judge.
//
// A clause naming paths ("`TestParse` is removed from `a/a_test.go`") is
// judged in those paths only, so a same-named definition elsewhere on the
// branch never makes it unmet.
func acRemoved(name string, re *regexp.Regexp, d *ACDiff, scope []string) acResult {
	if len(scope) > 0 {
		return acRemovedIn(name, re, d, scope)
	}
	loc, ok, err := d.findDefinitionErr(re)
	if err != nil {
		// The grep never answered, so absence is unproven.
		return acResult{}
	}
	if ok {
		file, _, _ := strings.Cut(loc, ":")
		return acAnswer(ACUnmet, fmt.Sprintf("`%s` is still defined at `%s`", name, loc), file)
	}
	var paths []string
	for _, p := range d.sortedPaths() {
		for _, l := range d.Files[p].Removed {
			if re.MatchString(l) {
				paths = append(paths, p)
				break
			}
		}
	}
	if len(paths) == 0 {
		return acResult{}
	}
	return acAnswer(ACMet, fmt.Sprintf("`%s` removed from `%s`; `git grep` finds no remaining definition", name, strings.Join(paths, "`, `")), paths...)
}

// acRemovedIn answers a removal clause scoped to named paths: a definition
// still in one of them is unmet; one the diff removed from them (or a path the
// diff deletes) with none left is met; one never in the diff is left to the
// judge, as is a path whose content cannot be read.
func acRemovedIn(name string, re *regexp.Regexp, d *ACDiff, scope []string) acResult {
	var removed []string
	for _, p := range scope {
		f := d.Files[p]
		if f != nil && f.Status == "deleted" {
			for _, l := range f.Removed {
				if re.MatchString(l) {
					removed = append(removed, p)
					break
				}
			}
			continue
		}
		lines, ok := d.lines(p)
		if !ok {
			return acResult{}
		}
		for i, l := range lines {
			if re.MatchString(l) {
				return acAnswer(ACUnmet, fmt.Sprintf("`%s` is still defined at `%s:%d`", name, p, i+1), p)
			}
		}
		if f != nil {
			for _, l := range f.Removed {
				if re.MatchString(l) {
					removed = append(removed, p)
					break
				}
			}
		}
	}
	if len(removed) == 0 {
		return acResult{}
	}
	return acAnswer(ACMet, fmt.Sprintf("`%s` removed from `%s`; no definition remains there", name, strings.Join(acUniq(removed), "`, `")), scope...)
}

// acNegContentRE marks a negative-content criterion (#3385): "`F` no longer
// contains `X`", "`F` no longer grants `X`" (#3582), "`git grep X` returns
// no match". Absence on the branch is the success condition, never whether
// the diff adds the token. Behaviour negatives ("no longer fails") are not
// matched: they stay with the judge.
var (
	acNegContentRE = regexp.MustCompile(`(?i)\bno longer (?:contains?|includes?|mentions?|references?|has)\b|\bno longer (?:grants?|allows?|admits?|permits?|carry|carries|lists?|declares?|defines?)\b|\bdoes(?: not|n't) (?:contain|include|mention|reference)\b|\bdoes(?: not|n't) (?:grant|allow|admit|permit)\b|\bcontains no\b|\b(?:returns?|finds?|has|have) no (?:match(?:es)?|occurrences?|hits?|results?)\b`)
	acGitGrepRE    = regexp.MustCompile(`^git\s+grep\b`)
	acNegCoverRE   = regexp.MustCompile(`(?i)\b(?:covers?|asserts?|verif(?:y|ies)|exercises?|checks?|tests?)\b`)
	acBREMetaRE    = regexp.MustCompile(`[.*\[\]^$\\]`)
)

// acAbsent answers a negative-content criterion. With named paths each token
// must be absent from each path's content on the branch; a path the diff
// deletes holds nothing. Without one, a `git grep` span is run over the
// branch tree. An unreadable file, a failed grep, or a span it cannot parse
// returns the empty result, leaving the clause to the judge.
func acAbsent(c acClause, d *ACDiff) acResult {
	if len(c.Paths) == 0 {
		for _, tok := range c.Tokens {
			if !acGitGrepRE.MatchString(tok) {
				continue
			}
			pat := acGitGrepPattern(tok)
			if pat == "" || d.Workdir == "" {
				return acResult{}
			}
			loc, found, err := acGitGrep(d.Workdir, "-F", pat)
			if err != nil {
				return acResult{}
			}
			if found {
				file, _, _ := strings.Cut(loc, ":")
				return acAnswer(ACUnmet, fmt.Sprintf("`%s` still matches at `%s`", pat, loc), file)
			}
			return acAnswer(ACMet, fmt.Sprintf("`git grep` finds no `%s` on the branch", pat))
		}
		return acResult{}
	}
	targets := acNegTargets(c)
	matchers := make([]func(string) bool, len(targets))
	for i, tok := range targets {
		matchers[i] = acSpanMatcher(tok)
	}
	var removedFrom []string
	for _, p := range c.Paths {
		if f := d.Files[p]; f != nil && f.Status == "deleted" {
			removedFrom = append(removedFrom, p)
			continue
		}
		lines, ok := d.lines(p)
		if !ok {
			return acResult{}
		}
		for ti, match := range matchers {
			for i, l := range lines {
				if match(l) {
					return acAnswer(ACUnmet, fmt.Sprintf("`%s` still present at `%s:%d`", targets[ti], p, i+1), p)
				}
			}
		}
		if f := d.Files[p]; f != nil {
			for _, match := range matchers {
				for _, l := range f.Removed {
					if match(l) {
						removedFrom = append(removedFrom, p)
						break
					}
				}
			}
		}
	}
	ev := fmt.Sprintf("%s absent from `%s` on the branch", acList(targets, 3), strings.Join(c.Paths, "`, `"))
	if len(removedFrom) > 0 {
		ev += fmt.Sprintf("; the diff removed it from `%s`", strings.Join(acUniq(removedFrom), "`, `"))
	}
	return acAnswer(ACMet, ev, c.Paths...)
}

// acNegTargets is the tokens a path-scoped negative criterion requires
// absent: those whose code span follows the negative phrase in c.Raw. In
// "`F` `tools:` no longer grants `X`" the span before the phrase is the
// subject, not text that must be gone (#3582). When none follows, every
// token is a target.
func acNegTargets(c acClause) []string {
	loc := acNegContentRE.FindStringIndex(c.Raw)
	if loc == nil {
		return c.Tokens
	}
	var out []string
	for _, tok := range c.Tokens {
		if i := strings.LastIndex(c.Raw, "`"+tok+"`"); i >= loc[1] {
			out = append(out, tok)
		}
	}
	if len(out) == 0 {
		return c.Tokens
	}
	return out
}

// acGitGrepPattern is the pattern a `git grep [flags] PATTERN [--] [path…]`
// span names: the `-e` argument, else the first argument that is not a flag.
// A quoted pattern, several `-e`, or a flag taking a value returns "", so the
// judge decides rather than a grep for the wrong string.
func acGitGrepPattern(tok string) string {
	if strings.ContainsAny(tok, `"'`) {
		return ""
	}
	fields := strings.Fields(tok)[2:]
	pat := ""
	fixed := false
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		switch {
		case f == "--":
			// A pathspec scopes the grep; the whole-tree grep would not.
			return ""
		case f == "-e":
			if pat != "" || i+1 >= len(fields) {
				return ""
			}
			pat = fields[i+1]
			i++
		case strings.HasPrefix(f, "-"):
			if !acGrepPlainFlagRE.MatchString(f) {
				// -i, -w, -E, -P and friends change what matches.
				return ""
			}
			if f == "--fixed-strings" || (!strings.HasPrefix(f, "--") && strings.Contains(f, "F")) {
				fixed = true
			}
		case pat == "":
			pat = f
		default:
			// A pathspec scopes the grep; the whole-tree grep would not.
			return ""
		}
	}
	if !fixed && acBREMetaRE.MatchString(pat) {
		// Plain git grep reads the pattern as a basic regex; a fixed-string
		// grep for it would answer a different question.
		return ""
	}
	return pat
}

// acGrepPlainFlagRE lists the flags that leave a case-sensitive whole-tree
// grep's answer unchanged. The pattern is grepped with -F, so a pattern
// holding a regex metacharacter needs -F in the span (acGitGrepPattern).
var acGrepPlainFlagRE = regexp.MustCompile(`^-(?:[nFIh]+|-(?:untracked|line-number|fixed-strings|no-color|color))$`)

func acUniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// acTest: the named test must be defined on the branch and the stage's test
// run must have passed.
func acTest(name string, re *regexp.Regexp, d *ACDiff, tests ACTestState) acResult {
	loc, ok := d.findDefinition(re)
	if !ok {
		return acAnswer(ACUnmet, fmt.Sprintf("test `%s` is not defined on the branch", name))
	}
	file, _, _ := strings.Cut(loc, ":")
	if passed, known := tests.named(name); known {
		if !passed {
			return acAnswer(ACUnmet, fmt.Sprintf("test `%s` is defined at `%s`, but %s records `--- FAIL: %s`", name, loc, tests.logSource(), name), file)
		}
		return acAnswer(ACMet, fmt.Sprintf("test `%s` at `%s` passed in %s (`--- PASS: %s`)", name, loc, tests.logSource(), name), file)
	}
	if !tests.Passed {
		src := tests.Source
		if src == "" {
			src = "no recorded test run"
		}
		return acAnswer(ACUnmet, fmt.Sprintf("test `%s` is defined at `%s`, but the test run did not pass (%s)", name, loc, src), file)
	}
	return acAnswer(ACMet, fmt.Sprintf("test `%s` at `%s` passed in %s", name, loc, tests.Source), file)
}

func (d *ACDiff) findDefinition(re *regexp.Regexp) (string, bool) {
	loc, found, _ := d.findDefinitionErr(re)
	return loc, found
}

// findDefinitionErr is findDefinition that tells "no definition" from "the
// grep never answered": err is set when there is no Workdir or git grep
// failed, so absence is never inferred from a search that did not run.
func (d *ACDiff) findDefinitionErr(re *regexp.Regexp) (string, bool, error) {
	for _, p := range d.sortedPaths() {
		for _, l := range d.Files[p].Added {
			if re.MatchString(l.Text) {
				return fmt.Sprintf("%s:%d", p, l.N), true, nil
			}
		}
	}
	if d.Workdir == "" {
		return "", false, errors.New("no workdir to grep")
	}
	return acGitGrep(d.Workdir, "-E", re.String())
}

// acGitGrep runs `git grep -n <mode> -m 1` over the branch tree and returns
// the first match as file:line. git grep exits 1 for no match, which is an
// answer; any other failure is an error.
func acGitGrep(workdir, mode, pat string) (string, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), acDiffTimeout)
	defer cancel()
	// submodule.recurse=false: a user config enabling it makes git grep
	// reject --untracked.
	cmd := exec.CommandContext(ctx, "git", "-c", "core.quotePath=false", "-c", "submodule.recurse=false", "grep", "-n", "-m", "1", mode, "--untracked", "-e", pat)
	cmd.Dir = workdir
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 && ctx.Err() == nil {
			return "", false, nil
		}
		return "", false, err
	}
	first, _, _ := strings.Cut(string(out), "\n")
	parts := strings.SplitN(first, ":", 3)
	if len(parts) < 2 {
		return "", false, fmt.Errorf("unparsed git grep output %q", first)
	}
	return parts[0] + ":" + parts[1], true, nil
}

// acSection: some added line in a named diffed file sits beneath the named
// headings, and those lines carry the clause's tokens.
func acSection(c acClause, diffed []string, d *ACDiff) acResult {
	levels := map[int][]string{}
	for _, h := range c.Headings {
		levels[h.Level] = append(levels[h.Level], h.Text)
	}
	under := func(stack map[int]string) bool {
		for lvl, texts := range levels {
			hit := false
			for _, t := range texts {
				hit = hit || acHeadingEq(stack[lvl], t)
			}
			if !hit {
				return false
			}
		}
		return true
	}
	// Every named file is tried; unmet only when every named file was read
	// and none satisfies. An unreadable file may hold the section, so a clause
	// with any unreadable file and no satisfying one is not diff-checkable,
	// as in acUnchanged (#2669).
	// A placeholder, glob or expression span cannot be checked literally, so a section
	// holding one is never met on its other spans (#3030).
	tokens, patterns := acLiteralSpans(c.Tokens)
	var reasons, tried []string
	skipped, refMissing := false, false
	for _, p := range diffed {
		f := d.Files[p]
		content, ok := d.lines(p)
		if !ok {
			skipped = true
			continue
		}
		tried = append(tried, p)
		var sec []acDiffLine
		for _, l := range f.Added {
			if strings.TrimSpace(l.Text) != "" && under(acHeadingStack(content, l.N)) {
				sec = append(sec, l)
			}
		}
		if len(sec) == 0 {
			reasons = append(reasons, fmt.Sprintf("`%s` adds no line under %s", p, acHeadingList(c.Headings, true)))
			continue
		}
		if missing := acMissingTokens(tokens, sec); len(missing) > 0 {
			reasons = append(reasons, fmt.Sprintf("lines `%s` adds under %s do not mention %s", p, acHeadingList(c.Headings, true), acList(missing, 5)))
			continue
		}
		if patterns {
			return acResult{}
		}
		// A prose ref the section lacks is context, not a miss: this file
		// neither satisfies nor refutes the clause, which is left to
		// validate when no other file satisfies it (#3233).
		if len(acMissingTokens(c.Refs, sec)) > 0 {
			refMissing = true
			continue
		}
		ev := fmt.Sprintf("`%s:%d` adds %d line(s) under %s", p, sec[0].N, len(sec), acHeadingList(c.Headings, true))
		if cited := append(append([]string{}, c.Tokens...), c.Refs...); len(cited) > 0 {
			ev += " citing " + acList(cited, 5)
		}
		return acAnswer(ACMet, ev, p)
	}
	if skipped || refMissing || len(tried) == 0 {
		return acResult{}
	}
	return acAnswer(ACUnmet, strings.Join(reasons, "; "), tried...)
}

// acTokenScope picks the changed files that may answer a token clause naming
// no diffed file (#2840). A changelog line restating a criterion is not
// evidence the criterion holds, so changelogs answer only a clause about the
// changelog: one whose prose says so, one naming a changelog file, or any
// clause of a change that touches nothing but changelogs. A clause naming
// other files, none of them diffed, is about those files, and a token in some
// unrelated changed file proves nothing about them: no scope.
func acTokenScope(c acClause, d *ACDiff) []string {
	about := acChangelogRE.MatchString(c.Prose)
	for _, p := range c.Paths {
		if acChangelogFileRE.MatchString(filepath.Base(p)) {
			about = true
		}
	}
	if len(c.Paths) > 0 && !about {
		return nil
	}
	var scope, changelogs []string
	for _, p := range d.sortedPaths() {
		if acChangelogFileRE.MatchString(filepath.Base(p)) {
			changelogs = append(changelogs, p)
			if !about {
				continue
			}
		}
		scope = append(scope, p)
	}
	// A clause about the changelog is answered by the changed changelogs, so
	// a token in some other changed file cannot meet it (#2669).
	if len(scope) == 0 || (about && len(changelogs) > 0) {
		return changelogs
	}
	return scope
}

// acTokens: every named token appears in the added lines of the clause's
// scope, or in its post-change content provided the diff adds at least one of
// the tokens. A clause whose tokens all predate the diff is undecided.
//
// The scope is the named diffed files. A named file is a place the change
// applies, not the only file that may hold the tokens (#2941): a token the
// named files lack is looked for in the rest of the diff (changelogs aside)
// before it counts as missing, and a package-qualified symbol is found where
// its package's diff changes it. Only a token the whole diff lacks is unmet.
//
// A clause naming no diffed file never draws met from an arbitrary changed
// file (#2669): for a clause about the changelog its scope is acTokenScope,
// otherwise the files inherited from a prior criterion. With neither it is not
// diff-checkable. verifyACClause keeps an inherited verdict only when met.
// Bounds are compared against the values the scope's diff adds.
func acTokens(c acClause, named, inherited []string, d *ACDiff) acResult {
	if len(c.Tokens) == 0 && len(c.Bounds) == 0 && len(c.Refs) == 0 {
		return acResult{}
	}
	scope := named
	switch {
	case len(scope) > 0:
	case acAboutChangelog(c, d):
		// A changelog clause is answered by the changelog, never by a file
		// inherited from an earlier criterion.
		scope = acTokenScope(c, d)
	case len(inherited) > 0:
		scope = inherited
	}
	if len(scope) == 0 {
		return acResult{}
	}
	var ev, paths, missing []string
	cite := func(loc string) {
		p, _, _ := strings.Cut(loc, ":")
		paths = append(paths, p)
	}
	decided := false
	for _, b := range c.Bounds {
		loc, val, ok, paired := acBoundAdded(b, scope, d)
		if !paired {
			continue
		}
		if !ok {
			return acAnswer(ACUnmet, fmt.Sprintf("no value the %s diff changes is %s `%s`", acList(scope, 3), b.Word, b.Value), scope...)
		}
		ev = append(ev, fmt.Sprintf("adds `%s` at `%s`, %s `%s`", val, loc, b.Word, b.Value))
		cite(loc)
		decided = true
	}
	var wide []string
	if len(named) > 0 {
		wide = acWideScope(c, d)
	}
	// Placeholder, glob and expression spans never reach the matchers: a missing literal
	// span is still unmet, otherwise the clause is left to validate (#3030, #3049).
	tokens, patterns := acLiteralSpans(c.Tokens)
	added := 0
	skipped := false
	for _, tok := range tokens {
		present, found, unread := "", false, false
		if loc, ok := acAddedToken(tok, scope, d); ok {
			ev = append(ev, fmt.Sprintf("adds `%s` at `%s`", tok, loc))
			cite(loc)
			added++
		} else if present, found, unread = acPresentToken(tok, scope, d); found {
			ev = append(ev, fmt.Sprintf("keeps `%s` already at `%s`", tok, present))
			cite(present)
		} else if loc, ok := acSymbolPlace(tok, d); ok {
			ev = append(ev, fmt.Sprintf("changes `%s` in `%s`", tok, loc))
			cite(loc)
			added++
		} else if loc, ok := acAddedToken(tok, wide, d); ok {
			ev = append(ev, fmt.Sprintf("adds `%s` at `%s`", tok, loc))
			cite(loc)
			added++
		} else {
			missing = append(missing, tok)
			skipped = skipped || unread ||
				acGoFieldUndecided(tok, scope, d) || acGoFieldWideUndecided(tok, wide, d)
		}
	}
	// A prose #NNN ref is context the criterion names, never text the diff
	// must add: one the diff adds is evidence, one it lacks leaves the clause
	// to validate instead of making it unmet (#3233).
	refMissing := false
	for _, ref := range c.Refs {
		loc, ok := acAddedToken(ref, scope, d)
		if !ok {
			loc, ok = acAddedToken(ref, wide, d)
		}
		if !ok {
			refMissing = true
			continue
		}
		ev = append(ev, fmt.Sprintf("adds `%s` at `%s`", ref, loc))
		cite(loc)
		added++
	}
	if len(missing) > 0 {
		if skipped {
			// A missing token may sit in a scope file the checker could not
			// read: leave the clause to validate rather than call it unmet
			// from content nobody saw (#3037, #2669 ADR-004).
			return acResult{}
		}
		if len(named) == 0 {
			return acAnswer(ACUnmet, fmt.Sprintf("the %s diff does not add %s", acList(scope, 3), acList(missing, 5)), scope...)
		}
		return acAnswer(ACUnmet, fmt.Sprintf("the diff does not add %s (looked in %s and every other changed file)", acList(missing, 5), acList(named, 3)), named...)
	}
	if patterns || refMissing || (added == 0 && !decided) {
		return acResult{}
	}
	return acAnswer(ACMet, "the diff "+strings.Join(ev, ", "), paths...)
}

// acAboutChangelog reports whether a clause naming no diffed file is about
// the changelog: its prose or a named path says so, or the change touches
// only changelogs.
func acAboutChangelog(c acClause, d *ACDiff) bool {
	if acChangelogRE.MatchString(c.Prose) {
		return true
	}
	for _, p := range c.Paths {
		if acChangelogFileRE.MatchString(filepath.Base(p)) {
			return true
		}
	}
	for _, p := range d.sortedPaths() {
		if !acChangelogFileRE.MatchString(filepath.Base(p)) {
			return false
		}
	}
	return len(d.Files) > 0
}

// acWideScope is every changed file a token may be found in when the named
// files lack it: changelogs only for a clause about the changelog, and
// non-Markdown files first, so evidence cites code before prose.
func acWideScope(c acClause, d *ACDiff) []string {
	about := acChangelogRE.MatchString(c.Prose)
	var code, prose []string
	for _, p := range d.sortedPaths() {
		if acChangelogFileRE.MatchString(filepath.Base(p)) && !about {
			continue
		}
		if strings.HasSuffix(p, ".md") || strings.HasSuffix(p, ".mdx") {
			prose = append(prose, p)
		} else {
			code = append(code, p)
		}
	}
	return append(code, prose...)
}

// acSymbolPlace finds a package-qualified symbol's place: a changed file in a
// directory named for the package whose diff adds or removes a line naming
// the symbol. Removed lines count, since renaming or deleting a symbol is a
// change to it.
func acSymbolPlace(tok string, d *ACDiff) (string, bool) {
	m := acQualifiedSymRE.FindStringSubmatch(tok)
	if m == nil {
		return "", false
	}
	for _, p := range d.sortedPaths() {
		if filepath.Base(filepath.Dir(p)) != m[1] {
			continue
		}
		f := d.Files[p]
		for _, l := range f.Added {
			if strings.Contains(l.Text, m[2]) {
				return fmt.Sprintf("%s:%d", p, l.N), true
			}
		}
		for _, l := range f.Removed {
			if strings.Contains(l, m[2]) {
				return p, true
			}
		}
	}
	return "", false
}

// acBoundAdded finds a value the named files' diff changes that satisfies the
// bound. Only a value the change replaces counts: an added line whose text,
// with its values masked, matches a removed line of the same file. Any other
// number on an added line (a timeout, a dependency version) is not the
// bounded subject. paired is false when no such replacement exists, which
// leaves the bound undecided rather than unmet.
func acBoundAdded(b acBound, paths []string, d *ACDiff) (loc, val string, ok, paired bool) {
	re := acIntegerRE
	if strings.Contains(b.Value, ".") {
		re = acVersionRE
	}
	mask := func(t string) string { return strings.TrimSpace(re.ReplaceAllString(t, "#")) }
	for _, p := range paths {
		f := d.Files[p]
		removed := map[string]bool{}
		for _, r := range f.Removed {
			if re.MatchString(r) {
				removed[mask(r)] = true
			}
		}
		for _, l := range f.Added {
			if !removed[mask(l.Text)] {
				continue
			}
			for _, m := range re.FindAllStringSubmatch(l.Text, -1) {
				paired = true
				if b.holds(m[1]) {
					return fmt.Sprintf("%s:%d", p, l.N), m[1], true, true
				}
			}
		}
	}
	return "", "", false, paired
}

// acKeyValueRE parses a key-value span such as `state: SUCCESS`: one key,
// one `:` or `=` separator and one whitespace-free value, each optionally
// quoted (#3030).
var acKeyValueRE = regexp.MustCompile(`^["']?([A-Za-z_][A-Za-z0-9_.\-]*)["']?\s*[:=]\s*["']?([^\s"':=]+)["']?$`)

// acCallSpanRE parses a call span: an identifier, optionally qualified,
// followed by empty parentheses, such as `name()` or `Type.Method()` (#3053),
// or by a placeholder argument list, such as `it(...)` or `run(…)` (#3226).
var acCallSpanRE = regexp.MustCompile(`^(?:[A-Za-z_$][A-Za-z0-9_$]*\.)*([A-Za-z_$][A-Za-z0-9_$]*)\(\s*(?:\.\.\.|…)?\s*\)$`)

// acWrappedIdentRE parses a span that is one identifier wrapped in `{}`, `<>`
// or `[]`, such as `{sessionJwt}` (#3154). Spaces are allowed inside `{}` and
// `[]`; a span holding more than one identifier does not match.
var acWrappedIdentRE = regexp.MustCompile(`^(?:\{\s*([A-Za-z_$][A-Za-z0-9_$]*)\s*\}|<([A-Za-z_$][A-Za-z0-9_$]*)>|\[\s*([A-Za-z_$][A-Za-z0-9_$]*)\s*\])$`)

// acWrappedIdent returns the identifier a wrapped-identifier span holds, or
// "" when tok is not one.
func acWrappedIdent(tok string) string {
	m := acWrappedIdentRE.FindStringSubmatch(tok)
	if m == nil {
		return ""
	}
	for _, g := range m[1:] {
		if g != "" {
			return g
		}
	}
	return ""
}

// acSpanMatcher returns the test a line must pass to contain tok. A key-value
// span matches the same pair written in any quoting and spacing, as JSON,
// YAML or a Go literal, with the key compared case-insensitively and the
// value exactly (#3030). A call span such as `name()` or `obj.name()` matches
// its last name followed by an open parenthesis, optionally after a generic,
// so declarations and calls with arguments count (#3053); a placeholder call
// span such as `it(...)` matches the same way (#3226). A single identifier
// wrapped in `{}`, `<>` or `[]` matches that identifier as a whole word, so
// `{sessionJwt}` matches `[{ sessionJwt: [] }]` but not `sessionJwtX`
// (#3154). A `{ident}` placeholder joined to the text before it by `/`, `.`
// or `-`, as in `/files/{sha}/analyse`, stands for one segment: it matches
// any run of non-space, non-`/` text, so `$sha`, `${sha}` and the literal
// `{sha}` all count (#3529). Any other span matches as a literal substring.
func acSpanMatcher(tok string) func(line string) bool {
	if c := acCallSpanRE.FindStringSubmatch(tok); c != nil {
		return regexp.MustCompile(`(?:^|[^A-Za-z0-9_$])` + regexp.QuoteMeta(c[1]) +
			`\s*(?:<[^>]*>)?\s*\(`).MatchString
	}
	// `...` or `…` stands for any text, never for itself (#3042).
	if acEllipsisRE.MatchString(tok) && !acSpreadRE.MatchString(tok) &&
		strings.TrimSpace(acEllipsisRE.ReplaceAllString(tok, "")) != "" {
		parts := acEllipsisRE.Split(tok, -1)
		for i, p := range parts {
			parts[i] = regexp.QuoteMeta(p)
		}
		return regexp.MustCompile(strings.Join(parts, ".+?")).MatchString
	}
	m := acKeyValueRE.FindStringSubmatch(tok)
	if m == nil {
		if id := acWrappedIdent(tok); id != "" {
			return regexp.MustCompile(`(?:^|[^A-Za-z0-9_$])` + regexp.QuoteMeta(id) +
				`(?:[^A-Za-z0-9_$]|$)`).MatchString
		}
		if re := acBracePlaceholderPattern(tok); re != nil {
			return re.MatchString
		}
		return func(line string) bool { return strings.Contains(line, tok) }
	}
	re := regexp.MustCompile(`(?:^|[^A-Za-z0-9_])["']?(?i:` + regexp.QuoteMeta(m[1]) +
		`)["']?\s*[:=]\s*["']?` + regexp.QuoteMeta(m[2]) + `(?:["']|[^A-Za-z0-9_]|$)`)
	return re.MatchString
}

// acBracePlaceholderRE finds a `{ident}` placeholder joined to the text
// before it by `/`, `.` or `-` (#3529). A whole-span `{ident}` is the
// wrapped-identifier rule's (#3154); `Type{Field}` and `map[K]{}` stay literal.
var acBracePlaceholderRE = regexp.MustCompile(`[/.\-]\{[A-Za-z_][A-Za-z0-9_]*\}`)

// acBracePlaceholderPattern returns the matcher for a span holding a joined
// `{ident}` placeholder, each placeholder matching one segment and the rest of
// the span literal; nil when tok holds none.
func acBracePlaceholderPattern(tok string) *regexp.Regexp {
	locs := acBracePlaceholderRE.FindAllStringIndex(tok, -1)
	if locs == nil {
		return nil
	}
	var b strings.Builder
	last := 0
	for _, l := range locs {
		// l[0] is the separator, kept literal; the placeholder follows it.
		b.WriteString(regexp.QuoteMeta(tok[last : l[0]+1]))
		b.WriteString(`[^\s/]+`)
		last = l[1]
	}
	b.WriteString(regexp.QuoteMeta(tok[last:]))
	return regexp.MustCompile(b.String())
}

// acBraceGrepLiteral is the longest literal run of a placeholder span.
func acBraceGrepLiteral(tok string) string {
	longest := ""
	for _, part := range acBracePlaceholderRE.Split(tok, -1) {
		if len(part) > len(longest) {
			longest = part
		}
	}
	return longest
}

// acPatternSpanRE matches notation that stands for text rather than being
// text (#3030): a `<placeholder>` holding a digit, `-` or `_`, or one joined
// to other text by a separator (`g<sha7>`, `<N>.md`); a path glob (`/**`,
// `**/`, `*.go`, `dir/*`); or a variable-plus-number expression such as
// `(Z+1)`. A bare `<div>`, a generic `Promise<void>`, a pointer `*p`, a
// `/* comment */`, `**kwargs` and a phrase like `(not-found)` stay literal.
var acPatternSpanRE = regexp.MustCompile(`<[A-Za-z][A-Za-z0-9]*[0-9_\-][A-Za-z0-9_\-]*>` +
	`|(?:^|[^A-Za-z0-9_]|(?:^|[^A-Za-z0-9_])[A-Za-z])<[A-Za-z][A-Za-z0-9_\-]*>[^\s>]` +
	`|[.\-/]<[A-Za-z][A-Za-z0-9_\-]*>` +
	`|/\*\*|\*\*/|\*\.[A-Za-z0-9]+|/\*(?:$|[/.A-Za-z0-9])` +
	`|\([A-Za-z][A-Za-z0-9_]*\s*[+\-]\s*[0-9]+\)`)

// acExpressionSpanRE matches a span stating a condition (#3049): a comparison
// or logical operator between two operands (`a === b`, `x <= n`, `p && q`).
// An arrow `=>`, a `--flag=value`, an assignment and a bare operator stay
// literal.
var acExpressionSpanRE = regexp.MustCompile(`[\w)\]"'.]\s*(?:===|!==|==|!=|<=|>=|&&|\|\|)\s*[\w(\["'!.\-]`)

// acIsPatternSpan reports whether a span is a placeholder, glob or expression
// that no literal line need contain, so it can neither meet nor contradict a
// clause (#3030, #3049).
func acIsPatternSpan(tok string) bool {
	if acIsGlobSpan(tok) {
		return false // a path glob is decided against the diffed paths (#3042)
	}
	return acPatternSpanRE.MatchString(tok) || acExpressionSpanRE.MatchString(tok)
}

// acUnchangedBreakRE matches text that ends the phrase an unchanged word can
// be about: a colon, a parenthesis, a semicolon or a sentence end.
var acUnchangedBreakRE = regexp.MustCompile(`[:();]|\.(?:\s|$)`)

// acUnchangedAboutTarget reports whether an unchanged word in raw is about a
// named path or heading (#3049): the nearest code span before the word is a
// path or heading, and no clause break or expression span lies between them.
// "`p` is unchanged", "leaves `p` unchanged" and "with `p` untouched" are;
// "covers: unchanged when short" and "(`a === b` is unchanged)" are not.
func acUnchangedAboutTarget(raw string) bool {
	spans := acCodeSpans(raw)
	for _, w := range acUnchangedRE.FindAllStringIndex(raw, -1) {
		inside := false
		for _, sp := range spans {
			if w[0] >= sp[0] && w[0] < sp[1] {
				inside = true
				break
			}
		}
		if inside {
			continue
		}
		for i := len(spans) - 1; i >= 0; i-- {
			sp := spans[i]
			if sp[1] > w[0] {
				continue
			}
			if acUnchangedBreakRE.MatchString(raw[sp[1]:w[0]]) {
				break
			}
			tok := acSpanContent(raw[sp[2]:sp[3]])
			if acExpressionSpanRE.MatchString(tok) {
				break
			}
			if acIsPath(tok) || acIsGlobSpan(tok) || acHeadingTokRE.MatchString(tok) {
				return true
			}
		}
	}
	return false
}

// acLiteralSpans splits tokens into literal spans and pattern spans
// (placeholder, glob or expression spans; #3030, #3049).
func acLiteralSpans(tokens []string) (literal []string, patterns bool) {
	for _, tok := range tokens {
		if acIsPatternSpan(tok) {
			patterns = true
			continue
		}
		literal = append(literal, tok)
	}
	return literal, patterns
}

func acAddedToken(tok string, scope []string, d *ACDiff) (string, bool) {
	match := acSpanMatcher(tok)
	for _, p := range scope {
		f := d.Files[p]
		if f == nil {
			continue
		}
		for _, l := range f.Added {
			if match(l.Text) {
				return fmt.Sprintf("%s:%d", p, l.N), true
			}
		}
		lines, _ := d.yamlKeyPath(tok, p)
		for _, n := range lines {
			for _, l := range f.Added {
				if l.N == n {
					return fmt.Sprintf("%s:%d", p, n), true
				}
			}
		}
		// A Go field span set by a keyed or positional literal (#3424).
		if g, _ := d.goFieldValue(tok, p); len(g.added) > 0 {
			return fmt.Sprintf("%s:%d", p, g.added[0]), true
		}
	}
	return "", false
}

// acGoFieldUndecided reports that no scope file can decide a missing Go
// field span `name: value` (#3424), so its absence is not evidence and the
// clause is not verifiable, never unmet. Only the files the clause names
// (its scope) decide: an unrelated changed file in wide scope never turns a
// named Markdown or TypeScript file into a decider. A scope file decides the
// span when the diff deletes it (it cannot set the field), when it is a Go
// file that reads, parses and resolves every positional literal on an added
// line, when it is a Go file whose matcher does not read tok as a field
// value (its text decides), or when it is a YAML file (its key-path rule
// decides). The span is
// undecided when any scope Go file is unresolved or unreadable, or when no
// scope file decides it: Markdown, JSON or TypeScript carry no field order.
// The field-span guard keeps every other token on today's verdict.
func acGoFieldUndecided(tok string, scope []string, d *ACDiff) bool {
	if !acIsFieldSpan(tok) {
		return false
	}
	decides := false
	for _, p := range scope {
		if f := d.Files[p]; f != nil && f.Status == "deleted" {
			decides = true
		} else if g, ok := d.goFieldValue(tok, p); ok {
			if g.undecided {
				return true
			}
			decides = true
		} else if acIsYAMLPath(p) || strings.HasSuffix(p, ".go") {
			// YAML keeps its key-path rule; a Go file the matcher does not
			// read as a field span (`On: a:b`) is judged by its text.
			decides = true
		}
	}
	return !decides
}

// acGoFieldWideUndecided reports that a changed Go file outside the named
// scope may set the field span positionally under a type it cannot resolve
// (#3424). Wide files never decide a span, so they can only make it
// undecided; a file the diff deletes has no content and is skipped.
func acGoFieldWideUndecided(tok string, wide []string, d *ACDiff) bool {
	for _, p := range wide {
		if f := d.Files[p]; f != nil && f.Status == "deleted" {
			continue
		}
		if g, ok := d.goFieldValue(tok, p); ok && g.undecided {
			return true
		}
	}
	return false
}

// acIsYAMLPath reports that p is a YAML file, judged by the key-path rule.
func acIsYAMLPath(p string) bool {
	return strings.HasSuffix(p, ".yml") || strings.HasSuffix(p, ".yaml")
}

// acYAMLKeyRE parses a YAML key-path span `a: b`: `:` only, so `a=b` stays
// a plain key-value span.
var acYAMLKeyRE = regexp.MustCompile(`^["']?([A-Za-z_][A-Za-z0-9_.\-]*)["']?\s*:\s*["']?([^\s"':=]+)["']?$`)

// yamlKeyPath matches a `a: b` span against the YAML file p as a nested key
// path (#3042): mapping key a, at any depth, whose value is the scalar b, a
// mapping holding key b, or a sequence holding item b. It returns the 1-based
// lines of each b node; ok=false when tok is no key path, p is not YAML, or
// p does not parse (the line matcher then stands alone).
func (d *ACDiff) yamlKeyPath(tok, p string) ([]int, bool) {
	m := acYAMLKeyRE.FindStringSubmatch(tok)
	if m == nil || !acIsYAMLPath(p) {
		return nil, false
	}
	if d.yaml == nil {
		d.yaml = map[string]*yaml.Node{}
	}
	root, seen := d.yaml[p]
	if !seen {
		if lines, ok := d.lines(p); ok {
			var n yaml.Node
			if yaml.Unmarshal([]byte(strings.Join(lines, "\n")), &n) == nil {
				root = &n
			}
		}
		d.yaml[p] = root
	}
	if root == nil {
		return nil, false
	}
	var out []int
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		if n.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(n.Content); i += 2 {
				k, v := n.Content[i], n.Content[i+1]
				if strings.EqualFold(k.Value, m[1]) {
					out = append(out, acYAMLChild(v, m[2])...)
				}
			}
		}
		for _, c := range n.Content {
			walk(c)
		}
	}
	walk(root)
	return out, true
}

// acYAMLChild returns the lines where v holds b: as its scalar value, as a
// mapping key, or as a sequence item.
func acYAMLChild(v *yaml.Node, b string) []int {
	switch v.Kind {
	case yaml.ScalarNode:
		if v.Value == b {
			return []int{v.Line}
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(v.Content); i += 2 {
			if v.Content[i].Value == b {
				return []int{v.Content[i].Line}
			}
		}
	case yaml.SequenceNode:
		for _, it := range v.Content {
			if it.Kind == yaml.ScalarNode && it.Value == b {
				return []int{it.Line}
			}
		}
	}
	return nil
}

// acPresentToken looks for tok in the post-change content of the scope
// files. unread reports that a scope file could not be read, so a token it
// did not find may still be there: an unreadable file is never empty (#3037).
func acPresentToken(tok string, scope []string, d *ACDiff) (loc string, found, unread bool) {
	match := acSpanMatcher(tok)
	for _, p := range scope {
		lines, ok := d.lines(p)
		if !ok {
			unread = true
			continue
		}
		for i, l := range lines {
			if match(l) {
				return fmt.Sprintf("%s:%d", p, i+1), true, unread
			}
		}
		if ys, _ := d.yamlKeyPath(tok, p); len(ys) > 0 {
			return fmt.Sprintf("%s:%d", p, ys[0]), true, unread
		}
	}
	return "", false, unread
}

func acMissingTokens(tokens []string, lines []acDiffLine) []string {
	var missing []string
	for _, tok := range tokens {
		found := false
		match := acSpanMatcher(tok)
		for _, l := range lines {
			if match(l.Text) {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, tok)
		}
	}
	return missing
}

func acList(items []string, limit int) string {
	quoted := make([]string, 0, len(items))
	for i, s := range items {
		if i == limit {
			quoted = append(quoted, fmt.Sprintf("and %d more", len(items)-limit))
			break
		}
		quoted = append(quoted, "`"+s+"`")
	}
	return strings.Join(quoted, ", ")
}

// hasDeliverable reports whether the diff changes anything outside the
// pipeline's bookkeeping directories (#2941).
func (d *ACDiff) hasDeliverable() bool {
	for p := range d.Files {
		if !ci.IsBookkeepingPath(p) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Preservation (#3423)
// ---------------------------------------------------------------------------

// acIsPreservation reports whether a clause asks for a named span to stay as
// it is: a preservation verb (preflight.ACAsksPreservation) governs a literal
// span that is not a path. A clause naming a heading keeps the section rule
// (acUnchanged), and one naming a test or a `go test -run` pattern keeps its
// test rule, which reads the test run.
func acIsPreservation(c acClause) bool {
	if !preflight.ACAsksPreservation(c.Raw) || len(c.Headings) > 0 || len(acPreservedTokens(c)) == 0 {
		return false
	}
	if _, ok := acRunPattern(c); ok {
		return false
	}
	_, _, isTest := acTestName(c)
	return !isTest
}

// acPreserveObjectRE marks a preservation verb that takes its span as an
// object ("keep `X`", "does not change `X`"); every other verb governs the
// span before it ("`X` remains", "`X` is unchanged").
var acPreserveObjectRE = regexp.MustCompile(`(?i)^(?:keeps?|do(?:es)? not change)$`)

// acTestScopeRE matches a test noun phrase after a span and any qualifiers,
// which can narrow what is preserved to that span's tests (#3501). A noun with file,
// suite or case always heads its phrase. A lone "test", "tests", "suite",
// "spec" or "specs" does only when no other noun follows, so "test flag" and
// "spec version" do not match.
var acTestScopeRE = regexp.MustCompile(`(?i)^\s+(?:` +
	`(?:tests?|spec) (?:files?|suites?|cases?)(?:[^A-Za-z0-9_-]|$)` +
	`|(?:tests?|suite|specs?)(?:\s*(?:[^\sA-Za-z0-9_-]|$)|\s+(?:[a-z]+ly|is|are|was|were|be|been|being|` +
	`remains?|stays?|keeps?|kept|still|untouched|unchanged|unmodified|intact|green|passing|pass(?:es)?|` +
	`continues?|must|should|will|can|does|do|has|have|not|that|which|` +
	`and|or|but|as|at|in|for|from|of|on|to|with|without)\b))`)

// acTestsOnlyRE matches what follows a span when the clause preserves its
// tests and nothing else: a test noun, then only words saying they are kept.
// "spec" and a lone "suite" are absent because they can name another thing (a
// document, a cipher suite).
var acTestsOnlyRE = regexp.MustCompile(acTestsTail + `))*[\s.!;,)]*$`)

// acTestResultOnlyRE is acTestsOnlyRE for a kept result, so it also takes the
// words naming one.
var acTestResultOnlyRE = regexp.MustCompile(acTestsTail + `|passing|green|succeed(?:s|ing)?|working))*[\s.!;,)]*$`)

const acTestsTail = `(?i)^\s+tests?(?: (?:files?|suites?|cases?))?` +
	`(?:\s+(?:is|are|be|remains?|stays?|must|should|still|left|completely|entirely|and|pass(?:es)?|` +
	`untouched|unchanged|unmodified|intact|not (?:modified|changed|touched|edited)`

// acTestsLeadRE matches all that may precede a span when its tests are the one
// thing kept: a command's result ("`npm test` passes with"), the verb keeping
// them and determiners, each optional. The list is closed, because no list of
// the words that can join or keep a second thing is complete.
var acTestsLeadRE = regexp.MustCompile("(?i)^\\s*(?:" + acCommandResult + ` with\s+)?` +
	`(?:(?:keeps?|do(?:es)? not change)\s+)?(?:(?:the|its|all|existing|current)\s+)*$`)

// acResultClauseRE matches a clause that is only a command's result, which
// keeps nothing.
var acResultClauseRE = regexp.MustCompile("(?i)^\\s*(?:and\\s+)?" + acCommandResult + `[\s.!]*$`)

// acCommandResult matches a command span, which holds whitespace and so is
// never a preserved span, and its passing.
const acCommandResult = "`[^`\\s]+\\s[^`]*`" + `\s+(?:pass(?:es)?|succeeds?|is green)`

// acPreserveWordRE matches "preserve", which asks for preservation without
// being a verb that governs a span.
var acPreserveWordRE = regexp.MustCompile(`(?i)\bpreserv(?:e[sd]?|ing)\b`)

// acMaxQualifiers is how many words may stand between a span and its test noun.
const acMaxQualifiers = 2

// acQualifierWordRE matches one whitespace-delimited word of letters.
var acQualifierWordRE = regexp.MustCompile(`^\s+([A-Za-z]+)\s`)

// acNotQualifierRE matches a word that cannot qualify a test noun: a test
// noun, a conjunction, preposition, article, pronoun, auxiliary or negation,
// an adverb, or a word in a single "s", which reads as a verb ("needs").
var acNotQualifierRE = regexp.MustCompile(`(?i)^(?:tests?|suites?|specs?|` +
	`and|or|nor|plus|but|yet|so|if|unless|while|when|whereas|because|although|though|than|then|` +
	`albeit|assuming|considering|given|granted|granting|how|however|immediately|lest|now|once|only|` +
	`provided|providing|save|seeing|since|supposing|till|whenever|whence|whereupon|wherever|whether|` +
	`whilst|why|amidst|amongst|beneath|notwithstanding|pending|regarding|underneath|versus|` +
	`apart|aside|bar|barring|beyond|minus|omitting|sans|unlike|` +
	`about|above|across|after|against|along|alongside|amid|among|around|at|before|behind|below|beside|` +
	`besides|between|beyond|by|despite|during|except|excluding|for|from|in|including|inside|into|like|near|` +
	`of|off|on|onto|out|outside|over|per|since|through|throughout|to|toward|under|until|up|upon|via|with|` +
	`within|without|a|an|the|its|it|their|they|them|this|that|these|those|which|who|whose|what|where|` +
	`our|we|you|your|all|any|each|every|both|some|is|are|am|was|were|be|been|being|has|have|had|having|` +
	`do|does|did|can|could|may|might|must|shall|should|will|would|need|not|no|never|` +
	`[a-z]+ly|[a-z]*[a-rt-z]s)$`)

// acAndOrRE matches a coordinating "and" or "or".
var acAndOrRE = regexp.MustCompile(`(?i)\b(?:and|or)\b`)

// acTestResultRE matches wording that keeps a test result.
var acTestResultRE = regexp.MustCompile(`(?i)\b(?:pass(?:es|ing)?|green|succeed(?:s|ing)?|working)\b`)

// acTestContentRE matches wording that keeps content, which decides when a
// result is named too ("still passes unchanged").
var acTestContentRE = regexp.MustCompile(`(?i)\b(?:untouched|unchanged|unmodified|intact|not (?:modified|changed|touched|edited)|` +
	`do(?:es)? not change|without (?:changes?|edits?|modifications?))\b`)

// acPreservedSpan is one reading of a governed span: the span itself, or,
// when testScoped, the tests that contain it. A testScoped reading without
// testsOnly may keep more after the span or in another clause, so the span
// itself is judged as well.
type acPreservedSpan struct {
	tok        string
	testScoped bool
	testsOnly  bool
}

// acPreservedTokens returns the distinct text of acPreservedSpans.
func acPreservedTokens(c acClause) []string {
	var out []string
	for _, s := range acPreservedSpans(c) {
		if !acContains(out, s.tok) {
			out = append(out, s.tok)
		}
	}
	return out
}

// acAsksKept reports whether text holds any word asking for preservation.
func acAsksKept(text string) bool {
	return len(preflight.ACPreservationVerbs(text)) > 0 ||
		acTestContentRE.MatchString(text) || acPreserveWordRE.MatchString(text)
}

// acPluralSingularRE matches "tests" before a singular verb, whose subject is
// then the span and not its tests ("`X` unlike tests is unchanged").
var acPluralSingularRE = regexp.MustCompile(`(?i)^\s+tests\s+(?:is|remains|stays)\b`)

// acTestNounAt finds the test noun phrase in the text after a span. It may
// follow the span directly or after one or two qualifiers ("unit", "browser
// regression"). Words can pose as qualifiers, so the qualified form also
// needs a tail that keeps only tests ("`X` needs tests and is unchanged"
// keeps X).
func acTestNounAt(after string) (at int, ok bool) {
	if acTestScopeRE.MatchString(after) {
		return 0, true
	}
	for range acMaxQualifiers {
		m := acQualifierWordRE.FindStringSubmatchIndex(after[at:])
		if m == nil {
			return 0, false
		}
		word := after[at+m[2] : at+m[3]]
		if lw := strings.ToLower(word); lw != "existing" && strings.HasSuffix(lw, "ing") {
			return 0, false // reads as a verb ("ignoring tests")
		}
		if acNotQualifierRE.MatchString(word) || acTestResultRE.MatchString(word) || acAsksKept(word) {
			return 0, false
		}
		at += m[3]
		tail := after[at:]
		if !acTestScopeRE.MatchString(tail) {
			continue
		}
		if acPluralSingularRE.MatchString(tail) || !acTailKeepsOnly(acKeptTailRE(tail), tail) {
			return 0, false
		}
		return at, true
	}
	return 0, false
}

// acKeptTailRE returns what may follow a test noun in wording that keeps only
// tests: result words too, when kept names a result and no content.
func acKeptTailRE(kept string) *regexp.Regexp {
	if acTestResultRE.MatchString(kept) && !acTestContentRE.MatchString(kept) {
		return acTestResultOnlyRE
	}
	return acTestsOnlyRE
}

// acTailKeepsOnly reports whether tail, the text from a test noun, is all
// allowed by re and has no and/or before it says the tests are kept
// ("`X` tests and is unchanged" keeps X).
func acTailKeepsOnly(re *regexp.Regexp, tail string) bool {
	if !re.MatchString(tail) {
		return false
	}
	end := len(tail)
	if v := preflight.ACPreservationVerbs(tail); len(v) > 0 {
		end = v[0][0]
	}
	if m := acTestContentRE.FindStringIndex(tail); m != nil {
		end = min(end, m[0])
	}
	return !acAndOrRE.MatchString(tail[:end])
}

// acObjectLeadRE matches a lead whose verb takes the span as its object.
var acObjectLeadRE = regexp.MustCompile(`(?i)(?:^|\s)(?:keeps?|do(?:es)? not change)\s+` +
	`(?:(?:the|its|all|existing|current)\s+)*$`)

// acFiniteTailRE matches a verb that gives a test noun its own predicate
// ("tests pass", "tests are untouched").
var acFiniteTailRE = regexp.MustCompile(`(?i)\b(?:is|are|be|remains?|stays?|must|should|pass(?:es)?|succeeds?)\b`)

// acTestScopeAt reports whether the test scope applies to the span sp, and
// where in c.Raw its noun phrase starts. The scope can turn a strict unmet
// into met, so it applies only when the span's tests are the one thing kept;
// any other phrasing keeps the strict verdict. After a verb that takes the span
// as its object, a qualified noun with its own verb starts another clause
// whatever the words between ("Keep `X` once tests pass" keeps X), so no list
// of those words has to be complete.
func acTestScopeAt(c acClause, sp [4]int) (noun int, ok bool) {
	at, ok := acTestNounAt(c.Raw[sp[1]:])
	if !ok || c.AfterAnd || !acTestsLeadRE.MatchString(c.Raw[:sp[0]]) {
		return sp[1], false
	}
	if at > 0 && acObjectLeadRE.MatchString(c.Raw[:sp[0]]) && acFiniteTailRE.MatchString(c.Raw[sp[1]+at:]) {
		return sp[1], false
	}
	return sp[1] + at, true
}

// acPreservedSpans returns the literal spans a preservation verb governs: the
// span directly before the verb ("`X` keeps its value", "`X` is unchanged")
// or, for a transitive verb, directly after it ("keep `X`"), with at most
// three words and no clause break between. A test scope's qualifiers are not
// counted among the three. A path, a span holding whitespace (a command such
// as `npm test`), a placeholder and an expression are never preserved spans.
func acPreservedSpans(c acClause) []acPreservedSpan {
	lit, _ := acLiteralSpans(c.Tokens)
	literal := map[string]bool{}
	for _, t := range lit {
		if !strings.ContainsAny(t, " \t") {
			literal[t] = true
		}
	}
	spans := acCodeSpans(c.Raw)
	near := func(gap string) bool {
		return !acUnchangedBreakRE.MatchString(gap) && !strings.Contains(gap, ",") && len(strings.Fields(gap)) <= 3
	}
	seen := map[acPreservedSpan]bool{}
	var out []acPreservedSpan
	take := func(sp [4]int, verb int) {
		s := acPreservedSpan{tok: strings.TrimSpace(acSpanContent(c.Raw[sp[2]:sp[3]]))}
		if noun, ok := acTestScopeAt(c, sp); ok {
			s.testScoped = true
			only := acKeptTailRE(c.Raw[min(sp[0], verb):])
			s.testsOnly = !c.Joined && acTailKeepsOnly(only, c.Raw[noun:])
		}
		if literal[s.tok] && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, v := range preflight.ACPreservationVerbs(c.Raw) {
		for i := len(spans) - 1; i >= 0; i-- {
			if spans[i][1] <= v[0] {
				from, _ := acTestScopeAt(c, spans[i]) // past any qualifiers
				if near(c.Raw[from:v[0]]) {
					take(spans[i], v[0])
				}
				break
			}
		}
		if !acPreserveObjectRE.MatchString(c.Raw[v[0]:v[1]]) {
			continue
		}
		for _, sp := range spans {
			if sp[0] >= v[1] {
				if near(c.Raw[v[1]:sp[0]]) {
					take(sp, v[0])
				}
				break
			}
		}
	}
	return out
}

// acAdditionPart narrows a mixed clause ("keep `X` and add `Y`") to the spans
// no preservation verb governs, so it is judged on the addition alone.
func acAdditionPart(c acClause) acClause {
	kept := map[string]bool{}
	for _, t := range acPreservedTokens(c) {
		kept[t] = true
	}
	out := c
	out.Tokens = nil
	for _, tok := range c.Tokens {
		if !kept[tok] {
			out.Tokens = append(out.Tokens, tok)
		}
	}
	return out
}

// acPreserved answers a preservation clause (#3423) by acPreservedStrict,
// except where a span's test scope applies (#3501). Such a span, a kept result
// included, is judged by acTestsPreserved; unless the clause says only that
// those tests are kept, a changed line containing the span is still unmet.
// When any scoped span gets no decision, the whole clause gets the strict
// verdict: the scope turns a strict unmet into met only on tests it has read,
// beside no touched file that may be a test it does not know.
// A path the clause names need only exist.
func acPreserved(c acClause, d *ACDiff) acResult {
	spans := acPreservedSpans(c)
	if len(spans) == 0 {
		return acResult{}
	}
	for _, p := range c.Paths {
		if f := d.Files[p]; f != nil && f.Status == "deleted" {
			return acAnswer(ACUnmet, fmt.Sprintf("the diff deletes `%s`", p), p)
		}
		if _, ok := d.lines(p); !ok {
			return acResult{}
		}
	}
	var tokens []string
	var scoped []acPreservedSpan
	for _, s := range spans {
		if s.testScoped {
			scoped = append(scoped, s)
		} else {
			tokens = append(tokens, s.tok)
		}
	}
	if len(scoped) == 0 {
		return acPreservedStrict(tokens, c.Paths, d)
	}
	strict := func() acResult { return acPreservedStrict(acPreservedTokens(c), c.Paths, d) }

	// Every scoped span is judged before any falls back, so an edited test is
	// named whatever the others answer.
	judged, decided := make([]acResult, len(scoped)), true
	for i, s := range scoped {
		r := acTestsPreserved(s.tok, d)
		if r.status == ACUnmet {
			return r
		}
		judged[i] = r
		decided = decided && r.ok
	}
	if !decided {
		return strict()
	}
	var testEv, mayKeep []string
	for i, s := range scoped {
		if !s.testsOnly {
			// The clause may also keep X itself ("tests and behavior", a spec
			// document): no changed line may contain it, and a change outside
			// the tests that names it gets the whole strict verdict.
			mayKeep = append(mayKeep, s.tok)
			if acChangesOutsideTests(s.tok, d) {
				if !acContains(tokens, s.tok) {
					tokens = append(tokens, s.tok)
				}
				continue
			}
		}
		testEv = append(testEv, judged[i].evidence)
	}
	if r := acChangedLine(mayKeep, d); r.ok {
		return r
	}
	if len(tokens) > 0 {
		r := acPreservedStrict(tokens, c.Paths, d)
		switch {
		case !r.ok:
			return strict()
		case r.status == ACUnmet:
			return r
		}
		testEv = append(testEv, r.evidence)
	}
	return acAnswer(ACMet, strings.Join(testEv, "; "))
}

// acPreservedStrict answers preserved spans by their text (#3423): met when
// each exists on the branch and no non-comment line the diff adds or removes
// contains it; unmet naming the changed line when one does, or when a search
// that ran finds no such span. A search that did not run answers nothing.
func acPreservedStrict(tokens, paths []string, d *ACDiff) acResult {
	if r := acChangedLine(tokens, d); r.ok {
		return r
	}
	var ev []string
	for _, tok := range tokens {
		loc, found, err := acPresentAnywhere(tok, paths, d)
		if err != nil {
			return acResult{}
		}
		if !found {
			return acAnswer(ACUnmet, fmt.Sprintf("`%s` is not on the branch, so nothing keeps it", tok), paths...)
		}
		ev = append(ev, fmt.Sprintf("`%s` at `%s`", tok, loc))
	}
	return acAnswer(ACMet, "the diff changes no line containing "+acList(tokens, 5)+"; kept: "+strings.Join(ev, ", "))
}

// acChangedLine is unmet naming the first non-comment line the diff adds or
// removes that contains one of tokens, and no answer when none does.
func acChangedLine(tokens []string, d *ACDiff) acResult {
	for _, tok := range tokens {
		match := acSpanMatcher(tok)
		for _, p := range d.sortedPaths() {
			f := d.Files[p]
			for _, l := range f.Added {
				if match(l.Text) && !acCommentLine(p, l.Text) {
					return acAnswer(ACUnmet, fmt.Sprintf("the diff changes a line containing `%s` at `%s:%d`", tok, p, l.N), p)
				}
			}
			for _, l := range f.Removed {
				if match(l) && !acCommentLine(p, l) {
					return acAnswer(ACUnmet, fmt.Sprintf("the diff removes a line containing `%s` from `%s`", tok, p), p)
				}
			}
		}
	}
	return acResult{}
}

// acTestFileRE matches a test file by a whole directory name or by its base
// name, so testdata/, specs/ and testing.go are not test files.
var acTestFileRE = regexp.MustCompile(`(?:^|/)(?:tests?|__tests__|spec)/` +
	`|(?:_test|\.test|\.spec)\.[^/]*$` +
	`|(?:^|/)test_[^/]*\.[^/]*$`)

// acDocExtRE matches a document by its extension, the only thing that keeps a
// path from being a test file or test-like. A docs directory does not.
var acDocExtRE = regexp.MustCompile(`(?i)\.(?:md|markdown|rst|adoc|txt)$`)

// acIsTestFile reports whether p is a recognised test file, by its path alone,
// never by its text.
func acIsTestFile(p string) bool {
	return acTestFileRE.MatchString(p) && !acDocExtRE.MatchString(p)
}

// acTestLikeSubstrings is the whole definition of a test-like path, closed by
// decision (#3501): add none. A layout outside these five is not a test.
var acTestLikeSubstrings = []string{"test", "spec", ".cy.", "e2e", "cypress"}

// acIsTestLike reports whether p may be a test file, by its path alone: its
// lower-cased form holds one of acTestLikeSubstrings and it is no document.
func acIsTestLike(p string) bool {
	if acDocExtRE.MatchString(p) {
		return false
	}
	lower := strings.ToLower(p)
	for _, sub := range acTestLikeSubstrings {
		if strings.Contains(lower, sub) {
			return true
		}
	}
	return false
}

// acTestsPreserved answers "`X` tests are untouched" and "`X` tests still
// pass" (#3501) on the test files that contain X, by path or content, at the
// base revision: unmet when the diff modifies, empties, deletes or renames
// one, comments included, and met when it touches none, every candidate was
// read and acTouchesTestLike finds nothing. The base decides, so an edit that
// removes X still counts and an added file never does. No such file, a touched
// test-like one, or any read that is uncertain, is no decision, for which
// acPreserved gives the strict verdict.
func acTestsPreserved(tok string, d *ACDiff) acResult {
	if d.Workdir == "" || d.BaseRev == "" || strings.HasPrefix(d.BaseRev, "-") {
		return acResult{}
	}
	tree, touched, ok := d.baseListing()
	if !ok {
		return acResult{}
	}
	// git grep skips a blob it cannot read and still exits 0 or 1, saying so
	// only on stderr.
	var grepErr bytes.Buffer
	hits, err := acGitList(d.Workdir, &grepErr, "grep", "--no-color", "-l", "-z", "-F", "-e", acGrepLiteral(tok), d.BaseRev)
	if grepErr.Len() > 0 {
		return acResult{}
	}
	var ee *exec.ExitError
	if err != nil && (!errors.As(err, &ee) || ee.ExitCode() != 1) {
		return acResult{} // exit 1 is "no match"; anything else did not answer
	}
	inContent := map[string]bool{}
	for _, h := range hits {
		p, ok := strings.CutPrefix(h, d.BaseRev+":")
		if !ok {
			return acResult{}
		}
		inContent[p] = true
	}
	changed := map[string]*ACFileDiff{}
	for _, p := range d.sortedPaths() {
		f := d.Files[p]
		changed[p] = f
		if f.OldPath != "" {
			changed[f.OldPath] = f
		}
	}

	name, match := acSpanName(tok), acSpanMatcher(tok)
	if name == tok && acNameSpanRE.MatchString(tok) {
		// A bare name is a word here: a `get` test is not one holding "widget".
		match = func(line string) bool { return acNamesWord(line, tok) }
	}
	// Every candidate is read in one batch; an absent one is unread.
	var candidates []string
	candidate := map[string]bool{}
	for _, p := range tree {
		if acIsTestFile(p) && !acNamesWord(p, name) && inContent[p] {
			candidates = append(candidates, p)
			candidate[p] = true
		}
	}
	blobs := acBlobs(d.Workdir, d.BaseRev, candidates)
	kept, unread := "", false
	for _, p := range tree {
		if !acIsTestFile(p) {
			continue
		}
		f := changed[p]
		edited := f != nil || touched[p]
		// git quotes such a path in a diff header, or the header is
		// ambiguous, so whether the diff touches it is unknown.
		unclear := strings.Contains(p, " b/") || strings.ContainsFunc(p, func(r rune) bool {
			return r < 0x20 || r == 0x7f || r == '"' || r == '\\'
		})
		contains := acNamesWord(p, name)
		if candidate[p] {
			data, ok := blobs[p]
			if !ok {
				unread = true
				continue
			}
			for _, line := range strings.Split(string(data), "\n") {
				if match(line) {
					contains = true
					break
				}
			}
		}
		switch {
		case !contains:
		case f != nil:
			action, paths := "modifies `"+p+"`", []string{p}
			switch {
			case f.OldPath == p:
				action = "renames `" + p + "` to `" + f.Path + "`"
				paths = append(paths, f.Path)
			case f.Status == "deleted":
				action = "deletes `" + p + "`"
			}
			return acAnswer(ACUnmet, fmt.Sprintf("the diff %s, an existing test file containing `%s`", action, tok), paths...)
		case edited:
			return acAnswer(ACUnmet, fmt.Sprintf("the diff modifies, deletes or renames `%s`, an existing test file containing `%s`", p, tok), p)
		case unclear:
			unread = true
		case kept == "":
			kept = p
		}
	}
	if unread || kept == "" || acTouchesTestLike(tok, d, tree, touched) {
		return acResult{}
	}
	return acAnswer(ACMet, fmt.Sprintf("the diff touches no existing test file containing `%s`; kept: `%s`", tok, kept))
}

// acTouchesTestLike reports whether the diff edits, empties, deletes or
// renames a base file that is test-like, not a recognised test file and holds
// tok in its path, base content or changed lines, or one for which that cannot
// be read. tree lists the base's files and touched what git says changed.
// Holding is the strict judge's matcher, not a word match, so every line that
// judge would flag in such a file reaches it.
func acTouchesTestLike(tok string, d *ACDiff, tree []string, touched map[string]bool) bool {
	unknownLayout := func(p string) bool { return acIsTestLike(p) && !acIsTestFile(p) }
	name, match := acSpanName(tok), acSpanMatcher(tok)
	inBase := map[string]bool{}
	for _, p := range tree {
		inBase[p] = true
	}
	inHeaders := map[string]bool{}
	for _, f := range d.Files {
		inHeaders[f.Path], inHeaders[f.OldPath] = true, true
		if !unknownLayout(f.Path) && !unknownLayout(f.OldPath) {
			continue
		}
		base := f.Path
		if f.OldPath != "" {
			base = f.OldPath
		}
		if !inBase[base] {
			if f.OldPath != "" {
				return true // a rename whose base path git quoted
			}
			continue // added
		}
		for _, p := range []string{f.Path, f.OldPath} {
			if p != "" && (acNamesWord(p, name) || match(p)) {
				return true
			}
		}
		for _, l := range f.Added {
			if match(l.Text) {
				return true
			}
		}
		// The base content holds every line the diff removes.
		data, ok := acBlob(d.Workdir, d.BaseRev, base)
		if !ok || slices.ContainsFunc(strings.Split(string(data), "\n"), match) {
			return true
		}
	}
	// A path missing from the headers has no changed lines to read.
	for p := range touched {
		if !inHeaders[p] && unknownLayout(p) {
			return true
		}
	}
	return false
}

// acNameSpanRE matches a span that is a bare name.
var acNameSpanRE = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$.-]*$`)

// acNamesWord reports whether s, a path or a line, holds name as whole words:
// at each end it meets the end of s, a character that is no letter or digit,
// or a case change, so "get" is named by "get_test.go" and "getUser" but not
// "widget".
func acNamesWord(s, name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i+len(name) <= len(s); i++ {
		end := i + len(name)
		if s[i:end] == name && acWordBreak(s, i) && acWordBreak(s, end) {
			return true
		}
	}
	return false
}

// acWordBreak reports whether a word can begin or end at s[i].
func acWordBreak(s string, i int) bool {
	if i == 0 || i == len(s) {
		return true
	}
	lower := func(b byte) bool { return b >= 'a' && b <= 'z' }
	upper := func(b byte) bool { return b >= 'A' && b <= 'Z' }
	digit := func(b byte) bool { return b >= '0' && b <= '9' }
	a, b := s[i-1], s[i]
	switch {
	case !lower(a) && !upper(a) && !digit(a), !lower(b) && !upper(b) && !digit(b):
		return true
	case upper(b):
		// "getUser", "v2Client", and the last capital of "HTTPClient".
		return !upper(a) || i+1 < len(s) && lower(s[i+1])
	}
	return false
}

// acChangesOutsideTests reports whether the diff changes a file that is not a
// recognised test and contains tok, by path, before the change or in a line it adds. A changed
// file whose base cannot be read counts, because it might.
func acChangesOutsideTests(tok string, d *ACDiff) bool {
	name, match := acSpanName(tok), acSpanMatcher(tok)
	for _, p := range d.sortedPaths() {
		f := d.Files[p]
		if acIsTestFile(p) {
			continue
		}
		if acNamesWord(p, name) || acNamesWord(f.OldPath, name) {
			return true
		}
		for _, l := range f.Added {
			if match(l.Text) {
				return true
			}
		}
		if f.Status == "added" {
			continue
		}
		base := p
		if f.OldPath != "" {
			base = f.OldPath
		}
		data, ok := acBlob(d.Workdir, d.BaseRev, base)
		if !ok {
			return true
		}
		for _, line := range strings.Split(string(data), "\n") {
			if match(line) {
				return true
			}
		}
	}
	return false
}

// acGitList runs a NUL-delimited git listing in workdir, copying git's stderr
// to stderr when that is not nil.
func acGitList(workdir string, stderr io.Writer, args ...string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), acDiffTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "submodule.recurse=false"}, args...)...)
	cmd.Dir = workdir
	cmd.Stderr = stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return strings.FieldsFunc(string(out), func(r rune) bool { return r == 0 }), nil
}

// acSpanName is the text to search for a span by: a call span's last name, a
// wrapped identifier's name, else the span.
func acSpanName(tok string) string {
	if m := acCallSpanRE.FindStringSubmatch(tok); m != nil {
		return m[1]
	}
	if id := acWrappedIdent(tok); id != "" {
		return id
	}
	return tok
}

// acGrepLiteral is text every line acSpanMatcher(tok) accepts must hold, so a
// fixed-string pre-filter drops none: a key-value span is searched by its
// value, an ellipsis span by its longest fixed part, and a `{ident}`
// placeholder span by its longest run outside the placeholders (#3529).
func acGrepLiteral(tok string) string {
	if acCallSpanRE.MatchString(tok) {
		return acSpanName(tok)
	}
	if acEllipsisRE.MatchString(tok) && !acSpreadRE.MatchString(tok) {
		longest := ""
		for _, part := range acEllipsisRE.Split(tok, -1) {
			if len(part) > len(longest) {
				longest = part
			}
		}
		if strings.TrimSpace(longest) != "" {
			return longest
		}
	}
	if m := acKeyValueRE.FindStringSubmatch(tok); m != nil {
		return m[2]
	}
	if acWrappedIdent(tok) == "" && acBracePlaceholderRE.MatchString(tok) {
		if l := acBraceGrepLiteral(tok); strings.TrimSpace(l) != "" {
			return l
		}
	}
	return acSpanName(tok)
}

// acPresentAnywhere finds tok on the branch: in the named paths first, else
// anywhere in the tree (at ToRev for a range diff, else the working tree).
// err is set when the search could not run, so absence is never inferred
// from a grep that did not answer.
func acPresentAnywhere(tok string, paths []string, d *ACDiff) (string, bool, error) {
	if len(paths) > 0 {
		if loc, found, _ := acPresentToken(tok, paths, d); found {
			return loc, true, nil
		}
	}
	if d.Workdir == "" {
		// Only the diffed files are known: look in each, and answer absent
		// only when every one could be read.
		loc, found, unread := acPresentToken(tok, d.sortedPaths(), d)
		if !found && unread {
			return "", false, errors.New("a diffed file could not be read")
		}
		return loc, found, nil
	}
	lit := acSpanName(tok)
	if d.ToRev != "" {
		return acGitGrepRev(d.Workdir, d.ToRev, "-F", lit, nil)
	}
	return acGitGrep(d.Workdir, "-F", lit)
}

// acCommentLine reports whether line is a comment in path's language, keyed
// on the file extension (#3423). Markdown and plain text have only `<!--`
// comments: a `* ` bullet or a `# ` heading there is content. `#` is a
// comment only in `#`-comment languages, and `//`, `/*` and a `*`
// continuation only in C-family code.
func acCommentLine(path, line string) bool {
	t := strings.TrimSpace(line)
	if t == "" {
		return false
	}
	base := filepath.Base(path)
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".mdx", ".markdown", ".txt", ".rst", ".html", ".htm", ".xml", ".svg", ".vue", ".svelte":
		return strings.HasPrefix(t, "<!--")
	case ".go", ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".mts", ".cts", ".c", ".h", ".cc", ".cpp",
		".hpp", ".java", ".kt", ".kts", ".rs", ".swift", ".dart", ".scala", ".cs", ".css", ".scss", ".less", ".proto":
		return strings.HasPrefix(t, "//") || strings.HasPrefix(t, "/*") ||
			t == "*" || t == "*/" || strings.HasPrefix(t, "* ")
	case ".yml", ".yaml", ".sh", ".bash", ".zsh", ".py", ".toml", ".rb", ".pl", ".r", ".mk", ".cfg", ".ini", ".conf", ".tf":
		return strings.HasPrefix(t, "#")
	}
	switch base {
	case "Makefile", "Dockerfile", ".gitignore", ".gitattributes", ".dockerignore", ".npmignore":
		return strings.HasPrefix(t, "#")
	}
	return false
}

// ---------------------------------------------------------------------------
// Merged-diff judge (#3423)
// ---------------------------------------------------------------------------

// CollectACDiffRange diffs from..to in the repository at workdir: the changes
// one commit range made, with content read at to, never from the working
// tree. A partial-delivery remainder is judged on the earlier PR's merge
// commit against its first parent (`<merge>^1`, `<merge>`). It fails when
// either revision is not a commit in the local object store.
func CollectACDiffRange(ctx context.Context, workdir, from, to string) (*ACDiff, error) {
	if workdir == "" || from == "" || to == "" || strings.HasPrefix(from, "-") || strings.HasPrefix(to, "-") {
		return nil, fmt.Errorf("diff range %q..%q in %q: missing or invalid revision", from, to, workdir)
	}
	ctx, cancel := context.WithTimeout(ctx, acDiffTimeout)
	defer cancel()
	git := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.quotePath=false"}, args...)...)
		cmd.Dir = workdir
		out, err := cmd.Output()
		return string(out), err
	}
	for _, rev := range []string{from, to} {
		if _, err := git("cat-file", "-e", rev+"^{commit}"); err != nil {
			return nil, fmt.Errorf("revision %s is not a commit in %s: %w", rev, workdir, err)
		}
	}
	out, err := git("diff", "--unified=0", "--no-color", "--no-ext-diff",
		"--src-prefix=a/", "--dst-prefix=b/", "-M", from, to)
	if err != nil {
		return nil, fmt.Errorf("git diff %s %s: %w", from, to, err)
	}
	return &ACDiff{
		Files: parseUnifiedDiff(out), Workdir: workdir, Uncommitted: map[string]bool{},
		BaseRev: from, FromRev: from, ToRev: to,
	}, nil
}

// acGitGrepRev is acGitGrep over the tree at rev, limited to paths when any
// are given. It returns the first match as file:line.
func acGitGrepRev(workdir, rev, mode, pat string, paths []string) (string, bool, error) {
	if rev == "" || strings.HasPrefix(rev, "-") {
		return "", false, fmt.Errorf("invalid revision %q", rev)
	}
	ctx, cancel := context.WithTimeout(context.Background(), acDiffTimeout)
	defer cancel()
	args := []string{"-c", "core.quotePath=false", "-c", "submodule.recurse=false", "grep", "-n", "-m", "1", mode, "-e", pat, rev}
	if len(paths) > 0 {
		args = append(append(args, "--"), paths...)
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = workdir
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 && ctx.Err() == nil {
			return "", false, nil
		}
		return "", false, err
	}
	first, _, _ := strings.Cut(string(out), "\n")
	first = strings.TrimPrefix(first, rev+":")
	parts := strings.SplitN(first, ":", 3)
	if len(parts) < 2 {
		return "", false, fmt.Errorf("unparsed git grep output %q", first)
	}
	return parts[0] + ":" + parts[1], true, nil
}

// VerifyACOnMerge judges one remaining criterion against a merged diff from
// CollectACDiffRange (#3423). Only two rules can answer met: a preservation
// (the diff changed no non-comment line containing the span, and the span
// exists at the merge; when the span's tests are the one thing kept, the diff
// touched no test file that contained it at the first parent, or, if that
// cannot be told or it touched a test-like file of another layout that holds
// it, the span's own line and presence check decides) and a removal (the span
// existed at the first parent and is absent at the merge). Every other
// outcome — a diff with no files, a clause that is neither, a clause naming no
// span, a span on neither side, a read or grep error in the deciding check —
// is unmet, with evidence beginning "undecidable:". met is never inferred
// from a missing answer.
func VerifyACOnMerge(text string, d *ACDiff) (met bool, evidence string) {
	if d == nil || d.FromRev == "" || d.ToRev == "" || d.Workdir == "" {
		return false, "undecidable: no merged diff to judge against"
	}
	if len(d.Files) == 0 {
		return false, "undecidable: the merged diff has no files to judge"
	}
	prose := acStripCodeSpans(text)
	for _, r := range acRuntimeSteps {
		if r.re.MatchString(prose) {
			return false, "undecidable: needs " + r.step
		}
	}
	var ev []string
	for _, c := range parseACClauses(text) {
		if strings.TrimSpace(c.Raw) == "" {
			continue
		}
		ok, e := acJudgeOnMerge(c, d)
		if !ok {
			return false, e
		}
		ev = append(ev, e)
	}
	if len(ev) == 0 {
		return false, "undecidable: the criterion names nothing to judge"
	}
	return true, strings.Join(ev, "; ")
}

// acJudgeOnMerge judges one clause of a remaining criterion on a merged diff.
func acJudgeOnMerge(c acClause, d *ACDiff) (bool, string) {
	if preflight.ACAsksAddition(c.Raw) {
		return false, "undecidable: asks for an addition, which only a new change can deliver"
	}
	switch {
	case acIsPreservation(c):
		r := acPreserved(c, d)
		switch {
		case !r.ok:
			return false, fmt.Sprintf("undecidable: %s could not be found at %s", acList(c.Tokens, 3), ShortRev(d.ToRev))
		case r.status == ACUnmet:
			return false, r.evidence
		}
		return true, r.evidence
	case preflight.ACAsksPreservation(c.Raw) && len(c.Paths) > 0 && len(c.Headings) == 0 && len(c.Globs) == 0:
		return acPathsPreservedOnMerge(c, d)
	case preflight.ACAsksRemoval(c.Raw):
		return acRemovedOnMerge(c, d)
	}
	return false, "undecidable: neither a preservation nor a removal"
}

// acPathsPreservedOnMerge answers "`p` is unchanged" on a merged diff: met
// when the diff does not touch p and p exists at the merge.
func acPathsPreservedOnMerge(c acClause, d *ACDiff) (bool, string) {
	for _, p := range c.Paths {
		if _, changed := d.Files[p]; changed {
			return false, fmt.Sprintf("`%s` changes in %s", p, ShortRev(d.ToRev))
		}
		if _, ok := acBlob(d.Workdir, d.ToRev, p); !ok {
			return false, fmt.Sprintf("undecidable: `%s` does not exist at %s", p, ShortRev(d.ToRev))
		}
	}
	return true, fmt.Sprintf("%s untouched by %s", acList(c.Paths, 3), ShortRev(d.ToRev))
}

// acRemovedOnMerge answers a removal on a merged diff: met only when the span
// existed at the first parent and is absent at the merge. A span that never
// existed is not removed — it is undecidable, so unmet.
func acRemovedOnMerge(c acClause, d *ACDiff) (bool, string) {
	span := preflight.ACRemovalSpan(c.Raw)
	if span == "" {
		return false, "undecidable: no removal span"
	}
	mode, pat, shown := "-F", span, span
	if name, re, ok := acRemovalTarget(c); ok {
		mode, pat, shown = "-E", re.String(), name
	}
	from, found, err := acGitGrepRev(d.Workdir, d.FromRev, mode, pat, c.Paths)
	if err != nil {
		return false, fmt.Sprintf("undecidable: could not search %s: %v", ShortRev(d.FromRev), err)
	}
	if !found {
		return false, fmt.Sprintf("undecidable: `%s` did not exist at %s, so the merge cannot have removed it", shown, ShortRev(d.FromRev))
	}
	to, found, err := acGitGrepRev(d.Workdir, d.ToRev, mode, pat, c.Paths)
	if err != nil {
		return false, fmt.Sprintf("undecidable: could not search %s: %v", ShortRev(d.ToRev), err)
	}
	if found {
		return false, fmt.Sprintf("`%s` is still at `%s` in %s", shown, to, ShortRev(d.ToRev))
	}
	return true, fmt.Sprintf("`%s` at `%s` before the merge; absent at %s", shown, from, ShortRev(d.ToRev))
}

// ShortRev shortens a full commit id for evidence to 12 characters;
// anything else is kept.
func ShortRev(rev string) string {
	if len(rev) > 12 && acRevisionRE.MatchString(rev) {
		return rev[:12]
	}
	return rev
}

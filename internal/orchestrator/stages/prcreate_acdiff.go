package stages

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/ci"
	"github.com/nightgauge/nightgauge/internal/preflight"
	"github.com/nightgauge/nightgauge/internal/reclaim"
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

	content map[string]acContent
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
	out, err := git("diff", "--unified=0", "--no-color", "--no-ext-diff", "-M", mergeBase)
	if err != nil {
		return nil, fmt.Errorf("git diff %s: %w", mergeBase, err)
	}
	d := &ACDiff{Files: parseUnifiedDiff(out), Workdir: workdir, Uncommitted: map[string]bool{}}
	dirty, err := git("diff", "--name-only", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("git diff --name-only HEAD: %w", err)
	}
	for _, p := range strings.Split(strings.TrimSpace(dirty), "\n") {
		if p != "" {
			d.Uncommitted[p] = true
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
	return d, nil
}

var acHunkRE = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// parseUnifiedDiff reads `git diff --unified=0` output into per-file changes.
func parseUnifiedDiff(out string) map[string]*ACFileDiff {
	files := map[string]*ACFileDiff{}
	var cur *ACFileDiff
	inHunk := false
	next := 0
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			cur = &ACFileDiff{Status: "modified"}
			inHunk = false
			if i := strings.LastIndex(line, " b/"); i >= 0 {
				cur.Path = line[i+3:]
				files[cur.Path] = cur
			}
		case cur == nil:
		case !inHunk && strings.HasPrefix(line, "new file mode"):
			cur.Status = "added"
		case !inHunk && strings.HasPrefix(line, "deleted file mode"):
			cur.Status = "deleted"
		case !inHunk && strings.HasPrefix(line, "rename to "):
			cur.Status = "renamed"
		case !inHunk && strings.HasPrefix(line, "+++ "):
			if p := strings.TrimPrefix(line, "+++ "); strings.HasPrefix(p, "b/") && p[2:] != cur.Path {
				delete(files, cur.Path)
				cur.Path = p[2:]
				files[cur.Path] = cur
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
	return files
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
	if d.Workdir != "" {
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
	Tokens   []string // code spans that are not paths or headings, plus #NNN refs
	// Bounds are code-span values the prose names as a bound ("a version
	// above `1.26.0`"): a comparison, not text the diff must add (#2941).
	Bounds []acBound
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

func parseACClause(raw string) acClause {
	c := acClause{Raw: raw, Prose: acStripCodeSpans(raw)}
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
	for _, m := range acProsePathRE.FindAllStringSubmatch(c.Prose, -1) {
		add(&c.Paths, m[1])
	}
	for _, m := range acIssueRefRE.FindAllStringSubmatch(c.Prose, -1) {
		add(&c.Tokens, m[1])
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
	for _, raw := range splitACClauses(text) {
		r := verifyACClause(parseACClause(raw), inherited, d, tests)
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
	switch {
	case acScopeRE.MatchString(c.Prose) && len(c.Paths) > 0:
		return acScope(c, d)
	case acUnchangedAboutTarget(c.Raw):
		return acUnchanged(c, d)
	case acChangelogRE.MatchString(c.Prose) && len(c.Paths) == 0 && len(c.Headings) > 0:
		return acChangelog(c, d)
	}
	if pattern, ok := acRunPattern(c); ok {
		return acRunTest(pattern, d, tests)
	}
	if name, re, ok := acTestName(c); ok {
		return acTest(name, re, d, tests)
	}
	named := acDiffedPaths(c, d)
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
	return r
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
		var hits []string
		for _, q := range d.sortedPaths() {
			if filepath.Base(q) == p {
				hits = append(hits, q)
			}
		}
		if len(hits) == 1 {
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
	for _, p := range d.sortedPaths() {
		for _, l := range d.Files[p].Added {
			if re.MatchString(l.Text) {
				return fmt.Sprintf("%s:%d", p, l.N), true
			}
		}
	}
	if d.Workdir == "" {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), acDiffTimeout)
	defer cancel()
	// submodule.recurse=false: a user config enabling it makes git grep
	// reject --untracked.
	cmd := exec.CommandContext(ctx, "git", "-c", "core.quotePath=false", "-c", "submodule.recurse=false", "grep", "-n", "-E", "--untracked", "-e", re.String())
	cmd.Dir = d.Workdir
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	first, _, _ := strings.Cut(string(out), "\n")
	parts := strings.SplitN(first, ":", 3)
	if len(parts) < 2 {
		return "", false
	}
	return parts[0] + ":" + parts[1], true
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
	skipped := false
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
		ev := fmt.Sprintf("`%s:%d` adds %d line(s) under %s", p, sec[0].N, len(sec), acHeadingList(c.Headings, true))
		if len(c.Tokens) > 0 {
			ev += " citing " + acList(c.Tokens, 5)
		}
		return acAnswer(ACMet, ev, p)
	}
	if skipped || len(tried) == 0 {
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
	if len(c.Tokens) == 0 && len(c.Bounds) == 0 {
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
			skipped = skipped || unread
		}
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
	if patterns || (added == 0 && !decided) {
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
// (#3154). Any other span matches as a literal substring.
func acSpanMatcher(tok string) func(line string) bool {
	m := acKeyValueRE.FindStringSubmatch(tok)
	if m == nil {
		if c := acCallSpanRE.FindStringSubmatch(tok); c != nil {
			return regexp.MustCompile(`(?:^|[^A-Za-z0-9_$])` + regexp.QuoteMeta(c[1]) +
				`\s*(?:<[^>]*>)?\s*\(`).MatchString
		}
		if id := acWrappedIdent(tok); id != "" {
			return regexp.MustCompile(`(?:^|[^A-Za-z0-9_$])` + regexp.QuoteMeta(id) +
				`(?:[^A-Za-z0-9_$]|$)`).MatchString
		}
		return func(line string) bool { return strings.Contains(line, tok) }
	}
	re := regexp.MustCompile(`(?:^|[^A-Za-z0-9_])["']?(?i:` + regexp.QuoteMeta(m[1]) +
		`)["']?\s*[:=]\s*["']?` + regexp.QuoteMeta(m[2]) + `(?:["']|[^A-Za-z0-9_]|$)`)
	return re.MatchString
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
			if acIsPath(tok) || acHeadingTokRE.MatchString(tok) {
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
		for _, l := range d.Files[p].Added {
			if match(l.Text) {
				return fmt.Sprintf("%s:%d", p, l.N), true
			}
		}
	}
	return "", false
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

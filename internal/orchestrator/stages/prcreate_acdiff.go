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

// ACTestState is the stage's test run as pr-create can see it. Per-test
// results are not recorded, so a named test "passed" when the run did.
type ACTestState struct {
	Passed bool
	Source string // e.g. "feature-validate unit tests"
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
}

var (
	acCodeSpanRE    = regexp.MustCompile("`([^`]+)`")
	acHeadingTokRE  = regexp.MustCompile(`^(#{1,6})\s+(\S.*)$`)
	acPathLikeRE    = regexp.MustCompile(`^[A-Za-z0-9_.\-/]+$`)
	acPathExtRE     = regexp.MustCompile(`\.[A-Za-z][A-Za-z0-9]{0,5}$`)
	acProsePathRE   = regexp.MustCompile(`(?:^|[\s(])((?:[A-Za-z0-9_.\-]+/)*[A-Za-z0-9_.\-]+\.(?:md|mdx|ts|tsx|js|jsx|mjs|json|sh|ya?ml|toml|go|py|rs|txt))\b`)
	acIssueRefRE    = regexp.MustCompile(`(?:^|[^\w&])(#\d+)\b`)
	acExistingRE    = regexp.MustCompile(`(?i)\bexisting\s*$`)
	acMdHeadingRE   = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*#*\s*$`)
	acClauseSplitRE = regexp.MustCompile(`^(?:,\s+and\s+|;\s+)`)
)

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

// splitACClauses splits on ", and" and ";" outside code spans, so each
// predicate of a compound criterion is checked on its own.
func splitACClauses(text string) []string {
	var out []string
	start, inCode := 0, false
	for i := 0; i < len(text); i++ {
		if text[i] == '`' {
			inCode = !inCode
			continue
		}
		if inCode {
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
	c := acClause{Raw: raw, Prose: acCodeSpanRE.ReplaceAllString(raw, " ")}
	seen := map[string]bool{}
	add := func(dst *[]string, s string) {
		if !seen[s] {
			seen[s] = true
			*dst = append(*dst, s)
		}
	}
	for _, m := range acCodeSpanRE.FindAllStringSubmatchIndex(raw, -1) {
		tok := strings.TrimSpace(raw[m[2]:m[3]])
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
		default:
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
	prose := acCodeSpanRE.ReplaceAllString(text, " ")
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
	case acUnchangedRE.MatchString(c.Prose):
		return acUnchanged(c, d)
	}
	if name, re, ok := acTestName(c); ok {
		return acTest(name, re, d, tests)
	}
	diffed := acDiffedPaths(c, d)
	fromInherited := len(diffed) == 0 && len(inherited) > 0
	if fromInherited {
		diffed = inherited
	}
	var r acResult
	if len(diffed) > 0 && len(c.Headings) > 0 {
		r = acSection(c, diffed, d)
	} else {
		r = acTokens(c, diffed, d)
	}
	if fromInherited && r.status == ACUnmet {
		return acResult{}
	}
	return r
}

func acDiffedPaths(c acClause, d *ACDiff) []string {
	var out []string
	for _, p := range c.Paths {
		if _, ok := d.Files[p]; ok {
			out = append(out, p)
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
	// Every named file is tried; unmet only when some file was read and none
	// satisfies. An unreadable file is skipped, and with none read the clause
	// is not diff-checkable (#2669).
	var reasons, tried []string
	for _, p := range diffed {
		f := d.Files[p]
		content, ok := d.lines(p)
		if !ok {
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
		if missing := acMissingTokens(c.Tokens, sec); len(missing) > 0 {
			reasons = append(reasons, fmt.Sprintf("lines `%s` adds under %s do not mention %s", p, acHeadingList(c.Headings, true), acList(missing, 5)))
			continue
		}
		ev := fmt.Sprintf("`%s:%d` adds %d line(s) under %s", p, sec[0].N, len(sec), acHeadingList(c.Headings, true))
		if len(c.Tokens) > 0 {
			ev += " citing " + acList(c.Tokens, 5)
		}
		return acAnswer(ACMet, ev, p)
	}
	if len(tried) == 0 {
		return acResult{}
	}
	return acAnswer(ACUnmet, strings.Join(reasons, "; "), tried...)
}

// acTokens: every named token appears in the added lines of the named (or
// inherited) diffed files. A clause with no file is not diff-checkable: a
// token found anywhere in the diff proves nothing about this criterion.
func acTokens(c acClause, diffed []string, d *ACDiff) acResult {
	if len(c.Tokens) == 0 || len(diffed) == 0 {
		return acResult{}
	}
	var lines []acDiffLine
	var where []string
	for _, p := range diffed {
		for _, l := range d.Files[p].Added {
			lines = append(lines, l)
			where = append(where, p)
		}
	}
	missing := acMissingTokens(c.Tokens, lines)
	if len(missing) > 0 {
		return acAnswer(ACUnmet, fmt.Sprintf("the %s diff does not add %s", acList(diffed, 3), acList(missing, 5)), diffed...)
	}
	var ev, paths []string
	for _, tok := range c.Tokens {
		for i, l := range lines {
			if strings.Contains(l.Text, tok) {
				ev = append(ev, fmt.Sprintf("`%s` at `%s:%d`", tok, where[i], l.N))
				paths = append(paths, where[i])
				break
			}
		}
	}
	return acAnswer(ACMet, "the diff adds "+strings.Join(ev, ", "), paths...)
}

func acMissingTokens(tokens []string, lines []acDiffLine) []string {
	var missing []string
	for _, tok := range tokens {
		found := false
		for _, l := range lines {
			if strings.Contains(l.Text, tok) {
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

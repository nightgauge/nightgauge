package stages

import (
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
}

// ACDiffChecker answers one criterion; ok=false means it cannot.
type ACDiffChecker func(text string) (ACCheck, bool)

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

	content map[string][]string
}

// acDiffTimeout bounds every git call the verifier makes.
const acDiffTimeout = 20 * time.Second

// acUntrackedMaxBytes skips untracked files too large to be a hand-written
// deliverable (generated output, fixtures).
const acUntrackedMaxBytes = 1 << 20

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
	d := &ACDiff{Files: parseUnifiedDiff(out), Workdir: workdir}

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
		data, readErr := os.ReadFile(filepath.Join(workdir, p))
		if readErr != nil || len(data) > acUntrackedMaxBytes || strings.ContainsRune(string(data), 0) {
			continue
		}
		f := &ACFileDiff{Path: p, Status: "added"}
		for i, l := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
			f.Added = append(f.Added, acDiffLine{N: i + 1, Text: l})
		}
		d.Files[p] = f
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
	return func(text string) (ACCheck, bool) { return VerifyACAgainstDiff(text, d, tests) }
}

func (d *ACDiff) lines(path string) []string {
	if d.content == nil {
		d.content = map[string][]string{}
	}
	if l, ok := d.content[path]; ok {
		return l
	}
	var l []string
	if d.Workdir != "" {
		if data, err := os.ReadFile(filepath.Join(d.Workdir, path)); err == nil {
			l = strings.Split(string(data), "\n")
		}
	}
	d.content[path] = l
	return l
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
	acCodeSpanRE    = regexp.MustCompile("`([^`]+)`")
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
	{regexp.MustCompile(`(?i)\bmanual(?:ly)?\b|\boperator\b|\bby hand\b|\bhuman\b|\bsign[- ]off\b`), "a manual or operator step"},
	{regexp.MustCompile(`(?i)\bcredentials?\b|\bAPI key\b|\brotat(?:e|ed|ion)\b`), "a credential"},
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
// ok=false means no clause was diff-checkable.
func VerifyACAgainstDiff(text string, d *ACDiff, tests ACTestState) (ACCheck, bool) {
	if d == nil || len(d.Files) == 0 {
		return ACCheck{}, false
	}
	prose := acCodeSpanRE.ReplaceAllString(text, " ")
	for _, r := range acRuntimeSteps {
		if r.re.MatchString(prose) {
			return ACCheck{Status: ACNotVerifiable, Step: r.step, Evidence: "needs " + r.step}, true
		}
	}
	var met, unmet []string
	for _, raw := range splitACClauses(text) {
		c := parseACClause(raw)
		status, evidence, ok := verifyACClause(c, d, tests)
		if !ok {
			continue
		}
		if status == ACUnmet {
			unmet = append(unmet, evidence)
		} else {
			met = append(met, evidence)
		}
	}
	switch {
	case len(unmet) > 0:
		return ACCheck{Status: ACUnmet, Evidence: strings.Join(unmet, "; ")}, true
	case len(met) > 0:
		return ACCheck{Status: ACMet, Evidence: strings.Join(met, "; ")}, true
	}
	return ACCheck{}, false
}

func verifyACClause(c acClause, d *ACDiff, tests ACTestState) (string, string, bool) {
	switch {
	case acScopeRE.MatchString(c.Prose) && len(c.Paths) > 0:
		return acScope(c, d)
	case acUnchangedRE.MatchString(c.Prose):
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
	diffed := acDiffedPaths(c, d)
	if len(diffed) > 0 && len(c.Headings) > 0 {
		return acSection(c, diffed, d)
	}
	return acTokens(c, diffed, d)
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
func acScope(c acClause, d *ACDiff) (string, string, bool) {
	var extra []string
	for _, p := range d.sortedPaths() {
		if !acPathAllowed(p, c.Paths) {
			extra = append(extra, p)
		}
	}
	if len(extra) > 0 {
		return ACUnmet, "the diff also changes " + acList(extra, 5), true
	}
	return ACMet, "the diff changes only " + acList(d.sortedPaths(), 5), true
}

// acUnchanged: a named path must be absent from the diff, or its diff must
// delete nothing. Headings narrow the check to the diffed files holding them;
// a heading not qualified by "existing" must also gain no lines.
func acUnchanged(c acClause, d *ACDiff) (string, string, bool) {
	if len(c.Paths) > 0 {
		var ev []string
		for _, p := range c.Paths {
			f, ok := d.Files[p]
			switch {
			case !ok:
				ev = append(ev, "`"+p+"` is not in the diff")
			case f.Deleted > 0 || f.Status == "deleted" || f.Status == "renamed":
				return ACUnmet, fmt.Sprintf("`%s` is %s with %d line(s) removed or rewritten", p, f.Status, f.Deleted), true
			case len(c.Headings) > 0:
				if status, e := acHeadingsUnchanged(c.Headings, f, d); status == ACUnmet {
					return ACUnmet, e, true
				}
				ev = append(ev, "`"+p+"` removes no lines and adds none under "+acHeadingList(c.Headings, false))
			default:
				return ACUnmet, fmt.Sprintf("`%s` changes in the diff", p), true
			}
		}
		return ACMet, strings.Join(ev, "; "), true
	}
	if len(c.Headings) == 0 {
		return "", "", false
	}
	var ev []string
	for _, p := range d.sortedPaths() {
		f := d.Files[p]
		if !acFileHasHeadings(d.lines(p), c.Headings) {
			continue
		}
		if f.Deleted > 0 {
			return ACUnmet, fmt.Sprintf("`%s` removes or rewrites %d line(s)", p, f.Deleted), true
		}
		if status, e := acHeadingsUnchanged(c.Headings, f, d); status == ACUnmet {
			return ACUnmet, e, true
		}
		ev = append(ev, fmt.Sprintf("`%s` is a pure insertion that adds nothing under %s", p, acHeadingList(c.Headings, false)))
	}
	if len(ev) == 0 {
		return "", "", false
	}
	return ACMet, strings.Join(ev, "; "), true
}

func acHeadingsUnchanged(hs []acHeading, f *ACFileDiff, d *ACDiff) (string, string) {
	content := d.lines(f.Path)
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
				return ACUnmet, fmt.Sprintf("`%s:%d` adds a line under %s", f.Path, l.N, h)
			}
		}
	}
	return ACMet, ""
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
func acChangelog(c acClause, d *ACDiff) (string, string, bool) {
	const path = "CHANGELOG.md"
	if _, ok := d.Files[path]; !ok {
		return ACUnmet, "`" + path + "` is not in the diff", true
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
func acRunTest(pattern string, d *ACDiff, tests ACTestState) (string, string, bool) {
	top, _, _ := strings.Cut(pattern, "/")
	re, err := regexp.Compile(top)
	if err != nil || top == "" {
		return "", "", false
	}
	name, loc, ok := d.findGoTest(re)
	if !ok {
		return ACUnmet, fmt.Sprintf("`-run` pattern `%s` matches no test defined on the branch", pattern), true
	}
	if passed, known := tests.named(name); known {
		if !passed {
			return ACUnmet, fmt.Sprintf("`-run` pattern `%s` matches `%s` at `%s`, which failed in %s", pattern, name, loc, tests.logSource()), true
		}
		return ACMet, fmt.Sprintf("`-run` pattern `%s` matches `%s` at `%s`, which passed in %s", pattern, name, loc, tests.logSource()), true
	}
	if !tests.Passed {
		src := tests.Source
		if src == "" {
			src = "no recorded test run"
		}
		return ACUnmet, fmt.Sprintf("`-run` pattern `%s` matches `%s` at `%s`, but the test run did not pass (%s)", pattern, name, loc, src), true
	}
	return ACMet, fmt.Sprintf("`-run` pattern `%s` matches `%s` at `%s`, passed in %s", pattern, name, loc, tests.Source), true
}

// findGoTest finds a Go test definition whose name re matches: in added
// lines, in the branch content of diffed test files, then via git grep.
func (d *ACDiff) findGoTest(re *regexp.Regexp) (string, string, bool) {
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
				return n, fmt.Sprintf("%s:%d", p, l.N), true
			}
		}
	}
	for _, p := range paths {
		if !strings.HasSuffix(p, "_test.go") {
			continue
		}
		for i, l := range d.lines(p) {
			if n, ok := hit(l); ok {
				return n, fmt.Sprintf("%s:%d", p, i+1), true
			}
		}
	}
	if d.Workdir == "" {
		return "", "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), acDiffTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-c", "core.quotePath=false", "-c", "submodule.recurse=false", "grep", "-n", "-E", "--untracked", "-e", `^func Test[A-Za-z0-9_]*\(`, "--", "*_test.go")
	cmd.Dir = d.Workdir
	out, err := cmd.Output()
	if err != nil {
		return "", "", false
	}
	for _, line := range strings.Split(string(out), "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) < 3 {
			continue
		}
		if n, ok := hit(parts[2]); ok {
			return n, parts[0] + ":" + parts[1], true
		}
	}
	return "", "", false
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
func acTest(name string, re *regexp.Regexp, d *ACDiff, tests ACTestState) (string, string, bool) {
	loc, ok := d.findDefinition(re)
	if !ok {
		return ACUnmet, fmt.Sprintf("test `%s` is not defined on the branch", name), true
	}
	if passed, known := tests.named(name); known {
		if !passed {
			return ACUnmet, fmt.Sprintf("test `%s` is defined at `%s`, but %s records `--- FAIL: %s`", name, loc, tests.logSource(), name), true
		}
		return ACMet, fmt.Sprintf("test `%s` at `%s` passed in %s (`--- PASS: %s`)", name, loc, tests.logSource(), name), true
	}
	if !tests.Passed {
		src := tests.Source
		if src == "" {
			src = "no recorded test run"
		}
		return ACUnmet, fmt.Sprintf("test `%s` is defined at `%s`, but the test run did not pass (%s)", name, loc, src), true
	}
	return ACMet, fmt.Sprintf("test `%s` at `%s` passed in %s", name, loc, tests.Source), true
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
func acSection(c acClause, diffed []string, d *ACDiff) (string, string, bool) {
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
	for _, p := range diffed {
		f := d.Files[p]
		content := d.lines(p)
		var sec []acDiffLine
		for _, l := range f.Added {
			if strings.TrimSpace(l.Text) != "" && under(acHeadingStack(content, l.N)) {
				sec = append(sec, l)
			}
		}
		if len(sec) == 0 {
			continue
		}
		if missing := acMissingTokens(c.Tokens, sec); len(missing) > 0 {
			return ACUnmet, fmt.Sprintf("lines `%s` adds under %s do not mention %s", p, acHeadingList(c.Headings, true), acList(missing, 5)), true
		}
		ev := fmt.Sprintf("`%s:%d` adds %d line(s) under %s", p, sec[0].N, len(sec), acHeadingList(c.Headings, true))
		if len(c.Tokens) > 0 {
			ev += " citing " + acList(c.Tokens, 5)
		}
		return ACMet, ev, true
	}
	return ACUnmet, fmt.Sprintf("%s adds no line under %s", acList(diffed, 3), acHeadingList(c.Headings, true)), true
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
	if len(scope) == 0 {
		return changelogs
	}
	return scope
}

// acTokens: every named token appears in the added lines (of the named
// diffed files, when any), or in their post-change content provided the diff
// adds at least one of the tokens. A clause whose tokens all predate the diff
// is undecided.
//
// A named file is a place the change applies, not the only file that may hold
// the tokens (#2941): a token the named files lack is looked for in the rest of
// the diff (changelogs aside) before it counts as missing, and a
// package-qualified symbol is found where its package's diff changes it. Only
// a token the whole diff lacks is unmet, and only when the clause names a
// diffed file; otherwise the clause may be about something else. Bounds are
// compared against the values the named files' diff adds.
func acTokens(c acClause, diffed []string, d *ACDiff) (string, string, bool) {
	if len(c.Tokens) == 0 && len(c.Bounds) == 0 {
		return "", "", false
	}
	var ev, missing []string
	decided := false
	if len(c.Bounds) > 0 && len(diffed) > 0 {
		for _, b := range c.Bounds {
			loc, val, ok, paired := acBoundAdded(b, diffed, d)
			if !paired {
				continue
			}
			if !ok {
				return ACUnmet, fmt.Sprintf("no value the %s diff changes is %s `%s`", acList(diffed, 3), b.Word, b.Value), true
			}
			ev = append(ev, fmt.Sprintf("adds `%s` at `%s`, %s `%s`", val, loc, b.Word, b.Value))
			decided = true
		}
	}
	scope := diffed
	if len(scope) == 0 {
		scope = acTokenScope(c, d)
	}
	wide := acWideScope(c, d)
	added := 0
	for _, tok := range c.Tokens {
		if len(scope) == 0 {
			break
		}
		if loc, ok := acAddedToken(tok, scope, d); ok {
			ev = append(ev, fmt.Sprintf("adds `%s` at `%s`", tok, loc))
			added++
		} else if loc, ok := acPresentToken(tok, scope, d); ok {
			ev = append(ev, fmt.Sprintf("keeps `%s` already at `%s`", tok, loc))
		} else if loc, ok := acSymbolPlace(tok, d); ok {
			ev = append(ev, fmt.Sprintf("changes `%s` in `%s`", tok, loc))
			added++
		} else if loc, ok := acAddedToken(tok, wide, d); ok && len(diffed) > 0 {
			ev = append(ev, fmt.Sprintf("adds `%s` at `%s`", tok, loc))
			added++
		} else {
			missing = append(missing, tok)
		}
	}
	if len(missing) > 0 {
		if len(diffed) == 0 {
			return "", "", false
		}
		return ACUnmet, fmt.Sprintf("the diff does not add %s (looked in %s and every other changed file)", acList(missing, 5), acList(diffed, 3)), true
	}
	if added == 0 && !decided {
		return "", "", false
	}
	return ACMet, "the diff " + strings.Join(ev, ", "), true
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

func acAddedToken(tok string, scope []string, d *ACDiff) (string, bool) {
	for _, p := range scope {
		for _, l := range d.Files[p].Added {
			if strings.Contains(l.Text, tok) {
				return fmt.Sprintf("%s:%d", p, l.N), true
			}
		}
	}
	return "", false
}

func acPresentToken(tok string, scope []string, d *ACDiff) (string, bool) {
	for _, p := range scope {
		for i, l := range d.lines(p) {
			if strings.Contains(l, tok) {
				return fmt.Sprintf("%s:%d", p, i+1), true
			}
		}
	}
	return "", false
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

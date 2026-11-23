package hooks

import (
	"reflect"
	"strings"
	"testing"
)

func TestPRCriteriaVerdicts(t *testing.T) {
	body := "## Acceptance criteria\n\n- ✅ met: a thing (with parens) (diff: x.go)\n- not a met line\n\n" +
		"## ⚠️ Unmet acceptance criteria\n\nThis PR leaves #1 open.\n\n" +
		"- measure later\n  _❔ not verifiable — needs: runs_\n- broken\n  _❌ unmet — test: red_\n" +
		"- unjudged\n  _❔ not verified_\n- bare\n\n## Other\n\n- ignored\n"
	got := prCriteriaVerdicts(body)
	want := []prVerdict{
		{"a thing (with parens) (diff: x.go)", verdictMet},
		{"measure later", verdictNotVerifiable},
		{"broken", verdictUnmet},
		{"unjudged", verdictNotVerified},
		{"bare", verdictUnmet},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestIssueCriteria(t *testing.T) {
	body := "Intro\n\n- plain outside section\n\n## Acceptance Criteria\n\n1. numbered one\n2) numbered two\n- plain\n* star\n- [x] boxed\n" +
		"```\n1. fenced\n```\n<details>\n1. hidden\n</details>\n  1. nested ignored\n\n" +
		"## Notes\n\n1. not a criterion\n\n### Acceptance criteria (Part B)\n\n+ part b\n"
	var texts []string
	for _, c := range issueCriteria(body) {
		texts = append(texts, c.Text)
	}
	want := []string{"numbered one", "numbered two", "plain", "star", "boxed", "part b"}
	if !reflect.DeepEqual(texts, want) {
		t.Errorf("got %q, want %q", texts, want)
	}
}

func TestCriterionMatches(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"second", "second", true},
		{"Use `foo` **now**.", "use foo now", true},
		{"short one", "short one plus more", false}, // under the guard
		{"a criterion long enough", "a criterion long enough (diff: x)", true},
		{"totally different text here", "another criterion text here", false},
	}
	for _, c := range cases {
		if got := criterionMatches(c.a, c.b); got != c.want {
			t.Errorf("criterionMatches(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestComputeRemainder(t *testing.T) {
	issue := "## Acceptance criteria\n\n1. delivered criterion text\n- [ ] delivered criterion text\n- [x] ticked but unmet criterion\n- [x] ticked untouched\n- [ ] open untouched\n"
	pr := "## Acceptance criteria\n\n- ✅ met: delivered criterion text (diff: a)\n\n" +
		"## ⚠️ Unmet acceptance criteria\n\n- ticked but unmet criterion\n  _❌ unmet — x_\n- never in the issue\n"
	got := computeRemainder(issue, pr, nil)
	if !reflect.DeepEqual(got.TickIndices, []int{1}) {
		t.Errorf("TickIndices = %v, want [1]", got.TickIndices)
	}
	want := []string{"ticked but unmet criterion", "open untouched", "never in the issue"}
	if !reflect.DeepEqual(got.Remaining, want) || got.NotVerifiableOnly || !got.Found {
		t.Errorf("got %+v, want Remaining %q", got, want)
	}
}

func TestComputeRemainderDeduplicatesAndNotVerifiableOnly(t *testing.T) {
	issue := "## Acceptance criteria\n\n1. measure the median after merge\n- [ ] measure the median after merge\n- [ ] **Owner** signs off\n"
	pr := "## ⚠️ Unmet acceptance criteria\n\n- measure the median after merge\n  _❔ not verifiable — needs: runs_\n"
	got := computeRemainder(issue, pr, nil)
	if !reflect.DeepEqual(got.Remaining, []string{"measure the median after merge", "**Owner** signs off"}) || !got.NotVerifiableOnly {
		t.Errorf("got %+v", got)
	}
	if computeRemainder("no criteria", "", nil).Found {
		t.Error("Found = true for a body with no criteria")
	}
}

func TestComputeRemainderTickNeedsWholeCriterion(t *testing.T) {
	issue := "- [ ] The API rejects requests without a valid token and logs it\n- [x] already ticked criterion text\n"
	pr := "## Acceptance criteria\n\n- ✅ met: The API rejects requests without (diff: a)\n- ✅ met: already ticked criterion text (diff: b)\n"
	got := computeRemainder(issue, pr, nil)
	if len(got.TickIndices) != 0 || len(got.Remaining) != 1 {
		t.Errorf("got %+v, want no ticks and the partial claim's criterion left", got)
	}
}

func TestComputeRemainderNotVerifiedIsActionable(t *testing.T) {
	pr := "## ⚠️ Unmet acceptance criteria\n\n- judge me later please\n  _❔ not verified_\n"
	if got := computeRemainder("", pr, nil); got.NotVerifiableOnly || len(got.Remaining) != 1 {
		t.Errorf("got %+v, want one actionable criterion", got)
	}
}

func TestComputeRemainderShortMetCriterionTicks(t *testing.T) {
	issue := "- [ ] Docs updated\n"
	pr := "## Acceptance criteria\n\n- ✅ met: Docs updated (diff: docs/X.md:3)\n"
	got := computeRemainder(issue, pr, nil)
	if len(got.TickIndices) != 1 || len(got.Remaining) != 0 {
		t.Errorf("got %+v, want the short criterion ticked and nothing left", got)
	}
}

func TestComputeRemainderPrefersWholeMatchOverPrefix(t *testing.T) {
	issue := "- [ ] The hook ticks the met criterion\n- [ ] The hook ticks the met criterion and logs the change\n"
	pr := "## ⚠️ Unmet acceptance criteria\n\n- The hook ticks the met criterion and logs the change\n  _❌ unmet_\n\n" +
		"## Acceptance criteria\n\n- ✅ met: The hook ticks the met criterion (diff: a.go:1)\n"
	got := computeRemainder(issue, pr, nil)
	if len(got.TickIndices) != 1 || len(got.Remaining) != 1 ||
		got.Remaining[0] != "The hook ticks the met criterion and logs the change" {
		t.Errorf("got %+v, want the short criterion ticked and only the long one left", got)
	}
}

func TestComputeRemainderPriorMet(t *testing.T) {
	issue := "## Acceptance criteria\n\n1. numbered met earlier\n2. numbered unmet now\n- [ ] boxed met now\n3. untouched\n"
	pr := "## Acceptance criteria\n\n- ✅ met: boxed met now (diff: a)\n- ✅ met: untouched (diff: b)\n\n" +
		"## ⚠️ Unmet acceptance criteria\n\n- numbered unmet now\n  _❌ unmet — x_\n"
	got := computeRemainder(issue, pr, []string{"numbered met earlier", "numbered unmet now"})
	if !reflect.DeepEqual(got.Remaining, []string{"numbered unmet now"}) {
		t.Errorf("Remaining = %q, want only the criterion this PR restates unmet", got.Remaining)
	}
	if !reflect.DeepEqual(got.Met, []string{"boxed met now", "untouched"}) {
		t.Errorf("Met = %q, want the boxed and numbered criteria this PR met", got.Met)
	}
	if without := computeRemainder(issue, pr, nil); len(without.Remaining) != 2 {
		t.Errorf("without priorMet Remaining = %q, want the earlier criterion kept", without.Remaining)
	}
}

// #3529 B3: pr-create renders a criterion with its closing keyword rewritten
// to `Refs`, so the remainder matched neither copy to the other and listed
// both. It lists the criterion once, in the issue body's exact text.
func TestComputeRemainderClosingKeywordListedOnce(t *testing.T) {
	const criterion = "B2: The description says `Closes #42`."
	issue := "## Acceptance criteria\n\n- [x] A1: the widget holds\n- [ ] " + criterion + "\n"
	pr := "## Acceptance criteria\n\n- ✅ met: A1: the widget holds (diff: a.go)\n\n" +
		"## ⚠️ Unmet acceptance criteria\n\nThis PR leaves #42 open.\n\n" +
		"- B2: The description says `Refs #42`.\n  _❔ not verifiable — feature-validate: no PR yet_\n\nRefs #42\n"
	got := computeRemainder(issue, pr, nil)
	if !reflect.DeepEqual(got.Remaining, []string{criterion}) {
		t.Fatalf("Remaining = %q, want exactly the issue's %q", got.Remaining, criterion)
	}
	comment := remainderComment(77, got.Remaining, got.NotVerifiable, got.Met, true, false, false, false)
	if strings.Contains(comment, "Refs #42`") {
		t.Errorf("comment carries the PR's rewritten copy:\n%s", comment)
	}
	if n := strings.Count(comment, "The description says"); n != 1 {
		t.Errorf("criterion listed %d times, want once:\n%s", n, comment)
	}
}

// #3529: the closing-keyword family folds to one form for matching only.
func TestNormaliseCriterionFoldsClosingKeywords(t *testing.T) {
	for _, s := range []string{"says `Closes #42`", "says Fixes #42", "says resolved #42", "says Refs #42",
		"says Closes: #42", "says closes:#42"} {
		if got := normaliseCriterion(s); got != "says refs #42" {
			t.Errorf("normaliseCriterion(%q) = %q, want %q", s, got, "says refs #42")
		}
	}
	if got := normaliseCriterion("says Closes acme/x#42"); got != "says refs acme/x#42" {
		t.Errorf("qualified ref: %q", got)
	}
	if got := normaliseCriterion("it closes the door"); got != "it closes the door" {
		t.Errorf("a keyword with no issue ref folded: %q", got)
	}
}

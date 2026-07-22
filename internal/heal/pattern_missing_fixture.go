package heal

import (
	"fmt"
	"regexp"
	"strings"
)

// MissingFixture matches inherited failures whose details mention a missing
// fixture file at a recognised fixtures path. The fix is deterministic: create
// an empty placeholder file at the referenced path so the failing test can
// load it (the test still has to be updated to match the placeholder's
// shape — flagged in the PR body).
type MissingFixture struct{}

// NewMissingFixture constructs the pattern.
func NewMissingFixture() *MissingFixture { return &MissingFixture{} }

// Slug implements HealPattern.
func (p *MissingFixture) Slug() string { return "missing-fixture" }

// Description implements HealPattern.
func (p *MissingFixture) Description() string {
	return "Test fixture file is missing under test/fixtures/ or __fixtures__/ — creates an empty placeholder so the failing test can load."
}

// fixturePathPattern captures paths under known fixture directories. The
// outer group captures the full path including the fixtures prefix.
var fixturePathPattern = regexp.MustCompile(`((?:[^\s'"]+/)?(?:__fixtures__|test/fixtures|tests/fixtures)/[A-Za-z0-9_./-]+\.[A-Za-z0-9]+)`)

// Matches implements HealPattern. Requires all failures to be inherited AND
// at least one failure to mention a missing-file phrasing alongside a
// fixtures-style path.
func (p *MissingFixture) Matches(failures []BaselineFailure) bool {
	if len(failures) == 0 {
		return false
	}
	for _, f := range failures {
		if f.Classification != "inherited" {
			return false
		}
	}
	for _, f := range failures {
		body := strings.ToLower(f.Name + " " + f.Details)
		hasMissingPhrase := containsAny(body, "enoent", "no such file", "cannot find module", "module not found", "fixture not found")
		if !hasMissingPhrase {
			continue
		}
		if fixturePathPattern.FindStringIndex(f.Name+" "+f.Details) != nil {
			return true
		}
	}
	return false
}

// GenerateFix creates a single empty placeholder fixture file at the path
// extracted from the failure details. When multiple distinct paths are
// referenced the pattern falls back to a needs-review PR — touching multiple
// fixtures at once exceeds the deterministic comfort zone.
func (p *MissingFixture) GenerateFix(failures []BaselineFailure) (HealFix, bool) {
	paths := uniqueFixturePaths(failures)
	if len(paths) == 0 {
		// Matches() saw a candidate but we could not extract a concrete path;
		// open a needs-review PR.
		return HealFix{
			PRTitle:  fmt.Sprintf("chore(heal): %s — needs review", p.Slug()),
			PRBody:   buildNeedsReviewBody(p, failures, "Could not extract a fixtures path from the failure details — human review needed."),
			PRLabels: []string{"pipeline-heal:needs-review", "pattern:" + p.Slug()},
		}, false
	}
	if len(paths) > 1 {
		return HealFix{
			PRTitle: fmt.Sprintf("chore(heal): %s — multiple fixtures missing", p.Slug()),
			PRBody: buildNeedsReviewBody(p, failures,
				fmt.Sprintf("Multiple fixture paths referenced (%d): %s. Deterministic fix only handles single-path cases — human review needed.",
					len(paths), strings.Join(paths, ", "))),
			PRLabels: []string{"pipeline-heal:needs-review", "pattern:" + p.Slug()},
		}, false
	}

	path := paths[0]
	body := fmt.Sprintf(`Missing fixture file detected on main: %s

Failing test(s):

%s

Created an empty placeholder so the test loader can find the file. The fixture's contents likely still need to be filled in to make the test green — this PR is a starting point, not a complete fix.
`, path, formatFailingTestList(failures))

	return HealFix{
		BranchName:    "missing-fixture-" + shortSlug(path),
		CommitMessage: fmt.Sprintf("chore(heal): add missing fixture %s", path),
		FilesToCreate: []HealFileChange{
			{Path: path, Content: ""},
		},
		PRTitle:          fmt.Sprintf("chore(heal): add missing fixture %s", path),
		PRBody:           body,
		PRLabels:         []string{"pipeline-heal:auto", "pattern:" + p.Slug()},
		DiffLineEstimate: 1,
	}, true
}

func uniqueFixturePaths(failures []BaselineFailure) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range failures {
		matches := fixturePathPattern.FindAllString(f.Name+" "+f.Details, -1)
		for _, m := range matches {
			m = strings.TrimSpace(m)
			if m == "" || seen[m] {
				continue
			}
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}

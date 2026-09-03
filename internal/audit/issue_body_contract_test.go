package audit

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The issue-body heading contract has one canonical copy:
// skills/nightgauge-issue-audit/SKILL.md (Phase 5). scripts/check-issue-body-contract.py
// pins the skill-authored bodies against it; these tests pin the two bodies the
// Go binary machine-authors (#1116). The table is parsed at test time so a
// change to the contract turns this red instead of shipping non-conformant
// issues — the test never carries its own copy of the headings.

const auditSkillRel = "skills/nightgauge-issue-audit/SKILL.md"

// repoRoot walks up from the package directory to the checkout root.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, auditSkillRel)); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not locate %s above %s — the contract table is required, never a silent pass", auditSkillRel, dir)
		}
		dir = parent
	}
}

// parseRequiredHeadingTable mirrors parse_table() in
// scripts/check-issue-body-contract.py: the first markdown table whose header
// row is `Type | Required …`, rows keyed by type (grouped `a / b` cells
// expanded), headings split on commas.
func parseRequiredHeadingTable(t *testing.T, text string) map[string][]string {
	t.Helper()
	rows := map[string][]string{}
	inTable := false
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "|") {
			if inTable {
				break
			}
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		if len(cells) < 2 {
			continue
		}
		if !inTable {
			head0 := strings.TrimSpace(strings.ReplaceAll(cells[0], "`", ""))
			head1 := strings.TrimSpace(strings.ReplaceAll(cells[1], "`", ""))
			if head0 == "Type" && strings.HasPrefix(head1, "Required") {
				inTable = true
			}
			continue
		}
		if strings.Trim(cells[0], "-: ") == "" {
			continue // separator row
		}
		var headings []string
		for _, h := range strings.Split(cells[1], ",") {
			if h = strings.TrimSpace(h); h != "" {
				headings = append(headings, h)
			}
		}
		for _, typ := range strings.Split(cells[0], "/") {
			typ = strings.Trim(strings.TrimSpace(typ), "`")
			if _, dup := rows[typ]; dup {
				t.Fatalf("%s: duplicate row for type %q", auditSkillRel, typ)
			}
			rows[typ] = headings
		}
	}
	if len(rows) == 0 {
		t.Fatalf("%s: could not locate the required-heading table", auditSkillRel)
	}
	return rows
}

// extractHeadingMatcher mirrors extract_heading_regex(): the matcher is taken
// from Phase 5's own bash, so a change to how the audit matches headings is
// what these tests run against, not a hard-coded copy.
func extractHeadingMatcher(t *testing.T, text string) func(body, heading string) bool {
	t.Helper()
	m := regexp.MustCompile(`grep -q[a-zA-Z]*E "(\^##\[\[:space:\]\]\+\$\{HEADING\}[^"]*)"`).FindStringSubmatch(text)
	if m == nil {
		t.Fatalf("%s: could not extract the Phase 5 heading regex from its bash", auditSkillRel)
	}
	template := strings.ReplaceAll(m[1], `\\s`, `\s`)
	return func(body, heading string) bool {
		re := regexp.MustCompile("(?m)" + strings.ReplaceAll(template, "${HEADING}", regexp.QuoteMeta(heading)))
		return re.MatchString(body)
	}
}

type headingContract struct {
	table   map[string][]string
	present func(body, heading string) bool
}

func loadHeadingContract(t *testing.T) headingContract {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), auditSkillRel))
	if err != nil {
		t.Fatalf("read %s: %v", auditSkillRel, err)
	}
	text := string(raw)
	c := headingContract{table: parseRequiredHeadingTable(t, text), present: extractHeadingMatcher(t, text)}
	// Self-test, as the script does: a matcher that cannot see a violation
	// makes every assertion below vacuous.
	if c.present("## Acceptance criteria\n", "Acceptance Criteria") {
		t.Fatalf("extracted matcher is case-insensitive; the contract check proves nothing")
	}
	if !c.present("## Summary\n\nx\n", "Summary") {
		t.Fatalf("extracted matcher rejected a correct '## Summary' heading")
	}
	return c
}

func (c headingContract) assertBody(t *testing.T, issueType, body string) {
	t.Helper()
	headings, ok := c.table[issueType]
	if !ok {
		t.Fatalf("%s: no row for type %q", auditSkillRel, issueType)
	}
	for _, h := range headings {
		if !c.present(body, h) {
			t.Errorf("MISSING_REQUIRED_HEADING: type %q requires %q; body:\n%s", issueType, h, body)
		}
	}
}

func TestGenerateSubIssueBody_SatisfiesHeadingContract(t *testing.T) {
	c := loadHeadingContract(t)
	// Sub-issues carry no type label; they are pinned against every row a
	// finding could be filed as, which must agree with each other.
	for _, typ := range []string{"feature", "docs", "refactor"} {
		for _, f := range []*AuditFinding{
			{ID: "abc123", Category: "API_MISMATCH", Description: "x", AcceptanceCriteria: []string{"Fix it", "Test it"}},
			{ID: "def456", Category: "SECURITY", Description: "y"},
		} {
			c.assertBody(t, typ, GenerateSubIssueBody(f, 1))
		}
	}
	body := GenerateSubIssueBody(&AuditFinding{Description: "x"}, 1)
	if strings.Contains(body, "Finding Description") {
		t.Errorf("legacy 'Finding Description' heading still emitted:\n%s", body)
	}
}

func TestGenerateEpicBody_SatisfiesHeadingContract(t *testing.T) {
	c := loadHeadingContract(t)
	for _, epic := range []*Epic{
		{Title: "api_alignment (repo-a)", Dimension: "api_alignment", Repository: "repo-a"},
		{Title: "security (repo-b)", Dimension: "security", Repository: "repo-b", Findings: []*AuditFinding{
			{ID: "abc", Category: "SEC", Description: "leak", Severity: "high"},
		}},
	} {
		c.assertBody(t, "epic", GenerateEpicBody(epic))
	}
}

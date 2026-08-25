package git

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// branchNameCase mirrors testdata/branch_name_cases.json.
type branchNameCase struct {
	Name        string   `json:"name"`
	IssueNumber int      `json:"issueNumber"`
	Title       string   `json:"title"`
	Labels      []string `json:"labels"`
	Want        string   `json:"want"`
}

func loadBranchNameCases(t *testing.T) []branchNameCase {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "branch_name_cases.json"))
	if err != nil {
		t.Fatalf("read contract fixture: %v", err)
	}
	var doc struct {
		Cases []branchNameCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse contract fixture: %v", err)
	}
	if len(doc.Cases) == 0 {
		// A fixture that silently loses its cases would make every assertion
		// below vacuous while the suite still reported green.
		t.Fatal("contract fixture has no cases")
	}
	return doc.Cases
}

// TestComposeBranchNameContract pins THE composer against the shared fixture.
func TestComposeBranchNameContract(t *testing.T) {
	for _, tc := range loadBranchNameCases(t) {
		t.Run(tc.Name, func(t *testing.T) {
			got := ComposeBranchName(tc.Labels, tc.IssueNumber, tc.Title)
			if got != tc.Want {
				t.Errorf("ComposeBranchName(%v, %d, %q)\n got %q\nwant %q",
					tc.Labels, tc.IssueNumber, tc.Title, got, tc.Want)
			}
		})
	}
}

// TestComposeBranchNameEmitsIssueNumberExactlyOnce is the number-doubling
// guard stated independently of the fixture's literals (#889 AC3): whatever
// the title carries, the composed name mentions the issue number once.
func TestComposeBranchNameEmitsIssueNumberExactlyOnce(t *testing.T) {
	for _, tc := range loadBranchNameCases(t) {
		t.Run(tc.Name, func(t *testing.T) {
			name := ComposeBranchName(tc.Labels, tc.IssueNumber, tc.Title)
			_, rest, ok := strings.Cut(name, "/")
			if !ok {
				t.Fatalf("no prefix separator in %q", name)
			}
			number, parsed := ParseIssueNumberFromBranch(name + "x")
			if !parsed || number != tc.IssueNumber {
				t.Fatalf("ParseIssueNumberFromBranch(%q) = %d, %v; want %d",
					name, number, parsed, tc.IssueNumber)
			}
			// The number must not appear again as the first slug token, which
			// is the exact shape the extension produced: 227-227-per-....
			slug := strings.TrimPrefix(rest, strings.SplitN(rest, "-", 2)[0]+"-")
			if first, _, _ := strings.Cut(slug, "-"); first == strings.SplitN(rest, "-", 2)[0] {
				t.Errorf("issue number repeated in slug: %q", name)
			}
		})
	}
}

// TestNoSecondBranchNameComposerInTypeScript is the regression guard that
// actually catches #889 coming back. The defect was never a wrong function —
// each composer was self-consistent — it was a SECOND composer, in the other
// language, on the path customers use. Asserting the Go composer alone passes
// today and proves nothing about that, so this asserts the absence of the
// thing itself.
func TestNoSecondBranchNameComposerInTypeScript(t *testing.T) {
	root := filepath.Join("..", "..", "packages", "nightgauge-vscode", "src")
	if _, err := os.Stat(root); err != nil {
		t.Skipf("extension sources not present: %v", err)
	}

	// A branch prefix glued to an interpolation and then a slug separator —
	// `feat/${n}-…` — is composition. A GLOB over branches someone else
	// composed (`epic/${n}-*`, passed to ls-remote or `branch --list`) is a
	// lookup and is fine; it is distinguished by the `*` immediately after the
	// separator, which is why the check below cannot be the regexp alone.
	composer := regexp.MustCompile(`(feat|fix|docs|chore|refactor|test|epic)/\$\{[^{}]*\}-`)

	var offenders []string
	scanned := 0
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".ts") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		scanned++
		for i, line := range strings.Split(string(body), "\n") {
			if strings.Contains(line, "#889-allow") {
				continue
			}
			for _, loc := range composer.FindAllStringIndex(line, -1) {
				if loc[1] < len(line) && line[loc[1]] == '*' {
					continue // a glob, not a composition
				}
				offenders = append(offenders,
					filepath.ToSlash(path)+":"+itoa(i+1)+": "+strings.TrimSpace(line))
				break
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk extension sources: %v", err)
	}
	if scanned == 0 {
		// Guard the guard: a walk that reads nothing reports "clean".
		t.Fatal("scanned no TypeScript files; the guard would pass vacuously")
	}
	if len(offenders) > 0 {
		t.Errorf("TypeScript composes branch names in %d place(s); the Go composer "+
			"(git.composeBranchName over IPC) is the authority (#889):\n  %s",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

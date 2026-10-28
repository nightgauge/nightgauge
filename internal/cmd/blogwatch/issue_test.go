package blogwatch

import (
	"strings"
	"testing"
)

func TestIssueLabelsClosedSet(t *testing.T) {
	got := IssueLabels()
	if strings.Join(got, ",") != "type:feature,area:blog-watch" {
		t.Fatalf("labels = %v", got)
	}
	for _, l := range got {
		if l == "auto-process" {
			t.Fatal("auto-process in label set")
		}
	}
}

func TestBuildIssueBody(t *testing.T) {
	long := strings.Repeat("x", 500) + "```"
	body, err := BuildIssueBody("Our skills never cache tool results.", long, "https://blog.test/eng/a")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(body, "Our skills never cache tool results.") {
		t.Fatalf("body does not lead with the gap: %q", body)
	}
	if n := strings.Count(body, "```"); n != 2 {
		t.Fatalf("fences = %d, want one block", n)
	}
	if !strings.Contains(body, "Trigger: https://blog.test/eng/a") {
		t.Fatal("no Trigger line")
	}
	if strings.Contains(body, strings.Repeat("x", MaxExcerptLen)) {
		t.Fatal("excerpt not capped")
	}
	noExcerpt, _ := BuildIssueBody("gap", "", "https://blog.test/eng/a")
	if strings.Contains(noExcerpt, "```") {
		t.Fatal("fence without excerpt")
	}
}

func TestBuildIssueBodyRejects(t *testing.T) {
	for _, c := range [][3]string{
		{"", "e", "https://blog.test/a"},
		{"the quote", "the quote", "https://blog.test/a"},
		{"gap ```x```", "", "https://blog.test/a"},
		{"gap", "", "http://blog.test/a"},
	} {
		if _, err := BuildIssueBody(c[0], c[1], c[2]); err == nil {
			t.Errorf("BuildIssueBody(%q) accepted", c)
		}
	}
}

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/github/githubtest"
)

// runEpicValidate drives `epic validate` through the assembled root command
// against fake, and returns what it wrote, the exit code main would use, and
// the error.
func runEpicValidate(t *testing.T, fake *githubtest.Forge, args ...string) (string, int, error) {
	t.Helper()
	sizeGateEnv(t)
	client := fake.Client()
	prev := newClientFromConfigFn
	newClientFromConfigFn = func(gh.TokenResolver, string, string) (*gh.Client, error) { return client, nil }
	t.Cleanup(func() { newClientFromConfigFn = prev })

	root := rootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(io.Discard)
	root.SetArgs(append([]string{"epic", "validate", "100", "--repo", githubtest.Owner + "/" + githubtest.Repo}, args...))
	leaf, err := root.ExecuteC()
	return out.String(), exitCodeFor(io.Discard, leaf, err), err
}

// validateEpic is epic #100 with sub-issues #101, #102 and #103, none of them
// blocked.
func validateEpic(t *testing.T) *githubtest.Forge {
	return githubtest.New(t, map[int]githubtest.Issue{
		100: {SubIssues: []int{101, 102, 103}},
		101: {},
		102: {},
		103: {},
	})
}

// TestEpicValidate_UnreadableSubIssueIsNotValid: #102 cannot be read.
// `epic validate` printed "valid — no structural gaps found (3 sub-issues
// checked)" and exited 0, having checked two. It now says how many it checked
// and how many it could not, names #102, and exits non-zero, in text and in
// JSON.
func TestEpicValidate_UnreadableSubIssueIsNotValid(t *testing.T) {
	t.Run("text", func(t *testing.T) {
		fake := validateEpic(t)
		fake.Unreadable = []int{102}

		out, code, err := runEpicValidate(t, fake)
		if err == nil || code == 0 {
			t.Fatalf("exit %d (err %v), want non-zero; output:\n%s", code, err, out)
		}
		if strings.Contains(out, "valid — no structural gaps found") {
			t.Fatalf("output calls the epic valid:\n%s", out)
		}
		for _, want := range []string{
			"Epic #100: not validated — 1 of 3 sub-issues could not be checked; 0 gap(s) found in the 2 checked",
			"[unchecked] acme/widgets#102",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("output lacks %q:\n%s", want, out)
			}
		}
	})

	t.Run("json", func(t *testing.T) {
		fake := validateEpic(t)
		fake.Unreadable = []int{102}

		out, code, err := runEpicValidate(t, fake, "--json")
		if err == nil || code == 0 {
			t.Fatalf("exit %d (err %v), want non-zero; output:\n%s", code, err, out)
		}
		var got struct {
			TotalSubIssues   int  `json:"totalSubIssues"`
			CheckedSubIssues int  `json:"checkedSubIssues"`
			FailedSubIssues  int  `json:"failedSubIssues"`
			Valid            bool `json:"valid"`
			Failures         []struct {
				SubIssueNumber int    `json:"subIssueNumber"`
				SubIssueRepo   string `json:"subIssueRepo"`
				Error          string `json:"error"`
			} `json:"failures"`
		}
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("output is not JSON: %v\n%s", err, out)
		}
		if got.Valid || got.TotalSubIssues != 3 || got.CheckedSubIssues != 2 || got.FailedSubIssues != 1 {
			t.Fatalf("result = %+v, want 3 listed, 2 checked, 1 failed, not valid", got)
		}
		if len(got.Failures) != 1 || got.Failures[0].SubIssueNumber != 102 ||
			got.Failures[0].SubIssueRepo != "acme/widgets" || got.Failures[0].Error == "" {
			t.Fatalf("failures = %+v, want acme/widgets#102 with its reason", got.Failures)
		}
	})
}

// TestEpicValidate_EverySubIssueCheckedIsValid: with every sub-issue read and
// no gap, the epic is valid and the command exits 0.
func TestEpicValidate_EverySubIssueCheckedIsValid(t *testing.T) {
	out, code, err := runEpicValidate(t, validateEpic(t))
	if err != nil || code != 0 {
		t.Fatalf("exit %d (err %v), want 0; output:\n%s", code, err, out)
	}
	if want := "Epic #100: valid — no structural gaps found (3 sub-issues checked)"; !strings.Contains(out, want) {
		t.Fatalf("output lacks %q:\n%s", want, out)
	}
}

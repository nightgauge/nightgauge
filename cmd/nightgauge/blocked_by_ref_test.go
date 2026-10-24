package main

import (
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/depgraph"
	"github.com/nightgauge/nightgauge/internal/forge"
)

// #1508: the blocker argument of add-blocked-by / remove-blocked-by names an
// issue in any repository. A bare number stays in --repo; repo#N resolves
// through the workspace alias map, falling back to --owner.
func TestParseBlockerRef(t *testing.T) {
	aliases := depgraph.WorkspaceRepoAliases([]string{
		"acme/acme-api",
		"acme/acme-web",
		"elsewhere/shared",
		"org-a/app",
		"org-b/app",
	})
	ok := func(owner, repo string, n int) forge.IssueRef {
		return forge.IssueRef{Owner: owner, Repo: repo, Number: n}
	}
	for _, tc := range []struct {
		arg     string
		want    forge.IssueRef
		wantErr string
	}{
		{arg: "1253", want: ok("acme", "acme-web", 1253)},
		{arg: "#1253", want: ok("acme", "acme-web", 1253)},
		{arg: "acme/acme-api#1253", want: ok("acme", "acme-api", 1253)},
		{arg: "someone/else.github.io#4", want: ok("someone", "else.github.io", 4)},
		{arg: "acme-api#1253", want: ok("acme", "acme-api", 1253)},
		{arg: "shared#9", want: ok("elsewhere", "shared", 9)},
		{arg: "SHARED#9", want: ok("elsewhere", "shared", 9)},
		{arg: "unknown-repo#9", want: ok("acme", "unknown-repo", 9)},
		{arg: "https://github.com/acme/acme-api/issues/1253", want: ok("acme", "acme-api", 1253)},
		{arg: "https://github.com/acme/acme-api/issues/1253/", want: ok("acme", "acme-api", 1253)},

		{arg: "app#3", wantErr: "ambiguous"},
		{arg: "", wantErr: "invalid blocker"},
		{arg: "0", wantErr: "invalid blocker"},
		{arg: "-4", wantErr: "invalid blocker"},
		{arg: "12a", wantErr: "invalid blocker"},
		{arg: "repo#", wantErr: "invalid blocker"},
		{arg: "#", wantErr: "invalid blocker"},
		{arg: "a/b/c#1", wantErr: "invalid blocker"},
		{arg: "../etc#1", wantErr: "invalid blocker"},
		{arg: "owner/..#1", wantErr: "invalid blocker"},
		{arg: "owner/repo#1#2", wantErr: "invalid blocker"},
		{arg: "owner/re po#1", wantErr: "invalid blocker"},
		{arg: "https://github.com/o/r/pull/5", wantErr: "invalid blocker"},
		{arg: "https://evil.example/o/r/issues/5", wantErr: "invalid blocker"},
		{arg: "https://github.com/o/r/issues/0", wantErr: "invalid blocker"},
		{arg: "https://github.com/o/r/issues/5?x=1", wantErr: "invalid blocker"},
	} {
		t.Run(tc.arg, func(t *testing.T) {
			got, err := parseBlockerRef(tc.arg, "acme", "acme-web", aliases)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("parseBlockerRef(%q) = %v, %v; want an error containing %q", tc.arg, got, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseBlockerRef(%q): %v", tc.arg, err)
			}
			if got != tc.want {
				t.Errorf("parseBlockerRef(%q) = %+v, want %+v", tc.arg, got, tc.want)
			}
		})
	}
}

package main

import (
	"errors"
	"io"
	"path/filepath"
	"testing"
)

var errBoardRecorded = errors.New("board recorded")

// TestBoardBackfillDoesNotOverrideRepo drives project sync-status, project add
// and hook post-merge through the assembled root command, so the config
// backfill in PersistentPreRunE runs. The backfilled local board must not read
// as an explicit --project: --repo <sibling> resolves the sibling's board
// (#3166).
func TestBoardBackfillDoesNotOverrideRepo(t *testing.T) {
	root := t.TempDir()
	writeRepoConfig(t, root, "acme", "web", 3)
	writeRepoConfig(t, filepath.Join(root, "platform"), "acme", "platform", 4)
	writeWorkspaceManifest(t, root, `workspace:
  name: Acme
repositories:
  - name: web
    path: .
    project_number: 3
  - name: platform
    path: platform
    project_number: 4
`)
	t.Chdir(root)
	t.Setenv("GH_TOKEN", "test-token")
	t.Setenv("GITHUB_TOKEN", "test-token")

	commands := []struct {
		name string
		args []string
		repo func(repo string) []string
	}{
		{"project sync-status", []string{"project", "sync-status", "1", "ready"},
			func(r string) []string { return []string{"--repo", r} }},
		{"project add", []string{"project", "add", "1"},
			func(r string) []string { return []string{"--repo", r} }},
		{"hook post-merge", []string{"hook", "post-merge", "--issue", "1", "--pr", "2"},
			func(r string) []string { return []string{"--owner", "acme", "--repo", r} }},
	}
	cases := []struct {
		name  string
		repo  string
		extra []string
		want  int
	}{
		{"sibling repo resolves sibling board", "acme/platform", nil, 4},
		{"explicit project wins", "acme/platform", []string{"--project", "7"}, 7},
		{"no repo resolves local board", "", nil, 3},
		{"local repo resolves local board", "acme/web", nil, 3},
	}

	for _, c := range commands {
		for _, tc := range cases {
			t.Run(c.name+"/"+tc.name, func(t *testing.T) {
				got := -1
				var gotCmd string
				prev := boardResolvedHook
				boardResolvedHook = func(command string, board int) error {
					gotCmd, got = command, board
					return errBoardRecorded
				}
				t.Cleanup(func() { boardResolvedHook = prev })

				args := append([]string{}, c.args...)
				if tc.repo != "" {
					args = append(args, c.repo(tc.repo)...)
				}
				args = append(args, tc.extra...)

				cmd := rootCmd()
				cmd.SetOut(io.Discard)
				cmd.SetErr(io.Discard)
				cmd.SetArgs(args)
				if err := cmd.Execute(); !errors.Is(err, errBoardRecorded) {
					t.Fatalf("Execute(%v) = %v, want the seam's sentinel", args, err)
				}
				if gotCmd != c.name {
					t.Fatalf("seam command = %q, want %q", gotCmd, c.name)
				}
				if got != tc.want {
					t.Errorf("board = %d, want %d", got, tc.want)
				}
			})
		}
	}
}

// TestBackfillFlagDefaultUnregisteredFlag checks that a command without the
// flag is left alone and the value does not mark a registered flag Changed.
func TestBackfillFlagDefaultUnregisteredFlag(t *testing.T) {
	root := rootCmd()
	sync := findSubcommand(t, root, "project", "sync-status")
	backfillFlagDefault(root, "no-such-flag", "1") // must not panic
	backfillFlagDefault(sync, "project", "9")
	if sync.Flags().Changed("project") {
		t.Fatal("backfill marked --project Changed")
	}
	if v := sync.Flags().Lookup("project").Value.String(); v != "9" {
		t.Fatalf("--project = %q, want 9", v)
	}
}

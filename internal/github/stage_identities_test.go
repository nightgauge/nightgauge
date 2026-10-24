package github

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
)

// fakeGH installs a gh stand-in that answers `auth status --json hosts` with
// status and `api user` with the login for the GH_TOKEN it was given.
func fakeGH(t *testing.T, status string, statusExit int) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = auth ]; then\n" +
		"  [ -n \"$GH_TOKEN$GITHUB_TOKEN\" ] && { echo 'env token leaked into the keyring read' >&2; exit 9; }\n" +
		"  cat <<'JSON'\n" + status + "\nJSON\n" +
		"  exit " + strconv.Itoa(statusExit) + "\nfi\n" +
		"if [ \"$1\" = api ]; then\n" +
		"  case \"$GH_TOKEN\" in ghp_maint) echo maintainer ;; ghp_bad) exit 1 ;; *) echo other ;; esac\n" +
		"  exit 0\nfi\nexit 2\n"
	path := filepath.Join(dir, "gh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := ghBinary
	ghBinary = path
	t.Cleanup(func() { ghBinary = orig })
}

// Every keyring account counts, not only the active one, plus each env
// token's account and the configured logins. An App token adds nothing.
func TestStageIdentities_EveryAccountTheStagesCanReach(t *testing.T) {
	fakeGH(t, `{"hosts":{"github.com":[{"login":"Maintainer","active":true},{"login":"second-account","active":false}]}}`, 0)
	t.Setenv("GH_TOKEN", "ghs_installation")
	t.Setenv("GITHUB_TOKEN", "ghp_maint")
	got, err := StageIdentities(context.Background(), []string{"configured-user"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"configured-user", "maintainer", "second-account"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("StageIdentities = %v, want %v", got, want)
	}
}

// A token whose account cannot be read fails the whole read: the gate then
// accepts no label approval.
func TestStageIdentities_AnUnreadableTokenFailsClosed(t *testing.T) {
	fakeGH(t, `{"hosts":{}}`, 0)
	t.Setenv("GH_TOKEN", "ghp_bad")
	t.Setenv("GITHUB_TOKEN", "")
	if _, err := StageIdentities(context.Background(), nil); err == nil {
		t.Fatal("an unreadable token was ignored")
	}
}

// A keyring gh cannot report is an error; a machine logged in to nothing is not.
func TestStageIdentities_KeyringReads(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	fakeGH(t, `garbage`, 1)
	if _, err := StageIdentities(context.Background(), nil); err == nil {
		t.Fatal("an unreadable keyring was ignored")
	}
	fakeGH(t, `You are not logged into any GitHub hosts.`, 1)
	got, err := StageIdentities(context.Background(), nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("not logged in: %v, %v", got, err)
	}
}

// A personal token under any environment variable name is an identity a
// stage can use — `env:GITHUB_TOKEN_<ORG>` config references put them there.
func TestStageIdentities_EveryPersonalTokenInTheEnvironment(t *testing.T) {
	fakeGH(t, `{"hosts":{}}`, 0)
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GITHUB_TOKEN_ACME", "ghp_maint")
	t.Setenv("SOME_OTHER_SECRET", "github_pat_other")
	t.Setenv("NOT_A_TOKEN", "hello")
	got, err := StageIdentities(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"maintainer", "other"}) {
		t.Fatalf("StageIdentities = %v, want [maintainer other]", got)
	}
}

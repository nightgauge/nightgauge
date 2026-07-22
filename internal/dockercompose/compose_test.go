package dockercompose

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// installFakeDocker writes a `docker` shim onto a fresh tempdir and prepends
// that dir to PATH for the duration of the test. It returns the call-log
// path; each call to the fake records its args (one line per call).
func installFakeDocker(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake-docker harness is POSIX-shell-based")
	}
	dir := t.TempDir()
	src, err := os.ReadFile(filepath.Join("testdata", "fake-docker.sh"))
	if err != nil {
		t.Fatalf("read fake-docker.sh: %v", err)
	}
	dst := filepath.Join(dir, "docker")
	if err := os.WriteFile(dst, src, 0o755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	logPath := filepath.Join(dir, "calls.log")
	t.Setenv("FAKE_DOCKER_LOG", logPath)
	return logPath
}

func readLog(t *testing.T, logPath string) string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("read log: %v", err)
	}
	return string(data)
}

func TestIsAvailable_ReturnsTrueWhenDockerExits0(t *testing.T) {
	installFakeDocker(t)
	if !IsAvailable(context.Background()) {
		t.Fatalf("IsAvailable should be true with fake docker on PATH")
	}
}

func TestIsAvailable_ReturnsFalseWhenDockerExitsNonZero(t *testing.T) {
	installFakeDocker(t)
	t.Setenv("FAKE_DOCKER_VERSION_EXIT", "1")
	if IsAvailable(context.Background()) {
		t.Fatalf("IsAvailable should be false when docker version fails")
	}
}

func TestTeardownProject_RunsExpectedCommands(t *testing.T) {
	logPath := installFakeDocker(t)
	t.Setenv("FAKE_DOCKER_IMAGES_OUT", "issue-42-api\nissue-42-worker\nunrelated-image\n")

	res, err := TeardownProject(context.Background(), "issue-42", TeardownOptions{RemoveImages: true})
	if err != nil {
		t.Fatalf("TeardownProject: %v", err)
	}
	if !res.ComposeRan {
		t.Errorf("expected ComposeRan=true")
	}
	if res.IssueNumber != 42 {
		t.Errorf("expected IssueNumber=42, got %d", res.IssueNumber)
	}

	calls := readLog(t, logPath)
	if !strings.Contains(calls, "compose -p issue-42 down -v --remove-orphans") {
		t.Errorf("expected compose down call, got log:\n%s", calls)
	}
	if !strings.Contains(calls, "rmi -f issue-42-api") {
		t.Errorf("expected rmi for issue-42-api, got log:\n%s", calls)
	}
	if !strings.Contains(calls, "rmi -f issue-42-worker") {
		t.Errorf("expected rmi for issue-42-worker, got log:\n%s", calls)
	}
	if strings.Contains(calls, "rmi -f unrelated-image") {
		t.Errorf("must NOT remove unrelated images, got log:\n%s", calls)
	}
	if len(res.ImagesRemoved) != 2 {
		t.Errorf("expected 2 images removed, got %v", res.ImagesRemoved)
	}
}

func TestTeardownProject_DryRunSkipsDownAndRmi(t *testing.T) {
	logPath := installFakeDocker(t)
	t.Setenv("FAKE_DOCKER_IMAGES_OUT", "issue-7-api\n")

	res, err := TeardownProject(context.Background(), "issue-7", TeardownOptions{
		RemoveImages: true,
		DryRun:       true,
	})
	if err != nil {
		t.Fatalf("TeardownProject: %v", err)
	}
	if !res.DryRun {
		t.Errorf("expected DryRun=true")
	}
	if res.ComposeRan {
		t.Errorf("DryRun must not invoke compose down")
	}
	if len(res.ImagesRemoved) != 1 || res.ImagesRemoved[0] != "issue-7-api" {
		t.Errorf("DryRun should still report discovered images, got %v", res.ImagesRemoved)
	}
	calls := readLog(t, logPath)
	if strings.Contains(calls, "down -v") {
		t.Errorf("DryRun must not run docker compose down, got:\n%s", calls)
	}
	if strings.Contains(calls, "rmi -f") {
		t.Errorf("DryRun must not run docker rmi, got:\n%s", calls)
	}
}

func TestTeardownProject_SkipsWhenDockerUnavailable(t *testing.T) {
	installFakeDocker(t)
	t.Setenv("FAKE_DOCKER_VERSION_EXIT", "1")

	res, err := TeardownProject(context.Background(), "issue-99", TeardownOptions{RemoveImages: true})
	if err != nil {
		t.Fatalf("expected nil error on docker unavailable, got %v", err)
	}
	if !res.Skipped {
		t.Errorf("expected Skipped=true")
	}
	if res.ComposeRan {
		t.Errorf("expected ComposeRan=false")
	}
}

func TestTeardownProject_TolerantOfNoSuchProject(t *testing.T) {
	installFakeDocker(t)
	t.Setenv("FAKE_DOCKER_DOWN_EXIT", "1")
	t.Setenv("FAKE_DOCKER_DOWN_STDERR", "no such project: issue-555")

	res, err := TeardownProject(context.Background(), "issue-555", TeardownOptions{})
	if err != nil {
		t.Fatalf("TeardownProject must treat 'no such project' as success, got: %v", err)
	}
	if !res.ComposeRan {
		t.Errorf("expected ComposeRan=true on idempotent no-op")
	}
}

func TestTeardownProject_SoftFailOnGenericDockerError(t *testing.T) {
	installFakeDocker(t)
	t.Setenv("FAKE_DOCKER_DOWN_EXIT", "2")
	t.Setenv("FAKE_DOCKER_DOWN_STDERR", "Cannot connect to the Docker daemon")

	res, err := TeardownProject(context.Background(), "issue-12", TeardownOptions{})
	if err != nil {
		t.Fatalf("TeardownProject must soft-fail on generic docker errors, got: %v", err)
	}
	if res.ComposeRan {
		t.Errorf("ComposeRan should remain false when docker compose down errored")
	}
}

func TestListIssueProjects_ParsesArrayFormat(t *testing.T) {
	installFakeDocker(t)
	t.Setenv("FAKE_DOCKER_LS_OUTPUT", `[
  {"Name":"issue-42","Status":"running(2)"},
  {"Name":"unrelated","Status":"running(1)"},
  {"Name":"issue-7","Status":"exited(0)"}
]`)

	projects, err := ListIssueProjects(context.Background())
	if err != nil {
		t.Fatalf("ListIssueProjects: %v", err)
	}
	if len(projects) != 2 {
		t.Fatalf("expected 2 issue-* projects, got %d (%v)", len(projects), projects)
	}
	got := map[int]string{}
	for _, p := range projects {
		got[p.IssueNumber] = p.Name
	}
	if got[42] != "issue-42" || got[7] != "issue-7" {
		t.Errorf("unexpected projects: %v", got)
	}
}

func TestListIssueProjects_ParsesNDJSONFormat(t *testing.T) {
	installFakeDocker(t)
	t.Setenv("FAKE_DOCKER_LS_OUTPUT",
		"{\"Name\":\"issue-1\",\"Status\":\"running(1)\"}\n"+
			"{\"Name\":\"issue-2\",\"Status\":\"exited(0)\"}\n",
	)

	projects, err := ListIssueProjects(context.Background())
	if err != nil {
		t.Fatalf("ListIssueProjects: %v", err)
	}
	if len(projects) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(projects))
	}
}

func TestListIssueProjects_ReturnsNilWhenDockerUnavailable(t *testing.T) {
	installFakeDocker(t)
	t.Setenv("FAKE_DOCKER_VERSION_EXIT", "1")
	projects, err := ListIssueProjects(context.Background())
	if err != nil {
		t.Fatalf("expected nil error when docker unavailable, got %v", err)
	}
	if projects != nil {
		t.Errorf("expected nil projects when docker unavailable, got %v", projects)
	}
}

func TestIssueNumberFromProject(t *testing.T) {
	cases := []struct {
		name    string
		wantNum int
		wantOK  bool
	}{
		{"issue-42", 42, true},
		{"issue-1", 1, true},
		{"issue-", 0, false},
		{"issue-abc", 0, false},
		{"my-issue-42", 0, false},
		{"", 0, false},
	}
	for _, tc := range cases {
		gotNum, gotOK := IssueNumberFromProject(tc.name)
		if gotNum != tc.wantNum || gotOK != tc.wantOK {
			t.Errorf("IssueNumberFromProject(%q) = (%d, %v), want (%d, %v)",
				tc.name, gotNum, gotOK, tc.wantNum, tc.wantOK)
		}
	}
}

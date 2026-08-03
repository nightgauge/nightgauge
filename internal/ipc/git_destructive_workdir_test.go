package ipc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gitops "github.com/nightgauge/nightgauge/internal/git"
)

// newGitRepoWithDirtyFile creates a real repo holding one uncommitted file, so
// a test can tell whether a handler actually reached `HardReset` + `clean -d`.
// Asserting on the returned error alone would not: the pre-#298 handler
// returned a perfectly successful "ok" while wiping the wrong tree.
func newGitRepoWithDirtyFile(t *testing.T) (root, dirtyPath string) {
	t.Helper()
	root = t.TempDir()
	repo, err := gitops.InitRepo(root)
	if err != nil {
		t.Fatalf("InitRepo: %v", err)
	}
	if err := gitops.CreateInitialCommit(repo, root); err != nil {
		t.Fatalf("CreateInitialCommit: %v", err)
	}
	dirtyPath = filepath.Join(root, "uncommitted-work.txt")
	if err := os.WriteFile(dirtyPath, []byte("an operator's uncommitted work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, dirtyPath
}

// TestDestructiveGitVerbsRefuseImplicitWorkspaceRoot covers #298: the
// git.resetPipeline handler discarded its `json.Unmarshal` error, so a
// malformed frame or a renamed `workDir` field left the zero value — and
// gitService substituted the workspace root. The result was not a failed call
// but a hard reset + `clean -d` aimed at the operator's main checkout: the
// exact #289 blast pattern, reachable from a single bad request.
//
// Each case asserts the workspace root is untouched, not merely that an error
// came back.
func TestDestructiveGitVerbsRefuseImplicitWorkspaceRoot(t *testing.T) {
	cases := []struct {
		name        string
		method      string
		params      json.RawMessage
		wantErrPart string
	}{
		{
			name:        "resetPipeline with malformed params",
			method:      "git.resetPipeline",
			params:      json.RawMessage(`{"workDir": `),
			wantErrPart: "invalid params",
		},
		{
			name:        "resetPipeline with a renamed workDir field",
			method:      "git.resetPipeline",
			params:      json.RawMessage(`{"work_dir":"/somewhere/else"}`),
			wantErrPart: "requires an explicit workDir",
		},
		{
			name:        "resetPipeline with no params at all",
			method:      "git.resetPipeline",
			params:      nil,
			wantErrPart: "invalid params",
		},
		{
			name:        "resetPipeline with an explicitly empty workDir",
			method:      "git.resetPipeline",
			params:      json.RawMessage(`{"workDir":""}`),
			wantErrPart: "requires an explicit workDir",
		},
		{
			name:        "resetPipeline with a whitespace workDir",
			method:      "git.resetPipeline",
			params:      json.RawMessage(`{"workDir":"   "}`),
			wantErrPart: "requires an explicit workDir",
		},
		{
			name:        "abortPipeline with an omitted workDir",
			method:      "git.abortPipeline",
			params:      json.RawMessage(`{"featureBranch":"feat/298-x"}`),
			wantErrPart: "requires an explicit workDir",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, dirty := newGitRepoWithDirtyFile(t)
			s := NewServer(nil, WithWorkspaceRoot(root))

			handler := s.methods[tc.method]
			if handler == nil {
				t.Fatalf("%s handler not registered", tc.method)
			}

			_, err := handler(nil, tc.params)
			if err == nil {
				t.Fatal("expected the handler to refuse, got success")
			}
			if !strings.Contains(err.Error(), tc.wantErrPart) {
				t.Errorf("error = %v, want it to mention %q", err, tc.wantErrPart)
			}
			if _, statErr := os.Stat(dirty); statErr != nil {
				t.Fatalf("the workspace root was mutated despite the refusal: %v", statErr)
			}
		})
	}
}

// TestResetPipelineHonoursExplicitWorkDir is the counterweight: a handler that
// refused everything would pass every case above while making the verb useless.
// An explicit workDir must still reach the git service — and must reach *that*
// directory, leaving the workspace root alone.
func TestResetPipelineHonoursExplicitWorkDir(t *testing.T) {
	root, rootDirty := newGitRepoWithDirtyFile(t)
	target, _ := newGitRepoWithDirtyFile(t)

	s := NewServer(nil, WithWorkspaceRoot(root))
	handler := s.methods["git.resetPipeline"]
	if handler == nil {
		t.Fatal("git.resetPipeline handler not registered")
	}

	params, err := json.Marshal(GitResetPipelineParams{WorkDir: target})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handler(nil, params); err != nil {
		t.Fatalf("handler refused an explicit workDir: %v", err)
	}

	if _, err := os.Stat(rootDirty); err != nil {
		t.Errorf("reset of %s touched the workspace root %s: %v", target, root, err)
	}
}

// TestGitResetPipelineParamsRequireWorkDir pins the contract at the wire level.
// `omitempty` on WorkDir is what made the generated client's parameter optional,
// so the extension could legally send a frame the server had to guess at; the
// field now serialises unconditionally.
func TestGitResetPipelineParamsRequireWorkDir(t *testing.T) {
	encoded, err := json.Marshal(GitResetPipelineParams{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"workDir"`) {
		t.Errorf("workDir must always be on the wire, got %s", encoded)
	}

	encoded, err = json.Marshal(GitAbortPipelineParams{FeatureBranch: "feat/298-x"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"workDir"`) {
		t.Errorf("workDir must always be on the wire, got %s", encoded)
	}
}

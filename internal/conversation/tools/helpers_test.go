package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nightgauge/nightgauge/pkg/types"
)

// fakeForge records what the tools asked of the forge.
type fakeForge struct {
	mu       sync.Mutex
	issues   map[string]*types.Issue // "owner/repo#n"
	prs      map[string]*types.PullRequest
	search   []types.Issue
	queries  []string
	created  []createdIssue
	createFn func(owner, repo string, asOperator bool) error
}

type createdIssue struct {
	Owner, Repo, Title, Body string
	AsOperator               bool
}

func newFakeForge() *fakeForge {
	return &fakeForge{issues: map[string]*types.Issue{}, prs: map[string]*types.PullRequest{}}
}

func key(owner, repo string, n int) string {
	b, _ := json.Marshal([]any{strings.ToLower(owner), strings.ToLower(repo), n})
	return string(b)
}

func (f *fakeForge) GetIssue(_ context.Context, owner, repo string, n int) (*types.Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i, ok := f.issues[key(owner, repo, n)]; ok {
		return i, nil
	}
	return nil, os.ErrNotExist
}

func (f *fakeForge) GetPullRequest(_ context.Context, owner, repo string, n int) (*types.PullRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p, ok := f.prs[key(owner, repo, n)]; ok {
		return p, nil
	}
	return nil, os.ErrNotExist
}

func (f *fakeForge) SearchIssues(_ context.Context, owner, repo, query string, limit int) ([]types.Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, query)
	return append([]types.Issue(nil), f.search...), nil
}

func (f *fakeForge) CreateIssue(_ context.Context, owner, repo, title, body string, asOperator bool) (*types.Issue, error) {
	if f.createFn != nil {
		if err := f.createFn(owner, repo, asOperator); err != nil {
			return nil, err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, createdIssue{owner, repo, title, body, asOperator})
	n := len(f.created) + 100
	return &types.Issue{Number: n, URL: "https://github.com/" + owner + "/" + repo + "/issues/" + itoa(n)}, nil
}

func (f *fakeForge) createdIssues() []createdIssue {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]createdIssue(nil), f.created...)
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// gitRepo makes a committed repository whose config names acme/api.
func gitRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write(t, root, ".nightgauge/config.yaml", "owner: acme\ndefault_repo: api\n")
	write(t, root, "README.md", "# api\n\nThe API service.\n")
	write(t, root, "src/main.go", "package main\n\nfunc main() {}\n")
	git(t, root, "init", "-q", "-b", "main")
	git(t, root, "add", "-A")
	git(t, root, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "init")
	return root
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// operatorTurn is a turn the host's operator posted.
func operatorTurn(message string) Turn {
	return Turn{
		ConversationID: "conv-1", TurnID: "turn-1", PosterAccountID: "acct-op", PosterLogin: "op",
		PosterIsOperator: true, ReadableRepos: []Repo{{"acme", "api"}}, Message: message,
	}
}

// teammateTurn is a turn a teammate posted.
func teammateTurn(account, login, message string, readable ...Repo) Turn {
	return Turn{
		ConversationID: "conv-1", TurnID: "turn-2", PosterAccountID: account, PosterLogin: login,
		ReadableRepos: readable, Message: message,
	}
}

func toolset(t *testing.T, root string, forge Forge, turn Turn) *Toolset {
	t.Helper()
	s := NewToolset(Env{Root: root, RepoRoots: map[string]string{"acme/api": root}, Forge: forge}, turn)
	t.Cleanup(s.Close)
	return s
}

func call(t *testing.T, s *Toolset, name string, args any) (string, *Error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return s.Call(context.Background(), name, raw)
}

// envelope decodes an untrusted-data result.
func envelope(t *testing.T, text string) (notice string, content map[string]any) {
	t.Helper()
	var e struct {
		Notice  string         `json:"notice"`
		Content map[string]any `json:"content"`
	}
	if err := json.Unmarshal([]byte(text), &e); err != nil {
		t.Fatalf("result is not the data envelope: %v\n%s", err, text)
	}
	return e.Notice, e.Content
}

func code(err *Error) string {
	if err == nil {
		return ""
	}
	return err.Code
}

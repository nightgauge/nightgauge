package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
	gh "github.com/nightgauge/nightgauge/internal/github"
)

// TestPRMerge_FromHomeWithExplicitTarget is #2423: run from a $HOME with a
// machine-tier ~/.nightgauge/config.yaml (no owner, machine-owned keys), no
// project config and no git remote, `pr merge N --repo owner/name --owner X`
// must reach the named repository instead of failing to load the machine file
// as a project config.
func TestPRMerge_FromHomeWithExplicitTarget(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("NIGHTGAUGE_CONFIG_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("NIGHTGAUGE_GITHUB_API_LOG", filepath.Join(t.TempDir(), "github-api.jsonl"))
	machineDir := filepath.Join(home, ".nightgauge")
	if err := os.MkdirAll(machineDir, 0o755); err != nil {
		t.Fatal(err)
	}
	machine := "github_user: someone\nplatform:\n  enabled: false\n"
	if err := os.WriteFile(filepath.Join(machineDir, "config.yaml"), []byte(machine), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(config.SwapMachineConfigPathForTest(func() (string, error) {
		return filepath.Join(machineDir, "config.yaml"), nil
	}))
	t.Chdir(home)

	var mu sync.Mutex
	var queried []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Variables map[string]any `json:"variables"`
		}
		_ = json.Unmarshal(body, &req)
		mu.Lock()
		queried = append(queried, req.Variables)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequest":{"id":"PR_1","number":24,"state":"MERGED","labels":{"nodes":[]}}}}}`))
	}))
	t.Cleanup(srv.Close)

	var clientOwner string
	prev := newClientFromConfigFn
	newClientFromConfigFn = func(_ gh.TokenResolver, owner, _ string) (*gh.Client, error) {
		clientOwner = owner
		return gh.NewClientWithHTTPClient(&http.Client{Transport: rewriteTransport{srv}}), nil
	}
	t.Cleanup(func() { newClientFromConfigFn = prev })

	cmd := rootCmd()
	cmd.SetArgs([]string{"pr", "merge", "24", "--repo", "acme/widget", "--owner", "acme"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("pr merge from $HOME: %v", err)
	}
	if clientOwner != "acme" {
		t.Errorf("client resolved for owner %q, want the explicit acme", clientOwner)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(queried) == 0 {
		t.Fatal("no request reached the forge")
	}
	if queried[0]["owner"] != "acme" || queried[0]["name"] != "widget" {
		t.Errorf("GetPR variables = %v, want owner acme, name widget", queried[0])
	}
}

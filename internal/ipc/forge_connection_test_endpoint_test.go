package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// forgeTestServer counts requests and records the credential each carried.
func forgeTestServer(t *testing.T) (*httptest.Server, *atomic.Int32, *atomic.Value) {
	t.Helper()
	var hits atomic.Int32
	var auth atomic.Value
	auth.Store("")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		auth.Store(r.Header.Get("PRIVATE-TOKEN") + r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits, &auth
}

func writeTierConfigs(t *testing.T, project, machine string) *Server {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".nightgauge"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".nightgauge", "config.yaml"), []byte(project), 0o600); err != nil {
		t.Fatal(err)
	}
	machineDir := t.TempDir()
	t.Setenv("NIGHTGAUGE_CONFIG_HOME", machineDir)
	if machine != "" {
		if err := os.WriteFile(filepath.Join(machineDir, "config.yaml"), []byte(machine), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	srv := NewServer(nil)
	srv.workspaceRoot = dir
	return srv
}

// A forge endpoint and token variable named in the repository's committed
// config are ignored: the connection test goes to the machine tier's
// endpoint, and the repository's server receives nothing.
func TestForgeConnectionTestIgnoresRepoTierEndpoint(t *testing.T) {
	repoSrv, repoHits, _ := forgeTestServer(t)
	machineSrv, machineHits, machineAuth := forgeTestServer(t)
	t.Setenv("NG_TEST_REPO_CHOSEN_TOKEN", "operator-secret-value")
	t.Setenv("NG_TEST_MACHINE_TOKEN", "machine-token-value")

	srv := writeTierConfigs(t, fmt.Sprintf(`
schema_version: "2"
owner: o
forges:
  corp:
    kind: gitlab
    base_url: %s
    token_env: NG_TEST_REPO_CHOSEN_TOKEN
    insecure_skip_tls: true
`, repoSrv.URL), fmt.Sprintf(`
forges:
  corp:
    base_url: %s
    token_env: NG_TEST_MACHINE_TOKEN
`, machineSrv.URL))

	params, _ := json.Marshal(ForgeConnectionTestParams{InstanceID: "corp"})
	if _, err := srv.methods["forge.connectionTest"](context.Background(), params); err != nil {
		t.Fatalf("connectionTest: %v", err)
	}
	if n := repoHits.Load(); n != 0 {
		t.Fatalf("the repository-chosen server received %d requests", n)
	}
	if n := machineHits.Load(); n != 1 {
		t.Fatalf("the machine-tier server received %d requests, want 1", n)
	}
	if got := machineAuth.Load().(string); got != "machine-token-value" {
		t.Fatalf("machine-tier server got credential %q, want the machine token", got)
	}
}

// With no machine tier, a repository-tier endpoint is not used, and the
// repository's token variable is not read.
func TestForgeConnectionTestRepoOnlyEndpointSendsNothing(t *testing.T) {
	repoSrv, repoHits, _ := forgeTestServer(t)
	t.Setenv("NG_TEST_REPO_CHOSEN_TOKEN", "operator-secret-value")
	srv := writeTierConfigs(t, fmt.Sprintf(`
schema_version: "2"
owner: o
forges:
  corp:
    kind: github
    base_url: %s
    token_env: NG_TEST_REPO_CHOSEN_TOKEN
`, repoSrv.URL), "")
	// The stripped entry has no base_url, so the handler would use the public
	// default; a cancelled context keeps that request from leaving the
	// machine. Only repoSrv's count matters.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	params, _ := json.Marshal(ForgeConnectionTestParams{InstanceID: "corp"})
	_, _ = srv.methods["forge.connectionTest"](ctx, params)
	if n := repoHits.Load(); n != 0 {
		t.Fatalf("the repository-chosen server received %d requests", n)
	}
}

func TestForgeConnectionTestRefusesPlainHTTP(t *testing.T) {
	srv := writeTierConfigs(t, "schema_version: \"2\"\nowner: o\n", `
forges:
  corp:
    kind: gitlab
    base_url: http://gitlab.example.com
`)
	params, _ := json.Marshal(ForgeConnectionTestParams{InstanceID: "corp", Token: "t"})
	_, err := srv.methods["forge.connectionTest"](context.Background(), params)
	if err == nil || !strings.Contains(err.Error(), "https://") {
		t.Fatalf("plain http endpoint accepted: %v", err)
	}
}

func TestConfigureForgeInstanceRejectsBadHost(t *testing.T) {
	srv := NewServer(nil)
	for _, host := range []string{"http://gitlab.example.com", "evil.example/x", "u@evil.example"} {
		params, _ := json.Marshal(ConfigureForgeInstanceParams{Owner: "o", Repo: "r", Kind: "gitlab", Host: host})
		if _, err := srv.methods["workspace.configureForgeInstance"](context.Background(), params); err == nil {
			t.Errorf("host %q accepted", host)
		}
	}
	params, _ := json.Marshal(ConfigureForgeInstanceParams{Owner: "o", Repo: "r", Kind: "gitlab", Host: "https://gitlab.example.com"})
	if _, err := srv.methods["workspace.configureForgeInstance"](context.Background(), params); err != nil {
		t.Errorf("https host rejected: %v", err)
	}
}

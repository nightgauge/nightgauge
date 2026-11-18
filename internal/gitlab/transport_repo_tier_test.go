package gitlab

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
)

// A repository's committed config cannot turn off TLS verification: its
// insecure_skip_tls is stripped before the merge, so the transport built from
// the loaded entry verifies certificates.
func TestRepoTierInsecureSkipTLSDoesNotReachTransport(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".nightgauge"), 0o755); err != nil {
		t.Fatal(err)
	}
	project := "schema_version: \"2\"\nowner: o\nforges:\n  corp:\n    kind: gitlab\n    insecure_skip_tls: true\n"
	if err := os.WriteFile(filepath.Join(dir, ".nightgauge", "config.yaml"), []byte(project), 0o600); err != nil {
		t.Fatal(err)
	}
	machineDir := t.TempDir()
	t.Setenv("NIGHTGAUGE_CONFIG_HOME", machineDir)
	machine := "forges:\n  corp:\n    base_url: https://gitlab.example.com\n"
	if err := os.WriteFile(filepath.Join(machineDir, "config.yaml"), []byte(machine), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	entry := cfg.Forges["corp"]
	if entry == nil {
		t.Fatal("forges.corp missing")
	}
	client, err := BuildTransport(*entry, filepath.Join(dir, ".nightgauge"))
	if err != nil {
		t.Fatalf("BuildTransport: %v", err)
	}
	tr, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type %T", client.Transport)
	}
	if tr.TLSClientConfig == nil || tr.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("a repository-tier insecure_skip_tls reached tls.Config.InsecureSkipVerify")
	}
}

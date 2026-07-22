package ipc

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestConfigureForgeInstance_GitHub verifies the workspace.configureForgeInstance
// handler accepts kind="github" and stores the config in the in-memory
// registry, where ForgeInstanceFor() can read it back.
func TestConfigureForgeInstance_GitHub(t *testing.T) {
	srv := NewServer(nil)
	handler, ok := srv.methods["workspace.configureForgeInstance"]
	if !ok {
		t.Fatal("workspace.configureForgeInstance handler not registered")
	}

	params, _ := json.Marshal(ConfigureForgeInstanceParams{
		Owner: "nightgauge",
		Repo:  "nightgauge",
		Kind:  "github",
		Host:  "github.com",
	})
	got, err := handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	res, ok := got.(*ConfigureForgeInstanceResult)
	if !ok {
		t.Fatalf("result type = %T, want *ConfigureForgeInstanceResult", got)
	}
	if !res.OK {
		t.Error("OK = false, want true")
	}
	if res.Kind != "github" {
		t.Errorf("Kind = %q, want github", res.Kind)
	}

	cfg, found := srv.ForgeInstanceFor("nightgauge", "nightgauge")
	if !found {
		t.Fatal("registry did not record the configured instance")
	}
	if cfg.Kind != "github" {
		t.Errorf("registry.Kind = %q, want github", cfg.Kind)
	}
	if cfg.Host != "github.com" {
		t.Errorf("registry.Host = %q, want github.com", cfg.Host)
	}
}

// TestConfigureForgeInstance_GitLab verifies kind="gitlab" is accepted on the
// same surface.
func TestConfigureForgeInstance_GitLab(t *testing.T) {
	srv := NewServer(nil)
	handler := srv.methods["workspace.configureForgeInstance"]

	params, _ := json.Marshal(ConfigureForgeInstanceParams{
		Owner: "acme",
		Repo:  "platform",
		Kind:  "gitlab",
		Host:  "gitlab.com",
	})
	got, err := handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	res := got.(*ConfigureForgeInstanceResult)
	if res.Kind != "gitlab" {
		t.Errorf("Kind = %q, want gitlab", res.Kind)
	}

	cfg, _ := srv.ForgeInstanceFor("acme", "platform")
	if cfg.Kind != "gitlab" {
		t.Errorf("registry.Kind = %q, want gitlab", cfg.Kind)
	}
}

// TestConfigureForgeInstance_RejectsUnknownKind verifies the handler rejects
// any kind other than "github" or "gitlab" — the contract is "fail loudly
// rather than silently default to a forge".
func TestConfigureForgeInstance_RejectsUnknownKind(t *testing.T) {
	srv := NewServer(nil)
	handler := srv.methods["workspace.configureForgeInstance"]

	params, _ := json.Marshal(ConfigureForgeInstanceParams{
		Owner: "o",
		Repo:  "r",
		Kind:  "bitbucket",
	})
	_, err := handler(context.Background(), params)
	if err == nil {
		t.Fatal("expected error for unknown kind, got nil")
	}
	if !strings.Contains(err.Error(), "github") || !strings.Contains(err.Error(), "gitlab") {
		t.Errorf("err = %v, want message naming valid kinds", err)
	}
}

// TestConfigureForgeInstance_RejectsMissingOwnerRepo verifies the handler
// rejects empty owner / repo — the registry key would be "/" otherwise.
func TestConfigureForgeInstance_RejectsMissingOwnerRepo(t *testing.T) {
	srv := NewServer(nil)
	handler := srv.methods["workspace.configureForgeInstance"]

	cases := []ConfigureForgeInstanceParams{
		{Owner: "", Repo: "r", Kind: "github"},
		{Owner: "o", Repo: "", Kind: "github"},
	}
	for _, p := range cases {
		raw, _ := json.Marshal(p)
		if _, err := handler(context.Background(), raw); err == nil {
			t.Errorf("params=%+v: expected error, got nil", p)
		}
	}
}

// TestConfigureForgeInstance_OverwritesExistingEntry verifies repeated calls
// for the same (owner, repo) replace rather than append.
func TestConfigureForgeInstance_OverwritesExistingEntry(t *testing.T) {
	srv := NewServer(nil)
	handler := srv.methods["workspace.configureForgeInstance"]

	first, _ := json.Marshal(ConfigureForgeInstanceParams{Owner: "o", Repo: "r", Kind: "github"})
	if _, err := handler(context.Background(), first); err != nil {
		t.Fatalf("first call: %v", err)
	}
	second, _ := json.Marshal(ConfigureForgeInstanceParams{Owner: "o", Repo: "r", Kind: "gitlab", Host: "gitlab.example.com"})
	if _, err := handler(context.Background(), second); err != nil {
		t.Fatalf("second call: %v", err)
	}

	cfg, _ := srv.ForgeInstanceFor("o", "r")
	if cfg.Kind != "gitlab" {
		t.Errorf("after overwrite, Kind = %q, want gitlab", cfg.Kind)
	}
	if cfg.Host != "gitlab.example.com" {
		t.Errorf("after overwrite, Host = %q, want gitlab.example.com", cfg.Host)
	}
}

// TestForgeInstanceFor_ReturnsFalseForUnknownRepo verifies the accessor's
// found flag is false when no entry has been registered.
func TestForgeInstanceFor_ReturnsFalseForUnknownRepo(t *testing.T) {
	srv := NewServer(nil)
	if _, found := srv.ForgeInstanceFor("nobody", "nothing"); found {
		t.Error("ForgeInstanceFor returned found=true for unregistered repo")
	}
}

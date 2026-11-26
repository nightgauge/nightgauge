package main

import (
	"bytes"
	"encoding/json"
	"testing"
)

// recheck-blocker prints the full JSON contract and exits 0 when the re-check
// could not run (no closed blocker named), and refuses a bad issue (#3618).
func TestPrStageRecheckBlocker_JSONShape(t *testing.T) {
	_, clone := refreshRepos(t)
	cmd := prStageRecheckBlockerCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	// No #N reference: no forge read and no git process.
	cmd.SetArgs([]string{"42", "--workdir", clone, "--repo", "acme/x", "--detail", "BLOCKED — nothing named"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("recheck-blocker: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output %q: %v", buf.String(), err)
	}
	for _, k := range []string{"ran", "rerun", "closed", "note", "stop_reason", "conflict_files"} {
		if _, ok := got[k]; !ok {
			t.Fatalf("key %q missing from %s", k, buf.String())
		}
	}
	if got["ran"] != false {
		t.Fatalf("ran = %v, want false", got["ran"])
	}
	if c, ok := got["closed"].([]any); !ok || len(c) != 0 {
		t.Fatalf("closed = %v, want []", got["closed"])
	}
}

func TestPrStageRecheckBlocker_UsageErrors(t *testing.T) {
	_, clone := refreshRepos(t)
	for name, args := range map[string][]string{
		"bad issue":      {"nope"},
		"malformed repo": {"42", "--workdir", clone, "--repo", "a/b/../../x", "--detail", "#7"},
	} {
		cmd := prStageRecheckBlockerCmd()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

// With no --repo and an origin that names no forge repository, the verb says
// so on stderr and prints ran:false without a forge read.
func TestPrStageRecheckBlocker_NoRepoResolvable(t *testing.T) {
	_, clone := refreshRepos(t) // origin is a local path, not owner/name
	cmd := prStageRecheckBlockerCmd()
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs([]string{"42", "--workdir", clone, "--detail", "BLOCKED on #7"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("recheck-blocker: %v", err)
	}
	var got struct {
		Ran bool `json:"ran"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.Ran {
		t.Fatalf("output %q (%v), want ran:false", out.String(), err)
	}
	if !bytes.Contains(errb.Bytes(), []byte("no repository resolvable")) {
		t.Fatalf("stderr %q does not explain the skip", errb.String())
	}
}

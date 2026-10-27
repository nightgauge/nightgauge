package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/preflight"
	"github.com/spf13/cobra"
)

func runACFingerprint(t *testing.T, stdin string, args ...string) (acFingerprintReport, error) {
	t.Helper()
	cmd := preflightACFingerprintCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(append(args, "--json"))
	if err := cmd.Execute(); err != nil {
		return acFingerprintReport{}, err
	}
	var r acFingerprintReport
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatalf("decode %q: %v", out.String(), err)
	}
	return r, nil
}

func TestPreflightACFingerprintStdin(t *testing.T) {
	body := "- [ ] one\n- [x] two\n"
	r, err := runACFingerprint(t, body, "7", "--body-file", "-")
	if err != nil {
		t.Fatal(err)
	}
	if r.IssueNumber != 7 || r.CriteriaCount != 2 || r.ACFingerprint != preflight.ACFingerprint(body) {
		t.Errorf("report = %+v", r)
	}
}

func TestPreflightACFingerprintForgeRead(t *testing.T) {
	orig := readIssueBodyForFingerprint
	t.Cleanup(func() { readIssueBodyForFingerprint = orig })
	var gotRepo string
	readIssueBodyForFingerprint = func(_ *cobra.Command, repo string, n int) (string, error) {
		gotRepo = repo
		return "- [ ] from forge\n", nil
	}
	r, err := runACFingerprint(t, "", "9", "--repo", "o/r")
	if err != nil {
		t.Fatal(err)
	}
	if gotRepo != "o/r" || r.ACFingerprint != preflight.ACFingerprint("- [ ] from forge\n") {
		t.Errorf("repo=%q report=%+v", gotRepo, r)
	}

	readIssueBodyForFingerprint = func(*cobra.Command, string, int) (string, error) {
		return "", errors.New("boom")
	}
	if _, err := runACFingerprint(t, "", "9"); err == nil {
		t.Error("forge read failure must fail the verb")
	}
}

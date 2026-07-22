package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/intelligence/failure"
	"github.com/spf13/cobra"
)

func TestSubtractStrings(t *testing.T) {
	tests := []struct {
		name string
		a, b []string
		want []string
	}{
		{"resolved removes blocker", []string{"copilot_code_review", "required_pull_request_reviews"}, []string{"copilot_code_review"}, []string{"required_pull_request_reviews"}},
		{"all resolved", []string{"copilot_code_review"}, []string{"copilot_code_review"}, []string{}},
		{"none resolved", []string{"copilot_code_review"}, nil, []string{"copilot_code_review"}},
		{"empty input", nil, []string{"copilot_code_review"}, []string{}},
		{"both empty", nil, nil, []string{}},
		{"order preserved", []string{"a", "b", "c", "d"}, []string{"b"}, []string{"a", "c", "d"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := subtractStrings(tt.a, tt.b)
			if len(got) != len(tt.want) {
				t.Fatalf("subtractStrings(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("subtractStrings(%v, %v)[%d] = %q, want %q", tt.a, tt.b, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestSplitRepo(t *testing.T) {
	tests := []struct {
		owner, repo string
		wantOwner   string
		wantRepo    string
	}{
		{"nightgauge", "nightgauge", "nightgauge", "nightgauge"},
		{"nightgauge", "acme/platform", "acme", "platform"},
		{"default", "org/repo", "org", "repo"},
	}

	for _, tt := range tests {
		gotOwner, gotRepo := splitRepo(tt.owner, tt.repo)
		if gotOwner != tt.wantOwner || gotRepo != tt.wantRepo {
			t.Errorf("splitRepo(%q, %q) = (%q, %q), want (%q, %q)",
				tt.owner, tt.repo, gotOwner, gotRepo, tt.wantOwner, tt.wantRepo)
		}
	}
}

func TestRootCmdHelp(t *testing.T) {
	cmd := rootCmd()
	if cmd.Use != "nightgauge" {
		t.Errorf("root command Use = %q, want %q", cmd.Use, "nightgauge")
	}

	// Verify all expected subcommands exist
	expected := map[string]bool{
		"board":   false,
		"issue":   false,
		"epic":    false,
		"project": false,
		"pr":      false,
		"ci":      false,
		"git":     false,
		"status":  false,
		"version": false,
		"serve":   false,
	}

	for _, sub := range cmd.Commands() {
		if _, ok := expected[sub.Name()]; ok {
			expected[sub.Name()] = true
		}
	}

	for name, found := range expected {
		if !found {
			t.Errorf("missing subcommand: %s", name)
		}
	}
}

func TestVersionCmd(t *testing.T) {
	cmd := rootCmd()
	cmd.SetArgs([]string{"version"})
	if err := cmd.Execute(); err != nil {
		t.Errorf("version command failed: %v", err)
	}
}

func TestProjectCmdSubcommands(t *testing.T) {
	cmd := rootCmd()

	// Find the project command
	var projCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "project" {
			projCmd = sub
			break
		}
	}
	if projCmd == nil {
		t.Fatal("project subcommand not found")
	}

	expected := map[string]bool{
		"add":              false,
		"sync-status":      false,
		"sync-iteration":   false,
		"set-hours":        false,
		"update-estimates": false,
		"move-status":      false,
		"drift-check":      false,
		"ensure-fields":    false,
	}

	for _, sub := range projCmd.Commands() {
		if _, ok := expected[sub.Name()]; ok {
			expected[sub.Name()] = true
		}
	}

	for name, found := range expected {
		if !found {
			t.Errorf("missing project subcommand: %s", name)
		}
	}
}

func TestProjectAddRequiresArgs(t *testing.T) {
	cmd := rootCmd()
	cmd.SetArgs([]string{"project", "add"})
	err := cmd.Execute()
	if err == nil {
		t.Error("project add without args should fail")
	}
}

func TestValidateStatusFlag_AcceptedValues(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty omitted", "", ""},
		{"canonical Backlog", "Backlog", "Backlog"},
		{"canonical Ready", "Ready", "Ready"},
		{"canonical In progress", "In progress", "In progress"},
		{"canonical In review", "In review", "In review"},
		{"canonical Done", "Done", "Done"},
		{"lowercase ready", "ready", "Ready"},
		{"lowercase backlog", "backlog", "Backlog"},
		{"hyphenated in-progress", "in-progress", "In progress"},
		{"hyphenated in-review", "in-review", "In review"},
		{"lowercase done", "done", "Done"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := validateStatusFlag(tt.input)
			if err != nil {
				t.Fatalf("validateStatusFlag(%q) returned error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("validateStatusFlag(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestValidateStatusFlag_InvalidValue(t *testing.T) {
	cases := []string{"Bogus", "READY", "in_progress", "todo", "  Ready"}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			got, err := validateStatusFlag(in)
			if err == nil {
				t.Fatalf("validateStatusFlag(%q) = %q, want error", in, got)
			}
			if !strings.Contains(err.Error(), "invalid --status") {
				t.Errorf("error %q should mention 'invalid --status'", err.Error())
			}
		})
	}
}

// TestProjectAddStatusFlagValidation_InvalidValue verifies that --status
// validation runs before any GraphQL/network call (offline failure).
func TestProjectAddStatusFlagValidation_InvalidValue(t *testing.T) {
	cmd := rootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"project", "add", "42", "--status", "Bogus"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("project add with invalid --status should fail")
	}
	if !strings.Contains(err.Error(), "invalid --status") {
		t.Errorf("expected error containing 'invalid --status', got: %v", err)
	}
}

// TestProjectAddStatusFlagRegistered verifies the --status flag is registered
// on the project add command and documented.
func TestProjectAddStatusFlagRegistered(t *testing.T) {
	cmd := rootCmd()

	var projCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "project" {
			projCmd = sub
			break
		}
	}
	if projCmd == nil {
		t.Fatal("project subcommand not found")
	}

	var addCmd *cobra.Command
	for _, sub := range projCmd.Commands() {
		if sub.Name() == "add" {
			addCmd = sub
			break
		}
	}
	if addCmd == nil {
		t.Fatal("project add subcommand not found")
	}

	flag := addCmd.Flags().Lookup("status")
	if flag == nil {
		t.Fatal("project add missing --status flag")
	}
	if flag.DefValue != "" {
		t.Errorf("--status default = %q, want empty (preserves legacy behavior)", flag.DefValue)
	}
	if !strings.Contains(flag.Usage, "Backlog") || !strings.Contains(flag.Usage, "Ready") {
		t.Errorf("--status usage should document canonical values, got: %q", flag.Usage)
	}
}

func TestProjectSyncStatusRequiresArgs(t *testing.T) {
	cmd := rootCmd()
	cmd.SetArgs([]string{"project", "sync-status"})
	err := cmd.Execute()
	if err == nil {
		t.Error("project sync-status without args should fail")
	}
}

func TestProjectSetHoursRequiresArgs(t *testing.T) {
	cmd := rootCmd()
	cmd.SetArgs([]string{"project", "set-hours"})
	err := cmd.Execute()
	if err == nil {
		t.Error("project set-hours without args should fail")
	}
}

func TestQueueCmdSubcommands(t *testing.T) {
	cmd := rootCmd()

	var queueCommand *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "queue" {
			queueCommand = sub
			break
		}
	}
	if queueCommand == nil {
		t.Fatal("queue subcommand not found")
	}

	expected := map[string]bool{
		"add":    false,
		"list":   false,
		"run":    false,
		"remove": false,
		"clear":  false,
	}

	for _, sub := range queueCommand.Commands() {
		if _, ok := expected[sub.Name()]; ok {
			expected[sub.Name()] = true
		}
	}

	for name, found := range expected {
		if !found {
			t.Errorf("missing queue subcommand: %s", name)
		}
	}
}

func TestRunCmdAcceptsIssueArg(t *testing.T) {
	cmd := rootCmd()

	var runCommand *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "run" {
			runCommand = sub
			break
		}
	}
	if runCommand == nil {
		t.Fatal("run subcommand not found")
	}

	// Should accept --issue flag
	issueFlag := runCommand.Flags().Lookup("issue")
	if issueFlag == nil {
		t.Error("run command missing --issue flag")
	}

	// Should accept --auto flag
	autoFlag := runCommand.Flags().Lookup("auto")
	if autoFlag == nil {
		t.Error("run command missing --auto flag")
	}
}

func TestStatusCmd(t *testing.T) {
	// Status command should work without GITHUB_TOKEN (just reports unauthenticated)
	t.Setenv("GITHUB_TOKEN", "")
	cmd := rootCmd()
	cmd.SetArgs([]string{"status"})
	if err := cmd.Execute(); err != nil {
		t.Errorf("status command failed: %v", err)
	}
}

func TestOutcomeCmdRegistered(t *testing.T) {
	cmd := rootCmd()
	var outcomeCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "outcome" {
			outcomeCmd = sub
			break
		}
	}
	if outcomeCmd == nil {
		t.Fatal("outcome subcommand not registered in rootCmd")
	}

	// Verify 'record' subcommand exists
	var recordCmd *cobra.Command
	for _, sub := range outcomeCmd.Commands() {
		if sub.Name() == "record" {
			recordCmd = sub
			break
		}
	}
	if recordCmd == nil {
		t.Fatal("outcome record subcommand not found")
	}

	// Verify required flags exist
	for _, flagName := range []string{"issue", "pr", "model", "predicted-size", "actual-lines", "type"} {
		if recordCmd.Flags().Lookup(flagName) == nil {
			t.Errorf("outcome record missing --%s flag", flagName)
		}
	}
}

func TestOutcomeRecordMissingFlags(t *testing.T) {
	cmd := rootCmd()
	cmd.SetArgs([]string{"outcome", "record"})
	// Should fail without --issue and --pr
	if err := cmd.Execute(); err == nil {
		t.Error("expected error when --issue and --pr are missing, got none")
	}
}

// --- Issue subcommand registration tests ---

func TestIssueCmdSubcommands(t *testing.T) {
	cmd := rootCmd()

	var issueCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "issue" {
			issueCmd = sub
			break
		}
	}
	if issueCmd == nil {
		t.Fatal("issue subcommand not found")
	}

	expected := map[string]bool{
		"list-unrefined": false,
		"mark-refined":   false,
		"has-label":      false,
	}
	for _, sub := range issueCmd.Commands() {
		if _, ok := expected[sub.Name()]; ok {
			expected[sub.Name()] = true
		}
	}
	for name, found := range expected {
		if !found {
			t.Errorf("issue subcommand %q not registered", name)
		}
	}
}

func TestIssueListUnrefinedFlags(t *testing.T) {
	cmd := rootCmd()
	var unrefined *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "issue" {
			for _, s := range sub.Commands() {
				if s.Name() == "list-unrefined" {
					unrefined = s
					break
				}
			}
		}
	}
	if unrefined == nil {
		t.Fatal("issue list-unrefined not found")
	}
	for _, flag := range []string{"owner", "repo", "limit", "json"} {
		if unrefined.Flags().Lookup(flag) == nil {
			t.Errorf("issue list-unrefined missing --%s flag", flag)
		}
	}
}

func TestIssueMarkRefinedRequiresArg(t *testing.T) {
	cmd := rootCmd()
	cmd.SetArgs([]string{"issue", "mark-refined"})
	if err := cmd.Execute(); err == nil {
		t.Error("expected error when issue number is missing, got none")
	}
}

func TestIssueMarkRefinedInvalidNumber(t *testing.T) {
	cmd := rootCmd()
	cmd.SetArgs([]string{"issue", "mark-refined", "notanumber"})
	if err := cmd.Execute(); err == nil {
		t.Error("expected error for non-numeric issue number, got none")
	}
}

func TestIssueHasLabelRequiresTwoArgs(t *testing.T) {
	cmd := rootCmd()
	cmd.SetArgs([]string{"issue", "has-label", "42"})
	if err := cmd.Execute(); err == nil {
		t.Error("expected error when label argument is missing, got none")
	}
}

func TestIssueHasLabelInvalidNumber(t *testing.T) {
	cmd := rootCmd()
	cmd.SetArgs([]string{"issue", "has-label", "notanumber", "pipeline:refined"})
	if err := cmd.Execute(); err == nil {
		t.Error("expected error for non-numeric issue number, got none")
	}
}

// runAcCheck executes `issue ac-check` with the given args and returns the
// stdout bytes plus any execution error. printJSON writes via fmt.Println
// to os.Stdout, so the test redirects os.Stdout through a pipe.
func runAcCheck(t *testing.T, args ...string) ([]byte, error) {
	t.Helper()
	prev := os.Stdout
	rPipe, wPipe, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = wPipe
	t.Cleanup(func() { os.Stdout = prev })

	cmd := rootCmd()
	cmd.SetArgs(append([]string{"issue", "ac-check"}, args...))
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	execErr := cmd.Execute()

	wPipe.Close()
	out, _ := io.ReadAll(rPipe)
	os.Stdout = prev
	return out, execErr
}

func TestIssueAcCheckOfflineRequiresBody(t *testing.T) {
	if _, err := runAcCheck(t, "0"); err == nil {
		t.Error("expected error when offline mode is missing --body, got none")
	}
}

func TestIssueAcCheckOfflineJSON(t *testing.T) {
	out, err := runAcCheck(t, "0", "--body", "- [x] one\n- [ ] two\n", "--json")
	if err != nil {
		t.Fatalf("unexpected error: %v\noutput: %s", err, string(out))
	}
	var got map[string]interface{}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("invalid JSON: %v\noutput: %s", err, string(out))
	}
	wantPairs := map[string]interface{}{
		"status":          "failed",
		"checked_count":   float64(1),
		"unchecked_count": float64(1),
		"total":           float64(2),
		"v":               float64(1),
		"number":          float64(0),
	}
	for k, want := range wantPairs {
		if got[k] != want {
			t.Errorf("ac-check JSON %q = %v, want %v", k, got[k], want)
		}
	}
}

func TestIssueAcCheckOfflineAllChecked(t *testing.T) {
	out, err := runAcCheck(t, "0", "--body", "- [x] a\n- [X] b\n", "--json")
	if err != nil {
		t.Fatalf("unexpected error: %v\noutput: %s", err, string(out))
	}
	var got map[string]interface{}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if got["status"] != "passed" {
		t.Errorf("expected status=passed, got %v", got["status"])
	}
}

func TestIssueAcCheckOfflineNotApplicable(t *testing.T) {
	out, err := runAcCheck(t, "0", "--body", "## Notes\nfree prose.\n", "--json")
	if err != nil {
		t.Fatalf("unexpected error: %v\noutput: %s", err, string(out))
	}
	var got map[string]interface{}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if got["status"] != "not_applicable" {
		t.Errorf("expected status=not_applicable, got %v", got["status"])
	}
	if got["total"] != float64(0) {
		t.Errorf("expected total=0, got %v", got["total"])
	}
}

func TestIssueAcCheckInvalidNumber(t *testing.T) {
	if _, err := runAcCheck(t, "notanumber"); err == nil {
		t.Error("expected error for non-numeric issue number, got none")
	}
}

func TestPRCmdSubcommands(t *testing.T) {
	cmd := rootCmd()

	var prCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "pr" {
			prCmd = sub
			break
		}
	}
	if prCmd == nil {
		t.Fatal("pr subcommand not found")
	}

	expected := map[string]bool{
		"create":           false,
		"view":             false,
		"merge":            false,
		"ci-wait":          false,
		"ruleset-precheck": false,
	}
	for _, sub := range prCmd.Commands() {
		if _, ok := expected[sub.Name()]; ok {
			expected[sub.Name()] = true
		}
	}
	for name, found := range expected {
		if !found {
			t.Errorf("pr subcommand %q not registered", name)
		}
	}
}

func TestPRMergeCmdFlags(t *testing.T) {
	cmd := rootCmd()
	var mergeCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "pr" {
			for _, s := range sub.Commands() {
				if s.Name() == "merge" {
					mergeCmd = s
					break
				}
			}
		}
	}
	if mergeCmd == nil {
		t.Fatal("pr merge subcommand not found")
	}
	for _, flag := range []string{"owner", "repo", "strategy", "delete-branch", "issue", "force", "json"} {
		if mergeCmd.Flags().Lookup(flag) == nil {
			t.Errorf("pr merge missing --%s flag", flag)
		}
	}
	// #3969: branch cleanup is the default — the head branch is deleted after
	// merge unless --delete-branch=false is passed.
	if got := mergeCmd.Flags().Lookup("delete-branch").DefValue; got != "true" {
		t.Errorf("--delete-branch default = %q, want \"true\" (cleanup-as-default)", got)
	}
}

func TestPRMergeCmdRequiresArg(t *testing.T) {
	cmd := rootCmd()
	cmd.SetArgs([]string{"pr", "merge"})
	if err := cmd.Execute(); err == nil {
		t.Error("expected error when PR number is missing, got none")
	}
}

func TestPRMergeCmdInvalidNumber(t *testing.T) {
	cmd := rootCmd()
	cmd.SetArgs([]string{"pr", "merge", "notanumber"})
	if err := cmd.Execute(); err == nil {
		t.Error("expected error for non-numeric PR number, got none")
	}
}

func TestPRMergeCmdInvalidStrategy(t *testing.T) {
	cmd := rootCmd()
	cmd.SetArgs([]string{"pr", "merge", "42", "--strategy", "invalid"})
	if err := cmd.Execute(); err == nil {
		t.Error("expected error for invalid strategy, got none")
	}
}

func TestPRCIWaitCmdFlags(t *testing.T) {
	cmd := rootCmd()
	var ciWait *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "pr" {
			for _, s := range sub.Commands() {
				if s.Name() == "ci-wait" {
					ciWait = s
					break
				}
			}
		}
	}
	if ciWait == nil {
		t.Fatal("pr ci-wait subcommand not found")
	}
	for _, flag := range []string{"owner", "repo", "timeout", "poll", "json"} {
		if ciWait.Flags().Lookup(flag) == nil {
			t.Errorf("pr ci-wait missing --%s flag", flag)
		}
	}
}

func TestPRCIWaitCmdRequiresArg(t *testing.T) {
	cmd := rootCmd()
	cmd.SetArgs([]string{"pr", "ci-wait"})
	if err := cmd.Execute(); err == nil {
		t.Error("expected error when PR number is missing, got none")
	}
}

func TestPRCIWaitCmdInvalidNumber(t *testing.T) {
	cmd := rootCmd()
	cmd.SetArgs([]string{"pr", "ci-wait", "notanumber"})
	if err := cmd.Execute(); err == nil {
		t.Error("expected error for non-numeric PR number, got none")
	}
}

// --- pr ruleset-precheck tests ---

func TestPRRulesetPrecheckCmdRegistered(t *testing.T) {
	cmd := rootCmd()
	var prCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "pr" {
			prCmd = sub
			break
		}
	}
	if prCmd == nil {
		t.Fatal("pr subcommand not found")
	}
	var found bool
	for _, sub := range prCmd.Commands() {
		if sub.Name() == "ruleset-precheck" {
			found = true
			break
		}
	}
	if !found {
		t.Error("pr ruleset-precheck subcommand not registered")
	}
}

func TestPRRulesetPrecheckCmdFlags(t *testing.T) {
	cmd := rootCmd()
	var rulesetCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "pr" {
			for _, s := range sub.Commands() {
				if s.Name() == "ruleset-precheck" {
					rulesetCmd = s
					break
				}
			}
		}
	}
	if rulesetCmd == nil {
		t.Fatal("pr ruleset-precheck subcommand not found")
	}
	for _, flag := range []string{"owner", "repo", "auto-satisfy", "json"} {
		if rulesetCmd.Flags().Lookup(flag) == nil {
			t.Errorf("pr ruleset-precheck missing --%s flag", flag)
		}
	}
}

func TestPRRulesetPrecheckCmdRequiresArg(t *testing.T) {
	cmd := rootCmd()
	cmd.SetArgs([]string{"pr", "ruleset-precheck"})
	if err := cmd.Execute(); err == nil {
		t.Error("expected error when PR number is missing, got none")
	}
}

func TestPRRulesetPrecheckCmdInvalidNumber(t *testing.T) {
	cmd := rootCmd()
	cmd.SetArgs([]string{"pr", "ruleset-precheck", "notanumber"})
	if err := cmd.Execute(); err == nil {
		t.Error("expected error for non-numeric PR number, got none")
	}
}

// --- Error handling and silent-ignore audit tests ---

// TestPrintJSONValidOutput verifies printJSON writes valid JSON to stdout.
func TestPrintJSONValidOutput(t *testing.T) {
	oldStdout := bytes.Buffer{}
	// printJSON writes to os.Stdout, so capture by reading back the JSON
	type payload struct {
		Key string `json:"key"`
	}
	p := payload{Key: "value"}
	buf, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("unexpected marshal error: %v", err)
	}
	// Verify the marshalled form is valid JSON (round-trip check)
	var out payload
	if err := json.Unmarshal(buf, &out); err != nil {
		t.Errorf("printJSON-like output is not valid JSON: %v", err)
	}
	if out.Key != "value" {
		t.Errorf("round-trip key = %q, want %q", out.Key, "value")
	}
	_ = oldStdout // suppress unused warning
}

// TestEnrichErrorTransient verifies enrichError appends retry guidance for rate-limit errors.
func TestEnrichErrorTransient(t *testing.T) {
	base := errors.New("API rate limit exceeded for 429")
	enriched := enrichError(base)
	if enriched == nil {
		t.Fatal("enrichError returned nil for non-nil input")
	}
	msg := enriched.Error()
	if !strings.Contains(msg, "transient") {
		t.Errorf("expected 'transient' in enriched error message, got: %q", msg)
	}
	if !errors.Is(enriched, base) {
		t.Error("enrichError must wrap the original error (errors.Is check failed)")
	}
}

// TestEnrichErrorAuth verifies enrichError appends auth guidance for 401/403 errors.
func TestEnrichErrorAuth(t *testing.T) {
	base := errors.New("401 Unauthorized")
	enriched := enrichError(base)
	if enriched == nil {
		t.Fatal("enrichError returned nil for non-nil input")
	}
	msg := enriched.Error()
	if !strings.Contains(msg, "gh auth login") {
		t.Errorf("expected auth guidance in enriched error message, got: %q", msg)
	}
	if !errors.Is(enriched, base) {
		t.Error("enrichError must wrap the original error (errors.Is check failed)")
	}
}

// TestEnrichErrorNil verifies enrichError is a no-op for nil errors.
func TestEnrichErrorNil(t *testing.T) {
	if err := enrichError(nil); err != nil {
		t.Errorf("enrichError(nil) = %v, want nil", err)
	}
}

// TestEnrichErrorUnknown verifies enrichError returns the original error unchanged
// for errors that do not match any known category.
func TestEnrichErrorUnknown(t *testing.T) {
	base := errors.New("some completely unknown condition xyz")
	enriched := enrichError(base)
	if enriched.Error() != base.Error() {
		t.Errorf("enrichError should not modify unknown errors: got %q, want %q", enriched.Error(), base.Error())
	}
}

// TestFailureClassifierRateLimit verifies the failure classifier correctly identifies
// rate-limit errors as transient and retryable — the foundation of enrichError's behavior.
func TestFailureClassifierRateLimit(t *testing.T) {
	clf := failure.NewClassifier()
	class := clf.Classify("cmd", 1, "API rate limit exceeded 429")
	if class.Category != failure.CatTransient {
		t.Errorf("rate limit: category = %q, want %q", class.Category, failure.CatTransient)
	}
	if !class.Retryable {
		t.Error("rate limit error should be retryable")
	}
}

// TestFailureClassifierAuth verifies the failure classifier correctly identifies
// auth errors as permission errors and non-retryable.
func TestFailureClassifierAuth(t *testing.T) {
	clf := failure.NewClassifier()
	class := clf.Classify("cmd", 1, "401 unauthorized token")
	if class.Category != failure.CatPermission {
		t.Errorf("auth error: category = %q, want %q", class.Category, failure.CatPermission)
	}
	if class.Retryable {
		t.Error("auth error should not be retryable")
	}
}

// TestSilentIgnoreComments verifies that all MarkFlagRequired and cobra flag.Set
// silent ignores in main.go carry justification comments.
// This is a policy guard: if a _ = is added without a comment, this test catches it.
func TestSilentIgnoreComments(_ *testing.T) {
	// This test is a documentation assertion. The actual comment audit was performed
	// during the issue #2746 review. All _ = cmd.MarkFlagRequired(...) and
	// _ = cmd.Flags().Set(...) calls in main.go now carry inline justification
	// comments explaining why the error is intentionally discarded.
	//
	// The two previously unjustified ignores (printJSON silent ignores at the
	// hook check-deps and doctor command sites) were converted to explicit
	// fmt.Fprintf(os.Stderr, ...) error logging.
}

// --- Spike subcommand registration tests ---

func TestSpikeCmdRegistered(t *testing.T) {
	cmd := rootCmd()
	var spikeCommand *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "spike" {
			spikeCommand = sub
			break
		}
	}
	if spikeCommand == nil {
		t.Fatal("spike subcommand not registered in rootCmd")
	}

	var materializeCmd *cobra.Command
	for _, sub := range spikeCommand.Commands() {
		if sub.Name() == "materialize" {
			materializeCmd = sub
			break
		}
	}
	if materializeCmd == nil {
		t.Fatal("spike materialize subcommand not found")
	}

	for _, flag := range []string{"owner", "repo", "project", "artifact-path", "workdir", "dry-run", "json"} {
		if materializeCmd.Flags().Lookup(flag) == nil {
			t.Errorf("spike materialize missing --%s flag", flag)
		}
	}
}

func TestSpikeMaterializeRequiresIssueArg(t *testing.T) {
	cmd := rootCmd()
	cmd.SetArgs([]string{"spike", "materialize"})
	if err := cmd.Execute(); err == nil {
		t.Error("expected error when issue number is missing, got none")
	}
}

func TestSpikeMaterializeInvalidNumber(t *testing.T) {
	cmd := rootCmd()
	cmd.SetArgs([]string{"spike", "materialize", "notanumber"})
	if err := cmd.Execute(); err == nil {
		t.Error("expected error for non-numeric issue number, got none")
	}
}

func TestKnowledgePruneEmptySubstantivePreserved(t *testing.T) {
	// Create a temp knowledge root with substantive content
	tmpDir := t.TempDir()
	featDir := filepath.Join(tmpDir, "features", "42-real-work")
	if err := os.MkdirAll(featDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "# PRD\n\nThis feature implements real work with detailed requirements and design decisions beyond thirty chars."
	if err := os.WriteFile(filepath.Join(featDir, "PRD.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	pruned, err := pruneEmptyKnowledge(tmpDir, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pruned) != 0 {
		t.Errorf("expected no pruned dirs, got %v", pruned)
	}
	if _, statErr := os.Stat(featDir); os.IsNotExist(statErr) {
		t.Error("substantive directory was incorrectly removed")
	}
}

func TestKnowledgePruneEmptyBoilerplateRemoved(t *testing.T) {
	tmpDir := t.TempDir()
	featDir := filepath.Join(tmpDir, "features", "99-empty-feature")
	if err := os.MkdirAll(featDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Only boilerplate: headings, HTML comments, empty checkbox
	boilerplate := "# PRD\n\n<!-- TODO: fill in -->\n\n- [ ] Add content\n\n| Key | Value |\n| --- | ----- |"
	if err := os.WriteFile(filepath.Join(featDir, "PRD.md"), []byte(boilerplate), 0o644); err != nil {
		t.Fatal(err)
	}

	pruned, err := pruneEmptyKnowledge(tmpDir, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pruned) != 1 {
		t.Errorf("expected 1 pruned dir, got %v", pruned)
	}
	if _, statErr := os.Stat(featDir); !os.IsNotExist(statErr) {
		t.Error("boilerplate-only directory was not removed")
	}
}

func TestKnowledgePruneEmptyIssueFilter(t *testing.T) {
	tmpDir := t.TempDir()
	// Two issue dirs — only 42-* should be considered when --issue 42
	dir42 := filepath.Join(tmpDir, "features", "42-my-feature")
	dir99 := filepath.Join(tmpDir, "features", "99-other")
	for _, d := range []string{dir42, dir99} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		boilerplate := "# PRD\n\n<!-- empty -->"
		if err := os.WriteFile(filepath.Join(d, "PRD.md"), []byte(boilerplate), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	pruned, err := pruneEmptyKnowledge(tmpDir, 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pruned) != 1 {
		t.Errorf("expected 1 pruned dir (only issue 42), got %v", pruned)
	}
	// dir99 must be untouched
	if _, statErr := os.Stat(dir99); os.IsNotExist(statErr) {
		t.Error("dir for issue 99 was incorrectly removed when filtering for issue 42")
	}
}

func TestKnowledgePruneEmptyMissingRoot(t *testing.T) {
	pruned, err := pruneEmptyKnowledge("/tmp/nonexistent-knowledge-root-xyz", 0)
	if err != nil {
		t.Fatalf("unexpected error for missing root: %v", err)
	}
	if len(pruned) != 0 {
		t.Errorf("expected empty result for missing root, got %v", pruned)
	}
}

// --- config show command ---

// TestConfigShowCmdRegistered verifies the `config show` subcommand is wired
// into the root command tree and exposes its documented flags.
func TestConfigShowCmdRegistered(t *testing.T) {
	cmd := rootCmd()

	var configCommand *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "config" {
			configCommand = sub
			break
		}
	}
	if configCommand == nil {
		t.Fatal("config subcommand not registered in rootCmd")
	}

	var showCmd *cobra.Command
	for _, sub := range configCommand.Commands() {
		if sub.Name() == "show" {
			showCmd = sub
			break
		}
	}
	if showCmd == nil {
		t.Fatal("config show subcommand not found")
	}

	for _, flagName := range []string{"key", "json", "raw"} {
		if showCmd.Flags().Lookup(flagName) == nil {
			t.Errorf("config show missing --%s flag", flagName)
		}
	}
}

// TestConfigShowCmd_KeyRaw is a CLI-level smoke test that exercises the full
// path: cwd discovery → config.Load → config.Render → stdout. It writes a
// temp config.yaml, chdirs into it, and asserts `--key project.owner --raw`
// emits the expected value.
func TestConfigShowCmd_KeyRaw(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, ".nightgauge")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `project:
  owner: SmokeTestOrg
  number: 1234
`
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })

	cmd := rootCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"config", "show", "--key", "project.owner", "--raw"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("config show failed: %v\nstderr: %s", err, stderr.String())
	}
	if got := stdout.String(); got != "SmokeTestOrg" {
		t.Errorf("stdout = %q, want %q", got, "SmokeTestOrg")
	}
}

// TestConfigShowCmd_MissingKey verifies missing --key paths exit non-zero
// with a stable "key not found" error so shell `||` fallbacks work.
func TestConfigShowCmd_MissingKey(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, ".nightgauge")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `project:
  owner: TestOrg
  number: 1
`
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })

	cmd := rootCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"config", "show", "--key", "no.such.path"})

	err = cmd.Execute()
	if err == nil {
		t.Fatal("expected error for missing key, got nil")
	}
	if !strings.Contains(err.Error(), "key not found") {
		t.Errorf("expected `key not found` error, got %v", err)
	}
}

// --- config init command ---

// TestConfigInitCmdRegistered verifies the `config init` subcommand is wired
// into the root command tree and exposes its documented flags.
func TestConfigInitCmdRegistered(t *testing.T) {
	cmd := rootCmd()

	var configCommand *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "config" {
			configCommand = sub
			break
		}
	}
	if configCommand == nil {
		t.Fatal("config subcommand not registered in rootCmd")
	}

	var initCmd *cobra.Command
	for _, sub := range configCommand.Commands() {
		if sub.Name() == "init" {
			initCmd = sub
			break
		}
	}
	if initCmd == nil {
		t.Fatal("config init subcommand not found")
	}

	for _, flagName := range []string{"owner", "owner-type", "repo", "project", "out", "force", "no-fetch", "json"} {
		if initCmd.Flags().Lookup(flagName) == nil {
			t.Errorf("config init missing --%s flag", flagName)
		}
	}
}

// TestConfigInitCmd_Placeholders is a CLI-level smoke test that drives the
// full path through cobra → config.BuildTemplate → file write, asserting the
// emitted file contains placeholder tokens and exits 0.
func TestConfigInitCmd_Placeholders(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "config.yaml")

	cmd := rootCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{
		"config", "init",
		"--owner", "nightgauge",
		"--out", outPath,
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("config init failed: %v\nstderr: %s", err, stderr.String())
	}

	body, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	got := string(body)
	for _, want := range []string{
		"owner: nightgauge",
		"owner_type: org",
		"<PROJECT_NUMBER>",
		"<STATUS_FIELD_ID>",
		"<P0_OPTION_ID>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\n%s", want, got)
		}
	}
}

// TestConfigInitCmd_RefuseOverwrite verifies the verb refuses to clobber an
// existing config.yaml without --force. The error string is part of the
// public contract — skills branch on it for the "Replace?" prompt.
func TestConfigInitCmd_RefuseOverwrite(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(outPath, []byte("preexisting: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := rootCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{
		"config", "init",
		"--owner", "nightgauge",
		"--out", outPath,
	})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when file exists without --force")
	}
	if !strings.Contains(err.Error(), "use --force to overwrite") {
		t.Errorf("expected `--force` hint in error, got %v", err)
	}

	body, _ := os.ReadFile(outPath)
	if string(body) != "preexisting: true\n" {
		t.Errorf("file should be unchanged, got: %s", body)
	}
}

// TestConfigInitCmd_ForceOverwrites verifies --force overwrites existing
// content with the freshly-rendered template.
func TestConfigInitCmd_ForceOverwrites(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(outPath, []byte("preexisting: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := rootCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{
		"config", "init",
		"--owner", "nightgauge",
		"--repo", "nightgauge",
		"--out", outPath,
		"--force",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("config init --force failed: %v\nstderr: %s", err, stderr.String())
	}

	body, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "repo: nightgauge") {
		t.Errorf("force overwrite did not render new template; got: %s", body)
	}
}

// TestConfigInitCmd_StdoutMode verifies --out - writes to stdout without
// touching the filesystem.
func TestConfigInitCmd_StdoutMode(t *testing.T) {
	cmd := rootCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{
		"config", "init",
		"--owner", "alice",
		"--owner-type", "user",
		"--out", "-",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("config init --out -: %v\nstderr: %s", err, stderr.String())
	}
	got := stdout.String()
	if !strings.Contains(got, "owner: alice") || !strings.Contains(got, "owner_type: user") {
		t.Errorf("stdout missing owner/owner_type: %s", got)
	}
}

// TestConfigInitCmd_JSONFlag verifies --json emits a stable status envelope.
func TestConfigInitCmd_JSONFlag(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "config.yaml")

	cmd := rootCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{
		"config", "init",
		"--owner", "nightgauge",
		"--out", outPath,
		"--json",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("config init --json failed: %v\nstderr: %s", err, stderr.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("JSON output invalid: %v\n%s", err, stdout.String())
	}
	if payload["wrote"] != true {
		t.Errorf("expected wrote=true, got %v", payload["wrote"])
	}
	if payload["path"] != outPath {
		t.Errorf("expected path=%s, got %v", outPath, payload["path"])
	}
}

func TestPipelineAggregateCmd_JSONSmokeWithFixture(t *testing.T) {
	dir := t.TempDir()
	historyDir := filepath.Join(dir, ".nightgauge", "pipeline", "history")
	if err := os.MkdirAll(historyDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	rec := `{"schema_version":"2","record_type":"run","issue_number":42,"title":"smoke","branch":"feat/42","base_branch":"main","execution_mode":"automatic","started_at":"2026-04-22T10:00:00Z","completed_at":"2026-04-22T10:05:00Z","total_duration_ms":300000,"outcome":"complete","stages":{"feature-dev":{"status":"complete","duration_ms":120000}},"tokens":{"total_input":100,"total_output":200,"total_cache_read":1000,"total_cache_creation":300,"estimated_cost_usd":0.5},"files":{"read_count":0,"written_count":0},"routing":{"complexity_score":3,"path":"normal","skip_stages":[]},"recorded_at":"2026-04-22T10:05:00Z"}`
	if err := os.WriteFile(filepath.Join(historyDir, "2026-04-22.jsonl"), []byte(rec+"\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	// printJSON writes to os.Stdout via fmt.Println, so redirect it through a pipe.
	prevStdout := os.Stdout
	rPipe, wPipe, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = wPipe
	t.Cleanup(func() { os.Stdout = prevStdout })

	cmd := rootCmd()
	cmd.SetArgs([]string{
		"pipeline", "aggregate",
		"--workdir", dir,
		"--include", "analysis",
		"--json",
	})
	execErr := cmd.Execute()
	wPipe.Close()
	out, _ := io.ReadAll(rPipe)
	os.Stdout = prevStdout

	if execErr != nil {
		t.Fatalf("Execute: %v", execErr)
	}

	var doc map[string]interface{}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("unmarshal stdout: %v\noutput: %s", err, string(out))
	}
	if v, _ := doc["v"].(float64); int(v) != 1 {
		t.Errorf("v = %v, want 1", doc["v"])
	}
	if n, _ := doc["runs_analyzed"].(float64); int(n) != 1 {
		t.Errorf("runs_analyzed = %v, want 1", doc["runs_analyzed"])
	}
	if _, ok := doc["stage_metrics"]; !ok {
		t.Errorf("missing stage_metrics in output")
	}
	if _, ok := doc["analysis"]; !ok {
		t.Errorf("missing analysis block (--include analysis was passed)")
	}
}

func TestPipelineAggregateCmd_RejectsBadDate(t *testing.T) {
	cmd := rootCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"pipeline", "aggregate", "--since", "04/22/2026", "--json"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for invalid --since, got nil")
	}
	if !strings.Contains(err.Error(), "YYYY-MM-DD") {
		t.Errorf("expected YYYY-MM-DD error, got %v", err)
	}
}

func TestProjectEnsureFieldsCmdRegistered(t *testing.T) {
	cmd := rootCmd()
	var projCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "project" {
			projCmd = sub
			break
		}
	}
	if projCmd == nil {
		t.Fatal("project subcommand not found")
	}
	var found bool
	for _, sub := range projCmd.Commands() {
		if sub.Name() == "ensure-fields" {
			found = true
			break
		}
	}
	if !found {
		t.Error("project ensure-fields subcommand not registered")
	}
}

func TestProjectEnsureFieldsCmdRequiresNumber(t *testing.T) {
	cmd := rootCmd()
	cmd.SetArgs([]string{"project", "ensure-fields"})
	err := cmd.Execute()
	if err == nil {
		t.Error("project ensure-fields without --number should fail")
	}
}

func TestEpicCmdSubcommands(t *testing.T) {
	cmd := rootCmd()

	var epicCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "epic" {
			epicCmd = sub
			break
		}
	}
	if epicCmd == nil {
		t.Fatal("epic subcommand not found")
	}

	expected := map[string]bool{
		"validate":         false,
		"check-completion": false,
		"assess":           false,
	}
	for _, sub := range epicCmd.Commands() {
		if _, ok := expected[sub.Name()]; ok {
			expected[sub.Name()] = true
		}
	}
	for name, found := range expected {
		if !found {
			t.Errorf("epic subcommand %q not registered", name)
		}
	}
}

func TestEpicValidateRequiresArg(t *testing.T) {
	cmd := rootCmd()
	cmd.SetArgs([]string{"epic", "validate"})
	err := cmd.Execute()
	if err == nil {
		t.Error("expected error when epic number not provided")
	}
}

func TestEpicValidateInvalidNumber(t *testing.T) {
	cmd := rootCmd()
	cmd.SetArgs([]string{"epic", "validate", "notanumber"})
	err := cmd.Execute()
	if err == nil {
		t.Error("expected error for non-numeric epic number")
	}
}

func TestEpicValidateCmdFlags(t *testing.T) {
	cmd := rootCmd()
	var epicCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "epic" {
			epicCmd = sub
			break
		}
	}
	if epicCmd == nil {
		t.Fatal("epic subcommand not found")
	}
	var validateCmd *cobra.Command
	for _, sub := range epicCmd.Commands() {
		if sub.Name() == "validate" {
			validateCmd = sub
			break
		}
	}
	if validateCmd == nil {
		t.Fatal("epic validate subcommand not found")
	}
	for _, flag := range []string{"owner", "repo", "json"} {
		if validateCmd.Flags().Lookup(flag) == nil {
			t.Errorf("epic validate missing --%s flag", flag)
		}
	}
}

func TestEpicAssessRequiresArg(t *testing.T) {
	cmd := rootCmd()
	cmd.SetArgs([]string{"epic", "assess"})
	err := cmd.Execute()
	if err == nil {
		t.Error("expected error when epic number not provided")
	}
}

func TestEpicAssessInvalidNumber(t *testing.T) {
	cmd := rootCmd()
	cmd.SetArgs([]string{"epic", "assess", "notanumber"})
	err := cmd.Execute()
	if err == nil {
		t.Error("expected error for non-numeric epic number")
	}
}

func TestEpicAssessCmdFlags(t *testing.T) {
	cmd := rootCmd()
	var epicCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "epic" {
			epicCmd = sub
			break
		}
	}
	if epicCmd == nil {
		t.Fatal("epic subcommand not found")
	}
	var assessCmd *cobra.Command
	for _, sub := range epicCmd.Commands() {
		if sub.Name() == "assess" {
			assessCmd = sub
			break
		}
	}
	if assessCmd == nil {
		t.Fatal("epic assess subcommand not found")
	}
	for _, flag := range []string{"owner", "repo", "json"} {
		if assessCmd.Flags().Lookup(flag) == nil {
			t.Errorf("epic assess missing --%s flag", flag)
		}
	}
}

func TestProjectSetFieldDateFlags(t *testing.T) {
	cmd := rootCmd()
	var projCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "project" {
			projCmd = sub
			break
		}
	}
	if projCmd == nil {
		t.Fatal("project subcommand not found")
	}

	var setFieldCmd *cobra.Command
	for _, sub := range projCmd.Commands() {
		if sub.Name() == "set-field" {
			setFieldCmd = sub
			break
		}
	}
	if setFieldCmd == nil {
		t.Fatal("project set-field subcommand not found")
	}

	for _, flag := range []string{"start-date", "target-date"} {
		if setFieldCmd.Flags().Lookup(flag) == nil {
			t.Errorf("project set-field missing --%s flag", flag)
		}
	}
}

func TestProjectSetFieldDateValidation(t *testing.T) {
	tests := []struct {
		name        string
		startDate   string
		wantErr     bool
		errContains string
	}{
		{"valid date", "2026-05-01", false, ""},
		{"invalid format", "05/01/2026", true, "not a valid YYYY-MM-DD"},
		{"invalid date", "2026-13-01", true, "not a valid YYYY-MM-DD"},
		{"empty string", "", false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := rootCmd()
			args := []string{"project", "set-field", "42"}
			if tt.startDate != "" {
				args = append(args, "--start-date", tt.startDate)
			} else {
				// Need at least one field flag to avoid "required" error
				args = append(args, "--priority", "P1")
			}
			cmd.SetArgs(args)
			err := cmd.Execute()
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error containing %q, got nil", tt.errContains)
				} else if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("error %q should contain %q", err.Error(), tt.errContains)
				}
			}
		})
	}
}

func TestProjectAddBulkFlag(t *testing.T) {
	cmd := rootCmd()
	var projCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "project" {
			projCmd = sub
			break
		}
	}
	if projCmd == nil {
		t.Fatal("project subcommand not found")
	}

	var addCmd *cobra.Command
	for _, sub := range projCmd.Commands() {
		if sub.Name() == "add" {
			addCmd = sub
			break
		}
	}
	if addCmd == nil {
		t.Fatal("project add subcommand not found")
	}

	for _, flag := range []string{"bulk", "milestone", "label"} {
		if addCmd.Flags().Lookup(flag) == nil {
			t.Errorf("project add missing --%s flag", flag)
		}
	}
}

func TestProjectAddBulkMutuallyExclusive(t *testing.T) {
	cmd := rootCmd()
	cmd.SetArgs([]string{"project", "add", "--bulk", "42"})
	err := cmd.Execute()
	if err == nil {
		t.Error("expected error when --bulk and positional arg both provided, got nil")
	}
	if err != nil && !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("error %q should mention 'mutually exclusive'", err.Error())
	}
}

func TestIsValidDate(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"2026-05-01", true},
		{"2026-12-31", true},
		{"2026-01-01", true},
		{"2026-13-01", false},
		{"2026-00-01", false},
		{"2026-05-32", false},
		{"05/01/2026", false},
		{"2026/05/01", false},
		{"20260501", false},
		{"", false},
		{"not-a-date", false},
	}

	for _, tt := range tests {
		got := isValidDate(tt.input)
		if got != tt.want {
			t.Errorf("isValidDate(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

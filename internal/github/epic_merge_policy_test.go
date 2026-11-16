package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func epicPolicyServer(t *testing.T, allowMerge bool, rulesStatus int, rules string) *Client {
	t.Helper()
	client, cleanup := newSettingsTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r":
			fmt.Fprintf(w, `{"full_name":"o/r","allow_merge_commit":%v,"allow_squash_merge":true,"default_branch":"main"}`, allowMerge)
		case "/repos/o/r/rules/branches/main":
			w.WriteHeader(rulesStatus)
			fmt.Fprint(w, rules)
		default:
			http.Error(w, "unexpected "+r.URL.Path, http.StatusTeapot)
		}
	})
	t.Cleanup(cleanup)
	return client
}

func TestCheckEpicMerge(t *testing.T) {
	for _, tc := range []struct {
		name       string
		allowMerge bool
		status     int
		rules      string
		allowed    bool
		reason     string
	}{
		{"squash-only repo", false, 200, `[]`, false, "allow_merge_commit=false"},
		{"pull_request rule squash only", true, 200, `[{"type":"pull_request","parameters":{"allowed_merge_methods":["squash"]}}]`, false, "pull_request rule allowed_merge_methods=[squash]"},
		{"merge queue squash", true, 200, `[{"type":"merge_queue","parameters":{"merge_method":"SQUASH"}}]`, false, "merge_queue rule merge_method=SQUASH"},
		{"merge allowed", true, 200, `[{"type":"pull_request","parameters":{"allowed_merge_methods":["merge","squash"]}},{"type":"merge_queue","parameters":{"merge_method":"MERGE"}}]`, true, ""},
		{"pull_request rule without methods is skipped", true, 200, `[{"type":"pull_request","parameters":{}}]`, true, ""},
		{"rules 404 setting allows", true, 404, `{}`, true, ""},
		{"rules 403 setting allows", true, 403, `{}`, true, ""},
		{"rules 403 setting refuses", false, 403, `{}`, false, "allow_merge_commit=false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := NewSettingsService(epicPolicyServer(t, tc.allowMerge, tc.status, tc.rules)).CheckEpicMerge(context.Background(), "o", "r")
			if err != nil {
				t.Fatal(err)
			}
			if tc.allowed && c.Reason != "" {
				t.Errorf("allowed check carries reason %q", c.Reason)
			}
			if c.Allowed != tc.allowed || !strings.Contains(c.Reason, tc.reason) || c.Method != EpicMergeMethod || c.Base != "main" {
				t.Errorf("got %+v, want allowed=%v reason~%q", c, tc.allowed, tc.reason)
			}
		})
	}
}

func TestCheckEpicMerge_RulesErrorReturned(t *testing.T) {
	_, err := NewSettingsService(epicPolicyServer(t, true, 422, `{}`)).CheckEpicMerge(context.Background(), "o", "r")
	if err == nil {
		t.Fatal("a 422 rules read must be an error, not a decision")
	}
}

func TestMergeEpicPR_RefusedNeverMerges(t *testing.T) {
	merged := false
	client, cleanup := newSettingsTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r":
			fmt.Fprint(w, `{"allow_merge_commit":true,"default_branch":"main"}`)
		case "/repos/o/r/rules/branches/main":
			fmt.Fprint(w, `[{"type":"merge_queue","parameters":{"merge_method":"SQUASH"}}]`)
		default:
			merged = true
			http.Error(w, "unexpected "+r.URL.Path, http.StatusTeapot)
		}
	})
	defer cleanup()
	err := NewPRService(client).MergeEpicPR(context.Background(), "o", "r", "PR_node", "epic/1-x")
	var refused *EpicMergeRefusedError
	if !errors.As(err, &refused) || !strings.Contains(err.Error(), "MERGE") || !strings.Contains(err.Error(), "merge_queue") {
		t.Fatalf("err = %v, want EpicMergeRefusedError naming MERGE and merge_queue", err)
	}
	if merged {
		t.Error("a refused epic merge still called the merge or delete API")
	}
}

// The guard fails closed: a policy read error aborts the epic merge (#3358).
func TestMergeEpicPR_ProbeErrorNeverMerges(t *testing.T) {
	merged := false
	client, cleanup := newSettingsTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r":
			fmt.Fprint(w, `{"allow_merge_commit":true,"default_branch":"main"}`)
		case "/repos/o/r/rules/branches/main":
			http.Error(w, "boom", http.StatusUnprocessableEntity)
		default:
			merged = true
			http.Error(w, "unexpected "+r.URL.Path, http.StatusTeapot)
		}
	})
	defer cleanup()
	err := NewPRService(client).MergeEpicPR(context.Background(), "o", "r", "PR_node", "epic/1-x")
	var refused *EpicMergeRefusedError
	if err == nil || errors.As(err, &refused) {
		t.Fatalf("err = %v, want a probe error that is not a refusal", err)
	}
	if merged {
		t.Error("an unreadable epic merge policy still reached the merge or delete API")
	}
}

package doctor

// The `project_workflows` check (#2499).
//
// project.builtin_workflows records which of a board's built-in workflows the
// operator expects to be enabled. GitHub offers no API to enable one, so the
// file can only drift from the board. This check reads the live board and
// reports the disagreement; it never changes either side.

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	gh "github.com/nightgauge/nightgauge/internal/github"
)

const codeProjectWorkflows = "NGD048"

func init() {
	builtinChecks = append(builtinChecks, Check{
		ID: "project_workflows", Title: "Board workflows", Group: "config", Code: codeProjectWorkflows,
		Timeout: 30 * time.Second, DependsOn: []string{"github_auth", "project"},
		Run: func(ctx context.Context, env *Env) []Finding {
			cfg := env.Cfg
			if cfg == nil || env.Client == nil || cfg.ProjectNumber <= 0 || cfg.Owner == "" ||
				cfg.BuiltinWorkflows == nil {
				env.SetDetail("project_workflows", "not applicable: no board or no project.builtin_workflows declared")
				return nil
			}
			ownerType := gh.OwnerTypeOrg
			if strings.EqualFold(cfg.OwnerType, "user") {
				ownerType = gh.OwnerTypeUser
			}
			board, err := gh.ListProjectWorkflows(ctx, env.Client, cfg.Owner, ownerType, cfg.ProjectNumber)
			if err != nil {
				env.SetDetail("project_workflows", "could not read the board's workflows")
				return []Finding{unverifiableFinding("project_workflows", codeProjectWorkflows,
					SeverityWarning, "Board workflows", err.Error())}
			}
			fs := projectWorkflowFindings(cfg.BuiltinWorkflows, board)
			env.SetDetail("project_workflows", fmt.Sprintf("%d declared, %d on the board, %d finding(s)",
				len(cfg.BuiltinWorkflows), len(board), len(fs)))
			return fs
		},
	})
}

// builtinWorkflowNames maps each project.builtin_workflows key to the name
// GitHub gives that workflow on the board.
var builtinWorkflowNames = map[string]string{
	"auto_add_sub_issues":   "Auto-add sub-issues to project",
	"item_added_to_project": "Item added to project",
	"item_closed_to_done":   "Item closed",
	"pr_merged_to_done":     "Pull request merged",
	"auto_close_issue":      "Auto-close issue",
	"pr_linked_to_issue":    "Pull request linked to issue",
	"auto_add_to_project":   "Auto-add to project",
	"auto_archive_days":     "Auto-archive items",
}

// declaredEnabled reads a declared value: a bool, or for auto_archive_days a
// positive number of days. ok is false for a value of any other type.
func declaredEnabled(key string, v any) (enabled, ok bool) {
	if key == "auto_archive_days" {
		switch n := v.(type) {
		case int:
			return n > 0, true
		case int64:
			return n > 0, true
		case uint64:
			return n > 0, true
		case float64:
			return n > 0, true
		}
		return false, false
	}
	b, ok := v.(bool)
	return b, ok
}

// projectWorkflowFindings compares the declared map to the board's workflows.
// A disagreement about enabled is a blocker; a key naming no workflow on the
// board, a board workflow the file does not declare, and a value of the wrong
// type are warnings.
func projectWorkflowFindings(declared map[string]any, board []gh.ProjectWorkflow) []Finding {
	const check = "project_workflows"
	byName := map[string]bool{}
	for _, w := range board {
		byName[w.Name] = w.Enabled
	}
	keys := make([]string, 0, len(declared))
	for k := range declared {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var fs []Finding
	covered := map[string]bool{}
	for _, key := range keys {
		name, known := builtinWorkflowNames[key]
		if !known {
			fs = append(fs, newFinding(check, codeProjectWorkflows, SeverityWarning,
				"project.builtin_workflows."+key+" names no known board workflow",
				"the key is not one the schema defines, so nothing can compare it to the board",
				map[string]string{"state": "unknown_key", "key": key}, []string{"unknown_key", key},
				manualRemedy("remove", "Remove the key from .nightgauge/config.yaml", check,
					"Delete project.builtin_workflows."+key)))
			continue
		}
		want, ok := declaredEnabled(key, declared[key])
		if !ok {
			fs = append(fs, newFinding(check, codeProjectWorkflows, SeverityWarning,
				"project.builtin_workflows."+key+" has the wrong type",
				"the value must be a boolean (a number of days for auto_archive_days)",
				map[string]string{"state": "bad_value", "key": key}, []string{"bad_value", key},
				manualRemedy("fix", "Correct the value in .nightgauge/config.yaml", check,
					"Set project.builtin_workflows."+key+" to true or false")))
			continue
		}
		covered[name] = true
		have, onBoard := byName[name]
		switch {
		case !onBoard:
			fs = append(fs, newFinding(check, codeProjectWorkflows, SeverityWarning,
				fmt.Sprintf("workflow %q (%s) is not on this board", name, key),
				"the file declares a workflow the board does not have, so it cannot be compared",
				map[string]string{"state": "missing_on_board", "key": key, "workflow": name},
				[]string{"missing_on_board", key},
				manualRemedy("remove", "Remove the key, or confirm the board's workflow name", check,
					"Delete project.builtin_workflows."+key+" if the board has no such workflow")))
		case have != want:
			fs = append(fs, newFinding(check, codeProjectWorkflows, SeverityBlocker,
				fmt.Sprintf("workflow %q (%s) is declared %s but is %s on the board",
					name, key, enabledWord(want), enabledWord(have)),
				"project.builtin_workflows has drifted from the live board; GitHub cannot enable workflows by API, so it is fixed by hand",
				map[string]string{"state": "drift", "key": key, "workflow": name,
					"declared": strconv.FormatBool(want), "board": strconv.FormatBool(have)},
				[]string{"drift", key},
				manualRemedy("reconcile", "Make the file and the board agree", check,
					"Change the workflow in the board's Workflows settings, or update project.builtin_workflows."+key)))
		}
	}

	extra := make([]string, 0)
	for _, w := range board {
		if !covered[w.Name] {
			extra = append(extra, w.Name)
		}
	}
	sort.Strings(extra)
	for _, name := range extra {
		fs = append(fs, newFinding(check, codeProjectWorkflows, SeverityWarning,
			fmt.Sprintf("board workflow %q is not declared in project.builtin_workflows", name),
			"the board has a workflow the file does not record, so a change to it would go unnoticed",
			map[string]string{"state": "undeclared", "workflow": name,
				"board": strconv.FormatBool(byName[name])},
			[]string{"undeclared", name},
			manualRemedy("declare", "Record the workflow in .nightgauge/config.yaml", check,
				"Add the matching key under project.builtin_workflows")))
	}
	return fs
}

func enabledWord(b bool) string {
	if b {
		return "enabled"
	}
	return "disabled"
}

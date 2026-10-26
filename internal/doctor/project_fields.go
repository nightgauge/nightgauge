package doctor

// The `project_fields` check (#1501).
//
// Board status writes — the Go scheduler's and, since #1501, the extension's —
// resolve the Status, Priority and Size single-select fields from the board by
// project number instead of reading hand-copied field IDs from config. A board
// missing one of those fields therefore fails every write for that field, and
// the failure surfaced only as a log line. This check reads the live board and
// names the field that cannot be resolved.

import (
	"context"
	"fmt"
	"strings"
	"time"

	gh "github.com/nightgauge/nightgauge/internal/github"
)

const codeProjectFields = "NGD049"

// requiredBoardFields are the single-select fields the board writers set.
var requiredBoardFields = []string{"Status", "Priority", "Size"}

func init() {
	builtinChecks = append(builtinChecks, Check{
		ID: "project_fields", Title: "Board fields", Group: "config", Code: codeProjectFields,
		Timeout: 30 * time.Second, DependsOn: []string{"github_auth", "project"},
		Run: func(ctx context.Context, env *Env) []Finding {
			cfg := env.Cfg
			if cfg == nil || env.Client == nil || cfg.ProjectNumber <= 0 || cfg.Owner == "" {
				env.SetDetail("project_fields", "not applicable: no board configured")
				return nil
			}
			ownerType := gh.OwnerTypeOrg
			if strings.EqualFold(cfg.OwnerType, "user") {
				ownerType = gh.OwnerTypeUser
			}
			snap, err := gh.NewProjectService(env.Client, cfg.Owner, cfg.ProjectNumber, ownerType).
				SnapshotFields(ctx)
			if err != nil {
				env.SetDetail("project_fields", "could not read the board's fields")
				return []Finding{unverifiableFinding("project_fields", codeProjectFields,
					SeverityWarning, "Board fields", err.Error())}
			}
			fs := projectFieldFindings(cfg.Owner, cfg.ProjectNumber, snap)
			env.SetDetail("project_fields", fmt.Sprintf("project %s/%d: %d of %d field(s) resolved",
				cfg.Owner, cfg.ProjectNumber, len(requiredBoardFields)-len(fs), len(requiredBoardFields)))
			return fs
		},
	})
}

// projectFieldFindings reports each required field the board does not carry
// as a single-select field. One finding per field, so each is fingerprinted
// and resolved on its own.
func projectFieldFindings(owner string, number int, snap *gh.FieldsSnapshot) []Finding {
	const check = "project_fields"
	board := fmt.Sprintf("%s/%d", owner, number)
	var fs []Finding
	for _, name := range requiredBoardFields {
		var info gh.FieldInfo
		ok := false
		if snap != nil {
			info, ok = snap.Fields[name]
		}
		if ok && info.Type == "single_select" && info.ID != "" {
			continue
		}
		fs = append(fs, newFinding(check, codeProjectFields, SeverityWarning,
			fmt.Sprintf("board %s has no %s single-select field", board, name),
			"board status writes resolve this field from the board by name, so every write of it fails",
			map[string]string{"state": "missing_field", "field": name, "board": board},
			[]string{"missing_field", name},
			manualRemedy("add", "Add the field to the board", check,
				fmt.Sprintf("Add a single-select field named %q to project %s", name, board))))
	}
	return fs
}

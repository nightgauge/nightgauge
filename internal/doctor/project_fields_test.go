package doctor

import (
	"strings"
	"testing"

	gh "github.com/nightgauge/nightgauge/internal/github"
)

func boardSnapshot(names ...string) *gh.FieldsSnapshot {
	snap := &gh.FieldsSnapshot{ProjectID: "PVT_1", Fields: map[string]gh.FieldInfo{}}
	for _, n := range names {
		snap.Fields[n] = gh.FieldInfo{ID: "F_" + n, Type: "single_select"}
	}
	return snap
}

func TestProjectFieldFindingsPassWhenAllFieldsResolve(t *testing.T) {
	if fs := projectFieldFindings("acme", 3, boardSnapshot("Status", "Priority", "Size")); len(fs) != 0 {
		t.Fatalf("findings = %+v, want none", fs)
	}
}

func TestProjectFieldFindingsNameEachMissingField(t *testing.T) {
	snap := boardSnapshot("Status")
	snap.Fields["Size"] = gh.FieldInfo{ID: "F_Size", Type: "text"} // wrong type: not resolvable
	fs := projectFieldFindings("acme", 3, snap)
	var got []string
	for _, f := range fs {
		if f.Code != codeProjectFields || f.Severity != SeverityWarning {
			t.Errorf("finding %q: code %s severity %s", f.Title, f.Code, f.Severity)
		}
		if !strings.Contains(f.Title, "acme/3") {
			t.Errorf("title %q does not name the board", f.Title)
		}
		got = append(got, f.Evidence["field"])
	}
	if strings.Join(got, ",") != "Priority,Size" {
		t.Fatalf("missing fields = %v, want [Priority Size]", got)
	}
}

func TestProjectFieldFindingsWithNoSnapshotReportEveryField(t *testing.T) {
	if fs := projectFieldFindings("acme", 3, nil); len(fs) != len(requiredBoardFields) {
		t.Fatalf("findings = %d, want %d", len(fs), len(requiredBoardFields))
	}
}

func TestProjectFieldsRegistered(t *testing.T) {
	for _, c := range DefaultRegistry().Checks() {
		if c.ID == "project_fields" {
			if c.Code != "NGD049" {
				t.Errorf("code = %s", c.Code)
			}
			return
		}
	}
	t.Fatal("project_fields is not registered")
}

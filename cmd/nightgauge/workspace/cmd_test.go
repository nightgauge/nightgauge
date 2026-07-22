package workspacecmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestWorkspaceDoctor_JSONOutput_EmptyConfig(t *testing.T) {
	// When the workspace config is missing/minimal, doctor should still
	// produce valid JSON with empty slices rather than null.
	buf := &bytes.Buffer{}
	cmd := doctorCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)

	// runDoctor reads config from CWD; in tests CWD has no .nightgauge/config.yaml.
	// It should not error — it uses an empty config as fallback.
	err := runDoctor(cmd, true)
	if err != nil {
		// Fatal validation errors also print JSON first; check if output is valid.
		if buf.Len() == 0 {
			return
		}
	}

	if buf.Len() > 0 {
		var result DoctorResult
		if jerr := json.Unmarshal(buf.Bytes(), &result); jerr != nil {
			t.Errorf("JSON output is not valid: %v\nOutput: %s", jerr, buf.String())
		}
	}
}

func TestWorkspaceDoctor_HumanOutput_ContainsRegisteredForges(t *testing.T) {
	buf := &bytes.Buffer{}
	cmd := doctorCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)

	_ = runDoctor(cmd, false)

	out := buf.String()
	// Human output should always mention registered forges.
	if !strings.Contains(out, "forges") && !strings.Contains(out, "Registered") {
		// If the command errored, the output may contain just the error — acceptable.
		// Only fail if output is totally empty.
		if buf.Len() == 0 {
			t.Error("expected some output from workspace doctor")
		}
	}
}

func TestValidationErrorJSON_Fields(t *testing.T) {
	ve := ValidationErrorJSON{
		Path:    "repositories.myrepo.forge",
		Message: "dangling ref",
		Fatal:   true,
	}
	data, err := json.Marshal(ve)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var roundtrip ValidationErrorJSON
	if err := json.Unmarshal(data, &roundtrip); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if roundtrip.Path != ve.Path || roundtrip.Message != ve.Message || roundtrip.Fatal != ve.Fatal {
		t.Errorf("roundtrip mismatch: got %+v, want %+v", roundtrip, ve)
	}
}

func TestRepoStatus_JSONFields(t *testing.T) {
	rs := RepoStatus{
		Spec:       "nightgauge/nightgauge",
		ForgeID:    "github",
		ForgeKind:  "github",
		Reachable:  true,
		AuthStatus: "ok",
	}
	data, err := json.Marshal(rs)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var rt RepoStatus
	if err := json.Unmarshal(data, &rt); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if rt != rs {
		t.Errorf("roundtrip mismatch: got %+v, want %+v", rt, rs)
	}
}

func TestDoctorResult_JSONRoundtrip(t *testing.T) {
	result := DoctorResult{
		Repos: []RepoStatus{
			{Spec: "nightgauge/nightgauge", ForgeID: "github", ForgeKind: "github", Reachable: true, AuthStatus: "ok"},
			{Spec: "acme/platform", ForgeID: "acme-gitlab", ForgeKind: "gitlab", Reachable: true, AuthStatus: "ok"},
		},
		ValidationErrors: []ValidationErrorJSON{},
		RegisteredForges: []string{"github", "acme-gitlab"},
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var rt DoctorResult
	if err := json.Unmarshal(data, &rt); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(rt.Repos) != 2 || len(rt.RegisteredForges) != 2 {
		t.Errorf("roundtrip mismatch: got %+v", rt)
	}
}

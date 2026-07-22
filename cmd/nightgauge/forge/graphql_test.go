package forgecmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGraphQL_StringField_PassesThroughQuery(t *testing.T) {
	gql := &fakeGraphQLService{resp: []byte(`{"data":{"viewer":{"login":"alice"}}}`)}
	withFakeForge(t, &fakeForge{graphql: gql, auth: &fakeAuthService{}})

	root := Cmd()
	stdout := &bytes.Buffer{}
	root.SetOut(stdout)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"graphql", "-f", "query=query { viewer { login } }"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if gql.lastQ != "query { viewer { login } }" {
		t.Errorf("query passed through wrong: %q", gql.lastQ)
	}
	if !strings.Contains(stdout.String(), `"login":"alice"`) {
		t.Errorf("expected raw envelope in stdout, got: %s", stdout.String())
	}
}

func TestGraphQL_RawField_DecodesNumbers(t *testing.T) {
	gql := &fakeGraphQLService{}
	withFakeForge(t, &fakeForge{graphql: gql})

	root := Cmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"graphql",
		"-f", "query=mutation { x }",
		"-F", "number=42",
		"-F", "draft=true",
		"-F", "name=alice",
	})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	got, want := gql.lastVars, map[string]interface{}{
		"number": float64(42),
		"draft":  true,
		"name":   "alice",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("var %q = %v (%T), want %v (%T)", k, got[k], got[k], v, v)
		}
	}
}

func TestGraphQL_FieldFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "q.graphql")
	const body = "query Q { viewer { login } }"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	gql := &fakeGraphQLService{}
	withFakeForge(t, &fakeForge{graphql: gql})

	root := Cmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"graphql", "-f", "query=@" + path})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if gql.lastQ != body {
		t.Errorf("query from file not loaded: got %q, want %q", gql.lastQ, body)
	}
}

func TestGraphQL_QueryFileFlag(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "q.graphql")
	const body = "query { viewer { login } }"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	gql := &fakeGraphQLService{}
	withFakeForge(t, &fakeForge{graphql: gql})

	root := Cmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"graphql", "--query-file", path})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if gql.lastQ != body {
		t.Errorf("--query-file body not loaded: got %q", gql.lastQ)
	}
}

func TestGraphQL_MissingQuery_FailsClearly(t *testing.T) {
	withFakeForge(t, &fakeForge{graphql: &fakeGraphQLService{}})
	root := Cmd()
	stderr := &bytes.Buffer{}
	root.SetOut(&bytes.Buffer{})
	root.SetErr(stderr)
	root.SetArgs([]string{"graphql", "-F", "number=1"})
	if err := root.ExecuteContext(context.Background()); err == nil {
		t.Fatal("expected error when query missing")
	}
	if !strings.Contains(stderr.String(), "missing 'query'") {
		t.Errorf("expected missing-query message, got: %s", stderr.String())
	}
}

func TestGraphQL_UnsupportedAdapter_ReturnsErrUnsupported(t *testing.T) {
	// fakeForge with no graphql field — fakeForge.ExecuteGraphQL returns
	// forge.ErrUnsupported.
	withFakeForge(t, &fakeForge{})
	root := Cmd()
	stderr := &bytes.Buffer{}
	root.SetOut(&bytes.Buffer{})
	root.SetErr(stderr)
	root.SetArgs([]string{"graphql", "-f", "query=q"})
	err := root.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected error for unsupported adapter")
	}
	if !strings.Contains(stderr.String(), "operation not supported") &&
		!strings.Contains(stderr.String(), "unsupported") {
		t.Errorf("expected unsupported message, got: %s", stderr.String())
	}
}

func TestBuildGraphQLPayload_VarsAndQuery(t *testing.T) {
	q, vars, err := buildGraphQLPayload(
		[]string{"query=Q { x }", "owner=nightgauge"},
		[]string{"number=7"},
		"",
		bytes.NewReader(nil),
	)
	if err != nil {
		t.Fatalf("buildGraphQLPayload: %v", err)
	}
	if q != "Q { x }" {
		t.Errorf("query = %q", q)
	}
	if vars["owner"] != "nightgauge" {
		t.Errorf("owner var = %v", vars["owner"])
	}
	if vars["number"] != float64(7) {
		t.Errorf("number var = %v (%T)", vars["number"], vars["number"])
	}

	// Verify the raw value round-trips through JSON for downstream use.
	out, err := json.Marshal(vars)
	if err != nil {
		t.Fatalf("marshal vars: %v", err)
	}
	if !strings.Contains(string(out), `"number":7`) {
		t.Errorf("number not serialized as JSON number: %s", out)
	}
}

func TestBuildGraphQLPayload_BadFlag(t *testing.T) {
	_, _, err := buildGraphQLPayload([]string{"=value"}, nil, "", nil)
	if err == nil {
		t.Fatal("expected error for malformed pair")
	}
}

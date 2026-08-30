package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestNewLabelService(t *testing.T) {
	client := NewClientWithToken("test-token")
	svc := NewLabelService(client, "nightgauge", "nightgauge")
	if svc == nil {
		t.Fatal("NewLabelService returned nil")
	}
	if svc.client != client {
		t.Error("LabelService.client is not the provided client")
	}
	if svc.owner != "nightgauge" {
		t.Errorf("LabelService.owner = %q, want %q", svc.owner, "nightgauge")
	}
	if svc.repo != "nightgauge" {
		t.Errorf("LabelService.repo = %q, want %q", svc.repo, "nightgauge")
	}
}

func TestLabelList(t *testing.T) {
	listResp := `[
		{"node_id": "MDU6TGFiZWwx", "name": "bug", "description": "Something wrong", "color": "d73a4a"},
		{"node_id": "MDU6TGFiZWwy", "name": "feature", "description": "New feature", "color": "a2eeef"}
	]`

	client, cleanup := mockForgeServer(t,
		map[string]string{"GET /repos/nightgauge/nightgauge/labels": listResp})
	defer cleanup()

	svc := NewLabelService(client, "nightgauge", "nightgauge")
	labels, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(labels) != 2 {
		t.Fatalf("List() returned %d labels, want 2", len(labels))
	}
	if labels[0].Name != "bug" {
		t.Errorf("labels[0].Name = %q, want %q", labels[0].Name, "bug")
	}
	if labels[0].ID != "MDU6TGFiZWwx" {
		t.Errorf("labels[0].ID = %q, want %q", labels[0].ID, "MDU6TGFiZWwx")
	}
	if labels[0].Color != "d73a4a" {
		t.Errorf("labels[0].Color = %q, want %q", labels[0].Color, "d73a4a")
	}
	if labels[1].Name != "feature" {
		t.Errorf("labels[1].Name = %q, want %q", labels[1].Name, "feature")
	}
}

func TestLabelList_Empty(t *testing.T) {
	client, cleanup := mockForgeServer(t,
		map[string]string{"GET /repos/nightgauge/nightgauge/labels": `[]`})
	defer cleanup()

	svc := NewLabelService(client, "nightgauge", "nightgauge")
	labels, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(labels) != 0 {
		t.Errorf("List() returned %d labels, want 0", len(labels))
	}
}

func TestLabelCreate_New(t *testing.T) {
	// The chain is List() (REST, empty) → GetRepositoryID (REST) →
	// createLabel mutation (GraphQL). Only the mutation is positional.
	createResp := `{
		"data": {
			"createLabel": {
				"label": {
					"id": "MDU6TGFiZWwz",
					"name": "priority:critical",
					"description": "Critical priority",
					"color": "ff0000"
				}
			}
		}
	}`

	client, cleanup := mockForgeServer(t, map[string]string{
		"GET /repos/nightgauge/nightgauge/labels": `[]`,
		"GET /repos/nightgauge/nightgauge":        `{"node_id":"R_kgDOHNxxx"}`,
	}, createResp)
	defer cleanup()

	svc := NewLabelService(client, "nightgauge", "nightgauge")
	label, err := svc.Create(context.Background(), "priority:critical", "Critical priority", "ff0000")
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	if label.ID != "MDU6TGFiZWwz" {
		t.Errorf("Create() ID = %q, want %q", label.ID, "MDU6TGFiZWwz")
	}
	if label.Name != "priority:critical" {
		t.Errorf("Create() Name = %q, want %q", label.Name, "priority:critical")
	}
	if label.Color != "ff0000" {
		t.Errorf("Create() Color = %q, want %q", label.Color, "ff0000")
	}
}

func TestLabelCreate_Existing(t *testing.T) {
	// Create() with an existing label returns it without calling createLabel mutation.
	// Only one response needed: List() returns existing label.
	listResp := `[
		{"node_id": "MDU6TGFiZWwx", "name": "bug", "description": "A bug", "color": "d73a4a"}
	]`

	client, cleanup := mockForgeServer(t,
		map[string]string{"GET /repos/nightgauge/nightgauge/labels": listResp})
	defer cleanup()

	svc := NewLabelService(client, "nightgauge", "nightgauge")
	label, err := svc.Create(context.Background(), "bug", "A bug", "d73a4a")
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	if label.ID != "MDU6TGFiZWwx" {
		t.Errorf("Create() returned wrong ID: %q", label.ID)
	}
	if label.Name != "bug" {
		t.Errorf("Create() returned wrong Name: %q", label.Name)
	}
}

func TestLabelCreate_DefaultColor(t *testing.T) {
	// When color is empty, Create() defaults to "cccccc".
	createResp := `{
		"data": {
			"createLabel": {
				"label": {
					"id": "MDU6TGFiZWw5",
					"name": "new-label",
					"description": "",
					"color": "cccccc"
				}
			}
		}
	}`

	client, cleanup := mockForgeServer(t, map[string]string{
		"GET /repos/nightgauge/nightgauge/labels": `[]`,
		"GET /repos/nightgauge/nightgauge":        `{"node_id":"R_kgDOHNxxx"}`,
	}, createResp)
	defer cleanup()

	svc := NewLabelService(client, "nightgauge", "nightgauge")
	label, err := svc.Create(context.Background(), "new-label", "", "")
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	if label.Color != "cccccc" {
		t.Errorf("Create() Color = %q, want default %q", label.Color, "cccccc")
	}
}

func TestLabelDelete(t *testing.T) {
	deleteResp := `{"data": {"deleteLabel": {"clientMutationId": null}}}`

	client, cleanup := mockGraphQLServer(t, deleteResp)
	defer cleanup()

	svc := NewLabelService(client, "nightgauge", "nightgauge")
	if err := svc.Delete(context.Background(), "MDU6TGFiZWwx"); err != nil {
		t.Fatalf("Delete() error: %v", err)
	}
}

// recordingForgeServer replays GraphQL responses in order (repeating the last)
// and captures EVERY request body — REST reads included, where the body is
// empty — so a test can assert which operations actually reached the API. That
// is the only way to prove the idempotent rename path mutates nothing.
//
// restBody answers the one REST read on this path: the label list, moved off
// GraphQL by #849. It is served by path prefix rather than by position,
// because the list is not always the first call.
func recordingForgeServer(t *testing.T, bodies *[]string, restBody string, responses ...string) (*Client, func()) {
	t.Helper()
	var mu sync.Mutex
	var callIdx int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		*bodies = append(*bodies, string(raw))
		idx := callIdx
		callIdx++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/repos/") {
			fmt.Fprint(w, restBody)
			return
		}
		if idx >= len(responses) {
			idx = len(responses) - 1
		}
		fmt.Fprint(w, responses[idx])
	}))
	return NewClientWithURL("test-token", srv.URL), srv.Close
}

// labelListFixture is the REST body for GET /repos/{o}/{r}/labels.
func labelListFixture(entries string) string {
	return `[` + entries + `]`
}

const areaVscodeNode = `{"node_id":"LA_old","name":"area:vscode","description":"VS Code extension","color":"c5def5"}`

func TestLabelRename_PreservesNodeID(t *testing.T) {
	updateResp := `{"data":{"updateLabel":{"label":{"id":"LA_old","name":"component:vscode","description":"VS Code extension","color":"7057ff"}}}}`
	var bodies []string
	client, cleanup := recordingForgeServer(t, &bodies, labelListFixture(areaVscodeNode), updateResp)
	defer cleanup()

	svc := NewLabelService(client, "nightgauge", "nightgauge")
	label, err := svc.Rename(context.Background(), "area:vscode", "component:vscode", "", "7057ff")
	if err != nil {
		t.Fatalf("Rename() error: %v", err)
	}
	if label.Name != "component:vscode" {
		t.Errorf("Name = %q, want %q", label.Name, "component:vscode")
	}
	// The whole point of rename-over-recreate: the node ID is unchanged, so
	// every issue carrying the label stays labelled.
	if label.ID != "LA_old" {
		t.Errorf("ID = %q, want the original %q — a new ID means issues were detached", label.ID, "LA_old")
	}
	if label.Color != "7057ff" {
		t.Errorf("Color = %q, want %q", label.Color, "7057ff")
	}
	if len(bodies) != 2 {
		t.Fatalf("made %d API calls, want 2 (list + update)", len(bodies))
	}
	if !strings.Contains(bodies[1], "updateLabel") {
		t.Errorf("second call was not updateLabel: %s", bodies[1])
	}
	// Description was not supplied, so it must not appear in the mutation at
	// all — sending an empty string would wipe the label's text.
	if strings.Contains(bodies[1], `"description"`) {
		t.Errorf("omitted --description leaked into the mutation and would clear it: %s", bodies[1])
	}
}

func TestLabelRename_AlreadyRenamedIsIdempotent(t *testing.T) {
	renamed := `{"node_id":"LA_old","name":"component:vscode","description":"VS Code extension","color":"7057ff"}`
	var bodies []string
	client, cleanup := recordingForgeServer(t, &bodies, labelListFixture(renamed))
	defer cleanup()

	svc := NewLabelService(client, "nightgauge", "nightgauge")
	label, err := svc.Rename(context.Background(), "area:vscode", "component:vscode", "", "")
	if err != nil {
		t.Fatalf("Rename() on an already-renamed label must succeed, got: %v", err)
	}
	if label.Name != "component:vscode" {
		t.Errorf("Name = %q, want %q", label.Name, "component:vscode")
	}
	if len(bodies) != 1 {
		t.Fatalf("made %d API calls, want 1 (list only) — the idempotent path must not mutate", len(bodies))
	}
}

func TestLabelRename_TargetNameOccupied(t *testing.T) {
	occupied := `{"node_id":"LA_new","name":"component:vscode","description":"","color":"7057ff"}`
	var bodies []string
	client, cleanup := recordingForgeServer(t, &bodies, labelListFixture(areaVscodeNode+","+occupied))
	defer cleanup()

	svc := NewLabelService(client, "nightgauge", "nightgauge")
	_, err := svc.Rename(context.Background(), "area:vscode", "component:vscode", "", "")
	if err == nil {
		t.Fatal("Rename() onto an occupied name must fail rather than silently merge")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error = %q, want it to name the collision", err)
	}
	if len(bodies) != 1 {
		t.Errorf("made %d API calls, want 1 (list only) — a rejected rename must not mutate", len(bodies))
	}
}

func TestLabelRename_NotFound(t *testing.T) {
	var bodies []string
	client, cleanup := recordingForgeServer(t, &bodies, labelListFixture(""))
	defer cleanup()

	svc := NewLabelService(client, "nightgauge", "nightgauge")
	_, err := svc.Rename(context.Background(), "area:vscode", "component:vscode", "", "")
	if err == nil {
		t.Fatal("Rename() of a missing label must fail")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want a not-found message", err)
	}
}

func TestLabelRename_SameNameUpdatesColorOnly(t *testing.T) {
	updateResp := `{"data":{"updateLabel":{"label":{"id":"LA_old","name":"area:vscode","description":"VS Code extension","color":"7057ff"}}}}`
	var bodies []string
	client, cleanup := recordingForgeServer(t, &bodies, labelListFixture(areaVscodeNode), updateResp)
	defer cleanup()

	svc := NewLabelService(client, "nightgauge", "nightgauge")
	label, err := svc.Rename(context.Background(), "area:vscode", "area:vscode", "", "7057ff")
	if err != nil {
		t.Fatalf("Rename() to the same name must be allowed for a colour-only edit: %v", err)
	}
	if label.Color != "7057ff" {
		t.Errorf("Color = %q, want %q", label.Color, "7057ff")
	}
}

func TestLabelRename_RequiresBothNames(t *testing.T) {
	svc := NewLabelService(NewClientWithToken("t"), "nightgauge", "nightgauge")
	if _, err := svc.Rename(context.Background(), "", "component:vscode", "", ""); err == nil {
		t.Error("empty old name must be rejected")
	}
	if _, err := svc.Rename(context.Background(), "area:vscode", "", "", ""); err == nil {
		t.Error("empty new name must be rejected")
	}
}

// --- ResolveNames (#1214) ---
//
// `issue create --labels` documented and consumed GraphQL node IDs while every
// caller — the issue-create skill's own worked examples included — passed
// names. The create mutation then failed with `Could not resolve to a node
// with the global id of 'type:bug'`, and agents fell back to `gh issue create`,
// bypassing the deterministic path the skill mandates.

const resolveNamesFixture = `[
	{"node_id": "LA_bug", "name": "type:bug", "description": "", "color": "d73a4a"},
	{"node_id": "LA_sdk", "name": "component:sdk", "description": "", "color": "a2eeef"},
	{"node_id": "LA_hi",  "name": "priority:high", "description": "", "color": "b60205"}
]`

func TestLabelResolveNames_MapsNamesToIDsInOrder(t *testing.T) {
	client, cleanup := mockForgeServer(t,
		map[string]string{"GET /repos/nightgauge/nightgauge/labels": resolveNamesFixture})
	defer cleanup()

	svc := NewLabelService(client, "nightgauge", "nightgauge")
	got, err := svc.ResolveNames(context.Background(), []string{"component:sdk", "type:bug"})
	if err != nil {
		t.Fatalf("ResolveNames() error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ResolveNames() returned %d labels, want 2", len(got))
	}
	// Order is the caller's, not the repository's — a caller reporting what it
	// applied must be able to pair its input with the result.
	if got[0].ID != "LA_sdk" || got[1].ID != "LA_bug" {
		t.Errorf("ids = [%s %s], want [LA_sdk LA_bug]", got[0].ID, got[1].ID)
	}
	if got[0].Name != "component:sdk" || got[1].Name != "type:bug" {
		t.Errorf("names = [%s %s], want [component:sdk type:bug]", got[0].Name, got[1].Name)
	}
}

func TestLabelResolveNames_UnknownNameIsAnError(t *testing.T) {
	client, cleanup := mockForgeServer(t,
		map[string]string{"GET /repos/nightgauge/nightgauge/labels": resolveNamesFixture})
	defer cleanup()

	svc := NewLabelService(client, "nightgauge", "nightgauge")
	got, err := svc.ResolveNames(context.Background(), []string{"type:bug", "no:such-label"})
	if err == nil {
		t.Fatalf("ResolveNames() with an unknown name returned %v, want an error", got)
	}
	// The message has to name the label AND the repo: "not found" alone leaves
	// the caller guessing whether the name or the repo is wrong.
	if !strings.Contains(err.Error(), `"no:such-label"`) {
		t.Errorf("error %q does not name the missing label", err)
	}
	if !strings.Contains(err.Error(), "nightgauge/nightgauge") {
		t.Errorf("error %q does not name the repository", err)
	}
	// All-or-nothing: a partial result would let a caller label an issue with
	// half of what it asked for and report success.
	if got != nil {
		t.Errorf("ResolveNames() returned %v alongside the error, want nil", got)
	}
}

func TestLabelResolveNames_ReportsEveryUnknownNameAtOnce(t *testing.T) {
	client, cleanup := mockForgeServer(t,
		map[string]string{"GET /repos/nightgauge/nightgauge/labels": resolveNamesFixture})
	defer cleanup()

	svc := NewLabelService(client, "nightgauge", "nightgauge")
	_, err := svc.ResolveNames(context.Background(), []string{"nope:one", "type:bug", "nope:two"})
	if err == nil {
		t.Fatal("ResolveNames() returned nil error, want an error")
	}
	for _, want := range []string{`"nope:one"`, `"nope:two"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q omits %s — a caller with two bad labels should "+
				"not learn about them over two round trips", err, want)
		}
	}
}

func TestLabelResolveNames_IsCaseSensitive(t *testing.T) {
	client, cleanup := mockForgeServer(t,
		map[string]string{"GET /repos/nightgauge/nightgauge/labels": resolveNamesFixture})
	defer cleanup()

	svc := NewLabelService(client, "nightgauge", "nightgauge")
	// GitHub label names are case-sensitive. A case-insensitive match would
	// silently apply a different label than the caller named.
	if _, err := svc.ResolveNames(context.Background(), []string{"Type:Bug"}); err == nil {
		t.Error("ResolveNames(\"Type:Bug\") succeeded; want a miss against \"type:bug\"")
	}
}

func TestLabelResolveNames_EmptyInputMakesNoRequest(t *testing.T) {
	// No REST fixture registered: mockForgeServer fails the test on any call,
	// so this asserts the zero-label path costs nothing.
	client, cleanup := mockForgeServer(t, map[string]string{})
	defer cleanup()

	svc := NewLabelService(client, "nightgauge", "nightgauge")
	got, err := svc.ResolveNames(context.Background(), nil)
	if err != nil {
		t.Fatalf("ResolveNames(nil) error: %v", err)
	}
	if got != nil {
		t.Errorf("ResolveNames(nil) = %v, want nil", got)
	}
}

func TestLabelResolveNames_TrimsSurroundingWhitespace(t *testing.T) {
	client, cleanup := mockForgeServer(t,
		map[string]string{"GET /repos/nightgauge/nightgauge/labels": resolveNamesFixture})
	defer cleanup()

	svc := NewLabelService(client, "nightgauge", "nightgauge")
	// `--labels "type:bug, component:sdk"` is how a human writes a list.
	got, err := svc.ResolveNames(context.Background(), []string{"type:bug", " component:sdk "})
	if err != nil {
		t.Fatalf("ResolveNames() error: %v", err)
	}
	if len(got) != 2 || got[1].ID != "LA_sdk" {
		t.Errorf("ResolveNames() = %v, want the padded name resolved", got)
	}
}

// TestResolveNamesThenCreateIssue_SendsIDsNotNames is the end-to-end assertion
// #1214 was missing: it inspects the createIssue mutation's actual variables.
//
// Every other test here can pass while names are handed straight to `labelIds`,
// because nothing else reads the request body. That is exactly how the bug
// shipped — the flag was named, documented and exercised, and no test ever
// looked at what went over the wire.
func TestResolveNamesThenCreateIssue_SendsIDsNotNames(t *testing.T) {
	var (
		mu       sync.Mutex
		seen     []string
		mutation map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasPrefix(r.URL.Path, "/repos/") && strings.HasSuffix(r.URL.Path, "/labels"):
			seen = append(seen, "GET labels")
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, resolveNamesFixture)
		default:
			seen = append(seen, "POST /graphql")
			raw, _ := io.ReadAll(r.Body)
			var payload struct {
				Variables map[string]any `json:"variables"`
			}
			if err := json.Unmarshal(raw, &payload); err != nil {
				t.Errorf("decode mutation body: %v", err)
			}
			mutation = payload.Variables
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"data":{"createIssue":{"issue":{"id":"I_1","number":42,"url":"https://x/42"}}}}`)
		}
	}))
	defer srv.Close()

	client := NewClientWithURL("test-token", srv.URL)
	resolved, err := NewLabelService(client, "nightgauge", "nightgauge").
		ResolveNames(context.Background(), []string{"type:bug", "component:sdk"})
	if err != nil {
		t.Fatalf("ResolveNames() error: %v", err)
	}
	ids := make([]string, len(resolved))
	for i, l := range resolved {
		ids[i] = l.ID
	}

	issue, err := NewIssueService(client).
		CreateIssue(context.Background(), "REPO_NODE_ID", "t", "b", ids)
	if err != nil {
		t.Fatalf("CreateIssue() error: %v", err)
	}
	if issue.Number != 42 {
		t.Errorf("issue.Number = %d, want 42", issue.Number)
	}

	// Exactly one label lookup, then the create — not a lookup per label.
	if want := []string{"GET labels", "POST /graphql"}; !reflect.DeepEqual(seen, want) {
		t.Errorf("requests = %v, want %v", seen, want)
	}

	input, ok := mutation["input"].(map[string]any)
	if !ok {
		t.Fatalf("mutation variables have no input object: %#v", mutation)
	}
	got, _ := json.Marshal(input["labelIds"])
	if want := `["LA_bug","LA_sdk"]`; string(got) != want {
		t.Errorf("labelIds = %s, want %s — a name reaching labelIds is the bug "+
			"(`Could not resolve to a node with the global id of 'type:bug'`)", got, want)
	}
}

// TestResolveNamesThenCreateIssue_UnknownNameCreatesNothing pins the ordering
// the fix depends on. A half-labelled issue that reports success is worse than
// a refusal, because the missing label is what the board syncs on.
func TestResolveNamesThenCreateIssue_UnknownNameCreatesNothing(t *testing.T) {
	var (
		mu   sync.Mutex
		seen []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/labels") {
			seen = append(seen, "GET labels")
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, resolveNamesFixture)
			return
		}
		seen = append(seen, "POST /graphql")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":{}}`)
	}))
	defer srv.Close()

	client := NewClientWithURL("test-token", srv.URL)
	if _, err := NewLabelService(client, "nightgauge", "nightgauge").
		ResolveNames(context.Background(), []string{"no:such-label"}); err == nil {
		t.Fatal("ResolveNames() returned nil error for an unknown label")
	}

	mu.Lock()
	defer mu.Unlock()
	for _, call := range seen {
		if call == "POST /graphql" {
			t.Fatalf("a mutation was issued despite the unresolvable label: %v", seen)
		}
	}
}

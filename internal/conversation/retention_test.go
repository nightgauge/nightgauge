package conversation

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/platform"
)

// The floor test the issue names (#1893): a classifier standing in for a
// model reply that claims `ephemeral` on a turn whose recorded tool calls
// include a mutating one is overridden, and the reported class is durable.
// It holds for every terminal state, so a turn that mutated and then failed
// or was cancelled stays durable too.
func TestRetain_TheMutationFloorOverridesTheClassifier(t *testing.T) {
	claimsEphemeral := func(TurnOutcome) Classification {
		return Classification{Class: platform.TurnRetentionEphemeral, Rule: "model-said-so"}
	}
	calls := []ToolCall{{Name: "queue_status"}, {Name: "file_issue", Mutating: true}}
	for _, state := range []string{platform.TurnStateCompleted, platform.TurnStateFailed, platform.TurnStateCancelled} {
		got := retain(claimsEphemeral, TurnOutcome{State: state, ToolCalls: calls})
		if got.Class != platform.TurnRetentionDurable || got.Rule != ruleMutationFloor {
			t.Errorf("%s turn that filed an issue = %+v, want durable by %s", state, got, ruleMutationFloor)
		}
	}
	// The same classifier is taken at its word on a turn that changed
	// nothing: the floor only ever raises.
	got := retain(claimsEphemeral, TurnOutcome{State: platform.TurnStateCompleted, ToolCalls: calls[:1]})
	if got.Class != platform.TurnRetentionEphemeral || got.Rule != "model-said-so" {
		t.Errorf("read-only turn = %+v, want the classifier's ephemeral", got)
	}
}

// Only an explicit `ephemeral` drops a turn from later context: an empty,
// misspelt or unknown answer is durable.
func TestRetain_OnlyAnExplicitEphemeralIsEphemeral(t *testing.T) {
	for _, answer := range []string{"", "Ephemeral", "transient", "ephemeral "} {
		classify := func(TurnOutcome) Classification { return Classification{Class: answer, Rule: "r"} }
		got := retain(classify, TurnOutcome{State: platform.TurnStateCompleted})
		if got.Class != platform.TurnRetentionDurable {
			t.Errorf("classifier answered %q: class = %q, want durable", answer, got.Class)
		}
	}
}

func TestClassifyByRules(t *testing.T) {
	read := []ToolCall{{Name: "queue_status"}}
	cases := []struct {
		name  string
		o     TurnOutcome
		class string
		rule  string
	}{
		{"completed, no tools", TurnOutcome{State: platform.TurnStateCompleted}, platform.TurnRetentionDurable, "no-live-read"},
		{"completed, read tool", TurnOutcome{State: platform.TurnStateCompleted, ToolCalls: read}, platform.TurnRetentionEphemeral, "live-read"},
		{"failed, read tool", TurnOutcome{State: platform.TurnStateFailed, ToolCalls: read}, platform.TurnRetentionDurable, "unfinished"},
		{"cancelled, no tools", TurnOutcome{State: platform.TurnStateCancelled}, platform.TurnRetentionDurable, "unfinished"},
		{"unknown state", TurnOutcome{State: "running", ToolCalls: read}, platform.TurnRetentionDurable, ruleNoMatch},
	}
	for _, c := range cases {
		if got := classifyByRules(c.o); got.Class != c.class || got.Rule != c.rule {
			t.Errorf("%s: %+v, want %s by %s", c.name, got, c.class, c.rule)
		}
	}
}

// The rule table is documented: docs/CONVERSATION_TURNS.md § Retention class
// lists exactly the rules the daemon applies, each with its states, tool
// record and class, so a reader can tell why a turn got its class.
func TestRetentionRules_MatchTheDocumentedTable(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "CONVERSATION_TURNS.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, section, ok := strings.Cut(string(raw), "\n## Retention class\n")
	if !ok {
		t.Fatal("docs/CONVERSATION_TURNS.md has no Retention class section")
	}
	section, _, _ = strings.Cut(section, "\n## ")
	documented := map[string][]string{}
	for _, line := range strings.Split(section, "\n") {
		cells := strings.Split(line, "|")
		if len(cells) < 6 || !strings.HasPrefix(strings.TrimSpace(cells[1]), "`") {
			continue
		}
		var row []string
		for _, c := range cells[1:5] {
			row = append(row, strings.TrimSpace(c))
		}
		documented[strings.Trim(row[0], "`")] = row[1:]
	}
	toolsDoc := map[string]string{toolsAny: "any", toolsNone: "none", toolsSome: "at least one"}
	want := map[string][]string{ruleNoMatch: {"any other", "any", "`" + platform.TurnRetentionDurable + "`"}}
	for _, r := range retentionRules {
		var states []string
		for _, s := range r.states {
			states = append(states, "`"+s+"`")
		}
		want[r.id] = []string{strings.Join(states, ", "), toolsDoc[r.tools], "`" + r.class + "`"}
	}
	for id, w := range want {
		if got, ok := documented[id]; !ok || !slices.Equal(got, w) {
			t.Errorf("rule %s documented as %q, want %q", id, got, w)
		}
	}
	for id := range documented {
		if _, ok := want[id]; !ok {
			t.Errorf("rule %s is documented but the daemon has no such rule", id)
		}
	}
	if !strings.Contains(section, "`"+ruleMutationFloor+"`") {
		t.Errorf("the section does not name the %s rule", ruleMutationFloor)
	}
}

// The tool record reads the claude stream: each call once, whether it is
// seen as a partial block start, a completed assistant message, or both, and
// nothing from reply text, tool results or an unreadable line.
func TestToolRecord_RecordsEachCallOnce(t *testing.T) {
	rec := newToolRecord(func(name string) bool { return name == "file_issue" })
	for _, line := range []string{
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"queue_status","input":{}}}}`,
		`{"type":"assistant","message":{"id":"m1","content":[{"type":"tool_use","id":"toolu_1","name":"queue_status","input":{}}]},"parent_tool_use_id":null}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"tool_use: 3 queued"}]}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"a tool_use block named file_issue"}}}`,
		`{"type":"assistant","message":{"id":"m2","content":[{"type":"text","text":"filing"},{"type":"tool_use","id":"toolu_2","name":"file_issue","input":{"title":"x"}}]}}`,
		`{"type":"assistant","message":{"id":"m3","content":[{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{}}]},"parent_tool_use_id":"toolu_9"}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"file_issue"` + "\n",
	} {
		rec.observe(line)
	}
	want := []ToolCall{{Name: "queue_status"}, {Name: "file_issue", Mutating: true}, {Name: "web_search"}}
	if !slices.Equal(rec.calls, want) {
		t.Errorf("calls = %+v, want %+v", rec.calls, want)
	}

	unplaced := newToolRecord(everyToolMutates)
	unplaced.observe(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_1","name":"queue_status"}]}}`)
	if len(unplaced.calls) != 1 || !unplaced.calls[0].Mutating {
		t.Errorf("with no tool registry: calls = %+v, want queue_status counted as mutating", unplaced.calls)
	}
}

// toolTurnFixture writes a claude stream in which the turn calls each named
// tool, gets its result, and then answers, ending with resultSubtype.
func toolTurnFixture(t *testing.T, resultSubtype string, tools ...string) string {
	t.Helper()
	lines := []string{
		`{"type":"system","subtype":"init","model":"claude-sonnet-5-5","per_turn_effort_active":true}`,
		`{"type":"stream_event","event":{"type":"message_start","message":{"id":"msg_tools","usage":{"input_tokens":100,"output_tokens":1}}}}`,
	}
	for i, name := range tools {
		id := fmt.Sprintf("toolu_%02d", i+1)
		lines = append(lines,
			fmt.Sprintf(`{"type":"stream_event","event":{"type":"content_block_start","index":%d,"content_block":{"type":"tool_use","id":%q,"name":%q,"input":{}}}}`, i, id, name),
			fmt.Sprintf(`{"type":"stream_event","event":{"type":"content_block_delta","index":%d,"delta":{"type":"input_json_delta","partial_json":"{}"}}}`, i),
			fmt.Sprintf(`{"type":"stream_event","event":{"type":"content_block_stop","index":%d}}`, i),
			fmt.Sprintf(`{"type":"assistant","message":{"id":"msg_tools","role":"assistant","content":[{"type":"tool_use","id":%q,"name":%q,"input":{}}]},"parent_tool_use_id":null}`, id, name),
			fmt.Sprintf(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":%q,"content":"ok"}]},"parent_tool_use_id":null}`, id),
		)
	}
	answer := fakeTextTurn([]string{"three ", "issues are queued"}, 200, 8)
	answer[len(answer)-1] = strings.Replace(answer[len(answer)-1], `"subtype":"success"`, `"subtype":"`+resultSubtype+`"`, 1)
	lines = append(lines, answer[1:]...)
	path := filepath.Join(t.TempDir(), "tool_turn.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// runToolTurn runs one turn through a daemon whose tool boundary says only
// file_issue mutates (nil keeps the host's default), and returns its
// terminal report.
func runToolTurn(t *testing.T, isMutating func(string) bool, resultSubtype string, tools ...string) map[string]any {
	t.Helper()
	hermetic(t)
	installFakeAdapters(t)
	t.Setenv("FAKE_ADAPTER_FIXTURE", toolTurnFixture(t, resultSubtype, tools...))
	fp := newFakePlatform(t)
	startDaemon(t, fp, workspace(t, claudeWorkspace), func(c *Config) { c.IsMutating = isMutating })
	fp.deliver(fp.postTurn("conv-1", "turn-1", "cmd-1", "What is queued?", nil))
	waitFor(t, "the terminal report", 20*time.Second, func() bool { return fp.endFrame("turn-1") != nil })
	if got := fp.reply("turn-1"); got != "three issues are queued" {
		t.Errorf("reply = %q: an ephemeral turn's reply is still returned in full", got)
	}
	return fp.endFrame("turn-1")
}

// The executor tests the issue names (#1893): a turn that only read live
// state reports `ephemeral`, and a turn that filed an issue reports
// `durable`. The rule table alone would call the second ephemeral too, since
// it read nothing the rules can tell apart, so deleting the mutation floor
// turns that case red.
func TestTurn_RetentionClassFollowsTheToolCalls(t *testing.T) {
	onlyFileIssueMutates := func(name string) bool { return name == "file_issue" }
	cases := []struct {
		name       string
		isMutating func(string) bool
		subtype    string
		tools      []string
		state      string
		class      string
	}{
		{"a read-only status turn", onlyFileIssueMutates, "success", []string{"queue_status"}, "completed", "ephemeral"},
		{"a turn that filed an issue", onlyFileIssueMutates, "success", []string{"queue_status", "file_issue"}, "completed", "durable"},
		{"a tool with no registry", nil, "success", []string{"queue_status"}, "completed", "durable"},
		{"a read that then failed", onlyFileIssueMutates, "error_during_execution", []string{"queue_status"}, "failed", "durable"},
		{"no tools", onlyFileIssueMutates, "success", nil, "completed", "durable"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			end := runToolTurn(t, c.isMutating, c.subtype, c.tools...)
			if end["state"] != c.state || end["retention_class"] != c.class {
				t.Errorf("terminal report = %v, want %s and %s", end, c.state, c.class)
			}
		})
	}
}

// The daemon applies no retention window of its own: the hosted service's
// context read is the whole of a turn's prior context, so every message the
// read returns, ephemeral ones included, is rendered, in order. A daemon-side
// window or class filter fails this.
func TestRenderMessage_KeepsEveryMessageTheContextReadReturns(t *testing.T) {
	var msgs []platform.TurnContextMessage
	var want []string
	pos := 0
	add := func(turn, role, text, class string) {
		pos++
		msgs = append(msgs, platform.TurnContextMessage{
			ID: fmt.Sprintf("m%d", pos), TurnID: turn, Position: pos, Role: role, Content: &text, RetentionClass: class,
		})
		want = append(want, text)
	}
	for i := 0; i < 52; i++ {
		class := platform.TurnRetentionEphemeral
		if i == 0 || i == 30 {
			class = platform.TurnRetentionDurable
		}
		turn := fmt.Sprintf("t%d", i)
		add(turn, "user", fmt.Sprintf("question %d", i), class)
		add(turn, "assistant", fmt.Sprintf("answer %d", i), class)
	}
	next := "what now?"
	msgs = append(msgs, platform.TurnContextMessage{ID: "last", TurnID: "t-new", Position: pos + 1, Role: "user", Content: &next})

	got, err := renderMessage(platform.TurnContext{TurnID: "t-new", Messages: msgs}, "t-new")
	if err != nil {
		t.Fatal(err)
	}
	var prior []priorMessage
	if err := json.Unmarshal([]byte(jsonLine(t, got)), &prior); err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, p := range prior {
		texts = append(texts, p.Text)
	}
	if !slices.Equal(texts, want) {
		t.Errorf("rendered %d prior messages, want all %d the read returned, in order", len(texts), len(want))
	}
}

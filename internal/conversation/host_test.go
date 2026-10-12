package conversation

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/credshape"
	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/platform"
)

// fixtureText is the reply text a recorded stream carries.
func fixtureText(t *testing.T, name string) string {
	t.Helper()
	f, err := os.Open(fixture(name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var b strings.Builder
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() {
		var ev struct {
			Type  string `json:"type"`
			Event struct {
				Type  string `json:"type"`
				Delta struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"delta"`
			} `json:"event"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) == nil && ev.Type == "stream_event" &&
			ev.Event.Type == "content_block_delta" && ev.Event.Delta.Type == "text_delta" {
			b.WriteString(ev.Event.Delta.Text)
		}
	}
	return b.String()
}

// A turn submitted to a running daemon over its agent command stream streams
// its reply back incrementally: at least two delta posts arrive before the
// terminal report, so a batch-then-emit implementation fails. The reply is
// the recorded CLI's, whole and in order, and the terminal report states the
// adapter, mode, effort and token counts the turn ran with.
func TestTurn_StreamsTheReplyIncrementallyOverTheCommandStream(t *testing.T) {
	hermetic(t)
	rec := installFakeAdapters(t)
	t.Setenv("FAKE_ADAPTER_FIXTURE", fixture("claude_turn_stream.jsonl"))
	t.Setenv("FAKE_ADAPTER_DELAY_MS", "25")
	fp := newFakePlatform(t)
	startDaemon(t, fp, workspace(t, claudeWorkspace), nil)

	fp.deliver(fp.postTurn("conv-1", "turn-1", "cmd-1", "Write five sentences about rivers.", nil))
	waitFor(t, "the terminal report", 20*time.Second, func() bool { return fp.endFrame("turn-1") != nil })

	posts := fp.framePosts("turn-1")
	deltaPostsBeforeEnd := 0
	for _, p := range posts {
		hasDelta, hasEnd := false, false
		for _, f := range p.frames {
			hasDelta = hasDelta || f["type"] == "turn.delta"
			hasEnd = hasEnd || f["type"] == "turn.end"
		}
		if hasEnd {
			break
		}
		if hasDelta {
			deltaPostsBeforeEnd++
		}
	}
	if deltaPostsBeforeEnd < 2 {
		t.Errorf("%d delta posts before the terminal report, want at least 2 (posts: %d)", deltaPostsBeforeEnd, len(posts))
	}
	if got, want := fp.reply("turn-1"), fixtureText(t, "claude_turn_stream.jsonl"); got != want {
		t.Errorf("reply =\n%q\nwant\n%q", got, want)
	}
	end := fp.endFrame("turn-1")
	if end["state"] != "completed" || end["retention_class"] != "durable" {
		t.Errorf("terminal report = %v", end)
	}
	profile, _ := end["execution_profile"].(map[string]any)
	if profile["adapter"] != "claude-headless" || profile["performance_mode"] != "elevated" || profile["effort"] != "low" {
		t.Errorf("execution profile = %v", profile)
	}
	// The recorded result: 2 fresh + 1489 cache-written + 535 cache-read
	// prompt tokens, 209 output.
	usage, _ := end["usage"].(map[string]any)
	if usage["input_tokens"] != float64(2026) || usage["output_tokens"] != float64(209) {
		t.Errorf("usage = %v, want 2026 in / 209 out", usage)
	}
	runs := invocations(t, rec)
	if len(runs) != 1 || runs[0].Name != "claude" || runs[0].Stdin != "Write five sentences about rivers." {
		t.Fatalf("adapter runs = %+v", runs)
	}
	for _, want := range [][]string{{"--model", "claude-sonnet-5-5"}, {"--effort", "low"}} {
		if !containsPair(runs[0].Args, want[0], want[1]) {
			t.Errorf("argv %v lacks %v", runs[0].Args, want)
		}
	}
}

func containsPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

// A turn is claimed before it runs: nothing is spawned before the daemon's
// own acknowledgement succeeded, a failed acknowledgement runs nothing, and a
// redelivered command runs nothing more, here or on another daemon that gets
// ALREADY_ACKED.
func TestTurn_ClaimedOnceBeforeAnythingRuns(t *testing.T) {
	hermetic(t)
	rec := installFakeAdapters(t)
	t.Setenv("FAKE_ADAPTER_FIXTURE", fixture("claude_turn_replay_1.jsonl"))
	fp := newFakePlatform(t)
	root := workspace(t, claudeWorkspace)
	d := startDaemon(t, fp, root, nil)
	fp.ackPlan["cmd-1"] = []int{http.StatusInternalServerError}

	frame := fp.postTurn("conv-1", "turn-1", "cmd-1", "My favourite fruit is the plum. Reply with just the word: noted.", nil)
	fp.deliver(frame)
	waitFor(t, "the failed acknowledgement", 5*time.Second, func() bool { return len(fp.ackRecords()) == 1 })
	time.Sleep(200 * time.Millisecond)
	if runs := invocations(t, rec); len(runs) != 0 {
		t.Fatalf("a turn ran after its acknowledgement failed: %+v", runs)
	}

	fp.deliver(frame) // the hosted service replays an unacknowledged command
	waitFor(t, "the turn to complete", 10*time.Second, func() bool { return fp.endFrame("turn-1") != nil })
	acks := fp.ackRecords()
	var spawned time.Time
	select {
	case <-d.pgids:
		spawned = time.Now()
	default:
		t.Fatal("no process was spawned")
	}
	if len(acks) != 2 || acks[1].status != http.StatusOK || !acks[1].at.Before(spawned) {
		t.Fatalf("acks = %+v; the spawn must follow a successful one", acks)
	}

	fp.deliver(frame) // a redelivery after the claim: never acknowledged again
	startDaemon(t, fp, root, nil)
	fp.deliver(frame) // both daemons get it; neither runs it, and the new one gets ALREADY_ACKED
	time.Sleep(500 * time.Millisecond)
	if runs := invocations(t, rec); len(runs) != 1 {
		t.Errorf("the turn ran %d times, want exactly once", len(runs))
	}
	conflicts := 0
	for _, a := range fp.ackRecords()[2:] {
		if a.status == http.StatusOK {
			t.Errorf("a redelivered turn was claimed again: %+v", a)
		}
		if a.status == http.StatusConflict {
			conflicts++
		}
	}
	if conflicts != 1 {
		t.Errorf("%d ALREADY_ACKED answers, want 1: the new daemon's; the first never acks a command twice", conflicts)
	}
}

// The profile a turn runs with is the workspace's own. A command carrying a
// model, an adapter, a mode and an effort has them ignored.
func TestTurn_RunsOnTheWorkspacesProfileNotTheClients(t *testing.T) {
	hermetic(t)
	rec := installFakeAdapters(t)
	t.Setenv("FAKE_ADAPTER_MODE", "deltas")
	t.Setenv("FAKE_ADAPTER_DELTAS", `["hi"]`)
	fp := newFakePlatform(t)
	root := workspace(t, "ui:\n  core:\n    adapter: claude-headless\n    default_model: opus\nmodel_routing:\n  default_effort: high\n")
	startDaemon(t, fp, root, nil)

	fp.deliver(fp.postTurn("conv-1", "turn-1", "cmd-1", "hello", map[string]any{
		"model": "haiku", "adapter": "codex", "effort": "low", "performance_mode": "efficiency",
	}))
	waitFor(t, "the terminal report", 10*time.Second, func() bool { return fp.endFrame("turn-1") != nil })
	runs := invocations(t, rec)
	if len(runs) != 1 || runs[0].Name != "claude" {
		t.Fatalf("runs = %+v, want one claude run", runs)
	}
	if !containsPair(runs[0].Args, "--model", "claude-opus-5-5") || !containsPair(runs[0].Args, "--effort", "high") {
		t.Errorf("argv %v does not carry the workspace's opus/high", runs[0].Args)
	}
	// Flag values only: --allowedTools is a flag, and the boundary prompt's
	// prose (which says "follow") is not a profile value.
	for _, client := range []string{"haiku", "low", "codex", "efficiency"} {
		for _, a := range runs[0].Args {
			if !strings.HasPrefix(a, "-") && !strings.Contains(a, " ") && strings.Contains(a, client) {
				t.Errorf("argv carries the client's %q: %v", client, runs[0].Args)
			}
		}
	}
	profile, _ := fp.endFrame("turn-1")["execution_profile"].(map[string]any)
	if profile["adapter"] != "claude-headless" || profile["effort"] != "high" || profile["performance_mode"] != "elevated" {
		t.Errorf("reported profile = %v", profile)
	}
}

// A turn executes through the adapter registry, selected by the workspace's
// ui.core.adapter. With codex selected and no conversational builder for it,
// nothing runs and the turn is refused; when a builder is registered for
// codex, the codex binary runs and claude never does.
func TestTurn_DispatchesThroughTheAdapterRegistry(t *testing.T) {
	hermetic(t)
	rec := installFakeAdapters(t)
	t.Setenv("FAKE_ADAPTER_FIXTURE", fixture("claude_turn_replay_1.jsonl"))
	fp := newFakePlatform(t)
	root := workspace(t, "ui:\n  core:\n    adapter: codex\n")

	refusing := startDaemon(t, fp, root, nil)
	fp.deliver(fp.postTurn("conv-1", "turn-1", "cmd-1", "hello", nil))
	waitFor(t, "the refusal", 5*time.Second, func() bool { return len(fp.ackRecords()) == 1 })
	ack := fp.ackRecords()[0]
	if ack.outcome != "rejected" || !strings.HasPrefix(ack.detail, "adapter-not-conversational:") {
		t.Errorf("ack = %+v, want rejected adapter-not-conversational", ack)
	}
	if runs := invocations(t, rec); len(runs) != 0 {
		t.Errorf("a turn on a non-conversational adapter ran: %+v", runs)
	}
	refusing.cancel()
	refusing.host.Wait()

	dispatching := startDaemon(t, fp, root, nil)
	dispatching.host.planner.builderFor = func(name string) (adapters.ConversationBuilder, bool) {
		if name == "codex" {
			return stubBuilder{cmd: "codex"}, true
		}
		return nil, false
	}
	fp.deliver(fp.postTurn("conv-2", "turn-2", "cmd-2", "hello", nil))
	waitFor(t, "the terminal report", 10*time.Second, func() bool { return fp.endFrame("turn-2") != nil })
	runs := invocations(t, rec)
	if len(runs) != 1 || runs[0].Name != "codex" {
		t.Errorf("runs = %+v, want the codex binary alone", runs)
	}
}

// stubBuilder is a conversational builder for an adapter the registry has
// none for, standing in for the next one the spike clears.
type stubBuilder struct{ cmd string }

func (b stubBuilder) BuildConversationTurn(adapters.ConversationTurnOptions) (string, []string, map[string]string) {
	return b.cmd, []string{"exec", "--ephemeral"}, nil
}
func (stubBuilder) ConversationStreamFormat() string { return adapters.ConversationStreamClaude }
func (stubBuilder) HasEffortControl() bool           { return false }

// A second message in a conversation has the first turn's context: the
// second turn's process is new and its only knowledge of the first is the
// context read replayed in its stdin, from which it answers.
func TestTurn_SecondTurnHasTheFirstTurnsContext(t *testing.T) {
	hermetic(t)
	rec := installFakeAdapters(t)
	t.Setenv("FAKE_ADAPTER_MODE", "fruit")
	fp := newFakePlatform(t)
	startDaemon(t, fp, workspace(t, claudeWorkspace), nil)

	fp.deliver(fp.postTurn("conv-1", "turn-1", "cmd-1", "My favourite fruit is the plum. Reply with just the word: noted.", nil))
	waitFor(t, "turn 1", 10*time.Second, func() bool { return fp.turnState("turn-1") == "completed" })
	fp.deliver(fp.postTurn("conv-1", "turn-2", "cmd-2", "Which fruit did I say was my favourite? Answer in one word.", nil))
	waitFor(t, "turn 2", 10*time.Second, func() bool { return fp.turnState("turn-2") == "completed" })

	if got := fp.reply("turn-1"); got != "noted" {
		t.Errorf("turn 1 reply = %q", got)
	}
	if got := fp.reply("turn-2"); !strings.Contains(got, "plum") {
		t.Errorf("turn 2 reply = %q, want it to name the plum only turn 1 mentioned", got)
	}
	runs := invocations(t, rec)
	if len(runs) != 2 || runs[0].PID == runs[1].PID {
		t.Fatalf("runs = %+v, want two separate processes", runs)
	}
	for _, r := range runs {
		for _, a := range r.Args {
			if a == "--resume" || a == "--continue" || a == "--session-id" {
				t.Errorf("a turn named a session: %v", r.Args)
			}
		}
		if !slices.Contains(r.Args, "--no-session-persistence") {
			t.Errorf("a turn ran with session persistence on: %v", r.Args)
		}
	}
}

// The real CLI answered the second turn from the message this host renders:
// the recorded stdin carried the same messages, in the same order, with the
// same roles and text, ahead of the same new message, and the recorded reply
// names the plum. The capture predates the authorship and data framing #1570
// added (author, by_poster, and the framing lines), which add to each
// message and change none of what the turn answered from.
func TestRenderMessage_MatchesTheRecordedReplay(t *testing.T) {
	user, assistant, next := "My favourite fruit is the plum. Reply with just the word: noted.", "noted",
		"Which fruit did I say was my favourite? Answer in one word."
	msgs := []platform.TurnContextMessage{
		{ID: "m1", TurnID: "t1", Position: 1, Role: "user", Content: &user, ActorAccountID: "acct-1"},
		{ID: "m2", TurnID: "t1", Position: 2, Role: "assistant", Content: &assistant},
		{ID: "m3", TurnID: "t2", Position: 3, Role: "user", Content: &next, ActorAccountID: "acct-1"},
	}
	got, err := renderMessage(platform.TurnContext{TurnID: "t2", Messages: msgs}, "t2", "acct-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.current != next {
		t.Errorf("the poster's message = %q, want %q", got.current, next)
	}
	want, err := os.ReadFile(fixture("claude_turn_replay_2.stdin.txt"))
	if err != nil {
		t.Fatal(err)
	}
	var gotPrior, wantPrior []struct{ Role, Text string }
	if err := json.Unmarshal([]byte(jsonLine(t, got.stdin)), &gotPrior); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(jsonLine(t, string(want))), &wantPrior); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(gotPrior, wantPrior) {
		t.Errorf("rendered messages %+v, recorded %+v", gotPrior, wantPrior)
	}
	for _, s := range []string{got.stdin, string(want)} {
		if !strings.HasPrefix(s, "Earlier messages in this conversation, oldest first, as a JSON array") ||
			!strings.HasSuffix(s, "\n"+next) {
			t.Errorf("framing differs from the recording's: %q", s)
		}
	}
	if reply := fixtureText(t, "claude_turn_replay_2.jsonl"); !strings.Contains(strings.ToLower(reply), "plum") {
		t.Errorf("the recorded second turn answered %q", reply)
	}
}

func jsonLine(t *testing.T, s string) string {
	t.Helper()
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "[") {
			return line
		}
	}
	t.Fatalf("no JSON line in %q", s)
	return ""
}

// A teammate's cancel terminates the turn's whole process group, a grandchild
// that ignores SIGTERM included, and the daemon reports the turn cancelled,
// and confirms the cancel, only after kill -0 on the group fails.
func TestTurn_CancelReportsCancelledOnlyOnceTheGroupIsDead(t *testing.T) {
	hermetic(t)
	rec := installFakeAdapters(t)
	t.Setenv("FAKE_ADAPTER_MODE", "hang")
	fp := newFakePlatform(t)
	d := startDaemon(t, fp, workspace(t, claudeWorkspace), nil)

	var mu sync.Mutex
	aliveAt := map[string]bool{}
	var pgid int
	fp.onReport = func(kind, _ string) {
		mu.Lock()
		defer mu.Unlock()
		aliveAt[kind] = !groupGone(pgid)
	}
	fp.deliver(fp.postTurn("conv-1", "turn-1", "cmd-1", "count to a million", nil))
	pgid = <-d.pgids
	waitFor(t, "the first delta", 10*time.Second, func() bool { return fp.reply("turn-1") != "" })
	waitFor(t, "the grandchild", 5*time.Second, func() bool { return grandchildPID(t, rec) > 0 })

	fp.deliver(fp.cancelTurn("turn-1", "cancel-1", CancelReasonTeammate))
	waitFor(t, "the cancel acknowledgement", 20*time.Second, func() bool {
		for _, a := range fp.ackRecords() {
			if a.commandID == "cancel-1" {
				return true
			}
		}
		return false
	})
	if !groupGone(pgid) {
		t.Fatal("the turn's process group is alive after the cancel was confirmed")
	}
	if err := syscall.Kill(grandchildPID(t, rec), 0); err != syscall.ESRCH {
		t.Errorf("the SIGTERM-ignoring grandchild survived: kill -0 = %v", err)
	}
	end := fp.endFrame("turn-1")
	if end == nil || end["state"] != "cancelled" {
		t.Fatalf("terminal report = %v, want cancelled", end)
	}
	mu.Lock()
	defer mu.Unlock()
	if alive, seen := aliveAt["end:cancelled"]; !seen || alive {
		t.Errorf("cancelled was reported while the group was alive (seen=%v alive=%v)", seen, alive)
	}
	if alive, seen := aliveAt["cancel-ack:applied"]; !seen || alive {
		t.Errorf("the cancel was confirmed while the group was alive (seen=%v alive=%v)", seen, alive)
	}
	if fp.turnState("turn-1") != "cancelled" {
		t.Errorf("turn state = %s", fp.turnState("turn-1"))
	}
}

func grandchildPID(t *testing.T, rec string) int {
	t.Helper()
	entries, _ := os.ReadDir(rec)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".grandchild") {
			raw, _ := os.ReadFile(rec + "/" + e.Name())
			return atoiOr(string(raw), 0)
		}
	}
	return 0
}

// A stop the hosted service already made terminal (here its timeout) kills
// the process and is confirmed, and the daemon sends no terminal report of
// its own.
func TestTurn_PlatformStopIsConfirmedWithoutAReport(t *testing.T) {
	hermetic(t)
	installFakeAdapters(t)
	t.Setenv("FAKE_ADAPTER_MODE", "hang")
	fp := newFakePlatform(t)
	d := startDaemon(t, fp, workspace(t, claudeWorkspace), nil)
	fp.deliver(fp.postTurn("conv-1", "turn-1", "cmd-1", "count", nil))
	pgid := <-d.pgids
	waitFor(t, "the first delta", 10*time.Second, func() bool { return fp.reply("turn-1") != "" })
	fp.deliver(fp.cancelTurn("turn-1", "cancel-1", "timeout"))
	waitFor(t, "the cancel acknowledgement", 20*time.Second, func() bool {
		acks := fp.ackRecords()
		return len(acks) > 0 && acks[len(acks)-1].commandID == "cancel-1"
	})
	if !groupGone(pgid) {
		t.Error("the process group is alive")
	}
	if acks := fp.ackRecords(); acks[len(acks)-1].outcome != "applied" {
		t.Errorf("cancel ack = %+v", acks[len(acks)-1])
	}
	d.cancel()
	d.host.Wait()
	if end := fp.endFrame("turn-1"); end != nil {
		t.Errorf("a turn the hosted service ended got a report: %v", end)
	}
}

// The daemon's own timeout ends a turn failed/timeout, after its group is
// dead.
func TestTurn_TimeoutEndsTheTurn(t *testing.T) {
	hermetic(t)
	installFakeAdapters(t)
	t.Setenv("FAKE_ADAPTER_MODE", "hang")
	fp := newFakePlatform(t)
	d := startDaemon(t, fp, workspace(t, claudeWorkspace), nil)
	fp.deliver(fp.postTurn("conv-1", "turn-1", "cmd-1", "count", map[string]any{
		"deadline_at": time.Now().Add(1500 * time.Millisecond).UTC().Format(time.RFC3339Nano),
	}))
	pgid := <-d.pgids
	waitFor(t, "the timeout report", 20*time.Second, func() bool { return fp.endFrame("turn-1") != nil })
	if end := fp.endFrame("turn-1"); end["state"] != "failed" || end["failure_reason"] != "timeout" {
		t.Errorf("terminal report = %v", end)
	}
	if !groupGone(pgid) {
		t.Error("the process group outlived the timeout")
	}
}

// A turn that fails after the host claimed it ends failed/host_error, never
// refused: refused means the host declined the turn (#2449). Both a crash of
// the adapter after part of the reply and a failed context read are host
// errors; a turn the workspace cannot serve is declined at the claim instead,
// acknowledged rejected with no terminal report.
func TestTurn_AFailureAfterTheClaimIsAHostError(t *testing.T) {
	hermetic(t)
	installFakeAdapters(t)
	t.Setenv("FAKE_ADAPTER_MODE", "crash")
	fp := newFakePlatform(t)
	startDaemon(t, fp, workspace(t, claudeWorkspace), nil)
	fp.deliver(fp.postTurn("conv-1", "turn-1", "cmd-1", "hello", nil))
	waitFor(t, "the crash report", 20*time.Second, func() bool { return fp.endFrame("turn-1") != nil })
	if end := fp.endFrame("turn-1"); end["state"] != "failed" || end["failure_reason"] != "host_error" {
		t.Errorf("after a crash: terminal report = %v, want failed/host_error", end)
	}

	fp.mu.Lock()
	fp.contextStatus = http.StatusInternalServerError
	fp.mu.Unlock()
	fp.deliver(fp.postTurn("conv-2", "turn-2", "cmd-2", "hello", nil))
	waitFor(t, "the context-read report", 20*time.Second, func() bool { return fp.endFrame("turn-2") != nil })
	if end := fp.endFrame("turn-2"); end["state"] != "failed" || end["failure_reason"] != "host_error" {
		t.Errorf("after a failed context read: terminal report = %v, want failed/host_error", end)
	}

	declining := newFakePlatform(t)
	startDaemon(t, declining, workspace(t, "ui:\n  core:\n    adapter: codex\n"), nil)
	declining.deliver(declining.postTurn("conv-3", "turn-3", "cmd-3", "hello", nil))
	waitFor(t, "the refusal", 5*time.Second, func() bool { return len(declining.ackRecords()) == 1 })
	if ack := declining.ackRecords()[0]; ack.outcome != "rejected" {
		t.Errorf("declined turn: ack = %+v, want rejected", ack)
	}
	if end := declining.endFrame("turn-3"); end != nil {
		t.Errorf("declined turn: terminal report %v, want none (the platform ends it refused)", end)
	}
}

// The token ceiling the command carries is enforced here, from the stream:
// a prompt over it ends the turn failed/token_ceiling.
func TestTurn_TokenCeilingEndsTheTurn(t *testing.T) {
	hermetic(t)
	installFakeAdapters(t)
	t.Setenv("FAKE_ADAPTER_MODE", "usage")
	fp := newFakePlatform(t)
	d := startDaemon(t, fp, workspace(t, claudeWorkspace), nil)
	fp.deliver(fp.postTurn("conv-1", "turn-1", "cmd-1", "hello", map[string]any{"token_ceiling": 1000}))
	pgid := <-d.pgids
	waitFor(t, "the ceiling report", 20*time.Second, func() bool { return fp.endFrame("turn-1") != nil })
	end := fp.endFrame("turn-1")
	if end["state"] != "failed" || end["failure_reason"] != "token_ceiling" {
		t.Errorf("terminal report = %v", end)
	}
	if usage, _ := end["usage"].(map[string]any); usage["input_tokens"] != float64(5000) {
		t.Errorf("usage = %v", usage)
	}
	if !groupGone(pgid) {
		t.Error("the process group outlived the ceiling")
	}
}

// A reply carrying a token-shaped string, split across two CLI deltas,
// leaves the daemon only as the marker: no frame, and no frame joined to the
// ones before it, carries any of it.
func TestTurn_RedactsCredentialsBeforeEgress(t *testing.T) {
	hermetic(t)
	installFakeAdapters(t)
	token := "gh" + "p_" + strings.Repeat("Q7w8", 9)
	deltas, _ := json.Marshal([]string{"Your token is ", token[:15], token[15:], " — keep it safe."})
	t.Setenv("FAKE_ADAPTER_MODE", "deltas")
	t.Setenv("FAKE_ADAPTER_DELTAS", string(deltas))
	t.Setenv("FAKE_ADAPTER_DELAY_MS", "40")
	fp := newFakePlatform(t)
	startDaemon(t, fp, workspace(t, claudeWorkspace), nil)
	fp.deliver(fp.postTurn("conv-1", "turn-1", "cmd-1", "what is my token?", nil))
	waitFor(t, "the terminal report", 10*time.Second, func() bool { return fp.endFrame("turn-1") != nil })

	if got := fp.reply("turn-1"); got != "Your token is "+credshape.Marker+" — keep it safe." {
		t.Errorf("reply = %q", got)
	}
	sofar := ""
	for _, p := range fp.framePosts("turn-1") {
		raw, _ := json.Marshal(p.frames)
		for _, f := range p.frames {
			if text, ok := f["text"].(string); ok {
				sofar += text
			}
		}
		if credshape.ContainsEgress(string(raw)) || credshape.ContainsEgress(sofar) ||
			strings.Contains(string(raw), token[15:]) || strings.Contains(string(raw), token[:15]) {
			t.Fatalf("a frame carried the token: %s", raw)
		}
	}
	if end := fp.endFrame("turn-1"); end["state"] != "completed" {
		t.Errorf("terminal report = %v", end)
	}
}

// A turn for a workspace another daemon on this machine serves is left alone:
// not claimed, not refused, nothing spawned.
func TestTurn_AnotherDaemonsWorkspaceIsLeftAlone(t *testing.T) {
	hermetic(t)
	rec := installFakeAdapters(t)
	fp := newFakePlatform(t)
	asked := make(chan string, 1)
	startDaemon(t, fp, workspace(t, claudeWorkspace), func(c *Config) {
		c.Owner = func(_ context.Context, agentID, target string) (bool, error) {
			asked <- agentID + "/" + target
			return false, nil
		}
	})
	fp.deliver(fp.postTurn("conv-1", "turn-1", "cmd-1", "hello", nil))
	if got := <-asked; got != testAgent+"/ws-1" {
		t.Errorf("owner asked about %s", got)
	}
	time.Sleep(300 * time.Millisecond)
	if acks := fp.ackRecords(); len(acks) != 0 {
		t.Errorf("a turn for another workspace was acknowledged: %+v", acks)
	}
	if runs := invocations(t, rec); len(runs) != 0 {
		t.Errorf("a turn for another workspace ran: %+v", runs)
	}
}

func TestServedWorkspaceOwner(t *testing.T) {
	covered := []platform.AgentWorkspaceThrottle{
		{WorkspaceID: "ws-api", TeamID: "team", Slug: "acme-api"},
		{WorkspaceID: "ws-default", TeamID: "team", Slug: "default"},
	}
	read := func(context.Context, string) ([]platform.AgentWorkspaceThrottle, error) { return covered, nil }
	named := ServedWorkspaceOwner(read, func() (string, error) { return "acme-api", nil })
	unnamed := ServedWorkspaceOwner(read, func() (string, error) { return "", nil })
	for _, tc := range []struct {
		owner  Owner
		target string
		want   bool
	}{
		{named, "ws-api", true}, {named, "ws-default", false}, {named, "ws-unknown", false},
		{unnamed, "ws-default", true}, {unnamed, "ws-api", false},
	} {
		got, err := tc.owner(context.Background(), "agent", tc.target)
		if err != nil || got != tc.want {
			t.Errorf("owner(%s) = %v, %v; want %v", tc.target, got, err, tc.want)
		}
	}
	if _, err := named(context.Background(), "agent", ""); err == nil {
		t.Error("a turn with no target was owned")
	}
}

// A turn command no daemon can read is left unacknowledged, not refused:
// without a readable target this daemon cannot tell it is its own, and the
// first acknowledgement decides a turn.
func TestTurn_AnUnreadableCommandIsLeftAlone(t *testing.T) {
	hermetic(t)
	rec := installFakeAdapters(t)
	fp := newFakePlatform(t)
	startDaemon(t, fp, workspace(t, claudeWorkspace), nil)
	fp.deliver(commandFrame("cmd-bad", platform.ConversationTurnCommandType, map[string]any{"turn_id": "../x"}))
	fp.deliver(commandFrame("cmd-bad-2", platform.ConversationTurnCommandType, "not an object"))
	time.Sleep(300 * time.Millisecond)
	if acks := fp.ackRecords(); len(acks) != 0 {
		t.Errorf("an unreadable turn command was acknowledged: %+v", acks)
	}
	if runs := invocations(t, rec); len(runs) != 0 {
		t.Errorf("an unreadable turn command ran: %+v", runs)
	}
}

// Ids from the hosted service reach URL paths and log lines, so only
// [A-Za-z0-9_-] gets through.
func TestParseTurnCommand_RefusesIdsThatCouldChangeAPathOrALogLine(t *testing.T) {
	for _, id := range []string{"..", "a/b", "turn\nforged log line", "t\x1b[31m", strings.Repeat("a", 101), ""} {
		raw, _ := json.Marshal(map[string]any{"conversation_id": "conv-1", "turn_id": id})
		if _, err := ParseTurnCommand(raw, time.Now()); err == nil {
			t.Errorf("turn_id %q accepted", id)
		}
		raw, _ = json.Marshal(map[string]any{"conversation_id": "conv-1", "turn_id": "t1", "turn_command_id": id})
		if _, err := ParseCancelCommand(raw); err == nil {
			t.Errorf("turn_command_id %q accepted", id)
		}
	}
	got, err := ParseTurnCommand(json.RawMessage(`{"conversation_id":"c-1","turn_id":"3f2a_9C-1"}`), time.Now())
	if err != nil || got.TokenCeiling != defaultTokenCeiling || got.Limits.MaxDeltaBytes != maxDeltaBytes ||
		got.Limits.CancelConfirmSeconds != defaultCancelConfirmSeconds {
		t.Errorf("defaults = %+v, %v", got, err)
	}
}

// A second command for a turn id that is still running never displaces it:
// the running turn stays findable by cancel, shutdown and the concurrency
// bound.
func TestHost_ARunningTurnIdIsNotDisplaced(t *testing.T) {
	h := NewHost(context.Background(), Config{})
	first := newTurn(TurnCommand{TurnID: "turn-1"}, "cmd-1", "agent")
	if !h.register(first) {
		t.Fatal("the first turn was not registered")
	}
	if h.register(newTurn(TurnCommand{TurnID: "turn-1"}, "cmd-2", "agent")) {
		t.Error("a second command displaced the running turn")
	}
	if h.turns["turn-1"] != first {
		t.Error("the running turn is no longer findable")
	}
	h.finish(first)
	if !h.register(newTurn(TurnCommand{TurnID: "turn-1"}, "cmd-3", "agent")) {
		t.Error("a finished turn's id could not be reused")
	}
}

// Once a turn's process is gone, nothing holds its process-group id: a late
// stop (a cancel, the timeout, shutdown) cannot signal a group the system may
// have handed to another process.
func TestTurn_AGoneProcessIsNeverSignalled(t *testing.T) {
	tr := newTurn(TurnCommand{TurnID: "turn-1"}, "cmd-1", "agent")
	// A pgid no process has, and no command: a signal reaching it would fall
	// back to the nil command and panic.
	tr.attach(&turnProcess{pgid: 1 << 30})
	tr.markGone(true)
	tr.stop(stopTeammate)
	if tr.proc != nil || !tr.verifiedDead() {
		t.Errorf("proc = %v, verified dead = %v", tr.proc, tr.verifiedDead())
	}
}

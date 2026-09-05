// Bounded shutdown for detached board-recovery work at serve exit (#489).
//
// `nightgauge serve` is the only process that owns the autonomous scheduler's
// detached board-recovery goroutines (revert-to-Ready, move-to-Done,
// promote-unblocked). Each of those runs against an 80-minute ceiling
// (boardRecoveryTimeout) precisely so a rate-limited MoveStatus PAUSES and
// completes rather than dying at a short deadline — which means the window in
// which one is mid-flight is long, and a process exit inside that window
// abandons a board write with nothing to notice it.
//
// The seam is Server.Run's teardown, because that is what `serve` returns
// through, and this file pins both halves of it:
//
//	the exit    — the stdio loop ends on the run context, so a SIGTERM
//	              (which only cancels that context) actually reaches teardown
//	the drain   — teardown joins the scheduler's tracked work before returning
package ipc

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/depgraph"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
)

// syncBuffer is a race-free sink for the process-wide logger. A plain
// bytes.Buffer is not usable here: a goroutine leaked by some other test in
// this package keeps writing to whatever log.SetOutput points at, and that is
// exactly how main went red at d9818067 (#1443). Foreign lines landing in the
// buffer are harmless — every assertion below looks for its own line.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) contains(s string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Contains(b.buf.Bytes(), []byte(s))
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureLogTo redirects the standard logger into a race-free buffer for the
// rest of the test. Unlike captureLog it does not bracket a call, because what
// these tests observe is emitted by a goroutine they must then interrogate.
func captureLogTo(t *testing.T) *syncBuffer {
	t.Helper()
	sink := &syncBuffer{}
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(sink)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})
	return sink
}

// parkedBoardRecovery stands up a scheduler whose next board write blocks, and
// drives the production path that spawns one: `autonomous.complete` with the
// github-network-outage terminal kind reverts the issue to Ready through a
// tracked goroutine (autonomous.go's onPipelineComplete). The forge is a real
// GraphQL endpoint — an httptest server — so the parked op is parked exactly
// where a rate-limited board write parks: inside an in-flight HTTP request
// owned by the lifecycle generation context.
//
// Returns the scheduler, the server it is attached to, a channel that is closed
// once the board write is in flight, the release func that lets it complete,
// and a predicate reporting whether the write actually completed (as opposed to
// being aborted by cancellation).
func parkedBoardRecovery(t *testing.T) (as *orchestrator.AutonomousScheduler, server *Server, inFlight <-chan struct{}, release func(), completed func() bool) {
	t.Helper()

	entered := make(chan struct{})
	releaseCh := make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	var mu sync.Mutex
	wrote := false

	board := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		enterOnce.Do(func() { close(entered) })
		select {
		case <-releaseCh:
		case <-r.Context().Done():
			// Cancellation reached the in-flight board write: the mutation is
			// abandoned, which is what the grace period bounds.
			return
		}
		mu.Lock()
		wrote = true
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	t.Cleanup(board.Close)

	releaseAll := func() { releaseOnce.Do(func() { close(releaseCh) }) }
	// Belt: a t.Fatal must not leave the tracked goroutine parked in the
	// handler, or a later join would never return.
	t.Cleanup(releaseAll)

	root := t.TempDir()
	cfg := orchestrator.DefaultAutonomousConfig()
	cfg.MaxConcurrent = 1
	as = orchestrator.NewAutonomousScheduler(
		orchestrator.NewScheduler(nil, orchestrator.SchedulerConfig{WorkspaceRoot: root}),
		gh.NewClientWithURL("test-token", board.URL),
		[]depgraph.RepoConfig{{Owner: "acme", Name: "platform", Project: 7}},
		nil, cfg, root,
	)

	server = NewServer(nil, WithAutonomousScheduler(as), WithWorkspaceRoot(root))
	server.writer = io.Discard // ipc.ready would otherwise land in the test log

	complete, ok := server.methods["autonomous.complete"]
	if !ok {
		t.Fatal("autonomous.complete handler not registered")
	}
	params := json.RawMessage(`{"owner":"acme","repo":"platform","issueNumber":901,` +
		`"success":false,"terminalFailureKind":"` + orchestrator.TerminalKindGitHubNetworkOutage + `"}`)
	if _, err := complete(t.Context(), params); err != nil {
		t.Fatalf("autonomous.complete: %v", err)
	}

	select {
	case <-entered:
	case <-time.After(15 * time.Second):
		t.Fatal("the board-recovery op never reached the forge — no detached work is parked, " +
			"so nothing below can tell a drain from an abandonment")
	}

	return as, server, entered, releaseAll, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return wrote
	}
}

// pipeStdin points os.Stdin at a pipe whose write end the test controls, so the
// stdio loop ends on demand instead of inheriting whatever the test runner
// attached. Same seam as TestOrphanReconcile_ServerRunArmsTheGraceInsteadOfSweepingInline.
func pipeStdin(t *testing.T) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = original
		_ = w.Close()
		_ = r.Close()
	})
	return w
}

// TestServerRun_DrainsBackgroundOnExit is the wiring pin. It cancels the run
// context — the only thing `serve`'s SIGTERM handler does — and requires that
// Run both (a) reaches its teardown at all and (b) does not return while a
// tracked board write is still in flight.
//
// Neither half is timing-dependent: the parked op cannot finish until this test
// releases it, and the drain announcing itself in the log is the happens-before
// edge that makes the "still blocked" check below an observation rather than a
// guess.
func TestServerRun_DrainsBackgroundOnExit(t *testing.T) {
	logs := captureLogTo(t)
	as, server, _, release, boardWriteCompleted := parkedBoardRecovery(t)

	_ = pipeStdin(t) // deliberately left OPEN: cancellation is the only exit signal

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	returned := make(chan error, 1)
	go func() { returned <- server.Run(ctx) }()

	cancel()

	deadline := time.Now().Add(20 * time.Second)
	for !logs.contains("draining 1 background op(s)") {
		select {
		case err := <-returned:
			t.Fatalf("Server.Run returned (%v) without draining detached board-recovery work — "+
				"a serve exit abandons the in-flight MoveStatus (#489)\nlog:\n%s", err, logs.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("Server.Run never reached its drain: the stdio loop does not end on the run "+
				"context, so a SIGTERM never gets to teardown (#489)\nlog:\n%s", logs.String())
		}
		time.Sleep(5 * time.Millisecond)
	}

	// The drain has announced itself and the board write cannot complete until
	// the line below releases it, so a returned Run here is a drain that does
	// not actually join.
	select {
	case err := <-returned:
		t.Fatalf("Server.Run returned (%v) while the drain's own log line says work is still "+
			"in flight — the teardown does not join", err)
	default:
	}

	release()

	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("Server.Run returned an error after a clean cancellation: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("Server.Run did not return after the drained op completed\nlog:\n%s", logs.String())
	}

	if n := as.BackgroundInFlight(); n != 0 {
		t.Errorf("BackgroundInFlight() = %d after Server.Run returned, want 0 — the join is partial", n)
	}
	if !boardWriteCompleted() {
		t.Error("the board write was abandoned rather than completed — the drain cancelled " +
			"inside the grace period instead of waiting for it")
	}
	if !logs.contains("shutdown drain: drained") {
		t.Errorf("no completion line for the drain\nlog:\n%s", logs.String())
	}
}

// TestAutonomousStop_ReportsBackgroundInFlight pins the operator-visible half.
// `autonomous.stop` is a PAUSE — it deliberately neither cancels nor joins, so
// the tail of detached board work outlives it — and the only way an operator
// can see that tail is the count in the status response.
func TestAutonomousStop_ReportsBackgroundInFlight(t *testing.T) {
	_, server, _, release, _ := parkedBoardRecovery(t)

	stop, ok := server.methods["autonomous.stop"]
	if !ok {
		t.Fatal("autonomous.stop handler not registered")
	}
	status, ok := server.methods["autonomous.status"]
	if !ok {
		t.Fatal("autonomous.status handler not registered")
	}

	inFlightFrom := func(t *testing.T, res interface{}) int {
		t.Helper()
		// Through JSON, because that is what the TypeScript client reads.
		encoded, err := json.Marshal(res)
		if err != nil {
			t.Fatalf("marshal status result: %v", err)
		}
		var decoded struct {
			BackgroundInFlight int `json:"backgroundInFlight"`
		}
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatalf("unmarshal status result: %v", err)
		}
		return decoded.BackgroundInFlight
	}

	res, err := stop(t.Context(), nil)
	if err != nil {
		t.Fatalf("autonomous.stop: %v", err)
	}
	if got := inFlightFrom(t, res); got != 1 {
		t.Errorf("autonomous.stop reported backgroundInFlight = %d, want 1 — the operator cannot "+
			"see the detached board work the pause left running (#489)", got)
	}

	release()

	deadline := time.Now().Add(20 * time.Second)
	for {
		res, err := status(t.Context(), nil)
		if err != nil {
			t.Fatalf("autonomous.status: %v", err)
		}
		got := inFlightFrom(t, res)
		if got == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("autonomous.status still reports backgroundInFlight = %d after the op "+
				"completed — the count never falls", got)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

package boardcache

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/forge"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

// countingBoard records how many times each read actually reached "the API".
// The point of every test here is a CALL COUNT, not a returned value: a cache
// that returns the right answer while still issuing the request has saved
// nothing, and that is precisely the failure #845 is about.
type countingBoard struct {
	mu        sync.Mutex
	openCalls int
	itemCalls int
	items     []forgetypes.BoardItem
	total     int
	err       error
	block     chan struct{} // when non-nil, ListOpenItems waits on it
}

func (b *countingBoard) ListOpenItems(context.Context) ([]forgetypes.BoardItem, int, error) {
	b.mu.Lock()
	b.openCalls++
	block := b.block
	items, total, err := b.items, b.total, b.err
	b.mu.Unlock()
	if block != nil {
		<-block
	}
	return items, total, err
}

func (b *countingBoard) ListItems(context.Context, string) ([]forgetypes.BoardItem, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.itemCalls++
	return b.items, b.err
}

func (b *countingBoard) GetItem(context.Context, string, string, int) (*forgetypes.BoardItem, error) {
	return nil, nil
}

func (b *countingBoard) calls() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.openCalls
}

func sampleItems() []forgetypes.BoardItem {
	return []forgetypes.BoardItem{{Repo: "acme/web"}, {Repo: "acme/api"}}
}

// AC1: within one sweep a board is read at most once regardless of how many
// producers consume it. This is the 74%-of-the-bill case, in miniature.
func TestSecondReaderInAWindowCostsNothing(t *testing.T) {
	inner := &countingBoard{items: sampleItems(), total: 2}
	board := New(0).Wrap(inner, "acme", 3)

	for i := 0; i < 5; i++ {
		items, total, err := board.ListOpenItems(context.Background())
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		if len(items) != 2 || total != 2 {
			t.Fatalf("read %d returned %d items / total %d, want 2/2", i, len(items), total)
		}
	}
	if got := inner.calls(); got != 1 {
		t.Errorf("issued %d board reads for 5 consumers, want 1 — at 17 points each that is %d wasted points", got, (got-1)*17)
	}
}

// Producers can run concurrently. If a cold cache lets each of them start its
// own fetch, the duplicate read survives exactly where it costs most.
func TestConcurrentReadersCollapseToOneFetch(t *testing.T) {
	release := make(chan struct{})
	inner := &countingBoard{items: sampleItems(), total: 2, block: release}
	board := New(0).Wrap(inner, "acme", 3)

	const readers = 8
	var wg sync.WaitGroup
	errs := make([]error, readers)
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, errs[i] = board.ListOpenItems(context.Background())
		}(i)
	}
	// Let every goroutine reach the cache before the single fetch completes.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("reader %d: %v", i, err)
		}
	}
	if got := inner.calls(); got != 1 {
		t.Errorf("%d concurrent readers issued %d fetches, want 1", readers, got)
	}
}

// AC3: the cache is honest about staleness — and a snapshot past its TTL is
// refetched rather than served.
func TestSnapshotExpiresAndReportsItsAge(t *testing.T) {
	inner := &countingBoard{items: sampleItems(), total: 2}
	c := New(60 * time.Second)
	now := time.Unix(1_700_000_000, 0)
	c.SetClock(func() time.Time { return now })
	board := c.Wrap(inner, "acme", 3)

	if _, _, err := board.ListOpenItems(context.Background()); err != nil {
		t.Fatalf("first read: %v", err)
	}
	snap, ok := c.Peek("acme", 3, "open")
	if !ok {
		t.Fatal("Peek found no snapshot after a successful read")
	}
	if age := snap.Age(now); age != 0 {
		t.Errorf("fresh snapshot reports age %v, want 0", age)
	}

	now = now.Add(30 * time.Second)
	if _, _, err := board.ListOpenItems(context.Background()); err != nil {
		t.Fatalf("read inside TTL: %v", err)
	}
	if got := inner.calls(); got != 1 {
		t.Errorf("a read inside the TTL issued a fetch (%d total), want 1", got)
	}
	if snap, _ := c.Peek("acme", 3, "open"); snap.Age(now) != 30*time.Second {
		t.Errorf("snapshot age = %v, want 30s — a cache that cannot report its own age cannot be audited", snap.Age(now))
	}

	now = now.Add(31 * time.Second) // 61s total: past the TTL
	if _, _, err := board.ListOpenItems(context.Background()); err != nil {
		t.Fatalf("read past TTL: %v", err)
	}
	if got := inner.calls(); got != 2 {
		t.Errorf("a read past the TTL issued %d fetches total, want 2", got)
	}
	if _, ok := c.Peek("acme", 3, "open"); !ok {
		t.Error("Peek lost the snapshot after a refetch")
	}
}

// A failed read must never be cached. "I could not look" memoized into "there
// is nothing there" is the Invariant-1 failure the sweep producers are written
// against, and a cache is a very efficient way to commit it.
func TestFailedReadIsNotCached(t *testing.T) {
	inner := &countingBoard{err: errors.New("rate limited")}
	board := New(0).Wrap(inner, "acme", 3)

	for i := 0; i < 3; i++ {
		if _, _, err := board.ListOpenItems(context.Background()); err == nil {
			t.Fatalf("read %d: want the inner error to surface", i)
		}
	}
	if got := inner.calls(); got != 3 {
		t.Errorf("issued %d fetches across 3 failing reads, want 3 — an error must not be memoized", got)
	}
	if _, ok := c0Peek(board); ok {
		t.Error("a failed read left a snapshot behind")
	}
}

// c0Peek reaches the cache behind a wrapped board for the error test.
func c0Peek(b forge.BoardService) (Snapshot, bool) {
	cb, ok := b.(*cachedBoard)
	if !ok {
		return Snapshot{}, false
	}
	return cb.cache.Peek("acme", 3, "open")
}

// Different filters are different questions and must not share an answer.
func TestDistinctQueriesDoNotShareAnEntry(t *testing.T) {
	inner := &countingBoard{items: sampleItems()}
	board := New(0).Wrap(inner, "acme", 3)

	if _, err := board.ListItems(context.Background(), "Ready"); err != nil {
		t.Fatal(err)
	}
	if _, err := board.ListItems(context.Background(), "Done"); err != nil {
		t.Fatal(err)
	}
	if _, err := board.ListItems(context.Background(), "Ready"); err != nil {
		t.Fatal(err)
	}
	if inner.itemCalls != 2 {
		t.Errorf("ListItems issued %d fetches for 2 distinct filters + 1 repeat, want 2", inner.itemCalls)
	}
}

// Two boards are two caches' worth of state; one must not answer for the other.
// This is the #844 failure mode one layer up — a shared object silently
// answering a question about the wrong board.
func TestBoardsAreKeyedSeparately(t *testing.T) {
	c := New(0)
	a := &countingBoard{items: sampleItems()}
	b := &countingBoard{items: sampleItems()}
	ca, cb := c.Wrap(a, "acme", 3), c.Wrap(b, "acme", 4)

	if _, _, err := ca.ListOpenItems(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := cb.ListOpenItems(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a.calls() != 1 || b.calls() != 1 {
		t.Errorf("board 3 read %d times, board 4 read %d times; want 1 each", a.calls(), b.calls())
	}
}

func TestInvalidateDropsOnlyTheNamedBoard(t *testing.T) {
	c := New(0)
	a := &countingBoard{items: sampleItems()}
	b := &countingBoard{items: sampleItems()}
	ca, cb := c.Wrap(a, "acme", 3), c.Wrap(b, "acme", 4)

	ctx := context.Background()
	_, _, _ = ca.ListOpenItems(ctx)
	_, _, _ = cb.ListOpenItems(ctx)
	c.Invalidate("acme", 3)
	_, _, _ = ca.ListOpenItems(ctx)
	_, _, _ = cb.ListOpenItems(ctx)

	if a.calls() != 2 {
		t.Errorf("invalidated board was read %d times, want 2", a.calls())
	}
	if b.calls() != 1 {
		t.Errorf("untouched board was read %d times, want 1 — invalidation is scoped to one board", b.calls())
	}
}

// Owner casing must not create a second entry for one board.
func TestBoardKeyIsCaseInsensitiveOnOwner(t *testing.T) {
	c := New(0)
	inner := &countingBoard{items: sampleItems()}
	lower := c.Wrap(inner, "acme", 3)
	upper := c.Wrap(inner, "ACME", 3)

	_, _, _ = lower.ListOpenItems(context.Background())
	_, _, _ = upper.ListOpenItems(context.Background())
	if inner.calls() != 1 {
		t.Errorf("owner casing split one board into %d reads, want 1", inner.calls())
	}
}

// readOnlyProjectMethods are the ProjectService methods that cannot change what
// a board read returns, so they are deliberately NOT intercepted.
var readOnlyProjectMethods = map[string]bool{
	"DriftCheck":     true, // reports drift; DriftFix is the one that writes
	"SnapshotFields": true, // reads the field schema
}

// The embedding in invalidatingProject is a convenience that would otherwise
// rot silently: add a mutating method to ProjectService and it passes straight
// through, leaving a stale snapshot with nothing to notice.
//
// This is deliberately BEHAVIOURAL rather than a reflection check on shape. The
// first version of this test compared the declaring type of each method and
// passed happily when a real override was deleted — a promoted method's
// receiver is the outer type too, so there was nothing to see. It asserted a
// property that is always true (§ 2.0d).
//
// What does discriminate: `defer p.invalidate()` runs even when the inner call
// panics. The fake's embedded ProjectService is nil, so a PROMOTED method
// panics without invalidating, while an OVERRIDDEN one invalidates on the way
// out. Seed the cache, call every interface method by reflection, and see which
// ones left the snapshot behind.
func TestEveryMutatingProjectMethodIsIntercepted(t *testing.T) {
	iface := reflect.TypeOf((*forge.ProjectService)(nil)).Elem()
	if iface.NumMethod() == 0 {
		t.Fatal("reflected zero methods off ProjectService — the guard would pass vacuously")
	}

	var missing []string
	for i := 0; i < iface.NumMethod(); i++ {
		name := iface.Method(i).Name
		if readOnlyProjectMethods[name] {
			continue
		}
		if !methodInvalidates(t, name) {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Errorf("these ProjectService methods reach the board without invalidating the cache: %v\n"+
			"Override each in invalidatingProject, or add it to readOnlyProjectMethods with a reason.", missing)
	}
}

// methodInvalidates calls one ProjectService method through the wrapper and
// reports whether the board's snapshot was dropped.
func methodInvalidates(t *testing.T, name string) bool {
	t.Helper()
	c := New(time.Hour)
	inner := &countingBoard{items: sampleItems()}
	client := WrapClient(c, &fakeClient{board: inner, project: &fakeProject{}}, "acme", 3)
	if _, _, err := client.Board().ListOpenItems(context.Background()); err != nil {
		t.Fatalf("%s: seeding the cache failed: %v", name, err)
	}
	if _, ok := c.Peek("acme", 3, "open"); !ok {
		t.Fatalf("%s: the cache was not seeded, so this proves nothing", name)
	}

	m := reflect.ValueOf(client.Project()).MethodByName(name)
	if !m.IsValid() {
		t.Fatalf("%s is on ProjectService but not reachable on the wrapper", name)
	}
	args := make([]reflect.Value, m.Type().NumIn())
	for i := range args {
		in := m.Type().In(i)
		if in == reflect.TypeOf((*context.Context)(nil)).Elem() {
			args[i] = reflect.ValueOf(context.Background())
			continue
		}
		args[i] = reflect.New(in).Elem()
	}
	func() {
		// A promoted method hits the fake's nil embedded interface and panics.
		// That IS the signal: it means nothing of ours ran around the call.
		defer func() { _ = recover() }()
		m.Call(args)
	}()

	_, stillCached := c.Peek("acme", 3, "open")
	return !stillCached
}

// AC2: a mutation this process issues invalidates the snapshot immediately.
func TestMutationInvalidatesTheSnapshot(t *testing.T) {
	c := New(time.Hour) // long TTL: only the mutation can explain a refetch
	inner := &countingBoard{items: sampleItems(), total: 2}
	project := &fakeProject{}
	client := WrapClient(c, &fakeClient{board: inner, project: project}, "acme", 3)

	ctx := context.Background()
	if _, _, err := client.Board().ListOpenItems(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.Board().ListOpenItems(ctx); err != nil {
		t.Fatal(err)
	}
	if inner.calls() != 1 {
		t.Fatalf("setup: want 1 read before the mutation, got %d", inner.calls())
	}

	if err := client.Project().SyncStatus(ctx, "acme", "web", 7, "Done"); err != nil {
		t.Fatalf("SyncStatus: %v", err)
	}
	if _, ok := c.Peek("acme", 3, "open"); ok {
		t.Error("the snapshot survived a mutation this process issued")
	}
	if _, _, err := client.Board().ListOpenItems(ctx); err != nil {
		t.Fatal(err)
	}
	if inner.calls() != 2 {
		t.Errorf("post-mutation read issued %d fetches total, want 2 — the next reader must get fresh data", inner.calls())
	}
}

// A mutation that FAILS is when the board's true state is least certain: the
// write may have landed before the error surfaced.
func TestFailedMutationAlsoInvalidates(t *testing.T) {
	c := New(time.Hour)
	inner := &countingBoard{items: sampleItems()}
	client := WrapClient(c, &fakeClient{board: inner, project: &fakeProject{err: errors.New("boom")}}, "acme", 3)

	ctx := context.Background()
	_, _, _ = client.Board().ListOpenItems(ctx)
	if err := client.Project().SyncStatus(ctx, "acme", "web", 7, "Done"); err == nil {
		t.Fatal("want the inner error")
	}
	if _, ok := c.Peek("acme", 3, "open"); ok {
		t.Error("a snapshot survived a FAILED mutation — the write may still have landed")
	}
}

// A nil Project() must stay nil through the wrapper: producers branch on it.
func TestNilProjectStaysNil(t *testing.T) {
	client := WrapClient(New(0), &fakeClient{board: &countingBoard{}}, "acme", 3)
	if client.Project() != nil {
		t.Error("wrapping invented a ProjectService where the adapter had none")
	}
}

// --- fakes -------------------------------------------------------------

type fakeClient struct {
	forge.ForgeClient
	board   forge.BoardService
	project forge.ProjectService
}

func (c *fakeClient) Board() forge.BoardService { return c.board }
func (c *fakeClient) Project() forge.ProjectService {
	if c.project == nil {
		return nil
	}
	return c.project
}

// fakeProject implements ProjectService with only the methods these tests call;
// the rest panic, which is louder than returning a zero value if one is reached.
type fakeProject struct {
	forge.ProjectService
	err   error
	syncs int
}

func (p *fakeProject) SyncStatus(context.Context, string, string, int, string) error {
	p.syncs++
	return p.err
}

var _ = fmt.Sprintf

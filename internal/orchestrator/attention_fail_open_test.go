package orchestrator

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
)

type verbExecutorFunc func(ctx context.Context) error

func (f verbExecutorFunc) ExecuteVerb(ctx context.Context, _ *attention.DecisionRequest, _ attention.Option) error {
	return f(ctx)
}

// TestScanEntryPointsFailOpenBehindABusyStore pins AC3 at the scheduler's
// seam (#2750): with Resolve holding the store in a verb that does not finish,
// each background entry point logs and returns, so the scan carries on.
func TestScanEntryPointsFailOpenBehindABusyStore(t *testing.T) {
	buf := withCapturedLog(t)
	store := attention.New(layouttest.Repo(t))
	store.SetProducerLockWait(50 * time.Millisecond)
	as := &AutonomousScheduler{attention: store, state: &AutonomousState{}}

	id, err := attention.NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	if _, _, err := store.Raise(attention.DecisionRequest{
		ID: id, IdempotencyKey: "fail-open:holder", Kind: attention.KindChoose,
		Severity: attention.SeverityFYI, Title: "holder", Producer: "test",
		Options:       []attention.Option{{ID: "go", Label: "Go", Verb: attention.VerbNoop}},
		DefaultAction: attention.ExpireNoop,
	}); err != nil {
		t.Fatalf("Raise: %v", err)
	}

	inVerb, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	resolved := make(chan error, 1)
	go func() {
		_, err := store.Resolve(context.Background(), id, "go", "tester", "", "", verbExecutorFunc(func(context.Context) error {
			close(inVerb)
			<-release
			return nil
		}))
		resolved <- err
	}()
	<-inVerb

	calls := []struct {
		name string
		call func()
	}{
		{"raiseThrough", func() { as.raiseWorkExhaustion(1) }},
		{"autoResolveAttention", func() { as.retractWorkExhaustion() }},
		{"AutoResolveKey", as.resolveSafetyRailTrip},
	}
	for _, c := range calls {
		t.Run(c.name, func(t *testing.T) {
			before := buf.String()
			done := make(chan struct{})
			go func() { c.call(); close(done) }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("the entry point never returned — the scan would park here")
			}
			if got := strings.TrimPrefix(buf.String(), before); !strings.Contains(got, "(fail-open)") || !strings.Contains(got, "store is busy") {
				t.Errorf("log lacks a fail-open busy-store warning: %q", got)
			}
		})
	}
}

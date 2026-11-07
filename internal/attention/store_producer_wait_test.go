package attention

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
)

// TestProducerWaitOutlastsAVerb keeps a healthy Resolve from costing a producer
// its raise: the wait has to outlast the longest legitimate hold (#2750).
func TestProducerWaitOutlastsAVerb(t *testing.T) {
	if defaultProducerLockWait <= verbTimeout {
		t.Fatalf("defaultProducerLockWait %s must exceed verbTimeout %s", defaultProducerLockWait, verbTimeout)
	}
}

// TestProducersGiveUpOnAWedgedStore is the background half of #1539's guarantee:
// no producer parks forever, because the scan loop is the goroutine that would
// park (#2750).
func TestProducersGiveUpOnAWedgedStore(t *testing.T) {
	s := New(layouttest.Repo(t))
	validID := raiseTestCard(t, s, "wedged:producer")
	s.SetProducerLockWait(50 * time.Millisecond)

	release, err := acquireDirCtx(context.Background(), s.dir)
	if err != nil {
		t.Fatalf("acquireDirCtx: %v", err)
	}
	defer release()

	cases := []struct {
		name string
		call func() error
	}{
		{"Raise", func() error {
			_, _, err := s.Raise(DecisionRequest{
				ID: validID, IdempotencyKey: "wedged:raise", Kind: KindChoose, Severity: SeverityFYI,
				Title: "raise", Producer: "test", DefaultAction: ExpireNoop,
			})
			return err
		}},
		{"SweepExpired", func() error {
			_, err := s.SweepExpired(context.Background(), NoopExecutor{})
			return err
		}},
		{"ReconcileStanding", func() error {
			_, err := s.ReconcileStanding(StandingSweep{Repo: "octocat/acme", Producers: []string{"test"}})
			return err
		}},
		{"AutoResolveUnobserved", func() error {
			_, err := s.AutoResolveUnobserved("test", nil)
			return err
		}},
		{"AutoResolveKey", func() error {
			_, err := s.AutoResolveKey("test", "wedged:producer")
			return err
		}},
		{"IncrementStreak", func() error {
			_, err := s.IncrementStreak("wedged:streak")
			return err
		}},
		{"ResetStreak", func() error { return s.ResetStreak("wedged:streak") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			done := make(chan error, 1)
			go func() { done <- tc.call() }()
			select {
			case err := <-done:
				if !errors.Is(err, ErrStoreBusy) {
					t.Fatalf("err = %v, want ErrStoreBusy", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the producer never returned — this is the #2750 scan park")
			}
		})
	}

	t.Run("StreakCount", func(t *testing.T) {
		done := make(chan int, 1)
		go func() { done <- s.StreakCount("wedged:streak") }()
		select {
		case n := <-done:
			if n != 0 {
				t.Fatalf("StreakCount = %d, want 0 while busy", n)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("StreakCount never returned — this is the #2750 scan park")
		}
	})
}

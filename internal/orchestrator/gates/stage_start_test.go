package gates

import (
	"context"
	"testing"
	"time"
)

func TestStageStart_RoundTrip(t *testing.T) {
	if _, ok := StageStart(context.Background()); ok {
		t.Fatal("StageStart on a bare context reported a start")
	}
	if _, ok := StageStart(WithStageStart(context.Background(), time.Time{})); ok {
		t.Fatal("a zero start must read as absent")
	}
	want := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	got, ok := StageStart(WithStageStart(context.Background(), want))
	if !ok || !got.Equal(want) {
		t.Fatalf("StageStart = %v, %v; want %v, true", got, ok, want)
	}
}

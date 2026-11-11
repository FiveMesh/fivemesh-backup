package recovery

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestContextSurvivesCancelledParentAndExpires(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	ctx, stop := Context(parent)
	defer stop()
	if err := ctx.Err(); err != nil {
		t.Fatalf("recovery context inherited parent cancellation: %v", err)
	}
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > DefaultTimeout {
		t.Fatal("recovery context does not have a strict deadline")
	}
}

func TestContextPreservesValues(t *testing.T) {
	type key struct{}
	parent := context.WithValue(context.Background(), key{}, "value")
	ctx, stop := Context(parent)
	defer stop()
	if got := ctx.Value(key{}); got != "value" {
		t.Fatalf("context value = %v", got)
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("fresh recovery context is cancelled")
	}
}

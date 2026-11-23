package service

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRunCommandOutputCap(t *testing.T) {
	_, _, err := RunCommand(context.Background(), "sh", "-c", "head -c 70000000 /dev/zero")
	if !errors.Is(err, ErrOutputTooLarge) {
		t.Fatalf("want ErrOutputTooLarge, got %v", err)
	}
}

func TestRunCommandKillsChildren(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	// The background sleep holds stdout open; without the group kill Run() waits 60s.
	_, _, _ = RunCommand(ctx, "sh", "-c", "sleep 60 & sleep 60")
	if d := time.Since(start); d > 7*time.Second {
		t.Fatalf("RunCommand took %s after cancel", d)
	}
}

func TestRunCommandWaitsForSlot(t *testing.T) {
	// Fill every slot, then a call with a short deadline must give up waiting.
	for i := 0; i < cap(slots); i++ {
		slots <- struct{}{}
	}
	defer func() {
		for i := 0; i < cap(slots); i++ {
			<-slots
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, _, err := RunCommand(ctx, "true"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want DeadlineExceeded, got %v", err)
	}
}

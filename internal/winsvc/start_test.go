package winsvc

import (
	"context"
	"testing"
	"time"
)

func nolog(string, ...any) {}

// The service must not start its work (open ports) before its rights are
// lowered, and not at all in a process that is about to be restarted.
func TestStartOrder(t *testing.T) {
	var order []string
	lower := func(restarting bool) func() bool {
		return func() bool { order = append(order, "lower"); return restarting }
	}
	run := func(context.Context) error { order = append(order, "run"); return nil }

	// Rights stay as they are (already limited, or the switch failed).
	if err := Start(context.Background(), lower(false), run, nolog); err != nil {
		t.Fatal(err)
	}
	if len(order) != 2 || order[0] != "lower" || order[1] != "run" {
		t.Fatalf("order: %v", order)
	}

	// Switched: the process waits for the restart and never runs.
	order = nil
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Start(ctx, lower(true), run, nolog) }()
	time.Sleep(100 * time.Millisecond)
	cancel() // the service manager stops the service
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(order) != 1 || order[0] != "lower" {
		t.Fatalf("the service ran in the process that was to be restarted: %v", order)
	}

	// The restart never came: the service runs after all.
	order = nil
	old := RestartWait
	RestartWait = 50 * time.Millisecond
	defer func() { RestartWait = old }()
	if err := Start(context.Background(), lower(true), run, nolog); err != nil {
		t.Fatal(err)
	}
	if len(order) != 2 || order[1] != "run" {
		t.Fatalf("no run after a lost restart: %v", order)
	}
}

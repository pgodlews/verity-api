package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Closing the window cancels in-flight Git work (and runs, through the same
// Session.Cancel used by CancelRun).
func TestShutdownCancelsInFlightGit(t *testing.T) {
	app := NewApp()
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- app.git.Run(context.Background(), func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	<-started
	app.shutdown(context.Background())
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("git op ended with %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not cancel the in-flight Git operation")
	}
}

func TestVersionBinding(t *testing.T) {
	v := (&App{}).Version()
	if v.Version == "" || v.Go == "" || v.OS == "" {
		t.Fatalf("version = %+v", v)
	}
}

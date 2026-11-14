package backup

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestWriteFenceDrainsAndResumes verifies the checkpoint waits for admitted
// activity and holds off new activity until the snapshot has been secured.
func TestWriteFenceDrainsAndResumes(t *testing.T) {
	var fence WriteFence
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done, err := fence.Enter(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	acquired := make(chan func(), 1)
	errs := make(chan error, 1)
	go func() {
		release, err := fence.Pause(ctx)
		if err != nil {
			errs <- err
			return
		}
		acquired <- release
	}()
	select {
	case release := <-acquired:
		release()
		t.Fatal("checkpoint acquired before active writes drained")
	case <-time.After(20 * time.Millisecond):
	}
	done()

	var resume func()
	select {
	case resume = <-acquired:
		defer resume()
	case err := <-errs:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	wait, stop := context.WithTimeout(ctx, 20*time.Millisecond)
	defer stop()
	if leave, err := fence.Enter(wait); !errors.Is(err, context.DeadlineExceeded) {
		if leave != nil {
			leave()
		}
		t.Fatalf("Enter while checkpoint is held = %v; want deadline", err)
	}
	resume()
	leave, err := fence.Enter(ctx)
	if err != nil {
		t.Fatalf("Enter after checkpoint = %v; want success", err)
	}
	leave()
}

// TestWriteFenceCancelledPause verifies timed-out checkpoint acquisition
// leaves existing and later requests free to finish.
func TestWriteFenceCancelledPause(t *testing.T) {
	var fence WriteFence
	release, err := fence.Enter(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if resume, err := fence.Pause(ctx); !errors.Is(err, context.DeadlineExceeded) {
		if resume != nil {
			resume()
		}
		t.Fatalf("Pause while a request is active = %v; want deadline", err)
	}
	leave, err := fence.Enter(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	leave()
}

package backup

import (
	"context"
	"sync"
)

// WriteFence drains ongoing controller writes before admitting a checkpoint.
// Its zero value starts open.
type WriteFence struct {
	mu      sync.Mutex
	active  int
	resumed chan struct{}
	drained chan struct{}
}

// Enter waits for any checkpoint to finish and admits one write activity.
func (f *WriteFence) Enter(ctx context.Context) (func(), error) {
	for {
		f.mu.Lock()
		if err := ctx.Err(); err != nil {
			f.mu.Unlock()
			return nil, err
		}
		resumed := f.resumed
		if resumed == nil {
			f.active++
			f.mu.Unlock()
			return sync.OnceFunc(f.leave), nil
		}
		f.mu.Unlock()
		select {
		case <-resumed:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (f *WriteFence) leave() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.active--
	if f.active == 0 && f.drained != nil {
		close(f.drained)
	}
}

// Pause closes admission and waits for current activity to finish. A cancelled
// pause restores admission before returning.
func (f *WriteFence) Pause(ctx context.Context) (func(), error) {
	for {
		f.mu.Lock()
		if err := ctx.Err(); err != nil {
			f.mu.Unlock()
			return nil, err
		}
		resumed := f.resumed
		if resumed == nil {
			f.resumed = make(chan struct{})
			drained := make(chan struct{})
			f.drained = drained
			if f.active == 0 {
				close(drained)
			}
			f.mu.Unlock()

			resume := sync.OnceFunc(f.resume)
			select {
			case <-drained:
				if err := ctx.Err(); err != nil {
					resume()
					return nil, err
				}
				return resume, nil
			case <-ctx.Done():
				resume()
				return nil, ctx.Err()
			}
		}
		f.mu.Unlock()
		select {
		case <-resumed:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (f *WriteFence) resume() {
	f.mu.Lock()
	defer f.mu.Unlock()
	close(f.resumed)
	f.resumed, f.drained = nil, nil
}

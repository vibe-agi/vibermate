// Package evidencearchive coordinates Capture creation with destructive
// whole-archive operations.
package evidencearchive

import (
	"context"
	"errors"
	"math"
	"sync"

	"golang.org/x/sync/semaphore"
)

const allCaptureCreationSlots = int64(math.MaxInt64)

var ErrBarrierUnavailable = errors.New("evidence archive barrier is unavailable")

// Release ends one barrier lease. A Release returned by Barrier is safe to
// call more than once, which keeps deferred cleanup safe on error paths.
type Release func()

// CaptureCreationBarrier is the narrow side used by Capture managers. Every
// kind of Capture enters the same shared boundary before it becomes durable.
type CaptureCreationBarrier interface {
	BeginCaptureCreation(context.Context) (Release, error)
}

// ClearBarrier is the destructive side used by the archive control action.
// Holding it excludes all Capture creation until the holder check and archive
// transaction have both completed.
type ClearBarrier interface {
	BeginClear(context.Context) (Release, error)
}

// Barrier admits concurrent Capture creations and one exclusive archive clear.
// A queued clear also prevents later Capture creations from jumping ahead of
// it, because semaphore.Weighted serves queued acquisitions in arrival order.
type Barrier struct {
	slots *semaphore.Weighted
}

func NewBarrier() *Barrier {
	return &Barrier{slots: semaphore.NewWeighted(allCaptureCreationSlots)}
}

func (barrier *Barrier) BeginCaptureCreation(ctx context.Context) (Release, error) {
	return barrier.acquire(ctx, 1)
}

func (barrier *Barrier) BeginClear(ctx context.Context) (Release, error) {
	return barrier.acquire(ctx, allCaptureCreationSlots)
}

func (barrier *Barrier) acquire(ctx context.Context, slots int64) (Release, error) {
	if barrier == nil || barrier.slots == nil || ctx == nil {
		return nil, ErrBarrierUnavailable
	}
	if err := barrier.slots.Acquire(ctx, slots); err != nil {
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() { barrier.slots.Release(slots) })
	}, nil
}

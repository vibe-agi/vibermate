package nativemcp

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/nativetool"
)

// The returned open snapshot is genuine. The actual lifecycle actor commits
// only after that read, so Finish must resolve its real conflict conservatively.
type unsentHistoryReadRace struct {
	nativetool.Repository
	target    nativetool.ID
	armed     atomic.Bool
	reads     atomic.Int32
	finishes  atomic.Int32
	once      sync.Once
	afterOpen func()
}

func (r *unsentHistoryReadRace) Finish(ctx context.Context, id nativetool.ID, in nativetool.TerminalInput) error {
	if id == r.target {
		r.finishes.Add(1)
	}
	return r.Repository.Finish(ctx, id, in)
}
func (r *unsentHistoryReadRace) FinishAttempt(ctx context.Context, receipt nativetool.StartReceipt, in nativetool.PhysicalFinishInput) error {
	if receipt.OperationID() == r.target {
		r.finishes.Add(1)
	}
	return r.Repository.FinishAttempt(ctx, receipt, in)
}

func (r *unsentHistoryReadRace) VerifyStartReceipt(ctx context.Context, receipt nativetool.StartReceipt, now time.Time) error {
	err := r.Repository.VerifyStartReceipt(ctx, receipt, now)
	if receipt.OperationID() == r.target && err != nil {
		r.armed.Store(true)
	}
	return err
}
func (r *unsentHistoryReadRace) Get(ctx context.Context, id nativetool.ID) (nativetool.Operation, error) {
	op, err := r.Repository.Get(ctx, id)
	if id == r.target && r.armed.Load() && err == nil {
		r.reads.Add(1)
		if op.State == "started" && op.CompletedAt == nil {
			r.once.Do(r.afterOpen)
		}
	}
	return op, err
}

func TestUnsentReadyKindsPreserveClosureWinningFinishRace(t *testing.T) {
	for _, kind := range []string{"ping", "list", "delete", "cancel", "recovery"} {
		t.Run(kind, func(t *testing.T) { testReadyUnsentLifecycle(t, kind, true, true) })
	}
}

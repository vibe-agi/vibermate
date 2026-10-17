package productruntime

import (
	"context"
	"fmt"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/recording"
	"sync"
	"time"
)

// runtimeEgressRepository keeps inspection and optional recording separate.
// Native reliable transactions use their repository directly.
type runtimeEgressRepository struct {
	delegate                egressaudit.Repository
	recorder                *observationRecorder
	status                  *statusTracker
	owner                   context.Context
	completionTimeout       time.Duration
	mu                      sync.Mutex
	shutdownContext         context.Context
	rawEvidenceFailureErr   error
	rawEvidenceFailureCount uint64
}

func newRuntimeEgressRepository(delegate egressaudit.Repository, status *statusTracker, owner context.Context, lifecycle LifecycleOptions, _ context.CancelCauseFunc) *runtimeEgressRepository {
	timeout := lifecycle.RollbackTimeout
	if lifecycle.ShutdownTimeout < timeout {
		timeout = lifecycle.ShutdownTimeout
	}
	return &runtimeEgressRepository{delegate: delegate, status: status, owner: owner, completionTimeout: timeout}
}
func (r *runtimeEgressRepository) ObserveEgress(s recording.Stamp, a egressaudit.Attempt) recording.OfferResult {
	if r.recorder == nil {
		return recording.DroppedUnavailable
	}
	return r.recorder.ObserveEgress(s, a)
}
func (r *runtimeEgressRepository) ScopeStamp(c captureidentity.Reference) recording.Stamp {
	if r.recorder == nil {
		return recording.Stamp{}
	}
	return r.recorder.ScopeStamp(c)
}
func (r *runtimeEgressRepository) ReportTerminalFailure(err error) {
	if err != nil && r.recorder != nil {
		r.recorder.budget.RecordOffer(recording.Egress, recording.DroppedInvalid, 0)
	}
}
func (r *runtimeEgressRepository) ReportRawEvidenceFailure(err error) {
	if err == nil {
		return
	}
	r.mu.Lock()
	r.rawEvidenceFailureErr = fmt.Errorf("record Raw evidence: %w", err)
	r.rawEvidenceFailureCount++
	r.mu.Unlock()
	if r.status != nil {
		r.status.failRecording("raw_evidence", err)
	}
}
func (r *runtimeEgressRepository) List(ctx context.Context, q egressaudit.PageRequest) (egressaudit.Page, error) {
	return r.delegate.List(ctx, q)
}
func (r *runtimeEgressRepository) failure() error { return nil }
func (r *runtimeEgressRepository) beginShutdown(ctx context.Context) {
	r.mu.Lock()
	r.shutdownContext = ctx
	r.mu.Unlock()
}
func (r *runtimeEgressRepository) finishShutdown() {}

// This context bounds live UDP terminal bookkeeping; it owns no persistence.
func (r *runtimeEgressRepository) completionContext() (context.Context, context.CancelFunc) {
	r.mu.Lock()
	parent := r.owner
	if r.shutdownContext != nil {
		parent = r.shutdownContext
	}
	r.mu.Unlock()
	return context.WithTimeout(parent, r.completionTimeout)
}

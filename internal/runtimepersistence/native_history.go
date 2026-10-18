package runtimepersistence

import (
	"context"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/recording"
	"time"
)

// One outer control transaction owns a fixed number of optional preparations.
// Helpers without this explicit context are intentionally recording-disabled:
// external mutation/close owners cannot publish before their own commit.
// Capture-run/manual-capture, assignment, environment, proxy-client, runtime-user,
// deletion and Store shutdown transactions deliberately pass no holder. Recovery
// through nativeToolRepository.transaction uses the same bounded holder when a
// recorder/issuer is available; startup without one simply drops projections.
type nativeHistoryKey struct{}
type nativeHistoryBatch struct {
	recorder nativetool.HistoryRecorder
	scopes   recording.ScopeIssuer
	tickets  [32]nativetool.PreparedHistory
	count    int
}

func (b *nativeHistoryBatch) discard(reason nativetool.HistoryDiscardReason) {
	if b == nil {
		return
	}
	for i := 0; i < b.count; i++ {
		b.tickets[i].Discard(reason)
		b.tickets[i] = nil
	}
	b.count = 0
}
func (b *nativeHistoryBatch) publish(ctx context.Context) {
	if b == nil {
		return
	}
	if ctx.Err() != nil {
		b.discard(nativetool.HistoryOwnerCanceled)
		return
	}
	for i := 0; i < b.count; i++ {
		b.tickets[i].Publish()
		b.tickets[i] = nil
	}
	b.count = 0
}
func stageNativeHistory(ctx context.Context, op nativetool.Operation, event string, at time.Time) {
	b, _ := ctx.Value(nativeHistoryKey{}).(*nativeHistoryBatch)
	if b == nil {
		return
	}
	report := func(reason nativetool.HistoryDropReason) {
		if reporter, ok := b.recorder.(nativetool.HistoryDropReporter); ok {
			reporter.NativeHistoryDropped(reason)
		}
	}
	if b.count == len(b.tickets) {
		report(nativetool.HistoryTransactionFull)
		return
	}
	if b.scopes == nil {
		report(nativetool.HistoryScopeUnavailable)
		return
	}
	stamp := recording.StampOriginal(b.scopes, op.RecordingScope)
	if !stamp.Current() || !stamp.MatchesCapture(op.RecordingScope.Capture) || stamp.Fence() != op.RecordingScope.Fence {
		report(nativetool.HistoryScopeStale)
		return
	}
	op.Arguments = nil
	op.Result = nil
	ticket, result := b.recorder.PrepareNativeHistory(stamp, nativetool.HistoryObservation{Operation: op, EventKind: event, EventAt: at, MaximumEventAt: op.MaximumEventAt, MetadataExpiresAt: op.MetadataExpiresAt})
	if result != recording.Accepted || ticket == nil {
		return
	}
	b.tickets[b.count] = ticket
	b.count++
}

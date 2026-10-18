package productruntime

import (
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/recording"
	"strings"
	"sync"
)

const nativePreparedBytes int64 = 16 << 10

type nativePreparedHistory struct {
	once     sync.Once
	recorder *observationRecorder
	item     *recordingItem
	result   recording.OfferResult
}

func nativeHistoryStringBytes(o nativetool.HistoryObservation) int64 {
	p := o.Operation
	fields := [18]string{string(p.ID), p.Capture.ID, string(p.Capture.Kind), p.RecordingScope.Capture.ID, string(p.RecordingScope.Capture.Kind), p.Capture.ID, string(p.Capture.Kind), string(p.EnvironmentID), string(p.Recording.Mode), string(p.Kind), p.ToolName, p.TargetOrigin, p.CredentialOwner, p.State, p.DecisionSource, p.Reason, o.EventKind, ""}
	if p.ApprovalID != nil {
		fields[17] = string(*p.ApprovalID)
	}
	var n int64
	for _, field := range fields {
		if len(field) > 1024 {
			return nativePreparedBytes
		}
		n += int64(len(field))
		if n > 1024 {
			return nativePreparedBytes
		}
	}
	return n
}
func (r *observationRecorder) PrepareNativeHistory(s recording.Stamp, o nativetool.HistoryObservation) (ticket nativetool.PreparedHistory, result recording.OfferResult) {
	defer func() {
		if result != recording.Accepted {
			r.budget.RecordOffer(recording.Native, result, 0)
		}
	}()
	if r.available != nil && !r.available() {
		return nil, recording.DroppedUnavailable
	}
	if !s.Current() {
		return nil, recording.DroppedStale
	}
	r.mu.Lock()
	closed := r.closed || r.sealed
	sink := r.nativeHistory
	r.mu.Unlock()
	if closed {
		return nil, recording.DroppedClosed
	}
	if sink == nil {
		return nil, recording.DroppedUnavailable
	}
	// Fixed envelope, ticket, holder bookkeeping, cloned pointers, complete string
	// backing and bounded JSON/driver workspace all fit this shared reservation.
	if 8192+8*nativeHistoryStringBytes(o) > nativePreparedBytes {
		return nil, recording.DroppedTooLarge
	}
	p := o.Operation
	if p.Arguments != nil || p.Result != nil || !s.MatchesCapture(p.Capture) || !s.MatchesCapture(p.RecordingScope.Capture) || s.Fence() != p.RecordingScope.Fence || p.Revision == 0 || o.EventKind == "" || o.EventAt.Before(p.CreatedAt) || o.MaximumEventAt.Before(o.EventAt) || !o.MetadataExpiresAt.After(p.CreatedAt) {
		return nil, recording.DroppedInvalid
	}
	reservation, ok := r.budget.TryReserve(recording.Native, nativePreparedBytes)
	if !ok {
		return nil, recording.DroppedCapacity
	}
	if err := (nativetool.RecordedHistory{Scope: o.Operation.RecordingScope, Observation: o}).Validate(); err != nil {
		reservation.Release()
		return nil, recording.DroppedInvalid
	}
	copy := cloneNativeHistory(o)
	owned := &nativetool.RecordedHistory{Scope: s.Scope(), Observation: copy}
	item := &recordingItem{kind: recording.Native, stamp: s, reservation: reservation, bytes: nativePreparedBytes, nativeHistory: owned, nativeSink: sink}
	return &nativePreparedHistory{recorder: r, item: item, result: recording.DroppedClosed}, recording.Accepted
}
func cloneNativeHistory(o nativetool.HistoryObservation) nativetool.HistoryObservation {
	p := &o.Operation
	p.ID = nativetool.ID(strings.Clone(string(p.ID)))
	p.Capture.ID = strings.Clone(p.Capture.ID)
	p.RecordingScope.Capture.ID = strings.Clone(p.RecordingScope.Capture.ID)
	p.EnvironmentID = typeClone(p.EnvironmentID)
	p.ToolName = strings.Clone(p.ToolName)
	p.TargetOrigin = strings.Clone(p.TargetOrigin)
	p.CredentialOwner = strings.Clone(p.CredentialOwner)
	p.State = strings.Clone(p.State)
	p.DecisionSource = strings.Clone(p.DecisionSource)
	p.Reason = strings.Clone(p.Reason)
	p.Kind = typeClone(p.Kind)
	p.Recording.Mode = typeClone(p.Recording.Mode)
	p.Capture.Kind = typeClone(p.Capture.Kind)
	p.RecordingScope.Capture.Kind = typeClone(p.RecordingScope.Capture.Kind)
	if p.ApprovalID != nil {
		v := nativetool.ID(strings.Clone(string(*p.ApprovalID)))
		p.ApprovalID = &v
	}
	if p.BindingDigest != nil {
		v := *p.BindingDigest
		p.BindingDigest = &v
	}
	if p.BytesIn != nil {
		v := *p.BytesIn
		p.BytesIn = &v
	}
	if p.BytesOut != nil {
		v := *p.BytesOut
		p.BytesOut = &v
	}
	if p.CompletedAt != nil {
		v := p.CompletedAt.UTC()
		p.CompletedAt = &v
	}
	p.CreatedAt = p.CreatedAt.UTC()
	p.MaximumEventAt = p.MaximumEventAt.UTC()
	p.MetadataExpiresAt = p.MetadataExpiresAt.UTC()
	o.EventKind = strings.Clone(o.EventKind)
	o.EventAt = o.EventAt.UTC()
	o.MaximumEventAt = o.MaximumEventAt.UTC()
	o.MetadataExpiresAt = o.MetadataExpiresAt.UTC()
	return o
}
func typeClone[T ~string](v T) T { return T(strings.Clone(string(v))) }
func (t *nativePreparedHistory) Publish() recording.OfferResult {
	t.once.Do(func() {
		r, item := t.recorder, t.item
		r.mu.Lock()
		defer r.mu.Unlock()
		result := recording.DroppedCapacity
		if r.closed || r.sealed {
			result = recording.DroppedClosed
		} else if !item.stamp.Current() {
			result = recording.DroppedStale
		} else {
			select {
			case r.queue <- item:
				result = recording.Accepted
				r.offered++
			default:
			}
		}
		t.result = result
		r.budget.RecordOffer(recording.Native, result, uint64(item.bytes))
		if result != recording.Accepted {
			item.reservation.Release()
		}
		t.item = nil
	})
	return t.result
}
func (t *nativePreparedHistory) Discard(reason nativetool.HistoryDiscardReason) {
	t.once.Do(func() {
		result := recording.DroppedOwnerCanceled
		switch reason {
		case nativetool.HistoryRolledBack:
			result = recording.DroppedRolledBack
		case nativetool.HistoryCommitUnknown:
			result = recording.DroppedControlCommitUnknown
		}
		t.result = result
		t.recorder.budget.RecordOffer(recording.Native, result, uint64(t.item.bytes))
		t.item.reservation.Release()
		t.item = nil
	})
}

var _ nativetool.HistoryRecorder = (*observationRecorder)(nil)

func (r *observationRecorder) NativeHistoryDropped(reason nativetool.HistoryDropReason) {
	result := recording.DroppedUnavailable
	switch reason {
	case nativetool.HistoryTransactionFull:
		result = recording.DroppedCapacity
	case nativetool.HistoryScopeStale:
		result = recording.DroppedStale
	}
	r.budget.RecordOffer(recording.Native, result, 0)
}

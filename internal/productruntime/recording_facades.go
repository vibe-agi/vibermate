package productruntime

import (
	"context"
	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"reflect"
)

// Read capabilities stay complete through the facade; producer methods only
// use the optional owner and never borrow the inspection/control path.
type recordingActivity struct {
	activity.InspectionReader
	owner *recordingStorage
}

func (a *recordingActivity) Prepare(e activity.Event) (activity.Record, error) {
	v, done, err := a.owner.acquire()
	if err != nil {
		return activity.Record{}, err
	}
	defer done()
	return v.activity.(activity.PreparedRecorder).Prepare(e)
}
func (a *recordingActivity) Record(ctx context.Context, e activity.Event) (activity.Record, error) {
	q := a.owner.recorder
	if !a.owner.available() {
		q.budget.RecordOffer(recording.Activity, recording.DroppedUnavailable, 0)
		return activity.Record{}, nil
	}
	prepared, err := a.Prepare(e)
	if err != nil {
		return prepared, err
	}
	if prepared.CaptureRunID != "" || prepared.ManualCaptureID != "" {
		q.budget.RecordOffer(recording.Activity, recording.DroppedStale, 0)
		return prepared, recording.ErrObservationDiscarded
	}
	stamp := q.ScopeStamp(captureidentity.Reference{})
	reservation, ok := (activityAttemptObserver{queue: q}).metadataReservation(stamp, e)
	if !ok {
		return prepared, nil
	}
	cloneSnapshot(reflect.ValueOf(&prepared).Elem())
	bytes := snapshotBytes(reflect.ValueOf(prepared))
	result := q.enqueue(&recordingItem{kind: recording.Activity, stamp: stamp, bytes: int64(bytes), reservation: reservation, metadata: &recordedMetadata{activity: prepared, activitySink: a}})
	q.budget.RecordOffer(recording.Activity, result, bytes)
	return prepared, nil
}

func (a *recordingActivity) RecordScopedPrepared(ctx context.Context, s recording.Scope, r activity.Record) (activity.Record, error) {
	v, done, err := a.owner.acquire()
	if err != nil {
		return activity.Record{}, err
	}
	defer done()
	return v.activity.(activity.ScopedPreparedRecorder).RecordScopedPrepared(ctx, s, r)
}
func (a *recordingActivity) Shutdown(context.Context) error { return nil }

type recordingContent struct {
	exchangecontent.InspectionReader
	owner *recordingStorage
}

func (a *recordingContent) Record(context.Context, exchangecontent.Record) error {
	return recording.ErrObservationDiscarded
}
func (a *recordingContent) RecordSource(context.Context, *exchangecontent.Source) error {
	return recording.ErrObservationDiscarded
}
func (a *recordingContent) RecordScopedSource(ctx context.Context, s recording.Scope, r *exchangecontent.Source) error {
	v, done, err := a.owner.acquire()
	if err != nil {
		return err
	}
	defer done()
	return v.content.(exchangecontent.ScopedSourceRecorder).RecordScopedSource(ctx, s, r)
}
func (a *recordingContent) Shutdown(context.Context) error { return nil }

type recordingRaw struct {
	*runtimeRawReader
	owner *recordingStorage
}

// Embedding the concrete independent reader retains its Begin/Check boundary.
type runtimeRawReader = runtimepersistence.RawInspectionReader

func (a *recordingRaw) Observe(ctx context.Context, o rawevidence.Observation) (rawevidence.Watermark, error) {
	if ctx == nil || ctx.Err() != nil {
		a.owner.recorder.budget.RecordOffer(recording.Raw, recording.DroppedClosed, 0)
		return rawevidence.Watermark{}, nil
	}
	a.TryObserve(o.ObservationStamp, o)
	return rawevidence.Watermark{}, nil
}
func (a *recordingRaw) Capture(meta rawevidence.CaptureMetadata, maximum int) *rawevidence.CaptureBuffer {
	v, done, err := a.owner.acquire()
	if err != nil {
		a.owner.recorder.budget.RecordOffer(recording.Raw, recording.DroppedUnavailable, 0)
		return nil
	}
	defer done()
	return v.raw.(interface {
		CaptureStamped(recording.Stamp, rawevidence.CaptureMetadata, int) *rawevidence.CaptureBuffer
	}).CaptureStamped(meta.Context.ObservationStamp, meta, maximum)
}

func (a *recordingRaw) TryObserve(s recording.Stamp, o rawevidence.Observation) recording.OfferResult {
	v, done, err := a.owner.acquire()
	if err != nil {
		a.owner.recorder.budget.RecordOffer(recording.Raw, recording.DroppedUnavailable, 0)
		return recording.DroppedUnavailable
	}
	defer done()
	return v.raw.(interface {
		TryObserve(recording.Stamp, rawevidence.Observation) recording.OfferResult
	}).TryObserve(s, o)
}
func (a *recordingRaw) BeginScope(ctx context.Context, k rawevidence.ScopeKind, id string) (rawevidence.ScopeLease, error) {
	v, done, err := a.owner.acquire()
	if err != nil {
		return emptyRecordingScope{}, nil
	}
	defer done()
	return v.raw.BeginScope(ctx, k, id)
}
func (a *recordingRaw) PrepareTerminalScope(ctx context.Context, k rawevidence.ScopeKind, id string) (rawevidence.TerminalScope, error) {
	v, done, err := a.owner.acquire()
	if err != nil {
		return emptyRecordingScope{}, nil
	}
	defer done()
	return v.raw.PrepareTerminalScope(ctx, k, id)
}
func (a *recordingRaw) Flush(ctx context.Context, w rawevidence.Watermark) error {
	v, done, err := a.owner.acquire()
	if err != nil {
		return err
	}
	defer done()
	return v.raw.Flush(ctx, w)
}
func (a *recordingRaw) FlushScope(ctx context.Context, k rawevidence.ScopeKind, id string) error {
	v, done, err := a.owner.acquire()
	if err != nil {
		return err
	}
	defer done()
	return v.raw.FlushScope(ctx, k, id)
}
func (a *recordingRaw) Statistics() rawevidence.Statistics {
	v, done, err := a.owner.acquire()
	if err != nil {
		return rawevidence.Statistics{}
	}
	defer done()
	return v.raw.Statistics()
}
func (a *recordingRaw) Shutdown(context.Context) error { return nil }
func (s *recordingStorage) Recovery() rawevidence.Recovery {
	v, done, err := s.acquire()
	if err != nil {
		return rawevidence.Recovery{}
	}
	defer done()
	return v.raw.Recovery()
}

type emptyRecordingScope struct{}

func (emptyRecordingScope) Release() {}
func (emptyRecordingScope) Commit()  {}
func (emptyRecordingScope) Abort()   {}

func (a *recordingRaw) SealTerminalScope(ctx context.Context, k rawevidence.ScopeKind, id string) (rawevidence.TerminalScope, error) {
	v, done, err := a.owner.acquire()
	if err != nil {
		return emptyRecordingScope{}, nil
	}
	defer done()
	return v.raw.(interface {
		SealTerminalScope(context.Context, rawevidence.ScopeKind, string) (rawevidence.TerminalScope, error)
	}).SealTerminalScope(ctx, k, id)
}

func (a *recordingRaw) RecordingAvailable() bool { return a.owner.available() }

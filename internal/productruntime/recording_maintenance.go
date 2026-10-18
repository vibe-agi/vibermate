package productruntime

import (
	"context"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"sync"
)

// recordingMaintenanceLease is the narrow Task5 handoff. It owns actual writer
// exclusion, reader invalidation and optional handles until Complete is called
// after the caller's real I/O return. false keeps the source unavailable.
type recordingMaintenanceLease interface {
	Store() *runtimepersistence.ObservationStore
	Publish([]runtimepersistence.InspectionSource) error
	Complete(usable bool)
}

type recordingMaintenanceRequest struct {
	ctx        context.Context
	generation uint64
	ready      chan recordingMaintenanceResult
	claimed    chan struct{}
}
type recordingMaintenanceResult struct {
	lease recordingMaintenanceLease
	err   error
}
type recordingMaintenance struct {
	store     *runtimepersistence.ObservationStore
	catalog   *runtimepersistence.InspectionMaintenance
	completed chan bool
	once      sync.Once
}

func (m *recordingMaintenance) Store() *runtimepersistence.ObservationStore { return m.store }
func (m *recordingMaintenance) Publish(sources []runtimepersistence.InspectionSource) error {
	return m.catalog.Publish(sources)
}
func (m *recordingMaintenance) Complete(usable bool) { m.once.Do(func() { m.completed <- usable }) }

func (s *recordingStorage) BeginMaintenance(ctx context.Context, generation uint64) (recordingMaintenanceLease, error) {
	if ctx == nil {
		return nil, recording.ErrInspectionUnavailable
	}
	request := recordingMaintenanceRequest{ctx: ctx, generation: generation, ready: make(chan recordingMaintenanceResult, 1), claimed: make(chan struct{})}
	select {
	case s.maintenanceRequests <- request:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.done:
		return nil, recording.ErrInspectionUnavailable
	}
	select {
	case result := <-request.ready:
		if result.lease != nil {
			close(request.claimed)
		}
		return result.lease, result.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.done:
		return nil, recording.ErrInspectionUnavailable
	}
}
func (s *recordingStorage) maintain(sinks *recordingSinks, request recordingMaintenanceRequest) {
	if s.unsafeMaintenance.Load() {
		request.ready <- recordingMaintenanceResult{err: recording.ErrInspectionUnavailable}
		return
	}
	if request.ctx.Err() != nil {
		return
	}
	s.paused.Store(true)
	s.recorder.mu.Lock()
	s.recorder.sealed = true
	s.recorder.mu.Unlock()
	resumeRaw := sinks.raw.(interface{ PauseOffers() func() }).PauseOffers()
	// The fixed owner retains every seal even when the requesting caller times
	// out. A retry cannot overtake actual I/O owned by the previous request.
	err := s.recorder.Flush(s.ctx)
	if err == nil {
		err = sinks.raw.Flush(s.ctx, rawevidence.Watermark{})
	}
	if err == nil {
		err = s.waitActive(s.ctx)
	}
	var token *runtimepersistence.InspectionMaintenance
	if err == nil {
		token, err = s.catalog.BeginMaintenance(s.ctx, request.generation)
	}
	usable := true
	if err == nil {
		lease := &recordingMaintenance{store: sinks.store, catalog: token, completed: make(chan bool, 1)}
		request.ready <- recordingMaintenanceResult{lease: lease}
		select {
		case <-request.claimed:
			usable = <-lease.completed // actual mutation owner, not caller context
		case <-request.ctx.Done():
		case <-s.ctx.Done():
		}
	} else {
		request.ready <- recordingMaintenanceResult{err: err}
	}
	if !usable {
		s.unsafeMaintenance.Store(true)
		// Unknown/post-mutation failure must never reopen this catalog or writer.
		// Mark closed before releasing the maintenance token, retaining handles.
		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		_ = s.catalog.Close(canceled)
		if token != nil {
			_ = token.Close()
		}
		s.tracker.recordingState(RecordingDegraded)
		return
	}
	if token != nil {
		_ = token.Close()
	}
	if s.ctx.Err() == nil {
		resumeRaw()
		s.recorder.mu.Lock()
		if !s.recorder.closed {
			s.recorder.sealed = false
		}
		s.recorder.mu.Unlock()
		s.paused.Store(false)
	}
}
func (s *recordingStorage) waitActive(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for s.active != 0 {
		changed := s.changed
		s.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			s.mu.Lock()
			return ctx.Err()
		}
		s.mu.Lock()
	}
	return nil
}

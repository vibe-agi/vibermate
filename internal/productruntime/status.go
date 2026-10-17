package productruntime

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
)

// RuntimeState is a language-independent lifecycle state.
type RuntimeState string

const (
	RuntimeStateStarting    RuntimeState = "starting"
	RuntimeStateInitialized RuntimeState = "initialized"
	RuntimeStateDegraded    RuntimeState = "degraded"
	RuntimeStateStopping    RuntimeState = "stopping"
	RuntimeStateStopped     RuntimeState = "stopped"
	RuntimeStateStopFailed  RuntimeState = "stop_failed"
)

const StopReasonShutdownFailed = "shutdown_failed"

// StorageState is a language-independent SQLite health state.
type StorageState string

const (
	StorageStateHealthy     StorageState = "healthy"
	StorageStateUnavailable StorageState = "unavailable"
)

// PersistenceFailure contains only closed diagnostic codes, never the original
// error (which may contain paths, SQL values, URLs or credentials).
type PersistenceFailure struct {
	Operation string    `json:"operation"`
	Reason    string    `json:"reason"`
	At        time.Time `json:"at"`
}

func (failure PersistenceFailure) Valid() bool {
	switch failure.Operation {
	case "egress_complete", "egress_terminal", "egress_drain", "activity_start", "activity_terminal", "conversation_identity", "usage", "response_content", "raw_evidence":
	default:
		return false
	}
	return !failure.At.IsZero() && (failure.Reason == "timeout" || failure.Reason == "canceled" || failure.Reason == "write_failed")
}

func (failure PersistenceFailure) CoreAudit() bool {
	return failure.Operation == "egress_complete" || failure.Operation == "egress_terminal" || failure.Operation == "egress_drain"
}

func persistenceFailure(operation string, err error, at time.Time) *PersistenceFailure {
	reason := "write_failed"
	if errors.Is(err, context.DeadlineExceeded) {
		reason = "timeout"
	} else if errors.Is(err, context.Canceled) {
		reason = "canceled"
	}
	return &PersistenceFailure{Operation: operation, Reason: reason, At: at.UTC()}
}

// RuntimeStatus is the immutable status projection returned to a host.
//
// InstanceID intentionally uses the existing instanceId wire field. It is a
// process incarnation, not an installation identity or durable revision.
type RuntimeStatus struct {
	State                 RuntimeState                 `json:"state"`
	InstanceID            string                       `json:"instanceId"`
	Host                  hostcontract.Kind            `json:"host"`
	SchemaRevision        int64                        `json:"schemaRevision"`
	Storage               StorageState                 `json:"storage"`
	StorageFailure        *PersistenceFailure          `json:"storageFailure,omitempty"`
	RecordingFailure      *PersistenceFailure          `json:"recordingFailure,omitempty"`
	EnvironmentProjection environment.ProjectionHealth `json:"environmentProjection"`
	OfflineHold           offlinehold.Snapshot         `json:"offlineHold"`
	StartedAt             time.Time                    `json:"startedAt"`
	StoppedAt             *time.Time                   `json:"stoppedAt,omitempty"`
	StopReasonCode        string                       `json:"stopReasonCode,omitempty"`
}

type statusTracker struct {
	mu                    sync.RWMutex
	status                RuntimeStatus
	storageFailureLatched bool
	clock                 Clock
}

func newStatusTracker(instanceID string, host hostcontract.Kind, startedAt time.Time) *statusTracker {
	return &statusTracker{
		clock: SystemClock{},
		status: RuntimeStatus{
			State:      RuntimeStateStarting,
			InstanceID: instanceID,
			Host:       host,
			Storage:    StorageStateHealthy,
			StartedAt:  startedAt,
		},
	}
}

func (t *statusTracker) snapshot() RuntimeStatus {
	t.mu.RLock()
	defer t.mu.RUnlock()
	status := t.status
	if status.StorageFailure != nil {
		failure := *status.StorageFailure
		status.StorageFailure = &failure
	}
	if status.RecordingFailure != nil {
		failure := *status.RecordingFailure
		status.RecordingFailure = &failure
	}
	return status
}

func (t *statusTracker) commitInitialized(schemaRevision int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.status.SchemaRevision = schemaRevision
	if t.storageFailureLatched {
		t.status.State = RuntimeStateDegraded
		t.status.Storage = StorageStateUnavailable
		return
	}
	t.status.State = RuntimeStateInitialized
	t.status.Storage = StorageStateHealthy
}

func (t *statusTracker) observeStorage(schemaRevision int64, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.status.State == RuntimeStateStopping || t.status.State == RuntimeStateStopped {
		return
	}
	if err != nil {
		t.status.Storage = StorageStateUnavailable
		if t.status.State == RuntimeStateInitialized {
			t.status.State = RuntimeStateDegraded
		}
		return
	}
	if t.storageFailureLatched {
		t.status.Storage = StorageStateUnavailable
		t.status.SchemaRevision = schemaRevision
		if t.status.State == RuntimeStateInitialized {
			t.status.State = RuntimeStateDegraded
		}
		return
	}
	t.status.Storage = StorageStateHealthy
	t.status.SchemaRevision = schemaRevision
	if t.status.State == RuntimeStateDegraded {
		t.status.State = RuntimeStateInitialized
	}
}

// failStorage latches a write-path durability failure for this process
// incarnation. A later read-only schema poll cannot prove that an omitted
// audit terminal was persisted, so only a restart and recovery may clear it.
func (t *statusTracker) failStorage(operation string, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.status.StorageFailure == nil {
		t.status.StorageFailure = persistenceFailure(operation, err, t.clock.Now())
	}
	t.storageFailureLatched = true
	t.status.Storage = StorageStateUnavailable
	switch t.status.State {
	case RuntimeStateInitialized:
		t.status.State = RuntimeStateDegraded
	case RuntimeStateStopped:
		t.status.State = RuntimeStateStopFailed
		t.status.StoppedAt = nil
		t.status.StopReasonCode = StopReasonShutdownFailed
	}
}

// Optional recording is not runtime admission authority. Preserve the first
// gap for operators without disabling ready, future requests or shutdown.
func (t *statusTracker) failRecording(operation string, err error) {
	if err == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.status.RecordingFailure == nil {
		t.status.RecordingFailure = persistenceFailure(operation, err, t.clock.Now())
	}
}

func (t *statusTracker) beginStopping() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.status.State == RuntimeStateStopped {
		return
	}
	t.status.State = RuntimeStateStopping
}

func (t *statusTracker) finishStopping(stoppedAt time.Time, shutdownErr error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.storageFailureLatched {
		t.status.State = RuntimeStateStopFailed
		t.status.Storage = StorageStateUnavailable
		t.status.StopReasonCode = StopReasonShutdownFailed
		return
	}
	if shutdownErr != nil {
		t.status.State = RuntimeStateStopFailed
		t.status.StopReasonCode = StopReasonShutdownFailed
		return
	}
	t.status.State = RuntimeStateStopped
	t.status.StoppedAt = &stoppedAt
}

package productruntime

import (
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/access"
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

// RuntimeStatus is the immutable status projection returned to a host.
//
// InstanceID intentionally uses the existing instanceId wire field. It is a
// process incarnation, not an installation identity or durable revision.
type RuntimeStatus struct {
	State            RuntimeState            `json:"state"`
	InstanceID       string                  `json:"instanceId"`
	Host             hostcontract.Kind       `json:"host"`
	SchemaRevision   int64                   `json:"schemaRevision"`
	Storage          StorageState            `json:"storage"`
	AccessProjection access.ProjectionHealth `json:"accessProjection"`
	OfflineHold      offlinehold.Snapshot    `json:"offlineHold"`
	StartedAt        time.Time               `json:"startedAt"`
	StoppedAt        *time.Time              `json:"stoppedAt,omitempty"`
	StopReasonCode   string                  `json:"stopReasonCode,omitempty"`
}

type statusTracker struct {
	mu     sync.RWMutex
	status RuntimeStatus
}

func newStatusTracker(instanceID string, host hostcontract.Kind, startedAt time.Time) *statusTracker {
	return &statusTracker{
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
	return t.status
}

func (t *statusTracker) commitInitialized(schemaRevision int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.status.State = RuntimeStateInitialized
	t.status.SchemaRevision = schemaRevision
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
	t.status.Storage = StorageStateHealthy
	t.status.SchemaRevision = schemaRevision
	if t.status.State == RuntimeStateDegraded {
		t.status.State = RuntimeStateInitialized
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
	if shutdownErr != nil {
		t.status.State = RuntimeStateStopFailed
		t.status.StopReasonCode = StopReasonShutdownFailed
		return
	}
	t.status.State = RuntimeStateStopped
	t.status.StoppedAt = &stoppedAt
}

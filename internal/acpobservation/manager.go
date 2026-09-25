package acpobservation

import (
	"context"
	"errors"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/environment"
)

type AssignmentReader interface {
	Resolve(context.Context, captureidentity.Reference) (captureassignment.Assignment, error)
}
type Clock interface{ Now() time.Time }

type Manager struct {
	repository   Repository
	runs         capturerun.ControlAuthorizer
	assignments  AssignmentReader
	environments environment.SnapshotResolver
	clock        Clock
}

func NewManager(repository Repository, runs capturerun.Controller, assignments AssignmentReader, environments environment.SnapshotResolver, clock Clock) (*Manager, error) {
	if repository == nil || runs == nil || assignments == nil || environments == nil || clock == nil {
		return nil, errors.New("ACP observation dependencies are incomplete")
	}
	authorizer, ok := runs.(capturerun.ControlAuthorizer)
	if !ok {
		return nil, errors.New("ACP requires Capture control authorization")
	}
	return &Manager{repository: repository, runs: authorizer, assignments: assignments, environments: environments, clock: clock}, nil
}

// Start freezes recording against this Capture's exact launch revision, not
// whichever revision is currently selected in a settings page. This authority
// never applies an HTTP proxy or claims model-service policy enforcement.
func (manager *Manager) Start(ctx context.Context, runID string, capability capturerun.ControlCapability, content bool) (Record, error) {
	view, err := manager.runs.AuthorizeControl(ctx, runID, capability)
	if err != nil {
		return Record{}, capturerun.ErrCapabilityRejected
	}
	if view.State != capturerun.StateCreated {
		// Transport is chosen before process attachment. An ordinary HTTP run
		// cannot later be relabeled ACP and hide its existing HTTP evidence.
		return Record{}, ErrConflict
	}
	capture, err := captureidentity.New(captureidentity.KindManagedRun, runID)
	if err != nil {
		return Record{}, ErrInvalid
	}
	assignment, err := manager.assignments.Resolve(ctx, capture)
	if err != nil {
		return Record{}, ErrInvalid
	}
	boundary := assignment.LaunchAuthority
	snapshot, err := manager.environments.ResolveRevision(ctx, boundary.InitialEnvironmentID(), boundary.InitialEnvironmentRevision())
	if err != nil || snapshot.Digest() != boundary.InitialEnvironmentDigest() {
		return Record{}, ErrInvalid
	}
	policy := snapshot.ContentRecording()
	if policy.Mode == environment.ContentRecordingFull && !content {
		policy.Mode = environment.ContentRecordingMetadataOnly
	}
	days := policy.RetentionDays
	if days == 0 {
		days = environment.DefaultContentRetentionDays
	}
	now := manager.clock.Now().UnixMilli()
	record := Record{RunID: runID, Policy: policy, ExpiresAtMillis: view.CreatedAt.Add(time.Duration(days) * 24 * time.Hour).UnixMilli(), Snapshot: NewObserver(false).Snapshot()}
	if err := manager.repository.Create(ctx, record, now); err != nil {
		return Record{}, err
	}
	return manager.repository.Read(ctx, runID, now)
}

func (manager *Manager) Publish(ctx context.Context, runID string, capability capturerun.ControlCapability, snapshot Snapshot) error {
	if _, err := manager.runs.AuthorizeControl(ctx, runID, capability); err != nil {
		return capturerun.ErrCapabilityRejected
	}
	return manager.repository.Save(ctx, runID, snapshot, manager.clock.Now().UnixMilli())
}

// Read is a management projection; Hosts must require their ordinary owner
// read authority. A run capability cannot use it to enumerate other captures.
func (manager *Manager) Read(ctx context.Context, runID string) (Record, error) {
	return manager.repository.Read(ctx, runID, manager.clock.Now().UnixMilli())
}

func (manager *Manager) Markers(ctx context.Context, runIDs []string) (map[string]bool, error) {
	return manager.repository.Markers(ctx, runIDs, manager.clock.Now().UnixMilli())
}

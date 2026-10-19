package acpobservation

import (
	"context"
	"errors"
	"github.com/vibe-agi/vibermate/internal/recording"
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
	repository   RegistrationRepository
	recording    RecordingOptions
	runs         capturerun.ControlAuthorizer
	assignments  AssignmentReader
	environments environment.SnapshotResolver
	clock        Clock
}

func NewManager(repository RegistrationRepository, runs capturerun.Controller, assignments AssignmentReader, environments environment.SnapshotResolver, clock Clock, options ...RecordingOptions) (*Manager, error) {
	if repository == nil || runs == nil || assignments == nil || environments == nil || clock == nil {
		return nil, errors.New("ACP observation dependencies are incomplete")
	}
	authorizer, ok := runs.(capturerun.ControlAuthorizer)
	if !ok {
		return nil, errors.New("ACP requires Capture control authorization")
	}
	var observation RecordingOptions
	if len(options) > 1 {
		return nil, errors.New("multiple ACP recording options")
	}
	if len(options) == 1 {
		observation = options[0]
	}
	return &Manager{repository: repository, recording: observation, runs: authorizer, assignments: assignments, environments: environments, clock: clock}, nil
}

// Start freezes recording against this Capture's exact launch revision, not
// whichever revision is currently selected in a settings page. This authority
// never applies an HTTP proxy or claims model-service policy enforcement.
func (manager *Manager) Start(ctx context.Context, runID string, capability capturerun.ControlCapability, content bool) (Record, error) {
	view, err := manager.runs.AuthorizeControl(ctx, runID, capability)
	if err != nil {
		return Record{}, err
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
		return Record{}, err
	}
	boundary := assignment.LaunchAuthority
	snapshot, err := manager.environments.ResolveRevision(ctx, boundary.InitialEnvironmentID(), boundary.InitialEnvironmentRevision())
	if err != nil {
		return Record{}, err
	}
	if snapshot.Digest() != boundary.InitialEnvironmentDigest() {
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
	registration, err := manager.repository.Register(ctx, Registration{RunID: runID, Policy: policy, ExpiresAtMillis: view.CreatedAt.Add(time.Duration(days) * 24 * time.Hour).UnixMilli()}, now)
	if err != nil {
		return Record{}, err
	}
	record := Record{RunID: registration.RunID, Policy: registration.Policy, ExpiresAtMillis: registration.ExpiresAtMillis, Scope: recording.Scope{Capture: capture, Fence: registration.Fence}, Snapshot: Snapshot{Revision: 1, Sessions: []Session{}, Prompts: []Prompt{}}}
	manager.observe(record)
	return record, nil
}

func (manager *Manager) Publish(ctx context.Context, runID string, capability capturerun.ControlCapability, snapshot Snapshot) error {
	if _, err := manager.runs.AuthorizeControl(ctx, runID, capability); err != nil {
		return err
	}
	registration, err := manager.repository.Registration(ctx, runID, manager.clock.Now().UnixMilli())
	if err != nil {
		return err
	}
	if err := snapshot.ValidatePolicy(registration.Policy); err != nil {
		return err
	}
	capture, err := captureidentity.New(captureidentity.KindManagedRun, runID)
	if err != nil {
		return ErrInvalid
	}
	manager.observe(Record{RunID: runID, Policy: registration.Policy, ExpiresAtMillis: registration.ExpiresAtMillis, Scope: recording.Scope{Capture: capture, Fence: registration.Fence}, Snapshot: snapshot})
	return nil
}

// Read is a management projection; Hosts must require their ordinary owner
// read authority. A run capability cannot use it to enumerate other captures.
func (manager *Manager) Read(ctx context.Context, runID string) (Record, error) {
	if manager.recording.Reader == nil {
		return Record{}, ErrNotFound
	}
	return manager.recording.Reader.Read(ctx, runID, manager.clock.Now().UnixMilli())
}

func (manager *Manager) Markers(ctx context.Context, runIDs []string) (map[string]bool, error) {
	return manager.repository.Markers(ctx, runIDs)
}

func (manager *Manager) observe(record Record) {
	if manager.recording.Recorder == nil || manager.recording.Scopes == nil {
		return
	}
	stamp := recording.StampOriginal(manager.recording.Scopes, record.Scope)
	if stamp.Fence() != record.Scope.Fence || !stamp.MatchesCapture(record.Scope.Capture) {
		return
	}
	manager.recording.Recorder.ObserveACP(stamp, record)
}

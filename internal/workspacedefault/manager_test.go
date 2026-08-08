package workspacedefault

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
	"github.com/vibe-agi/vibermate/internal/workspaceidentity"
)

func TestManagerSelectsOnlyActiveUserEnvironmentsWithCAS(t *testing.T) {
	t.Parallel()
	key := testKey(t)
	active := compileEnvironment(t, "work", environment.StateActive)
	disabled := compileEnvironment(t, "disabled", environment.StateDisabled)
	repository := &memoryRepository{}
	manager, err := New(
		repository,
		fixedEnvironments{active.ID(): active, disabled.ID(): disabled},
		fixedClock{value: time.Date(2026, 8, 8, 9, 30, 0, 123456789, time.UTC)},
	)
	if err != nil {
		t.Fatal(err)
	}

	if _, exists, err := manager.Get(context.Background(), key); err != nil || exists {
		t.Fatalf("empty default = exists %t, error %v", exists, err)
	}
	first, err := manager.Set(context.Background(), SetCommand{
		Key: key, ExpectedRevision: 0, EnvironmentID: active.ID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision != 1 || first.EnvironmentID != active.ID() || first.UpdatedAt.Nanosecond() != 123_000_000 {
		t.Fatalf("first record = %+v", first)
	}
	if _, err := manager.Set(context.Background(), SetCommand{
		Key: key, ExpectedRevision: 0, EnvironmentID: active.ID(),
	}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale set error = %v, want revision conflict", err)
	}
	if _, err := manager.Set(context.Background(), SetCommand{
		Key: key, ExpectedRevision: 1, EnvironmentID: disabled.ID(),
	}); !errors.Is(err, ErrEnvironmentNotActive) {
		t.Fatalf("disabled set error = %v, want inactive", err)
	}
	if _, err := manager.Set(context.Background(), SetCommand{
		Key: key, ExpectedRevision: 1, EnvironmentID: environment.SystemTransparentID,
	}); !errors.Is(err, ErrEnvironmentNotActive) {
		t.Fatalf("transparent set error = %v, want inactive", err)
	}

	scope, err := workspaceidentity.NewScope(
		key.MachineID, key.WorkspaceID, "workspace", workspaceidentity.EvidenceLocalLauncher, 1, 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	resolved, exists, err := manager.Resolve(context.Background(), scope)
	if err != nil || !exists || resolved != first {
		t.Fatalf("resolved default = %+v, exists %t, error %v", resolved, exists, err)
	}
	if err := manager.Clear(context.Background(), ClearCommand{Key: key, ExpectedRevision: 1}); err != nil {
		t.Fatal(err)
	}
	if _, exists, err := manager.Resolve(context.Background(), scope); err != nil || exists {
		t.Fatalf("cleared default = exists %t, error %v", exists, err)
	}
}

type memoryRepository struct {
	mu     sync.Mutex
	record Record
	exists bool
}

func (repository *memoryRepository) Load(_ context.Context, key Key) (Record, bool, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if !repository.exists || repository.record.Key != key {
		return Record{}, false, nil
	}
	return repository.record, true, nil
}

func (repository *memoryRepository) Write(_ context.Context, expected uint64, candidate Record) (CommitResult, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	actual := uint64(0)
	if repository.exists {
		actual = repository.record.Revision
	}
	if actual != expected {
		return CommitResult{Outcome: CommitConflict, Record: repository.record, Actual: actual}, nil
	}
	repository.record, repository.exists = candidate, true
	return CommitResult{Outcome: CommitCommitted, Record: candidate, Actual: candidate.Revision}, nil
}

func (repository *memoryRepository) Delete(_ context.Context, key Key, expected uint64) (CommitResult, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if !repository.exists || repository.record.Key != key || repository.record.Revision != expected {
		actual := uint64(0)
		if repository.exists && repository.record.Key == key {
			actual = repository.record.Revision
		}
		return CommitResult{Outcome: CommitConflict, Actual: actual}, nil
	}
	repository.record, repository.exists = Record{}, false
	return CommitResult{Outcome: CommitCommitted, Actual: expected, Deleted: true}, nil
}

type fixedEnvironments map[environment.EnvironmentID]environment.EnvironmentSnapshot

func (environments fixedEnvironments) Get(_ context.Context, id environment.EnvironmentID) (environment.EnvironmentSnapshot, error) {
	snapshot, exists := environments[id]
	if !exists {
		return environment.EnvironmentSnapshot{}, environment.ErrEnvironmentNotFound
	}
	return snapshot, nil
}

type fixedClock struct{ value time.Time }

func (clock fixedClock) Now() time.Time { return clock.value }

type inertProtocols struct{}

func (inertProtocols) Resolve(protocolspec.Dialect, protocolspec.Dialect) (protocolspec.CodecPlan, error) {
	return protocolspec.CodecPlan{}, protocolspec.ErrUnsupportedCodecPair
}

func (inertProtocols) OperationsForDialect(protocolspec.Dialect) ([]protocolspec.ClientOperationPlan, error) {
	return nil, protocolspec.ErrUnknownDialect
}

type inertWireProfiles struct{}

func (inertWireProfiles) Resolve(wireprofile.UpstreamWireProfileRef) (wireprofile.CompiledUpstreamWireProfile, error) {
	return wireprofile.CompiledUpstreamWireProfile{}, wireprofile.ErrInvalidProfile
}

func compileEnvironment(t *testing.T, id string, state environment.State) environment.EnvironmentSnapshot {
	t.Helper()
	compiler, err := environment.NewCompiler(nil, inertProtocols{}, inertWireProfiles{})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := compiler.Compile(environment.Environment{
		ID: environment.EnvironmentID(id), Name: id, State: state, Revision: 1,
		ContentRecording: environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff},
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func testKey(t *testing.T) Key {
	t.Helper()
	encoded := func(value byte) string {
		return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{value}, 32))
	}
	key, err := NewKey(encoded(1), encoded(2))
	if err != nil {
		t.Fatal(err)
	}
	return key
}

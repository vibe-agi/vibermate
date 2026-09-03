package manualcapture

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturecredential"
	"github.com/vibe-agi/vibermate/internal/evidencearchive"
)

func TestCreateHoldsTheArchiveBarrierUntilPersistenceCompletes(t *testing.T) {
	t.Parallel()

	barrier := &recordingCaptureCreationBarrier{}
	repository := &barrierCheckingRepository{
		Repository: &successfulCreateRepository{},
		barrier:    barrier,
	}
	options := DefaultOptions(repository)
	options.Random = strings.NewReader(strings.Repeat("m", 128))
	options.ArchiveBarrier = barrier
	manager, err := NewManager(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.Create(context.Background(), CreateCommand{
		Owner:       NewLocalOwnerScope(),
		DisplayName: "Desktop client",
		ClientClass: ClientCLI,
		Lifetime:    LifetimeUntilRevoked,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !repository.sawBarrier || barrier.calls != 1 || barrier.releases != 1 ||
		barrier.active != 0 {
		t.Fatalf(
			"archive barrier saw=%t calls=%d releases=%d active=%d",
			repository.sawBarrier,
			barrier.calls,
			barrier.releases,
			barrier.active,
		)
	}
}

type recordingCaptureCreationBarrier struct {
	active   int
	calls    int
	releases int
}

func (barrier *recordingCaptureCreationBarrier) BeginCaptureCreation(
	context.Context,
) (evidencearchive.Release, error) {
	barrier.calls++
	barrier.active++
	return func() {
		barrier.releases++
		barrier.active--
	}, nil
}

type barrierCheckingRepository struct {
	Repository
	barrier    *recordingCaptureCreationBarrier
	sawBarrier bool
}

type successfulCreateRepository struct {
	recovery     Recovery
	recoverCalls int
}

func (*successfulCreateRepository) Create(
	context.Context,
	DurableRecord,
) error {
	return nil
}

func (*successfulCreateRepository) Rotate(
	context.Context,
	OwnerScope,
	ID,
	CredentialRevision,
	CredentialDigest,
	time.Time,
) (DurableRecord, error) {
	return DurableRecord{}, errors.New("unexpected Rotate")
}

func (*successfulCreateRepository) Revoke(
	context.Context,
	OwnerScope,
	ID,
	CredentialRevision,
	time.Time,
) (DurableRecord, error) {
	return DurableRecord{}, errors.New("unexpected Revoke")
}

func (*successfulCreateRepository) AuthorizeProxy(
	context.Context,
	CredentialDigest,
	time.Time,
) (DurableRecord, error) {
	return DurableRecord{}, errors.New("unexpected AuthorizeProxy")
}

func (*successfulCreateRepository) Get(
	context.Context,
	OwnerScope,
	ID,
	time.Time,
) (DurableRecord, error) {
	return DurableRecord{}, errors.New("unexpected Get")
}

func (*successfulCreateRepository) List(
	context.Context,
	PageRequest,
	time.Time,
) ([]DurableRecord, error) {
	return nil, errors.New("unexpected List")
}

func (repository *successfulCreateRepository) Recover(context.Context, time.Time) (Recovery, error) {
	repository.recoverCalls++
	return repository.recovery, nil
}

func (*successfulCreateRepository) Active(context.Context, ID, time.Time) (bool, error) {
	return false, errors.New("unexpected Active")
}

func TestActiveCountReconcilesEveryOwnerWithoutExposingTheirCatalogs(t *testing.T) {
	t.Parallel()
	repository := &successfulCreateRepository{}
	manager, err := NewManager(context.Background(), DefaultOptions(repository))
	if err != nil {
		t.Fatal(err)
	}
	repository.recovery = Recovery{ActiveCount: 3}
	count, err := manager.ActiveCount(context.Background())
	if err != nil || count != 3 || repository.recoverCalls != 2 {
		t.Fatalf("active count=%d calls=%d error=%v", count, repository.recoverCalls, err)
	}
}

func (repository *barrierCheckingRepository) Create(
	ctx context.Context,
	record DurableRecord,
) error {
	repository.sawBarrier = repository.barrier.active == 1
	return repository.Repository.Create(ctx, record)
}

func TestGeneratedManualCaptureIDHasAControlResourcePrefix(t *testing.T) {
	t.Parallel()

	id, err := newManualCaptureID(bytes.NewReader(bytes.Repeat([]byte{0xfb}, manualCaptureIDBytes)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(id.String(), manualCaptureIDPrefix) ||
		id.String()[0] == '-' || id.String()[0] == '_' {
		t.Fatalf("generated ManualCapture ID = %q", id.String())
	}
}

type fixedClock struct {
	now time.Time
}

func (clock fixedClock) Now() time.Time {
	return clock.now
}

type blockedCreateRepository struct {
	started chan struct{}
}

func (repository *blockedCreateRepository) Create(
	ctx context.Context,
	_ DurableRecord,
) error {
	close(repository.started)
	<-ctx.Done()
	return context.Cause(ctx)
}

func (*blockedCreateRepository) Rotate(
	context.Context,
	OwnerScope,
	ID,
	CredentialRevision,
	CredentialDigest,
	time.Time,
) (DurableRecord, error) {
	return DurableRecord{}, errors.New("unexpected Rotate")
}

func (*blockedCreateRepository) Revoke(
	context.Context,
	OwnerScope,
	ID,
	CredentialRevision,
	time.Time,
) (DurableRecord, error) {
	return DurableRecord{}, errors.New("unexpected Revoke")
}

func (*blockedCreateRepository) AuthorizeProxy(
	context.Context,
	CredentialDigest,
	time.Time,
) (DurableRecord, error) {
	return DurableRecord{}, errors.New("unexpected AuthorizeProxy")
}

func (*blockedCreateRepository) Get(
	context.Context,
	OwnerScope,
	ID,
	time.Time,
) (DurableRecord, error) {
	return DurableRecord{}, errors.New("unexpected Get")
}

func (*blockedCreateRepository) List(
	context.Context,
	PageRequest,
	time.Time,
) ([]DurableRecord, error) {
	return nil, errors.New("unexpected List")
}

func (*blockedCreateRepository) Recover(context.Context, time.Time) (Recovery, error) {
	return Recovery{}, nil
}

func (*blockedCreateRepository) Active(context.Context, ID, time.Time) (bool, error) {
	return false, errors.New("unexpected Active")
}

func TestManagerShutdownCancelsAndDrainsOperationsWithoutRevokingCaptures(t *testing.T) {
	repository := &blockedCreateRepository{started: make(chan struct{})}
	manager, err := NewManager(context.Background(), Options{
		Repository:           repository,
		Clock:                fixedClock{now: time.Date(2026, 8, 4, 13, 0, 0, 0, time.UTC)},
		Random:               strings.NewReader(strings.Repeat("r", 128)),
		MaxTemporaryLifetime: time.Hour,
		ArchiveBarrier:       evidencearchive.NewBarrier(),
	})
	if err != nil {
		t.Fatal(err)
	}
	createResult := make(chan error, 1)
	go func() {
		_, createErr := manager.Create(context.Background(), CreateCommand{
			Owner:       NewLocalOwnerScope(),
			DisplayName: "blocked create",
			ClientClass: ClientCLI,
			Lifetime:    LifetimeUntilRevoked,
		})
		createResult <- createErr
	}()
	<-repository.started
	shutdownContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.Shutdown(shutdownContext); err != nil {
		t.Fatalf("shutdown ManualCapture manager: %v", err)
	}
	if err := <-createResult; !errors.Is(err, ErrRuntimeStopping) {
		t.Fatalf("canceled create error = %v", err)
	}
	if _, err := manager.Create(context.Background(), CreateCommand{
		Owner:       NewLocalOwnerScope(),
		DisplayName: "after shutdown",
		ClientClass: ClientCLI,
		Lifetime:    LifetimeUntilRevoked,
	}); !errors.Is(err, ErrRuntimeStopping) {
		t.Fatalf("post-shutdown create error = %v", err)
	}
}

func TestManualCaptureTypesRejectRouteAuthorityAndRedactCredentials(t *testing.T) {
	wireCredential, err := capturecredential.New(
		capturecredential.KindManualCapture,
		[]byte(strings.Repeat("c", capturecredential.EntropyBytes)),
	)
	if err != nil {
		t.Fatal(err)
	}
	credentialValue := wireCredential.Value()
	credential, err := NewProxyCredential(credentialValue)
	if err != nil {
		t.Fatal(err)
	}
	managedCredential, err := capturecredential.New(
		capturecredential.KindManagedRun,
		[]byte(strings.Repeat("r", capturecredential.EntropyBytes)),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewProxyCredential(managedCredential.Value()); !errors.Is(err, ErrCredentialRejected) {
		t.Fatalf("managed credential accepted as ManualCapture credential: %v", err)
	}
	if strings.Contains(credential.String(), credentialValue) ||
		strings.Contains(credential.GoString(), credentialValue) {
		t.Fatal("credential formatting exposed the bearer value")
	}
	for _, value := range []any{DurableRecord{}, View{}, Evidence{}} {
		typeOf := reflect.TypeOf(value)
		for _, forbidden := range []string{
			"AccessID", "ProfileID", "RouteID", "AccountID", "Model", "PluginID",
			"MachineID", "WorkspaceID", "ProcessID", "Adapter",
		} {
			if _, exists := typeOf.FieldByName(forbidden); exists {
				t.Fatalf("%s exposes forbidden authority field %s", typeOf, forbidden)
			}
		}
	}
	owner, err := NewProxyClientOwnerScope("binding-one")
	if err != nil || !owner.Valid() {
		t.Fatalf("remote owner=%+v err=%v", owner, err)
	}
	if _, err := NewProxyClientOwnerScope("route/001"); err == nil {
		t.Fatal("route-shaped owner identity was accepted")
	}
}

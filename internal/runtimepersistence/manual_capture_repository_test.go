package runtimepersistence

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/manualcapture"
)

type manualCaptureClock struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *manualCaptureClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *manualCaptureClock) Set(value time.Time) {
	clock.mu.Lock()
	clock.now = value
	clock.mu.Unlock()
}

type manualCaptureRandom struct {
	mu   sync.Mutex
	next byte
}

func (source *manualCaptureRandom) Read(buffer []byte) (int, error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	for index := range buffer {
		source.next++
		buffer[index] = source.next
	}
	return len(buffer), nil
}

func newManualCaptureManager(
	t *testing.T,
	store *Store,
	clock *manualCaptureClock,
	random *manualCaptureRandom,
) *manualcapture.Manager {
	t.Helper()
	options := manualcapture.DefaultOptions(store.ManualCaptureRepository())
	options.Clock = clock
	options.Random = random
	manager, err := manualcapture.NewManager(context.Background(), options)
	if err != nil {
		t.Fatalf("start ManualCapture manager: %v", err)
	}
	return manager
}

func TestManualCaptureCreateRotateAuthorizeRevokeAndReopen(t *testing.T) {
	t.Parallel()
	databasePath := filepath.Join(t.TempDir(), "data", "runtime.db")
	clock := &manualCaptureClock{now: time.Date(2026, 8, 4, 10, 0, 0, 123456789, time.UTC)}
	random := &manualCaptureRandom{}
	owner := manualcapture.NewLocalOwnerScope()

	store := openTestStore(t, databasePath)
	manager := newManualCaptureManager(t, store, clock, random)
	first, err := manager.Create(context.Background(), manualcapture.CreateCommand{
		Owner:       owner,
		DisplayName: "Claude desktop",
		ClientClass: manualcapture.ClientDesktopApp,
		Lifetime:    manualcapture.LifetimeUntilRevoked,
	})
	if err != nil {
		t.Fatalf("create ManualCapture: %v", err)
	}
	if first.Capture.AdmissionRef != "manual-capture/"+first.Capture.ID ||
		first.Capture.CredentialRevision != 1 ||
		first.Capture.Observation != manualcapture.ObservationWaiting {
		t.Fatalf("created ManualCapture = %+v", first.Capture)
	}
	if strings.Contains(first.LogValue().String(), first.Credential.Value()) {
		t.Fatal("ManualCapture structured log exposed its credential")
	}
	var storedHash []byte
	if err := store.database.QueryRowContext(
		context.Background(),
		`SELECT proxy_credential_hash FROM manual_captures WHERE capture_id = ?`,
		first.Capture.ID,
	).Scan(&storedHash); err != nil {
		t.Fatalf("read stored ManualCapture credential hash: %v", err)
	}
	if len(storedHash) != 32 || string(storedHash) == first.Credential.Value() {
		t.Fatal("ManualCapture did not persist a digest-only credential")
	}

	firstEvidence, err := manager.AuthorizeProxy(context.Background(), first.Credential)
	if err != nil || firstEvidence.CredentialRevision != 1 {
		t.Fatalf("authorize first credential evidence=%+v err=%v", firstEvidence, err)
	}
	observed, err := manager.Get(context.Background(), owner, firstEvidence.ManualCaptureID)
	if err != nil || observed.Observation != manualcapture.ObservationObserved ||
		observed.LastObservedAt == nil {
		t.Fatalf("observed ManualCapture = %+v err=%v", observed, err)
	}

	clock.Set(clock.Now().Add(time.Second))
	second, err := manager.Rotate(context.Background(), manualcapture.RotateCommand{
		Owner:                      owner,
		ID:                         firstEvidence.ManualCaptureID,
		ExpectedCredentialRevision: 1,
	})
	if err != nil {
		t.Fatalf("rotate ManualCapture: %v", err)
	}
	if second.Capture.CredentialRevision != 2 ||
		second.Capture.Observation != manualcapture.ObservationWaiting ||
		second.Capture.LastObservedAt != nil ||
		second.Credential.Value() == first.Credential.Value() {
		t.Fatalf("rotated ManualCapture = %+v", second.Capture)
	}
	if _, err := manager.AuthorizeProxy(context.Background(), first.Credential); !errors.Is(
		err,
		manualcapture.ErrCredentialRejected,
	) {
		t.Fatalf("old credential authorization error = %v", err)
	}
	if _, err := manager.Rotate(context.Background(), manualcapture.RotateCommand{
		Owner:                      owner,
		ID:                         firstEvidence.ManualCaptureID,
		ExpectedCredentialRevision: 1,
	}); !errors.Is(err, manualcapture.ErrRevisionConflict) {
		t.Fatalf("stale rotation error = %v", err)
	}
	if _, err := manager.AuthorizeProxy(context.Background(), second.Credential); err != nil {
		t.Fatalf("authorize rotated credential: %v", err)
	}

	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown first ManualCapture manager: %v", err)
	}
	shutdownTestStore(t, store)

	reopened := openTestStore(t, databasePath)
	defer shutdownTestStore(t, reopened)
	recovered := newManualCaptureManager(t, reopened, clock, random)
	defer func() { _ = recovered.Shutdown(context.Background()) }()
	if recovered.Recovery().ActiveCount != 1 {
		t.Fatalf("ManualCapture recovery = %+v", recovered.Recovery())
	}
	if _, err := recovered.AuthorizeProxy(context.Background(), second.Credential); err != nil {
		t.Fatalf("authorize recovered credential: %v", err)
	}
	revoked, err := recovered.Revoke(context.Background(), manualcapture.RevokeCommand{
		Owner:                      owner,
		ID:                         firstEvidence.ManualCaptureID,
		ExpectedCredentialRevision: 2,
	})
	if err != nil || revoked.State != manualcapture.StateRevoked {
		t.Fatalf("revoke ManualCapture view=%+v err=%v", revoked, err)
	}
	if _, err := recovered.Revoke(context.Background(), manualcapture.RevokeCommand{
		Owner:                      owner,
		ID:                         firstEvidence.ManualCaptureID,
		ExpectedCredentialRevision: 2,
	}); err != nil {
		t.Fatalf("idempotent revoke: %v", err)
	}
	if _, err := recovered.AuthorizeProxy(context.Background(), second.Credential); !errors.Is(
		err,
		manualcapture.ErrCredentialRejected,
	) {
		t.Fatalf("revoked credential authorization error = %v", err)
	}
}

func TestManualCaptureRecoveryExpiresTemporaryAndKeepsOwnersIsolated(t *testing.T) {
	t.Parallel()
	databasePath := filepath.Join(t.TempDir(), "runtime.db")
	clock := &manualCaptureClock{now: time.Date(2026, 8, 4, 11, 0, 0, 0, time.UTC)}
	random := &manualCaptureRandom{}
	localOwner := manualcapture.NewLocalOwnerScope()
	remoteOwner, err := manualcapture.NewProxyClientOwnerScope("binding-one")
	if err != nil {
		t.Fatal(err)
	}

	store := openTestStore(t, databasePath)
	manager := newManualCaptureManager(t, store, clock, random)
	temporary, err := manager.Create(context.Background(), manualcapture.CreateCommand{
		Owner:       localOwner,
		DisplayName: "temporary CLI",
		ClientClass: manualcapture.ClientCLI,
		Lifetime:    manualcapture.LifetimeTemporary,
		ExpiresIn:   time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	remote, err := manager.Create(context.Background(), manualcapture.CreateCommand{
		Owner:       remoteOwner,
		DisplayName: "remote app",
		ClientClass: manualcapture.ClientOther,
		Lifetime:    manualcapture.LifetimeUntilRevoked,
	})
	if err != nil {
		t.Fatal(err)
	}
	remoteID, _ := manualcapture.ParseID(remote.Capture.ID)
	if _, err := manager.Get(context.Background(), localOwner, remoteID); !errors.Is(
		err,
		manualcapture.ErrNotFound,
	) {
		t.Fatalf("foreign owner read error = %v", err)
	}
	localPage, err := manager.List(context.Background(), manualcapture.PageRequest{Owner: localOwner})
	if err != nil || len(localPage.Items) != 1 || localPage.Items[0].ID != temporary.Capture.ID {
		t.Fatalf("local owner page=%+v err=%v", localPage, err)
	}

	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	shutdownTestStore(t, store)
	clock.Set(clock.Now().Add(time.Minute))

	reopened := openTestStore(t, databasePath)
	defer shutdownTestStore(t, reopened)
	recovered := newManualCaptureManager(t, reopened, clock, random)
	defer func() { _ = recovered.Shutdown(context.Background()) }()
	if recovered.Recovery() != (manualcapture.Recovery{ExpiredCount: 1, ActiveCount: 1}) {
		t.Fatalf("recovery = %+v", recovered.Recovery())
	}
	temporaryID, _ := manualcapture.ParseID(temporary.Capture.ID)
	view, err := recovered.Get(context.Background(), localOwner, temporaryID)
	if err != nil || view.State != manualcapture.StateExpired {
		t.Fatalf("expired ManualCapture view=%+v err=%v", view, err)
	}
	if _, err := recovered.AuthorizeProxy(context.Background(), temporary.Credential); !errors.Is(
		err,
		manualcapture.ErrCredentialRejected,
	) {
		t.Fatalf("expired credential authorization error = %v", err)
	}
	fresh, err := recovered.Create(context.Background(), manualcapture.CreateCommand{
		Owner:       localOwner,
		DisplayName: "expires during authorization",
		ClientClass: manualcapture.ClientCLI,
		Lifetime:    manualcapture.LifetimeTemporary,
		ExpiresIn:   time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	clock.Set(clock.Now().Add(time.Minute))
	if _, err := recovered.AuthorizeProxy(context.Background(), fresh.Credential); !errors.Is(
		err,
		manualcapture.ErrCredentialRejected,
	) {
		t.Fatalf("just-expired credential authorization error = %v", err)
	}
	freshID, _ := manualcapture.ParseID(fresh.Capture.ID)
	freshView, err := recovered.Get(context.Background(), localOwner, freshID)
	if err != nil || freshView.State != manualcapture.StateExpired {
		t.Fatalf("authorization expiry was not committed: view=%+v err=%v", freshView, err)
	}
}

func TestManualCaptureOwnerReadCannotExpireAnotherOwnersCapture(t *testing.T) {
	t.Parallel()
	clock := &manualCaptureClock{now: time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)}
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)
	manager := newManualCaptureManager(t, store, clock, &manualCaptureRandom{})
	defer func() { _ = manager.Shutdown(context.Background()) }()
	localOwner := manualcapture.NewLocalOwnerScope()
	remoteOwner, err := manualcapture.NewProxyClientOwnerScope("binding-isolated")
	if err != nil {
		t.Fatal(err)
	}
	local, err := manager.Create(context.Background(), manualcapture.CreateCommand{
		Owner:       localOwner,
		DisplayName: "local temporary",
		ClientClass: manualcapture.ClientCLI,
		Lifetime:    manualcapture.LifetimeTemporary,
		ExpiresIn:   time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	remote, err := manager.Create(context.Background(), manualcapture.CreateCommand{
		Owner:       remoteOwner,
		DisplayName: "remote temporary",
		ClientClass: manualcapture.ClientOther,
		Lifetime:    manualcapture.LifetimeTemporary,
		ExpiresIn:   time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	clock.Set(clock.Now().Add(time.Minute))
	page, err := manager.List(context.Background(), manualcapture.PageRequest{Owner: localOwner})
	if err != nil || len(page.Items) != 1 || page.Items[0].State != manualcapture.StateExpired {
		t.Fatalf("local page=%+v err=%v", page, err)
	}

	var remoteState string
	if err := store.database.QueryRowContext(
		context.Background(),
		`SELECT state FROM manual_captures WHERE capture_id = ?`,
		remote.Capture.ID,
	).Scan(&remoteState); err != nil {
		t.Fatal(err)
	}
	if remoteState != string(manualcapture.StateActive) {
		t.Fatalf("foreign owner state = %q after listing %s", remoteState, local.Capture.ID)
	}
}

func TestManualCaptureConcurrentCASHasOneWinner(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)
	clock := &manualCaptureClock{now: time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)}
	manager := newManualCaptureManager(t, store, clock, &manualCaptureRandom{})
	defer func() { _ = manager.Shutdown(context.Background()) }()
	owner := manualcapture.NewLocalOwnerScope()
	grant, err := manager.Create(context.Background(), manualcapture.CreateCommand{
		Owner:       owner,
		DisplayName: "concurrent",
		ClientClass: manualcapture.ClientCLI,
		Lifetime:    manualcapture.LifetimeUntilRevoked,
	})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := manualcapture.ParseID(grant.Capture.ID)
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, rotateErr := manager.Rotate(context.Background(), manualcapture.RotateCommand{
				Owner:                      owner,
				ID:                         id,
				ExpectedCredentialRevision: 1,
			})
			results <- rotateErr
		}()
	}
	wait.Wait()
	close(results)
	var succeeded, conflicted int
	for result := range results {
		switch {
		case result == nil:
			succeeded++
		case errors.Is(result, manualcapture.ErrRevisionConflict):
			conflicted++
		default:
			t.Fatalf("concurrent rotation error = %v", result)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent rotations succeeded=%d conflicted=%d", succeeded, conflicted)
	}
}

package capturerun_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturecredential"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/evidencearchive"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"github.com/vibe-agi/vibermate/internal/workspaceidentity"
)

func TestCaptureRunCreateHoldsTheArchiveBarrierUntilPersistenceCompletes(
	t *testing.T,
) {
	t.Parallel()

	store := openStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownStore(t, store)
	barrier := &recordingCaptureCreationBarrier{}
	repository := &barrierCheckingCaptureRunRepository{
		Repository: store.CaptureRunRepository(),
		barrier:    barrier,
	}
	options := capturerun.DefaultOptions(repository)
	options.ArchiveBarrier = barrier
	manager, err := capturerun.NewManager(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.Create(context.Background(), capturerun.CreateCommand{
		CWD:                     filepath.Join(t.TempDir(), "workspace"),
		CanonicalExecutablePath: filepath.Join(t.TempDir(), "bin", "codex"),
		ExecutableLabel:         "codex",
		Lifetime:                time.Minute,
		CatalogRevision:         1,
		Workspace:               testWorkspaceScope(t),
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

type barrierCheckingCaptureRunRepository struct {
	capturerun.Repository
	barrier    *recordingCaptureCreationBarrier
	sawBarrier bool
}

func (repository *barrierCheckingCaptureRunRepository) Create(
	ctx context.Context,
	record capturerun.DurableRecord,
) error {
	repository.sawBarrier = repository.barrier.active == 1
	return repository.Repository.Create(ctx, record)
}

func TestCaptureRunPersistsVerifiedAdapterEvidenceWithProxyCapability(
	t *testing.T,
) {
	t.Parallel()

	databasePath := filepath.Join(t.TempDir(), "data", "runtime.db")
	clock := newClock(time.Date(2026, 7, 29, 0, 30, 0, 0, time.UTC))
	firstStore := openStore(t, databasePath)
	first := newManager(t, firstStore, clock)
	adapter := clientadapter.Evidence{
		ID:              "codex-cli",
		Revision:        3,
		Version:         "0.145.0",
		CatalogRevision: 7,
		InstallShape:    clientadapter.InstallNPMWrapperNativeChild,
		ReleaseSHA256:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		LaunchRecipe:    clientadapter.LaunchCodexResponsesHTTP,
		Features:        clientadapter.FeatureResponsesWebSocketHTTPFallback,
	}
	grant, err := first.Create(
		context.Background(),
		capturerun.CreateCommand{
			CWD:                     filepath.Join(t.TempDir(), "workspace"),
			CanonicalExecutablePath: filepath.Join(t.TempDir(), "bin", "codex"),
			ExecutableLabel:         "codex",
			Lifetime:                2 * time.Minute,
			CatalogRevision:         7,
			Adapter:                 &adapter,
			Recognition:             clientadapter.RecognitionVerified,
			Workspace:               testWorkspaceScope(t),
		},
	)
	if err != nil {
		t.Fatalf("create verified CaptureRun: %v", err)
	}
	adapter.Version = "caller-mutated"
	evidence, err := first.AuthorizeProxy(
		context.Background(),
		grant.ProxyCapability,
	)
	if err != nil {
		t.Fatal(err)
	}
	assertCodexEvidence(t, evidence)

	shutdownStore(t, firstStore)
	secondStore := openStore(t, databasePath)
	defer shutdownStore(t, secondStore)
	second := newManager(t, secondStore, clock)
	recovered, err := second.AuthorizeProxy(
		context.Background(),
		grant.ProxyCapability,
	)
	if err != nil {
		t.Fatal(err)
	}
	assertCodexEvidence(t, recovered)
}

func TestCaptureRunCapabilitiesArePersistedAsHashesAndDriveLifecycle(
	t *testing.T,
) {
	t.Parallel()

	databasePath := filepath.Join(t.TempDir(), "data", "runtime.db")
	store := openStore(t, databasePath)
	clock := newClock(time.Date(2026, 7, 29, 1, 2, 3, 0, time.UTC))
	manager := newManager(t, store, clock)
	workspace := testWorkspaceScope(t)

	grant, err := manager.Create(context.Background(), capturerun.CreateCommand{
		CWD:                     filepath.Join(t.TempDir(), "workspace"),
		CanonicalExecutablePath: filepath.Join(t.TempDir(), "bin", "claude"),
		ExecutableLabel:         "claude",
		Lifetime:                2 * time.Minute,
		CatalogRevision:         1,
		Workspace:               workspace,
	})
	if err != nil {
		t.Fatalf("create CaptureRun: %v", err)
	}
	if grant.Run.ID == "" ||
		grant.Run.ExecutableLabel != "claude" ||
		grant.ProxyCapability.Value() == "" ||
		grant.ControlCapability.Value() == "" ||
		grant.ProxyCapability.Value() == grant.ControlCapability.Value() {
		t.Fatalf("incomplete launch grant: %+v", grant.Run)
	}
	encoded, err := json.Marshal(grant)
	if err != nil {
		t.Fatalf("marshal redacted grant: %v", err)
	}
	if bytes.Contains(encoded, []byte(grant.ProxyCapability.Value())) ||
		bytes.Contains(encoded, []byte(grant.ControlCapability.Value())) {
		t.Fatalf("JSON exposed a bearer capability: %s", encoded)
	}

	wrongCredential, err := capturecredential.New(
		capturecredential.KindManagedRun,
		bytes.Repeat([]byte{0x7f}, capturecredential.EntropyBytes),
	)
	if err != nil {
		t.Fatal(err)
	}
	wrong, err := capturerun.NewProxyCapability(wrongCredential.Value())
	if err != nil {
		t.Fatal(err)
	}
	manualCredential, err := capturecredential.New(
		capturecredential.KindManualCapture,
		bytes.Repeat([]byte{0x6f}, capturecredential.EntropyBytes),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := capturerun.NewProxyCapability(manualCredential.Value()); !errors.Is(err, capturerun.ErrCapabilityRejected) {
		t.Fatalf("manual credential accepted as CaptureRun capability: %v", err)
	}
	if _, err := manager.AuthorizeProxy(
		context.Background(),
		wrong,
	); !errors.Is(err, capturerun.ErrCapabilityRejected) {
		t.Fatalf("wrong proxy capability error = %v", err)
	}
	evidence, err := manager.AuthorizeProxy(
		context.Background(),
		grant.ProxyCapability,
	)
	if err != nil {
		t.Fatalf("authorize proxy capability: %v", err)
	}
	if evidence.RunID != grant.Run.ID ||
		evidence.ExecutableLabel != "claude" ||
		evidence.ProcessID != 0 {
		t.Fatalf("proxy evidence = %+v", evidence)
	}
	active, err := store.CaptureRunRepository().Active(
		context.Background(), grant.Run.ID, clock.Now(),
	)
	if err != nil || !active {
		t.Fatalf("created CaptureRun activity = %v, %v", active, err)
	}

	attached, err := manager.Attach(
		context.Background(),
		grant.Run.ID,
		grant.ControlCapability,
		321,
	)
	if err != nil || attached.State != capturerun.StateAttached ||
		attached.ProcessID != 321 {
		t.Fatalf("attach view=%+v err=%v", attached, err)
	}
	clock.Advance(time.Minute)
	heartbeat, err := manager.Heartbeat(
		context.Background(),
		grant.Run.ID,
		grant.ControlCapability,
		3*time.Minute,
	)
	if err != nil {
		t.Fatalf("heartbeat CaptureRun: %v", err)
	}
	if !heartbeat.ExpiresAt.Equal(clock.Now().Add(3 * time.Minute)) {
		t.Fatalf("heartbeat expiry = %s", heartbeat.ExpiresAt)
	}
	if err := manager.Finish(
		context.Background(),
		grant.Run.ID,
		grant.ControlCapability,
	); err != nil {
		t.Fatalf("finish CaptureRun: %v", err)
	}
	if err := manager.Finish(
		context.Background(),
		grant.Run.ID,
		grant.ControlCapability,
	); err != nil {
		t.Fatalf("idempotent finish CaptureRun: %v", err)
	}
	active, err = store.CaptureRunRepository().Active(
		context.Background(), grant.Run.ID, clock.Now(),
	)
	if err != nil || active {
		t.Fatalf("finished CaptureRun activity = %v, %v", active, err)
	}
	if _, err := manager.AuthorizeProxy(
		context.Background(),
		grant.ProxyCapability,
	); !errors.Is(err, capturerun.ErrCapabilityRejected) {
		t.Fatalf("finished run remained authorized: %v", err)
	}

	shutdownStore(t, store)
	for _, path := range []string{
		databasePath,
		databasePath + "-wal",
		databasePath + "-shm",
	} {
		data, readErr := os.ReadFile(path)
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			t.Fatalf("read SQLite artifact: %v", readErr)
		}
		if bytes.Contains(data, []byte(grant.ProxyCapability.Value())) ||
			bytes.Contains(data, []byte(grant.ControlCapability.Value())) {
			t.Fatalf("SQLite artifact %q contains a raw capability", filepath.Base(path))
		}
	}
}

func TestCaptureRunFinishDoesNotCommitWhenEvidenceBarrierFails(t *testing.T) {
	t.Parallel()

	store := openStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownStore(t, store)
	clock := newClock(time.Date(2026, 8, 13, 1, 2, 3, 0, time.UTC))
	barrierErr := errors.New("raw evidence flush failed")
	barrier := &captureRunBarrier{err: barrierErr}
	options := capturerun.DefaultOptions(store.CaptureRunRepository())
	options.Clock = clock
	options.EvidenceBarrier = barrier
	manager, err := capturerun.NewManager(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := manager.Create(context.Background(), capturerun.CreateCommand{
		CWD:                     filepath.Join(t.TempDir(), "workspace"),
		CanonicalExecutablePath: filepath.Join(t.TempDir(), "bin", "claude"),
		ExecutableLabel:         "claude",
		Lifetime:                time.Minute,
		CatalogRevision:         1,
		Workspace:               testWorkspaceScope(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Finish(
		context.Background(),
		grant.Run.ID,
		grant.ControlCapability,
	); !errors.Is(err, barrierErr) {
		t.Fatalf("Finish error = %v", err)
	}
	view, err := manager.GetRun(context.Background(), grant.Run.ID)
	if err != nil || view.State == capturerun.StateFinished ||
		barrier.runID != grant.Run.ID {
		t.Fatalf("view=%+v barrier=%q err=%v", view, barrier.runID, err)
	}
}

type captureRunBarrier struct {
	runID string
	err   error
}

func (barrier *captureRunBarrier) PrepareManagedRun(
	_ context.Context,
	runID string,
) (capturerun.TerminalEvidence, error) {
	barrier.runID = runID
	return captureRunTerminal{}, barrier.err
}

type captureRunTerminal struct{}

func (captureRunTerminal) Commit() {}
func (captureRunTerminal) Abort()  {}

func TestCaptureRunCatalogPaginatesRunningFirstAtSharedTimestamp(t *testing.T) {
	t.Parallel()

	store := openStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownStore(t, store)
	clock := newClock(time.Date(2026, 8, 11, 4, 5, 6, 0, time.UTC))
	manager := newManager(t, store, clock)
	workspace := testWorkspaceScope(t)
	create := func(label string, finish bool) {
		t.Helper()
		grant, err := manager.Create(context.Background(), capturerun.CreateCommand{
			CWD:                     filepath.Join(t.TempDir(), "workspace"),
			CanonicalExecutablePath: filepath.Join(t.TempDir(), "bin", label),
			ExecutableLabel:         label,
			Lifetime:                2 * time.Minute,
			CatalogRevision:         1,
			Workspace:               workspace,
		})
		if err != nil {
			t.Fatalf("create CaptureRun %q: %v", label, err)
		}
		if finish {
			if err := manager.Finish(
				context.Background(),
				grant.Run.ID,
				grant.ControlCapability,
			); err != nil {
				t.Fatalf("finish CaptureRun %q: %v", label, err)
			}
		}
	}
	create("running-a", false)
	create("finished-a", true)
	create("running-b", false)
	create("finished-b", true)

	seen := make(map[string]struct{}, 4)
	var cursor *capturerun.PageCursor
	for index := range 4 {
		page, err := manager.ListRuns(context.Background(), capturerun.PageRequest{
			Limit:  1,
			Cursor: cursor,
		})
		if err != nil {
			t.Fatalf("list CaptureRun page %d: %v", index+1, err)
		}
		if len(page.Items) != 1 {
			t.Fatalf("CaptureRun page %d = %+v", index+1, page.Items)
		}
		item := page.Items[0]
		if _, duplicate := seen[item.ID]; duplicate {
			t.Fatalf("CaptureRun %q appeared on multiple pages", item.ID)
		}
		seen[item.ID] = struct{}{}
		running := item.State == capturerun.StateCreated || item.State == capturerun.StateAttached
		if wantRunning := index < 2; running != wantRunning {
			t.Fatalf("CaptureRun page %d state = %q", index+1, item.State)
		}
		cursor = &capturerun.PageCursor{
			Running:            running,
			UpdatedAt:          item.UpdatedAt,
			AfterID:            item.ID,
			IncludeAtUpdatedAt: true,
		}
	}
	page, err := manager.ListRuns(context.Background(), capturerun.PageRequest{
		Limit:  1,
		Cursor: cursor,
	})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("CaptureRun terminal page = %+v, %v", page.Items, err)
	}
}

func TestCaptureRunCatalogReconcilesExpiredLeaseBeforeListing(t *testing.T) {
	t.Parallel()

	store := openStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownStore(t, store)
	clock := newClock(time.Date(2026, 8, 13, 4, 5, 6, 0, time.UTC))
	manager := newManager(t, store, clock)
	grant, err := manager.Create(context.Background(), capturerun.CreateCommand{
		CWD:                     filepath.Join(t.TempDir(), "workspace"),
		CanonicalExecutablePath: filepath.Join(t.TempDir(), "bin", "claude"),
		ExecutableLabel:         "claude",
		Lifetime:                time.Minute,
		CatalogRevision:         1,
		Workspace:               testWorkspaceScope(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	if grant.Run.State != capturerun.StateCreated {
		t.Fatalf("new CaptureRun state = %q", grant.Run.State)
	}

	clock.Advance(2 * time.Minute)
	page, err := manager.ListRuns(
		context.Background(),
		capturerun.PageRequest{Limit: 10},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != grant.Run.ID ||
		page.Items[0].State != capturerun.StateExpired {
		t.Fatalf("reconciled CaptureRun page = %+v", page.Items)
	}
}

func TestCaptureRunRestartRecoveryRetainsOnlyFreshCapabilityHashes(
	t *testing.T,
) {
	t.Parallel()

	databasePath := filepath.Join(t.TempDir(), "data", "runtime.db")
	clock := newClock(time.Date(2026, 7, 29, 2, 0, 0, 0, time.UTC))
	firstStore := openStore(t, databasePath)
	first := newManager(t, firstStore, clock)
	grant, err := first.Create(context.Background(), capturerun.CreateCommand{
		CWD:                     filepath.Join(t.TempDir(), "workspace"),
		CanonicalExecutablePath: filepath.Join(t.TempDir(), "bin", "claude"),
		ExecutableLabel:         "claude",
		Lifetime:                90 * time.Second,
		CatalogRevision:         1,
		Workspace:               testWorkspaceScope(t),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Omitting Manager.Shutdown models abrupt process loss at the persistence
	// boundary. The database close is orderly, so this does not claim power-loss
	// or filesystem durability evidence.
	shutdownStore(t, firstStore)
	secondStore := openStore(t, databasePath)
	second := newManager(t, secondStore, clock)
	if recovery := second.Recovery(); recovery.ActiveCount != 1 ||
		recovery.ExpiredCount != 0 {
		t.Fatalf("fresh recovery = %+v", recovery)
	}
	if _, err := second.AuthorizeProxy(
		context.Background(),
		grant.ProxyCapability,
	); err != nil {
		t.Fatalf("recovered fresh capability: %v", err)
	}
	shutdownStore(t, secondStore)

	clock.Advance(2 * time.Minute)
	thirdStore := openStore(t, databasePath)
	defer shutdownStore(t, thirdStore)
	third := newManager(t, thirdStore, clock)
	if recovery := third.Recovery(); recovery.ActiveCount != 0 ||
		recovery.ExpiredCount != 1 {
		t.Fatalf("expired recovery = %+v", recovery)
	}
	if _, err := third.AuthorizeProxy(
		context.Background(),
		grant.ProxyCapability,
	); !errors.Is(err, capturerun.ErrCapabilityRejected) {
		t.Fatalf("expired recovered capability error = %v", err)
	}
}

func TestCaptureRunShutdownRevokesBeforeSQLiteClose(t *testing.T) {
	t.Parallel()

	databasePath := filepath.Join(t.TempDir(), "data", "runtime.db")
	clock := newClock(time.Date(2026, 7, 29, 3, 0, 0, 0, time.UTC))
	store := openStore(t, databasePath)
	manager := newManager(t, store, clock)
	grant, err := manager.Create(context.Background(), capturerun.CreateCommand{
		CWD:                     filepath.Join(t.TempDir(), "workspace"),
		CanonicalExecutablePath: "/usr/bin/true",
		ExecutableLabel:         "true",
		Lifetime:                time.Minute,
		CatalogRevision:         1,
		Workspace:               testWorkspaceScope(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown CaptureRun manager: %v", err)
	}
	if _, err := manager.AuthorizeProxy(
		context.Background(),
		grant.ProxyCapability,
	); !errors.Is(err, capturerun.ErrRuntimeStopping) {
		t.Fatalf("stopped Manager authorization error = %v", err)
	}
	shutdownStore(t, store)

	reopened := openStore(t, databasePath)
	defer shutdownStore(t, reopened)
	recovered := newManager(t, reopened, clock)
	if recovery := recovered.Recovery(); recovery.ActiveCount != 0 {
		t.Fatalf("revoked recovery = %+v", recovery)
	}
	if _, err := recovered.AuthorizeProxy(
		context.Background(),
		grant.ProxyCapability,
	); !errors.Is(err, capturerun.ErrCapabilityRejected) {
		t.Fatalf("revoked capability after reopen error = %v", err)
	}
}

func assertCodexEvidence(
	t *testing.T,
	evidence capturerun.Evidence,
) {
	t.Helper()

	if evidence.CatalogRevision != 7 ||
		evidence.Adapter == nil ||
		evidence.Adapter.ID != "codex-cli" ||
		evidence.Adapter.Version != "0.145.0" ||
		evidence.Adapter.CatalogRevision != evidence.CatalogRevision ||
		!evidence.Adapter.Supports(
			clientadapter.FeatureResponsesWebSocketHTTPFallback,
		) {
		t.Fatalf("CaptureRun adapter evidence = %+v", evidence)
	}
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock(now time.Time) *fakeClock {
	return &fakeClock{now: now}
}

func (clock *fakeClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *fakeClock) Advance(duration time.Duration) {
	clock.mu.Lock()
	clock.now = clock.now.Add(duration)
	clock.mu.Unlock()
}

func openStore(t *testing.T, databasePath string) *runtimepersistence.Store {
	t.Helper()
	store, err := runtimepersistence.Open(context.Background(), runtimepersistence.Options{
		DatabasePath:           databasePath,
		BusyTimeout:            runtimepersistence.DefaultBusyTimeout,
		CommitReconcileTimeout: runtimepersistence.DefaultCommitReconcileTimeout,
	})
	if err != nil {
		t.Fatalf("open runtime store: %v", err)
	}
	return store
}

func newManager(
	t *testing.T,
	store *runtimepersistence.Store,
	clock capturerun.Clock,
) *capturerun.Manager {
	t.Helper()
	options := capturerun.DefaultOptions(store.CaptureRunRepository())
	options.Clock = clock
	manager, err := capturerun.NewManager(context.Background(), options)
	if err != nil {
		t.Fatalf("open CaptureRun manager: %v", err)
	}
	return manager
}

func shutdownStore(t *testing.T, store *runtimepersistence.Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := store.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown store: %v", err)
	}
}

func testWorkspaceScope(t *testing.T) workspaceidentity.Scope {
	t.Helper()
	machineID, err := workspaceidentity.ParseMachineID(
		base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x71}, 32)),
	)
	if err != nil {
		t.Fatal(err)
	}
	workspaceID, err := workspaceidentity.ParseWorkspaceID(
		base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x72}, 32)),
	)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := workspaceidentity.NewScope(
		machineID,
		workspaceID,
		"workspace",
		workspaceidentity.EvidenceLocalLauncher,
		1,
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	return scope
}

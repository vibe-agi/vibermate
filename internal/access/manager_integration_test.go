package access_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
)

func TestAccessCASPublishesImmutableMonotonicSnapshotsAndRestores(t *testing.T) {
	t.Parallel()

	databasePath := filepath.Join(t.TempDir(), "data", "runtime.db")
	store := openStore(t, databasePath)
	projection := access.NewSnapshotProjection()
	manager := newManager(t, store, projection)
	accessID := newAccessID(t, "access-primary")

	if _, err := manager.ResolveAccess(accessID); !errors.Is(
		err,
		access.ErrAccessNotConfigured,
	) {
		t.Fatalf("resolve absent Access: %v", err)
	} else {
		assertFailureCode(t, err, access.ReasonAccessNotConfigured)
	}

	missingUpdate, missingUpdateErr := manager.WriteAccess(
		context.Background(),
		access.WriteCommand{
			AccessID:         accessID,
			ExpectedRevision: 1,
			Binding:          access.Binding{Name: "Missing"},
		},
	)
	if missingUpdate.Outcome != access.WriteOutcomeNotCommitted ||
		!errors.Is(missingUpdateErr, access.ErrAccessNotConfigured) {
		t.Fatalf("missing update result=%+v err=%v", missingUpdate, missingUpdateErr)
	}
	assertFailureCode(t, missingUpdateErr, access.ReasonAccessNotConfigured)

	cancelledContext, cancelWrite := context.WithCancel(context.Background())
	cancelWrite()
	cancelled, cancelledErr := manager.WriteAccess(
		cancelledContext,
		access.WriteCommand{
			AccessID:         accessID,
			ExpectedRevision: 0,
			Binding:          access.Binding{Name: "Cancelled"},
		},
	)
	if cancelled.Outcome != access.WriteOutcomeNotCommitted ||
		!errors.Is(cancelledErr, context.Canceled) {
		t.Fatalf("pre-commit cancellation result=%+v err=%v", cancelled, cancelledErr)
	}
	assertFailureCode(t, cancelledErr, access.ReasonWriteNotCommitted)
	if _, err := manager.ResolveAccess(accessID); !errors.Is(
		err,
		access.ErrAccessNotConfigured,
	) {
		t.Fatalf("pre-commit cancellation published a snapshot: %v", err)
	}

	create := access.WriteCommand{
		AccessID:         accessID,
		ExpectedRevision: 0,
		Binding: access.Binding{
			Name:        "Primary",
			Description: "Initial description",
		},
	}
	created, err := manager.WriteAccess(context.Background(), create)
	if err != nil {
		t.Fatalf("create Access: %v", err)
	}
	if created.Outcome != access.WriteOutcomeCommitted || created.Snapshot.Revision() != 1 {
		t.Fatalf("create result = %+v", created)
	}

	create.Binding.Name = "Mutated input"
	create.Binding.Description = "Mutated input description"
	if got := created.Snapshot.Binding().Name; got != "Primary" {
		t.Fatalf("input alias changed published snapshot: %q", got)
	}
	output := created.Snapshot.Binding()
	output.Name = "Mutated output"
	output.Description = "Mutated output description"
	if got := created.Snapshot.Binding().Name; got != "Primary" {
		t.Fatalf("output alias changed published snapshot: %q", got)
	}

	oldHandle, err := manager.ResolveAccess(accessID)
	if err != nil {
		t.Fatalf("resolve first snapshot: %v", err)
	}
	updated, err := manager.WriteAccess(context.Background(), access.WriteCommand{
		AccessID:         accessID,
		ExpectedRevision: 1,
		Binding: access.Binding{
			Name:        "Primary updated",
			Description: "Second revision",
		},
	})
	if err != nil {
		t.Fatalf("update Access: %v", err)
	}
	if updated.Snapshot.Revision() != 2 {
		t.Fatalf("updated revision = %d, want 2", updated.Snapshot.Revision())
	}
	if oldHandle.Revision() != 1 || oldHandle.Binding().Name != "Primary" {
		t.Fatalf("old snapshot handle changed: revision=%d binding=%+v",
			oldHandle.Revision(), oldHandle.Binding())
	}
	newHandle, err := manager.ResolveAccess(accessID)
	if err != nil {
		t.Fatalf("resolve second snapshot: %v", err)
	}
	if newHandle.Revision() != 2 || newHandle.Binding().Name != "Primary updated" {
		t.Fatalf("new snapshot = revision:%d binding:%+v",
			newHandle.Revision(), newHandle.Binding())
	}
	if err := projection.Publish(oldHandle); !errors.Is(
		err,
		access.ErrPublishedRevisionRegression,
	) {
		t.Fatalf("projection accepted a regressing revision: %v", err)
	}
	afterRegressionAttempt, err := manager.ResolveAccess(accessID)
	if err != nil {
		t.Fatalf("resolve after regression attempt: %v", err)
	}
	if afterRegressionAttempt != newHandle {
		t.Fatalf("regression attempt changed snapshot: before=%+v after=%+v",
			newHandle, afterRegressionAttempt)
	}

	stale, staleErr := manager.WriteAccess(context.Background(), access.WriteCommand{
		AccessID:         accessID,
		ExpectedRevision: 1,
		Binding:          access.Binding{Name: "Stale"},
	})
	if stale.Outcome != access.WriteOutcomeNotCommitted ||
		!errors.Is(staleErr, access.ErrRevisionConflict) {
		t.Fatalf("stale CAS result=%+v err=%v", stale, staleErr)
	}
	var failure *access.Failure
	if !errors.As(staleErr, &failure) || failure.ActualRevision != 2 {
		t.Fatalf("stale CAS actual revision: %v", staleErr)
	}
	afterStale, err := manager.ResolveAccess(accessID)
	if err != nil {
		t.Fatalf("resolve after stale CAS: %v", err)
	}
	if afterStale != newHandle {
		t.Fatalf("stale CAS changed snapshot: before=%+v after=%+v", newHandle, afterStale)
	}

	shutdownManager(t, manager)
	shutdownStore(t, store)

	reopened := openStore(t, databasePath)
	defer shutdownStore(t, reopened)
	recovered := newManager(t, reopened, access.NewSnapshotProjection())
	defer shutdownManager(t, recovered)
	recoveredSnapshot, err := recovered.ResolveAccess(accessID)
	if err != nil {
		t.Fatalf("resolve recovered Access: %v", err)
	}
	if recoveredSnapshot != newHandle {
		t.Fatalf("recovered snapshot=%+v, want %+v", recoveredSnapshot, newHandle)
	}
}

func TestAccessConcurrentCreateCASAllowsOneWriter(t *testing.T) {
	t.Parallel()

	store := openStore(t, filepath.Join(t.TempDir(), "data", "runtime.db"))
	defer shutdownStore(t, store)
	manager := newManager(t, store, access.NewSnapshotProjection())
	defer shutdownManager(t, manager)
	accessID := newAccessID(t, "access-concurrent")

	type writeResponse struct {
		result access.WriteResult
		err    error
	}
	responses := make(chan writeResponse, 2)
	start := make(chan struct{})
	for _, name := range []string{"Writer A", "Writer B"} {
		name := name
		go func() {
			<-start
			result, err := manager.WriteAccess(context.Background(), access.WriteCommand{
				AccessID:         accessID,
				ExpectedRevision: 0,
				Binding:          access.Binding{Name: name},
			})
			responses <- writeResponse{result: result, err: err}
		}()
	}
	close(start)

	var committed, conflicts int
	for range 2 {
		response := <-responses
		switch {
		case response.err == nil &&
			response.result.Outcome == access.WriteOutcomeCommitted:
			committed++
		case errors.Is(response.err, access.ErrRevisionConflict) &&
			response.result.Outcome == access.WriteOutcomeNotCommitted:
			conflicts++
		default:
			t.Fatalf("unexpected concurrent CAS result=%+v err=%v",
				response.result, response.err)
		}
	}
	if committed != 1 || conflicts != 1 {
		t.Fatalf("concurrent results: committed=%d conflicts=%d", committed, conflicts)
	}
	snapshot, err := manager.ResolveAccess(accessID)
	if err != nil {
		t.Fatalf("resolve concurrent result: %v", err)
	}
	if snapshot.Revision() != 1 {
		t.Fatalf("concurrent revision = %d, want 1", snapshot.Revision())
	}
}

func TestAccessIDsAndRevisionsAreAggregateLocal(t *testing.T) {
	t.Parallel()

	store := openStore(t, filepath.Join(t.TempDir(), "data", "runtime.db"))
	defer shutdownStore(t, store)
	manager := newManager(t, store, access.NewSnapshotProjection())
	defer shutdownManager(t, manager)
	firstID := newAccessID(t, "access-one")
	secondID := newAccessID(t, "access-two")

	for _, command := range []access.WriteCommand{
		{
			AccessID:         firstID,
			ExpectedRevision: 0,
			Binding:          access.Binding{Name: "First"},
		},
		{
			AccessID:         secondID,
			ExpectedRevision: 0,
			Binding:          access.Binding{Name: "Second"},
		},
	} {
		result, err := manager.WriteAccess(context.Background(), command)
		if err != nil || result.Snapshot.Revision() != 1 {
			t.Fatalf("create aggregate-local Access result=%+v err=%v", result, err)
		}
	}
	if _, err := manager.WriteAccess(context.Background(), access.WriteCommand{
		AccessID:         firstID,
		ExpectedRevision: 1,
		Binding:          access.Binding{Name: "First revision two"},
	}); err != nil {
		t.Fatalf("update first Access: %v", err)
	}

	first, err := manager.ResolveAccess(firstID)
	if err != nil {
		t.Fatalf("resolve first Access: %v", err)
	}
	second, err := manager.ResolveAccess(secondID)
	if err != nil {
		t.Fatalf("resolve second Access: %v", err)
	}
	if first.Revision() != 2 || second.Revision() != 1 {
		t.Fatalf("aggregate revisions: first=%d second=%d",
			first.Revision(), second.Revision())
	}
	records, err := store.AccessRepository().LoadAll(context.Background())
	if err != nil {
		t.Fatalf("load aggregate-local records: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("durable Access count = %d, want 2", len(records))
	}
}

func TestAccessConcurrentResolversObserveCompleteSnapshots(t *testing.T) {
	t.Parallel()

	store := openStore(t, filepath.Join(t.TempDir(), "data", "runtime.db"))
	defer shutdownStore(t, store)
	manager := newManager(t, store, access.NewSnapshotProjection())
	defer shutdownManager(t, manager)
	accessID := newAccessID(t, "access-reader-race")

	if _, err := manager.WriteAccess(context.Background(), access.WriteCommand{
		AccessID:         accessID,
		ExpectedRevision: 0,
		Binding:          access.Binding{Name: "Revision 1"},
	}); err != nil {
		t.Fatalf("create Access: %v", err)
	}

	const readers = 8
	done := make(chan struct{})
	readerErrors := make(chan error, readers)
	var waitGroup sync.WaitGroup
	for range readers {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				snapshot, err := manager.ResolveAccess(accessID)
				if err != nil {
					readerErrors <- err
					return
				}
				wantName := fmt.Sprintf("Revision %d", snapshot.Revision())
				if snapshot.Binding().Name != wantName {
					readerErrors <- fmt.Errorf(
						"incomplete snapshot: revision=%d name=%q",
						snapshot.Revision(),
						snapshot.Binding().Name,
					)
					return
				}
			}
		}()
	}

	for revision := access.Revision(2); revision <= 50; revision++ {
		if _, err := manager.WriteAccess(context.Background(), access.WriteCommand{
			AccessID:         accessID,
			ExpectedRevision: revision - 1,
			Binding: access.Binding{
				Name: fmt.Sprintf("Revision %d", revision),
			},
		}); err != nil {
			close(done)
			waitGroup.Wait()
			t.Fatalf("write revision %d: %v", revision, err)
		}
	}
	close(done)
	waitGroup.Wait()
	close(readerErrors)
	for err := range readerErrors {
		t.Errorf("concurrent resolver: %v", err)
	}
	final, err := manager.ResolveAccess(accessID)
	if err != nil {
		t.Fatalf("resolve final snapshot: %v", err)
	}
	if final.Revision() != 50 || final.Binding().Name != "Revision 50" {
		t.Fatalf("final snapshot: revision=%d binding=%+v",
			final.Revision(), final.Binding())
	}
}

func TestAccessWriterSerializesCommitThroughPublication(t *testing.T) {
	t.Parallel()

	store := openStore(t, filepath.Join(t.TempDir(), "data", "runtime.db"))
	defer shutdownStore(t, store)
	projection := newBlockingProjection(1)
	manager := newManager(t, store, projection)
	defer shutdownManager(t, manager)
	accessID := newAccessID(t, "access-linearized")

	firstContext, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	type writeResponse struct {
		result access.WriteResult
		err    error
	}
	firstDone := make(chan writeResponse, 1)
	go func() {
		result, err := manager.WriteAccess(firstContext, access.WriteCommand{
			AccessID:         accessID,
			ExpectedRevision: 0,
			Binding:          access.Binding{Name: "Revision one"},
		})
		firstDone <- writeResponse{result: result, err: err}
	}()

	select {
	case <-projection.entered:
	case <-time.After(time.Second):
		t.Fatal("first writer did not reach post-commit publication")
	}
	records, err := store.AccessRepository().LoadAll(context.Background())
	if err != nil {
		t.Fatalf("read durable state in commit-to-publish window: %v", err)
	}
	if len(records) != 1 || records[0].Revision != 1 {
		t.Fatalf("durable state before publication: %+v", records)
	}

	cancelFirst()
	secondDone := make(chan writeResponse, 1)
	secondStarted := make(chan struct{})
	go func() {
		close(secondStarted)
		result, err := manager.WriteAccess(context.Background(), access.WriteCommand{
			AccessID:         accessID,
			ExpectedRevision: 1,
			Binding:          access.Binding{Name: "Revision two"},
		})
		secondDone <- writeResponse{result: result, err: err}
	}()
	<-secondStarted
	select {
	case response := <-secondDone:
		t.Fatalf("second writer crossed unpublished first commit: %+v %v",
			response.result, response.err)
	case <-time.After(40 * time.Millisecond):
	}

	close(projection.release)
	first := <-firstDone
	if first.err != nil || first.result.Outcome != access.WriteOutcomeCommitted {
		t.Fatalf("post-commit cancellation result=%+v err=%v", first.result, first.err)
	}
	second := <-secondDone
	if second.err != nil || second.result.Snapshot.Revision() != 2 {
		t.Fatalf("second write result=%+v err=%v", second.result, second.err)
	}

	if revisions := projection.publishedRevisions(); !equalRevisions(
		revisions,
		[]access.Revision{1, 2},
	) {
		t.Fatalf("published revisions = %v, want [1 2]", revisions)
	}
	snapshot, err := manager.ResolveAccess(accessID)
	if err != nil {
		t.Fatalf("resolve serialized result: %v", err)
	}
	if snapshot.Revision() != 2 {
		t.Fatalf("published revision regressed to %d", snapshot.Revision())
	}
}

func TestAccessCloseAndReopenRecoversCommitWithoutProcessPublication(t *testing.T) {
	t.Parallel()

	databasePath := filepath.Join(t.TempDir(), "data", "runtime.db")
	store := openStore(t, databasePath)
	accessID := newAccessID(t, "access-unpublished-reopen")
	candidate := access.Record{
		AccessID: accessID,
		Revision: 1,
		Binding:  access.Binding{Name: "Durable before publication"},
	}
	result, err := store.AccessRepository().CompareAndSwap(
		context.Background(),
		access.Mutation{ExpectedRevision: 0, Candidate: candidate},
	)
	if err != nil || result.Outcome != access.CommitOutcomeCommitted {
		t.Fatalf("commit without projection result=%+v err=%v", result, err)
	}
	shutdownStore(t, store)

	reopened := openStore(t, databasePath)
	defer shutdownStore(t, reopened)
	manager := newManager(t, reopened, access.NewSnapshotProjection())
	defer shutdownManager(t, manager)
	snapshot, err := manager.ResolveAccess(accessID)
	if err != nil {
		t.Fatalf("recover committed Access: %v", err)
	}
	if snapshot.Revision() != 1 ||
		snapshot.Binding().Name != "Durable before publication" {
		t.Fatalf("recovered snapshot: revision=%d binding=%+v",
			snapshot.Revision(), snapshot.Binding())
	}
}

func TestAccessPublishFailurePoisonsOnlyAffectedProjectionUntilRestart(
	t *testing.T,
) {
	t.Parallel()

	databasePath := filepath.Join(t.TempDir(), "data", "runtime.db")
	store := openStore(t, databasePath)
	projection := newFailingProjection(2)
	manager := newManager(t, store, projection)
	accessID := newAccessID(t, "access-publication-failure")

	created, err := manager.WriteAccess(context.Background(), access.WriteCommand{
		AccessID:         accessID,
		ExpectedRevision: 0,
		Binding:          access.Binding{Name: "Revision one"},
	})
	if err != nil || created.Outcome != access.WriteOutcomeCommitted {
		t.Fatalf("create Access result=%+v err=%v", created, err)
	}
	oldHandle, err := manager.ResolveAccess(accessID)
	if err != nil {
		t.Fatalf("resolve first revision: %v", err)
	}

	updated, updateErr := manager.WriteAccess(
		context.Background(),
		access.WriteCommand{
			AccessID:         accessID,
			ExpectedRevision: 1,
			Binding:          access.Binding{Name: "Revision two"},
		},
	)
	if updated.Outcome != access.WriteOutcomeCommitted ||
		updated.Snapshot.Revision() != 2 ||
		!errors.Is(updateErr, access.ErrProjectionUnavailable) ||
		!errors.Is(updateErr, errInjectedPublication) {
		t.Fatalf("publication failure result=%+v err=%v", updated, updateErr)
	}
	assertFailureCode(t, updateErr, access.ReasonProjectionUnavailable)
	if oldHandle.Revision() != 1 ||
		oldHandle.Binding().Name != "Revision one" {
		t.Fatalf("publication failure changed old handle: revision=%d binding=%+v",
			oldHandle.Revision(), oldHandle.Binding())
	}
	if _, err := manager.ResolveAccess(accessID); !errors.Is(
		err,
		access.ErrProjectionUnavailable,
	) {
		t.Fatalf("poisoned projection served a stale snapshot: %v", err)
	} else {
		assertFailureCode(t, err, access.ReasonProjectionUnavailable)
	}
	health := manager.ProjectionHealth()
	if health.State != access.ProjectionStateUnavailable ||
		health.UnavailableAccessCount != 1 {
		t.Fatalf("poisoned projection health = %+v", health)
	}

	retry, retryErr := manager.WriteAccess(
		context.Background(),
		access.WriteCommand{
			AccessID:         accessID,
			ExpectedRevision: 2,
			Binding:          access.Binding{Name: "Ambiguous retry"},
		},
	)
	if retry.Outcome != access.WriteOutcomeNotCommitted ||
		!errors.Is(retryErr, access.ErrProjectionUnavailable) {
		t.Fatalf("poisoned projection accepted a write result=%+v err=%v",
			retry, retryErr)
	}
	assertFailureCode(t, retryErr, access.ReasonProjectionUnavailable)

	otherID := newAccessID(t, "access-still-available")
	other, otherErr := manager.WriteAccess(
		context.Background(),
		access.WriteCommand{
			AccessID:         otherID,
			ExpectedRevision: 0,
			Binding:          access.Binding{Name: "Independent Access"},
		},
	)
	if otherErr != nil || other.Outcome != access.WriteOutcomeCommitted {
		t.Fatalf("unaffected Access write result=%+v err=%v", other, otherErr)
	}

	records, err := store.AccessRepository().LoadAll(context.Background())
	if err != nil {
		t.Fatalf("load durable state after publication failure: %v", err)
	}
	if len(records) != 2 ||
		records[0].AccessID != accessID ||
		records[0].Revision != 2 ||
		records[0].Binding.Name != "Revision two" {
		t.Fatalf("durable state after publication failure = %+v", records)
	}

	shutdownManager(t, manager)
	shutdownStore(t, store)

	reopened := openStore(t, databasePath)
	defer shutdownStore(t, reopened)
	recovered := newManager(t, reopened, access.NewSnapshotProjection())
	defer shutdownManager(t, recovered)
	recoveredSnapshot, err := recovered.ResolveAccess(accessID)
	if err != nil {
		t.Fatalf("resolve recovered publication failure: %v", err)
	}
	if recoveredSnapshot.Revision() != 2 ||
		recoveredSnapshot.Binding().Name != "Revision two" {
		t.Fatalf("recovered publication failure snapshot: revision=%d binding=%+v",
			recoveredSnapshot.Revision(), recoveredSnapshot.Binding())
	}
	if health := recovered.ProjectionHealth(); health.State != access.ProjectionStateHealthy ||
		health.UnavailableAccessCount != 0 {
		t.Fatalf("recovered projection health = %+v", health)
	}
}

func TestAccessShutdownBoundsBlockedPostCommitPublicationAndRejectsWrites(t *testing.T) {
	t.Parallel()

	store := openStore(t, filepath.Join(t.TempDir(), "data", "runtime.db"))
	defer shutdownStore(t, store)
	projection := newBlockingProjection(1)
	manager := newManager(t, store, projection)
	accessID := newAccessID(t, "access-shutdown")

	writeDone := make(chan error, 1)
	go func() {
		_, err := manager.WriteAccess(context.Background(), access.WriteCommand{
			AccessID:         accessID,
			ExpectedRevision: 0,
			Binding:          access.Binding{Name: "Committed"},
		})
		writeDone <- err
	}()
	select {
	case <-projection.entered:
	case <-time.After(time.Second):
		t.Fatal("writer did not reach blocked publication")
	}

	shutdownContext, cancelShutdown := context.WithTimeout(
		context.Background(),
		40*time.Millisecond,
	)
	shutdownErr := manager.Shutdown(shutdownContext)
	cancelShutdown()
	if !errors.Is(shutdownErr, context.DeadlineExceeded) {
		t.Fatalf("blocked Access shutdown error = %v", shutdownErr)
	}
	if _, err := manager.WriteAccess(context.Background(), access.WriteCommand{
		AccessID:         newAccessID(t, "access-rejected"),
		ExpectedRevision: 0,
		Binding:          access.Binding{Name: "Rejected"},
	}); !errors.Is(err, access.ErrAccessRuntimeStopping) {
		t.Fatalf("Access shutdown accepted a new write: %v", err)
	}

	close(projection.release)
	if err := <-writeDone; err != nil {
		t.Fatalf("committed write failed after publication release: %v", err)
	}
	retryContext, cancelRetry := context.WithTimeout(context.Background(), time.Second)
	defer cancelRetry()
	if err := manager.Shutdown(retryContext); err != nil {
		t.Fatalf("retry Access shutdown: %v", err)
	}
}

type blockingProjection struct {
	delegate      access.SnapshotProjection
	blockRevision access.Revision
	entered       chan struct{}
	release       chan struct{}
	enterOnce     sync.Once

	mu        sync.Mutex
	published []access.Revision
}

func newBlockingProjection(blockRevision access.Revision) *blockingProjection {
	return &blockingProjection{
		delegate:      access.NewSnapshotProjection(),
		blockRevision: blockRevision,
		entered:       make(chan struct{}),
		release:       make(chan struct{}),
	}
}

func (p *blockingProjection) Restore(snapshots []access.Snapshot) error {
	return p.delegate.Restore(snapshots)
}

func (p *blockingProjection) Publish(snapshot access.Snapshot) error {
	if snapshot.Revision() == p.blockRevision {
		p.enterOnce.Do(func() {
			close(p.entered)
		})
		<-p.release
	}
	if err := p.delegate.Publish(snapshot); err != nil {
		return err
	}
	p.mu.Lock()
	p.published = append(p.published, snapshot.Revision())
	p.mu.Unlock()
	return nil
}

func (p *blockingProjection) ResolveAccess(
	accessID access.AccessID,
) (access.Snapshot, error) {
	return p.delegate.ResolveAccess(accessID)
}

func (p *blockingProjection) MarkUnavailable(accessID access.AccessID) {
	p.delegate.MarkUnavailable(accessID)
}

func (p *blockingProjection) ProjectionHealth() access.ProjectionHealth {
	return p.delegate.ProjectionHealth()
}

func (p *blockingProjection) publishedRevisions() []access.Revision {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]access.Revision(nil), p.published...)
}

var errInjectedPublication = errors.New("injected snapshot publication error")

type failingProjection struct {
	delegate     access.SnapshotProjection
	failRevision access.Revision
}

func newFailingProjection(failRevision access.Revision) *failingProjection {
	return &failingProjection{
		delegate:     access.NewSnapshotProjection(),
		failRevision: failRevision,
	}
}

func (p *failingProjection) Restore(snapshots []access.Snapshot) error {
	return p.delegate.Restore(snapshots)
}

func (p *failingProjection) Publish(snapshot access.Snapshot) error {
	if snapshot.Revision() == p.failRevision {
		return errInjectedPublication
	}
	return p.delegate.Publish(snapshot)
}

func (p *failingProjection) ResolveAccess(
	accessID access.AccessID,
) (access.Snapshot, error) {
	return p.delegate.ResolveAccess(accessID)
}

func (p *failingProjection) MarkUnavailable(accessID access.AccessID) {
	p.delegate.MarkUnavailable(accessID)
}

func (p *failingProjection) ProjectionHealth() access.ProjectionHealth {
	return p.delegate.ProjectionHealth()
}

func equalRevisions(left, right []access.Revision) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func newAccessID(t *testing.T, value string) access.AccessID {
	t.Helper()
	accessID, err := access.NewAccessID(value)
	if err != nil {
		t.Fatalf("construct Access ID: %v", err)
	}
	return accessID
}

func openStore(t *testing.T, databasePath string) *runtimepersistence.Store {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	store, err := runtimepersistence.Open(ctx, runtimepersistence.Options{
		DatabasePath:           databasePath,
		BusyTimeout:            runtimepersistence.DefaultBusyTimeout,
		CommitReconcileTimeout: runtimepersistence.DefaultCommitReconcileTimeout,
	})
	if err != nil {
		t.Fatalf("open runtime store: %v", err)
	}
	return store
}

func shutdownStore(t *testing.T, store *runtimepersistence.Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := store.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown runtime store: %v", err)
	}
}

func newManager(
	t *testing.T,
	store *runtimepersistence.Store,
	projection access.SnapshotProjection,
) *access.Manager {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	manager, err := access.NewManager(ctx, store.AccessRepository(), projection)
	if err != nil {
		t.Fatalf("construct Access manager: %v", err)
	}
	return manager
}

func shutdownManager(t *testing.T, manager *access.Manager) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown Access manager: %v", err)
	}
}

func assertFailureCode(t *testing.T, err error, expected access.ReasonCode) {
	t.Helper()
	var failure *access.Failure
	if !errors.As(err, &failure) {
		t.Fatalf("error is not an Access failure: %v", err)
	}
	if failure.Code != expected {
		t.Fatalf("failure code = %q, want %q", failure.Code, expected)
	}
}

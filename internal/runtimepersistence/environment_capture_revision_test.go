package runtimepersistence

import (
	"context"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

func TestSQLiteCaptureKeepsLaunchRevisionAfterEnvironmentPublish(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, t.TempDir()+"/runtime.db")
	defer shutdownTestStore(t, store)
	projection := environment.NewAtomicProjection()
	environments, err := environment.NewManager(
		context.Background(),
		store.EnvironmentRepository(),
		runtimeEnvironmentCompiler(t),
		projection,
		emptyCaptureInspector{},
	)
	if err != nil {
		t.Fatal(err)
	}
	assignments, err := captureassignment.NewManager(captureassignment.Options{
		Repository:   store.CaptureAssignmentRepository(),
		Environments: environments,
		Activity:     sqliteCaptureActivity{},
		Clock:        sqliteCaptureClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := assignments.Shutdown(ctx); err != nil {
			t.Errorf("shutdown assignments: %v", err)
		}
	}()

	work := environmentFixture(t, "work", 1)
	publishEnvironmentRevision(t, environments, work, 0)
	firstCapture := captureAssignmentReference(
		t,
		captureidentity.KindManagedRun,
		"run.frozen",
	)
	firstAssignment, err := assignments.Create(context.Background(), captureassignment.CreateCommand{
		Capture:       firstCapture,
		EnvironmentID: work.ID,
		Source:        captureassignment.SourceLaunch,
	})
	if err != nil {
		t.Fatal(err)
	}
	if firstAssignment.LaunchAuthority.InitialEnvironmentRevision() != 1 {
		t.Fatalf("first launch revision = %d", firstAssignment.LaunchAuthority.InitialEnvironmentRevision())
	}
	firstConnection, err := assignments.RegisterConnection(
		context.Background(),
		firstCapture,
		"connection.first",
		runtimeEnvironmentOrigin(t, "https://relay.example"),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer firstConnection.Close()
	facts := environment.RequestFacts{
		Target: protocolspec.RequestTarget{
			Method:    "POST",
			Path:      "/v1/messages",
			Transport: protocolspec.ClientOperationTransportHTTP,
		},
		DownstreamProtocol: wireprofile.ApplicationProtocolHTTP1,
	}
	firstRequest, err := assignments.BeginRequest(
		context.Background(), firstCapture, "connection.first", facts,
	)
	if err != nil {
		t.Fatal(err)
	}
	if firstRequest.Plan().EnvironmentRevision() != 1 {
		t.Fatalf("first request revision = %d", firstRequest.Plan().EnvironmentRevision())
	}

	revisionTwo := work.Clone()
	revisionTwo.Revision = 2
	revisionTwo.ClientEndpoints[0].Revision = 2
	revisionTwo.ClientEndpoints[0].ProtocolPlans[0].Revision = 2
	revisionTwo.ClientEndpoints[0].ProtocolPlans[0].ClientAdapterPolicy = environment.ClientAdapterPolicy{
		ID:       "adapter.replacement",
		Revision: 2,
	}
	publishEnvironmentRevision(t, environments, revisionTwo, 1)
	if firstRequest.Plan().EnvironmentRevision() != 1 {
		t.Fatalf("in-flight request drifted to revision %d", firstRequest.Plan().EnvironmentRevision())
	}
	firstRequest.Release()

	// A new connection inside the already launched Capture resolves r1 from the
	// durable revision archive, even though the active projection now holds r2.
	laterConnection, err := assignments.RegisterConnection(
		context.Background(),
		firstCapture,
		"connection.later",
		runtimeEnvironmentOrigin(t, "https://relay.example"),
	)
	if err != nil {
		t.Fatalf("register later connection: %v", err)
	}
	defer laterConnection.Close()
	laterRequest, err := assignments.BeginRequest(
		context.Background(), firstCapture, "connection.later", facts,
	)
	if err != nil {
		t.Fatal(err)
	}
	if laterRequest.Plan().EnvironmentRevision() != 1 {
		t.Fatalf("later request in existing Capture revision = %d", laterRequest.Plan().EnvironmentRevision())
	}
	laterRequest.Release()

	secondCapture := captureAssignmentReference(
		t,
		captureidentity.KindManagedRun,
		"run.new",
	)
	secondAssignment, err := assignments.Create(context.Background(), captureassignment.CreateCommand{
		Capture:       secondCapture,
		EnvironmentID: work.ID,
		Source:        captureassignment.SourceLaunch,
	})
	if err != nil {
		t.Fatal(err)
	}
	if secondAssignment.LaunchAuthority.InitialEnvironmentRevision() != 2 {
		t.Fatalf("new Capture launch revision = %d", secondAssignment.LaunchAuthority.InitialEnvironmentRevision())
	}
	secondConnection, err := assignments.RegisterConnection(
		context.Background(),
		secondCapture,
		"connection.second",
		runtimeEnvironmentOrigin(t, "https://relay.example"),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer secondConnection.Close()
	secondRequest, err := assignments.BeginRequest(
		context.Background(), secondCapture, "connection.second", facts,
	)
	if err != nil {
		t.Fatal(err)
	}
	if secondRequest.Plan().EnvironmentRevision() != 2 {
		t.Fatalf("new Capture request revision = %d", secondRequest.Plan().EnvironmentRevision())
	}
	secondRequest.Release()
}

func publishEnvironmentRevision(
	t *testing.T,
	manager *environment.Manager,
	candidate environment.Environment,
	expectedBase environment.Revision,
) {
	t.Helper()
	draft, err := manager.SaveDraft(context.Background(), environment.DraftCommand{
		ExpectedBaseRevision: expectedBase,
		Candidate:            candidate,
	})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := manager.Preview(context.Background(), candidate.ID, draft.Revision)
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.Publish(context.Background(), preview)
	if err != nil || result.Outcome != environment.CommitOutcomeCommitted {
		t.Fatalf("publish revision %d = %+v, %v", candidate.Revision, result, err)
	}
}

type emptyCaptureInspector struct{}

func (emptyCaptureInspector) ActiveCaptures(
	context.Context,
	environment.EnvironmentID,
	int,
) ([]environment.CaptureReference, error) {
	return nil, nil
}

type sqliteCaptureActivity struct{}

func (sqliteCaptureActivity) Active(
	context.Context,
	captureidentity.Reference,
) (bool, error) {
	return true, nil
}

type sqliteCaptureClock struct{}

func (sqliteCaptureClock) Now() time.Time {
	return time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
}

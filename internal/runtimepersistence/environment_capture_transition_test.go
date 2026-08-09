package runtimepersistence

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

func TestSQLiteEnvironmentPublishDrainsOnlyIncompatibleCaptureConnections(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, t.TempDir()+"/runtime.db")
	defer shutdownTestStore(t, store)
	projection := environment.NewAtomicProjection()
	closer := &transitionConnectionCloser{}
	assignments, err := captureassignment.NewManager(captureassignment.Options{
		Repository: store.CaptureAssignmentRepository(), Environments: projection,
		Activity: transitionCaptureActivity{}, Clock: transitionClock{},
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
	environments, err := environment.NewManager(
		context.Background(), store.EnvironmentRepository(), runtimeEnvironmentCompiler(t), projection, assignments,
	)
	if err != nil {
		t.Fatal(err)
	}
	work := environmentFixture(t, "work", 1)
	draft, err := environments.SaveDraft(context.Background(), environment.DraftCommand{Candidate: work})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := environments.Preview(context.Background(), work.ID, draft.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := environments.Publish(context.Background(), preview); err != nil {
		t.Fatal(err)
	}
	capture := captureAssignmentReference(t, captureidentity.KindManagedRun, "run.transition")
	if _, err := assignments.Create(context.Background(), captureassignment.CreateCommand{
		Capture: capture, EnvironmentID: work.ID, Source: captureassignment.SourceLaunch,
	}); err != nil {
		t.Fatal(err)
	}
	semanticConnection, err := assignments.RegisterConnection(
		context.Background(), capture, "connection.semantic", runtimeEnvironmentOrigin(t, "https://relay.example"), closer.Handle("connection.semantic"),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer semanticConnection.Close()
	blindConnection, err := assignments.RegisterConnection(
		context.Background(), capture, "connection.blind", runtimeEnvironmentOrigin(t, "https://files.example"), closer.Handle("connection.blind"),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer blindConnection.Close()
	facts := environment.RequestFacts{
		Target: protocolspec.RequestTarget{
			Method: "POST", Path: "/v1/messages",
			Transport: protocolspec.ClientOperationTransportHTTP,
		},
		DownstreamProtocol: wireprofile.ApplicationProtocolHTTP1,
	}
	oldSemanticRequest, err := assignments.BeginRequest(context.Background(), capture, "connection.semantic", facts)
	if err != nil {
		t.Fatal(err)
	}

	candidate := work.Clone()
	candidate.Revision = 2
	candidate.ClientEndpoints[0].Revision = 2
	candidate.ClientEndpoints[0].ProtocolPlans[0].Revision = 2
	candidate.ClientEndpoints[0].ProtocolPlans[0].ClientAdapterPolicy = environment.ClientAdapterPolicy{
		ID: "adapter.replacement", Revision: 2,
	}
	draft, err = environments.SaveDraft(context.Background(), environment.DraftCommand{
		ExpectedBaseRevision: 1, ExpectedDraftRevision: 0, Candidate: candidate,
	})
	if err != nil {
		t.Fatal(err)
	}
	preview, err = environments.Preview(context.Background(), work.ID, draft.Revision)
	if err != nil || preview.ReconnectRequiredCount != 1 {
		t.Fatalf("impact = %+v, %v", preview, err)
	}
	type publishAnswer struct {
		result environment.CommitResult
		err    error
	}
	published := make(chan publishAnswer, 1)
	go func() {
		result, err := environments.Publish(context.Background(), preview)
		published <- publishAnswer{result: result, err: err}
	}()
	select {
	case answer := <-published:
		t.Fatalf("publish completed before incompatible request drained: %+v", answer)
	case <-time.After(20 * time.Millisecond):
	}
	oldSemanticRequest.Release()
	select {
	case answer := <-published:
		if answer.err != nil || answer.result.Outcome != environment.CommitOutcomeCommitted {
			t.Fatalf("publish = %+v, %v", answer.result, answer.err)
		}
	case <-time.After(time.Second):
		t.Fatal("publish did not complete after incompatible request drained")
	}
	if closed := closer.IDs(); len(closed) != 1 || closed[0] != "connection.semantic" {
		t.Fatalf("closed = %v", closed)
	}
	if oldSemanticRequest.Plan().EnvironmentRevision() != 1 {
		t.Fatalf("old request revision = %d", oldSemanticRequest.Plan().EnvironmentRevision())
	}
	newSemanticConnection, err := assignments.RegisterConnection(
		context.Background(), capture, "connection.semantic.new", runtimeEnvironmentOrigin(t, "https://relay.example"), closer.Handle("connection.semantic.new"),
	)
	if err != nil {
		t.Fatalf("register new semantic connection: %v", err)
	}
	defer newSemanticConnection.Close()
	newRequest, err := assignments.BeginRequest(context.Background(), capture, "connection.semantic.new", facts)
	if err != nil {
		t.Fatalf("begin new semantic request: %v", err)
	}
	if newRequest.Plan().EnvironmentRevision() != 2 {
		t.Fatalf("new request revision = %d", newRequest.Plan().EnvironmentRevision())
	}
	newRequest.Release()
}

type transitionCaptureActivity struct{}

func (transitionCaptureActivity) Active(
	context.Context,
	captureidentity.Reference,
) (bool, error) {
	return true, nil
}

type transitionClock struct{}

func (transitionClock) Now() time.Time {
	return time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
}

type transitionConnectionCloser struct {
	mu  sync.Mutex
	ids []string
}

type transitionConnectionCloseHandle struct {
	closer *transitionConnectionCloser
	id     string
}

func (closer *transitionConnectionCloser) Handle(id string) captureassignment.ConnectionCloseHandle {
	return &transitionConnectionCloseHandle{closer: closer, id: id}
}

func (handle *transitionConnectionCloseHandle) Close(_ context.Context) error {
	closer := handle.closer
	closer.mu.Lock()
	defer closer.mu.Unlock()
	closer.ids = append(closer.ids, handle.id)
	return nil
}

func (closer *transitionConnectionCloser) IDs() []string {
	closer.mu.Lock()
	defer closer.mu.Unlock()
	return append([]string(nil), closer.ids...)
}

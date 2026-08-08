package activity_test

import (
	"context"
	"crypto/rand"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
)

const activityIntegrationStartupTimeout = 60 * time.Second

func TestActivityTimelinePersistsRedactedEventsAndPaginates(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "data", "runtime.db")
	store := openStore(t, path)
	manager, err := activity.New(activity.Options{
		Repository: store.ActivityRepository(),
		Clock:      activity.SystemClock{},
		Random:     rand.Reader,
	})
	if err != nil {
		t.Fatal(err)
	}
	empty, err := manager.List(context.Background(), activity.PageRequest{
		Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if empty.Items == nil || len(empty.Items) != 0 {
		t.Fatalf("empty Activity page = %+v", empty)
	}
	environmentID, err := environment.NewEnvironmentID("environment-activity")
	if err != nil {
		t.Fatal(err)
	}
	first, err := manager.Record(context.Background(), activity.Event{
		Kind:                activity.KindEnvironmentApplied,
		EnvironmentID:       environmentID,
		EnvironmentRevision: 1,
		EnvironmentDigest:   strings.Repeat("1", 64),
		SubjectID:           "environment-activity",
		Status:              activity.StatusSucceeded,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Record(context.Background(), activity.Event{
		Kind:                activity.KindApprovalResolved,
		EnvironmentID:       environmentID,
		EnvironmentRevision: 1,
		EnvironmentDigest:   strings.Repeat("1", 64),
		SubjectID:           "approval-1",
		Status:              activity.StatusFailed,
		ReasonCode:          "tool_denied",
	})
	if err != nil {
		t.Fatal(err)
	}
	third, err := manager.Record(context.Background(), activity.Event{
		Kind:            activity.KindCredentialSecretReplaced,
		AccountID:       "anthropic-work",
		AccountRevision: 2,
		CredentialEpoch: 3,
		SubjectID:       "credential-1",
		Status:          activity.StatusSucceeded,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Sequence <= 0 ||
		second.Sequence != first.Sequence+1 ||
		third.Sequence != second.Sequence+1 {
		t.Fatalf(
			"Activity sequences first=%d second=%d third=%d",
			first.Sequence,
			second.Sequence,
			third.Sequence,
		)
	}
	page, err := manager.List(context.Background(), activity.PageRequest{
		Limit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 ||
		page.Items[0].ID != third.ID ||
		page.NextBeforeSequence != third.Sequence {
		t.Fatalf("latest Activity page = %+v", page)
	}
	older, err := manager.List(context.Background(), activity.PageRequest{
		BeforeSequence: page.NextBeforeSequence,
		Limit:          10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(older.Items) != 2 ||
		older.Items[0].ID != second.ID ||
		older.Items[1].ID != first.ID {
		t.Fatalf("older Activity page = %+v", older)
	}
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.List(
		context.Background(),
		activity.PageRequest{Limit: 1},
	); !errors.Is(err, activity.ErrRuntimeStopping) {
		t.Fatalf("stopped Activity runtime accepted a read: %v", err)
	}
	if _, err := manager.ListExchanges(
		context.Background(),
		activity.PageRequest{Limit: 1},
	); !errors.Is(err, activity.ErrRuntimeStopping) {
		t.Fatalf("stopped Activity runtime accepted an Exchange read: %v", err)
	}
	shutdownStore(t, store)

	reopened := openStore(t, path)
	defer shutdownStore(t, reopened)
	recovered, err := reopened.ActivityRepository().List(
		context.Background(),
		activity.PageRequest{Limit: 10},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered.Items) != 3 ||
		recovered.Items[0].ID != third.ID ||
		recovered.Items[1].ID != second.ID ||
		recovered.Items[2].ID != first.ID {
		t.Fatalf("recovered Activity timeline = %+v", recovered)
	}
}

func TestActivityPersistsImmutableTransportSelectionEvidence(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "data", "runtime.db")
	store := openStore(t, path)
	manager, err := activity.New(activity.Options{
		Repository: store.ActivityRepository(),
		Clock:      activity.SystemClock{},
		Random:     rand.Reader,
	})
	if err != nil {
		t.Fatal(err)
	}
	requested := activity.TransportProfileEvidence{
		Ref:      "observed-client-strict-h1",
		Revision: 1,
		Source:   "observed_client",
	}
	persistedRequested := requested
	effective := requested
	evidence := activity.TransportEvidence{
		Presentation: &activity.WirePresentationEvidence{
			RequestedRef:     "follow-client",
			EffectiveRef:     "follow-client",
			Revision:         1,
			Mode:             "follow_client",
			ClientProtocol:   "http/1.1",
			UpstreamProtocol: "http/1.1",
		},
		Requested:     &requested,
		Effective:     &effective,
		FallbackChain: []activity.TransportProfileEvidence{requested},
		ClientOfferedALPN: []string{
			"h2",
			"http/1.1",
		},
		DownstreamNegotiatedALPN: "http/1.1",
		UpstreamOfferedALPN:      []string{"http/1.1"},
		UpstreamNegotiatedALPN:   "http/1.1",
		HTTPTransport:            "http1",
	}
	event := validExchangeEvent(t)
	event.SubjectID = "exchange-transport"
	event.SourceKind = activity.SourceSystemProxy
	event.SourceDisplayName = "ViberMate runtime"
	event.SourceRecognition = activity.SourceRecognitionUnknown
	event.CaptureRunID = ""
	event.ConnectionID = "connection-transport"
	event.Transport = &evidence
	recorded, err := manager.Record(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	evidence.FallbackChain[0].Ref = "mutated"
	evidence.ClientOfferedALPN[0] = "mutated"
	evidence.Presentation.RequestedRef = "mutated"
	evidence.Requested.Ref = "mutated"
	page, err := manager.List(
		context.Background(),
		activity.PageRequest{Limit: 10},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 ||
		page.Items[0].ID != recorded.ID ||
		page.Items[0].Transport == nil {
		t.Fatalf("transport Activity = %+v", page)
	}
	want := activity.TransportEvidence{
		Presentation: &activity.WirePresentationEvidence{
			RequestedRef:     "follow-client",
			EffectiveRef:     "follow-client",
			Revision:         1,
			Mode:             "follow_client",
			ClientProtocol:   "http/1.1",
			UpstreamProtocol: "http/1.1",
		},
		Requested:                &persistedRequested,
		Effective:                &effective,
		FallbackChain:            []activity.TransportProfileEvidence{persistedRequested},
		ClientOfferedALPN:        []string{"h2", "http/1.1"},
		DownstreamNegotiatedALPN: "http/1.1",
		UpstreamOfferedALPN:      []string{"http/1.1"},
		UpstreamNegotiatedALPN:   "http/1.1",
		HTTPTransport:            "http1",
	}
	if !reflect.DeepEqual(*page.Items[0].Transport, want) {
		t.Fatalf("transport evidence = %+v, want %+v", page.Items[0].Transport, want)
	}
	page.Items[0].Transport.FallbackChain[0].Ref = "output-mutated"
	page.Items[0].Transport.Presentation.RequestedRef = "output-mutated"
	page.Items[0].Transport.Requested.Ref = "output-mutated"
	again, err := manager.List(
		context.Background(),
		activity.PageRequest{Limit: 10},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*again.Items[0].Transport, want) {
		t.Fatal("Activity reader returned aliased transport evidence")
	}
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	shutdownStore(t, store)

	reopened := openStore(t, path)
	defer shutdownStore(t, reopened)
	recovered, err := reopened.ActivityRepository().List(
		context.Background(),
		activity.PageRequest{Limit: 10},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered.Items) != 1 ||
		recovered.Items[0].Transport == nil ||
		!reflect.DeepEqual(*recovered.Items[0].Transport, want) {
		t.Fatalf("recovered transport evidence = %+v", recovered)
	}
}

func openStore(t *testing.T, path string) *runtimepersistence.Store {
	t.Helper()
	// Full-suite race instrumentation runs this migration beside the other
	// repository fixtures. Preserve a hard test bound without making scheduler
	// contention look like a migration defect.
	ctx, cancel := context.WithTimeout(
		context.Background(),
		activityIntegrationStartupTimeout,
	)
	defer cancel()
	store, err := runtimepersistence.Open(ctx, runtimepersistence.Options{
		DatabasePath:           path,
		BusyTimeout:            runtimepersistence.DefaultBusyTimeout,
		CommitReconcileTimeout: runtimepersistence.DefaultCommitReconcileTimeout,
	})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func shutdownStore(t *testing.T, store *runtimepersistence.Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := store.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

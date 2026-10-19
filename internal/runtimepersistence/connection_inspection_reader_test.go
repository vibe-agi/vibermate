package runtimepersistence

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/recording"
)

type connectionInspectionSink struct {
	writer connectionevent.ScopedStore
	events []connectionevent.Event
	err    error
}

func (s *connectionInspectionSink) ObserveConnection(stamp recording.Stamp, event connectionevent.Event) recording.OfferResult {
	s.events = append(s.events, event)
	if err := s.writer.PutConnection(context.Background(), stamp.Scope(), event); err != nil {
		s.err = err
		return recording.DroppedStale
	}
	return recording.Accepted
}
func newRepeatedInspectionConnection(t *testing.T, f *egressInspectionFixture, r *ConnectionInspectionReader) (string, []connectionevent.Event) {
	t.Helper()
	ctx := context.Background()
	sink := &connectionInspectionSink{writer: f.current.ConnectionEventRepository().(connectionevent.ScopedStore)}
	manager, err := connectionevent.New(ctx, connectionevent.Options{Reader: r, Observer: sink, Clock: runtimeUserTestClock{now: time.Unix(10, 0).UTC()}, Random: bytes.NewReader(bytes.Repeat([]byte{0x42}, 20))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Shutdown(ctx) })
	connection, err := manager.Start(ctx, connectionevent.Attempt{Source: connectionevent.Source{Confidence: connectionevent.SourceConfidenceUnknown}, RequestedHost: "example.test", Port: 443})
	if err != nil {
		t.Fatal(err)
	}
	epoch, _ := recording.NewEpoch(1)
	stamp, err := recording.NewStamp(epoch, f.scope.Capture, f.scope.Fence)
	if err != nil {
		t.Fatal(err)
	}
	if err = connection.BindObservationScope(stamp); err != nil {
		t.Fatal(err)
	}
	ask := connectionevent.DecisionEvidence{Source: connectionevent.Source{Confidence: connectionevent.SourceConfidenceUnknown}, Decision: connectionevent.DecisionAsk, RuleID: "ask", Decryption: connectionevent.DecryptionNone}
	for i := 0; i < 2; i++ {
		if err = connection.Decide(ctx, ask); err != nil {
			t.Fatal(err)
		}
	}
	allow := connectionevent.DecisionEvidence{Source: connectionevent.Source{Confidence: connectionevent.SourceConfidenceUnknown}, Decision: connectionevent.DecisionAllow, RuleID: "allow", RouteHost: "example.test", Decryption: connectionevent.DecryptionBlind}
	if err = connection.Decide(ctx, allow); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = connection.Connected(ctx, connectionevent.ConnectedEvidence{}); err != nil {
			t.Fatal(err)
		}
	}
	if err = connection.Finish(ctx, connectionevent.TerminalEvidence{Outcome: connectionevent.OutcomeCompleted}); err != nil {
		t.Fatal(err)
	}
	if sink.err != nil {
		t.Fatal(sink.err)
	}
	if len(sink.events) != 7 || !reflect.DeepEqual(sink.events[1], sink.events[2]) || !reflect.DeepEqual(sink.events[4], sink.events[5]) {
		t.Fatal("producer did not emit real repeated identical phases")
	}
	return connection.ID(), sink.events
}
func connectionInspectionLegacyRoot(t *testing.T, f *egressInspectionFixture, id string) {
	t.Helper()
	input := normalizedEgressStart(providerAttempt(t, "audit-for-"+id))
	input.ConnectionID = id
	a, err := egressaudit.New(input)
	if err != nil {
		t.Fatal(err)
	}
	f.put(t, false, a)
	f.activityRoot(t, a, "audit-a")
}
func TestConnectionInspectionSelectsWholeTimelineAndPreservesRepeatedPhases(t *testing.T) {
	f := newEgressInspectionFixture(t)
	r := NewConnectionInspectionReader(f.reader.catalog, f.control)
	ctx := context.Background()
	id, events := newRepeatedInspectionConnection(t, f, r)
	if _, err := appendConnectionEvent(ctx, f.legacy, events[0]); err != nil {
		t.Fatal(err)
	}
	connectionInspectionLegacyRoot(t, f, id)
	for _, event := range events {
		event.ConnectionID = "legacy-other"
		if _, err := appendConnectionEvent(ctx, f.legacy, event); err != nil {
			t.Fatal(err)
		}
	}
	connectionInspectionLegacyRoot(t, f, "legacy-other")
	timeline, err := r.Timeline(ctx, id)
	if err != nil || len(timeline.Events) != 7 {
		t.Fatalf("canonical timeline merged/deduplicated real occurrences: %+v %v", timeline, err)
	}
	for i, event := range events {
		if !reflect.DeepEqual(timeline.Events[i].Event, event) {
			t.Fatalf("event %d changed: %+v", i, timeline.Events[i])
		}
	}
	cursor := ""
	seen := map[string]bool{}
	for pages := 0; pages < 10; pages++ {
		page, err := r.List(ctx, connectionevent.PageRequest{Cursor: cursor, Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			key := fmt.Sprintf("%s/%d", item.ConnectionID, item.Sequence)
			if seen[key] {
				t.Fatalf("repeated source occurrence: %s", key)
			}
			seen[key] = true
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	if len(seen) != 14 || cursor != "" {
		t.Fatalf("source-local pagination lost/replayed phases: %d %q", len(seen), cursor)
	}
	latest, err := r.List(ctx, connectionevent.PageRequest{Limit: 10, LatestPerConnection: true})
	if err != nil || len(latest.Items) != 2 || latest.Items[0].ConnectionID != id || latest.Items[0].Phase != connectionevent.PhaseClosed {
		t.Fatalf("latest state from canonical source: %+v %v", latest, err)
	}
}

func TestConnectionInspectionLatestPrecedesIngressAndSourceSwitchRejectsCursor(t *testing.T) {
	f := newEgressInspectionFixture(t)
	r := NewConnectionInspectionReader(f.reader.catalog, f.control)
	ctx := context.Background()
	first := connectionevent.Event{ConnectionID: "switching", IngressID: "old-ingress", SourceConfidence: connectionevent.SourceConfidenceUnknown, RequestedHost: "example.test", Port: 443, Decryption: connectionevent.DecryptionNone, Phase: connectionevent.PhaseAttempted, StartedAt: time.Unix(10, 0).UTC()}
	last := first
	last.IngressID = "new-ingress"
	last.SourceConfidence = connectionevent.SourceConfidenceConfigured
	last.SourceLabel = "Frozen source"
	last.Decision = connectionevent.DecisionAsk
	last.RuleID = "ask"
	last.Phase = connectionevent.PhaseAsked
	for _, e := range []connectionevent.Event{first, last} {
		if _, err := appendConnectionEvent(ctx, f.legacy, e); err != nil {
			t.Fatal(err)
		}
	}
	connectionInspectionLegacyRoot(t, f, first.ConnectionID)
	page, err := r.List(ctx, connectionevent.PageRequest{Limit: 10, IngressID: "old-ingress", LatestPerConnection: true})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("old ingress resurrected before latest selection: %+v %v", page, err)
	}
	all, err := r.List(ctx, connectionevent.PageRequest{Limit: 10, IngressID: "old-ingress"})
	if err != nil || len(all.Items) != 1 {
		t.Fatalf("historical occurrence disappeared: %+v %v", all, err)
	}
	firstPage, err := r.List(ctx, connectionevent.PageRequest{Limit: 1})
	if err != nil || len(firstPage.Items) != 1 || firstPage.NextCursor == "" {
		t.Fatalf("legacy continuation absent: %+v %v", firstPage, err)
	}
	if err = f.current.ConnectionEventRepository().(connectionevent.ScopedStore).PutConnection(ctx, f.scope, first); err != nil {
		t.Fatal(err)
	}
	if _, err = r.List(ctx, connectionevent.PageRequest{Limit: 1, Cursor: firstPage.NextCursor}); !errors.Is(err, ErrInspectionChanged) {
		t.Fatalf("source-switched local boundary reused: %v", err)
	}
	timeline, err := r.Timeline(ctx, first.ConnectionID)
	if err != nil || len(timeline.Events) != 1 || timeline.Events[0].Phase != connectionevent.PhaseAttempted {
		t.Fatalf("whole canonical source was merged with archive: %+v %v", timeline, err)
	}
}
func TestConnectionInspectionUnknownCurrentDoesNotBorrowLegacyOwnership(t *testing.T) {
	f := newEgressInspectionFixture(t)
	r := NewConnectionInspectionReader(f.reader.catalog, f.control)
	ctx := context.Background()
	e := connectionevent.Event{ConnectionID: "unknown-current", SourceConfidence: connectionevent.SourceConfidenceUnknown, RequestedHost: "example.test", Port: 443, Decryption: connectionevent.DecryptionNone, Phase: connectionevent.PhaseAttempted, StartedAt: time.Unix(10, 0).UTC()}
	if _, err := appendConnectionEvent(ctx, f.legacy, e); err != nil {
		t.Fatal(err)
	}
	connectionInspectionLegacyRoot(t, f, e.ConnectionID)
	if _, err := f.current.ConnectionEventRepository().Append(ctx, e); err != nil {
		t.Fatal(err)
	}
	page, err := r.List(ctx, connectionevent.PageRequest{Limit: 10})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("unknown current scope relabeled: %+v %v", page, err)
	}
	if _, err = r.Timeline(ctx, e.ConnectionID); !errors.Is(err, connectionevent.ErrNotFound) {
		t.Fatalf("unattributed current timeline visible: %v", err)
	}
}
func TestConnectionInspectionTimelineBoundAndFinalVisibility(t *testing.T) {
	f := newEgressInspectionFixture(t)
	r := NewConnectionInspectionReader(f.reader.catalog, f.control)
	ctx := context.Background()
	e := connectionevent.Event{ConnectionID: "bounded", SourceConfidence: connectionevent.SourceConfidenceUnknown, RequestedHost: "example.test", Port: 443, Decryption: connectionevent.DecryptionNone, Phase: connectionevent.PhaseAsked, Decision: connectionevent.DecisionAsk, RuleID: "ask", StartedAt: time.Unix(10, 0).UTC()}
	writer := f.current.ConnectionEventRepository().(connectionevent.ScopedStore)
	for i := 0; i < connectionevent.MaxTimelineSize+1; i++ {
		if err := writer.PutConnection(ctx, f.scope, e); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.Timeline(ctx, e.ConnectionID); !errors.Is(err, ErrInspectionSourceLimit) {
		t.Fatalf("oversized timeline was partial success: %v", err)
	}
	page, err := r.List(ctx, connectionevent.PageRequest{Limit: 1, LatestPerConnection: true})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("bounded state listing required entire timeline output: %+v %v", page, err)
	}
	r.visibility = &usageInspectionChangingVisibility{ControlStore: f.control, beforeCheck: func() {
		nativeExec(t, f.control.store.database, `UPDATE runtime_storage_state SET visibility_revision=visibility_revision+1`)
	}}
	page, err = r.List(ctx, connectionevent.PageRequest{Limit: 1})
	if !errors.Is(err, ErrInspectionChanged) || len(page.Items) != 0 {
		t.Fatalf("stale connection state published: %+v %v", page, err)
	}
}

func TestConnectionInspectionFrozenV1AndNativeOnceOnlyOwnership(t *testing.T) {
	for _, revision := range []int{1, 2} {
		t.Run(fmt.Sprint(revision), func(t *testing.T) {
			f := newEgressInspectionFixture(t, revision)
			r := NewConnectionInspectionReader(f.reader.catalog, f.control)
			ctx := context.Background()
			id := "legacy-connection"
			if revision == 2 {
				a := nativeAuditFixture(t, "native-connection-audit")
				id = a.ConnectionID()
				f.put(t, false, a)
				nativeExec(t, f.legacy, `INSERT INTO native_tool_scopes VALUES('managed_run','audit-a',1,1,'closed',1,100);INSERT INTO native_tool_once VALUES('managed_run','audit-a',randomblob(32),'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',randomblob(32),'native-connection-audit',1,1)`)
			} else {
				connectionInspectionLegacyRoot(t, f, id)
			}
			nativeExec(t, f.legacy, `INSERT INTO runtime_connection_events(connection_id,source_confidence,requested_host,port,decryption,phase,started_at_unix_ms) VALUES(?,'unknown','example.test',443,'none','attempted',1000)`, id)
			timeline, err := r.Timeline(ctx, id)
			if err != nil || len(timeline.Events) != 1 || timeline.Events[0].InspectionPurpose != "" {
				t.Fatalf("frozen native ownership/unknown purpose: %+v %v", timeline, err)
			}
		})
	}
}

func TestAuditInspectionRechecksSourceAfterSQLCloses(t *testing.T) {
	for _, domain := range []string{"connection", "egress"} {
		t.Run(domain, func(t *testing.T) {
			f := newEgressInspectionFixture(t)
			ctx := context.Background()
			f.put(t, true, providerAttempt(t, "current-guard"))
			e := connectionevent.Event{ConnectionID: "current-guard", SourceConfidence: connectionevent.SourceConfidenceUnknown, RequestedHost: "example.test", Port: 443, Decryption: connectionevent.DecryptionNone, Phase: connectionevent.PhaseAttempted, StartedAt: time.Unix(10, 0).UTC()}
			if err := f.current.ConnectionEventRepository().(connectionevent.ScopedStore).PutConnection(ctx, f.scope, e); err != nil {
				t.Fatal(err)
			}
			type maintenanceResult struct {
				token *InspectionMaintenance
				err   error
			}
			done := make(chan maintenanceResult, 1)
			visibility := &usageInspectionChangingVisibility{ControlStore: f.control, beforeCheck: func() {
				var busy, frames, checkpointed int
				if err := f.current.store.database.QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &frames, &checkpointed); err != nil || busy != 0 {
					t.Fatalf("audit SQL retained across final control read: %d %v", busy, err)
				}
				f.reader.catalog.mu.Lock()
				changed := f.reader.catalog.changed
				active := f.reader.catalog.active
				f.reader.catalog.mu.Unlock()
				if active != 1 {
					t.Fatalf("actual inspection owner released early: %d", active)
				}
				go func() { token, err := f.reader.catalog.BeginMaintenance(ctx, 2); done <- maintenanceResult{token, err} }()
				select {
				case <-changed:
				case <-time.After(time.Second):
					t.Fatal("maintenance did not reserve generation")
				}
			}}
			var err error
			if domain == "connection" {
				r := NewConnectionInspectionReader(f.reader.catalog, visibility)
				var page connectionevent.Page
				page, err = r.List(ctx, connectionevent.PageRequest{Limit: 10})
				if len(page.Items) != 0 {
					t.Fatal("stale connection page escaped")
				}
			} else {
				f.reader.visibility = visibility
				var page egressaudit.Page
				page, err = f.reader.List(ctx, egressaudit.PageRequest{Limit: 10})
				if len(page.Items) != 0 {
					t.Fatal("stale egress page escaped")
				}
			}
			if !errors.Is(err, ErrInspectionChanged) {
				t.Fatalf("source guard lost after actual SQL: %v", err)
			}
			finished := <-done
			if finished.err != nil {
				t.Fatal(finished.err)
			}
			finished.token.Close()
		})
	}
}

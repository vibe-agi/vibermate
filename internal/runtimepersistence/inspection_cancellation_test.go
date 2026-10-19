package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/recording"
)

func heldInspectionCancellationFixture(t *testing.T) (*InspectionCatalog, *ControlStore, *inspectionLease) {
	t.Helper()
	options, current := inspectionSourcesFixture(t)
	options.Concurrency = 1
	control := inspectionControlFixture(t)
	seedCompleteInspectionMirror(t, current, control)
	catalog, err := NewInspectionCatalog(options)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := catalog.acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := lease.close(); err != nil {
			t.Error(err)
		}
		if err := catalog.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return catalog, control, lease
}

func TestInspectionClosedOwnerReturnsUnavailableWithoutReleasingLease(t *testing.T) {
	catalog, control, lease := heldInspectionCancellationFixture(t)
	closeContext, cancelClose := context.WithCancel(t.Context())
	cancelClose()
	if err := catalog.Close(closeContext); !errors.Is(err, context.Canceled) {
		t.Fatalf("Close did not retain the physically held lease: %v", err)
	}
	caller, cancelCaller := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelCaller()
	// The sole slot is still physically owned, so the read must observe owner
	// cancellation rather than win a slot and reach the later closed check.
	other, err := catalog.acquire(caller)
	if other != nil {
		other.close()
		t.Fatal("closed owner granted a second lease")
	}
	if !errors.Is(err, ErrInspectionUnavailable) || !errors.Is(err, context.Canceled) || caller.Err() != nil {
		t.Errorf("owner close was confused with caller cancellation: err=%v caller=%v", err, caller.Err())
	}
	page, err := NewConnectionInspectionReader(catalog, control).List(caller, connectionevent.PageRequest{Limit: 1})
	if !errors.Is(err, ErrInspectionUnavailable) || !errors.Is(err, context.Canceled) || caller.Err() != nil || len(page.Items) != 0 || page.NextCursor != "" {
		t.Errorf("closed-owner connection read did not return an empty typed refusal: page=%+v err=%v caller=%v", page, err, caller.Err())
	}
	catalog.mu.Lock()
	active, slots := catalog.active, len(catalog.slots)
	catalog.mu.Unlock()
	if active != 1 || slots != 1 {
		t.Fatalf("cancellation released another reader's physical ownership: active=%d slots=%d", active, slots)
	}
	if err := lease.close(); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Close(caller); err != nil {
		t.Fatal("closed catalog did not drain after physical release", err)
	}
}

func TestInspectionCallerCancellationRemainsDistinctFromOwnerClose(t *testing.T) {
	catalog, _, _ := heldInspectionCancellationFixture(t)
	for _, deadline := range []bool{false, true} {
		name := "canceled"
		if deadline {
			name = "deadline"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			want := error(context.Canceled)
			if deadline {
				cancel()
				ctx, cancel = context.WithTimeout(t.Context(), 30*time.Millisecond)
				want = context.DeadlineExceeded
			} else {
				cancel()
			}
			defer cancel()
			lease, err := catalog.acquire(ctx)
			if lease != nil {
				lease.close()
				t.Fatal("canceled caller obtained the occupied slot")
			}
			if !errors.Is(err, want) || errors.Is(err, ErrInspectionUnavailable) || catalog.owner.Err() != nil {
				t.Fatalf("caller cancellation was relabeled as owner close: err=%v owner=%v", err, catalog.owner.Err())
			}
		})
	}
}

// The existing visibility port can fail before an audit lease is acquired.
// These faults exercise classification/retained details, never supply authority.
type failedInspectionSnapshot struct {
	recording.InspectionVisibility
	failure error
}

func (v failedInspectionSnapshot) ReadInspectionSnapshot(context.Context, []captureidentity.Reference) (recording.VisibilitySnapshot, error) {
	return recording.VisibilitySnapshot{}, v.failure
}

func TestAuditInspectionClassifiesOnlyActiveCallerOwnerCancellation(t *testing.T) {
	for _, test := range []struct {
		name                     string
		closeOwner, cancelCaller bool
		cause                    error
		unavailable              bool
	}{
		{"owner_closed", true, false, context.Canceled, true},
		{"owner_active", false, false, context.Canceled, false},
		{"caller_canceled", true, true, context.Canceled, false},
		{"unrelated_sql_error", true, false, sql.ErrConnDone, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			catalog, control, _ := heldInspectionCancellationFixture(t)
			if test.closeOwner {
				closing, cancel := context.WithCancel(t.Context())
				cancel()
				if err := catalog.Close(closing); !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			}
			caller, cancel := context.WithCancel(t.Context())
			defer cancel()
			if test.cancelCaller {
				cancel()
			}
			detail := errors.New("visibility stage detail")
			failure := fmt.Errorf("visibility read: %w", errors.Join(test.cause, detail))
			reader := NewConnectionInspectionReader(catalog, failedInspectionSnapshot{control, failure})
			page, err := reader.List(caller, connectionevent.PageRequest{Limit: 1})
			if len(page.Items) != 0 || page.NextCursor != "" || !errors.Is(err, failure) || !errors.Is(err, detail) || !errors.Is(err, test.cause) || errors.Is(err, ErrInspectionUnavailable) != test.unavailable {
				t.Fatalf("inspection failure classification/details changed: page=%+v err=%v", page, err)
			}
			if !test.unavailable && err != failure {
				t.Fatal("unrelated/caller failure was wrapped", err)
			}
		})
	}
}

func closeInspectionOwnerDuringRead(t *testing.T, catalog *InspectionCatalog, caller, work context.Context) {
	t.Helper()
	catalog.mu.Lock()
	active, slots := catalog.active, len(catalog.slots)
	catalog.mu.Unlock()
	if active != 1 || slots != 1 {
		t.Fatalf("post-begin read lacks its real lease: active=%d slots=%d", active, slots)
	}
	closing, cancel := context.WithCancel(caller)
	cancel()
	if err := catalog.Close(closing); !errors.Is(err, context.Canceled) {
		t.Fatalf("Close released active read: %v", err)
	}
	select {
	case <-work.Done():
	case <-caller.Done():
		t.Fatal("owner did not cancel private read before caller deadline", caller.Err())
	}
	if caller.Err() != nil {
		t.Fatal("original caller was canceled", caller.Err())
	}
}

type closingAuditVisibility struct {
	recording.InspectionVisibility
	t       *testing.T
	catalog *InspectionCatalog
	caller  context.Context
	checked bool
}

func (v *closingAuditVisibility) RecheckInspectionStamp(ctx context.Context, _ recording.InspectionStamp) error {
	v.checked = true
	closeInspectionOwnerDuringRead(v.t, v.catalog, v.caller, ctx)
	// Honor the actual received context, as a cancellation-aware visibility
	// reader may. Initial authority/schema/data were read from real stores.
	return ctx.Err()
}

func TestAuditInspectionOwnerCloseAfterBegin(t *testing.T) {
	for _, operation := range []string{"connection_list", "connection_timeline", "egress_list", "connection_sql"} {
		t.Run(operation, func(t *testing.T) {
			f := newEgressInspectionFixture(t)
			catalog := f.reader.catalog
			caller, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			id := "post-begin-connection"
			event := connectionevent.Event{ConnectionID: id, SourceConfidence: connectionevent.SourceConfidenceUnknown, RequestedHost: "example.test", Port: 443, Decryption: connectionevent.DecryptionNone, Phase: connectionevent.PhaseAttempted, StartedAt: time.Unix(10, 0).UTC()}
			if err := f.current.ConnectionEventRepository().(connectionevent.ScopedStore).PutConnection(caller, f.scope, event); err != nil {
				t.Fatal(err)
			}
			attempt := providerAttempt(t, "post-begin-attempt")
			f.put(t, true, attempt)
			if operation == "connection_sql" {
				read, err := beginAuditInspection(caller, catalog, f.control)
				if err != nil {
					t.Fatal(err)
				}
				defer read.close()
				q := connectionevent.PageRequest{Limit: 1}
				candidates, _, err := read.connectionCandidates(q, nil, 1)
				if err != nil || len(candidates) != 1 || candidates[0].id != id {
					t.Fatalf("real candidate query missing before Close: %+v %v", candidates, err)
				}
				if source, err := read.connectionSource(id); err != nil || source < 0 {
					t.Fatalf("real source query missing before Close: %d %v", source, err)
				}
				closeInspectionOwnerDuringRead(t, catalog, caller, read.lease.ctx)
				candidates, more, candidateErr := read.connectionCandidates(q, nil, 1)
				source, sourceErr := read.connectionSource(id)
				if len(candidates) != 0 || more || source != -1 || !errors.Is(candidateErr, context.Canceled) || !errors.Is(sourceErr, context.Canceled) || errors.Is(candidateErr, ErrInspectionUnavailable) || errors.Is(sourceErr, ErrInspectionUnavailable) {
					t.Fatalf("post-begin SQL cancellation witness changed: candidates=%+v more=%t source=%d errors=%v/%v", candidates, more, source, candidateErr, sourceErr)
				}
				t.Logf("actual post-begin SQL refused without data: candidate=%v source=%v originalCaller=%v", candidateErr, sourceErr, caller.Err())
				read.close()
			} else {
				read := func(visibility recording.InspectionVisibility) (int, error) {
					switch operation {
					case "connection_list":
						page, err := NewConnectionInspectionReader(catalog, visibility).List(caller, connectionevent.PageRequest{Limit: 1})
						if err == nil && (len(page.Items) != 1 || page.Items[0].ConnectionID != id) {
							t.Fatalf("real successful connection page changed: %+v", page)
						}
						if err != nil && page.NextCursor != "" {
							t.Fatal("failed page retained cursor")
						}
						return len(page.Items), err
					case "connection_timeline":
						timeline, err := NewConnectionInspectionReader(catalog, visibility).Timeline(caller, id)
						if err == nil && (timeline.ConnectionID != id || len(timeline.Events) != 1 || timeline.Events[0].ConnectionID != id) {
							t.Fatalf("real successful timeline changed: %+v", timeline)
						}
						if err != nil && timeline.ConnectionID != "" {
							t.Fatal("failed timeline retained identity")
						}
						return len(timeline.Events), err
					default:
						page, err := NewEgressInspectionReader(catalog, visibility).List(caller, egressaudit.PageRequest{Limit: 1})
						if err == nil && (len(page.Items) != 1 || page.Items[0].Attempt.ID() != attempt.ID()) {
							t.Fatalf("real successful egress page changed: %+v", page)
						}
						if err != nil && page.NextCursor != "" {
							t.Fatal("failed egress page retained cursor")
						}
						return len(page.Items), err
					}
				}
				if count, err := read(f.control); err != nil || count != 1 {
					t.Fatalf("positive read failed: count=%d err=%v", count, err)
				}
				visibility := &closingAuditVisibility{InspectionVisibility: f.control, t: t, catalog: catalog, caller: caller}
				count, err := read(visibility)
				if !visibility.checked || count != 0 || caller.Err() != nil || !errors.Is(err, ErrInspectionUnavailable) || !errors.Is(err, context.Canceled) {
					t.Errorf("post-begin close was not an empty typed refusal: checked=%t count=%d err=%v caller=%v", visibility.checked, count, err, caller.Err())
				}
			}
			catalog.mu.Lock()
			active, slots := catalog.active, len(catalog.slots)
			catalog.mu.Unlock()
			if active != 0 || slots != 0 {
				t.Fatalf("public read leaked physical ownership: active=%d slots=%d", active, slots)
			}
			if err := catalog.Close(caller); err != nil {
				t.Fatal("Close did not join after real reader release", err)
			}
		})
	}
}

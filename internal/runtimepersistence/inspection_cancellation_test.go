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

package runtimepersistence

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/nativetool"
)

// The Approvals screen lists every session's waiting tool questions at once.
// The store pages them oldest first across Captures, skipping questions that
// are decided or already past their deadline.
func TestNativeAllPendingApprovalsSpanCapturesOldestFirst(t *testing.T) {
	store, first, _ := nativeStoreFixture(t, environment.ToolPolicyReview)
	repo := store.NativeToolRepository()
	authority := store.nativeTools
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	base, _ := nativeDecisionFixture(t, store, first)

	captures := newManualCaptureManager(t, store, &manualCaptureClock{now: now}, &manualCaptureRandom{next: 100})
	created, err := captures.Create(ctx, manualcapture.CreateCommand{Owner: manualcapture.NewLocalOwnerScope(), DisplayName: "Second Native", ClientClass: manualcapture.ClientCLI, Lifetime: manualcapture.LifetimeUntilRevoked})
	if err != nil {
		t.Fatal(err)
	}
	other := captureidentity.Reference{Kind: captureidentity.KindManualCapture, ID: created.Capture.ID}
	if other == base.Capture() {
		t.Fatal("fixture Captures collide")
	}
	second, _ := nativeFixtureCapture(t, store, environment.ToolPolicyReview, false, base.Plan(), other, captureassignment.SourceManualCreate, now, nativeFixtureIDs{
		session:     "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		initialize:  "a1111111111111111111111111111111",
		initialized: "a2222222222222222222222222222222",
		list:        "a3333333333333333333333333333333",
		call:        "a4444444444444444444444444444444",
	})
	third := nativetool.ID("55555555555555555555555555555555")
	nativeBindCall(t, repo, base, third, 5, nil, now)
	expiring := nativetool.ID("66666666666666666666666666666666")
	nativeBindCall(t, repo, base, expiring, 6, nil, now)
	decided := nativetool.ID("77777777777777777777777777777777")
	nativeBindCall(t, repo, base, decided, 7, nil, now)

	minute := now.Add(time.Minute)
	request := func(op, id nativetool.ID, created, expires time.Time) {
		t.Helper()
		if _, err := repo.RequestApproval(ctx, op, id, created, expires); err != nil {
			t.Fatal(err)
		}
	}
	request(expiring, "e0000000000000000000000000000000", now, now.Add(5*time.Millisecond))
	request(first, "c1000000000000000000000000000000", now.Add(time.Millisecond), minute)
	request(second, "c2000000000000000000000000000000", now.Add(2*time.Millisecond), minute)
	request(decided, "d0000000000000000000000000000000", now.Add(2*time.Millisecond), minute)
	request(third, "c3000000000000000000000000000000", now.Add(3*time.Millisecond), minute)
	if _, err := repo.DecideApproval(ctx, nativetool.DecisionInput{ApprovalID: "d0000000000000000000000000000000", ExpectedRevision: 1, Decision: nativetool.DecisionDeny, IdempotencyKey: "native-all-pending-deny", ObservedAt: now.Add(4 * time.Millisecond)}); err != nil {
		t.Fatal(err)
	}

	observed := now.Add(10 * time.Millisecond)
	page, err := authority.ListAllPendingApprovals(ctx, nativetool.AllPendingApprovalRequest{ObservedAt: observed, Limit: 2})
	if err != nil || len(page.Items) != 2 || page.NextID != "c2000000000000000000000000000000" || !page.NextCreatedAt.Equal(now.Add(2*time.Millisecond)) {
		t.Fatalf("first page: %+v %v", page, err)
	}
	if page.Items[0].Operation.ID != first || page.Items[0].Operation.Capture != base.Capture() ||
		page.Items[1].Operation.ID != second || page.Items[1].Operation.Capture != other {
		t.Fatalf("first page is not oldest first across both Captures: %+v", page.Items)
	}
	rest, err := authority.ListAllPendingApprovals(ctx, nativetool.AllPendingApprovalRequest{AfterCreatedAt: page.NextCreatedAt, AfterID: page.NextID, ObservedAt: observed, Limit: 2})
	if err != nil || len(rest.Items) != 1 || rest.Items[0].Operation.ID != third || rest.NextID != "" {
		t.Fatalf("last page: %+v %v", rest, err)
	}
	// Before its deadline the expiring question leads the list.
	early, err := authority.ListAllPendingApprovals(ctx, nativetool.AllPendingApprovalRequest{ObservedAt: now.Add(time.Millisecond), Limit: 1})
	if err != nil || len(early.Items) != 1 || early.Items[0].Operation.ID != expiring {
		t.Fatalf("oldest pending question not first: %+v %v", early, err)
	}
	// One Capture's own list is unchanged.
	own, err := authority.ListPendingApprovals(ctx, nativetool.PendingApprovalRequest{Capture: other, Limit: 10})
	if err != nil || len(own.Items) != 1 || own.Items[0].Operation.ID != second {
		t.Fatalf("per-Capture list: %+v %v", own, err)
	}

	for _, bad := range []nativetool.AllPendingApprovalRequest{
		{Limit: 1},
		{ObservedAt: observed},
		{ObservedAt: observed, Limit: 200},
		{ObservedAt: observed, Limit: 1, AfterID: "c2000000000000000000000000000000"},
		{ObservedAt: observed, Limit: 1, AfterCreatedAt: now},
		{ObservedAt: observed, Limit: 1, AfterCreatedAt: now, AfterID: "legacy"},
	} {
		if _, err := authority.ListAllPendingApprovals(ctx, bad); !errors.Is(err, nativetool.ErrInvalid) {
			t.Fatalf("accepted %+v: %v", bad, err)
		}
	}
}

// Listing reads on the screen-read pool: a writer busy forwarding traffic
// neither delays it nor is held up by it.
func TestNativeAllPendingApprovalsReadBesideTheWriter(t *testing.T) {
	store, id, _ := nativeStoreFixture(t, environment.ToolPolicyReview)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	if _, err := store.NativeToolRepository().RequestApproval(ctx, id, "c1000000000000000000000000000000", now, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	writer, err := store.database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback()
	if _, err := writer.ExecContext(ctx, `UPDATE native_tool_approvals SET revision=revision WHERE approval_id='c1000000000000000000000000000000'`); err != nil {
		t.Fatal(err)
	}
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	page, err := store.nativeTools.ListAllPendingApprovals(bounded, nativetool.AllPendingApprovalRequest{ObservedAt: now, Limit: 5})
	if err != nil || len(page.Items) != 1 || page.Items[0].Operation.ID != id {
		t.Fatalf("listing while the writer is held: %+v %v", page, err)
	}
}

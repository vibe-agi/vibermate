package productruntime

import (
	"context"
	"errors"
	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/captureadmission"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/runtimeusage"
	"testing"
	"time"
)

type usagePolicyStore struct {
	runtimeusage.Repository
	next runtimeusage.CollectionPolicy
	err  error
}

func (s *usagePolicyStore) SetUsagePolicy(context.Context, runtimeusage.CollectionPolicy, time.Time) (runtimeusage.CollectionPolicy, error) {
	return s.next, s.err
}
func TestUsagePolicyPublicationAcceptsOnlyMonotonicSuccessfulCommits(t *testing.T) {
	b, _ := recording.NewBudget(recording.Limits{Records: 1, Bytes: 64 << 20})
	q := newObservationRecorder(context.Background(), b, nil, nil)
	defer q.Shutdown(context.Background())
	initial := runtimeusage.CollectionPolicy{Revision: 1, RetentionDays: 30, Enabled: false}
	q.publishUsagePolicy(initial)
	store := &usagePolicyStore{next: runtimeusage.CollectionPolicy{Revision: 2, RetentionDays: 30, Enabled: true}, err: errors.New("CAS conflict")}
	repo := recordingUsageRepository{Repository: store, recorder: q}
	_, _ = repo.SetUsagePolicy(context.Background(), initial, time.Now())
	if p := q.usagePolicy.Load(); p == nil || p.Revision != 1 || p.Enabled {
		t.Fatal("failed commit activated collection or initial publication missing")
	}
	store.err = nil
	_, _ = repo.SetUsagePolicy(context.Background(), initial, time.Now())
	if p := q.usagePolicy.Load(); p == nil || p.Revision != 2 || !p.Enabled {
		t.Fatal("successful commit not published")
	}
	store.next = initial
	_, _ = repo.SetUsagePolicy(context.Background(), initial, time.Now())
	if p := q.usagePolicy.Load(); p.Revision != 2 || !p.Enabled {
		t.Fatal("late successful return rolled back published revision")
	}
}

type collectedUsageSink struct {
	err    error
	values chan runtimeusage.CollectedObservation
}

func (s *collectedUsageSink) RecordUsage(context.Context, runtimeusage.Observation) error {
	panic("worker must use typed collected usage")
}
func (s *collectedUsageSink) PutCollectedUsage(_ context.Context, v runtimeusage.CollectedObservation) error {
	s.values <- v
	return s.err
}
func TestCollectedUsageFreezesAdmittedOwnerPolicyAndLabels(t *testing.T) {
	b, _ := recording.NewBudget(recording.Limits{Records: 2, Bytes: 64 << 20})
	q := newObservationRecorder(context.Background(), b, nil, nil)
	defer q.Shutdown(context.Background())
	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	q.publishUsagePolicy(runtimeusage.CollectionPolicy{Revision: 3, RetentionDays: 7, Enabled: true, CollectingSince: &since})
	admission, err := captureadmission.NewManagedRun(captureadmission.ManagedRunEvidence{CaptureRunID: "run-owned", SourceLabel: "client", RuntimeUserID: "user.AAAAAAAAAAAAAAAAAAAAAAAAAAA", RuntimeUsername: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	writer := &failingPreparedActivity{writes: make(chan activity.Record, 1)}
	usage := &collectedUsageSink{values: make(chan runtimeusage.CollectedObservation, 1)}
	observer := activityAttemptObserver{queue: q, recorder: writer, usage: usage}
	occurred := since.Add(time.Hour)
	err = observer.ObserveTerminal(context.Background(), exchange.AttemptObservation{ObservationStamp: q.ScopeStamp(captureidentity.Reference{}), ExchangeID: "exchange", Outcome: exchange.AttemptSucceeded, Admission: admission, HasAdmission: true, StartedAt: occurred, OccurredAt: occurred, EnvironmentID: "env", EnvironmentName: "Frozen environment", AccountID: "account", AccountName: "Frozen account"})
	if err != nil {
		t.Fatal(err)
	}
	if err = q.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case v := <-usage.values:
		if v.Value.UserID != "user.AAAAAAAAAAAAAAAAAAAAAAAAAAA" || v.Value.Source != "member" || v.Value.Attribution.CallerLabel != "owner" || v.Value.EnvironmentName != "Frozen environment" || v.Value.AccountName != "Frozen account" || v.PolicyRevision != 3 || !v.ExpiresAt.Equal(occurred.Add(7*24*time.Hour)) {
			t.Fatalf("frozen facts lost: %+v", v)
		}
	default:
		t.Fatal("independent collected usage was suppressed")
	}
}

func TestCollectedUsagePolicyDiscardHasDistinctSettlement(t *testing.T) {
	b, _ := recording.NewBudget(recording.Limits{Records: 2, Bytes: 64 << 20})
	q := newObservationRecorder(context.Background(), b, nil, nil)
	defer q.Shutdown(context.Background())
	since := time.Now().UTC().Add(-time.Hour)
	q.publishUsagePolicy(runtimeusage.CollectionPolicy{Enabled: true, RetentionDays: 7, Revision: 1, CollectingSince: &since})
	sink := &collectedUsageSink{values: make(chan runtimeusage.CollectedObservation, 1), err: runtimeusage.ErrObservationDiscarded}
	observer := activityAttemptObserver{queue: q, recorder: &failingPreparedActivity{writes: make(chan activity.Record, 1)}, usage: sink}
	now := time.Now().UTC()
	if err := observer.ObserveTerminal(context.Background(), exchange.AttemptObservation{ObservationStamp: q.ScopeStamp(captureidentity.Reference{}), ExchangeID: "discard", Outcome: exchange.AttemptSucceeded, StartedAt: now, OccurredAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := q.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	stats := b.Snapshot()
	if stats.ByKind[recording.Usage].Settlements[recording.Discarded].Records != 1 || stats.ByKind[recording.Usage].Settlements[recording.Persisted].Records != 0 || stats.ByKind[recording.Activity].Settlements[recording.Unconfirmed].Records != 1 {
		t.Fatalf("policy discard conflated with write failure: %+v", stats)
	}
}

type heldUsageActivity struct {
	failingPreparedActivity
	entered chan struct{}
	release chan struct{}
}

func (s *heldUsageActivity) RecordPrepared(_ context.Context, r activity.Record) (activity.Record, error) {
	close(s.entered)
	<-s.release
	return r, nil
}
func TestDisablingUsageDropsQueuedCollectionWithoutControlLookup(t *testing.T) {
	b, _ := recording.NewBudget(recording.Limits{Records: 2, Bytes: 64 << 20})
	q := newObservationRecorder(context.Background(), b, nil, nil)
	defer q.Shutdown(context.Background())
	since := time.Now().Add(-time.Hour)
	p := runtimeusage.CollectionPolicy{Enabled: true, RetentionDays: 7, Revision: 1, CollectingSince: &since}
	q.publishUsagePolicy(p)
	held := &heldUsageActivity{entered: make(chan struct{}), release: make(chan struct{})}
	sink := &collectedUsageSink{values: make(chan runtimeusage.CollectedObservation, 1)}
	observer := activityAttemptObserver{queue: q, recorder: held, usage: sink}
	now := time.Now()
	if err := observer.ObserveTerminal(context.Background(), exchange.AttemptObservation{ObservationStamp: q.ScopeStamp(captureidentity.Reference{}), ExchangeID: "queued", Outcome: exchange.AttemptSucceeded, StartedAt: now, OccurredAt: now}); err != nil {
		t.Fatal(err)
	}
	<-held.entered
	p.Enabled = false
	p.Revision++
	q.publishUsagePolicy(p)
	close(held.release)
	if err := q.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-sink.values:
		t.Fatal("queued old consent reached usage writer")
	default:
	}
	if b.Snapshot().ByKind[recording.Usage].Settlements[recording.Discarded].Records != 1 {
		t.Fatal("queued consent discard not settled")
	}
	p.Enabled = true
	p.Revision++
	fresh := now.Add(time.Hour)
	p.CollectingSince = &fresh
	q.publishUsagePolicy(p)
	if v := observer.collectUsage(exchange.AttemptObservation{StartedAt: now, OccurredAt: fresh, ExchangeID: "late"}, activity.StatusSucceeded); v != nil {
		t.Fatal("re-enable revived old callback")
	}
}

func (s *heldUsageActivity) RecordScopedPrepared(ctx context.Context, _ recording.Scope, r activity.Record) (activity.Record, error) {
	return s.RecordPrepared(ctx, r)
}

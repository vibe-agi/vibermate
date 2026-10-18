package toolapproval_test

import (
	"context"
	"crypto/rand"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

type clearHeldRepository struct {
	toolapproval.Repository
	createEntered, createReturn, joinEntered, joinReturn, decisionCommitted, decisionReturn chan struct{}
	createOnce, joinOnce, decisionOnce                                                      sync.Once
}

func (r *clearHeldRepository) Create(ctx context.Context, v toolapproval.Record) error {
	r.createOnce.Do(func() {
		if r.createEntered != nil {
			close(r.createEntered)
			<-r.createReturn
		}
	})
	return r.Repository.Create(ctx, v)
}
func (r *clearHeldRepository) Join(ctx context.Context, id string) (toolapproval.Record, error) {
	r.joinOnce.Do(func() {
		if r.joinEntered != nil {
			close(r.joinEntered)
			<-r.joinReturn
		}
	})
	return r.Repository.Join(ctx, id)
}
func (r *clearHeldRepository) Decide(ctx context.Context, c toolapproval.DecisionCommand, now time.Time) (toolapproval.Record, error) {
	v, err := r.Repository.Decide(ctx, c, now)
	r.decisionOnce.Do(func() {
		if r.decisionCommitted != nil {
			close(r.decisionCommitted)
			<-r.decisionReturn
		}
	})
	return v, err
}
func waitClearSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("authority boundary not reached")
	}
}
func TestArchiveClearAuthoritySealsCreatorsAndJoiners(t *testing.T) {
	for _, kind := range []string{"tool", "network"} {
		t.Run(kind, func(t *testing.T) {
			store := openStore(t, filepath.Join(t.TempDir(), "data", "runtime.db"))
			defer shutdownStore(t, store)
			repo := &clearHeldRepository{Repository: store.ToolApprovalRepository(), createEntered: make(chan struct{}), createReturn: make(chan struct{}), joinEntered: make(chan struct{}), joinReturn: make(chan struct{})}
			var releaseCreate, releaseJoin sync.Once
			defer releaseCreate.Do(func() { close(repo.createReturn) })
			defer releaseJoin.Do(func() { close(repo.joinReturn) })
			authority, err := toolapproval.New(t.Context(), toolapproval.Options{Repository: repo, Clock: toolapproval.SystemClock{}, Random: rand.Reader, Config: toolapproval.Config{DecisionTimeout: time.Minute}})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				releaseCreate.Do(func() { close(repo.createReturn) })
				releaseJoin.Do(func() { close(repo.joinReturn) })
				shutdownAuthority(t, authority)
			}()
			results := make(chan bool, 3)
			failures := make(chan error, 3)
			ask := func() {
				if kind == "tool" {
					v, err := authority.Decide(context.Background(), decisionRequest(t, `{"path":"fixture"}`))
					results <- v.Outcome == exchange.ToolDecisionApproved
					failures <- err
				} else {
					v, err := authority.AskNetwork(context.Background(), askRequest())
					results <- v.Allowed
					failures <- err
				}
			}
			go ask()
			waitClearSignal(t, repo.createEntered)
			blocked, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
			lease, err := authority.BeginArchiveClear(blocked)
			cancel()
			if lease != nil {
				lease.Complete(false)
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("clear crossed admitted creator: %v", err)
			}
			go ask()
			releaseCreate.Do(func() { close(repo.createReturn) })
			waitClearSignal(t, repo.joinEntered)
			blocked, cancel = context.WithTimeout(t.Context(), 30*time.Millisecond)
			lease, err = authority.BeginArchiveClear(blocked)
			cancel()
			if lease != nil {
				lease.Complete(false)
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("clear crossed admitted joiner: %v", err)
			}
			releaseJoin.Do(func() { close(repo.joinReturn) })
			lease, err = authority.BeginArchiveClear(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if _, err = repo.CancelPending(t.Context(), "archive_cleared", time.Now()); err != nil {
				lease.Complete(false)
				t.Fatal(err)
			}
			lease.Complete(true)
			for i := 0; i < 2; i++ {
				select {
				case allowed := <-results:
					if allowed {
						t.Fatal("old pending question was allowed after clear")
					}
				case <-time.After(3 * time.Second):
					t.Fatal("old waiter abandoned")
				}
				if err = <-failures; err != nil {
					t.Fatal(err)
				}
			}
			go ask()
			pending := waitForPending(t, authority)
			if _, err = authority.DecideApproval(t.Context(), toolapproval.DecisionCommand{ApprovalID: pending.ID, ExpectedRevision: pending.Revision, IdempotencyKey: "after-clear-decision-0001", Decision: toolapproval.DecisionAllowOnce, Scope: toolapproval.ScopeRequest}); err != nil {
				t.Fatal(err)
			}
			select {
			case allowed := <-results:
				if !allowed {
					t.Fatal("fresh same aggregate joined cleared entry")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("fresh question did not resolve")
			}
			if err = <-failures; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestArchiveClearAuthorityOrdersDecisionPublicationAndFailure(t *testing.T) {
	store := openStore(t, filepath.Join(t.TempDir(), "data", "runtime.db"))
	defer shutdownStore(t, store)
	repo := &clearHeldRepository{Repository: store.ToolApprovalRepository(), decisionCommitted: make(chan struct{}), decisionReturn: make(chan struct{})}
	var releaseDecision sync.Once
	authority, err := toolapproval.New(t.Context(), toolapproval.Options{Repository: repo, Clock: toolapproval.SystemClock{}, Random: rand.Reader, Config: toolapproval.Config{DecisionTimeout: time.Minute}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { releaseDecision.Do(func() { close(repo.decisionReturn) }); shutdownAuthority(t, authority) }()
	results := make(chan bool, 1)
	go func() {
		v, err := authority.AskNetwork(context.Background(), askRequest())
		results <- err == nil && v.Allowed
	}()
	pending := waitForPending(t, authority)
	// Required failure is a release, not a successful reset/cancellation.
	lease, err := authority.BeginArchiveClear(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	lease.Complete(false)
	select {
	case <-results:
		t.Fatal("failed required clear canceled a live question")
	default:
	}
	decided := make(chan error, 1)
	go func() {
		_, err := authority.DecideApproval(context.Background(), toolapproval.DecisionCommand{ApprovalID: pending.ID, ExpectedRevision: pending.Revision, IdempotencyKey: "before-clear-decision-0001", Decision: toolapproval.DecisionAllowOnce, Scope: toolapproval.ScopeRequest})
		decided <- err
	}()
	waitClearSignal(t, repo.decisionCommitted)
	stored, err := repo.Get(t.Context(), pending.ID)
	if err != nil || stored.State != toolapproval.StateAllowed {
		t.Fatal("decision did not reach actual commit", err)
	}
	blocked, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	lease, err = authority.BeginArchiveClear(blocked)
	cancel()
	if lease != nil {
		lease.Complete(false)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("clear crossed committed but unpublished decision: %v", err)
	}
	select {
	case <-results:
		t.Fatal("waiter released before decision publication")
	default:
	}
	releaseDecision.Do(func() { close(repo.decisionReturn) })
	if err = <-decided; err != nil {
		t.Fatal(err)
	}
	lease, err = authority.BeginArchiveClear(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.CancelPending(t.Context(), "archive_cleared", time.Now()); err != nil {
		lease.Complete(false)
		t.Fatal(err)
	}
	lease.Complete(true)
	select {
	case allowed := <-results:
		if !allowed {
			t.Fatal("already published allow lost historical result")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("historical result abandoned")
	}
}

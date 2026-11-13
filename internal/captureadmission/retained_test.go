package captureadmission

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureauthority"
)

type controlledAuthority struct {
	read func(context.Context, Admission) (captureauthority.Validity, error)
}

func (a controlledAuthority) Authorize(context.Context, ProxyCredential) (Admission, error) {
	return Admission{}, errors.New("test does not authenticate")
}
func (a controlledAuthority) Revalidate(ctx context.Context, admission Admission) (captureauthority.Validity, error) {
	return a.read(ctx, admission)
}
func retainedTestAdmission(t *testing.T) (Admission, captureauthority.Validity) {
	t.Helper()
	a, err := NewManual("manual.test", 1, "test")
	if err != nil {
		t.Fatal(err)
	}
	a.receipt = &retainedReceipt{issuer: &compositeAuthorizer{}}
	ref, _ := a.CaptureReference()
	return a, captureauthority.Validity{Capture: ref, CredentialRevision: 1}
}
func awaitRetained(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("retained owner did not complete")
	}
}

// Shutdown must join ownership even after the entry's permission was removed.
func TestRetainedShutdownJoinsWithdrawnSubscription(t *testing.T) {
	admission, validity := retainedTestAdmission(t)
	var mu sync.Mutex
	valid := true
	r := NewRetained(t.Context(), controlledAuthority{read: func(context.Context, Admission) (captureauthority.Validity, error) {
		mu.Lock()
		defer mu.Unlock()
		if !valid {
			return captureauthority.Validity{}, errors.New("revoked")
		}
		return validity, nil
	}}, time.Now)
	canceled := make(chan struct{})
	release, err := r.Retain(t.Context(), admission, func() { close(canceled) })
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	mu.Lock()
	valid = false
	mu.Unlock()
	wait := r.BeginChange(captureauthority.Change{Capture: validity.Capture})
	awaitRetained(t, canceled)
	stopped := make(chan struct{})
	go func() { _ = r.Shutdown(t.Context()); close(stopped) }()
	select {
	case <-stopped:
		t.Error("shutdown returned before withdrawn connection released")
	case <-time.After(30 * time.Millisecond):
	}
	release()
	awaitRetained(t, stopped)
	if err := wait(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestRetainedRegistrationFencesConcurrentWithdrawal(t *testing.T) {
	admission, validity := retainedTestAdmission(t)
	entered, continueRead := make(chan struct{}), make(chan struct{})
	var once sync.Once
	r := NewRetained(t.Context(), controlledAuthority{read: func(ctx context.Context, _ Admission) (captureauthority.Validity, error) {
		first := false
		once.Do(func() { first = true; close(entered) })
		if first {
			<-continueRead
			return validity, nil
		}
		return captureauthority.Validity{}, errors.New("revoked")
	}}, time.Now)
	defer r.Shutdown(t.Context())
	done := make(chan struct{})
	var err error
	go func() {
		defer close(done)
		var release func()
		release, err = r.Retain(t.Context(), admission, func() {})
		if release != nil {
			release()
		}
	}()
	awaitRetained(t, entered)
	wait := r.BeginChange(captureauthority.Change{Capture: validity.Capture})
	close(continueRead)
	awaitRetained(t, done)
	if err == nil {
		t.Fatal("stale successful source read admitted withdrawn connection")
	}
	if err := wait(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestRetainedStaleDeadlineCannotWithdrawRenewedAuthority(t *testing.T) {
	admission, validity := retainedTestAdmission(t)
	validity.ExpiresAt = time.Now().Add(60 * time.Millisecond)
	entered, continueRead := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	reads := 0
	r := NewRetained(t.Context(), controlledAuthority{read: func(context.Context, Admission) (captureauthority.Validity, error) {
		mu.Lock()
		reads++
		n := reads
		mu.Unlock()
		if n == 2 {
			close(entered)
			<-continueRead
			return captureauthority.Validity{}, errors.New("old deadline")
		}
		v := validity
		if n > 2 {
			v.ExpiresAt = time.Now().Add(time.Hour)
		}
		return v, nil
	}}, time.Now)
	canceled := make(chan struct{})
	release, err := r.Retain(t.Context(), admission, func() { close(canceled) })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { release(); r.Shutdown(t.Context()) }()
	awaitRetained(t, entered)
	wait := r.BeginChange(captureauthority.Change{Capture: validity.Capture})
	close(continueRead)
	if err := wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-canceled:
		t.Fatal("stale expiry withdrew renewed authority")
	default:
	}
}

func TestRetainedShutdownCancelsAndJoinsLookup(t *testing.T) {
	admission, _ := retainedTestAdmission(t)
	entered, exited := make(chan struct{}), make(chan struct{})
	r := NewRetained(t.Context(), controlledAuthority{read: func(ctx context.Context, _ Admission) (captureauthority.Validity, error) {
		close(entered)
		<-ctx.Done()
		defer close(exited)
		return captureauthority.Validity{}, ctx.Err()
	}}, time.Now)
	returned := make(chan struct{})
	go func() {
		defer close(returned)
		release, _ := r.Retain(t.Context(), admission, func() {})
		if release != nil {
			release()
		}
	}()
	awaitRetained(t, entered)
	if err := r.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	awaitRetained(t, exited)
	awaitRetained(t, returned)
}

func TestRetainedCannotBorrowReceiptByCaptureID(t *testing.T) {
	admission, validity := retainedTestAdmission(t)
	admission.receipt = &retainedReceipt{issuer: &compositeAuthorizer{}}
	r := NewRetained(t.Context(), controlledAuthority{read: func(context.Context, Admission) (captureauthority.Validity, error) { return validity, nil }}, time.Now)
	release, err := r.Retain(t.Context(), admission, func() {})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { release(); r.Shutdown(t.Context()) }()
	forged, _ := NewManual("manual.test", 1, "test")
	borrowed, err := r.Retain(t.Context(), forged, func() {})
	if borrowed != nil {
		borrowed()
	}
	if err == nil {
		t.Fatal("public attribution constructor borrowed another connection's sealed receipt")
	}
}

func TestRetainedDelayedHintsPreserveNewGenerationAndSession(t *testing.T) {
	admission, validity := retainedTestAdmission(t)
	admission.credentialRevision = 2
	validity.CredentialRevision = 2
	validity.UserID = "user.synthetic"
	validity.SessionDigest[0] = 2
	r := NewRetained(t.Context(), controlledAuthority{read: func(context.Context, Admission) (captureauthority.Validity, error) { return validity, nil }}, time.Now)
	canceled := make(chan struct{})
	release, err := r.Retain(t.Context(), admission, func() { close(canceled) })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { release(); r.Shutdown(t.Context()) }()
	for _, change := range []captureauthority.Change{{Capture: validity.Capture}, {UserID: validity.UserID}, {SessionDigest: [32]byte{1}}} {
		if err := r.Changed(t.Context(), change); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-canceled:
		t.Fatal("delayed event label withdrew current authority")
	default:
	}
}

func TestRetainedWithdrawnReceiptCannotBeReanimated(t *testing.T) {
	admission, validity := retainedTestAdmission(t)
	var mu sync.Mutex
	valid := true
	r := NewRetained(t.Context(), controlledAuthority{read: func(context.Context, Admission) (captureauthority.Validity, error) {
		mu.Lock()
		defer mu.Unlock()
		if !valid {
			return captureauthority.Validity{}, errors.New("expired")
		}
		return validity, nil
	}}, time.Now)
	defer r.Shutdown(t.Context())
	canceled := make(chan struct{})
	release, err := r.Retain(t.Context(), admission, func() { close(canceled) })
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	valid = false
	mu.Unlock()
	wait := r.BeginChange(captureauthority.Change{Capture: validity.Capture})
	awaitRetained(t, canceled)
	release()
	if err = wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	valid = true
	mu.Unlock() // A control heartbeat may revive the durable run.
	revived, err := r.Retain(t.Context(), admission, func() {})
	if revived != nil {
		revived()
	}
	if err == nil {
		t.Fatal("withdrawn receipt became live again without fresh authentication")
	}
	fresh := admission
	fresh.receipt = &retainedReceipt{issuer: admission.receipt.issuer}
	next, err := r.Retain(t.Context(), fresh, func() {})
	if err != nil {
		t.Fatal("fresh admission did not recover current grant", err)
	}
	next()
}

// A rejected final read returns permission failure, not completed cleanup.
func TestRetainedPendingFailureKeepsOuterCleanupOwned(t *testing.T) {
	for _, cause := range []string{"withdrawal", "caller_canceled"} {
		t.Run(cause, func(t *testing.T) {
			admission, validity := retainedTestAdmission(t)
			entered := make(chan struct{})
			var first atomic.Bool
			r := NewRetained(t.Context(), controlledAuthority{read: func(ctx context.Context, _ Admission) (captureauthority.Validity, error) {
				if first.CompareAndSwap(false, true) {
					close(entered)
					<-ctx.Done()
				}
				return captureauthority.Validity{}, errors.New("withdrawn")
			}}, time.Now)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			type outcome struct {
				release func()
				err     error
			}
			result := make(chan outcome, 1)
			go func() { release, err := r.Retain(ctx, admission, func() {}); result <- outcome{release, err} }()
			awaitRetained(t, entered)
			var wait func(context.Context) error
			if cause == "withdrawal" {
				wait = r.BeginChange(captureauthority.Change{Capture: validity.Capture})
			} else {
				cancel()
			}
			var got outcome
			select {
			case got = <-result:
			case <-time.After(time.Second):
				t.Fatal("pending admission did not reject")
			}
			if got.release != nil {
				defer got.release()
			}
			if got.err == nil {
				t.Error("pending admission did not fail")
			}
			if got.release == nil {
				t.Error("inserted subscription lost its cleanup handle on error")
			}
			joined := make(chan struct{})
			go func() {
				if wait != nil {
					_ = wait(t.Context())
				} else {
					_ = r.Shutdown(t.Context())
				}
				close(joined)
			}()
			select {
			case <-joined:
				t.Error("rejected admission cleanup was reported joined before outer release")
			case <-time.After(30 * time.Millisecond):
			}
			if got.release != nil {
				got.release()
			}
			awaitRetained(t, joined)
			if err := r.Shutdown(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRetainedLaterChangeJoinsWithdrawnOwnership(t *testing.T) {
	for _, known := range []bool{false, true} {
		t.Run(map[bool]string{false: "first_read_failed", true: "previously_valid"}[known], func(t *testing.T) {
			admission, validity := retainedTestAdmission(t)
			validity.UserID = "user.original"
			validity.SessionDigest[0] = 1
			var mu sync.Mutex
			valid := known
			r := NewRetained(t.Context(), controlledAuthority{read: func(context.Context, Admission) (captureauthority.Validity, error) {
				mu.Lock()
				defer mu.Unlock()
				if !valid {
					return captureauthority.Validity{}, errors.New("withdrawn")
				}
				return validity, nil
			}}, time.Now)
			canceled := make(chan struct{})
			release, err := r.Retain(t.Context(), admission, func() { close(canceled) })
			if known && err != nil {
				t.Fatal(err)
			}
			if release != nil {
				defer release()
			}
			if known {
				mu.Lock()
				valid = false
				mu.Unlock()
				r.BeginChange(captureauthority.Change{Capture: validity.Capture})
			}
			awaitRetained(t, canceled)
			for _, change := range []captureauthority.Change{{Capture: validity.Capture}, {UserID: validity.UserID}, {SessionDigest: validity.SessionDigest}} {
				joined := make(chan struct{})
				wait := r.BeginChange(change)
				go func() { _ = wait(t.Context()); close(joined) }()
				select {
				case <-joined:
					t.Error("later matching change missed withdrawn cleanup")
				case <-time.After(30 * time.Millisecond):
				}
				// Collect the bounded waiter after releasing once all scopes were checked.
				t.Cleanup(func() { awaitRetained(t, joined) })
			}
			unrelatedCapture := validity.Capture
			unrelatedCapture.ID = "manual.unrelated"
			otherContext, stopOther := context.WithTimeout(t.Context(), time.Second)
			if err := r.Changed(otherContext, captureauthority.Change{Capture: unrelatedCapture}); err != nil {
				t.Error("unrelated Capture waited for withdrawn cleanup", err)
			}
			stopOther()
			if known {
				other := captureauthority.Change{SessionDigest: [32]byte{2}}
				ctx, stop := context.WithTimeout(t.Context(), time.Second)
				defer stop()
				if err := r.Changed(ctx, other); err != nil {
					t.Fatal("unrelated exact Session waited for withdrawn cleanup", err)
				}
			}
			if release != nil {
				release()
			}
			if err := r.Shutdown(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRetainedLaterChangeJoinsOldGenerationWithoutCancelingReplacement(t *testing.T) {
	admission, validity := retainedTestAdmission(t)
	var valid atomic.Bool
	valid.Store(true)
	r := NewRetained(t.Context(), controlledAuthority{read: func(_ context.Context, a Admission) (captureauthority.Validity, error) {
		if a.CredentialRevision() == 1 && !valid.Load() {
			return captureauthority.Validity{}, errors.New("rotated")
		}
		v := validity
		v.CredentialRevision = a.CredentialRevision()
		return v, nil
	}}, time.Now)
	oldCanceled, newCanceled := make(chan struct{}), make(chan struct{})
	oldRelease, err := r.Retain(t.Context(), admission, func() { close(oldCanceled) })
	if err != nil {
		t.Fatal(err)
	}
	defer oldRelease()
	valid.Store(false)
	r.BeginChange(captureauthority.Change{Capture: validity.Capture})
	awaitRetained(t, oldCanceled)
	next := admission
	next.credentialRevision = 2
	next.receipt = &retainedReceipt{issuer: admission.receipt.issuer}
	newRelease, err := r.Retain(t.Context(), next, func() { close(newCanceled) })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { newRelease(); oldRelease(); r.Shutdown(t.Context()) }()
	joined := make(chan struct{})
	wait := r.BeginChange(captureauthority.Change{Capture: validity.Capture})
	go func() { _ = wait(t.Context()); close(joined) }()
	select {
	case <-joined:
		t.Error("replacement entry hid old generation's outstanding cleanup")
	case <-time.After(30 * time.Millisecond):
	}
	oldRelease()
	awaitRetained(t, joined)
	select {
	case <-newCanceled:
		t.Fatal("joining old cleanup canceled valid replacement generation")
	default:
	}
	if _, err := r.Revalidate(t.Context(), next); err != nil {
		t.Fatal("replacement lost current permission", err)
	}
}

// stallingAuthority answers the first read, then fails every later read with
// the given error until told to recover.
type stallingAuthority struct {
	validity  captureauthority.Validity
	failure   error
	reads     atomic.Int64
	recovered atomic.Bool
	stalled   chan struct{}
	once      sync.Once
}

func (a *stallingAuthority) read(context.Context, Admission) (captureauthority.Validity, error) {
	n := a.reads.Add(1)
	if n == 1 {
		return a.validity, nil
	}
	if a.recovered.Load() {
		v := a.validity
		v.ExpiresAt = time.Now().Add(time.Hour)
		return v, nil
	}
	a.once.Do(func() { close(a.stalled) })
	return captureauthority.Validity{}, a.failure
}

func stalledRead() error { return errors.Join(ErrCredentialRejected, context.DeadlineExceeded) }

// A control read that only times out must not end work that was admitted by
// the last successful read: a slow store is not a revocation.
func TestRetainedReadTimeoutKeepsRunningWorkAndRecovers(t *testing.T) {
	retainedUnansweredReadKeepsRunningWork(t, stalledRead())
}

// A read that could not reach storage is the same non-answer as a timeout.
func TestRetainedStorageFailureKeepsRunningWorkAndRecovers(t *testing.T) {
	retainedUnansweredReadKeepsRunningWork(t, errors.Join(ErrCredentialRejected, captureauthority.ErrUnavailable, errors.New("disk I/O error")))
}

func retainedUnansweredReadKeepsRunningWork(t *testing.T, failure error) {
	admission, validity := retainedTestAdmission(t)
	validity.ExpiresAt = time.Now().Add(40 * time.Millisecond)
	authority := &stallingAuthority{validity: validity, failure: failure, stalled: make(chan struct{})}
	r := NewRetained(t.Context(), controlledAuthority{read: authority.read}, time.Now)
	canceled := make(chan struct{})
	release, err := r.Retain(t.Context(), admission, func() { close(canceled) })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { release(); r.Shutdown(t.Context()) }()
	awaitRetained(t, authority.stalled)
	stalledReads := authority.reads.Load()
	authority.recovered.Store(true)
	deadline := time.After(5 * time.Second)
	for authority.reads.Load() == stalledReads {
		select {
		case <-canceled:
			t.Fatal("a timed-out validity read withdrew running work")
		case <-deadline:
			t.Fatal("the stalled read was never retried")
		case <-time.After(20 * time.Millisecond):
		}
	}
	select {
	case <-canceled:
		t.Fatal("running work was withdrawn after the store recovered")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestRetainedReadTimeoutBeyondGraceWithdraws(t *testing.T) {
	admission, validity := retainedTestAdmission(t)
	validity.ExpiresAt = time.Now().Add(40 * time.Millisecond)
	authority := &stallingAuthority{validity: validity, failure: stalledRead(), stalled: make(chan struct{})}
	var skew atomic.Int64
	clock := func() time.Time { return time.Now().Add(time.Duration(skew.Load())) }
	r := NewRetained(t.Context(), controlledAuthority{read: authority.read}, clock)
	canceled := make(chan struct{})
	release, err := r.Retain(t.Context(), admission, func() { close(canceled) })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { release(); r.Shutdown(t.Context()) }()
	awaitRetained(t, authority.stalled)
	select {
	case <-canceled:
		t.Fatal("withdrawn before the grace elapsed")
	default:
	}
	skew.Store(int64(revalidationStallGrace + time.Second))
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("a store that stayed unreadable past the grace kept its work")
	}
}

// An explicit Change is a revocation hint. It is never waited out.
func TestRetainedChangeDuringStallWithdraws(t *testing.T) {
	admission, validity := retainedTestAdmission(t)
	validity.ExpiresAt = time.Now().Add(40 * time.Millisecond)
	authority := &stallingAuthority{validity: validity, failure: stalledRead(), stalled: make(chan struct{})}
	r := NewRetained(t.Context(), controlledAuthority{read: authority.read}, time.Now)
	canceled := make(chan struct{})
	release, err := r.Retain(t.Context(), admission, func() { close(canceled) })
	if err != nil {
		t.Fatal(err)
	}
	defer r.Shutdown(t.Context())
	awaitRetained(t, authority.stalled)
	wait := r.BeginChange(captureauthority.Change{Capture: validity.Capture})
	awaitRetained(t, canceled)
	release()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := wait(ctx); err != nil {
		t.Fatal(err)
	}
}

// Only a timeout is tolerated. Any other failed read is still a withdrawal.
func TestRetainedRejectedRereadStillWithdraws(t *testing.T) {
	admission, validity := retainedTestAdmission(t)
	validity.ExpiresAt = time.Now().Add(40 * time.Millisecond)
	authority := &stallingAuthority{validity: validity, failure: ErrCredentialRejected, stalled: make(chan struct{})}
	r := NewRetained(t.Context(), controlledAuthority{read: authority.read}, time.Now)
	canceled := make(chan struct{})
	release, err := r.Retain(t.Context(), admission, func() { close(canceled) })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { release(); r.Shutdown(t.Context()) }()
	awaitRetained(t, canceled)
}

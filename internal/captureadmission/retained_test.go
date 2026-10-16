package captureadmission

import (
	"context"
	"errors"
	"sync"
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

package runtimepersistence

import (
	"context"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"path/filepath"
	"testing"
	"time"
)

func TestRetainedManualValidityIsReadOnlyAndHonorsExpiryAndRotation(t *testing.T) {
	clock := &manualCaptureClock{now: time.Now().UTC().Truncate(time.Millisecond)}
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	manager := newManualCaptureManager(t, store, clock, &manualCaptureRandom{})
	owner := manualcapture.NewLocalOwnerScope()
	grant, err := manager.Create(t.Context(), manualcapture.CreateCommand{Owner: owner, DisplayName: "retained", ClientClass: manualcapture.ClientCLI, Lifetime: manualcapture.LifetimeTemporary, ExpiresIn: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := manualcapture.ParseID(grant.Capture.ID)
	validity, err := manager.ProxyValidity(t.Context(), grant.Credential)
	if err != nil || !validity.ExpiresAt.Equal(clock.Now().Add(time.Minute)) {
		t.Fatalf("validity deadline=%v err=%v", validity.ExpiresAt, err)
	}
	view, err := manager.Get(t.Context(), owner, id)
	if err != nil || view.Observation != manualcapture.ObservationWaiting || !view.UpdatedAt.Equal(grant.Capture.UpdatedAt) {
		t.Fatal("validity read recorded observed traffic", err)
	}
	rotated, err := manager.Rotate(t.Context(), manualcapture.RotateCommand{Owner: owner, ID: id, ExpectedCredentialRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.ProxyValidity(t.Context(), grant.Credential); err == nil {
		t.Fatal("old credential still valid")
	}
	validity, err = manager.ProxyValidity(t.Context(), rotated.Credential)
	if err != nil || validity.CredentialRevision != 2 {
		t.Fatalf("new credential revision=%d err=%v", validity.CredentialRevision, err)
	}
	clock.Set(clock.Now().Add(time.Minute))
	if _, err = manager.ProxyValidity(t.Context(), rotated.Credential); err == nil {
		t.Fatal("expired receipt remains valid")
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = manager.ProxyValidity(canceled, rotated.Credential); err == nil {
		t.Fatal("canceled lookup allowed")
	}
}

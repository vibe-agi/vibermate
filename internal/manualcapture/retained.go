package manualcapture

import (
	"context"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureauthority"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
)

// ProxyValidity reads current permission without recording observed traffic.
func (manager *Manager) ProxyValidity(ctx context.Context, credential ProxyCredential) (captureauthority.Validity, error) {
	reader, ok := manager.repository.(interface {
		ProxyValidity(context.Context, CredentialDigest, time.Time) (captureauthority.Validity, error)
	})
	if !ok {
		return captureauthority.Validity{}, ErrCredentialRejected
	}
	operation, finish, err := manager.lifecycle.Begin(ctx)
	if err != nil {
		return captureauthority.Validity{}, err
	}
	defer finish()
	return reader.ProxyValidity(operation, credentialDigest(credential.value), manager.now())
}

func (manager *Manager) notifyAuthorityChange(ctx context.Context, id string) error {
	observer, ok := manager.nativeRevocation.(captureauthority.Observer)
	if !ok {
		return nil
	}
	capture, err := captureidentity.New(captureidentity.KindManualCapture, id)
	if err != nil {
		return err
	}
	return observer.Changed(ctx, captureauthority.Change{Capture: capture})
}

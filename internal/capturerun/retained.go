package capturerun

import (
	"context"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureauthority"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
)

// ProxyValidity reads current permission without recording observed traffic.
func (manager *Manager) ProxyValidity(ctx context.Context, credential ProxyCapability) (captureauthority.Validity, error) {
	reader, ok := manager.repository.(interface {
		ProxyValidity(context.Context, CapabilityDigest, time.Time) (captureauthority.Validity, error)
	})
	if !ok {
		return captureauthority.Validity{}, ErrCapabilityRejected
	}
	operation, finish, err := manager.lifecycle.begin(ctx)
	if err != nil {
		return captureauthority.Validity{}, err
	}
	defer finish()
	return reader.ProxyValidity(operation, capabilityDigest(proxyDigestDomain, credential.value), manager.clock.Now().UTC())
}

func (manager *Manager) notifyAuthorityChange(ctx context.Context, id string) error {
	observer, ok := manager.nativeRevocation.(captureauthority.Observer)
	if !ok {
		return nil
	}
	capture, err := captureidentity.New(captureidentity.KindManagedRun, id)
	if err != nil {
		return err
	}
	return observer.Changed(ctx, captureauthority.Change{Capture: capture})
}

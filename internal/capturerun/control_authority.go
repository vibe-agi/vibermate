package capturerun

import (
	"context"
	"time"
)

// ControlAuthorizer validates authority without attaching a PID, extending a
// lease, or claiming HTTP observation. Registration may precede child start.
type ControlAuthorizer interface {
	AuthorizeControl(context.Context, string, ControlCapability) (View, error)
}

type controlAuthorityRepository interface {
	AuthorizeControl(context.Context, string, CapabilityDigest, time.Time) (DurableRecord, error)
}

func (manager *Manager) AuthorizeControl(ctx context.Context, id string, capability ControlCapability) (View, error) {
	if manager == nil || validateID(id) != nil || validateCapability(capability.value) != nil {
		return View{}, ErrCapabilityRejected
	}
	repository, ok := manager.repository.(controlAuthorityRepository)
	if !ok {
		return View{}, ErrCapabilityRejected
	}
	operation, finish, err := manager.lifecycle.begin(ctx)
	if err != nil {
		return View{}, err
	}
	defer finish()
	record, err := repository.AuthorizeControl(operation, id, capabilityDigest(controlDigestDomain, capability.value), manager.clock.Now().UTC())
	if err != nil {
		return View{}, err
	}
	if record.Validate() != nil || !record.State.active() {
		return View{}, ErrCapabilityRejected
	}
	return ViewOf(record), nil
}

package acpobservation

import (
	"context"
	"errors"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"testing"
)

type unavailableControl struct{ err error }

func (u unavailableControl) AuthorizeControl(context.Context, string, capturerun.ControlCapability) (capturerun.View, error) {
	return capturerun.View{}, u.err
}
func TestACPAuthorityUnavailabilityIsNotRevocation(t *testing.T) {
	unavailable := errors.New("control file unavailable")
	m := &Manager{runs: unavailableControl{unavailable}}
	if _, err := m.Start(context.Background(), "run", capturerun.ControlCapability{}, false); !errors.Is(err, unavailable) {
		t.Fatalf("temporary authority failure became rejection: %v", err)
	}
	if err := m.Publish(context.Background(), "run", capturerun.ControlCapability{}, Snapshot{}); !errors.Is(err, unavailable) {
		t.Fatalf("temporary authority failure became rejection: %v", err)
	}
	m.runs = unavailableControl{capturerun.ErrCapabilityRejected}
	if err := m.Publish(context.Background(), "run", capturerun.ControlCapability{}, Snapshot{}); !errors.Is(err, capturerun.ErrCapabilityRejected) {
		t.Fatalf("actual rejection lost: %v", err)
	}
}

package offlinehold

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func startedGateWithAction(
	t *testing.T,
	actionID string,
) (*Gate, *ActionLease) {
	t.Helper()

	gate := startTestGate(t, Config{
		MaxHeldRequests:    8,
		MaxHeldBytes:       1 << 20,
		MaxHoldDuration:    time.Second,
		ReleaseConcurrency: 1,
	})
	action, err := gate.BeginAction(
		context.Background(),
		ActionRequest{ActionID: actionID},
	)
	if err != nil {
		t.Fatalf("BeginAction(%q) error = %v", actionID, err)
	}
	return gate, action
}

func identityProbeTarget() ProbeTarget {
	return providerTarget("identity")
}

func coordinatorSource(t *testing.T) string {
	t.Helper()

	data, err := os.ReadFile("coordinator.go")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// ADR-0015 section 10 forbids deriving a relationship from an identity string.
// Membership in an action is proved by the typed ActionLease the caller already
// holds, so an egress request ID is independent of it.
func TestAcquireAcceptsAnIndependentRequestIdentity(t *testing.T) {
	t.Parallel()

	gate, action := startedGateWithAction(t, "action-independent")
	defer func() { gate.BeginShutdown() }()

	lease, err := gate.Acquire(context.Background(), AcquireRequest{
		RequestID: "egress-unrelated-identity",
		Action:    action,
		Target:    identityProbeTarget(),
	})
	if err != nil {
		t.Fatalf("independent request identity was rejected: %v", err)
	}
	lease.Release()
}

func TestAcquireRejectsAnActionThisGateDoesNotOwn(t *testing.T) {
	t.Parallel()

	gate, _ := startedGateWithAction(t, "action-owned")
	defer func() { gate.BeginShutdown() }()
	other, foreign := startedGateWithAction(t, "action-foreign")
	defer func() { other.BeginShutdown() }()

	if _, err := gate.Acquire(context.Background(), AcquireRequest{
		RequestID: "egress-1",
		Action:    foreign,
		Target:    identityProbeTarget(),
	}); err == nil {
		t.Fatal("a foreign action lease was accepted")
	}
}

func TestAcquireRejectsAMissingAction(t *testing.T) {
	t.Parallel()

	gate, _ := startedGateWithAction(t, "action-missing")
	defer func() { gate.BeginShutdown() }()

	if _, err := gate.Acquire(context.Background(), AcquireRequest{
		RequestID: "egress-1",
		Target:    identityProbeTarget(),
	}); err == nil {
		t.Fatal("a nil action lease was accepted")
	}
}

// A released action can no longer admit egress, and that must not depend on
// how the request identity happens to be spelled.
func TestAcquireRejectsAReleasedAction(t *testing.T) {
	t.Parallel()

	gate, action := startedGateWithAction(t, "action-released")
	defer func() { gate.BeginShutdown() }()
	action.Release()

	for _, requestID := range []string{
		"action-released",
		"action-released/attempt-1",
		"egress-unrelated",
	} {
		if _, err := gate.Acquire(context.Background(), AcquireRequest{
			RequestID: requestID,
			Action:    action,
			Target:    identityProbeTarget(),
		}); err == nil {
			t.Fatalf("released action admitted request %q", requestID)
		}
	}
}

func TestCoordinatorDoesNotParseIdentityContainment(t *testing.T) {
	t.Parallel()

	source := coordinatorSource(t)
	if strings.Contains(source, `actionID+"/"`) ||
		strings.Contains(source, "actionID + \"/\"") {
		t.Fatal("the coordinator still derives membership from an identity prefix")
	}
}

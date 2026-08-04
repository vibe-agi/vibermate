package runlauncher

import (
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/codesignature"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

// The three budgets are chosen in three packages that do not import one
// another: how long a signature may take to verify, how long a person may be
// asked to decide, and how long the launcher waits for a create. Nothing
// connected them, which is how the outer one came to be a small fraction of
// the sum of the inner ones and stayed that way through review.
//
// This is the connection. It lives in a test rather than in a constant
// expression so the launcher binary does not link the approval subsystem to
// obtain a number.
func TestTheCreateBudgetCoversWhatACreateCanContain(t *testing.T) {
	t.Parallel()

	inner := codesignature.VerifyDeadline + toolapproval.DefaultClientRootGrace

	if defaultCreateTimeout < inner {
		t.Fatalf(
			"the create budget is %v but a create can honestly take %v "+
				"(%v verifying a signature, %v waiting for a person); the "+
				"recognized tier cannot complete inside it",
			defaultCreateTimeout,
			inner,
			codesignature.VerifyDeadline,
			toolapproval.DefaultClientRootGrace,
		)
	}
	// A ceiling too, so that widening one of the inner bounds cannot quietly
	// turn an unattended launch into a multi-minute hang.
	if defaultCreateTimeout > inner+30*time.Second {
		t.Fatalf(
			"the create budget is %v, far above the %v it must cover; a "+
				"launch that cannot reach anyone should start without a Root "+
				"promptly rather than hold the terminal",
			defaultCreateTimeout,
			inner,
		)
	}
}

// The pinned loopback client has its own response-header and whole-request
// timeout. It is a backstop rather than the owner of operation budgets, so it
// must not expire before either per-operation context can. A real recognized
// Claude launch exposed the old hard-coded 10s cap: the create context still
// had eighty seconds left while http.Client abandoned the request.
func TestTheControlTransportCoversTheWidestOperation(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		config  Config
		expects time.Duration
	}{
		{
			name: "create is wider",
			config: Config{
				ControlTimeout: 5 * time.Second,
				CreateTimeout:  defaultCreateTimeout,
			},
			expects: defaultCreateTimeout,
		},
		{
			name: "ordinary control is wider",
			config: Config{
				ControlTimeout: 2 * time.Minute,
				CreateTimeout:  time.Minute,
			},
			expects: 2 * time.Minute,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if actual := controlTransportTimeout(test.config); actual != test.expects {
				t.Fatalf("control transport timeout = %v, want %v", actual, test.expects)
			}
		})
	}
}

// Create is the only control call that can contain a person. Every other one
// keeps the short budget, and must: giving them the create budget would let a
// hung runtime hold an attach or a heartbeat for a minute and a half.
func TestOnlyCreateCarriesTheWideBudget(t *testing.T) {
	t.Parallel()

	if defaultControlTimeout >= defaultCreateTimeout {
		t.Fatalf(
			"the ordinary control budget is %v and the create budget is %v; "+
				"they are no longer distinct, so one of them is wrong",
			defaultControlTimeout,
			defaultCreateTimeout,
		)
	}
	if defaultControlTimeout > 10*time.Second {
		t.Fatalf(
			"the ordinary control budget grew to %v; attach, heartbeat and "+
				"finish contain no human step and must not wait like one",
			defaultControlTimeout,
		)
	}
}

// A person waiting at a terminal for a program to start is not a connection
// that is already open, so the ask inside a launch may not borrow the
// installation's decision budget. Design 11 §7 names this shape directly: an
// ask may deny after a short grace.
func TestTheInLaunchAskIsShorterThanAHeldConnectionsWait(t *testing.T) {
	t.Parallel()

	full := toolapproval.DefaultConfig().DecisionTimeout
	if toolapproval.DefaultClientRootGrace >= full {
		t.Fatalf(
			"the in-launch ask waits %v, the same as a held connection's %v; "+
				"an unanswered question would hold a terminal that long "+
				"before starting a client that was always going to start",
			toolapproval.DefaultClientRootGrace,
			full,
		)
	}
}

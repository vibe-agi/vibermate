package toolapproval_test

import (
	"context"
	"crypto/rand"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

func newRootAskAuthority(
	t *testing.T,
	config toolapproval.Config,
) *toolapproval.Authority {
	t.Helper()

	store := openStore(t, filepath.Join(t.TempDir(), "data", "runtime.db"))
	t.Cleanup(func() { shutdownStore(t, store) })
	authority, err := toolapproval.New(
		context.Background(),
		toolapproval.Options{
			Repository: store.ToolApprovalRepository(),
			Clock:      toolapproval.SystemClock{},
			Random:     rand.Reader,
			Config:     config,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { shutdownAuthority(t, authority) })
	return authority
}

func rootAskRequest() toolapproval.ClientRootAskRequest {
	return toolapproval.ClientRootAskRequest{
		SignerID:       "claude-code",
		SignerRevision: 1,
		SignedPath:     "/Applications/Claude.app/Contents/MacOS/claude",
	}
}

// Every other ask interrupts something already open: a connection waits with
// its socket held, and the installation's decision timeout is what bounds it.
// This one sits between a person typing a command and that program starting,
// so borrowing that budget means an unanswered question holds the terminal for
// as long as the whole installation allows — before starting a client that was
// always going to start, just without a Root.
//
// Design 11 §7 names the shape this must have instead: deny after a short
// grace.
func TestAnInLaunchAskDeniesAfterItsGraceNotAfterTheDecisionTimeout(t *testing.T) {
	t.Parallel()

	authority := newRootAskAuthority(t, toolapproval.Config{
		DecisionTimeout: 30 * time.Second,
		ClientRootGrace: 150 * time.Millisecond,
	})

	started := time.Now()
	outcome, err := authority.AskClientRoot(
		context.Background(),
		rootAskRequest(),
	)
	waited := time.Since(started)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Allowed {
		t.Fatal("an unanswered in-launch ask handed out the Root")
	}
	if waited > 5*time.Second {
		t.Fatalf(
			"the ask waited %v; it took the installation's decision timeout "+
				"rather than its own grace, so a launch hangs for as long as "+
				"a held connection would",
			waited,
		)
	}
	// The reason has to survive too. Bounding this by cancelling the caller's
	// context would produce the same timing and say "the connection went
	// away", which is a different fact and the one design 11 §7 requires be
	// recorded distinctly.
	if outcome.ReasonCode != "approval_expired" {
		t.Fatalf(
			"reason is %q, want the deadline reason",
			outcome.ReasonCode,
		)
	}
}

// An installation that bounds decisions more tightly than the default grace
// means it: nobody may be kept waiting longer than it says, including a
// launch.
func TestTheGraceNeverExceedsTheInstallationsOwnBound(t *testing.T) {
	t.Parallel()

	authority := newRootAskAuthority(t, toolapproval.Config{
		DecisionTimeout: 150 * time.Millisecond,
	})

	started := time.Now()
	outcome, err := authority.AskClientRoot(
		context.Background(),
		rootAskRequest(),
	)
	waited := time.Since(started)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Allowed {
		t.Fatal("an unanswered in-launch ask handed out the Root")
	}
	if waited > 5*time.Second {
		t.Fatalf(
			"the ask waited %v despite a %v decision timeout; the default "+
				"grace overrode the installation instead of yielding to it",
			waited,
			150*time.Millisecond,
		)
	}
}

// The grace belongs to the in-launch ask alone.
//
// Nothing stopped the shorter budget from being applied to every kind while
// the two call sites sat four lines apart, and no existing test could tell:
// they configure a decision timeout below the grace, where the two are
// clamped to the same value and therefore indistinguishable. A held
// connection given the launch grace would be denied while the person was
// still reading the prompt.
func TestAHeldConnectionKeepsTheFullDecisionBudget(t *testing.T) {
	t.Parallel()

	authority := newRootAskAuthority(t, toolapproval.Config{
		DecisionTimeout: 45 * time.Second,
		ClientRootGrace: 100 * time.Millisecond,
	})

	answered := make(chan struct{})
	go func() {
		_, _ = authority.AskNetwork(context.Background(), askRequest())
		close(answered)
	}()

	select {
	case <-answered:
		t.Fatal(
			"a held connection was denied inside the in-launch grace; that " +
				"budget exists because a launch has nobody waiting behind it, " +
				"and a connection does",
		)
	case <-time.After(2 * time.Second):
	}
}

// The durable row must expire when the wait does.
//
// A question that outlived its caller would be shown as still live and could
// be answered by somebody minutes after the launch had already gone ahead
// without a Root — an allow arriving with nobody left to receive it. Design 11
// §7 forbids exactly that: an ask may not quietly allow after the caller's
// own timeout has passed.
func TestThePendingRowExpiresWhenTheLaunchStopsWaiting(t *testing.T) {
	t.Parallel()

	const decisionTimeout = 30 * time.Second
	const grace = 2 * time.Second
	authority := newRootAskAuthority(t, toolapproval.Config{
		DecisionTimeout: decisionTimeout,
		ClientRootGrace: grace,
	})

	go func() {
		_, _ = authority.AskClientRoot(
			context.Background(),
			rootAskRequest(),
		)
	}()

	pending := waitForPendingKind(t, authority, toolapproval.KindClientRootAsk)
	window := pending.ExpiresAt.Sub(pending.CreatedAt)
	if window > grace+time.Second {
		t.Fatalf(
			"the question claims a %v window while its caller waits %v; it "+
				"would be shown as live, and answerable, after the launch had "+
				"already started the client without a Root",
			window,
			grace,
		)
	}
}

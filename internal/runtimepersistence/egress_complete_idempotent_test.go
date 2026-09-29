package runtimepersistence

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressaudit"
)

// A terminal write can commit and still report failure (for example when its
// deadline expires at commit). Retrying the same terminal must then succeed,
// while a different terminal for the same attempt stays an integrity error.
func TestEgressAttemptCompleteIsIdempotentForTheSameTerminal(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, filepath.Join(t.TempDir(), "data", "runtime.db"))
	repository := store.EgressAttemptRepository()
	attempt := providerAttempt(t, "egress-retry")
	if _, err := repository.Append(context.Background(), attempt); err != nil {
		t.Fatal(err)
	}
	finish := func(bytesIn int64) egressaudit.Attempt {
		terminal, err := attempt.Finish(egressaudit.TerminalInput{
			Outcome: egressaudit.OutcomeCompleted, BytesOut: 128, BytesIn: bytesIn,
			CompletedAt: time.Date(2026, 8, 2, 1, 2, 5, 0, time.UTC),
		})
		if err != nil {
			t.Fatal(err)
		}
		return terminal
	}
	terminal := finish(4096)
	if _, err := repository.Complete(context.Background(), terminal); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Complete(context.Background(), terminal); err != nil {
		t.Fatalf("retrying the committed terminal: %v", err)
	}
	if _, err := repository.Complete(context.Background(), finish(1)); err == nil {
		t.Fatal("a different terminal overwrote or matched the committed one")
	}
	if _, err := repository.Complete(context.Background(), providerAttemptTerminal(t, "egress-absent")); err == nil {
		t.Fatal("completed an attempt that was never appended")
	}
}

func providerAttemptTerminal(t *testing.T, id string) egressaudit.Attempt {
	t.Helper()
	terminal, err := providerAttempt(t, id).Finish(egressaudit.TerminalInput{
		Outcome: egressaudit.OutcomeCompleted, BytesOut: 1, BytesIn: 1,
		CompletedAt: time.Date(2026, 8, 2, 1, 2, 5, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	return terminal
}

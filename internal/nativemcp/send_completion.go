package nativemcp

import (
	"context"
	"net/http"
	"time"

	"github.com/vibe-agi/vibermate/internal/nativetool"
)

// Only Serve's own successful Start plus first exclusive unsent claim selects
// this closure. It records failure, never new send or retry permission.
func (e *executor) finishKnownUnsent(id nativetool.ID, s *session, w http.ResponseWriter, cause error) Terminal {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	err := e.dependencies.Store.Finish(ctx, id, nativetool.TerminalInput{Outcome: "failed", Reason: "transport_failed", BytesIn: 0, BytesOut: 0, ObservedAt: e.dependencies.Clock().UTC()})
	cancel()
	if err != nil {
		// Finish can have committed despite its error. Revoke preserves known
		// terminal rows and reconciles still-open work; it fences memory before
		// touching SQL and retains the owner for Close/Recover if SQL is down.
		// OutcomeUncertain does not execute Serve's rejected/canceled defer.
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		_ = e.Revoke(cleanup, s.owner.capture, RevocationOwnerChanged)
		stop()
		if w != nil {
			reject(w, id, ReasonStorageUnavailable)
		}
		return Terminal{OperationID: id, Outcome: OutcomeUncertain, Reason: ReasonStorageUnavailable}
	}
	reason := reasonFor(cause)
	if w != nil {
		reject(w, id, reason)
	}
	return Terminal{OperationID: id, Outcome: OutcomeFailed, Reason: reason}
}

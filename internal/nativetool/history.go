package nativetool

import (
	"context"
	"github.com/vibe-agi/vibermate/internal/recording"
	"time"
)

// HistoryObservation carries optional inspection facts, never executable proof.
type HistoryObservation struct {
	Operation                                  Operation
	EventKind                                  string
	EventAt, MaximumEventAt, MetadataExpiresAt time.Time
}
type RecordedHistory struct {
	Scope       recording.Scope
	Observation HistoryObservation
}
type HistoryDiscardReason uint8

const (
	HistoryRolledBack HistoryDiscardReason = iota
	HistoryCommitUnknown
	HistoryOwnerCanceled
)

type PreparedHistory interface {
	Publish() recording.OfferResult
	Discard(HistoryDiscardReason)
}
type HistoryRecorder interface {
	PrepareNativeHistory(recording.Stamp, HistoryObservation) (PreparedHistory, recording.OfferResult)
}
type HistoryWriter interface {
	PutNativeHistory(context.Context, RecordedHistory) error
}

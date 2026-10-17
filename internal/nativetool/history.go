package nativetool

import (
	"context"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/environment"
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

// InspectionIdentity is immutable at Receive, before a binding/tool name is
// known. Content publication derives these same fields from ContentIdentity;
// it never infers a logical state or operation revision.
type InspectionIdentity struct {
	OperationID         ID
	Capture             captureidentity.Reference
	EnvironmentID       environment.EnvironmentID
	EnvironmentRevision environment.Revision
	EnvironmentDigest   environment.CandidateDigest
	Kind                RequestKind
	TargetOrigin        string
	Recording           environment.ContentRecordingPolicy
	CreatedAt           time.Time
}
type InspectionIndexRequest struct {
	Capture       captureidentity.Reference
	AfterPosition int64
	Limit         int
}
type InspectionIndexEntry struct {
	Scope           recording.Scope
	Position        int64
	Identity        InspectionIdentity
	HistoryRevision uint64
}
type InspectionIndexPage struct {
	Items        []InspectionIndexEntry
	NextPosition int64
}

// ContentIdentity freezes display and expiry coordinates before asynchronous
// body publication. Task2 supplies sanitized owned content; no writer reloads
// these values from live control or an earlier lifecycle observation.
type ContentIdentity struct {
	OperationID                             ID
	Scope                                   recording.Scope
	EnvironmentID                           environment.EnvironmentID
	EnvironmentRevision                     environment.Revision
	EnvironmentDigest                       environment.CandidateDigest
	Kind                                    RequestKind
	ToolName, TargetOrigin, CredentialOwner string
	Recording                               environment.ContentRecordingPolicy
	CreatedAt, ExpiresAt                    time.Time
}
type ContentPart string

const (
	ContentToolArguments ContentPart = "tool_arguments"
	ContentToolResult    ContentPart = "tool_result"
)

type ContentObservation struct {
	Identity   ContentIdentity
	ObservedAt time.Time
	Part       ContentPart
	Body       []byte
}

// HistoryDropReporter reports a known loss before preparation without borrowing
// or materializing a snapshot. It is optional for external recorder adapters.
type HistoryDropReason uint8

const (
	HistoryTransactionFull HistoryDropReason = iota
	HistoryScopeUnavailable
	HistoryScopeStale
)

type HistoryDropReporter interface{ NativeHistoryDropped(HistoryDropReason) }

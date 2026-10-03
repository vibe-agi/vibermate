package nativetool

import (
	"context"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/environment"
)

// Repository is the single durable authority contract. Its SQLite adapter and
// sealed start receipts are implemented only after the reviewed schema switch.
type ScopeInput struct {
	Capture                      captureidentity.Reference
	ExpectedRevision, OwnerEpoch uint64
	ExpiresAt                    time.Time
}
type SessionInput struct {
	ID                                                        ID
	Capture                                                   captureidentity.Reference
	Plan                                                      environment.ClientToolPlan
	AssignmentRevision, ExpectedAuthorityRevision, OwnerEpoch uint64
	ExpiresAt                                                 time.Time
}
type ReceiveInput struct {
	ID                                 ID
	Capture                            captureidentity.Reference
	EnvironmentID                      environment.EnvironmentID
	EnvironmentRevision                environment.Revision
	EnvironmentDigest                  environment.CandidateDigest
	ConnectionID, TargetOrigin, Method string
	Kind                               RequestKind
	SessionID                          *ID
	RPCIDDigest, RequestDigest         *Digest
	Recording                          environment.ContentRecordingPolicy
	ObservedAt                         time.Time
}
type ReceiveResult struct {
	OperationID ID
	Duplicate   bool
}
type DecisionInput struct {
	ApprovalID       ID
	ExpectedRevision uint64
	Decision         Decision
	IdempotencyKey   string
	ObservedAt       time.Time
}
type Approval struct {
	ID, OperationID      ID
	BindingDigest        Digest
	Revision             uint64
	State                string
	CreatedAt, ExpiresAt time.Time
	ConsumedAt           *time.Time
}
type ContentDescriptor struct {
	Digest    Digest
	ExpiresAt time.Time
}
type Operation struct {
	ID                                      ID
	Capture                                 captureidentity.Reference
	EnvironmentID                           environment.EnvironmentID
	EnvironmentRevision                     environment.Revision
	EnvironmentDigest                       environment.CandidateDigest
	BindingDigest                           *Digest
	ApprovalID                              *ID
	Recording                               environment.ContentRecordingPolicy
	Kind                                    RequestKind
	ToolName, TargetOrigin, CredentialOwner string
	Arguments, Result                       *ContentDescriptor
	Revision                                uint64
	State, DecisionSource, Reason           string
	CreatedAt                               time.Time
	CompletedAt                             *time.Time
	BytesIn, BytesOut                       *int64
}
type TerminalInput struct {
	Outcome, Reason   string
	BytesIn, BytesOut int64
	ObservedAt        time.Time
}
type StartReceipt struct {
	operationID       ID
	attemptID         string
	authorityRevision uint64
}

func (receipt StartReceipt) OperationID() ID   { return receipt.operationID }
func (receipt StartReceipt) AttemptID() string { return receipt.attemptID }

type PageRequest struct {
	Capture       captureidentity.Reference
	AfterSequence int64
	Limit         int
}
type Page struct {
	Items        []Operation
	NextSequence int64
}
type Repository interface {
	EnsureScope(context.Context, ScopeInput) (uint64, error)
	RegisterSession(context.Context, SessionInput) error
	ReadySession(context.Context, ID, uint64, time.Time) (uint64, error)
	SetCatalog(context.Context, ID, uint64, Digest, time.Time) (uint64, error)
	Receive(context.Context, ReceiveInput) (ReceiveResult, error)
	Bind(context.Context, ID, Binding, time.Time) error
	RequestApproval(context.Context, ID, ID, time.Time, time.Time) (Approval, error)
	DecideApproval(context.Context, DecisionInput) (Approval, error)
	Get(context.Context, ID) (Operation, error)
	GetApproval(context.Context, ID) (Approval, error)
	Start(context.Context, ID, string, uint64, time.Time) (StartReceipt, error)
	Finish(context.Context, ID, TerminalInput) error
	Revoke(context.Context, captureidentity.Reference, uint64, string, time.Time) error
	MarkReplayBarrier(context.Context, captureidentity.Reference, uint64, time.Time) error
	Recover(context.Context, time.Time) error
	Page(context.Context, PageRequest) (Page, error)
	PutContent(context.Context, ID, string, []byte, time.Time) error
	ReadContent(context.Context, captureidentity.Reference, ID, string, int, int) ([]byte, error)
}

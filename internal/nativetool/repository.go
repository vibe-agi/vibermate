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

// Scope is body-free CAS evidence, never live permission or send authority.
type Scope struct {
	Capture              captureidentity.Reference
	Revision, OwnerEpoch uint64
	State                string
	ReplayBarrier        bool
	ExpiresAt            time.Time
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

// ApprovalClosure is lifecycle termination, never a user allow/deny answer.
type ApprovalClosure string

const (
	ApprovalClosureCancelled ApprovalClosure = "canceled"
	ApprovalClosureExpired   ApprovalClosure = "expired"
)

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
	ownerEpoch        uint64
	bindingDigest     Digest
	issuer            [32]byte
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
type LifecycleAuthority interface {
	GetScope(context.Context, captureidentity.Reference) (Scope, error)
	EnsureScope(context.Context, ScopeInput) (uint64, error)
	RegisterSession(context.Context, SessionInput) error
	ReadySession(context.Context, ID, uint64, time.Time) (uint64, error)
	SetCatalog(context.Context, ID, uint64, CatalogEvidence, time.Time) (uint64, error)
	InvalidateCatalog(context.Context, ID, uint64, time.Time) (uint64, error)
	CloseSession(context.Context, ID, uint64, time.Time) (uint64, error)
	TouchSession(context.Context, ID, uint64, time.Time, time.Time) error
	Receive(context.Context, ReceiveInput) (ReceiveResult, error)
	Bind(context.Context, ID, Binding, time.Time) error
	RequestApproval(context.Context, ID, ID, time.Time, time.Time) (Approval, error)
	DecideApproval(context.Context, DecisionInput) (Approval, error)
	CloseApproval(context.Context, ID, uint64, ApprovalClosure, time.Time) (Approval, error)
	Get(context.Context, ID) (Operation, error)
	GetApproval(context.Context, ID) (Approval, error)
}

type LifecycleRepository interface {
	LifecycleAuthority
	Page(context.Context, PageRequest) (Page, error)
}
type ExecutionAuthority interface {
	StreamRepository
	Start(context.Context, ID, string, uint64, time.Time) (StartReceipt, error)
	VerifyStartReceipt(context.Context, StartReceipt, time.Time) error
	Finish(context.Context, ID, TerminalInput) error
	Revoke(context.Context, captureidentity.Reference, uint64, string, time.Time) error
	MarkReplayBarrier(context.Context, captureidentity.Reference, uint64, time.Time) error
	Recover(context.Context, time.Time) error
}

type ExecutionRepository interface {
	ExecutionAuthority
	PutContent(context.Context, ID, string, []byte, time.Time) error
	ReadContent(context.Context, captureidentity.Reference, ID, string, int, int) ([]byte, error)
}
type Repository interface {
	LifecycleRepository
	ExecutionRepository
}

// AuthorityRepository reads only current control facts. Optional inspection is separate.
type AuthorityRepository interface {
	LifecycleAuthority
	ExecutionAuthority
	ListPendingApprovals(context.Context, PendingApprovalRequest) (PendingApprovalPage, error)
}
type PendingApprovalRequest struct {
	Capture        captureidentity.Reference
	AfterCreatedAt time.Time
	AfterID        ID
	Limit          int
}
type PendingApprovalItem struct {
	Approval  Approval
	Operation Operation
}
type PendingApprovalPage struct {
	Items         []PendingApprovalItem
	NextCreatedAt time.Time
	NextID        ID
}

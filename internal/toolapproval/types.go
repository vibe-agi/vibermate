// Package toolapproval owns durable, fail-closed decisions for complete tool
// intent groups. Safe projections never include raw tool arguments.
package toolapproval

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/access"
)

const (
	MaxIdentityBytes = 512
	MaxToolIntents   = 128
	MaxPageSize      = 200
)

var (
	ErrInvalidApproval  = errors.New("invalid tool approval")
	ErrNotFound         = errors.New("tool approval not found")
	ErrRevisionConflict = errors.New("tool approval revision conflict")
	ErrRuntimeStopping  = errors.New("tool approval runtime is stopping")
)

type State string

const (
	StatePending  State = "pending"
	StateAllowed  State = "allowed"
	StateDenied   State = "denied"
	StateCanceled State = "canceled"
	StateExpired  State = "expired"
)

type Decision string

const (
	DecisionAllowOnce Decision = "allow-once"
	DecisionDeny      Decision = "deny"
)

const ScopeRequest = "request"

type Record struct {
	ID             string
	Revision       uint64
	ExchangeID     string
	AccessID       access.AccessID
	PlanRevision   access.Revision
	PlanHash       access.PlanHash
	ToolCallIDs    []string
	ToolNames      []string
	State          State
	Decision       Decision
	DecisionScope  string
	DecisionReason string
	IdempotencyKey string
	CreatedAt      time.Time
	ExpiresAt      time.Time
	ResolvedAt     time.Time
}

func (record Record) Validate() error {
	if err := validateIdentity("approval ID", record.ID, false); err != nil {
		return err
	}
	if record.Revision == 0 ||
		record.AccessID.String() == "" ||
		record.PlanRevision == 0 ||
		record.PlanHash.IsZero() ||
		record.CreatedAt.IsZero() ||
		record.ExpiresAt.IsZero() ||
		!record.CreatedAt.Before(record.ExpiresAt) {
		return ErrInvalidApproval
	}
	if err := validateIdentity("Exchange ID", record.ExchangeID, false); err != nil {
		return err
	}
	if len(record.ToolCallIDs) == 0 ||
		len(record.ToolCallIDs) > MaxToolIntents ||
		len(record.ToolCallIDs) != len(record.ToolNames) {
		return ErrInvalidApproval
	}
	for index := range record.ToolCallIDs {
		if err := validateIdentity("tool call ID", record.ToolCallIDs[index], false); err != nil {
			return err
		}
		if err := validateIdentity("tool name", record.ToolNames[index], false); err != nil {
			return err
		}
	}
	switch record.State {
	case StatePending:
		if record.Decision != "" ||
			record.DecisionScope != "" ||
			record.DecisionReason != "" ||
			record.IdempotencyKey != "" ||
			!record.ResolvedAt.IsZero() {
			return ErrInvalidApproval
		}
	case StateAllowed:
		if record.Decision != DecisionAllowOnce ||
			record.DecisionScope != ScopeRequest ||
			record.DecisionReason != "" ||
			record.IdempotencyKey == "" ||
			record.ResolvedAt.IsZero() {
			return ErrInvalidApproval
		}
	case StateDenied:
		if record.Decision != DecisionDeny ||
			record.DecisionScope != ScopeRequest ||
			record.DecisionReason == "" ||
			record.IdempotencyKey == "" ||
			record.ResolvedAt.IsZero() {
			return ErrInvalidApproval
		}
	case StateCanceled, StateExpired:
		if record.Decision != "" ||
			record.DecisionScope != "" ||
			record.DecisionReason == "" ||
			record.IdempotencyKey != "" ||
			record.ResolvedAt.IsZero() {
			return ErrInvalidApproval
		}
	default:
		return ErrInvalidApproval
	}
	if !record.ResolvedAt.IsZero() && record.ResolvedAt.Before(record.CreatedAt) {
		return ErrInvalidApproval
	}
	return nil
}

func (record Record) Clone() Record {
	cloned := record
	cloned.ToolCallIDs = slices.Clone(record.ToolCallIDs)
	cloned.ToolNames = slices.Clone(record.ToolNames)
	return cloned
}

type Choice struct {
	Decision Decision `json:"decision"`
	Scope    string   `json:"scope"`
}

type View struct {
	ID             string          `json:"id"`
	Revision       uint64          `json:"revision"`
	Kind           string          `json:"kind"`
	State          State           `json:"state"`
	Risk           string          `json:"risk"`
	TitleKey       string          `json:"titleKey"`
	SummaryKey     string          `json:"summaryKey"`
	ExchangeID     string          `json:"exchangeId"`
	AccessID       string          `json:"accessId"`
	PlanRevision   access.Revision `json:"planRevision"`
	PlanHash       string          `json:"planHash"`
	ToolCallIDs    []string        `json:"toolCallIds"`
	ToolNames      []string        `json:"toolNames"`
	Choices        []Choice        `json:"choices"`
	CreatedAt      time.Time       `json:"createdAt"`
	ExpiresAt      time.Time       `json:"expiresAt"`
	ResolvedAt     *time.Time      `json:"resolvedAt,omitempty"`
	Decision       Decision        `json:"decision,omitempty"`
	DecisionScope  string          `json:"decisionScope,omitempty"`
	TerminalReason string          `json:"terminalReason,omitempty"`
}

func ViewOf(record Record) View {
	view := View{
		ID:           record.ID,
		Revision:     record.Revision,
		Kind:         "tool-intent",
		State:        record.State,
		Risk:         "high",
		TitleKey:     "approval.toolIntent.title",
		SummaryKey:   "approval.toolIntent.summary",
		ExchangeID:   record.ExchangeID,
		AccessID:     record.AccessID.String(),
		PlanRevision: record.PlanRevision,
		PlanHash:     record.PlanHash.String(),
		ToolCallIDs:  slices.Clone(record.ToolCallIDs),
		ToolNames:    slices.Clone(record.ToolNames),
		Choices: []Choice{
			{Decision: DecisionAllowOnce, Scope: ScopeRequest},
			{Decision: DecisionDeny, Scope: ScopeRequest},
		},
		CreatedAt:      record.CreatedAt,
		ExpiresAt:      record.ExpiresAt,
		Decision:       record.Decision,
		DecisionScope:  record.DecisionScope,
		TerminalReason: record.DecisionReason,
	}
	if !record.ResolvedAt.IsZero() {
		resolved := record.ResolvedAt
		view.ResolvedAt = &resolved
	}
	return view
}

type PageRequest struct {
	State State
	Limit int
}

func (request PageRequest) Validate() error {
	if request.Limit <= 0 || request.Limit > MaxPageSize {
		return ErrInvalidApproval
	}
	switch request.State {
	case "", StatePending, StateAllowed, StateDenied, StateCanceled, StateExpired:
	default:
		return ErrInvalidApproval
	}
	return nil
}

type Page struct {
	Items []View `json:"items"`
}

type DecisionCommand struct {
	ApprovalID       string
	ExpectedRevision uint64
	IdempotencyKey   string
	Decision         Decision
	Scope            string
	ReasonCode       string
}

func (command DecisionCommand) Validate() error {
	if validateIdentity("approval ID", command.ApprovalID, false) != nil ||
		command.ExpectedRevision == 0 ||
		validateIdentity("idempotency key", command.IdempotencyKey, false) != nil ||
		len(command.IdempotencyKey) < 16 ||
		command.Scope != ScopeRequest {
		return ErrInvalidApproval
	}
	switch command.Decision {
	case DecisionAllowOnce:
		if command.ReasonCode != "" {
			return ErrInvalidApproval
		}
	case DecisionDeny:
		if validateIdentity("decision reason code", command.ReasonCode, false) != nil {
			return ErrInvalidApproval
		}
	default:
		return ErrInvalidApproval
	}
	return nil
}

type Recovery struct {
	CanceledPending int
}

type Repository interface {
	Recover(context.Context, time.Time) (Recovery, error)
	Create(context.Context, Record) error
	Get(context.Context, string) (Record, error)
	List(context.Context, PageRequest) ([]Record, error)
	Decide(context.Context, DecisionCommand, time.Time) (Record, error)
	Cancel(context.Context, string, string, time.Time) (Record, error)
	CancelPending(context.Context, string, time.Time) ([]Record, error)
}

type Controller interface {
	GetApproval(context.Context, string) (View, error)
	ListApprovals(context.Context, PageRequest) (Page, error)
	DecideApproval(context.Context, DecisionCommand) (View, error)
}

func validateIdentity(label string, value string, allowEmpty bool) error {
	if value == "" && allowEmpty {
		return nil
	}
	if value == "" ||
		len(value) > MaxIdentityBytes ||
		!utf8.ValidString(value) ||
		strings.TrimSpace(value) != value {
		return fmt.Errorf("%w: %s is invalid", ErrInvalidApproval, label)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("%w: %s contains a control character", ErrInvalidApproval, label)
		}
	}
	return nil
}

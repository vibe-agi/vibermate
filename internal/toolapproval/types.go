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

const MaxSubjectLabelBytes = 512

// Kind names what is being approved. Risk, copy keys, and available choices
// are derived from it, so adding a kind does not mean forking the record.
type Kind string

const (
	KindToolIntent Kind = "tool_intent"
	KindNetworkAsk Kind = "network_ask"
)

type presentation struct {
	risk       string
	titleKey   string
	summaryKey string
	choices    []Choice
}

// requestChoices is the decision set for a question answered for this request
// only. Remembered scopes arrive with the rules that would store them.
func requestChoices() []Choice {
	return []Choice{
		{Decision: DecisionAllowOnce, Scope: ScopeRequest},
		{Decision: DecisionDeny, Scope: ScopeRequest},
	}
}

var presentations = map[Kind]presentation{
	KindToolIntent: {
		risk:       "high",
		titleKey:   "approval.toolIntent.title",
		summaryKey: "approval.toolIntent.summary",
		choices:    requestChoices(),
	},
	KindNetworkAsk: {
		risk:       "medium",
		titleKey:   "approval.networkAsk.title",
		summaryKey: "approval.networkAsk.summary",
		choices:    requestChoices(),
	},
}

func (kind Kind) valid() bool {
	_, known := presentations[kind]
	return known
}

// requiresAccessPlan reports whether this kind is decided after an Access plan
// exists. A network ask is decided before any Access is resolved, so it has no
// binding to supply.
func (kind Kind) requiresAccessPlan() bool {
	return kind == KindToolIntent
}

type Record struct {
	ID       string
	Revision uint64
	Kind     Kind
	// AggregateKey merges identical pending questions into one entry, so a
	// burst is one prompt rather than one per event.
	AggregateKey string
	// SubjectRefs and SubjectLabels carry redacted identifiers and safe
	// display labels only: never a path, header, body, argument, or
	// credential.
	SubjectRefs   []string
	SubjectLabels []string
	// RequestCount and WaiterCount describe how much this one entry stands
	// for.
	RequestCount   uint32
	WaiterCount    uint32
	ExchangeID     string
	AccessID       access.AccessID
	PlanRevision   access.Revision
	PlanHash       access.PlanHash
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
	if !record.Kind.valid() {
		return ErrInvalidApproval
	}
	if record.Revision == 0 ||
		record.CreatedAt.IsZero() ||
		record.ExpiresAt.IsZero() ||
		!record.CreatedAt.Before(record.ExpiresAt) {
		return ErrInvalidApproval
	}
	if err := validateIdentity(
		"approval aggregate key",
		record.AggregateKey,
		false,
	); err != nil {
		return err
	}
	if record.RequestCount == 0 ||
		record.WaiterCount == 0 ||
		record.WaiterCount > record.RequestCount {
		return ErrInvalidApproval
	}
	// A binding is mandatory only for a kind decided after Access resolution.
	// When present it is complete, so a partial binding cannot pass.
	hasBinding := record.AccessID.String() != "" ||
		record.PlanRevision != 0 ||
		!record.PlanHash.IsZero() ||
		record.ExchangeID != ""
	if record.Kind.requiresAccessPlan() || hasBinding {
		if record.AccessID.String() == "" ||
			record.PlanRevision == 0 ||
			record.PlanHash.IsZero() {
			return ErrInvalidApproval
		}
		if err := validateIdentity(
			"Exchange ID",
			record.ExchangeID,
			false,
		); err != nil {
			return err
		}
	}
	if len(record.SubjectRefs) == 0 ||
		len(record.SubjectRefs) > MaxToolIntents ||
		len(record.SubjectRefs) != len(record.SubjectLabels) {
		return ErrInvalidApproval
	}
	for index := range record.SubjectRefs {
		if err := validateIdentity(
			"approval subject reference",
			record.SubjectRefs[index],
			false,
		); err != nil {
			return err
		}
		if err := validateIdentity(
			"approval subject label",
			record.SubjectLabels[index],
			false,
		); err != nil {
			return err
		}
		if len(record.SubjectLabels[index]) > MaxSubjectLabelBytes {
			return ErrInvalidApproval
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
	cloned.SubjectRefs = slices.Clone(record.SubjectRefs)
	cloned.SubjectLabels = slices.Clone(record.SubjectLabels)
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
	AggregateKey   string          `json:"aggregateKey"`
	SubjectRefs    []string        `json:"subjectRefs"`
	SubjectLabels  []string        `json:"subjectLabels"`
	RequestCount   uint32          `json:"requestCount"`
	WaiterCount    uint32          `json:"waiterCount"`
	Choices        []Choice        `json:"choices"`
	CreatedAt      time.Time       `json:"createdAt"`
	ExpiresAt      time.Time       `json:"expiresAt"`
	ResolvedAt     *time.Time      `json:"resolvedAt,omitempty"`
	Decision       Decision        `json:"decision,omitempty"`
	DecisionScope  string          `json:"decisionScope,omitempty"`
	TerminalReason string          `json:"terminalReason,omitempty"`
}

func ViewOf(record Record) View {
	look := presentations[record.Kind]
	view := View{
		ID:             record.ID,
		Revision:       record.Revision,
		Kind:           string(record.Kind),
		State:          record.State,
		Risk:           look.risk,
		TitleKey:       look.titleKey,
		SummaryKey:     look.summaryKey,
		AggregateKey:   record.AggregateKey,
		ExchangeID:     record.ExchangeID,
		AccessID:       record.AccessID.String(),
		PlanRevision:   record.PlanRevision,
		PlanHash:       record.PlanHash.String(),
		SubjectRefs:    slices.Clone(record.SubjectRefs),
		SubjectLabels:  slices.Clone(record.SubjectLabels),
		RequestCount:   record.RequestCount,
		WaiterCount:    record.WaiterCount,
		Choices:        slices.Clone(look.choices),
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
	// Join and Leave keep the counts on a pending question true while callers
	// arrive and go. They do not move the revision: the revision guards the
	// decision, and who is currently waiting is not a decision.
	Join(context.Context, string) (Record, error)
	Leave(context.Context, string) (Record, error)
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

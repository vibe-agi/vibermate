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

// A scope says how far an answer reaches. `request` answers only the question
// in front of the person; a remembered scope also writes a rule, so the same
// question is not asked again.
const (
	ScopeRequest = "request"
	// ScopeHostPort remembers an answer for exactly the host and port that
	// were asked about. It is deliberately no wider: allowing a host on one
	// port is not a statement about any other port, and a broader rule stays
	// something a person writes on purpose.
	ScopeHostPort = "host_port"
)

// validScope reports whether this kind may be answered at this scope.
func validScope(kind Kind, scope string) bool {
	if scope != ScopeRequest && scope != ScopeHostPort {
		return false
	}
	return kind.canRemember(scope)
}

// remembers reports whether a scope writes a rule.
func remembers(scope string) bool {
	return scope == ScopeHostPort
}

// canRemember reports whether a kind has anything to remember. A tool intent
// is bound to one Exchange and one plan, so there is no later connection for a
// remembered answer to decide.
func (kind Kind) canRemember(scope string) bool {
	if !remembers(scope) {
		return true
	}
	return kind == KindNetworkAsk
}

const MaxSubjectLabelBytes = 512

// Kind names what is being approved. Risk, copy keys, and available choices
// are derived from it, so adding a kind does not mean forking the record.
type Kind string

const (
	KindToolIntent Kind = "tool_intent"
	KindNetworkAsk Kind = "network_ask"
	// KindClientRootAsk: may a client recognized by its publisher, rather than
	// by a catalogued build, be given the local Root? Asked once per publisher
	// entry, before the client launches.
	KindClientRootAsk Kind = "client_root_ask"
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
		{
			Decision: DecisionAllowOnce,
			Scope:    ScopeRequest,
			LabelKey: "approval.toolIntent.choice.allowOnce",
		},
		{
			Decision: DecisionDeny,
			Scope:    ScopeRequest,
			LabelKey: "approval.toolIntent.choice.deny",
		},
	}
}

// networkAskChoices adds remembering. The remembered choices carry the same
// subject as the question, so what the person is agreeing to is the host and
// port in front of them and nothing wider.
func networkAskChoices() []Choice {
	return []Choice{
		{
			Decision: DecisionAllowOnce,
			Scope:    ScopeRequest,
			LabelKey: "approval.networkAsk.choice.allowOnce",
		},
		{
			Decision: DecisionAllowOnce,
			Scope:    ScopeHostPort,
			LabelKey: "approval.networkAsk.choice.allowHostPort",
		},
		{
			Decision: DecisionDeny,
			Scope:    ScopeRequest,
			LabelKey: "approval.networkAsk.choice.denyOnce",
		},
		{
			Decision: DecisionDeny,
			Scope:    ScopeHostPort,
			LabelKey: "approval.networkAsk.choice.denyHostPort",
		},
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
		choices:    networkAskChoices(),
	},
	KindClientRootAsk: {
		// Handing out the Root is what makes a client's traffic readable, so
		// this is not a medium-risk convenience prompt.
		risk:       "high",
		titleKey:   "approval.clientRootAsk.title",
		summaryKey: "approval.clientRootAsk.summary",
		choices:    clientRootAskChoices(),
	},
}

// clientRootAskChoices offers this launch and nothing wider.
//
// A remembered answer would need a rule keyed on a publisher, and the only
// remembered scope that exists writes a host and port rule, which is about a
// connection rather than about who signed a program. Rather than reuse it for
// something it does not mean, this kind answers one launch at a time until
// that store exists. The cost is a prompt per launch and it is visible; the
// alternative would have been a remembered answer that quietly decided more
// than the person was asked.
func clientRootAskChoices() []Choice {
	return []Choice{
		{
			Decision: DecisionAllowOnce,
			Scope:    ScopeRequest,
			LabelKey: "approval.clientRootAsk.choice.allowOnce",
		},
		{
			Decision: DecisionDeny,
			Scope:    ScopeRequest,
			LabelKey: "approval.clientRootAsk.choice.denyOnce",
		},
	}
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

// Target is the connection a network ask is about, in typed fields. A
// remembered answer builds its rule from these rather than from taking a
// subject string apart, so nothing has to encode structure into an identifier
// and then recover it.
type Target struct {
	Host string `json:"host"`
	Port uint16 `json:"port"`
}

func (target Target) present() bool {
	return target.Host != "" || target.Port != 0
}

func (target Target) validate() error {
	if target.Host == "" ||
		len(target.Host) > 253 ||
		strings.ToLower(target.Host) != target.Host ||
		strings.ContainsAny(target.Host, " \t\r\n") ||
		target.Port == 0 {
		return fmt.Errorf("%w: approval target is invalid", ErrInvalidApproval)
	}
	return nil
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
	// Target is the connection this record is about, when it is about one. A
	// remembered answer builds its rule from these typed fields.
	Target Target
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
	// A kind that can be remembered must say what it is about, and a kind that
	// cannot must not carry a connection it would never decide.
	switch record.Kind {
	case KindNetworkAsk:
		if err := record.Target.validate(); err != nil {
			return err
		}
	default:
		if record.Target.present() {
			return ErrInvalidApproval
		}
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
			!validScope(record.Kind, record.DecisionScope) ||
			record.DecisionReason != "" ||
			record.IdempotencyKey == "" ||
			record.ResolvedAt.IsZero() {
			return ErrInvalidApproval
		}
	case StateDenied:
		if record.Decision != DecisionDeny ||
			!validScope(record.Kind, record.DecisionScope) ||
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
	// LabelKey names the sentence a person reads before choosing. A
	// remembered choice has to say that it is remembered, because the
	// difference between answering once and writing a rule is the whole
	// decision.
	LabelKey string `json:"labelKey"`
}

type View struct {
	ID           string          `json:"id"`
	Revision     uint64          `json:"revision"`
	Kind         string          `json:"kind"`
	State        State           `json:"state"`
	Risk         string          `json:"risk"`
	TitleKey     string          `json:"titleKey"`
	SummaryKey   string          `json:"summaryKey"`
	ExchangeID   string          `json:"exchangeId,omitempty"`
	AccessID     string          `json:"accessId,omitempty"`
	PlanRevision access.Revision `json:"planRevision,omitempty"`
	PlanHash     string          `json:"planHash,omitempty"`
	AggregateKey string          `json:"aggregateKey"`
	// Target is what a connection question is about, so a window can name the
	// host and port instead of taking a subject string apart.
	Target         *Target    `json:"target,omitempty"`
	SubjectRefs    []string   `json:"subjectRefs"`
	SubjectLabels  []string   `json:"subjectLabels"`
	RequestCount   uint32     `json:"requestCount"`
	WaiterCount    uint32     `json:"waiterCount"`
	Choices        []Choice   `json:"choices"`
	CreatedAt      time.Time  `json:"createdAt"`
	ExpiresAt      time.Time  `json:"expiresAt"`
	ResolvedAt     *time.Time `json:"resolvedAt,omitempty"`
	Decision       Decision   `json:"decision,omitempty"`
	DecisionScope  string     `json:"decisionScope,omitempty"`
	TerminalReason string     `json:"terminalReason,omitempty"`
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
	// A record with no plan binding has no plan hash. Presenting a zero hash
	// would show a person a fact that does not exist.
	if record.PlanRevision != 0 {
		view.PlanHash = record.PlanHash.String()
	}
	if record.Target.present() {
		target := record.Target
		view.Target = &target
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
		(command.Scope != ScopeRequest && command.Scope != ScopeHostPort) {
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

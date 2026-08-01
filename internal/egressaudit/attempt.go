// Package egressaudit models immutable evidence for one logical outbound
// transport attempt. It deliberately does not reuse connection-level fields:
// a connection answers "who connected where from here", an egress attempt
// answers "where did this one request actually go". A persistent connection
// carrying several requests must not overwrite the first answer with the last.
package egressaudit

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// EgressPurpose answers what an outbound attempt was for. It is orthogonal to
// both the policy authority that selected the route and the Offline Hold queue
// class that admitted it.
type EgressPurpose string

const (
	PurposeProviderAttempt   EgressPurpose = "provider_attempt"
	PurposeProfileOperation  EgressPurpose = "profile_operation"
	PurposeOriginalOrigin    EgressPurpose = "original_origin"
	PurposeAgentProbe        EgressPurpose = "agent_probe"
	PurposeBlindTunnel       EgressPurpose = "blind_tunnel"
	PurposeAuxiliaryLLM      EgressPurpose = "auxiliary_llm"
	PurposeLanguageTransform EgressPurpose = "language_transform"
	PurposeUpdate            EgressPurpose = "update"
)

// PolicyAuthorityKind names the configuration that owns the candidates and the
// default rule for a purpose.
type PolicyAuthorityKind string

const (
	AuthorityAccess  PolicyAuthorityKind = "access"
	AuthorityNetwork PolicyAuthorityKind = "network"
	AuthorityRuntime PolicyAuthorityKind = "runtime"
)

// AuthorityForPurpose is the exhaustive typed mapping. An unknown purpose has
// no authority and therefore no route, so it fails rather than defaulting.
func AuthorityForPurpose(
	purpose EgressPurpose,
) (PolicyAuthorityKind, error) {
	switch purpose {
	case PurposeProviderAttempt, PurposeProfileOperation:
		return AuthorityAccess, nil
	case PurposeOriginalOrigin, PurposeAgentProbe, PurposeBlindTunnel:
		return AuthorityNetwork, nil
	case PurposeAuxiliaryLLM, PurposeLanguageTransform, PurposeUpdate:
		return AuthorityRuntime, nil
	default:
		return "", fmt.Errorf("unknown egress purpose %q", purpose)
	}
}

// PayloadClass is the egress-side classification. It extends the catalog's
// operation classes with the two shapes that are not catalog operations at all,
// and it deliberately has no unknown member: an unclassified operation never
// produces an outbound attempt.
type PayloadClass string

const (
	PayloadNone           PayloadClass = "none"
	PayloadControl        PayloadClass = "control"
	PayloadClientData     PayloadClass = "client_data"
	PayloadClientSemantic PayloadClass = "client_semantic"
	PayloadOpaqueTunnel   PayloadClass = "opaque_tunnel"
	PayloadRuntime        PayloadClass = "runtime"
)

func (class PayloadClass) valid() bool {
	switch class {
	case PayloadNone,
		PayloadControl,
		PayloadClientData,
		PayloadClientSemantic,
		PayloadOpaqueTunnel,
		PayloadRuntime:
		return true
	default:
		return false
	}
}

func (class PayloadClass) carriesClientPayload() bool {
	return class == PayloadClientData || class == PayloadClientSemantic
}

type ParentKind string

const (
	ParentUpstreamAttempt ParentKind = "upstream_attempt"
	ParentClientOperation ParentKind = "client_operation"
	ParentOriginalRequest ParentKind = "original_request"
	ParentBlindConnection ParentKind = "blind_connection"
	ParentRuntimeAction   ParentKind = "runtime_action"
)

type CallerKind string

const (
	CallerCore   CallerKind = "core"
	CallerPlugin CallerKind = "plugin"
)

type Outcome string

const (
	OutcomeCompleted Outcome = "completed"
	OutcomeFailed    Outcome = "failed"
	OutcomeCanceled  Outcome = "canceled"
)

func (outcome Outcome) valid() bool {
	switch outcome {
	case OutcomeCompleted, OutcomeFailed, OutcomeCanceled:
		return true
	default:
		return false
	}
}

// ParentRef associates an attempt with what caused it. ExchangeID is a separate
// field rather than a prefix of ID, so no identity encodes containment of
// another.
type ParentRef struct {
	Kind       ParentKind
	ID         string
	ExchangeID string
}

// DecisionRef records which policy produced the route, without the candidate
// set or any credential.
type DecisionRef struct {
	PolicyID       string
	PolicyRevision uint64
	Authority      PolicyAuthorityKind
	RuleID         string
	ProxyID        string
}

type NewInput struct {
	ID              string
	ConnectionID    string
	Purpose         EgressPurpose
	PayloadClass    PayloadClass
	Parent          ParentRef
	Caller          CallerKind
	CallerID        string
	TargetOrigin    string
	Decision        DecisionRef
	ReusedTransport bool
	StartedAt       time.Time
}

type TerminalInput struct {
	Outcome     Outcome
	ErrorClass  string
	BytesOut    int64
	BytesIn     int64
	CompletedAt time.Time
}

// Attempt is immutable. Finish returns a new terminal value rather than
// mutating, so an earlier record can never be overwritten.
type Attempt struct {
	id              string
	connectionID    string
	purpose         EgressPurpose
	payloadClass    PayloadClass
	parent          ParentRef
	caller          CallerKind
	callerID        string
	targetOrigin    string
	decision        DecisionRef
	reusedTransport bool
	startedAt       time.Time

	terminal    bool
	outcome     Outcome
	errorClass  string
	bytesOut    int64
	bytesIn     int64
	completedAt time.Time
}

func New(input NewInput) (Attempt, error) {
	if err := validateIdentity("EgressAttempt ID", input.ID); err != nil {
		return Attempt{}, err
	}
	authority, err := AuthorityForPurpose(input.Purpose)
	if err != nil {
		return Attempt{}, err
	}
	if input.Decision.Authority != authority {
		return Attempt{}, fmt.Errorf(
			"purpose %q requires %s authority, got %s",
			input.Purpose,
			authority,
			input.Decision.Authority,
		)
	}
	if !input.PayloadClass.valid() {
		return Attempt{}, fmt.Errorf(
			"egress payload class %q is invalid",
			input.PayloadClass,
		)
	}
	if err := validatePayloadClass(
		input.Purpose,
		input.PayloadClass,
	); err != nil {
		return Attempt{}, err
	}
	if err := validateParent(
		input.Purpose,
		input.ConnectionID,
		input.Parent,
		input.ID,
	); err != nil {
		return Attempt{}, err
	}
	if err := validateCaller(input.Caller, input.CallerID); err != nil {
		return Attempt{}, err
	}
	if err := validateDecision(input.Decision); err != nil {
		return Attempt{}, err
	}
	if input.TargetOrigin == "" ||
		!strings.HasPrefix(input.TargetOrigin, "https://") &&
			!strings.HasPrefix(input.TargetOrigin, "http://") {
		return Attempt{}, errors.New("egress target origin is invalid")
	}
	if input.StartedAt.IsZero() {
		return Attempt{}, errors.New("egress start time is required")
	}
	return Attempt{
		id:              input.ID,
		connectionID:    input.ConnectionID,
		purpose:         input.Purpose,
		payloadClass:    input.PayloadClass,
		parent:          input.Parent,
		caller:          input.Caller,
		callerID:        input.CallerID,
		targetOrigin:    input.TargetOrigin,
		decision:        input.Decision,
		reusedTransport: input.ReusedTransport,
		startedAt:       input.StartedAt.UTC(),
	}, nil
}

// Finish returns the terminal value for this attempt. A terminal attempt
// cannot be finished again, so a late writer cannot rewrite an outcome.
func (attempt Attempt) Finish(input TerminalInput) (Attempt, error) {
	if attempt.terminal {
		return Attempt{}, errors.New("EgressAttempt already reached a terminal")
	}
	if !input.Outcome.valid() {
		return Attempt{}, fmt.Errorf("egress outcome %q is invalid", input.Outcome)
	}
	if input.BytesOut < 0 || input.BytesIn < 0 {
		return Attempt{}, errors.New("egress byte counts cannot be negative")
	}
	if input.CompletedAt.IsZero() ||
		input.CompletedAt.Before(attempt.startedAt) {
		return Attempt{}, errors.New("egress completion time is invalid")
	}
	if input.ErrorClass != "" {
		if err := validateIdentity(
			"egress error class",
			input.ErrorClass,
		); err != nil {
			return Attempt{}, err
		}
	}
	attempt.terminal = true
	attempt.outcome = input.Outcome
	attempt.errorClass = input.ErrorClass
	attempt.bytesOut = input.BytesOut
	attempt.bytesIn = input.BytesIn
	attempt.completedAt = input.CompletedAt.UTC()
	return attempt, nil
}

func (attempt Attempt) ID() string                 { return attempt.id }
func (attempt Attempt) ConnectionID() string       { return attempt.connectionID }
func (attempt Attempt) Purpose() EgressPurpose     { return attempt.purpose }
func (attempt Attempt) PayloadClass() PayloadClass { return attempt.payloadClass }
func (attempt Attempt) Parent() ParentRef          { return attempt.parent }
func (attempt Attempt) Caller() CallerKind         { return attempt.caller }
func (attempt Attempt) CallerID() string           { return attempt.callerID }
func (attempt Attempt) TargetOrigin() string       { return attempt.targetOrigin }
func (attempt Attempt) Decision() DecisionRef      { return attempt.decision }
func (attempt Attempt) ReusedTransport() bool      { return attempt.reusedTransport }
func (attempt Attempt) StartedAt() time.Time       { return attempt.startedAt }
func (attempt Attempt) Terminal() bool             { return attempt.terminal }
func (attempt Attempt) Outcome() Outcome           { return attempt.outcome }
func (attempt Attempt) ErrorClass() string         { return attempt.errorClass }
func (attempt Attempt) BytesOut() int64            { return attempt.bytesOut }
func (attempt Attempt) BytesIn() int64             { return attempt.bytesIn }
func (attempt Attempt) CompletedAt() time.Time     { return attempt.completedAt }

// validatePayloadClass keeps the fail-closed rule at the record boundary too:
// a record that claims client payload reached the inbound origin is a release
// blocker, so it cannot be constructed in the first place.
func validatePayloadClass(
	purpose EgressPurpose,
	class PayloadClass,
) error {
	switch purpose {
	case PurposeOriginalOrigin, PurposeAgentProbe:
		if class.carriesClientPayload() {
			return fmt.Errorf(
				"purpose %q cannot record payload class %q",
				purpose,
				class,
			)
		}
	case PurposeBlindTunnel:
		if class != PayloadOpaqueTunnel {
			return fmt.Errorf(
				"blind tunnel must record the opaque tunnel payload class, got %q",
				class,
			)
		}
	case PurposeAuxiliaryLLM, PurposeLanguageTransform, PurposeUpdate:
		if class != PayloadRuntime {
			return fmt.Errorf(
				"runtime purpose %q must record the runtime payload class, got %q",
				purpose,
				class,
			)
		}
	}
	return nil
}

func validateParent(
	purpose EgressPurpose,
	connectionID string,
	parent ParentRef,
	attemptID string,
) error {
	if err := validateIdentity("egress parent ID", parent.ID); err != nil {
		return err
	}
	if strings.Contains(attemptID, parent.ID) ||
		(parent.ExchangeID != "" &&
			strings.Contains(attemptID, parent.ExchangeID)) {
		return errors.New(
			"EgressAttempt ID encodes its parent; identities are independent",
		)
	}
	requireConnection := func() error {
		if err := validateIdentity(
			"egress connection ID",
			connectionID,
		); err != nil {
			return err
		}
		return nil
	}
	switch purpose {
	case PurposeProviderAttempt:
		if parent.Kind != ParentUpstreamAttempt {
			return errors.New("provider attempt requires an UpstreamAttempt parent")
		}
		// The Exchange and attempt identities are always present. A downstream
		// connection may be absent, because a runtime-originated Exchange such
		// as a quality run has no client connection.
		if err := validateIdentity(
			"egress parent Exchange ID",
			parent.ExchangeID,
		); err != nil {
			return err
		}
		if connectionID == "" {
			return nil
		}
		return requireConnection()
	case PurposeProfileOperation:
		if parent.Kind != ParentClientOperation || parent.ExchangeID != "" {
			return errors.New(
				"profile operation requires a client-operation parent with no Exchange",
			)
		}
		return requireConnection()
	case PurposeOriginalOrigin, PurposeAgentProbe:
		if parent.Kind != ParentOriginalRequest || parent.ExchangeID != "" {
			return errors.New(
				"original-origin egress requires an original-request parent with no Exchange",
			)
		}
		return requireConnection()
	case PurposeBlindTunnel:
		if parent.Kind != ParentBlindConnection || parent.ExchangeID != "" {
			return errors.New(
				"blind tunnel requires a blind-connection parent with no Exchange",
			)
		}
		return requireConnection()
	default:
		if parent.Kind != ParentRuntimeAction || parent.ExchangeID != "" {
			return errors.New(
				"runtime egress requires a runtime-action parent with no Exchange",
			)
		}
		if connectionID != "" {
			return errors.New("runtime egress has no client connection")
		}
		return nil
	}
}

func validateCaller(caller CallerKind, callerID string) error {
	switch caller {
	case CallerCore:
		if callerID != "" {
			return errors.New("a core caller cannot carry a plugin identity")
		}
		return nil
	case CallerPlugin:
		return validateIdentity("plugin caller ID", callerID)
	default:
		return errors.New("egress caller kind is required")
	}
}

func validateDecision(decision DecisionRef) error {
	if err := validateIdentity(
		"egress policy ID",
		decision.PolicyID,
	); err != nil {
		return err
	}
	if decision.PolicyRevision == 0 {
		return errors.New("egress policy revision is required")
	}
	if err := validateIdentity("egress rule ID", decision.RuleID); err != nil {
		return err
	}
	return validateIdentity("egress proxy ID", decision.ProxyID)
}

func validateIdentity(label, value string) error {
	if value == "" ||
		len(value) > 512 ||
		!utf8.ValidString(value) ||
		strings.TrimSpace(value) != value {
		return fmt.Errorf("%s is invalid", label)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("%s contains a control character", label)
		}
	}
	return nil
}

// BuiltInDirectPolicyID names the decision the runtime actually makes today:
// there is exactly one egress path and it is Direct. This is a real decision
// with a stable identity rather than a placeholder, so an audit reader is told
// which rule applied instead of being shown a blank. The egress-policy Goal
// replaces it with configured rules and revisions.
const (
	BuiltInDirectPolicyID = "builtin.direct"
	BuiltInDirectRuleID   = "builtin.direct.default"
	BuiltInDirectProxyID  = "direct"
)

// BuiltInDirectDecision returns that decision for the authority a purpose
// requires.
func BuiltInDirectDecision(authority PolicyAuthorityKind) DecisionRef {
	return DecisionRef{
		PolicyID:       BuiltInDirectPolicyID,
		PolicyRevision: 1,
		Authority:      authority,
		RuleID:         BuiltInDirectRuleID,
		ProxyID:        BuiltInDirectProxyID,
	}
}

// PurposeForEgressKind maps the Offline Hold admission class the transports
// already carry onto an audit purpose. The two taxonomies are orthogonal, so
// the mapping is explicit rather than a cast. The plugin class has no purpose
// on purpose: ADR-0015 makes a plugin a caller, never a destination owner, so
// its egress inherits the purpose of the resource it was granted.
func PurposeForEgressKind(kind string) (EgressPurpose, error) {
	switch kind {
	case "provider":
		return PurposeProviderAttempt, nil
	case "opaque":
		return PurposeOriginalOrigin, nil
	case "auxiliary":
		return PurposeAgentProbe, nil
	case "blind_tunnel":
		return PurposeBlindTunnel, nil
	case "update":
		return PurposeUpdate, nil
	default:
		return "", fmt.Errorf("no egress purpose for hold kind %q", kind)
	}
}

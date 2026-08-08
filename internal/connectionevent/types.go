// Package connectionevent owns the durable, body-free connection audit
// timeline. It is independent from semantic Exchange and Activity records.
package connectionevent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/environment"
)

const (
	MaxIdentityBytes   = 512
	MaxHostBytes       = 1024
	MaxPageSize        = 200
	MaxTimelineSize    = 128
	RecoveryErrorClass = "daemon_restarted"
)

var (
	ErrInvalidEvent    = errors.New("invalid ConnectionEvent")
	ErrInvalidCursor   = errors.New("invalid ConnectionEvent cursor")
	ErrInvalidPhase    = errors.New("invalid ConnectionEvent phase transition")
	ErrNotFound        = errors.New("ConnectionEvent was not found")
	ErrRuntimeStopping = errors.New("ConnectionEvent runtime is stopping")
)

type SourceConfidence string

const (
	SourceConfidenceVerified   SourceConfidence = "verified"
	SourceConfidenceConfigured SourceConfidence = "configured"
	SourceConfidenceUnknown    SourceConfidence = "unknown"
)

type Decision string

const (
	DecisionAllow Decision = "allow"
	DecisionDeny  Decision = "deny"
	DecisionAsk   Decision = "ask"
)

type EgressScope string

const (
	EgressScopeEnvironment EgressScope = "environment"
	EgressScopeNetwork     EgressScope = "network"
)

type EgressSource string

const (
	EgressSourceEnvironmentRule    EgressSource = "environment_rule"
	EgressSourceEnvironmentPlugin  EgressSource = "environment_plugin"
	EgressSourceEnvironmentDefault EgressSource = "environment_default"
	EgressSourceNetworkRule        EgressSource = "network_rule"
	EgressSourceNetworkDefault     EgressSource = "network_default"
)

type Decryption string

const (
	DecryptionBlind Decryption = "blind"
	DecryptionMITM  Decryption = "mitm"
	DecryptionNone  Decryption = "none"
)

type Phase string

const (
	PhaseAttempted Phase = "attempted"
	PhaseAsked     Phase = "asked"
	PhaseDecided   Phase = "decided"
	PhaseConnected Phase = "connected"
	PhaseClosed    Phase = "closed"
	PhaseFailed    Phase = "failed"
)

type Outcome string

const (
	OutcomeCompleted Outcome = "completed"
	OutcomeDenied    Outcome = "denied"
	OutcomeCanceled  Outcome = "canceled"
	OutcomeFailed    Outcome = "failed"
)

type Source struct {
	IngressID  string
	Label      string
	Confidence SourceConfidence
}

func (source Source) validate() error {
	switch source.Confidence {
	case SourceConfidenceUnknown:
		if err := validateIdentity("ingress ID", source.IngressID, true); err != nil {
			return err
		}
		return validateIdentity("source label", source.Label, true)
	case SourceConfidenceVerified, SourceConfidenceConfigured:
		if err := validateIdentity("ingress ID", source.IngressID, false); err != nil {
			return err
		}
		return validateIdentity("source label", source.Label, false)
	default:
		return fmt.Errorf("%w: source confidence is invalid", ErrInvalidEvent)
	}
}

// Event is one immutable phase snapshot for a connection. It contains no URL
// path, header, body, raw credential, or TLS byte sequence.
type Event struct {
	ConnectionID           string                       `json:"connectionId"`
	IngressID              string                       `json:"ingressId,omitempty"`
	SourceLabel            string                       `json:"sourceLabel,omitempty"`
	SourceConfidence       SourceConfidence             `json:"sourceConfidence"`
	EnvironmentID          environment.EnvironmentID    `json:"environmentId,omitempty"`
	EnvironmentName        string                       `json:"environmentName,omitempty"`
	EnvironmentRevision    environment.Revision         `json:"environmentRevision,omitempty"`
	ClientEndpointID       environment.ClientEndpointID `json:"clientEndpointId,omitempty"`
	ClientEndpointRevision environment.Revision         `json:"clientEndpointRevision,omitempty"`
	RequestedHost          string                       `json:"requestedHost"`
	ObservedSNI            string                       `json:"observedSni,omitempty"`
	RouteHost              string                       `json:"routeHost,omitempty"`
	IP                     string                       `json:"ip,omitempty"`
	Port                   uint16                       `json:"port"`
	Decision               Decision                     `json:"decision,omitempty"`
	RuleID                 string                       `json:"ruleId,omitempty"`
	CredentialBindingID    string                       `json:"credentialBindingId,omitempty"`
	EgressScope            EgressScope                  `json:"egressScope,omitempty"`
	EgressSource           EgressSource                 `json:"egressSource,omitempty"`
	EgressRuleID           string                       `json:"egressRuleId,omitempty"`
	EgressSelectorRunID    string                       `json:"egressSelectorRunId,omitempty"`
	EgressProxyID          string                       `json:"egressProxyId,omitempty"`
	EgressPolicyRevision   uint64                       `json:"egressPolicyRevision,omitempty"`
	Decryption             Decryption                   `json:"decryption"`
	Phase                  Phase                        `json:"phase"`
	BytesUp                uint64                       `json:"bytesUp"`
	BytesDown              uint64                       `json:"bytesDown"`
	StartedAt              time.Time                    `json:"startedAt"`
	EndedAt                time.Time                    `json:"endedAt,omitempty"`
	Outcome                Outcome                      `json:"outcome,omitempty"`
	ErrorClass             string                       `json:"errorClass,omitempty"`
}

// MarshalJSON omits EndedAt until a connection reaches a terminal phase.
// time.Time is a struct, so encoding/json does not honor omitempty for its zero
// value. Exposing year 1 as a terminal timestamp would make a valid in-flight
// record fail the control-contract validator.
func (event Event) MarshalJSON() ([]byte, error) {
	type eventAlias Event
	var endedAt *time.Time
	if !event.EndedAt.IsZero() {
		value := event.EndedAt
		endedAt = &value
	}
	return json.Marshal(struct {
		eventAlias
		EndedAt *time.Time `json:"endedAt,omitempty"`
	}{
		eventAlias: eventAlias(event),
		EndedAt:    endedAt,
	})
}

func (event Event) Validate() error {
	if err := validateIdentity(
		"connection ID",
		event.ConnectionID,
		false,
	); err != nil {
		return err
	}
	if err := (Source{
		IngressID:  event.IngressID,
		Label:      event.SourceLabel,
		Confidence: event.SourceConfidence,
	}).validate(); err != nil {
		return err
	}
	environmentEvidencePresent := event.EnvironmentID != "" ||
		event.EnvironmentName != "" ||
		event.EnvironmentRevision != 0
	if environmentEvidencePresent {
		_, environmentIDErr := environment.NewEnvironmentID(event.EnvironmentID.String())
		if environmentIDErr != nil ||
			validateIdentity("Environment name", event.EnvironmentName, false) != nil ||
			event.EnvironmentRevision == 0 ||
			event.EnvironmentRevision > environment.MaxRevision {
			return fmt.Errorf(
				"%w: Environment relation evidence is incomplete",
				ErrInvalidEvent,
			)
		}
	}
	clientEndpointEvidencePresent := event.ClientEndpointID != "" ||
		event.ClientEndpointRevision != 0
	if clientEndpointEvidencePresent {
		_, clientEndpointIDErr := environment.NewClientEndpointID(event.ClientEndpointID.String())
		if clientEndpointIDErr != nil ||
			event.ClientEndpointRevision == 0 ||
			event.ClientEndpointRevision > environment.MaxRevision {
			return fmt.Errorf(
				"%w: ClientEndpoint relation evidence is incomplete",
				ErrInvalidEvent,
			)
		}
		if !environmentEvidencePresent ||
			event.Decryption != DecryptionMITM ||
			event.Decision != DecisionAllow {
			return fmt.Errorf(
				"%w: ClientEndpoint relation exists without an allowed MITM decision",
				ErrInvalidEvent,
			)
		}
	}
	if err := validateHost("requested host", event.RequestedHost, false); err != nil {
		return err
	}
	for _, field := range []struct {
		label string
		value string
		host  bool
	}{
		{label: "observed SNI", value: event.ObservedSNI, host: true},
		{label: "route host", value: event.RouteHost, host: true},
		{label: "IP", value: event.IP, host: true},
		{label: "rule ID", value: event.RuleID},
		{label: "credential binding ID", value: event.CredentialBindingID},
		{label: "egress rule ID", value: event.EgressRuleID},
		{label: "egress selector run ID", value: event.EgressSelectorRunID},
		{label: "egress proxy ID", value: event.EgressProxyID},
		{label: "error class", value: event.ErrorClass},
	} {
		validator := validateIdentity
		if field.host {
			validator = validateHost
		}
		if err := validator(field.label, field.value, true); err != nil {
			return err
		}
	}
	if event.StartedAt.IsZero() {
		return fmt.Errorf("%w: start time is empty", ErrInvalidEvent)
	}
	switch event.Decryption {
	case DecryptionBlind, DecryptionMITM, DecryptionNone:
	default:
		return fmt.Errorf("%w: decryption state is invalid", ErrInvalidEvent)
	}
	switch event.Phase {
	case PhaseAttempted, PhaseAsked, PhaseDecided,
		PhaseConnected, PhaseClosed, PhaseFailed:
	default:
		return fmt.Errorf("%w: phase is invalid", ErrInvalidEvent)
	}
	if event.Decryption == DecryptionMITM &&
		event.Decision == DecisionAllow &&
		(!environmentEvidencePresent || !clientEndpointEvidencePresent) {
		return fmt.Errorf(
			"%w: allowed MITM has no Environment and ClientEndpoint relation",
			ErrInvalidEvent,
		)
	}
	if err := event.validateDecision(); err != nil {
		return err
	}
	if err := event.validateEgress(); err != nil {
		return err
	}
	return event.validateTerminal()
}

func (event Event) validateDecision() error {
	switch event.Decision {
	case "":
		if event.Phase != PhaseAttempted && event.Phase != PhaseFailed {
			return fmt.Errorf("%w: phase requires a decision", ErrInvalidEvent)
		}
		if event.RuleID != "" {
			return fmt.Errorf("%w: rule exists without a decision", ErrInvalidEvent)
		}
	case DecisionAllow:
		if event.RuleID == "" || event.RouteHost == "" || event.Port == 0 {
			return fmt.Errorf("%w: allow decision is incomplete", ErrInvalidEvent)
		}
		if event.Decryption == DecryptionNone {
			return fmt.Errorf("%w: allowed connection has no transport mode", ErrInvalidEvent)
		}
	case DecisionDeny:
		if event.RuleID == "" {
			return fmt.Errorf("%w: deny decision has no rule", ErrInvalidEvent)
		}
	case DecisionAsk:
		if event.RuleID == "" ||
			(event.Phase != PhaseAsked &&
				event.Phase != PhaseClosed &&
				event.Phase != PhaseFailed) {
			return fmt.Errorf("%w: ask decision is invalid", ErrInvalidEvent)
		}
	default:
		return fmt.Errorf("%w: decision is invalid", ErrInvalidEvent)
	}
	return nil
}

func (event Event) validateEgress() error {
	if event.EgressScope == "" && event.EgressSource == "" &&
		event.EgressPolicyRevision == 0 &&
		event.EgressRuleID == "" &&
		event.EgressSelectorRunID == "" &&
		event.EgressProxyID == "" {
		return nil
	}
	switch event.EgressScope {
	case EgressScopeEnvironment:
		switch event.EgressSource {
		case EgressSourceEnvironmentRule,
			EgressSourceEnvironmentPlugin,
			EgressSourceEnvironmentDefault:
		default:
			return fmt.Errorf("%w: Environment egress source is invalid", ErrInvalidEvent)
		}
	case EgressScopeNetwork:
		switch event.EgressSource {
		case EgressSourceNetworkRule, EgressSourceNetworkDefault:
		default:
			return fmt.Errorf("%w: network egress source is invalid", ErrInvalidEvent)
		}
	default:
		return fmt.Errorf("%w: egress scope is invalid", ErrInvalidEvent)
	}
	if event.EgressPolicyRevision == 0 {
		return fmt.Errorf("%w: egress policy revision is empty", ErrInvalidEvent)
	}
	return nil
}

func (event Event) validateTerminal() error {
	terminal := event.Phase == PhaseClosed ||
		event.Phase == PhaseFailed ||
		(event.Phase == PhaseDecided && event.Decision == DecisionDeny)
	if !terminal {
		if !event.EndedAt.IsZero() ||
			event.Outcome != "" ||
			event.ErrorClass != "" {
			return fmt.Errorf("%w: nonterminal event has terminal evidence", ErrInvalidEvent)
		}
		return nil
	}
	if event.EndedAt.IsZero() || event.EndedAt.Before(event.StartedAt) {
		return fmt.Errorf("%w: terminal time is invalid", ErrInvalidEvent)
	}
	switch event.Outcome {
	case OutcomeCompleted:
		if event.Phase != PhaseClosed || event.ErrorClass != "" {
			return fmt.Errorf("%w: completed outcome is invalid", ErrInvalidEvent)
		}
	case OutcomeDenied:
		if event.Phase != PhaseDecided ||
			event.Decision != DecisionDeny ||
			event.ErrorClass == "" {
			return fmt.Errorf("%w: denied outcome is invalid", ErrInvalidEvent)
		}
	case OutcomeCanceled:
		if event.Phase != PhaseClosed || event.ErrorClass == "" {
			return fmt.Errorf("%w: canceled outcome is invalid", ErrInvalidEvent)
		}
	case OutcomeFailed:
		if event.Phase != PhaseFailed || event.ErrorClass == "" {
			return fmt.Errorf("%w: failed outcome is invalid", ErrInvalidEvent)
		}
	default:
		return fmt.Errorf("%w: terminal outcome is invalid", ErrInvalidEvent)
	}
	return nil
}

type Record struct {
	Sequence int64 `json:"sequence"`
	Event
}

// MarshalJSON keeps the durable sequence alongside Event's zero-time
// omission. Without an explicit outer projection, Event.MarshalJSON would be
// promoted through embedding and hide Sequence from the wire document.
func (record Record) MarshalJSON() ([]byte, error) {
	type eventAlias Event
	var endedAt *time.Time
	if !record.EndedAt.IsZero() {
		value := record.EndedAt
		endedAt = &value
	}
	return json.Marshal(struct {
		Sequence int64 `json:"sequence"`
		eventAlias
		EndedAt *time.Time `json:"endedAt,omitempty"`
	}{
		Sequence:   record.Sequence,
		eventAlias: eventAlias(record.Event),
		EndedAt:    endedAt,
	})
}

func (record Record) Validate() error {
	if record.Sequence <= 0 {
		return fmt.Errorf("%w: sequence is invalid", ErrInvalidEvent)
	}
	return record.Event.Validate()
}

type PageRequest struct {
	BeforeSequence      int64
	Limit               int
	IngressID           string
	LatestPerConnection bool
}

func (request PageRequest) Validate() error {
	if request.BeforeSequence < 0 ||
		request.Limit <= 0 ||
		request.Limit > MaxPageSize ||
		validateIdentity("ingress filter", request.IngressID, true) != nil {
		return ErrInvalidEvent
	}
	return nil
}

type Page struct {
	Items      []Record `json:"items"`
	NextCursor string   `json:"nextCursor,omitempty"`
}

type Timeline struct {
	ConnectionID string   `json:"connectionId"`
	Events       []Record `json:"events"`
}

type Repository interface {
	Append(context.Context, Event) (Record, error)
	List(context.Context, PageRequest) (Page, error)
	Timeline(context.Context, string) (Timeline, error)
	Recover(context.Context, time.Time) (int, error)
}

type Reader interface {
	List(context.Context, PageRequest) (Page, error)
	Timeline(context.Context, string) (Timeline, error)
}

func Cursor(sequence int64) (string, error) {
	if sequence <= 0 {
		return "", ErrInvalidCursor
	}
	payload := "v1:" + strconv.FormatInt(sequence, 10)
	return base64.RawURLEncoding.EncodeToString([]byte(payload)), nil
}

func ParseCursor(value string) (int64, error) {
	if value == "" ||
		len(value) > 128 ||
		strings.ContainsAny(value, " \t\r\n=") {
		return 0, ErrInvalidCursor
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || !strings.HasPrefix(string(decoded), "v1:") {
		return 0, ErrInvalidCursor
	}
	sequence, err := strconv.ParseInt(strings.TrimPrefix(string(decoded), "v1:"), 10, 64)
	if err != nil || sequence <= 0 {
		return 0, ErrInvalidCursor
	}
	canonical, err := Cursor(sequence)
	if err != nil || canonical != value {
		return 0, ErrInvalidCursor
	}
	return sequence, nil
}

func validateIdentity(label, value string, allowEmpty bool) error {
	if value == "" && allowEmpty {
		return nil
	}
	if value == "" ||
		len(value) > MaxIdentityBytes ||
		!utf8.ValidString(value) ||
		strings.TrimSpace(value) != value {
		return fmt.Errorf("%w: %s is invalid", ErrInvalidEvent, label)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("%w: %s contains a control character", ErrInvalidEvent, label)
		}
	}
	return nil
}

func validateHost(label, value string, allowEmpty bool) error {
	if value == "" && allowEmpty {
		return nil
	}
	if value == "" || len(value) > MaxHostBytes {
		return fmt.Errorf("%w: %s is invalid", ErrInvalidEvent, label)
	}
	// A host is not an authority. The port is a separate field, and a host
	// field holding one would state the port twice and render it twice.
	if strings.ContainsAny(value, ":/") {
		return fmt.Errorf(
			"%w: %s carries a port or a path",
			ErrInvalidEvent,
			label,
		)
	}
	return validateIdentity(label, value, false)
}

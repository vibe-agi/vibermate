// Package activity owns the durable, redacted timeline projected by the local
// control plane. Records contain stable identifiers and reason codes, never
// prompts, credentials, headers, or raw tool arguments.
package activity

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
	MaxPageSize      = 200
)

var (
	ErrExchangeNotFound = errors.New("Activity Exchange was not found")
	ErrInvalidEvent     = errors.New("invalid Activity event")
	ErrRuntimeStopping  = errors.New("Activity runtime is stopping")
)

type Kind string

const (
	KindAccessApplied            Kind = "access.applied"
	KindCredentialSecretReplaced Kind = "credential.secret_replaced"
	KindOfflineHoldEntered       Kind = "offline_hold.entered"
	KindOfflineHoldResumed       Kind = "offline_hold.resumed"
	KindApprovalPending          Kind = "approval.pending"
	KindApprovalResolved         Kind = "approval.resolved"
	KindExchangeCompleted        Kind = "exchange.completed"
)

type Status string

const (
	StatusSucceeded Status = "succeeded"
	StatusPending   Status = "pending"
	StatusFailed    Status = "failed"
	StatusCanceled  Status = "canceled"
)

type Event struct {
	Kind       Kind
	AccessID   access.AccessID
	SubjectID  string
	Status     Status
	ReasonCode string
	Diagnosis  Diagnosis
	Transport  *TransportEvidence
}

func (event Event) Validate() error {
	switch event.Kind {
	case KindAccessApplied,
		KindCredentialSecretReplaced,
		KindOfflineHoldEntered,
		KindOfflineHoldResumed,
		KindApprovalPending,
		KindApprovalResolved,
		KindExchangeCompleted:
	default:
		return fmt.Errorf("%w: kind is unsupported", ErrInvalidEvent)
	}
	switch event.Status {
	case StatusSucceeded, StatusPending, StatusFailed, StatusCanceled:
	default:
		return fmt.Errorf("%w: status is unsupported", ErrInvalidEvent)
	}
	if err := validateIdentity("subject ID", event.SubjectID, false); err != nil {
		return err
	}
	if event.ReasonCode != "" {
		if err := validateIdentity("reason code", event.ReasonCode, false); err != nil {
			return err
		}
	}
	if err := event.Diagnosis.validate(); err != nil {
		return err
	}
	if event.Kind == KindAccessApplied && event.AccessID.String() == "" {
		return fmt.Errorf("%w: Access event has no Access ID", ErrInvalidEvent)
	}
	if event.Transport != nil {
		if event.Kind != KindExchangeCompleted {
			return fmt.Errorf(
				"%w: transport evidence belongs only to an Exchange",
				ErrInvalidEvent,
			)
		}
		if err := event.Transport.Validate(); err != nil {
			return err
		}
	}
	return nil
}

type TransportProfileEvidence struct {
	Ref      string `json:"ref"`
	Revision uint64 `json:"revision"`
	Source   string `json:"source"`
}

// WirePresentationEvidence records the user-visible product-level choice and
// the same-protocol variant selected for this Exchange. It never stores the
// observed ClientHello or semantic headers.
type WirePresentationEvidence struct {
	RequestedRef     string `json:"requestedRef"`
	EffectiveRef     string `json:"effectiveRef,omitempty"`
	Revision         uint64 `json:"revision"`
	Mode             string `json:"mode"`
	Product          string `json:"product,omitempty"`
	ClientProtocol   string `json:"clientProtocol"`
	UpstreamProtocol string `json:"upstreamProtocol,omitempty"`
	EvidenceDigest   string `json:"evidenceDigest,omitempty"`
}

func (evidence WirePresentationEvidence) Validate() error {
	if validateIdentity(
		"requested wire presentation reference",
		evidence.RequestedRef,
		false,
	) != nil ||
		evidence.Revision == 0 {
		return fmt.Errorf(
			"%w: wire presentation identity is invalid",
			ErrInvalidEvent,
		)
	}
	switch evidence.Mode {
	case "follow_client":
		if evidence.Product != "" || evidence.EvidenceDigest != "" {
			return fmt.Errorf(
				"%w: follow-client presentation contains product evidence",
				ErrInvalidEvent,
			)
		}
	case "emulate_product":
		if evidence.Product != "claude_code" {
			return fmt.Errorf(
				"%w: emulated presentation product is invalid",
				ErrInvalidEvent,
			)
		}
	default:
		return fmt.Errorf(
			"%w: wire presentation mode is invalid",
			ErrInvalidEvent,
		)
	}
	if evidence.ClientProtocol != "http/1.1" && evidence.ClientProtocol != "h2" {
		return fmt.Errorf(
			"%w: wire presentation client protocol is invalid",
			ErrInvalidEvent,
		)
	}
	if evidence.EffectiveRef == "" {
		if evidence.UpstreamProtocol != "" || evidence.EvidenceDigest != "" {
			return fmt.Errorf(
				"%w: unavailable wire presentation contains effective evidence",
				ErrInvalidEvent,
			)
		}
		return nil
	}
	if evidence.EffectiveRef != evidence.RequestedRef ||
		evidence.UpstreamProtocol != evidence.ClientProtocol {
		return fmt.Errorf(
			"%w: wire presentation changed profile or client protocol",
			ErrInvalidEvent,
		)
	}
	if evidence.Mode == "emulate_product" && evidence.EvidenceDigest == "" {
		return fmt.Errorf(
			"%w: emulated presentation has no evidence digest",
			ErrInvalidEvent,
		)
	}
	if evidence.EvidenceDigest != "" {
		if len(evidence.EvidenceDigest) != 64 {
			return fmt.Errorf(
				"%w: wire presentation evidence digest is invalid",
				ErrInvalidEvent,
			)
		}
		for _, character := range evidence.EvidenceDigest {
			if !((character >= '0' && character <= '9') ||
				(character >= 'a' && character <= 'f')) {
				return fmt.Errorf(
					"%w: wire presentation evidence digest is invalid",
					ErrInvalidEvent,
				)
			}
		}
	}
	return nil
}

func (evidence TransportProfileEvidence) Validate() error {
	if validateIdentity(
		"transport profile reference",
		evidence.Ref,
		false,
	) != nil ||
		evidence.Revision == 0 {
		return fmt.Errorf(
			"%w: transport profile evidence is invalid",
			ErrInvalidEvent,
		)
	}
	switch evidence.Source {
	case "observed_client", "named_profile", "standard":
		return nil
	default:
		return fmt.Errorf(
			"%w: transport profile source is invalid",
			ErrInvalidEvent,
		)
	}
}

// TransportEvidence is a redacted durable projection of actual transport
// selection. It carries no raw ClientHello, certificate, header, or secret.
type TransportEvidence struct {
	Presentation             *WirePresentationEvidence  `json:"presentation,omitempty"`
	Requested                *TransportProfileEvidence  `json:"requested,omitempty"`
	Effective                *TransportProfileEvidence  `json:"effective,omitempty"`
	FallbackChain            []TransportProfileEvidence `json:"fallbackChain"`
	FallbackReason           string                     `json:"fallbackReason,omitempty"`
	ClientOfferedALPN        []string                   `json:"clientOfferedAlpn"`
	DownstreamNegotiatedALPN string                     `json:"downstreamNegotiatedAlpn,omitempty"`
	UpstreamOfferedALPN      []string                   `json:"upstreamOfferedAlpn"`
	UpstreamNegotiatedALPN   string                     `json:"upstreamNegotiatedAlpn,omitempty"`
	HTTPTransport            string                     `json:"httpTransport,omitempty"`
}

func (evidence TransportEvidence) Validate() error {
	if evidence.Presentation == nil {
		return fmt.Errorf(
			"%w: transport evidence has no wire presentation",
			ErrInvalidEvent,
		)
	}
	if err := evidence.Presentation.Validate(); err != nil {
		return err
	}
	if evidence.Requested == nil {
		if evidence.Effective != nil ||
			len(evidence.FallbackChain) != 0 ||
			evidence.FallbackReason != "" ||
			len(evidence.ClientOfferedALPN) != 0 ||
			evidence.DownstreamNegotiatedALPN != "" ||
			len(evidence.UpstreamOfferedALPN) != 0 ||
			evidence.UpstreamNegotiatedALPN != "" ||
			evidence.HTTPTransport != "" {
			return fmt.Errorf(
				"%w: presentation-only evidence contains transport facts",
				ErrInvalidEvent,
			)
		}
		return nil
	}
	if evidence.Presentation.EffectiveRef == "" {
		return fmt.Errorf(
			"%w: transport facts exist for an unavailable wire presentation",
			ErrInvalidEvent,
		)
	}
	if err := evidence.Requested.Validate(); err != nil {
		return err
	}
	expectedSource := "observed_client"
	if evidence.Presentation.Mode == "emulate_product" {
		expectedSource = "named_profile"
	}
	if evidence.Requested.Source != expectedSource {
		return fmt.Errorf(
			"%w: wire presentation and transport source disagree",
			ErrInvalidEvent,
		)
	}
	if len(evidence.FallbackChain) == 0 ||
		len(evidence.FallbackChain) > 16 ||
		evidence.FallbackChain[0] != *evidence.Requested {
		return fmt.Errorf(
			"%w: transport fallback chain is invalid",
			ErrInvalidEvent,
		)
	}
	for _, profile := range evidence.FallbackChain {
		if err := profile.Validate(); err != nil {
			return err
		}
	}
	if evidence.Effective != nil {
		if err := evidence.Effective.Validate(); err != nil {
			return err
		}
		if evidence.FallbackChain[len(evidence.FallbackChain)-1] !=
			*evidence.Effective {
			return fmt.Errorf(
				"%w: effective transport is outside the fallback chain",
				ErrInvalidEvent,
			)
		}
	}
	if evidence.FallbackReason != "" {
		if err := validateIdentity(
			"transport fallback reason",
			evidence.FallbackReason,
			false,
		); err != nil {
			return err
		}
	}
	for _, protocols := range [][]string{
		evidence.ClientOfferedALPN,
		evidence.UpstreamOfferedALPN,
	} {
		if len(protocols) > 16 {
			return fmt.Errorf(
				"%w: transport ALPN evidence exceeds the limit",
				ErrInvalidEvent,
			)
		}
		for _, protocol := range protocols {
			if err := validateIdentity("ALPN protocol", protocol, false); err != nil {
				return err
			}
		}
	}
	for _, protocol := range []string{
		evidence.DownstreamNegotiatedALPN,
		evidence.UpstreamNegotiatedALPN,
	} {
		if protocol != "" {
			if err := validateIdentity("negotiated ALPN", protocol, false); err != nil {
				return err
			}
		}
	}
	switch evidence.HTTPTransport {
	case "http1":
		if evidence.Presentation.UpstreamProtocol != "http/1.1" {
			return fmt.Errorf(
				"%w: wire presentation and HTTP transport disagree",
				ErrInvalidEvent,
			)
		}
	case "http2":
		if evidence.Presentation.UpstreamProtocol != "h2" {
			return fmt.Errorf(
				"%w: wire presentation and HTTP transport disagree",
				ErrInvalidEvent,
			)
		}
	default:
		return fmt.Errorf(
			"%w: HTTP transport evidence is invalid",
			ErrInvalidEvent,
		)
	}
	return nil
}

func (evidence TransportEvidence) Clone() TransportEvidence {
	cloned := evidence
	if evidence.Presentation != nil {
		presentation := *evidence.Presentation
		cloned.Presentation = &presentation
	}
	if evidence.Requested != nil {
		requested := *evidence.Requested
		cloned.Requested = &requested
	}
	if evidence.Effective != nil {
		effective := *evidence.Effective
		cloned.Effective = &effective
	}
	cloned.FallbackChain = slices.Clone(evidence.FallbackChain)
	cloned.ClientOfferedALPN = slices.Clone(evidence.ClientOfferedALPN)
	cloned.UpstreamOfferedALPN = slices.Clone(evidence.UpstreamOfferedALPN)
	return cloned
}

// Record is the immutable durable projection returned by readers.
type Record struct {
	Sequence   int64              `json:"sequence"`
	ID         string             `json:"id"`
	OccurredAt time.Time          `json:"occurredAt"`
	Kind       Kind               `json:"kind"`
	AccessID   string             `json:"accessId,omitempty"`
	SubjectID  string             `json:"subjectId"`
	Status     Status             `json:"status"`
	ReasonCode string             `json:"reasonCode,omitempty"`
	Diagnosis  *Diagnosis         `json:"diagnosis,omitempty"`
	Transport  *TransportEvidence `json:"transport,omitempty"`
}

// Diagnosis is what a failed request can say about itself without saying what
// it contained. Design 06 §4.1 bounds it: an HTTP status, a field name from a
// closed vocabulary, and a path of field names and indices. No value from the
// request, no credential, and no provider text appears here.
type Diagnosis struct {
	ProviderStatus int    `json:"providerStatus,omitempty"`
	ProviderField  string `json:"providerField,omitempty"`
	ClientField    string `json:"clientField,omitempty"`
	// ClientPath names where in the request's shape the failure happened. A
	// closed vocabulary cannot name a field the translator does not model,
	// which is exactly the case that was impossible to diagnose.
	ClientPath string `json:"clientPath,omitempty"`
}

// Empty reports a diagnosis that says nothing.
func (diagnosis Diagnosis) Empty() bool {
	return diagnosis == Diagnosis{}
}

func (diagnosis Diagnosis) validate() error {
	if diagnosis.ProviderStatus < 0 || diagnosis.ProviderStatus > 599 {
		return fmt.Errorf("%w: provider status is invalid", ErrInvalidEvent)
	}
	for label, value := range map[string]string{
		"provider field": diagnosis.ProviderField,
		"client field":   diagnosis.ClientField,
	} {
		if len(value) > 128 || strings.TrimSpace(value) != value {
			return fmt.Errorf("%w: %s is invalid", ErrInvalidEvent, label)
		}
	}
	if len(diagnosis.ClientPath) > 256 {
		return fmt.Errorf("%w: client path is too long", ErrInvalidEvent)
	}
	// A path is field names and indices. Anything else came from somewhere
	// else, and a diagnostic that leaks content is worse than none.
	for _, character := range diagnosis.ClientPath {
		switch {
		case character >= 'a' && character <= 'z',
			character >= 'A' && character <= 'Z',
			character >= '0' && character <= '9',
			character == '$',
			character == '.',
			character == '_',
			character == '-',
			character == '[',
			character == ']':
		default:
			return fmt.Errorf(
				"%w: client path is not a structural path",
				ErrInvalidEvent,
			)
		}
	}
	return nil
}

func (record Record) Validate() error {
	if record.Sequence <= 0 ||
		record.OccurredAt.IsZero() ||
		validateIdentity("Activity ID", record.ID, false) != nil {
		return ErrInvalidEvent
	}
	var accessID access.AccessID
	var err error
	if record.AccessID != "" {
		accessID, err = access.NewAccessID(record.AccessID)
		if err != nil {
			return err
		}
	}
	return Event{
		Kind:       record.Kind,
		AccessID:   accessID,
		SubjectID:  record.SubjectID,
		Status:     record.Status,
		ReasonCode: record.ReasonCode,
		Transport:  record.Transport,
	}.Validate()
}

type PageRequest struct {
	BeforeSequence int64
	Limit          int
}

func (request PageRequest) Validate() error {
	if request.BeforeSequence < 0 ||
		request.Limit <= 0 ||
		request.Limit > MaxPageSize {
		return ErrInvalidEvent
	}
	return nil
}

type Page struct {
	Items              []Record `json:"items"`
	NextBeforeSequence int64    `json:"nextBeforeSequence,omitempty"`
}

type Repository interface {
	Append(context.Context, Record) (Record, error)
	GetExchange(context.Context, string) (Record, error)
	List(context.Context, PageRequest) (Page, error)
	ListExchanges(context.Context, PageRequest) (Page, error)
}

type Recorder interface {
	Record(context.Context, Event) (Record, error)
}

type Reader interface {
	GetExchange(context.Context, string) (Record, error)
	List(context.Context, PageRequest) (Page, error)
	ListExchanges(context.Context, PageRequest) (Page, error)
}

type Runtime interface {
	Recorder
	Reader
	Shutdown(context.Context) error
}

func validateIdentity(label string, value string, allowEmpty bool) error {
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

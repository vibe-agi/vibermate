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
	ErrInvalidEvent    = errors.New("invalid Activity event")
	ErrRuntimeStopping = errors.New("Activity runtime is stopping")
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
	Requested                TransportProfileEvidence   `json:"requested"`
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
	if err := evidence.Requested.Validate(); err != nil {
		return err
	}
	if len(evidence.FallbackChain) == 0 ||
		len(evidence.FallbackChain) > 16 ||
		evidence.FallbackChain[0] != evidence.Requested {
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
	case "", "http1", "http2":
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
	Transport  *TransportEvidence `json:"transport,omitempty"`
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
	List(context.Context, PageRequest) (Page, error)
}

type Recorder interface {
	Record(context.Context, Event) (Record, error)
}

type Reader interface {
	List(context.Context, PageRequest) (Page, error)
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

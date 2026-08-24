// Package agentconversation derives rebuildable Agent Conversation projections
// from explicit protocol evidence. Native parent identifiers are retained for
// presentation, but relationships are never invented from time, title, or
// content and Exchanges are never merged by those heuristics.
package agentconversation

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

const maxIdentityBytes = 512

const maxClientEvidenceValues = 8192

const (
	ClientIdentitySourceLocalState       = "client_local_state"
	ClientIdentitySourceProtocolEvidence = "client_protocol_evidence"
)

// ClientEvidenceValue retains one client-specific value without forcing
// heterogeneous Agent clients into a false shared wire schema. Names are
// explicitly namespaced (for example claude.request_id or
// codex.response_item_id); generic Conversation code treats values as opaque.
type ClientEvidenceValue struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// ClientIdentityLookup is an exact join request. Batch resolution lets a
// client adapter scan its append-only local authority once for many Exchanges
// instead of re-reading it once per Turn.
type ClientIdentityLookup struct {
	ProviderResponseID       string
	ObservedAt               time.Time
	ProtocolEvidence         []protocolcore.ProtocolEvidenceValue
	ResponseProtocolEvidence []protocolcore.ProtocolEvidenceValue
}

// ClientIdentityResolver is the protocol-adapter seam. Every implementation
// retains its own namespaced identifiers and attributes while returning the
// small common identity needed to build a Conversation index.
type ClientIdentityResolver interface {
	ResolveBatch(
		context.Context,
		string,
		[]ClientIdentityLookup,
	) (map[string]ClientIdentity, error)
}

// ClientIdentity is durable, protocol-neutral association evidence recovered
// from a client-owned local session log. It deliberately preserves the
// provider's opaque identifiers instead of trying to derive identity from
// request timing, titles, or prompt content.
type ClientIdentity struct {
	Client             string                `json:"client"`
	SessionID          string                `json:"sessionId"`
	SessionResumable   bool                  `json:"sessionResumable"`
	ActorID            string                `json:"actorId,omitempty"`
	ActorLabel         string                `json:"actorLabel,omitempty"`
	ActorType          string                `json:"actorType,omitempty"`
	ActorIsSubagent    bool                  `json:"actorIsSubagent"`
	ProviderResponseID string                `json:"providerResponseId,omitempty"`
	ProviderMessageID  string                `json:"providerMessageId,omitempty"`
	Source             string                `json:"source"`
	Confidence         string                `json:"confidence"`
	ObservedAt         time.Time             `json:"observedAt"`
	ProtocolIDs        []ClientEvidenceValue `json:"protocolIds,omitempty"`
	Attributes         []ClientEvidenceValue `json:"attributes,omitempty"`
}

func (identity ClientIdentity) Validate() error {
	providerResponseOptional := identity.Source == ClientIdentitySourceProtocolEvidence
	if !validText(identity.Client, false) ||
		!validText(identity.SessionID, false) ||
		!validText(identity.ActorID, true) ||
		!validText(identity.ActorLabel, true) ||
		!validText(identity.ActorType, true) ||
		!validText(identity.ProviderResponseID, providerResponseOptional) ||
		!validText(identity.ProviderMessageID, true) ||
		(identity.Source != ClientIdentitySourceLocalState &&
			identity.Source != ClientIdentitySourceProtocolEvidence) ||
		identity.Confidence != "exact" || identity.ObservedAt.IsZero() {
		return errors.New("client Agent identity evidence is invalid")
	}
	if identity.Client != "claude" && identity.Client != "codex" {
		return errors.New("client Agent identity kind is unsupported")
	}
	if identity.ActorID == "" &&
		(identity.ActorLabel != "" || identity.ActorType != "" || identity.ActorIsSubagent) {
		return errors.New("client Agent actor attribution has no actor identity")
	}
	if err := validateEvidenceValues(identity.ProtocolIDs, false); err != nil {
		return fmt.Errorf("client Agent protocol IDs: %w", err)
	}
	if err := validateEvidenceValues(identity.Attributes, true); err != nil {
		return fmt.Errorf("client Agent attributes: %w", err)
	}
	return nil
}

// ClientIdentityFromProtocolEvidence derives only identities explicitly
// advertised by the client protocol. It deliberately requires a native
// session identifier and never falls back to timing, prompt text, or titles.
// Local client state remains the richer authority and may later replace this
// read-time fallback with labels, actor type, and additional attributes.
func ClientIdentityFromProtocolEvidence(
	values []protocolcore.ProtocolEvidenceValue,
	providerResponseID string,
	observedAt time.Time,
) (ClientIdentity, bool) {
	if observedAt.IsZero() ||
		protocolcore.ValidateProtocolEvidence(values) != nil ||
		!validText(providerResponseID, true) {
		return ClientIdentity{}, false
	}
	fields := make(map[string]string, 6)
	protocolIDs := make([]ClientEvidenceValue, 0, 6)
	client := ""
	assignClient := func(value string) bool {
		if client != "" && client != value {
			return false
		}
		client = value
		return true
	}
	for _, value := range values {
		switch value.Name {
		case "claude.agent_id", "claude.parent_agent_id", "claude.session_id":
			if !assignClient("claude") {
				return ClientIdentity{}, false
			}
		case "openai_responses.session_id", "openai_responses.thread_id",
			"openai_responses.turn_id", "openai_responses.previous_response_id":
			if !assignClient("codex") {
				return ClientIdentity{}, false
			}
		default:
			continue
		}
		if previous, present := fields[value.Name]; present && previous != value.Value {
			return ClientIdentity{}, false
		}
		fields[value.Name] = value.Value
		protocolIDs = append(protocolIDs, ClientEvidenceValue{
			Name: value.Name, Value: value.Value,
		})
	}
	sessionID := fields["claude.session_id"]
	if client == "codex" {
		sessionID = fields["openai_responses.session_id"]
	}
	actorID := fields["claude.agent_id"]
	parentID := fields["claude.parent_agent_id"]
	if client == "" || sessionID == "" ||
		(client == "claude" && parentID != "" && actorID == "") {
		return ClientIdentity{}, false
	}
	identity := ClientIdentity{
		Client: client, SessionID: sessionID, SessionResumable: true,
		ActorID: actorID, ActorIsSubagent: parentID != "",
		ProviderResponseID: providerResponseID,
		Source:             ClientIdentitySourceProtocolEvidence,
		Confidence:         "exact",
		ObservedAt:         observedAt.UTC().Truncate(time.Millisecond),
		ProtocolIDs:        protocolIDs,
	}
	if client == "claude" && providerResponseID != "" {
		identity.ProviderMessageID = providerResponseID
	}
	if identity.Validate() != nil {
		return ClientIdentity{}, false
	}
	return identity, true
}

// Clone keeps repository and API boundaries from sharing mutable evidence
// slices with protocol-specific resolvers.
func (identity ClientIdentity) Clone() ClientIdentity {
	identity.ProtocolIDs = slices.Clone(identity.ProtocolIDs)
	identity.Attributes = slices.Clone(identity.Attributes)
	return identity
}

// Equal compares the canonical evidence rather than Go slice headers.
func (identity ClientIdentity) Equal(other ClientIdentity) bool {
	left := identity.Clone()
	right := other.Clone()
	left.ObservedAt = left.ObservedAt.UTC().Truncate(time.Millisecond)
	right.ObservedAt = right.ObservedAt.UTC().Truncate(time.Millisecond)
	return left.Client == right.Client &&
		left.SessionID == right.SessionID &&
		left.SessionResumable == right.SessionResumable &&
		left.ActorID == right.ActorID &&
		left.ActorLabel == right.ActorLabel &&
		left.ActorType == right.ActorType &&
		left.ActorIsSubagent == right.ActorIsSubagent &&
		left.ProviderResponseID == right.ProviderResponseID &&
		left.ProviderMessageID == right.ProviderMessageID &&
		left.Source == right.Source &&
		left.Confidence == right.Confidence &&
		left.ObservedAt.Equal(right.ObservedAt) &&
		slices.Equal(left.ProtocolIDs, right.ProtocolIDs) &&
		slices.Equal(left.Attributes, right.Attributes)
}

// MergeClientIdentity monotonically deepens exact identity evidence for one
// Exchange. Protocol headers are available before a semantic transcript (and
// even when content recording is disabled); a later client-local lookup may
// add labels and native IDs, but neither source may rewrite the session or
// actor that was already observed on the wire.
func MergeClientIdentity(
	existing ClientIdentity,
	incoming ClientIdentity,
) (ClientIdentity, bool, error) {
	existing = existing.Clone()
	incoming = incoming.Clone()
	existing.ObservedAt = existing.ObservedAt.UTC().Truncate(time.Millisecond)
	incoming.ObservedAt = incoming.ObservedAt.UTC().Truncate(time.Millisecond)
	if existing.Validate() != nil || incoming.Validate() != nil {
		return ClientIdentity{}, false, errors.New("client Agent identity merge input is invalid")
	}
	if existing.Client != incoming.Client ||
		existing.SessionID != incoming.SessionID ||
		existing.SessionResumable != incoming.SessionResumable ||
		existing.ActorID != incoming.ActorID {
		return ClientIdentity{}, false, errors.New("client Agent identity association changed")
	}

	merged := existing.Clone()
	var err error
	if merged.ActorIsSubagent, err = mergeActorSubagent(existing, incoming); err != nil {
		return ClientIdentity{}, false, err
	}
	if merged.ActorLabel, err = mergeOptionalIdentityValue(
		"actor label", existing.ActorLabel, incoming.ActorLabel,
	); err != nil {
		return ClientIdentity{}, false, err
	}
	if merged.ActorType, err = mergeOptionalIdentityValue(
		"actor type", existing.ActorType, incoming.ActorType,
	); err != nil {
		return ClientIdentity{}, false, err
	}
	if merged.ProviderResponseID, err = mergeOptionalIdentityValue(
		"provider response ID",
		existing.ProviderResponseID,
		incoming.ProviderResponseID,
	); err != nil {
		return ClientIdentity{}, false, err
	}
	if merged.ProviderMessageID, err = mergeOptionalIdentityValue(
		"provider message ID",
		existing.ProviderMessageID,
		incoming.ProviderMessageID,
	); err != nil {
		return ClientIdentity{}, false, err
	}
	merged.ProtocolIDs, err = mergeIdentityEvidence(
		existing.ProtocolIDs,
		incoming.ProtocolIDs,
		false,
	)
	if err != nil {
		return ClientIdentity{}, false, err
	}
	merged.Attributes, err = mergeIdentityEvidence(
		existing.Attributes,
		incoming.Attributes,
		true,
	)
	if err != nil {
		return ClientIdentity{}, false, err
	}
	if incoming.Source == ClientIdentitySourceLocalState {
		merged.Source = ClientIdentitySourceLocalState
	}
	if incoming.ObservedAt.Before(merged.ObservedAt) {
		merged.ObservedAt = incoming.ObservedAt
	}
	if err := merged.Validate(); err != nil {
		return ClientIdentity{}, false, fmt.Errorf(
			"merged client Agent identity: %w", err,
		)
	}
	return merged, !merged.Equal(existing), nil
}

// mergeActorSubagent treats the boolean as positive evidence only. A missing
// parent identifier means unknown, not root, so any later exact observation may
// deepen false to true and a sparse retry cannot erase that fact. Conflicting
// concrete parent identifiers are rejected by mergeIdentityEvidence below.
func mergeActorSubagent(existing, incoming ClientIdentity) (bool, error) {
	return existing.ActorIsSubagent || incoming.ActorIsSubagent, nil
}

func mergeOptionalIdentityValue(name, existing, incoming string) (string, error) {
	switch {
	case existing == "":
		return incoming, nil
	case incoming == "" || incoming == existing:
		return existing, nil
	default:
		return "", fmt.Errorf("client Agent identity %s changed", name)
	}
}

func mergeIdentityEvidence(
	existing []ClientEvidenceValue,
	incoming []ClientEvidenceValue,
	singleValueNames bool,
) ([]ClientEvidenceValue, error) {
	values := make(map[ClientEvidenceValue]struct{}, len(existing)+len(incoming))
	named := make(map[string]string, len(existing)+len(incoming))
	associationNames := map[string]struct{}{
		"claude.agent_id": {}, "claude.parent_agent_id": {},
		"claude.session_id": {}, "codex.session_id": {},
		"codex.thread_id": {}, "codex.parent_thread_id": {},
		"codex.forked_from_thread_id": {},
	}
	appendValues := func(source []ClientEvidenceValue) error {
		for _, value := range source {
			if singleValueNames {
				if previous, present := named[value.Name]; present && previous != value.Value {
					return fmt.Errorf(
						"client Agent identity attribute %q changed", value.Name,
					)
				}
				named[value.Name] = value.Value
			} else if _, stable := associationNames[value.Name]; stable {
				if previous, present := named[value.Name]; present && previous != value.Value {
					return fmt.Errorf(
						"client Agent identity protocol ID %q changed", value.Name,
					)
				}
				named[value.Name] = value.Value
			}
			values[value] = struct{}{}
		}
		return nil
	}
	if err := appendValues(existing); err != nil {
		return nil, err
	}
	if err := appendValues(incoming); err != nil {
		return nil, err
	}
	merged := make([]ClientEvidenceValue, 0, len(values))
	for value := range values {
		merged = append(merged, value)
	}
	slices.SortFunc(merged, func(left, right ClientEvidenceValue) int {
		if byName := strings.Compare(left.Name, right.Name); byName != 0 {
			return byName
		}
		return strings.Compare(left.Value, right.Value)
	})
	return merged, nil
}

func validateEvidenceValues(values []ClientEvidenceValue, singleValueNames bool) error {
	if len(values) > maxClientEvidenceValues {
		return errors.New("evidence value count exceeds the durable limit")
	}
	seenPairs := make(map[ClientEvidenceValue]struct{}, len(values))
	seenNames := make(map[string]struct{}, len(values))
	previous := ClientEvidenceValue{}
	for index, value := range values {
		if !validEvidenceName(value.Name) || !validText(value.Value, false) {
			return errors.New("evidence value is invalid")
		}
		if index > 0 && (value.Name < previous.Name ||
			(value.Name == previous.Name && value.Value < previous.Value)) {
			return errors.New("evidence values are not canonically ordered")
		}
		if _, duplicate := seenPairs[value]; duplicate {
			return errors.New("evidence value is duplicated")
		}
		seenPairs[value] = struct{}{}
		if singleValueNames {
			if _, duplicate := seenNames[value.Name]; duplicate {
				return errors.New("attribute name is duplicated")
			}
			seenNames[value.Name] = struct{}{}
		}
		previous = value
	}
	return nil
}

func validEvidenceName(value string) bool {
	if value == "" || len(value) > 128 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') ||
			character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

type Kind string

const (
	KindPendingExchange  Kind = "pending_exchange"
	KindMain             Kind = "main"
	KindAgent            Kind = "agent"
	KindIsolatedSubagent Kind = "isolated_subagent"
	KindIsolatedExchange Kind = "isolated_exchange"
)

type Evidence string

const (
	EvidencePending                Evidence = "pending"
	EvidenceCaptureRun             Evidence = "capture_run"
	EvidenceExplicitSession        Evidence = "explicit_session"
	EvidenceExplicitActor          Evidence = "explicit_actor"
	EvidenceClientAssertedSubagent Evidence = "client_asserted_subagent"
	EvidenceAmbiguousActor         Evidence = "ambiguous_actor"
	EvidenceUndecodedExchange      Evidence = "undecoded_exchange"
	EvidenceExchangeBoundary       Evidence = "exchange_boundary"
)

// Ref is structural metadata, not a second runtime authority. A native Client
// Session projection is stable across Captures; Actor is retained only when
// the client protocol explicitly supplied it.
type Ref struct {
	ProjectionID string   `json:"projectionId"`
	DisplayName  string   `json:"displayName,omitempty"`
	Kind         Kind     `json:"kind"`
	Evidence     Evidence `json:"evidence"`
	Actor        string   `json:"actor,omitempty"`
}

// ValidateProjectionID validates an opaque projection identity accepted by
// control-plane filters. Callers must not parse the identifier: its shape is
// deliberately private to this package.
func ValidateProjectionID(value string) error {
	if !validText(value, false) {
		return errors.New("Agent Conversation projection identity is invalid")
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '_' || character == '-' || character == '.' ||
			character == ':' {
			continue
		}
		return errors.New("Agent Conversation projection identity is invalid")
	}
	return nil
}

func (ref Ref) Validate() error {
	if ValidateProjectionID(ref.ProjectionID) != nil ||
		!validText(ref.DisplayName, true) ||
		!validText(ref.Actor, true) {
		return errors.New("Agent Conversation reference contains an invalid identity")
	}
	switch ref.Kind {
	case KindPendingExchange:
		if ref.Evidence != EvidencePending || ref.Actor != "" {
			return errors.New("pending Agent Conversation evidence is invalid")
		}
	case KindMain:
		if (ref.Evidence != EvidenceCaptureRun &&
			ref.Evidence != EvidenceExplicitSession) || ref.Actor != "" {
			return errors.New("main Agent Conversation evidence is invalid")
		}
	case KindAgent:
		if ref.Evidence != EvidenceExplicitActor || ref.Actor == "" {
			return errors.New("explicit Agent Conversation evidence is invalid")
		}
	case KindIsolatedSubagent:
		if ref.Evidence != EvidenceClientAssertedSubagent || ref.Actor != "" {
			return errors.New("isolated subagent Conversation evidence is invalid")
		}
	case KindIsolatedExchange:
		if ref.Actor != "" || (ref.Evidence != EvidenceAmbiguousActor &&
			ref.Evidence != EvidenceUndecodedExchange &&
			ref.Evidence != EvidenceExchangeBoundary) {
			return errors.New("isolated Exchange Conversation evidence is invalid")
		}
	default:
		return errors.New("Agent Conversation kind is invalid")
	}
	return nil
}

// Pending keeps a live Exchange isolated until protocol decoding can prove a
// stable stream. This prevents a subagent request from appearing briefly in
// the main chat while it is in flight.
func Pending(exchangeID string) (Ref, error) {
	return checked(Ref{
		ProjectionID: exchangeProjectionID(exchangeID),
		Kind:         KindPendingExchange,
		Evidence:     EvidencePending,
	})
}

type ProjectionInput struct {
	CaptureRunID      string
	ExchangeID        string
	SourceDisplayName string
	Request           *protocolcore.Request
	Response          *protocolcore.Response
	ClientIdentity    *ClientIdentity
}

// Project creates one flat Conversation reference. A CaptureRun is necessary
// to group a main stream or an explicit actor across Exchanges. Manual and
// system captures remain Exchange-scoped even if their payload mentions an
// actor because no AgentSession authority exists for them.
func Project(input ProjectionInput) (Ref, error) {
	if !validText(input.ExchangeID, false) ||
		!validText(input.CaptureRunID, true) ||
		!validText(input.SourceDisplayName, true) {
		return Ref{}, errors.New("Agent Conversation projection input is invalid")
	}
	if input.CaptureRunID == "" {
		return checked(Ref{
			ProjectionID: exchangeProjectionID(input.ExchangeID),
			Kind:         KindIsolatedExchange,
			Evidence:     EvidenceExchangeBoundary,
		})
	}
	if input.ClientIdentity != nil {
		if err := input.ClientIdentity.Validate(); err != nil {
			return Ref{}, err
		}
		identity := *input.ClientIdentity
		scope := identityProjectionScope(identity)
		if identity.ActorID != "" {
			displayName := identity.ActorLabel
			if displayName == "" {
				displayName = actorLeaf(identity.ActorID)
			}
			return checked(Ref{
				ProjectionID: actorProjectionID(
					input.CaptureRunID,
					scope,
					identity.ActorID,
				),
				DisplayName: displayName,
				Kind:        KindAgent,
				Evidence:    EvidenceExplicitActor,
				Actor:       identity.ActorID,
			})
		}
		projectionID, evidence := mainProjectionID(
			input.CaptureRunID,
			scope,
		)
		return checked(Ref{
			ProjectionID: projectionID,
			DisplayName:  input.SourceDisplayName,
			Kind:         KindMain,
			Evidence:     evidence,
		})
	}
	if input.Request == nil {
		return checked(Ref{
			ProjectionID: exchangeProjectionID(input.ExchangeID),
			Kind:         KindIsolatedExchange,
			Evidence:     EvidenceUndecodedExchange,
		})
	}
	scope, _ := protocolProjectionScope(input.Request.ProtocolEvidence)
	if actor := protocolActor(input.Request.ProtocolEvidence); actor != "" {
		return checked(Ref{
			ProjectionID: actorProjectionID(
				input.CaptureRunID,
				scope,
				actor,
			),
			DisplayName: actorLeaf(actor),
			Kind:        KindAgent,
			Evidence:    EvidenceExplicitActor,
			Actor:       actor,
		})
	}
	actor, ambiguous := explicitCurrentActor(*input.Request, input.Response)
	if actor != "" && !ambiguous {
		return checked(Ref{
			ProjectionID: actorProjectionID(
				input.CaptureRunID,
				scope,
				actor,
			),
			DisplayName: actorLeaf(actor),
			Kind:        KindAgent,
			Evidence:    EvidenceExplicitActor,
			Actor:       actor,
		})
	}
	if hasClientAssertedSubagent(*input.Request) {
		return checked(Ref{
			ProjectionID: exchangeProjectionID(input.ExchangeID),
			Kind:         KindIsolatedSubagent,
			Evidence:     EvidenceClientAssertedSubagent,
		})
	}
	if ambiguous {
		return checked(Ref{
			ProjectionID: exchangeProjectionID(input.ExchangeID),
			Kind:         KindIsolatedExchange,
			Evidence:     EvidenceAmbiguousActor,
		})
	}
	projectionID, evidence := mainProjectionID(input.CaptureRunID, scope)
	return checked(Ref{
		ProjectionID: projectionID,
		DisplayName:  input.SourceDisplayName,
		Kind:         KindMain,
		Evidence:     evidence,
	})
}

type projectionScope struct {
	client    string
	sessionID string
	threadID  string
}

func protocolProjectionScope(
	values []protocolcore.ProtocolEvidenceValue,
) (projectionScope, bool) {
	if protocolcore.ValidateProtocolEvidence(values) != nil {
		return projectionScope{}, false
	}
	var scope projectionScope
	assign := func(target *string, value string) bool {
		if *target != "" && *target != value {
			return false
		}
		*target = value
		return true
	}
	for _, value := range values {
		valueClient := ""
		switch value.Name {
		case "claude.session_id":
			valueClient = "claude"
			if !assign(&scope.sessionID, value.Value) {
				return projectionScope{}, false
			}
		case "openai_responses.session_id", "codex.session_id":
			valueClient = "codex"
			if !assign(&scope.sessionID, value.Value) {
				return projectionScope{}, false
			}
		case "openai_responses.thread_id", "codex.thread_id":
			valueClient = "codex"
			if !assign(&scope.threadID, value.Value) {
				return projectionScope{}, false
			}
		default:
			continue
		}
		if scope.client != "" && scope.client != valueClient {
			return projectionScope{}, false
		}
		scope.client = valueClient
	}
	if scope.sessionID == "" {
		return projectionScope{}, false
	}
	return scope, true
}

func identityProjectionScope(identity ClientIdentity) projectionScope {
	scope := projectionScope{client: identity.Client, sessionID: identity.SessionID}
	for _, value := range identity.ProtocolIDs {
		if identity.Client != "codex" ||
			(value.Name != "openai_responses.thread_id" &&
				value.Name != "codex.thread_id") {
			continue
		}
		if scope.threadID == "" || scope.threadID == value.Value {
			scope.threadID = value.Value
		}
	}
	return scope
}

func mainProjectionID(
	captureRunID string,
	scope projectionScope,
) (string, Evidence) {
	if scope.sessionID == "" {
		return "capture_run:" + captureRunID + ":main", EvidenceCaptureRun
	}
	projectionID := sessionProjectionPrefix(scope.client, scope.sessionID)
	if scope.threadID != "" {
		projectionID += ":thread:" + projectionDigest(scope.threadID)
	}
	return projectionID + ":main", EvidenceExplicitSession
}

func actorProjectionID(
	captureRunID string,
	scope projectionScope,
	actorID string,
) string {
	prefix := "capture_run:" + captureRunID
	if scope.sessionID != "" {
		prefix = sessionProjectionPrefix(scope.client, scope.sessionID)
	}
	return prefix + ":agent:" + projectionDigest(actorID)
}

func sessionProjectionPrefix(client, sessionID string) string {
	return "client_session:" + client + ":" + projectionDigest(sessionID)
}

func projectionDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func protocolActor(values []protocolcore.ProtocolEvidenceValue) string {
	if protocolcore.ValidateProtocolEvidence(values) != nil {
		return ""
	}
	for _, value := range values {
		if value.Name == "claude.agent_id" {
			return value.Value
		}
	}
	return ""
}

func checked(ref Ref) (Ref, error) {
	if err := ref.Validate(); err != nil {
		return Ref{}, err
	}
	return ref, nil
}

func exchangeProjectionID(exchangeID string) string {
	return "exchange:" + exchangeID
}

func explicitCurrentActor(
	request protocolcore.Request,
	response *protocolcore.Response,
) (string, bool) {
	if response != nil {
		actors := make(map[string]struct{})
		for _, block := range response.Blocks {
			if actor := producer(block.Agent); actor != "" {
				actors[actor] = struct{}{}
			}
		}
		if len(actors) == 1 {
			for actor := range actors {
				return actor, false
			}
		}
		if len(actors) > 1 {
			return "", true
		}
	}
	for index := len(request.Messages) - 1; index >= 0; index-- {
		message := request.Messages[index]
		actors := make(map[string]struct{})
		if actor := producer(message.Agent); actor != "" {
			actors[actor] = struct{}{}
		}
		for _, block := range message.Blocks {
			if actor := producer(block.Agent); actor != "" {
				actors[actor] = struct{}{}
			}
		}
		if len(actors) == 1 {
			for actor := range actors {
				return actor, false
			}
		}
		if len(actors) > 1 {
			return "", true
		}
	}
	return "", false
}

func producer(context *protocolcore.AgentMessageContext) string {
	if context == nil {
		return ""
	}
	if context.AgentName != "" {
		return context.AgentName
	}
	return context.Author
}

func hasClientAssertedSubagent(request protocolcore.Request) bool {
	containsMarker := func(blocks []protocolcore.ContentBlock) bool {
		for _, block := range blocks {
			if block.Kind == protocolcore.BlockText &&
				strings.Contains(block.Text, "cc_is_subagent=true") {
				return true
			}
		}
		return false
	}
	if containsMarker(request.System) {
		return true
	}
	for _, message := range request.Messages {
		if message.Role == protocolcore.RoleSystem && containsMarker(message.Blocks) {
			return true
		}
	}
	return false
}

func actorLeaf(actor string) string {
	trimmed := strings.TrimRight(actor, "/")
	if separator := strings.LastIndex(trimmed, "/"); separator >= 0 &&
		separator+1 < len(trimmed) {
		return trimmed[separator+1:]
	}
	return actor
}

func validText(value string, allowEmpty bool) bool {
	if value == "" {
		return allowEmpty
	}
	if len(value) > maxIdentityBytes || !utf8.ValidString(value) ||
		strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character == '\uFEFF' || unicode.IsControl(character) {
			return false
		}
	}
	return true
}

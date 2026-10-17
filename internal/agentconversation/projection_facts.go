package agentconversation

import "errors"

// ProjectionFacts is a body-free summary. Its scalar strings borrow admitted
// values until the observer reserves and materializes an owned projection.
// It never retains protocol evidence slices, requests, responses, or body owners.
type ProjectionFacts struct {
	ref                      Ref
	captureRunID, exchangeID string
	scope                    projectionScope
	identity                 uint8
}

const (
	projectionExchange uint8 = iota
	projectionMain
	projectionActor
)

func (f ProjectionFacts) Materialize() (Ref, error) {
	if f.Empty() {
		return Ref{}, nil
	}
	ref := f.ref
	switch f.identity {
	case projectionMain:
		ref.ProjectionID, _ = mainProjectionID(f.captureRunID, f.scope)
	case projectionActor:
		ref.ProjectionID = actorProjectionID(f.captureRunID, f.scope, ref.Actor)
	default:
		ref.ProjectionID = exchangeProjectionID(f.exchangeID)
	}
	return checked(ref)
}
func (f ProjectionFacts) Evidence() Evidence { return f.ref.Evidence }
func (f ProjectionFacts) Empty() bool        { return f.ref.Kind == "" }
func PendingFacts(exchangeID string) (ProjectionFacts, error) {
	if !validText(exchangeID, false) {
		return ProjectionFacts{}, errors.New("Agent Conversation projection input is invalid")
	}
	return ProjectionFacts{exchangeID: exchangeID, ref: Ref{Kind: KindPendingExchange, Evidence: EvidencePending}}, nil
}
func PrepareProjection(input ProjectionInput) (ProjectionFacts, error) {
	if !validText(input.ExchangeID, false) || !validText(input.CaptureRunID, true) || !validText(input.SourceDisplayName, true) {
		return ProjectionFacts{}, errors.New("Agent Conversation projection input is invalid")
	}
	f := ProjectionFacts{captureRunID: input.CaptureRunID, exchangeID: input.ExchangeID}
	isolated := func(kind Kind, evidence Evidence) (ProjectionFacts, error) {
		f.ref = Ref{Kind: kind, Evidence: evidence}
		return f, nil
	}
	actor := func(id, label string) (ProjectionFacts, error) {
		f.identity = projectionActor
		f.ref = Ref{Kind: KindAgent, Evidence: EvidenceExplicitActor, Actor: id, DisplayName: label}
		return f, nil
	}
	main := func() (ProjectionFacts, error) {
		f.identity = projectionMain
		evidence := EvidenceCaptureRun
		if f.scope.sessionID != "" {
			evidence = EvidenceExplicitSession
		}
		f.ref = Ref{Kind: KindMain, Evidence: evidence, DisplayName: input.SourceDisplayName}
		return f, nil
	}
	if input.CaptureRunID == "" {
		return isolated(KindIsolatedExchange, EvidenceExchangeBoundary)
	}
	if input.ClientIdentity != nil {
		if err := input.ClientIdentity.Validate(); err != nil {
			return ProjectionFacts{}, err
		}
		identity := *input.ClientIdentity
		f.scope = identityProjectionScope(identity)
		if identity.ActorID != "" {
			label := identity.ActorLabel
			if label == "" {
				label = actorLeaf(identity.ActorID)
			}
			return actor(identity.ActorID, label)
		}
		return main()
	}
	if input.Request == nil {
		return isolated(KindIsolatedExchange, EvidenceUndecodedExchange)
	}
	f.scope, _ = protocolProjectionScope(input.Request.ProtocolEvidence)
	if id := protocolActor(input.Request.ProtocolEvidence); id != "" {
		return actor(id, actorLeaf(id))
	}
	if f.scope.client == "codex" && f.scope.sessionID != "" {
		return main()
	}
	id, ambiguous := explicitCurrentActor(*input.Request, input.Response)
	if id != "" && !ambiguous {
		return actor(id, actorLeaf(id))
	}
	if hasClientAssertedSubagent(*input.Request) {
		return isolated(KindIsolatedSubagent, EvidenceClientAssertedSubagent)
	}
	if ambiguous {
		return isolated(KindIsolatedExchange, EvidenceAmbiguousActor)
	}
	return main()
}

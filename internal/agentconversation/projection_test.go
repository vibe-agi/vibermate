package agentconversation_test

import (
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/agentconversation"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

func TestProjectionGroupsExactClientActorAcrossCaptures(t *testing.T) {
	t.Parallel()

	identity := agentconversation.ClientIdentity{
		Client: "claude", SessionID: "session-1", SessionResumable: true,
		ActorID: "ace66aeb17c2cf4d", ActorLabel: "code-review",
		ActorType: "general-purpose", ActorIsSubagent: true,
		ProviderResponseID: "msg-1", ProviderMessageID: "msg-1",
		Source: "client_local_state", Confidence: "exact",
		ObservedAt: time.Date(2026, 8, 13, 19, 0, 0, 0, time.UTC),
	}
	first, err := agentconversation.Project(agentconversation.ProjectionInput{
		CaptureRunID: "run-1", ExchangeID: "exchange-1",
		SourceDisplayName: "Claude", ClientIdentity: &identity,
	})
	if err != nil {
		t.Fatal(err)
	}
	identity.ProviderResponseID = "msg-2"
	identity.ProviderMessageID = "msg-2"
	second, err := agentconversation.Project(agentconversation.ProjectionInput{
		CaptureRunID: "run-2", ExchangeID: "exchange-2",
		SourceDisplayName: "Claude", ClientIdentity: &identity,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.ProjectionID != second.ProjectionID || first.Actor != identity.ActorID ||
		first.DisplayName != "code-review" || first.Kind != agentconversation.KindAgent {
		t.Fatalf("stable actor projections = %#v / %#v", first, second)
	}
}

func TestProjectionGroupsExactClaudeHeaderActorAcrossExchanges(t *testing.T) {
	t.Parallel()

	request := protocolcore.Request{ProtocolEvidence: []protocolcore.ProtocolEvidenceValue{
		{Name: "claude.agent_id", Value: "a5ef98e49c0e228c9"},
		{Name: "claude.parent_agent_id", Value: "aaac343a3a31d4ccf"},
		{Name: "claude.session_id", Value: "64fe284e-4565-4065-961d-3db7351ff152"},
	}}
	first := projectExchange(t, "run-1", "exchange-1", request, nil)
	second := projectExchange(t, "run-1", "exchange-2", request, nil)
	if first.ProjectionID != second.ProjectionID ||
		first.Actor != "a5ef98e49c0e228c9" ||
		first.Kind != agentconversation.KindAgent {
		t.Fatalf("header actor projections = %#v / %#v", first, second)
	}
}

func TestProjectionGroupsCodexTurnsByExactSessionAndThread(t *testing.T) {
	t.Parallel()

	request := func(sessionID, threadID, turnID string) protocolcore.Request {
		return protocolcore.Request{ProtocolEvidence: []protocolcore.ProtocolEvidenceValue{
			{Name: "openai_responses.session_id", Value: sessionID},
			{Name: "openai_responses.thread_id", Value: threadID},
			{Name: "openai_responses.turn_id", Value: turnID},
		}}
	}
	first := projectExchange(
		t,
		"run-1",
		"exchange-1",
		request("session-1", "thread-1", "turn-1"),
		nil,
	)
	resumed := projectExchange(
		t,
		"run-1",
		"exchange-2",
		request("session-1", "thread-1", "turn-2"),
		nil,
	)
	otherSession := projectExchange(
		t,
		"run-1",
		"exchange-3",
		request("session-2", "thread-2", "turn-1"),
		nil,
	)
	if first.Kind != agentconversation.KindMain ||
		first.ProjectionID != resumed.ProjectionID ||
		first.ProjectionID == otherSession.ProjectionID {
		t.Fatalf(
			"Codex Session projections = first=%#v resumed=%#v other=%#v",
			first,
			resumed,
			otherSession,
		)
	}
}

func TestNativeResumeKeepsClientSessionConversationAcrossCaptures(t *testing.T) {
	t.Parallel()

	request := protocolcore.Request{ProtocolEvidence: []protocolcore.ProtocolEvidenceValue{
		{Name: "openai_responses.session_id", Value: "01a02deb-d420-79e2-b0bc-1a9cbdaa643f"},
		{Name: "openai_responses.thread_id", Value: "thread-main"},
	}}
	first := projectExchange(t, "run-before-exit", "exchange-1", request, nil)
	resumed := projectExchange(t, "run-after-resume", "exchange-2", request, nil)

	if first.ProjectionID != resumed.ProjectionID ||
		first.Evidence != agentconversation.EvidenceExplicitSession ||
		resumed.Evidence != agentconversation.EvidenceExplicitSession {
		t.Fatalf(
			"native resume projections = first=%#v resumed=%#v",
			first,
			resumed,
		)
	}
}

func TestClientSessionIdentityIncludesTheNativeClient(t *testing.T) {
	t.Parallel()

	codex := projectExchange(
		t,
		"run-codex",
		"exchange-codex",
		protocolcore.Request{ProtocolEvidence: []protocolcore.ProtocolEvidenceValue{
			{Name: "openai_responses.session_id", Value: "same-opaque-session-id"},
		}},
		nil,
	)
	claude := projectExchange(
		t,
		"run-claude",
		"exchange-claude",
		protocolcore.Request{ProtocolEvidence: []protocolcore.ProtocolEvidenceValue{
			{Name: "claude.session_id", Value: "same-opaque-session-id"},
		}},
		nil,
	)

	if codex.ProjectionID == claude.ProjectionID {
		t.Fatalf("different native clients shared Client Session projection %q", codex.ProjectionID)
	}
}

func TestClientIdentityFromProtocolEvidenceRetainsNativeHierarchy(t *testing.T) {
	t.Parallel()

	observedAt := time.Date(2026, 8, 14, 11, 31, 5, 123456789, time.UTC)
	identity, found := agentconversation.ClientIdentityFromProtocolEvidence(
		[]protocolcore.ProtocolEvidenceValue{
			{Name: "claude.agent_id", Value: "a5ef98e49c0e228c9"},
			{Name: "claude.parent_agent_id", Value: "aaac343a3a31d4ccf"},
			{Name: "claude.session_id", Value: "64fe284e-4565-4065-961d-3db7351ff152"},
		},
		"",
		observedAt,
	)
	if !found {
		t.Fatal("exact client protocol identity was not derived")
	}
	if identity.Source != agentconversation.ClientIdentitySourceProtocolEvidence ||
		identity.SessionID != "64fe284e-4565-4065-961d-3db7351ff152" ||
		identity.ActorID != "a5ef98e49c0e228c9" ||
		!identity.ActorIsSubagent || identity.ProviderResponseID != "" ||
		!identity.ObservedAt.Equal(observedAt.Truncate(time.Millisecond)) {
		t.Fatalf("protocol identity = %#v", identity)
	}
}

func TestClientIdentityFromProtocolEvidenceRetainsCodexSession(t *testing.T) {
	t.Parallel()

	observedAt := time.Date(2026, 8, 23, 15, 12, 38, 0, time.UTC)
	identity, found := agentconversation.ClientIdentityFromProtocolEvidence(
		[]protocolcore.ProtocolEvidenceValue{
			{Name: "openai_responses.session_id", Value: "session-1"},
			{Name: "openai_responses.thread_id", Value: "thread-1"},
			{Name: "openai_responses.turn_id", Value: "turn-2"},
		},
		"response-2",
		observedAt,
	)
	if !found {
		t.Fatal("exact Codex protocol identity was not derived")
	}
	if identity.Client != "codex" || identity.SessionID != "session-1" ||
		identity.ActorID != "" || identity.ActorIsSubagent ||
		identity.ProviderResponseID != "response-2" ||
		identity.Source != agentconversation.ClientIdentitySourceProtocolEvidence ||
		len(identity.ProtocolIDs) != 3 {
		t.Fatalf("Codex protocol identity = %#v", identity)
	}
}

func TestClientIdentityRejectsByteOrderMark(t *testing.T) {
	t.Parallel()

	identity := agentconversation.ClientIdentity{
		Client:             "claude",
		SessionID:          "session\uFEFFid",
		SessionResumable:   true,
		Source:             agentconversation.ClientIdentitySourceProtocolEvidence,
		Confidence:         "exact",
		ObservedAt:         time.Date(2026, 8, 15, 1, 2, 3, 0, time.UTC),
		ProviderResponseID: "response-1",
	}
	if err := identity.Validate(); err == nil {
		t.Fatal("Validate() succeeded, want byte-order-mark failure")
	}
}

func TestMergeClientIdentityDeepensWireEvidenceWithoutChangingAssociation(t *testing.T) {
	t.Parallel()

	observedAt := time.Date(2026, 8, 14, 11, 31, 5, 0, time.UTC)
	wire, found := agentconversation.ClientIdentityFromProtocolEvidence(
		[]protocolcore.ProtocolEvidenceValue{
			{Name: "claude.agent_id", Value: "agent-review"},
			{Name: "claude.parent_agent_id", Value: "agent-main"},
			{Name: "claude.session_id", Value: "session-resumable"},
		},
		"",
		observedAt,
	)
	if !found {
		t.Fatal("wire identity was not derived")
	}
	local := agentconversation.ClientIdentity{
		Client: "claude", SessionID: "session-resumable", SessionResumable: true,
		ActorID: "agent-review", ActorLabel: "Angle A line-by-line scan",
		ActorType: "general-purpose", ActorIsSubagent: true,
		ProviderResponseID: "msg-provider", ProviderMessageID: "msg-provider",
		Source:     agentconversation.ClientIdentitySourceLocalState,
		Confidence: "exact", ObservedAt: observedAt.Add(time.Second),
		ProtocolIDs: []agentconversation.ClientEvidenceValue{
			{Name: "claude.agent_id", Value: "agent-review"},
			{Name: "claude.request_id", Value: "request-1"},
			{Name: "claude.session_id", Value: "session-resumable"},
		},
		Attributes: []agentconversation.ClientEvidenceValue{
			{Name: "claude.description", Value: "Angle A line-by-line scan"},
		},
	}
	merged, changed, err := agentconversation.MergeClientIdentity(wire, local)
	if err != nil || !changed {
		t.Fatalf("MergeClientIdentity() = %#v, %v, %v", merged, changed, err)
	}
	if merged.Source != agentconversation.ClientIdentitySourceLocalState ||
		merged.ActorLabel != local.ActorLabel ||
		merged.ProviderResponseID != "msg-provider" ||
		!merged.ObservedAt.Equal(observedAt) ||
		len(merged.ProtocolIDs) != 4 {
		t.Fatalf("merged identity = %#v", merged)
	}
	// A later weak observation cannot erase the local label or native IDs.
	again, changed, err := agentconversation.MergeClientIdentity(merged, wire)
	if err != nil || changed || !again.Equal(merged) {
		t.Fatalf("wire downgrade = %#v, %v, %v", again, changed, err)
	}
}

func TestMergeClientIdentityDeepensUnknownWireParentFromLocalState(t *testing.T) {
	t.Parallel()

	observedAt := time.Date(2026, 8, 14, 11, 31, 5, 0, time.UTC)
	wire, found := agentconversation.ClientIdentityFromProtocolEvidence(
		[]protocolcore.ProtocolEvidenceValue{
			{Name: "claude.agent_id", Value: "agent-review"},
			{Name: "claude.session_id", Value: "session-resumable"},
		},
		"msg-provider",
		observedAt,
	)
	if !found || wire.ActorIsSubagent {
		t.Fatalf("wire identity = %#v, %v", wire, found)
	}
	local := wire.Clone()
	local.ActorLabel = "Angle A line-by-line scan"
	local.ActorType = "general-purpose"
	local.ActorIsSubagent = true
	local.Source = agentconversation.ClientIdentitySourceLocalState
	local.Attributes = []agentconversation.ClientEvidenceValue{
		{Name: "claude.description", Value: local.ActorLabel},
	}

	merged, changed, err := agentconversation.MergeClientIdentity(wire, local)
	if err != nil || !changed || !merged.ActorIsSubagent ||
		merged.ActorLabel != local.ActorLabel ||
		merged.Source != agentconversation.ClientIdentitySourceLocalState {
		t.Fatalf("MergeClientIdentity() = %#v, %v, %v", merged, changed, err)
	}
	// Reindexing may observe the same incomplete headers again. They cannot
	// downgrade the client-local hierarchy that was already joined exactly.
	again, changed, err := agentconversation.MergeClientIdentity(merged, wire)
	if err != nil || changed || !again.Equal(merged) {
		t.Fatalf("wire downgrade = %#v, %v, %v", again, changed, err)
	}
}

func TestMergeClientIdentityDeepensUnknownWireParentFromLaterHeader(t *testing.T) {
	t.Parallel()

	observedAt := time.Date(2026, 8, 14, 11, 31, 5, 0, time.UTC)
	identity := func(parent string, at time.Time) agentconversation.ClientIdentity {
		evidence := []protocolcore.ProtocolEvidenceValue{
			{Name: "claude.agent_id", Value: "agent-review"},
		}
		if parent != "" {
			evidence = append(evidence, protocolcore.ProtocolEvidenceValue{
				Name: "claude.parent_agent_id", Value: parent,
			})
		}
		evidence = append(evidence, protocolcore.ProtocolEvidenceValue{
			Name: "claude.session_id", Value: "session-resumable",
		})
		candidate, found := agentconversation.ClientIdentityFromProtocolEvidence(
			evidence,
			"msg-provider",
			at,
		)
		if !found {
			t.Fatal("wire identity was not derived")
		}
		return candidate
	}

	sparse := identity("", observedAt)
	withParent := identity("agent-main", observedAt.Add(time.Second))
	merged, changed, err := agentconversation.MergeClientIdentity(sparse, withParent)
	if err != nil || !changed || !merged.ActorIsSubagent {
		t.Fatalf("MergeClientIdentity() = %#v, %v, %v", merged, changed, err)
	}
	again, changed, err := agentconversation.MergeClientIdentity(merged, sparse)
	if err != nil || changed || !again.Equal(merged) {
		t.Fatalf("sparse wire downgrade = %#v, %v, %v", again, changed, err)
	}
}

func TestMergeClientIdentityRejectsChangedExactParent(t *testing.T) {
	t.Parallel()

	observedAt := time.Date(2026, 8, 14, 11, 31, 5, 0, time.UTC)
	identity := func(parent string) agentconversation.ClientIdentity {
		candidate, found := agentconversation.ClientIdentityFromProtocolEvidence(
			[]protocolcore.ProtocolEvidenceValue{
				{Name: "claude.agent_id", Value: "agent-review"},
				{Name: "claude.parent_agent_id", Value: parent},
				{Name: "claude.session_id", Value: "session-resumable"},
			},
			"",
			observedAt,
		)
		if !found {
			t.Fatal("wire identity was not derived")
		}
		return candidate
	}
	if _, _, err := agentconversation.MergeClientIdentity(
		identity("agent-main-one"),
		identity("agent-main-two"),
	); err == nil {
		t.Fatal("changed exact parent identity was accepted")
	}
}

func TestProjectionKeepsManagedMainTrafficTogether(t *testing.T) {
	t.Parallel()

	ref := project(t, "run-1", request(t, nil, "hello"), nil)
	if ref.Kind != agentconversation.KindMain ||
		ref.ProjectionID != "capture_run:run-1:main" || ref.DisplayName != "Claude" {
		t.Fatalf("main projection = %#v", ref)
	}
}

func TestProjectionUsesExplicitCodexActorWithoutBuildingATree(t *testing.T) {
	t.Parallel()

	context := &protocolcore.AgentMessageContext{
		AgentName: "/root/reviewer",
		Author:    "/root/reviewer",
		Recipient: "/root",
	}
	ref := project(t, "run-1", request(t, context, "reviewed"), nil)
	if ref.Kind != agentconversation.KindAgent ||
		ref.Evidence != agentconversation.EvidenceExplicitActor ||
		ref.Actor != "/root/reviewer" || ref.DisplayName != "reviewer" {
		t.Fatalf("actor projection = %#v", ref)
	}
	if ref.ProjectionID == "" || ref.ProjectionID == ref.Actor {
		t.Fatalf("actor projection ID is not opaque and stable: %#v", ref)
	}
}

func TestProjectionNeverMergesUnnamedClaudeSubagents(t *testing.T) {
	t.Parallel()

	request := request(t, nil, "hello")
	system, err := protocolcore.NewTextBlock("metadata cc_is_subagent=true")
	if err != nil {
		t.Fatal(err)
	}
	request.System = []protocolcore.ContentBlock{system}
	first := projectExchange(t, "run-1", "exchange-1", request, nil)
	second := projectExchange(t, "run-1", "exchange-2", request, nil)
	if first.Kind != agentconversation.KindIsolatedSubagent ||
		second.Kind != agentconversation.KindIsolatedSubagent ||
		first.ProjectionID == second.ProjectionID {
		t.Fatalf("subagent projections = %#v / %#v", first, second)
	}
}

func TestProjectionIsolatesAmbiguousAgentOutput(t *testing.T) {
	t.Parallel()

	first, _ := protocolcore.NewTextBlock("first")
	first.Agent = &protocolcore.AgentMessageContext{AgentName: "/root"}
	second, _ := protocolcore.NewTextBlock("second")
	second.Agent = &protocolcore.AgentMessageContext{AgentName: "/root/reviewer"}
	response := &protocolcore.Response{Blocks: []protocolcore.ContentBlock{first, second}}
	ref := project(t, "run-1", request(t, nil, "hello"), response)
	if ref.Kind != agentconversation.KindIsolatedExchange ||
		ref.Evidence != agentconversation.EvidenceAmbiguousActor {
		t.Fatalf("ambiguous projection = %#v", ref)
	}
}

func TestProjectionKeepsManualCaptureExchangeScoped(t *testing.T) {
	t.Parallel()

	context := &protocolcore.AgentMessageContext{AgentName: "/root"}
	ref := project(t, "", request(t, context, "hello"), nil)
	if ref.Kind != agentconversation.KindIsolatedExchange ||
		ref.Evidence != agentconversation.EvidenceExchangeBoundary ||
		ref.ProjectionID != "exchange:exchange-1" {
		t.Fatalf("manual projection = %#v", ref)
	}
}

func TestPendingExchangeIsAlwaysIsolated(t *testing.T) {
	t.Parallel()

	ref, err := agentconversation.Pending("exchange-1")
	if err != nil {
		t.Fatal(err)
	}
	if ref.Kind != agentconversation.KindPendingExchange ||
		ref.ProjectionID != "exchange:exchange-1" {
		t.Fatalf("pending projection = %#v", ref)
	}
}

func project(
	t *testing.T,
	captureRunID string,
	request protocolcore.Request,
	response *protocolcore.Response,
) agentconversation.Ref {
	t.Helper()
	return projectExchange(t, captureRunID, "exchange-1", request, response)
}

func projectExchange(
	t *testing.T,
	captureRunID string,
	exchangeID string,
	request protocolcore.Request,
	response *protocolcore.Response,
) agentconversation.Ref {
	t.Helper()
	ref, err := agentconversation.Project(agentconversation.ProjectionInput{
		CaptureRunID:      captureRunID,
		ExchangeID:        exchangeID,
		SourceDisplayName: "Claude",
		Request:           &request,
		Response:          response,
	})
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func request(
	t *testing.T,
	agent *protocolcore.AgentMessageContext,
	text string,
) protocolcore.Request {
	t.Helper()
	block, err := protocolcore.NewTextBlock(text)
	if err != nil {
		t.Fatal(err)
	}
	return protocolcore.Request{
		Messages: []protocolcore.Message{{
			Role: protocolcore.RoleUser, Blocks: []protocolcore.ContentBlock{block}, Agent: agent,
		}},
	}
}

package desktopcontrol

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/agentconversation"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

func TestExchangeDetailProjectsOrderedRedactedEvidence(t *testing.T) {
	t.Parallel()

	newAttempt := func(id string, upstreamID string) egressaudit.Attempt {
		t.Helper()
		attempt, err := egressaudit.New(egressaudit.NewInput{
			ID:           id,
			ConnectionID: "connection-detail",
			Purpose:      egressaudit.PurposeProviderAttempt,
			PayloadClass: egressaudit.PayloadClientSemantic,
			Parent: egressaudit.ParentRef{
				Kind:       egressaudit.ParentUpstreamAttempt,
				ID:         upstreamID,
				ExchangeID: "exchange-detail",
			},
			Caller:       egressaudit.CallerCore,
			TargetOrigin: "https://provider.example:443",
			Decision: egressaudit.DecisionRef{
				PolicyID:       "policy-detail",
				PolicyRevision: 1,
				Authority:      egressaudit.AuthorityEnvironment,
				RuleID:         "rule-detail",
				ProxyID:        "company-proxy",
			},
			StartedAt: time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC),
		})
		if err != nil {
			t.Fatal(err)
		}
		return attempt
	}
	record := activity.Record{
		Sequence:               7,
		ID:                     "activity-detail",
		OccurredAt:             time.Date(2026, 8, 3, 12, 0, 2, 0, time.UTC),
		Kind:                   activity.KindExchangeCompleted,
		EnvironmentID:          "environment-detail",
		EnvironmentRevision:    4,
		EnvironmentDigest:      "4141414141414141414141414141414141414141414141414141414141414141",
		ClientEndpointID:       "endpoint-detail",
		ClientEndpointRevision: 2,
		ProtocolPlanID:         "protocol-detail",
		ProtocolPlanRevision:   3,
		RouteID:                "route-detail",
		RouteRevision:          5,
		SubjectID:              "exchange-detail",
		Status:                 activity.StatusFailed,
		ReasonCode:             "provider_transport_failed",
		SourceKind:             activity.SourceSystemProxy,
		SourceDisplayName:      "ViberMate runtime",
		SourceRecognition:      activity.SourceRecognitionUnknown,
		ConnectionID:           "connection-detail",
		Diagnosis: &activity.Diagnosis{
			ClientField: "messages",
			ClientPath:  "$.messages[2].content[0].type",
		},
	}
	setConversationRef(&record)
	detail, err := exchangeDetailOf(record, egressaudit.Page{
		Items: []egressaudit.Record{
			{Sequence: 12, Attempt: newAttempt("egress-2", "attempt-2")},
			{Sequence: 11, Attempt: newAttempt("egress-1", "attempt-1")},
			{Sequence: 10, Attempt: newAttempt("egress-0", "attempt-1")},
		},
	}, nil, ExchangeContentViewIncremental)
	if err != nil {
		t.Fatal(err)
	}
	if detail.ID != record.SubjectID ||
		detail.Environment.ID != record.EnvironmentID ||
		detail.Environment.Revision != record.EnvironmentRevision ||
		detail.Environment.RouteID != record.RouteID ||
		detail.Status != string(record.Status) ||
		detail.ProcessingTrace.Result != record.ReasonCode ||
		detail.ProcessingTrace.EgressProxyID != "company-proxy" ||
		len(detail.ProcessingTrace.PluginRunIDs) != 0 ||
		len(detail.ProcessingTrace.Attempts) != 3 ||
		detail.ProcessingTrace.Attempts[0].ID != "egress-0" ||
		detail.ProcessingTrace.Attempts[0].Parent.ID != "attempt-1" ||
		detail.ProcessingTrace.Attempts[1].ID != "egress-1" ||
		detail.ProcessingTrace.Attempts[2].ID != "egress-2" ||
		detail.ProcessingTrace.Attempts[2].TargetOrigin != "https://provider.example:443" ||
		detail.Diagnosis == nil || detail.Diagnosis.ClientField != "messages" ||
		detail.Diagnosis.ClientPath != "$.messages[2].content[0].type" {
		t.Fatalf("Exchange detail = %+v", detail)
	}
	wire, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), `"recordedAt"`) ||
		strings.Contains(string(wire), `"expiresAt"`) ||
		!strings.Contains(string(wire), `"content":{"state":"not_recorded"}`) {
		t.Fatalf("not-recorded wire = %s", wire)
	}
	if _, err := exchangeDetailOf(record, egressaudit.Page{
		NextCursor: "more-evidence",
	}, nil, ExchangeContentViewIncremental); err == nil {
		t.Fatal("a truncated Exchange detail was accepted")
	}
}

func TestActivitySummaryPreservesTheStableFailureReason(t *testing.T) {
	t.Parallel()
	record := activity.Record{
		Sequence: 1, ID: "activity-expired", OccurredAt: time.Date(2026, 8, 9, 9, 0, 0, 0, time.UTC),
		Kind:          activity.KindExchangeCompleted,
		EnvironmentID: "work", EnvironmentRevision: 4,
		EnvironmentDigest: strings.Repeat("a", 64),
		ClientEndpointID:  "endpoint.claude", ClientEndpointRevision: 2,
		ProtocolPlanID: "plan.claude", ProtocolPlanRevision: 3,
		RouteID: "route.claude", RouteRevision: 5,
		SubjectID: "exchange-expired", Status: activity.StatusFailed,
		ReasonCode: "tool_decision_expired",
		SourceKind: activity.SourceCaptureRun, SourceDisplayName: "claude",
		SourceRecognition: activity.SourceRecognitionVerified, CaptureRunID: "run-expired",
		ConnectionID: "connection-expired",
	}
	setConversationRef(&record)
	page, err := activityPageOf(activity.Page{Items: []activity.Record{record}})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ReasonCode != record.ReasonCode {
		t.Fatalf("Activity page = %+v", page)
	}
}

func TestPendingExchangeIsVisibleBeforeItsResponseArrives(t *testing.T) {
	t.Parallel()
	record := activity.Record{
		Sequence: 1, ID: "activity-pending", OccurredAt: time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC),
		Kind:          activity.KindExchangeStarted,
		EnvironmentID: "work", EnvironmentRevision: 4,
		EnvironmentDigest: strings.Repeat("a", 64),
		ClientEndpointID:  "endpoint.claude", ClientEndpointRevision: 2,
		ProtocolPlanID: "plan.claude", ProtocolPlanRevision: 3,
		RouteID: "route.claude", RouteRevision: 5,
		SubjectID: "exchange-pending", Status: activity.StatusPending,
		SourceKind: activity.SourceCaptureRun, SourceDisplayName: "claude",
		SourceRecognition: activity.SourceRecognitionVerified,
		CaptureRunID:      "run-pending", ConnectionID: "connection-pending",
	}
	setConversationRef(&record)
	content := exchangeContentFixture(t, record)
	content.Response = nil

	page, err := activityPageOf(activity.Page{Items: []activity.Record{record}})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Status != string(activity.StatusPending) {
		t.Fatalf("pending Activity page = %+v", page)
	}

	detail, err := exchangeDetailOf(record, egressaudit.Page{}, &content, ExchangeContentViewIncremental)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Status != string(activity.StatusPending) ||
		detail.ProcessingTrace.Result != string(activity.StatusPending) ||
		detail.Content.Request == nil || detail.Content.Response != nil {
		t.Fatalf("pending Exchange detail = %+v", detail)
	}
}

func TestExchangeDetailJoinsOnlyMatchingFrozenConversationEvidence(t *testing.T) {
	t.Parallel()

	record := activity.Record{
		Sequence: 7, ID: "activity-content",
		OccurredAt:    time.Date(2026, 8, 8, 2, 0, 0, 0, time.UTC),
		Kind:          activity.KindExchangeCompleted,
		EnvironmentID: "work", EnvironmentRevision: 4,
		EnvironmentDigest: strings.Repeat("a", 64),
		ClientEndpointID:  "endpoint.claude", ClientEndpointRevision: 2,
		ProtocolPlanID: "plan.claude", ProtocolPlanRevision: 3,
		RouteID: "route.claude", RouteRevision: 5,
		SubjectID: "exchange-content", Status: activity.StatusSucceeded,
		SourceKind: activity.SourceSystemProxy, SourceDisplayName: "system proxy",
		SourceRecognition: activity.SourceRecognitionUnknown,
	}
	setConversationRef(&record)
	content := exchangeContentFixture(t, record)
	detail, err := exchangeDetailOf(
		record, egressaudit.Page{}, &content, ExchangeContentViewIncremental,
	)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Content.State != ExchangeContentRecorded ||
		detail.Content.Mode != string(environment.ContentRecordingFull) ||
		detail.Content.RecordedAt == nil || detail.Content.ExpiresAt == nil ||
		detail.Content.Request == nil ||
		detail.Content.RequestProjection == nil ||
		detail.Content.RequestProjection.View != ExchangeContentViewIncremental ||
		detail.Content.RequestProjection.Relationship != exchangecontent.RequestPresentationCheckpoint ||
		detail.Content.RequestProjection.TotalMessageCount != 1 ||
		detail.Content.RequestProjection.InheritedMessageCount != 0 ||
		detail.Content.RequestProjection.FullSnapshotAvailable ||
		detail.Content.Request.Messages[0].Blocks[0].Text != "inspect this" ||
		detail.Content.Response == nil ||
		detail.Content.Response.Blocks[0].ToolName != "read_file" ||
		detail.Content.Response.Usage.Output.Tokens != 3 {
		t.Fatalf("Exchange content detail = %+v", detail.Content)
	}

	tampered := content.Clone()
	tampered.Frozen.RouteRevision++
	if _, err := exchangeDetailOf(
		record, egressaudit.Page{}, &tampered, ExchangeContentViewIncremental,
	); err == nil {
		t.Fatal("content from a different frozen Route revision was joined")
	}
}

func TestExchangeDetailDefaultsToIncrementalMessagesAndCanReturnFullSnapshot(t *testing.T) {
	t.Parallel()
	record := activity.Record{
		Sequence: 8, ID: "activity-incremental",
		OccurredAt:    time.Date(2026, 8, 9, 4, 0, 0, 0, time.UTC),
		Kind:          activity.KindExchangeCompleted,
		EnvironmentID: "work", EnvironmentRevision: 4,
		EnvironmentDigest: strings.Repeat("a", 64),
		ClientEndpointID:  "endpoint.claude", ClientEndpointRevision: 2,
		ProtocolPlanID: "plan.claude", ProtocolPlanRevision: 3,
		RouteID: "route.claude", RouteRevision: 5,
		SubjectID: "exchange-incremental", Status: activity.StatusSucceeded,
		SourceKind: activity.SourceCaptureRun, SourceDisplayName: "claude",
		SourceRecognition: activity.SourceRecognitionVerified,
		CaptureRunID:      "run-incremental", ConnectionID: "connection-incremental",
	}
	setConversationRef(&record)
	content := exchangeContentFixture(t, record)
	inherited := content.Request.Messages[0]
	content.Request.Messages = append(
		[]exchangecontent.Message{inherited, inherited}, content.Request.Messages...,
	)
	content.Presentation = exchangecontent.RequestPresentation{
		Mode:                  exchangecontent.RequestPresentationIncremental,
		InheritedMessageCount: 2,
	}

	incremental, err := exchangeDetailOf(
		record, egressaudit.Page{}, &content, ExchangeContentViewIncremental,
	)
	if err != nil {
		t.Fatal(err)
	}
	if incremental.Content.Request == nil ||
		len(incremental.Content.Request.Messages) != 1 ||
		incremental.Content.RequestProjection == nil ||
		incremental.Content.RequestProjection.View != ExchangeContentViewIncremental ||
		incremental.Content.RequestProjection.Relationship != exchangecontent.RequestPresentationIncremental ||
		incremental.Content.RequestProjection.InheritedMessageCount != 2 ||
		incremental.Content.RequestProjection.TotalMessageCount != 3 ||
		!incremental.Content.RequestProjection.FullSnapshotAvailable {
		t.Fatalf("incremental detail = %+v", incremental.Content)
	}

	full, err := exchangeDetailOf(
		record, egressaudit.Page{}, &content, ExchangeContentViewFull,
	)
	if err != nil {
		t.Fatal(err)
	}
	if full.Content.Request == nil || len(full.Content.Request.Messages) != 3 ||
		full.Content.RequestProjection == nil ||
		full.Content.RequestProjection.View != ExchangeContentViewFull ||
		full.Content.RequestProjection.InheritedMessageCount != 2 ||
		full.Content.RequestProjection.TotalMessageCount != 3 {
		t.Fatalf("full detail = %+v", full.Content)
	}
}

func TestExchangeDetailProjectsOnlyProtocolProvenAgentRelationships(t *testing.T) {
	t.Parallel()
	record := activity.Record{
		Sequence: 9, ID: "activity-agent-conversation",
		OccurredAt:    time.Date(2026, 8, 12, 4, 0, 0, 0, time.UTC),
		Kind:          activity.KindExchangeCompleted,
		EnvironmentID: "work", EnvironmentRevision: 4,
		EnvironmentDigest: strings.Repeat("a", 64),
		ClientEndpointID:  "endpoint.codex", ClientEndpointRevision: 2,
		ProtocolPlanID: "plan.codex", ProtocolPlanRevision: 3,
		RouteID: "route.codex", RouteRevision: 5,
		SubjectID: "exchange-agent-conversation", Status: activity.StatusSucceeded,
		SourceKind: activity.SourceCaptureRun, SourceDisplayName: "codex",
		SourceRecognition: activity.SourceRecognitionVerified,
		CaptureRunID:      "run-agent-conversation", ConnectionID: "connection-agent-conversation",
	}
	setConversationRef(&record)
	content := exchangeContentFixture(t, record)
	content.Request.Messages = append(content.Request.Messages, exchangecontent.Message{
		Role: "assistant",
		Blocks: []exchangecontent.Block{
			{
				Kind: "tool_call", Availability: exchangecontent.AvailabilityRecorded,
				OriginalSize: len(`{"description":"inspect tests"}`),
				CallID:       "claude-agent-call", ToolName: "Agent",
				Arguments: json.RawMessage(`{"description":"inspect tests"}`),
			},
		},
	})
	content.Response.Blocks = append(content.Response.Blocks,
		exchangecontent.Block{
			Kind: "text", Availability: exchangecontent.AvailabilityRecorded,
			Text: "Review this boundary.", OriginalSize: len("Review this boundary."),
			Agent: &exchangecontent.AgentContext{
				AgentName: "root", Author: "root", Recipient: "reviewer",
			},
		},
		exchangecontent.Block{
			Kind: "tool_call", Availability: exchangecontent.AvailabilityRecorded,
			OriginalSize: len(`{"task_name":"reviewer"}`),
			CallID:       "agent-call-1", ToolNamespace: "multi_agent", ToolName: "spawn_agent",
			Arguments: json.RawMessage(`{"task_name":"reviewer"}`),
			Agent:     &exchangecontent.AgentContext{AgentName: "root"},
		},
		exchangecontent.Block{
			Kind: "tool_result", Availability: exchangecontent.AvailabilityRecorded,
			Text: "reviewer started", OriginalSize: len("reviewer started"),
			CallID: "agent-call-1", ToolNamespace: "multi_agent", ToolName: "spawn_agent",
			Agent: &exchangecontent.AgentContext{AgentName: "reviewer"},
		},
	)
	if err := content.Validate(); err != nil {
		t.Fatalf("agent content fixture is invalid: %v", err)
	}

	detail, err := exchangeDetailOf(record, egressaudit.Page{}, &content, ExchangeContentViewIncremental)
	if err != nil {
		t.Fatal(err)
	}
	projection := detail.Content.AgentConversation
	if projection == nil || projection.Scope != "capture_run" || len(projection.Agents) != 2 ||
		projection.Agents[0].Name != "root" || projection.Agents[1].Name != "reviewer" ||
		len(projection.Relationships) != 1 ||
		projection.Relationships[0].Source != "root" || projection.Relationships[0].Target != "reviewer" ||
		len(projection.Actions) != 2 {
		t.Fatalf("agent conversation projection = %+v", projection)
	}
	actionsByCallID := make(map[string]AgentConversationAction, len(projection.Actions))
	for _, action := range projection.Actions {
		actionsByCallID[action.CallID] = action
	}
	spawn := actionsByCallID["agent-call-1"]
	if spawn.CallID != "agent-call-1" || spawn.Name != "spawn_agent" ||
		spawn.Status != "completed" || spawn.SourceAgent != "root" ||
		spawn.ResultAgent != "reviewer" || !spawn.Attributed {
		t.Fatalf("spawn projection = %+v", spawn)
	}
	subtask := actionsByCallID["claude-agent-call"]
	if subtask.CallID != "claude-agent-call" || subtask.Name != "subtask" ||
		subtask.Status != "requested" || subtask.Attributed ||
		subtask.SourceAgent != "" || subtask.ResultAgent != "" {
		t.Fatalf("unattributed subtask projection = %+v", subtask)
	}
}

func TestExchangeContentViewQueryIsClosed(t *testing.T) {
	t.Parallel()
	for rawQuery, want := range map[string]ExchangeContentView{
		"":                        ExchangeContentViewIncremental,
		"contentView=incremental": ExchangeContentViewIncremental,
		"contentView=full":        ExchangeContentViewFull,
	} {
		got, err := parseExchangeContentView(rawQuery)
		if err != nil || got != want {
			t.Fatalf("parseExchangeContentView(%q) = %q, %v", rawQuery, got, err)
		}
	}
	for _, rawQuery := range []string{
		"contentView=", "contentView=delta", "contentView=full&contentView=full",
		"contentView=full&extra=1", "extra=1", "contentView=%zz",
	} {
		if _, err := parseExchangeContentView(rawQuery); err == nil {
			t.Fatalf("parseExchangeContentView(%q) unexpectedly succeeded", rawQuery)
		}
	}
}

func setConversationRef(record *activity.Record) {
	if record.Kind == activity.KindExchangeStarted {
		ref := agentconversation.Ref{
			ProjectionID: "exchange:" + record.SubjectID,
			Kind:         agentconversation.KindPendingExchange,
			Evidence:     agentconversation.EvidencePending,
		}
		record.Conversation = &ref
		return
	}
	if record.CaptureRunID != "" {
		ref := agentconversation.Ref{
			ProjectionID: "capture_run:" + record.CaptureRunID + ":main",
			DisplayName:  record.SourceDisplayName,
			Kind:         agentconversation.KindMain,
			Evidence:     agentconversation.EvidenceCaptureRun,
		}
		record.Conversation = &ref
		return
	}
	ref := agentconversation.Ref{
		ProjectionID: "exchange:" + record.SubjectID,
		Kind:         agentconversation.KindIsolatedExchange,
		Evidence:     agentconversation.EvidenceUndecodedExchange,
	}
	record.Conversation = &ref
}

func exchangeContentFixture(t *testing.T, activityRecord activity.Record) exchangecontent.Record {
	t.Helper()
	user, err := protocolcore.NewTextBlock("inspect this")
	if err != nil {
		t.Fatal(err)
	}
	arguments, err := protocolcore.NewJSONObject([]byte(`{"path":"~/README.md"}`), protocolcore.MaxToolJSONBytes)
	if err != nil {
		t.Fatal(err)
	}
	key, err := protocolcore.NewCallKey("anthropic", "call-read")
	if err != nil {
		t.Fatal(err)
	}
	call, err := protocolcore.NewToolCallBlock(protocolcore.ToolCall{
		Key: key, Name: "read_file", Arguments: arguments,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := protocolcore.Request{
		RequestedModel: "claude", EffectiveModel: "claude", MaxOutputTokens: 128,
		Messages: []protocolcore.Message{{
			Role: protocolcore.RoleUser, Blocks: []protocolcore.ContentBlock{user},
		}},
	}
	response := protocolcore.Response{
		ID: "response-content", RequestedModel: "claude", EffectiveModel: "claude",
		ReportedModel: "claude", Blocks: []protocolcore.ContentBlock{call},
		StopReason: protocolcore.StopReasonToolUse,
		Usage: protocolcore.Usage{
			Output: protocolcore.UsageValue{Known: true, Tokens: 3, Source: "provider"},
		},
	}
	content, err := exchangecontent.NewRecord(
		activityRecord.SubjectID,
		exchangecontent.FrozenRef{
			EnvironmentID: activityRecord.EnvironmentID, EnvironmentRevision: activityRecord.EnvironmentRevision,
			EnvironmentDigest: activityRecord.EnvironmentDigest,
			ClientEndpointID:  activityRecord.ClientEndpointID, ClientEndpointRevision: activityRecord.ClientEndpointRevision,
			ProtocolPlanID: activityRecord.ProtocolPlanID, ProtocolPlanRevision: activityRecord.ProtocolPlanRevision,
			RouteID: activityRecord.RouteID, RouteRevision: activityRecord.RouteRevision,
		},
		environment.DefaultContentRecordingPolicy(),
		activityRecord.OccurredAt,
		request,
		&response,
		exchangecontent.WithParentRef(exchangecontent.ParentRef{
			CaptureRunID:    activityRecord.CaptureRunID,
			ManualCaptureID: activityRecord.ManualCaptureID,
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func TestActivityCursorIsCanonicalAndCollectionScoped(t *testing.T) {
	t.Parallel()

	cursor, err := activityCursor(42)
	if err != nil {
		t.Fatal(err)
	}
	if cursor == "42" {
		t.Fatal("Activity cursor exposed the sequence directly")
	}
	sequence, err := parseActivityCursor(cursor)
	if err != nil || sequence != 42 {
		t.Fatalf("parseActivityCursor() = %d, %v", sequence, err)
	}
	for _, invalid := range []string{
		"",
		"42",
		cursor + "=",
		base64.RawURLEncoding.EncodeToString([]byte("v1:42")),
		base64.RawURLEncoding.EncodeToString(
			[]byte(activityCursorPrefix + "042"),
		),
		base64.RawURLEncoding.EncodeToString(
			[]byte("v2:activity-requests:42"),
		),
		base64.RawURLEncoding.EncodeToString(
			[]byte("v1:connections:42"),
		),
	} {
		if _, err := parseActivityCursor(invalid); err == nil {
			t.Fatalf("parseActivityCursor(%q) unexpectedly succeeded", invalid)
		}
	}
}

func TestActivityListQueryIsClosedAndBounded(t *testing.T) {
	t.Parallel()

	defaults, err := parseActivityListQuery("")
	if err != nil || defaults.limit != 50 || defaults.beforeSequence != 0 {
		t.Fatalf("default Activity query = %+v, %v", defaults, err)
	}
	cursor, err := activityCursor(91)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseActivityListQuery(
		"limit=200&cursor=" + cursor +
			"&kind=exchange&captureRunId=run-one&environmentId=work" +
			"&conversationId=capture_run%3Arun-one%3Amain",
	)
	if err != nil ||
		parsed.limit != 200 ||
		parsed.beforeSequence != 91 ||
		parsed.captureRunID != "run-one" ||
		parsed.environmentID != "work" ||
		parsed.conversationID != "capture_run:run-one:main" {
		t.Fatalf("parsed Activity query = %+v, %v", parsed, err)
	}
	manual, err := parseActivityListQuery(
		"kind=exchange&manualCaptureId=manual-one&environmentId=work",
	)
	if err != nil || manual.manualCaptureID != "manual-one" ||
		manual.captureRunID != "" || manual.environmentID != "work" {
		t.Fatalf("parsed ManualCapture Activity query = %+v, %v", manual, err)
	}
	for _, invalid := range []string{
		"cursor=",
		"cursor=not-a-cursor",
		"limit=",
		"limit=0",
		"limit=201",
		"limit=one",
		"unknown=1",
		"beforeSequence=91",
		"limit=1&limit=2",
		"cursor=" + cursor + "&cursor=" + cursor,
		"captureRunId=",
		"captureRunId=run-one&captureRunId=run-two",
		"captureRunId=run%2Fone",
		"manualCaptureId=",
		"manualCaptureId=manual-one&manualCaptureId=manual-two",
		"manualCaptureId=manual%2Fone",
		"captureRunId=run-one&manualCaptureId=manual-one",
		"environmentId=",
		"environmentId=work&environmentId=personal",
		"conversationId=",
		"conversationId=bad%0Aid",
		"conversationId=one&conversationId=two",
		"kind=connection",
		"kind=exchange&kind=exchange",
		"limit=10;cursor=" + cursor,
		"cursor=%zz",
	} {
		if _, err := parseActivityListQuery(invalid); err == nil {
			t.Fatalf("parseActivityListQuery(%q) unexpectedly succeeded", invalid)
		}
	}
}

func TestConversationPageKeepsFlatProjectionIdentity(t *testing.T) {
	t.Parallel()
	record := activity.Record{
		Sequence: 3, ID: "activity-agent", OccurredAt: time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC),
		Kind: activity.KindExchangeCompleted, EnvironmentID: "work", EnvironmentRevision: 1,
		EnvironmentDigest: strings.Repeat("a", 64), ClientEndpointID: "endpoint", ClientEndpointRevision: 1,
		ProtocolPlanID: "protocol", ProtocolPlanRevision: 1, RouteID: "route", RouteRevision: 1,
		SubjectID: "exchange-agent", Status: activity.StatusSucceeded,
		SourceKind: activity.SourceCaptureRun, SourceDisplayName: "codex",
		SourceRecognition: activity.SourceRecognitionVerified, CaptureRunID: "run-agent", ConnectionID: "connection-agent",
	}
	ref := agentconversation.Ref{
		ProjectionID: "capture_run:run-agent:agent:reviewer", DisplayName: "reviewer",
		Kind: agentconversation.KindAgent, Evidence: agentconversation.EvidenceExplicitActor,
		Actor: "/root/reviewer",
	}
	record.Conversation = &ref
	page, err := conversationPageOf(activity.ConversationPage{Items: []activity.ConversationRecord{{
		Conversation: ref, FirstSequence: 1,
		FirstOccurredAt: time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC),
		TurnCount:       3, Latest: record,
	}}})
	if err != nil || len(page.Items) != 1 || page.Items[0].Conversation.ID != ref.ProjectionID ||
		page.Items[0].TurnCount != 3 || page.Items[0].Latest.ID != record.SubjectID {
		t.Fatalf("Conversation page = %+v, %v", page, err)
	}
}

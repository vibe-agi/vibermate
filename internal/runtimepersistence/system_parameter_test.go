package runtimepersistence

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

func systemParameterRecordFixture(
	t *testing.T,
	exchangeID string,
	recordedAt time.Time,
	systemText string,
	messages []transcriptMessage,
	responseText string,
) exchangecontent.Record {
	t.Helper()
	systemBlock, err := protocolcore.NewTextBlock(systemText)
	if err != nil {
		t.Fatal(err)
	}
	requestMessages := make([]protocolcore.Message, 0, len(messages))
	for _, message := range messages {
		block, blockErr := protocolcore.NewTextBlock(message.text)
		if blockErr != nil {
			t.Fatal(blockErr)
		}
		requestMessages = append(requestMessages, protocolcore.Message{
			Role: message.role, Blocks: []protocolcore.ContentBlock{block},
		})
	}
	request := protocolcore.Request{
		RequestedModel: "model", EffectiveModel: "model", MaxOutputTokens: 16,
		System:   []protocolcore.ContentBlock{systemBlock},
		Messages: requestMessages,
	}
	var response *protocolcore.Response
	if responseText != "" {
		block, blockErr := protocolcore.NewTextBlock(responseText)
		if blockErr != nil {
			t.Fatal(blockErr)
		}
		response = &protocolcore.Response{
			ID:             "response-" + exchangeID,
			RequestedModel: "model", EffectiveModel: "model", ReportedModel: "model",
			Blocks:     []protocolcore.ContentBlock{block},
			StopReason: protocolcore.StopReasonEndTurn,
		}
	}
	record, err := exchangecontent.NewRecord(
		exchangeID,
		exchangecontent.FrozenRef{
			EnvironmentID: "work", EnvironmentRevision: 1,
			EnvironmentDigest: strings.Repeat("a", 64),
			ClientEndpointID:  "endpoint", ClientEndpointRevision: 1,
			ProtocolPlanID: "plan", ProtocolPlanRevision: 1,
			RouteID: "route", RouteRevision: 1,
		},
		environment.DefaultContentRecordingPolicy(),
		recordedAt,
		request,
		response,
		exchangecontent.WithParentRef(
			exchangecontent.ParentRef{CaptureRunID: "run-system"},
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

// The top-level system parameter is per-request configuration, not conversation
// history, and real clients put per-request transport telemetry inside it:
// Claude Code sends `x-anthropic-billing-header: …; cch=…; cc_prev_req=…` as
// system[0], and those fields change on every single request. Synthesizing that
// into the front of the message list made it transcript depth 1, so the chain
// forked every turn — 706 of 744 measured Exchanges inherited nothing.
//
// Both dialects with a top-level instruction parameter keep it out of `messages`
// on the wire, and the codec already refuses to hoist an instruction that
// arrived inline. The store must agree.
func TestASystemParameterChangeDoesNotForkTheTranscript(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)
	repository := store.ExchangeContentRepository()
	recordedAt := time.Date(2026, 8, 17, 3, 0, 0, 0, time.UTC)

	first := systemParameterRecordFixture(
		t, "exchange-system-first", recordedAt,
		"x-anthropic-billing-header: cch=aaaaa; cc_prev_req=req_001;",
		[]transcriptMessage{{role: protocolcore.RoleUser, text: "first question"}},
		"first answer",
	)
	if err := repository.Put(context.Background(), first); err != nil {
		t.Fatal(err)
	}

	// Only the per-request telemetry changed. The conversation continued.
	second := systemParameterRecordFixture(
		t, "exchange-system-second", recordedAt.Add(time.Minute),
		"x-anthropic-billing-header: cch=bbbbb; cc_prev_req=req_002;",
		[]transcriptMessage{
			{role: protocolcore.RoleUser, text: "first question"},
			{role: protocolcore.RoleAssistant, text: "first answer"},
			{role: protocolcore.RoleUser, text: "second question"},
		},
		"",
	)
	if err := repository.Put(context.Background(), second); err != nil {
		t.Fatal(err)
	}

	projection, err := repository.GetProjection(
		context.Background(),
		"exchange-system-second",
		recordedAt.Add(2*time.Minute),
		exchangecontent.RequestViewIncremental,
	)
	if err != nil {
		t.Fatal(err)
	}
	if projection.Presentation.Mode != exchangecontent.RequestPresentationIncremental {
		t.Fatalf(
			"presentation = %q, want incremental; a per-request system parameter forked the chain",
			projection.Presentation.Mode,
		)
	}
	if projection.Presentation.InheritedMessageCount != 2 {
		t.Fatalf(
			"inherited = %d, want 2",
			projection.Presentation.InheritedMessageCount,
		)
	}
	if len(projection.Request.Messages) != 1 ||
		projection.Request.Messages[0].Blocks[0].Text != "second question" {
		t.Fatalf("incremental suffix = %+v", projection.Request.Messages)
	}
}

// The system parameter is still recorded, verbatim, as a per-Exchange field.
// Un-flattening moves where it lives; it must not drop anything.
func TestTheSystemParameterIsRetainedAsAPerExchangeField(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)
	repository := store.ExchangeContentRepository()
	recordedAt := time.Date(2026, 8, 17, 4, 0, 0, 0, time.UTC)

	const instructions = "You are an interactive agent. Stay precise."
	record := systemParameterRecordFixture(
		t, "exchange-system-kept", recordedAt, instructions,
		[]transcriptMessage{{role: protocolcore.RoleUser, text: "question"}},
		"answer",
	)
	if err := repository.Put(context.Background(), record); err != nil {
		t.Fatal(err)
	}

	loaded, err := repository.Get(
		context.Background(), "exchange-system-kept", recordedAt.Add(time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Request.System) != 1 ||
		loaded.Request.System[0].Text != instructions {
		t.Fatalf("system parameter was not retained: %+v", loaded.Request.System)
	}
	for _, message := range loaded.Request.Messages {
		if message.Role == "system" {
			t.Fatal("the top-level system parameter re-entered the message list")
		}
	}
}

package runtimepersistence

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/anthropicchat"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/openairesponses"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

// The freeze gate asks that a multi-turn run in each dialect reach incremental
// Request presentation, and that neither invent a continuity the wire did not
// show. These tests drive the real client codecs over real wire bodies into the
// real store, so the claim covers the decoders that actually run in production
// rather than a hand-built IR.
//
// They deliberately do not reach a live provider: the property under test is
// what the store concludes from bytes a client sent, and a network round trip
// would add nothing to it.

// claudeTurnBody renders an Anthropic Messages body. The system parameter
// carries the per-request telemetry Claude Code really sends — `cch` and
// `cc_prev_req` change on every request — so a store that treats the parameter
// as history cannot reach incremental presentation.
func claudeTurnBody(turn int, messages []string) string {
	var rendered strings.Builder
	for index, text := range messages {
		if index > 0 {
			rendered.WriteString(",")
		}
		role := "user"
		if index%2 == 1 {
			role = "assistant"
		}
		fmt.Fprintf(
			&rendered,
			`{"role":%q,"content":[{"type":"text","text":%q}]}`,
			role, text,
		)
	}
	return fmt.Sprintf(`{
	  "model":"claude-opus-5",
	  "max_tokens":1024,
	  "system":[
	    {"type":"text","text":"x-anthropic-billing-header: cc_entrypoint=cli; cch=%05d; cc_prev_req=req_%03d;"},
	    {"type":"text","text":"You are an interactive agent that helps with software engineering tasks."}
	  ],
	  "messages":[%s]
	}`, turn*7919%100000, turn, rendered.String())
}

// codexTurnBody renders an OpenAI Responses body. `instructions` is that
// dialect's top-level parameter and gets the same per-request treatment.
func codexTurnBody(turn int, messages []string) string {
	var rendered strings.Builder
	for index, text := range messages {
		if index > 0 {
			rendered.WriteString(",")
		}
		role := "user"
		if index%2 == 1 {
			role = "assistant"
		}
		fmt.Fprintf(
			&rendered,
			`{"type":"message","role":%q,"content":[{"type":"input_text","text":%q}]}`,
			role, text,
		)
	}
	return fmt.Sprintf(`{
	  "model":"gpt-5.6-sol",
	  "max_output_tokens":1024,
	  "instructions":"session %d: you are a coding agent",
	  "input":[%s]
	}`, turn, rendered.String())
}

func storeWireTurn(
	t *testing.T,
	repository exchangecontent.Repository,
	exchangeID string,
	recordedAt time.Time,
	captureRunID string,
	request protocolcore.Request,
) {
	t.Helper()
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
		nil,
		exchangecontent.WithParentRef(
			exchangecontent.ParentRef{CaptureRunID: captureRunID},
		),
	)
	if err != nil {
		t.Fatalf("%s: build record: %v", exchangeID, err)
	}
	if err := repository.Put(context.Background(), record); err != nil {
		t.Fatalf("%s: put: %v", exchangeID, err)
	}
}

func TestARealClaudeMultiTurnRunReachesIncrementalPresentation(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)
	repository := store.ExchangeContentRepository()
	codec, err := anthropicchat.New(anthropicchat.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	recordedAt := time.Date(2026, 8, 17, 7, 0, 0, 0, time.UTC)

	history := []string{"first question"}
	for turn := 1; turn <= 4; turn++ {
		request, _, decodeErr := codec.DecodeClientRequest(
			[]byte(claudeTurnBody(turn, history)),
		)
		if decodeErr != nil {
			t.Fatalf("turn %d: decode: %v", turn, decodeErr)
		}
		storeWireTurn(
			t, repository,
			fmt.Sprintf("claude-turn-%d", turn),
			recordedAt.Add(time.Duration(turn)*time.Minute),
			"run-claude", request,
		)
		history = append(
			history,
			fmt.Sprintf("answer %d", turn),
			fmt.Sprintf("question %d", turn+1),
		)
	}

	projection, err := repository.GetProjection(
		context.Background(), "claude-turn-4", recordedAt.Add(time.Hour),
		exchangecontent.RequestViewIncremental,
	)
	if err != nil {
		t.Fatal(err)
	}
	if projection.Presentation.Mode !=
		exchangecontent.RequestPresentationIncremental {
		t.Fatalf(
			"presentation = %q, want incremental after four Claude turns",
			projection.Presentation.Mode,
		)
	}
	if projection.Presentation.InheritedMessageCount != 5 {
		t.Fatalf("inherited = %d, want 5",
			projection.Presentation.InheritedMessageCount)
	}
	if len(projection.Request.Messages) != 2 {
		t.Fatalf("suffix = %d messages, want 2", len(projection.Request.Messages))
	}
}

func TestARealCodexMultiTurnRunReachesIncrementalPresentation(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)
	repository := store.ExchangeContentRepository()
	codec, err := openairesponses.New(openairesponses.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	recordedAt := time.Date(2026, 8, 17, 8, 0, 0, 0, time.UTC)

	history := []string{"first question"}
	for turn := 1; turn <= 4; turn++ {
		request, _, decodeErr := codec.DecodeClientRequest(
			[]byte(codexTurnBody(turn, history)),
		)
		if decodeErr != nil {
			t.Fatalf("turn %d: decode: %v", turn, decodeErr)
		}
		storeWireTurn(
			t, repository,
			fmt.Sprintf("codex-turn-%d", turn),
			recordedAt.Add(time.Duration(turn)*time.Minute),
			"run-codex", request,
		)
		history = append(
			history,
			fmt.Sprintf("answer %d", turn),
			fmt.Sprintf("question %d", turn+1),
		)
	}

	projection, err := repository.GetProjection(
		context.Background(), "codex-turn-4", recordedAt.Add(time.Hour),
		exchangecontent.RequestViewIncremental,
	)
	if err != nil {
		t.Fatal(err)
	}
	if projection.Presentation.Mode !=
		exchangecontent.RequestPresentationIncremental {
		t.Fatalf(
			"presentation = %q, want incremental after four Codex turns",
			projection.Presentation.Mode,
		)
	}
	if projection.Presentation.InheritedMessageCount != 5 {
		t.Fatalf("inherited = %d, want 5",
			projection.Presentation.InheritedMessageCount)
	}
}

// The other half of the gate. Continuity is derived from the messages a request
// carried; a client's own claim that it continues an earlier response must not
// produce one.
func TestClientAssertedContinuityDoesNotJoinExchanges(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)
	repository := store.ExchangeContentRepository()
	codec, err := openairesponses.New(openairesponses.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	recordedAt := time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC)

	first, _, err := codec.DecodeClientRequest([]byte(codexTurnBody(
		1, []string{"first question", "first answer", "second question"},
	)))
	if err != nil {
		t.Fatal(err)
	}
	storeWireTurn(
		t, repository, "codex-server-state-1", recordedAt, "run-server-state",
		first,
	)

	// The server holds the history, so the client sends only the new input and
	// names the response it continues. The store never saw that history.
	continuation, _, err := codec.DecodeClientRequest([]byte(`{
	  "model":"gpt-5.6-sol",
	  "max_output_tokens":1024,
	  "previous_response_id":"resp_abc123",
	  "input":[
	    {"type":"message","role":"user",
	     "content":[{"type":"input_text","text":"third question"}]}
	  ]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	storeWireTurn(
		t, repository, "codex-server-state-2", recordedAt.Add(time.Minute),
		"run-server-state", continuation,
	)

	projection, err := repository.GetProjection(
		context.Background(), "codex-server-state-2", recordedAt.Add(time.Hour),
		exchangecontent.RequestViewIncremental,
	)
	if err != nil {
		t.Fatal(err)
	}
	if projection.Presentation.InheritedMessageCount != 0 {
		t.Fatalf(
			"inherited = %d from a client-asserted continuation; the store "+
				"invented a transcript it never observed",
			projection.Presentation.InheritedMessageCount,
		)
	}
	if projection.Presentation.Mode !=
		exchangecontent.RequestPresentationCheckpoint {
		t.Fatalf("presentation = %q, want checkpoint",
			projection.Presentation.Mode)
	}
	// The claim is still retained as protocol evidence, because the client did
	// make it.
	found := false
	for _, value := range projection.Request.ProtocolEvidence {
		if value.Name == "openai_responses.previous_response_id" &&
			value.Value == "resp_abc123" {
			found = true
		}
	}
	if !found {
		t.Fatalf(
			"previous_response_id was not retained as evidence: %+v",
			projection.Request.ProtocolEvidence,
		)
	}
}

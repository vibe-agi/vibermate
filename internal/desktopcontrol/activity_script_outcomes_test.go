package desktopcontrol

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
)

func scriptOutcomeRecord() activity.Record {
	record := activity.Record{
		Sequence: 1, ID: "activity-scripts", OccurredAt: time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC),
		Kind:          activity.KindExchangeCompleted,
		EnvironmentID: "work", EnvironmentRevision: 4,
		EnvironmentDigest: strings.Repeat("a", 64),
		ClientEndpointID:  "endpoint.claude", ClientEndpointRevision: 2,
		ProtocolPlanID: "plan.claude", ProtocolPlanRevision: 3,
		RouteID: "route.claude", RouteRevision: 5,
		SubjectID: "exchange-scripts", Status: activity.StatusFailed,
		ReasonCode: "message_transform_failed",
		SourceKind: activity.SourceCaptureRun, SourceDisplayName: "claude",
		SourceRecognition: activity.SourceRecognitionVerified, CaptureRunID: "run-scripts",
		ConnectionID: "connection-scripts",
		Scripts: []activity.ScriptOutcome{
			{ScriptID: "mask", Revision: 4, Name: "Mask secrets", Stage: activity.ScriptStageRequest, Outcome: activity.ScriptChanged, Applications: 1, Changed: 1, BodyChanged: true, BytesBefore: 900, BytesAfter: 880},
			{ScriptID: "guard", Revision: 2, Name: "Block dangerous commands", Stage: activity.ScriptStageRequest, Outcome: activity.ScriptBlocked, BlockReason: "secret-in-prompt", Applications: 1, BytesBefore: 880, BytesAfter: 880},
		},
	}
	setConversationRef(&record)
	return record
}

func TestActivitySummaryReportsScriptOutcomes(t *testing.T) {
	t.Parallel()
	record := scriptOutcomeRecord()
	if err := record.Validate(); err != nil {
		t.Fatalf("fixture record is invalid: %v", err)
	}
	page, err := activityPageOf(activity.Page{Items: []activity.Record{record}})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(page.Items[0])
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Scripts []activity.ScriptOutcome `json:"scripts"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Scripts) != 2 || decoded.Scripts[0] != record.Scripts[0] || decoded.Scripts[1] != record.Scripts[1] {
		t.Fatalf("summary scripts = %+v", decoded.Scripts)
	}
	// The view must not alias the journal record.
	page.Items[0].Scripts[0].Name = "changed"
	if record.Scripts[0].Name != "Mask secrets" {
		t.Fatal("summary shares its script outcomes with the record")
	}

	record.Scripts = nil
	page, err = activityPageOf(activity.Page{Items: []activity.Record{record}})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err = json.Marshal(page.Items[0])
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(`"scripts"`)) {
		t.Fatalf("a request no script touched still reports scripts: %s", encoded)
	}
}

func TestExchangeDetailReportsScriptOutcomesInBothEncodings(t *testing.T) {
	t.Parallel()
	record := scriptOutcomeRecord()
	detail, err := exchangeDetailOf(record, egressaudit.Page{}, nil, ExchangeContentViewFull)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.ProcessingTrace.Scripts) != 2 || detail.ProcessingTrace.Scripts[1].BlockReason != "secret-in-prompt" {
		t.Fatalf("detail scripts = %+v", detail.ProcessingTrace.Scripts)
	}
	standard, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	var streamed bytes.Buffer
	if err := writeExchangeDetailJSON(&streamed, detail); err != nil {
		t.Fatal(err)
	}
	var left, right any
	if err := json.Unmarshal(standard, &left); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(streamed.Bytes(), &right); err != nil {
		t.Fatalf("streamed detail is not JSON: %v\n%s", err, streamed.String())
	}
	leftText, _ := json.Marshal(left)
	rightText, _ := json.Marshal(right)
	if !bytes.Equal(leftText, rightText) {
		t.Fatalf("streamed detail differs from the standard encoding\nstandard: %s\nstreamed: %s", leftText, rightText)
	}
	if !bytes.Contains(streamed.Bytes(), []byte(`"blockReason":"secret-in-prompt"`)) {
		t.Fatalf("streamed detail omits script outcomes: %s", streamed.String())
	}
}

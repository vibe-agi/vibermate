package desktopcontrol_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/agentconversation"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

func TestCompleteBlockDetailsHTTPUsesExistingScopedPageRoute(t *testing.T) {
	runtime := startRuntime(t)
	t.Cleanup(func() { shutdownRuntime(t, runtime) })
	text, _ := protocolcore.NewTextBlock("question")
	request := protocolcore.Request{RequestedModel: "model", EffectiveModel: "model", Messages: []protocolcore.Message{{Role: protocolcore.RoleUser, Blocks: []protocolcore.ContentBlock{text}}}}
	response := protocolcore.Response{ID: "details", RequestedModel: "model", EffectiveModel: "model", ReportedModel: "model", StopReason: protocolcore.StopReasonEndTurn, Blocks: []protocolcore.ContentBlock{text}}
	captureID, scope := createScopedFixtureCapture(t, runtime)
	parent := exchangecontent.ParentRef{CaptureRunID: captureID}
	frozen := exchangecontent.FrozenRef{EnvironmentID: "work", EnvironmentRevision: 1, EnvironmentDigest: strings.Repeat("a", 64), ClientEndpointID: "client", ClientEndpointRevision: 1, ProtocolPlanID: "plan", ProtocolPlanRevision: 1, RouteID: "route", RouteRevision: 1}
	record, err := exchangecontent.NewRecord("exchange-complete-details", frozen, environment.DefaultContentRecordingPolicy(), time.Now().UTC(), request, &response, exchangecontent.WithParentRef(parent))
	if err != nil {
		t.Fatal(err)
	}
	block := exchangecontent.Block{Kind: "reasoning", Availability: exchangecontent.AvailabilityRecorded, Text: "short body", ProviderSource: strings.Repeat("p", (1<<20)+1), ProviderKind: "thinking"}
	record.Response.Blocks = []exchangecontent.Block{block}
	appendScopedFixtureRecord(t, runtime, scope, record)
	conversation, err := agentconversation.Project(agentconversation.ProjectionInput{CaptureRunID: parent.CaptureRunID, ExchangeID: record.ExchangeID, SourceDisplayName: "Codex", Request: &request})
	if err != nil {
		t.Fatal(err)
	}
	appendScopedFixtureActivity(t, runtime, scope, activity.Event{Kind: activity.KindExchangeCompleted, SubjectID: record.ExchangeID, Status: activity.StatusSucceeded, EnvironmentID: environment.EnvironmentID(frozen.EnvironmentID), EnvironmentRevision: 1, EnvironmentDigest: frozen.EnvironmentDigest, ClientEndpointID: "client", ClientEndpointRevision: 1, ProtocolPlanID: "plan", ProtocolPlanRevision: 1, RouteID: "route", RouteRevision: 1, SourceKind: activity.SourceCaptureRun, SourceDisplayName: "Codex", SourceRecognition: activity.SourceRecognitionVerified, CaptureRunID: parent.CaptureRunID, ConnectionID: "details-connection", Conversation: conversation})
	app, err := desktopcontrol.New(desktopcontrol.Options{Readiness: readyState(true), Status: runtime, Environments: runtime.Environments(), Assignments: runtime.CaptureAssignments(), Activities: runtime.Activities(), Contents: runtime.ExchangeContents(), Connections: runtime.ConnectionEvents(), Egress: runtime.EgressAttempts(), Approvals: runtime.ToolApprovals(), Endpoints: runtime.UpstreamEndpoints(), Accounts: runtime.ProviderAccounts(), Offline: runtime, Clock: desktopcontrol.SystemClock{}})
	if err != nil {
		t.Fatal(err)
	}
	get := func(id, cursor string) *httptest.ResponseRecorder {
		q := "?contentMode=paged"
		if cursor != "" {
			q += "&contentCursor=" + url.QueryEscape(cursor)
		}
		w := httptest.NewRecorder()
		app.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/exchanges/"+id+q, nil))
		return w
	}
	initial := get(record.ExchangeID, "")
	var detail desktopcontrol.ExchangeDetail
	if initial.Code != 200 || json.Unmarshal(initial.Body.Bytes(), &detail) != nil {
		t.Fatalf("initial HTTP status%d", initial.Code)
	}
	read := func(cursor string) exchangecontent.ContentPage {
		t.Helper()
		w := get(record.ExchangeID, cursor)
		var page exchangecontent.ContentPage
		if w.Code != 200 || w.Body.Len() > exchangecontent.MaxPageBytes || json.Unmarshal(w.Body.Bytes(), &page) != nil {
			t.Fatalf("detail HTTP status%d bytes%d: %s", w.Code, w.Body.Len(), w.Body.String())
		}
		return page
	}
	message := read(detail.Content.Response.Blocks[0].Deferred.Cursor)
	body := read(message.Blocks[0].Deferred.Cursor)
	if body.Text != "short body" || body.CanonicalCursor == "" {
		t.Fatal("readable body or complete-record action missing")
	}
	var complete bytes.Buffer
	for cursor := body.CanonicalCursor; cursor != ""; {
		page := read(cursor)
		if page.Kind != "block_bytes" || page.Offset != complete.Len() {
			t.Fatal("canonical page position changed")
		}
		complete.Write(page.Data)
		cursor = page.NextCursor
	}
	want, _ := json.Marshal(block)
	if !bytes.Equal(complete.Bytes(), want) {
		t.Fatal("HTTP lost retained metadata bytes")
	}
	if wrong := get("different-exchange", body.CanonicalCursor); wrong.Code == 200 {
		t.Fatal("cursor disclosed a different Exchange")
	}
}

func TestPagedExchangeHTTPKeepsLargeHistoryBelowTheControlReadLimit(t *testing.T) {
	runtime := startRuntime(t)
	t.Cleanup(func() { shutdownRuntime(t, runtime) })
	text, err := protocolcore.NewTextBlock(strings.Repeat("retained history ", 150))
	if err != nil {
		t.Fatal(err)
	}
	request := protocolcore.Request{RequestedModel: "model", EffectiveModel: "model", MaxOutputTokens: 8}
	for range 1118 {
		request.Messages = append(request.Messages, protocolcore.Message{Role: protocolcore.RoleUser, Blocks: []protocolcore.ContentBlock{text}})
	}
	output, err := protocolcore.NewTextBlock(strings.Repeat("large output ", 200000))
	if err != nil {
		t.Fatal(err)
	}
	response := protocolcore.Response{ID: "response-pages", RequestedModel: "model", EffectiveModel: "model", ReportedModel: "model", StopReason: protocolcore.StopReasonEndTurn, Blocks: []protocolcore.ContentBlock{output}}
	const id = "exchange-pages"
	captureID, scope := createScopedFixtureCapture(t, runtime)
	parent := exchangecontent.ParentRef{CaptureRunID: captureID}
	frozen := exchangecontent.FrozenRef{EnvironmentID: "work", EnvironmentRevision: 1, EnvironmentDigest: strings.Repeat("a", 64), ClientEndpointID: "client", ClientEndpointRevision: 1, ProtocolPlanID: "plan", ProtocolPlanRevision: 1, RouteID: "route", RouteRevision: 1}
	record, err := exchangecontent.NewRecord(id, frozen, environment.DefaultContentRecordingPolicy(), time.Now().UTC(), request, &response, exchangecontent.WithParentRef(parent))
	if err != nil {
		t.Fatal(err)
	}
	appendScopedFixtureRecord(t, runtime, scope, record)
	conversation, err := agentconversation.Project(agentconversation.ProjectionInput{CaptureRunID: parent.CaptureRunID, ExchangeID: id, SourceDisplayName: "Codex", Request: &request})
	if err != nil {
		t.Fatal(err)
	}
	appendScopedFixtureActivity(t, runtime, scope, activity.Event{Kind: activity.KindExchangeCompleted, SubjectID: id, Status: activity.StatusSucceeded, EnvironmentID: environment.EnvironmentID(frozen.EnvironmentID), EnvironmentRevision: 1, EnvironmentDigest: frozen.EnvironmentDigest, ClientEndpointID: "client", ClientEndpointRevision: 1, ProtocolPlanID: "plan", ProtocolPlanRevision: 1, RouteID: "route", RouteRevision: 1, SourceKind: activity.SourceCaptureRun, SourceDisplayName: "Codex", SourceRecognition: activity.SourceRecognitionVerified, CaptureRunID: parent.CaptureRunID, ConnectionID: "connection-pages", Conversation: conversation})
	application, err := desktopcontrol.New(desktopcontrol.Options{Readiness: readyState(true), Status: runtime, Environments: runtime.Environments(), Assignments: runtime.CaptureAssignments(), Activities: runtime.Activities(), Contents: runtime.ExchangeContents(), Connections: runtime.ConnectionEvents(), Egress: runtime.EgressAttempts(), Approvals: runtime.ToolApprovals(), Endpoints: runtime.UpstreamEndpoints(), Accounts: runtime.ProviderAccounts(), Offline: runtime, Clock: desktopcontrol.SystemClock{}})
	if err != nil {
		t.Fatal(err)
	}
	get := func(query string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRecorder()
		application.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/api/v1/exchanges/"+id+query, nil))
		return r
	}
	legacy := get("")
	if legacy.Code != 200 || legacy.Body.Len() <= 2<<20 {
		t.Fatalf("fixture did not reproduce the old response limit: status=%d bytes=%d", legacy.Code, legacy.Body.Len())
	}
	initial := get("?contentMode=paged")
	if initial.Code != 200 || initial.Body.Len() > 2<<20 {
		t.Fatalf("paged response: status=%d bytes=%d", initial.Code, initial.Body.Len())
	}
	var detail desktopcontrol.ExchangeDetail
	if json.Unmarshal(initial.Body.Bytes(), &detail) != nil || detail.Content.Page == nil || len(detail.Content.Request.Messages) != 0 || detail.Content.Page.RequestNextCursor == "" {
		t.Fatal("checkpoint did not defer history")
	}
	for _, cursor := range []string{detail.Content.Page.RequestNextCursor, detail.Content.Response.Blocks[0].Deferred.Cursor} {
		r := get("?contentMode=paged&contentCursor=" + url.QueryEscape(cursor))
		var page exchangecontent.ContentPage
		if r.Code != 200 || r.Body.Len() > 2<<20 || json.Unmarshal(r.Body.Bytes(), &page) != nil || page.ExchangeID != id {
			t.Fatalf("content page: status=%d bytes=%d", r.Code, r.Body.Len())
		}
	}
	for _, query := range []string{"?contentMode=unbounded", "?contentCursor=unscoped", "?contentMode=paged&contentCursor=invalid", "?contentMode=paged&contentView=full&contentCursor=x", "?contentMode=paged&contentMode=paged"} {
		if r := get(query); r.Code != 422 {
			t.Fatalf("invalid query %s: %d", query, r.Code)
		}
	}
}

func TestToolLookupFindsCheckpointReturnOutsideVisiblePage(t *testing.T) {
	testToolLookup(t, "RETAINED_TOOL_RESULT")
}

func TestToolLookupFindsDeferredLongReturn(t *testing.T) {
	testToolLookup(t, strings.Repeat("LONG_TOOL_RESULT\n", 16000))
}

func testToolLookup(t *testing.T, resultText string) {
	t.Helper()
	runtime := startRuntime(t)
	t.Cleanup(func() { shutdownRuntime(t, runtime) })
	ctx := context.Background()
	frozen := exchangecontent.FrozenRef{EnvironmentID: "work", EnvironmentRevision: 1, EnvironmentDigest: strings.Repeat("a", 64), ClientEndpointID: "client", ClientEndpointRevision: 1, ProtocolPlanID: "plan", ProtocolPlanRevision: 1, RouteID: "route", RouteRevision: 1}
	captureID, scope := createScopedFixtureCapture(t, runtime)
	parent := exchangecontent.ParentRef{CaptureRunID: captureID}
	key, err := protocolcore.NewCallKey("test", "wanted-call")
	if err != nil {
		t.Fatal(err)
	}
	arguments, err := protocolcore.NewJSONObject([]byte(`{"cmd":"pwd"}`), 1024)
	if err != nil {
		t.Fatal(err)
	}
	call, err := protocolcore.NewToolCallBlock(protocolcore.ToolCall{Key: key, Name: "exec_command", Arguments: arguments})
	if err != nil {
		t.Fatal(err)
	}
	returned, err := protocolcore.NewToolResultBlock(protocolcore.ToolResult{Key: key, Content: resultText})
	if err != nil {
		t.Fatal(err)
	}
	question, _ := protocolcore.NewTextBlock("QUESTION")
	reply, _ := protocolcore.NewTextBlock("ANSWER")
	requests := []protocolcore.Request{
		{RequestedModel: "model", EffectiveModel: "model", Messages: []protocolcore.Message{{Role: protocolcore.RoleUser, Blocks: []protocolcore.ContentBlock{question}}}},
		{RequestedModel: "model", EffectiveModel: "model", Messages: []protocolcore.Message{{Role: protocolcore.RoleUser, Blocks: []protocolcore.ContentBlock{question}}, {Role: protocolcore.RoleTool, Blocks: []protocolcore.ContentBlock{returned}}}},
	}
	// Keep the matching return beyond the ordinary tail page.
	for range 60 {
		requests[1].Messages = append(requests[1].Messages, protocolcore.Message{Role: protocolcore.RoleAssistant, Blocks: []protocolcore.ContentBlock{reply}})
	}
	for i, id := range []string{"tool-origin", "tool-return"} {
		request := requests[i]
		output := []protocolcore.ContentBlock{call}
		if i == 1 {
			output = []protocolcore.ContentBlock{reply}
		}
		response := protocolcore.Response{ID: "response-" + id, RequestedModel: "model", EffectiveModel: "model", ReportedModel: "model", StopReason: protocolcore.StopReasonEndTurn, Blocks: output}
		content, e := exchangecontent.NewRecord(id, frozen, environment.DefaultContentRecordingPolicy(), time.Now().UTC(), request, &response, exchangecontent.WithParentRef(parent))
		if e != nil {
			t.Fatal(e)
		}
		appendScopedFixtureRecord(t, runtime, scope, content)
		conversation, e := agentconversation.Project(agentconversation.ProjectionInput{CaptureRunID: parent.CaptureRunID, ExchangeID: id, SourceDisplayName: "Codex", Request: &request})
		if e != nil {
			t.Fatal(e)
		}
		appendScopedFixtureActivity(t, runtime, scope, activity.Event{Kind: activity.KindExchangeCompleted, SubjectID: id, Status: activity.StatusSucceeded, EnvironmentID: "work", EnvironmentRevision: 1, EnvironmentDigest: frozen.EnvironmentDigest, ClientEndpointID: "client", ClientEndpointRevision: 1, ProtocolPlanID: "plan", ProtocolPlanRevision: 1, RouteID: "route", RouteRevision: 1, SourceKind: activity.SourceCaptureRun, SourceDisplayName: "Codex", SourceRecognition: activity.SourceRecognitionVerified, CaptureRunID: parent.CaptureRunID, ConnectionID: "connection-tools", Conversation: conversation})
	}
	application, err := desktopcontrol.New(desktopcontrol.Options{Readiness: readyState(true), Status: runtime, Environments: runtime.Environments(), Assignments: runtime.CaptureAssignments(), Activities: runtime.Activities(), Contents: runtime.ExchangeContents(), Connections: runtime.ConnectionEvents(), Egress: runtime.EgressAttempts(), Approvals: runtime.ToolApprovals(), Endpoints: runtime.UpstreamEndpoints(), Accounts: runtime.ProviderAccounts(), Offline: runtime, Clock: desktopcontrol.SystemClock{}})
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"?callId=wanted-call&unknown=1", "?callId=wanted-call&callId=other", "?callId=%zz", "?callId=wanted-call&cursor=invalid"} {
		recorder := httptest.NewRecorder()
		application.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/exchanges/tool-origin/tool-results"+query, nil))
		if recorder.Code != 422 {
			t.Fatalf("invalid lookup query accepted: %s: %d", query, recorder.Code)
		}
	}
	recorderWithBody := httptest.NewRecorder()
	application.ServeHTTP(recorderWithBody, httptest.NewRequest(http.MethodGet, "/api/v1/exchanges/tool-origin/tool-results?callId=wanted-call", strings.NewReader("unexpected")))
	if recorderWithBody.Code != 422 {
		t.Fatal("lookup accepted a request body")
	}
	cursor := ""
	for i := 0; i < 8; i++ {
		query := url.Values{"callId": {"wanted-call"}}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		recorder := httptest.NewRecorder()
		application.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/exchanges/tool-origin/tool-results?"+query.Encode(), nil))
		var page struct {
			Items []struct {
				ExchangeID string                `json:"exchangeId"`
				Block      exchangecontent.Block `json:"block"`
			}
			Next string `json:"nextCursor"`
		}
		if recorder.Code != 200 || json.Unmarshal(recorder.Body.Bytes(), &page) != nil {
			t.Fatalf("lookup failed: %d %s", recorder.Code, recorder.Body)
		}
		if len(page.Items) > 0 {
			if page.Items[0].ExchangeID != "tool-return" {
				t.Fatal("unrelated return")
			}
			block := page.Items[0].Block
			if block.Deferred == nil {
				if block.Text != resultText {
					t.Fatal("unrelated result text")
				}
			} else {
				bodyCursor, retained := block.Deferred.Cursor, ""
				for bodyCursor != "" {
					bodyPage, e := runtime.ExchangeContents().GetContentPage(ctx, "tool-return", bodyCursor)
					if e != nil || bodyPage.CallID != "wanted-call" || bodyPage.BlockKind != "tool_result" {
						t.Fatalf("deferred result lost its scope: %v", e)
					}
					retained += bodyPage.Text
					bodyCursor = bodyPage.NextCursor
				}
				if retained != resultText {
					t.Fatal("deferred result was truncated")
				}
			}
			return
		}
		if page.Next == "" || page.Next == cursor {
			t.Fatal("lookup stopped before the retained return")
		}
		cursor = page.Next
	}
	t.Fatal("lookup did not finish within its bounded pages")
}

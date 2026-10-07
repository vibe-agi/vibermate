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
	ctx := context.Background()
	text, _ := protocolcore.NewTextBlock("question")
	request := protocolcore.Request{RequestedModel: "model", EffectiveModel: "model", Messages: []protocolcore.Message{{Role: protocolcore.RoleUser, Blocks: []protocolcore.ContentBlock{text}}}}
	response := protocolcore.Response{ID: "details", RequestedModel: "model", EffectiveModel: "model", ReportedModel: "model", StopReason: protocolcore.StopReasonEndTurn, Blocks: []protocolcore.ContentBlock{text}}
	parent := exchangecontent.ParentRef{CaptureRunID: "details-run"}
	frozen := exchangecontent.FrozenRef{EnvironmentID: "work", EnvironmentRevision: 1, EnvironmentDigest: strings.Repeat("a", 64), ClientEndpointID: "client", ClientEndpointRevision: 1, ProtocolPlanID: "plan", ProtocolPlanRevision: 1, RouteID: "route", RouteRevision: 1}
	record, err := exchangecontent.NewRecord("exchange-complete-details", frozen, environment.DefaultContentRecordingPolicy(), time.Now().UTC(), request, &response, exchangecontent.WithParentRef(parent))
	if err != nil {
		t.Fatal(err)
	}
	block := exchangecontent.Block{Kind: "reasoning", Availability: exchangecontent.AvailabilityRecorded, Text: "short body", ProviderSource: strings.Repeat("p", (1<<20)+1), ProviderKind: "thinking"}
	record.Response.Blocks = []exchangecontent.Block{block}
	if err := runtime.ExchangeContents().(exchangecontent.Recorder).Record(ctx, record); err != nil {
		t.Fatal(err)
	}
	conversation, err := agentconversation.Project(agentconversation.ProjectionInput{CaptureRunID: parent.CaptureRunID, ExchangeID: record.ExchangeID, SourceDisplayName: "Codex", Request: &request})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.Activities().Record(ctx, activity.Event{Kind: activity.KindExchangeCompleted, SubjectID: record.ExchangeID, Status: activity.StatusSucceeded, EnvironmentID: environment.EnvironmentID(frozen.EnvironmentID), EnvironmentRevision: 1, EnvironmentDigest: frozen.EnvironmentDigest, ClientEndpointID: "client", ClientEndpointRevision: 1, ProtocolPlanID: "plan", ProtocolPlanRevision: 1, RouteID: "route", RouteRevision: 1, SourceKind: activity.SourceCaptureRun, SourceDisplayName: "Codex", SourceRecognition: activity.SourceRecognitionVerified, CaptureRunID: parent.CaptureRunID, ConnectionID: "details-connection", Conversation: conversation})
	if err != nil {
		t.Fatal(err)
	}
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
	ctx := context.Background()
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
	parent := exchangecontent.ParentRef{CaptureRunID: "run-pages"}
	frozen := exchangecontent.FrozenRef{EnvironmentID: "work", EnvironmentRevision: 1, EnvironmentDigest: strings.Repeat("a", 64), ClientEndpointID: "client", ClientEndpointRevision: 1, ProtocolPlanID: "plan", ProtocolPlanRevision: 1, RouteID: "route", RouteRevision: 1}
	record, err := exchangecontent.NewRecord(id, frozen, environment.DefaultContentRecordingPolicy(), time.Now().UTC(), request, &response, exchangecontent.WithParentRef(parent))
	if err != nil {
		t.Fatal(err)
	}
	if err = runtime.ExchangeContents().(exchangecontent.Recorder).Record(ctx, record); err != nil {
		t.Fatal(err)
	}
	conversation, err := agentconversation.Project(agentconversation.ProjectionInput{CaptureRunID: parent.CaptureRunID, ExchangeID: id, SourceDisplayName: "Codex", Request: &request})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.Activities().Record(ctx, activity.Event{Kind: activity.KindExchangeCompleted, SubjectID: id, Status: activity.StatusSucceeded, EnvironmentID: environment.EnvironmentID(frozen.EnvironmentID), EnvironmentRevision: 1, EnvironmentDigest: frozen.EnvironmentDigest, ClientEndpointID: "client", ClientEndpointRevision: 1, ProtocolPlanID: "plan", ProtocolPlanRevision: 1, RouteID: "route", RouteRevision: 1, SourceKind: activity.SourceCaptureRun, SourceDisplayName: "Codex", SourceRecognition: activity.SourceRecognitionVerified, CaptureRunID: parent.CaptureRunID, ConnectionID: "connection-pages", Conversation: conversation})
	if err != nil {
		t.Fatal(err)
	}
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

package desktopcontrol_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/agentconversation"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

func TestCandidateDeepResponseAndNextHistoryThroughCompleteAndPagedHTTP(t *testing.T) {
	limits := exchangecontent.SourceLimits{Semantic: protocolcore.ResourceLimits{Request: protocolcore.ResourceCost{PayloadBytes: 32 << 20, StructureBytes: 32 << 20}, Response: protocolcore.ResourceCost{PayloadBytes: 32 << 20, StructureBytes: 32 << 20}}, Scratch: protocolcore.ResourceCost{PayloadBytes: 128 << 20, StructureBytes: 32 << 20}, CanonicalBytes: 256 << 20, RetainedBytes: 64 << 20, StructureBytes: 32 << 20}
	reserve, scratch, err := exchange.RequiredResponseReservation(limits.Semantic)
	if err != nil {
		t.Fatal(err)
	}
	limits.RetainedBytes += reserve.RetainedBytes
	limits.CanonicalBytes += reserve.CanonicalBytes
	limits.StructureBytes += reserve.StructureBytes
	limits.Scratch = scratch
	requestBytes, responseBytes, err := exchange.RequiredExecutionEnvelope(limits)
	if err != nil {
		t.Fatal(err)
	}
	runtime := startRuntimeWithSecrets(t, newCredentialStoreFixture(), exchange.ResourcePolicy{Content: limits, RequestBytes: requestBytes, ResponseBytes: responseBytes, SlotBytes: requestBytes + responseBytes, ActiveBytes: 4 * (requestBytes + responseBytes)})
	defer shutdownRuntime(t, runtime)
	ctx := context.Background()
	raw := `{"a":` + strings.Repeat("[", 9999) + "0" + strings.Repeat("]", 9999) + "}"
	arguments, err := protocolcore.NewJSONObject([]byte(raw), protocolcore.MaxToolJSONBytes)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := protocolcore.NewCallKey("openai-responses", "call_deep")
	call, err := protocolcore.NewToolCallBlock(protocolcore.ToolCall{Key: key, Name: "f", Arguments: arguments})
	if err != nil {
		t.Fatal(err)
	}
	text, _ := protocolcore.NewTextBlock("question")
	request := protocolcore.Request{RequestedModel: "m", EffectiveModel: "m", Messages: []protocolcore.Message{{Role: protocolcore.RoleUser, Blocks: []protocolcore.ContentBlock{text}}}}
	response := protocolcore.Response{ID: "response-deep", RequestedModel: "m", EffectiveModel: "m", ReportedModel: "m", StopReason: protocolcore.StopReasonToolUse, Blocks: []protocolcore.ContentBlock{call}}
	parent := exchangecontent.ParentRef{CaptureRunID: "deep-run"}
	frozen := exchangecontent.FrozenRef{EnvironmentID: "work", EnvironmentRevision: 1, EnvironmentDigest: strings.Repeat("a", 64), ClientEndpointID: "client", ClientEndpointRevision: 1, ProtocolPlanID: "plan", ProtocolPlanRevision: 1, RouteID: "route", RouteRevision: 1}
	recorder := runtime.ExchangeContents().(exchangecontent.SourceRecorder)
	for _, id := range []string{"deep-response", "deep-next-history"} {
		var terminal *protocolcore.Response = &response
		if id == "deep-next-history" {
			request.Messages = append(request.Messages, protocolcore.Message{Role: protocolcore.RoleAssistant, Blocks: []protocolcore.ContentBlock{call}}, protocolcore.Message{Role: protocolcore.RoleUser, Blocks: []protocolcore.ContentBlock{text}})
			terminal = nil
		}
		source, err := exchangecontent.NewSourceWithin(limits, id, frozen, environment.DefaultContentRecordingPolicy(), time.Now().UTC(), request, terminal, exchangecontent.WithParentRef(parent))
		if err != nil {
			t.Fatal(err)
		}
		if err = recorder.RecordSource(ctx, source); err != nil {
			t.Fatal(err)
		}
		conversation, err := agentconversation.Project(agentconversation.ProjectionInput{CaptureRunID: parent.CaptureRunID, ExchangeID: id, SourceDisplayName: "Codex", Request: &request})
		if err != nil {
			t.Fatal(err)
		}
		_, err = runtime.Activities().Record(ctx, activity.Event{Kind: activity.KindExchangeCompleted, SubjectID: id, Status: activity.StatusSucceeded, EnvironmentID: "work", EnvironmentRevision: 1, EnvironmentDigest: frozen.EnvironmentDigest, ClientEndpointID: "client", ClientEndpointRevision: 1, ProtocolPlanID: "plan", ProtocolPlanRevision: 1, RouteID: "route", RouteRevision: 1, SourceKind: activity.SourceCaptureRun, SourceDisplayName: "Codex", SourceRecognition: activity.SourceRecognitionVerified, CaptureRunID: parent.CaptureRunID, ConnectionID: "deep-connection", Conversation: conversation})
		if err != nil {
			t.Fatal(err)
		}
	}
	app, err := desktopcontrol.New(desktopcontrol.Options{ContentLimits: &limits, Readiness: readyState(true), Status: runtime, Environments: runtime.Environments(), Assignments: runtime.CaptureAssignments(), Activities: runtime.Activities(), Contents: runtime.ExchangeContents(), Connections: runtime.ConnectionEvents(), Egress: runtime.EgressAttempts(), Approvals: runtime.ToolApprovals(), Endpoints: runtime.UpstreamEndpoints(), Accounts: runtime.ProviderAccounts(), Offline: runtime, Clock: desktopcontrol.SystemClock{}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/fault-") {
			mode := r.URL.Path
			ctx, cancel := context.WithCancel(r.Context())
			defer cancel()
			r = r.WithContext(ctx)
			r.URL.Path = "/api/v1/exchanges/deep-response"
			r.URL.RawQuery = "contentView=full"
			if mode == "/fault-before-headers" {
				cancel()
				app.ServeHTTP(w, r)
				return
			}
			writer := &interruptedDetailWriter{ResponseWriter: w, fail: mode == "/fault-write"}
			if mode == "/fault-cancel" {
				writer.cancel = cancel
			}
			app.ServeHTTP(writer, r)
			return
		}
		app.ServeHTTP(w, r)
	}))
	defer server.Close()
	fixtures := make(map[string]string)
	get := func(id, query string) []byte {
		t.Helper()
		base := "contentView=full"
		if strings.Contains(query, "contentCursor=") {
			base = "contentMode=paged"
		}
		path := "/api/v1/exchanges/" + id + "?" + base + query
		resp, err := server.Client().Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("HTTP %d err=%v body=%s", resp.StatusCode, err, b)
		}
		if strings.Contains(path, "contentMode=paged") {
			fixtures[path] = base64.StdEncoding.EncodeToString(b)
		}
		return b
	}
	for _, id := range []string{"deep-response", "deep-next-history"} {
		body := get(id, "")
		if !bytes.Contains(body, []byte(`"arguments":`+raw)) {
			t.Fatalf("complete HTTP lost deep raw for %s", id)
		}
		var detail desktopcontrol.ExchangeDetail
		if err = json.Unmarshal(get(id, "&contentMode=paged"), &detail); err != nil {
			t.Fatal(err)
		}
		var cursor string
		if id == "deep-response" {
			cursor = detail.Content.Response.Blocks[0].Deferred.Cursor
		} else {
			cursor = detail.Content.Request.Messages[1].Blocks[0].Deferred.Cursor
		}
		var assembled strings.Builder
		var canonical string
		for cursor != "" {
			var page exchangecontent.ContentPage
			if err = json.Unmarshal(get(id, "&contentCursor="+url.QueryEscape(cursor)), &page); err != nil {
				t.Fatal(err)
			}
			if len(page.Blocks) > 0 && page.Blocks[0].Deferred != nil {
				cursor = page.Blocks[0].Deferred.Cursor
				continue
			}
			if page.CanonicalCursor != "" {
				canonical = page.CanonicalCursor
			}
			assembled.WriteString(page.Text)
			cursor = page.NextCursor
		}
		if assembled.String() != raw {
			t.Fatalf("paged HTTP lost complete deep arguments for %s: %d bytes", id, assembled.Len())
		}
		if canonical == "" {
			t.Fatal("complete canonical cursor missing")
		}
		var exact bytes.Buffer
		for canonical != "" {
			var page exchangecontent.ContentPage
			if err = json.Unmarshal(get(id, "&contentCursor="+url.QueryEscape(canonical)), &page); err != nil {
				t.Fatal(err)
			}
			exact.Write(page.Data)
			canonical = page.NextCursor
		}
		if !bytes.Contains(exact.Bytes(), []byte(`"arguments":`+raw)) {
			t.Fatal("exact byte route lost deep arguments")
		}
	}
	for _, path := range []string{"/fault-write", "/fault-cancel"} {
		response, err := server.Client().Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_, readErr := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != 200 || readErr == nil {
			t.Fatalf("published failed writer reported a complete response: %s status=%d err=%v", path, response.StatusCode, readErr)
		}
	}
	responseHTTP, err := server.Client().Get(server.URL + "/fault-before-headers")
	if err != nil {
		t.Fatal(err)
	}
	problem, readErr := io.ReadAll(responseHTTP.Body)
	responseHTTP.Body.Close()
	if responseHTTP.StatusCode != 503 || readErr != nil || !json.Valid(problem) {
		t.Fatal("pre-header cancellation did not produce the existing complete error")
	}
	if path := os.Getenv("TASK5B_HTTP_FIXTURE"); path != "" {
		encoded, err := json.Marshal(fixtures)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, encoded, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

type interruptedDetailWriter struct {
	http.ResponseWriter
	cancel context.CancelFunc
	fail   bool
}

func (w *interruptedDetailWriter) WriteHeader(status int) {
	w.ResponseWriter.WriteHeader(status)
	if status == 200 {
		w.ResponseWriter.(http.Flusher).Flush()
		if w.cancel != nil {
			w.cancel()
		}
	}
}
func (w *interruptedDetailWriter) Write(b []byte) (int, error) {
	if w.fail {
		return 0, io.ErrClosedPipe
	}
	return w.ResponseWriter.Write(b)
}

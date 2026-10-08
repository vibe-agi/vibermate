package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/desktopbootstrap"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/environment"
)

func TestControlRequestAcceptsClosedProblemDocument(t *testing.T) {
	client := testControlClient(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Accept") !=
			"application/json, application/problem+json" {
			t.Errorf("Accept = %q", request.Header.Get("Accept"))
		}
		writer.Header().Set("Content-Type", "application/problem+json")
		writer.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = writer.Write([]byte(
			`{"type":"urn:vibermate:error:invalid-control-request",` +
				`"title":"Unprocessable Entity","status":422,` +
				`"code":"invalid_control_request","operationId":"op-1"}`,
		))
	})

	status, problem, err := client.request(
		context.Background(),
		http.MethodGet,
		"/api/v1/test",
		false,
		nil,
		nil,
		&struct{}{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusUnprocessableEntity ||
		problem.ReasonCode != "invalid_control_request" ||
		problem.OperationID != "op-1" {
		t.Fatalf("problem = %+v, status = %d", problem, status)
	}
}

func TestControlRequestRejectsInvalidProblemContract(t *testing.T) {
	valid := `{"type":"urn:vibermate:error:invalid-control-request",` +
		`"title":"Unprocessable Entity","status":422,` +
		`"code":"invalid_control_request"}`
	for _, test := range []struct {
		name        string
		contentType string
		payload     string
	}{
		{
			name:        "missing problem media type",
			contentType: "application/json",
			payload:     valid,
		},
		{
			name:        "parameterized problem media type",
			contentType: "application/problem+json; charset=utf-8",
			payload:     valid,
		},
		{
			name:        "unknown field",
			contentType: "application/problem+json",
			payload:     strings.TrimSuffix(valid, "}") + `,"detail":"secret"}`,
		},
		{
			name:        "trailing JSON",
			contentType: "application/problem+json",
			payload:     valid + `{}`,
		},
		{
			name:        "nonlexical code",
			contentType: "application/problem+json",
			payload: strings.ReplaceAll(
				strings.ReplaceAll(valid, "invalid_control_request", "Invalid_Control"),
				"invalid-control-request",
				"Invalid-Control",
			),
		},
		{
			name:        "empty operation ID",
			contentType: "application/problem+json",
			payload:     strings.TrimSuffix(valid, "}") + `,"operationId":""}`,
		},
		{
			name:        "null operation ID",
			contentType: "application/problem+json",
			payload:     strings.TrimSuffix(valid, "}") + `,"operationId":null}`,
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			client := testControlClient(t, func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", test.contentType)
				writer.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = writer.Write([]byte(test.payload))
			})
			_, _, err := client.request(
				context.Background(),
				http.MethodGet,
				"/api/v1/test",
				false,
				nil,
				nil,
				&struct{}{},
			)
			if err == nil {
				t.Fatal("invalid Problem response was accepted")
			}
		})
	}
}

func TestControlRequestRequiresClosedJSONSuccess(t *testing.T) {
	for _, test := range []struct {
		name        string
		contentType string
		payload     string
		wantError   bool
	}{
		{
			name:        "valid",
			contentType: "application/json",
			payload:     `{"ready":true}`,
		},
		{
			name:        "wrong media type",
			contentType: "text/json",
			payload:     `{"ready":true}`,
			wantError:   true,
		},
		{
			name:        "parameterized media type",
			contentType: "application/json; charset=utf-8",
			payload:     `{"ready":true}`,
			wantError:   true,
		},
		{
			name:        "unknown field",
			contentType: "application/json",
			payload:     `{"ready":true,"secret":"leak"}`,
			wantError:   true,
		},
		{
			name:        "trailing JSON",
			contentType: "application/json",
			payload:     `{"ready":true}{"ready":false}`,
			wantError:   true,
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			client := testControlClient(t, func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", test.contentType)
				_, _ = writer.Write([]byte(test.payload))
			})
			var output struct {
				Ready bool `json:"ready"`
			}
			_, _, err := client.request(
				context.Background(),
				http.MethodGet,
				"/api/v1/test",
				false,
				nil,
				nil,
				&output,
			)
			if (err != nil) != test.wantError {
				t.Fatalf("error = %v", err)
			}
			if err == nil && !output.Ready {
				t.Fatal("valid response was not decoded")
			}
		})
	}
}

func TestControlActivitiesUsesCanonicalCursorPage(t *testing.T) {
	cursor := base64.RawURLEncoding.EncodeToString(
		[]byte("v1:activity-requests:41"),
	)
	client := testControlClient(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/activities" ||
			request.URL.Query().Get("limit") != strconv.Itoa(activity.MaxPageSize) ||
			request.URL.Query().Get("cursor") != cursor {
			t.Errorf("Activity request URL = %q", request.URL.String())
		}
		writer.Header().Set("Content-Type", "application/json")
		digest := strings.Repeat("a", 64)
		_, _ = writer.Write([]byte(
			`{"items":[{"id":"Exchange-1",` +
				`"occurredAt":"2026-08-03T01:02:03Z",` +
				`"kind":"exchange","title":"claude","status":"failed",` +
				`"source":{"kind":"capture_run","displayName":"claude",` +
				`"recognition":"configured"},` +
				`"conversation":{"id":"capture_run:Run-1:main",` +
				`"displayName":"claude","kind":"main",` +
				`"evidence":"capture_run"},` +
				`"environment":{"id":"environment-1","revision":1,` +
				`"digest":"` + digest + `","clientEndpointId":"endpoint-1",` +
				`"clientEndpointRevision":1,"protocolPlanId":"plan-1",` +
				`"protocolPlanRevision":1,"routeId":"route-1","routeRevision":1},` +
				`"parentRefs":{"captureRunId":"Run-1",` +
				`"connectionId":"Connection-1",` +
				`"exchangeId":"Exchange-1"}}],` +
				`"nextCursor":"` + cursor + `"}`,
		))
	})

	page, err := client.activities(context.Background(), cursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != "Exchange-1" ||
		page.Items[0].OccurredAt != time.Date(
			2026, time.August, 3, 1, 2, 3, 0, time.UTC,
		) || page.NextCursor != cursor {
		t.Fatalf("Activity page = %+v", page)
	}
}

func TestControlActivitiesRejectsInvalidWireShape(t *testing.T) {
	digest := strings.Repeat("a", 64)
	validItem := `{"id":"Exchange-1",` +
		`"occurredAt":"2026-08-03T01:02:03Z",` +
		`"kind":"exchange","title":"claude","status":"failed",` +
		`"source":{"kind":"capture_run","displayName":"claude",` +
		`"recognition":"configured"},` +
		`"conversation":{"id":"capture_run:Run-1:main",` +
		`"displayName":"claude","kind":"main",` +
		`"evidence":"capture_run"},` +
		`"environment":{"id":"environment-1","revision":1,` +
		`"digest":"` + digest + `","clientEndpointId":"endpoint-1",` +
		`"clientEndpointRevision":1,"protocolPlanId":"plan-1",` +
		`"protocolPlanRevision":1,"routeId":"route-1","routeRevision":1},` +
		`"parentRefs":{"captureRunId":"Run-1",` +
		`"connectionId":"Connection-1",` +
		`"exchangeId":"Exchange-1"}}`
	for _, test := range []struct {
		name    string
		payload string
	}{
		{name: "missing items", payload: `{}`},
		{name: "null items", payload: `{"items":null}`},
		{
			name:    "extra page field",
			payload: `{"items":[` + validItem + `],"raw":"secret"}`,
		},
		{
			name: "extra summary field",
			payload: `{"items":[` +
				strings.TrimSuffix(validItem, "}") + `,"raw":"secret"}]}`,
		},
		{
			name: "invalid Environment ID",
			payload: `{"items":[` + strings.Replace(
				validItem,
				`"environment-1"`,
				`" environment-1"`,
				1,
			) + `]}`,
		},
		{
			name: "empty status",
			payload: `{"items":[` + strings.Replace(
				validItem,
				`"failed"`,
				`""`,
				1,
			) + `]}`,
		},
		{
			name: "unknown status",
			payload: `{"items":[` + strings.Replace(
				validItem,
				`"failed"`,
				`"unknown"`,
				1,
			) + `]}`,
		},
		{
			name:    "null next cursor",
			payload: `{"items":[` + validItem + `],"nextCursor":null}`,
		},
		{
			name:    "noncanonical next cursor",
			payload: `{"items":[` + validItem + `],"nextCursor":"AB"}`,
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			client := testControlClient(t, func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(test.payload))
			})
			if _, err := client.activities(context.Background(), ""); err == nil {
				t.Fatal("invalid Activity response was accepted")
			}
		})
	}
}

func testControlClient(
	t *testing.T,
	handler http.HandlerFunc,
) *controlClient {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/v1/captures" {
			if limit, _ := strconv.Atoi(request.URL.Query().Get("limit")); limit > 199 {
				writer.Header().Set("Content-Type", "application/problem+json")
				writer.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = writer.Write([]byte(`{"type":"urn:vibermate:error:invalid-control-request","title":"Unprocessable Entity","status":422,"code":"invalid_control_request"}`))
				return
			}
		}
		handler(writer, request)
	}))
	t.Cleanup(server.Close)
	client, err := newControlClient(desktopbootstrap.Session{
		BaseURL:    server.URL,
		ReadToken:  "read-token",
		WriteToken: "write-token",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.client.Close)
	return client
}

// A request above the actual Capture API maximum breaks even an empty catalog.
func TestControlCapturesEmptyCatalogUsesLegalDefault(t *testing.T) {
	client := testControlClient(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/api/v1/captures" || request.URL.RawQuery != "" {
			t.Error("initial Capture request must use the API default")
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"items":[]}`))
	})
	got, err := client.captures(context.Background())
	if err != nil || len(got.Items) != 0 || got.NextCursor != "" {
		t.Fatal("clean empty Capture catalog did not succeed")
	}
}

func captureFixtureItem(kind captureidentity.Kind, id string) desktopcontrol.CaptureResponse {
	when := time.Date(2026, 10, 9, 1, 2, 3, 0, time.UTC)
	item := desktopcontrol.CaptureResponse{
		Key: string(kind) + ":" + id, ID: id, Kind: kind,
		DisplayName: "fixture evidence", State: "running", Observation: "observed",
		CreatedAt: when, UpdatedAt: when, ActivityAt: when, Transport: "loopback",
	}
	if kind == captureidentity.KindManagedRun {
		item.ManagedRun = &desktopcontrol.ManagedRunResponse{
			ExecutableLabel: "synthetic", CWD: "/fixture/work", CanonicalExecutablePath: "/fixture/client",
			HomeDirectory: "/fixture/home", Recognition: "configured", ExpiresAt: when.Add(time.Hour),
			ProcessID: 123, WorkspaceID: "fixture-workspace", FirstObservedAt: &when,
		}
	} else {
		item.ManualCapture = &desktopcontrol.ManualCaptureResponse{CredentialRevision: 7, ExpiresAt: &when, LastObservedAt: &when}
	}
	return item
}

func writeCaptureFixturePage(t *testing.T, writer http.ResponseWriter, page desktopcontrol.CaptureListResponse) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(page); err != nil {
		t.Error("failed to encode fixture page")
	}
}

// Ignoring continuation, deduplicating raw IDs, or losing nested evidence fails.
func TestControlCapturesCollectsCompleteTypedCatalog(t *testing.T) {
	want := []desktopcontrol.CaptureResponse{
		captureFixtureItem(captureidentity.KindManagedRun, "shared"),
		captureFixtureItem(captureidentity.KindManualCapture, "shared"),
	}
	for index := 2; index < 250; index++ {
		want = append(want, captureFixtureItem(captureidentity.KindManagedRun, fmt.Sprintf("run-%03d", index)))
	}
	cursors := []string{"", "opaque+page/2?x=a&b=%#", "page:3", "page:4", "page:5"}
	wantQueries := []string{"", "cursor=opaque%2Bpage%2F2%3Fx%3Da%26b%3D%25%23", "cursor=page%3A3", "cursor=page%3A4", "cursor=page%3A5"}
	requests := 0
	client := testControlClient(t, func(writer http.ResponseWriter, request *http.Request) {
		index := requests
		requests++
		if index >= len(cursors) {
			t.Error("unexpected extra page request")
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		if request.URL.RawQuery != wantQueries[index] || request.URL.Query().Get("cursor") != cursors[index] {
			t.Error("opaque cursor was not relayed with query escaping")
		}
		next := ""
		if index+1 < len(cursors) {
			next = cursors[index+1]
		}
		writeCaptureFixturePage(t, writer, desktopcontrol.CaptureListResponse{Items: want[index*50 : (index+1)*50], NextCursor: next})
	})
	got, err := client.captures(context.Background())
	if err != nil || !reflect.DeepEqual(got.Items, want) || got.NextCursor != "" || requests != 5 {
		t.Fatal("Capture snapshot did not preserve the complete ordered typed catalog")
	}
}

func TestControlCapturesFailsClosedOnInvalidEvidence(t *testing.T) {
	valid := captureFixtureItem(captureidentity.KindManagedRun, "sentinel-capture")
	oversized := make([]desktopcontrol.CaptureResponse, 51)
	for index := range oversized {
		oversized[index] = captureFixtureItem(captureidentity.KindManagedRun, fmt.Sprintf("run-%d", index))
	}
	for _, test := range []struct {
		name        string
		pages       []desktopcontrol.CaptureListResponse
		laterStatus int
		raw         string
	}{
		{name: "later HTTP failure", pages: []desktopcontrol.CaptureListResponse{{Items: []desktopcontrol.CaptureResponse{valid}, NextCursor: "next"}}, laterStatus: 422},
		{name: "later JSON failure", pages: []desktopcontrol.CaptureListResponse{{Items: []desktopcontrol.CaptureResponse{valid}, NextCursor: "next"}}, raw: `{"items":`},
		{name: "duplicate key", pages: []desktopcontrol.CaptureListResponse{{Items: []desktopcontrol.CaptureResponse{valid}, NextCursor: "next"}, {Items: []desktopcontrol.CaptureResponse{valid}}}},
		{name: "duplicate key within page", pages: []desktopcontrol.CaptureListResponse{{Items: []desktopcontrol.CaptureResponse{valid, valid}}}},
		{name: "repeated cursor", pages: []desktopcontrol.CaptureListResponse{{Items: []desktopcontrol.CaptureResponse{valid}, NextCursor: "sentinel-cursor"}, {Items: []desktopcontrol.CaptureResponse{captureFixtureItem(captureidentity.KindManagedRun, "second")}, NextCursor: "sentinel-cursor"}}},
		{name: "cyclic cursor", pages: []desktopcontrol.CaptureListResponse{{Items: []desktopcontrol.CaptureResponse{valid}, NextCursor: "one"}, {Items: []desktopcontrol.CaptureResponse{captureFixtureItem(captureidentity.KindManagedRun, "second")}, NextCursor: "two"}, {Items: []desktopcontrol.CaptureResponse{captureFixtureItem(captureidentity.KindManagedRun, "third")}, NextCursor: "one"}}},
		{name: "empty nonterminal", pages: []desktopcontrol.CaptureListResponse{{Items: []desktopcontrol.CaptureResponse{}, NextCursor: "next"}}},
		{name: "oversized cursor", pages: []desktopcontrol.CaptureListResponse{{Items: []desktopcontrol.CaptureResponse{valid}, NextCursor: strings.Repeat("x", 513)}}},
		{name: "control cursor", pages: []desktopcontrol.CaptureListResponse{{Items: []desktopcontrol.CaptureResponse{valid}, NextCursor: "next\n"}}},
		{name: "whitespace cursor", pages: []desktopcontrol.CaptureListResponse{{Items: []desktopcontrol.CaptureResponse{valid}, NextCursor: "next page"}}},
		{name: "missing items", raw: `{}`},
		{name: "null items", raw: `{"items":null}`},
		{name: "null cursor", raw: `{"items":[],"nextCursor":null}`},
		{name: "nonstring cursor", raw: `{"items":[],"nextCursor":123}`},
		{name: "invalid UTF8 cursor", raw: "{\"items\":[],\"nextCursor\":\"" + string([]byte{0xff}) + "\"}"},
		{name: "malformed page", raw: `{"items":`},
		{name: "unknown page field", raw: `{"items":[],"sentinel-capture":true}`},
		{name: "unknown item field", raw: `{"items":[{"key":"managed_run:sentinel-capture","id":"sentinel-capture","kind":"managed_run","sentinel-capture":true}]}`},
		{name: "invalid key", pages: []desktopcontrol.CaptureListResponse{{Items: []desktopcontrol.CaptureResponse{{Key: "sentinel-capture", ID: "sentinel-capture", Kind: captureidentity.KindManagedRun}}}}},
		{name: "wrong ID", pages: []desktopcontrol.CaptureListResponse{{Items: []desktopcontrol.CaptureResponse{{Key: "managed_run:sentinel-capture", ID: "other", Kind: captureidentity.KindManagedRun}}}}},
		{name: "wrong kind", pages: []desktopcontrol.CaptureListResponse{{Items: []desktopcontrol.CaptureResponse{{Key: "managed_run:sentinel-capture", ID: "sentinel-capture", Kind: captureidentity.KindManualCapture}}}}},
		{name: "oversized page", pages: []desktopcontrol.CaptureListResponse{{Items: oversized}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			requests := 0
			client := testControlClient(t, func(writer http.ResponseWriter, _ *http.Request) {
				index := requests
				requests++
				if index < len(test.pages) {
					writeCaptureFixturePage(t, writer, test.pages[index])
					return
				}
				if test.laterStatus != 0 {
					writer.Header().Set("Content-Type", "application/problem+json")
					writer.WriteHeader(test.laterStatus)
					_, _ = writer.Write([]byte(`{"type":"urn:vibermate:error:invalid-control-request","title":"Unprocessable Entity","status":422,"code":"invalid_control_request"}`))
					return
				}
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(test.raw))
			})
			got, err := client.captures(context.Background())
			if err == nil || len(got.Items) != 0 || got.NextCursor != "" {
				t.Fatal("invalid evidence returned success or a partial snapshot")
			}
			if strings.Contains(err.Error(), "sentinel-capture") || strings.Contains(err.Error(), "sentinel-cursor") {
				t.Fatal("error disclosed Capture evidence")
			}
		})
	}
}

func TestControlCapturesRelaysCursorAtTransportBound(t *testing.T) {
	cursor := strings.Repeat("é", 256) // 512 UTF-8 bytes; opaque to the client.
	requests := 0
	client := testControlClient(t, func(writer http.ResponseWriter, request *http.Request) {
		requests++
		if requests == 1 {
			writeCaptureFixturePage(t, writer, desktopcontrol.CaptureListResponse{Items: []desktopcontrol.CaptureResponse{captureFixtureItem(captureidentity.KindManagedRun, "first")}, NextCursor: cursor})
			return
		}
		if request.URL.Query().Get("cursor") != cursor {
			t.Error("maximum transport cursor was not preserved")
		}
		writeCaptureFixturePage(t, writer, desktopcontrol.CaptureListResponse{Items: []desktopcontrol.CaptureResponse{}})
	})
	got, err := client.captures(context.Background())
	if err != nil || len(got.Items) != 1 || got.NextCursor != "" || requests != 2 {
		t.Fatal("legal maximum opaque cursor failed")
	}
}

func TestControlCapturesCancellationReturnsNoPartialEvidence(t *testing.T) {
	for _, later := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if !later {
			cancel()
		}
		client := testControlClient(t, func(writer http.ResponseWriter, _ *http.Request) {
			cancel()
			writeCaptureFixturePage(t, writer, desktopcontrol.CaptureListResponse{Items: []desktopcontrol.CaptureResponse{captureFixtureItem(captureidentity.KindManagedRun, "first")}, NextCursor: "next"})
		})
		got, err := client.captures(ctx)
		if !errors.Is(err, context.Canceled) || len(got.Items) != 0 || got.NextCursor != "" {
			t.Fatal("cancellation did not fail without partial evidence")
		}
	}
}

func TestControlCapturesFixturePageBound(t *testing.T) {
	for _, terminal := range []bool{true, false} {
		requests := 0
		client := testControlClient(t, func(writer http.ResponseWriter, _ *http.Request) {
			requests++
			next := fmt.Sprintf("page-%d", requests+1)
			if terminal && requests == 32 {
				next = ""
			}
			writeCaptureFixturePage(t, writer, desktopcontrol.CaptureListResponse{Items: []desktopcontrol.CaptureResponse{captureFixtureItem(captureidentity.KindManagedRun, fmt.Sprintf("run-%d", requests))}, NextCursor: next})
		})
		got, err := client.captures(context.Background())
		if terminal {
			if err != nil || len(got.Items) != 32 || requests != 32 || got.NextCursor != "" {
				t.Fatal("terminal page 32 did not succeed")
			}
		} else if err == nil || !strings.Contains(err.Error(), "incomplete") || len(got.Items) != 0 || got.NextCursor != "" || requests != 32 {
			t.Fatal("nonterminal page 32 did not fail with incomplete evidence")
		}
	}
}

func TestAssemblyEnvironmentKeepsClientAndProviderIdentityExact(t *testing.T) {
	t.Parallel()
	for _, clientID := range []acceptanceClientID{acceptanceClientClaudeCode, acceptanceClientCodexCLI} {
		configured := config{clientID: clientID, environmentID: "assembly-001"}
		aggregate, err := assemblyEnvironment(configured, 1, nil)
		if err != nil {
			t.Fatal(err)
		}
		client, err := selectedAcceptanceClient(configured)
		if err != nil {
			t.Fatal(err)
		}
		if aggregate.ID.String() != configured.environmentID || aggregate.Revision != 1 || len(aggregate.ClientEndpoints) != 1 {
			t.Fatalf("Environment = %+v", aggregate)
		}
		if aggregate.ContentRecording != environment.DefaultContentRecordingPolicy() {
			t.Fatalf("content recording = %+v", aggregate.ContentRecording)
		}
		endpoint := aggregate.ClientEndpoints[0]
		if endpoint.ClientOrigin.String() != client.ClientOrigin || len(endpoint.ProtocolPlans) != 1 || endpoint.ProtocolPlans[0].ClientProtocol != client.ClientProtocol {
			t.Fatalf("client edge = %+v", endpoint)
		}
		plan := endpoint.ProtocolPlans[0]
		if plan.Destination.Kind != environment.DestinationKindOriginal ||
			plan.Destination.Upstream != nil {
			t.Fatalf("original Destination = %+v", plan.Destination)
		}
	}
}

func TestPublishInitialEnvironmentSendsCompleteDraftPolicy(t *testing.T) {
	t.Parallel()
	configured := config{
		clientID:      acceptanceClientClaudeCode,
		environmentID: "assembly-001",
	}
	observed := false
	client := testControlClient(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPut ||
			request.URL.Path != "/api/v1/environments/assembly-001/draft" {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		var input desktopcontrol.EnvironmentDraftInput
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Errorf("decode draft input: %v", err)
		}
		if input.ContentRecording != environment.DefaultContentRecordingPolicy() {
			t.Errorf("content recording = %+v", input.ContentRecording)
		}
		observed = true
		writer.Header().Set("Content-Type", "application/problem+json")
		writer.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = writer.Write([]byte(
			`{"type":"urn:vibermate:error:invalid-control-request",` +
				`"title":"Unprocessable Entity","status":422,` +
				`"code":"invalid_control_request"}`,
		))
	})

	_, status, problem, err := client.publishInitialEnvironment(
		context.Background(),
		configured,
		0,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !observed || status != http.StatusUnprocessableEntity ||
		problem.ReasonCode != "invalid_control_request" {
		t.Fatalf("observed=%t status=%d problem=%+v", observed, status, problem)
	}
}

func TestAcceptanceConnectionRuleSetAllowsOnlyTheFixedClientOrigin(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		id   acceptanceClientID
		host string
	}{
		{
			name: "Claude",
			id:   acceptanceClientClaudeCode,
			host: "api.anthropic.com",
		},
		{
			name: "Codex",
			id:   acceptanceClientCodexCLI,
			host: "api.openai.com",
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			input, err := acceptanceConnectionRuleSet(
				config{clientID: test.id},
				desktopcontrol.ConnectionRuleSetResponse{
					Revision: 1,
					Rules:    []desktopcontrol.ConnectionRuleInput{},
					Mode:     "monitor",
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			if len(input.Rules) != 1 {
				t.Fatalf("rules = %+v", input.Rules)
			}
			rule := input.Rules[0]
			if rule.ID != acceptanceConnectionRuleID ||
				rule.Priority != 100 ||
				rule.Decision != "allow" ||
				rule.Match != "exact_host_port" ||
				rule.Host != test.host ||
				rule.Port != 443 {
				t.Fatalf("rule = %+v", rule)
			}
			if input.Mode != "ask_unknown" {
				t.Fatalf("mode = %q", input.Mode)
			}
		})
	}
}

func TestAcceptanceConnectionRuleSetRefusesPreauthorizedInput(t *testing.T) {
	t.Parallel()

	_, err := acceptanceConnectionRuleSet(
		config{clientID: acceptanceClientClaudeCode},
		desktopcontrol.ConnectionRuleSetResponse{
			Revision: 2,
			Rules: []desktopcontrol.ConnectionRuleInput{{
				ID:       "unexpected.allow",
				Priority: 100,
				Decision: "allow",
				Match:    "exact_host_port",
				Host:     "api.anthropic.com",
				Port:     443,
			}},
			Mode: "monitor",
		},
	)
	if err == nil {
		t.Fatal("preauthorized connection rules were accepted")
	}
}

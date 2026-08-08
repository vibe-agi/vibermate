package main

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
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
				strings.TrimSuffix(validItem, "}") + `,"reasonCode":"secret"}]}`,
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
	server := httptest.NewServer(handler)
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
		endpoint := aggregate.ClientEndpoints[0]
		if endpoint.ClientOrigin.String() != client.ClientOrigin || len(endpoint.ProtocolPlans) != 1 || endpoint.ProtocolPlans[0].ClientProtocol != client.ClientProtocol {
			t.Fatalf("client edge = %+v", endpoint)
		}
		plan := endpoint.ProtocolPlans[0]
		route := plan.UpstreamPlan.Routes[0]
		if plan.Mode != environment.PlanModeOriginalPassthrough || route.ProviderTarget.Origin.String() != client.ClientOrigin || route.AccountPolicy.Mode != environment.AccountModeClientPassthrough {
			t.Fatalf("original passthrough route = %+v", route)
		}
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

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/accessapply"
	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/desktopbootstrap"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
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

func TestValidateAccessApplyResponseRequiresClosedApplicationState(t *testing.T) {
	activeHash := strings.Repeat("ab", 32)
	tests := []struct {
		name      string
		response  desktopcontrol.AccessApplyResponse
		wantError bool
	}{
		{
			name: "active",
			response: desktopcontrol.AccessApplyResponse{
				Outcome:          access.WriteOutcomeCommitted,
				Revision:         1,
				ApplicationState: desktopcontrol.AccessApplicationStateActive,
				PlanHash:         activeHash,
			},
		},
		{
			name: "unavailable",
			response: desktopcontrol.AccessApplyResponse{
				Outcome:          access.WriteOutcomeCommitted,
				Revision:         1,
				ApplicationState: desktopcontrol.AccessApplicationStateUnavailable,
			},
		},
		{
			name: "missing application state",
			response: desktopcontrol.AccessApplyResponse{
				Outcome:  access.WriteOutcomeCommitted,
				Revision: 1,
				PlanHash: activeHash,
			},
			wantError: true,
		},
		{
			name: "active without hash",
			response: desktopcontrol.AccessApplyResponse{
				Outcome:          access.WriteOutcomeCommitted,
				Revision:         1,
				ApplicationState: desktopcontrol.AccessApplicationStateActive,
			},
			wantError: true,
		},
		{
			name: "active with noncanonical hash",
			response: desktopcontrol.AccessApplyResponse{
				Outcome:          access.WriteOutcomeCommitted,
				Revision:         1,
				ApplicationState: desktopcontrol.AccessApplicationStateActive,
				PlanHash:         strings.ToUpper(activeHash),
			},
			wantError: true,
		},
		{
			name: "unavailable with hash",
			response: desktopcontrol.AccessApplyResponse{
				Outcome:          access.WriteOutcomeCommitted,
				Revision:         1,
				ApplicationState: desktopcontrol.AccessApplicationStateUnavailable,
				PlanHash:         activeHash,
			},
			wantError: true,
		},
		{
			name: "noncommitted",
			response: desktopcontrol.AccessApplyResponse{
				Outcome:          access.WriteOutcomeNotCommitted,
				Revision:         1,
				ApplicationState: desktopcontrol.AccessApplicationStateUnavailable,
			},
			wantError: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateAccessApplyResponse(test.response)
			if (err != nil) != test.wantError {
				t.Fatalf("response=%+v error=%v", test.response, err)
			}
		})
	}
}

func TestDecodeAccessApplyResponseRequiresConditionalExactFields(t *testing.T) {
	activeHash := strings.Repeat("ab", 32)
	tests := []struct {
		name              string
		payload           string
		wantState         desktopcontrol.AccessApplicationState
		wantPlanHash      string
		wantDecodeFailure bool
	}{
		{
			name: "active exact fields",
			payload: `{"outcome":"committed","revision":1,` +
				`"applicationState":"active","planHash":"` + activeHash + `"}`,
			wantState:    desktopcontrol.AccessApplicationStateActive,
			wantPlanHash: activeHash,
		},
		{
			name: "unavailable exact fields",
			payload: `{"outcome":"committed","revision":1,` +
				`"applicationState":"unavailable"}`,
			wantState: desktopcontrol.AccessApplicationStateUnavailable,
		},
		{
			name: "unavailable explicit empty hash",
			payload: `{"outcome":"committed","revision":1,` +
				`"applicationState":"unavailable","planHash":""}`,
			wantDecodeFailure: true,
		},
		{
			name: "unavailable extra field",
			payload: `{"outcome":"committed","revision":1,` +
				`"applicationState":"unavailable","detail":"must-not-pass"}`,
			wantDecodeFailure: true,
		},
		{
			name: "active missing hash",
			payload: `{"outcome":"committed","revision":1,` +
				`"applicationState":"active"}`,
			wantDecodeFailure: true,
		},
		{
			name: "active extra field",
			payload: `{"outcome":"committed","revision":1,` +
				`"applicationState":"active","planHash":"` + activeHash +
				`","detail":"must-not-pass"}`,
			wantDecodeFailure: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := decodeAccessApplyResponse([]byte(test.payload))
			if (err != nil) != test.wantDecodeFailure {
				t.Fatalf("payload=%s result=%+v error=%v", test.payload, result, err)
			}
			if err == nil &&
				(result.ApplicationState != test.wantState ||
					result.PlanHash != test.wantPlanHash) {
				t.Fatalf("decoded response = %+v", result)
			}
		})
	}
}

func TestAccessApplyReplaysAndRetainsExactAmbiguousCommand(t *testing.T) {
	type observedCommand struct {
		body []byte
		key  string
	}
	commands := make([]observedCommand, 0, 4)
	client := testControlClient(t, func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		commands = append(commands, observedCommand{
			body: append([]byte(nil), body...),
			key:  request.Header.Get("Idempotency-Key"),
		})
		writer.Header().Set("Content-Type", "application/json")
		if len(commands) <= 2 {
			_, _ = writer.Write([]byte(
				`{"outcome":"committed","revision":1,` +
					`"applicationState":"active","planHash":"` +
					strings.Repeat("ab", 32) + `","extra":"not-closed"}`,
			))
			return
		}
		_, _ = writer.Write([]byte(
			`{"outcome":"committed","revision":1,` +
				`"applicationState":"active","planHash":"` +
				strings.Repeat("ab", 32) + `"}`,
		))
	})
	input := config{
		clientID:       acceptanceClientClaudeCode,
		accessID:       "work",
		providerOrigin: "https://api.openai.com/v1",
		providerModel:  "acceptance-model",
		secretRef:      "secret://provider/acceptance",
	}

	if _, _, _, err := client.applyAccess(context.Background(), input, 0); err == nil {
		t.Fatal("two invalid success receipts were accepted")
	}
	result, status, _, err := client.applyAccess(context.Background(), input, 0)
	if err != nil || status != http.StatusOK || result.Revision != 1 {
		t.Fatalf("replayed result=%+v status=%d error=%v", result, status, err)
	}
	if len(commands) != 3 {
		t.Fatalf("command count = %d, want 3", len(commands))
	}
	for index, command := range commands {
		if command.key == "" || command.key != commands[0].key {
			t.Fatalf("command %d key = %q, first = %q", index, command.key, commands[0].key)
		}
		if string(command.body) != string(commands[0].body) {
			t.Fatalf("command %d body differs from the retained command", index)
		}
	}

	if _, _, _, err := client.applyAccess(context.Background(), input, 0); err != nil {
		t.Fatal(err)
	}
	if len(commands) != 4 || commands[3].key == commands[0].key {
		t.Fatalf("settled command keys = %#v", commands)
	}
}

func TestAccessApplyPreCanceledContextDoesNotSendOrAllocate(t *testing.T) {
	type observedCommand struct {
		body string
		key  string
	}
	commands := make(chan observedCommand, 1)
	client := testControlClient(t, func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		commands <- observedCommand{
			body: string(body),
			key:  request.Header.Get("Idempotency-Key"),
		}
		writeAccessApplySuccess(t, writer, http.StatusOK)
	})
	input := acceptanceAccessConfig()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, _, _, err := client.applyAccess(ctx, input, 0); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("pre-canceled apply error = %v", err)
	}
	select {
	case command := <-commands:
		t.Fatalf("pre-canceled apply sent command %+v", command)
	default:
	}
	client.accessMu.Lock()
	unresolved := len(client.unresolvedAccessMutations)
	client.accessMu.Unlock()
	if unresolved != 0 {
		t.Fatalf(
			"unresolved command count = %d, want 0",
			unresolved,
		)
	}

	result, status, _, err := client.applyAccess(context.Background(), input, 0)
	if err != nil || status != http.StatusOK || result.Revision != 1 {
		t.Fatalf("explicit retry result=%+v status=%d error=%v", result, status, err)
	}
	command := <-commands
	if command.key == "" || command.body == "" {
		t.Fatalf("fresh command = %+v", command)
	}
	client.accessMu.Lock()
	unresolved = len(client.unresolvedAccessMutations)
	client.accessMu.Unlock()
	if unresolved != 0 {
		t.Fatalf("settled unresolved command count = %d, want 0", unresolved)
	}
}

func TestAccessApplyTreatsClosedProblemAsAuthoritative(t *testing.T) {
	type observedCommand struct {
		body string
		key  string
	}
	commands := make(chan observedCommand, 2)
	var attempts atomic.Int32
	client := testControlClient(t, func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		commands <- observedCommand{
			body: string(body),
			key:  request.Header.Get("Idempotency-Key"),
		}
		if attempts.Add(1) == 1 {
			writer.Header().Set("Content-Type", "application/problem+json")
			writer.WriteHeader(http.StatusConflict)
			_, _ = writer.Write([]byte(
				`{"type":"urn:vibermate:error:revision-conflict",` +
					`"title":"Conflict","status":409,` +
					`"code":"revision_conflict"}`,
			))
			return
		}
		writeAccessApplySuccess(t, writer, http.StatusOK)
	})
	input := acceptanceAccessConfig()

	_, status, problem, err := client.applyAccess(context.Background(), input, 0)
	if err != nil || status != http.StatusConflict ||
		problem.ReasonCode != "revision_conflict" {
		t.Fatalf("authoritative response status=%d problem=%+v error=%v", status, problem, err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("authoritative Problem attempt count = %d, want 1", attempts.Load())
	}
	result, status, _, err := client.applyAccess(context.Background(), input, 0)
	if err != nil || status != http.StatusOK || result.Revision != 1 {
		t.Fatalf("next command result=%+v status=%d error=%v", result, status, err)
	}
	first, second := <-commands, <-commands
	if first.key == "" || second.key == "" || first.key == second.key ||
		first.body != second.body {
		t.Fatalf("authoritative command keys/bodies = %#v, %#v", first, second)
	}
}

func TestAccessApplyReplaysUnexpectedSuccessStatusWithSameKey(t *testing.T) {
	type observedCommand struct {
		body string
		key  string
	}
	commands := make(chan observedCommand, 2)
	var attempts atomic.Int32
	client := testControlClient(t, func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		commands <- observedCommand{
			body: string(body),
			key:  request.Header.Get("Idempotency-Key"),
		}
		status := http.StatusOK
		if attempts.Add(1) == 1 {
			status = http.StatusCreated
		}
		writeAccessApplySuccess(t, writer, status)
	})

	result, status, _, err := client.applyAccess(
		context.Background(),
		acceptanceAccessConfig(),
		0,
	)
	if err != nil || status != http.StatusOK || result.Revision != 1 {
		t.Fatalf("replayed result=%+v status=%d error=%v", result, status, err)
	}
	first, second := <-commands, <-commands
	if attempts.Load() != 2 || first.key == "" || first.key != second.key ||
		first.body != second.body {
		t.Fatalf("replayed commands = %#v, %#v", first, second)
	}
}

func TestAccessApplyReplaysControlledDisconnectWithSameKey(t *testing.T) {
	type observedCommand struct {
		body string
		key  string
	}
	commands := make(chan observedCommand, 2)
	var attempts atomic.Int32
	client := testControlClient(t, func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		commands <- observedCommand{
			body: string(body),
			key:  request.Header.Get("Idempotency-Key"),
		}
		if attempts.Add(1) == 1 {
			hijacker, ok := writer.(http.Hijacker)
			if !ok {
				t.Error("test response writer cannot disconnect the transport")
				return
			}
			connection, _, err := hijacker.Hijack()
			if err != nil {
				t.Errorf("hijack first Access response: %v", err)
				return
			}
			if err := connection.Close(); err != nil {
				t.Errorf("close first Access response: %v", err)
			}
			return
		}
		writeAccessApplySuccess(t, writer, http.StatusOK)
	})

	result, status, _, err := client.applyAccess(
		context.Background(),
		acceptanceAccessConfig(),
		0,
	)
	if err != nil || status != http.StatusOK || result.Revision != 1 {
		t.Fatalf("disconnect replay result=%+v status=%d error=%v", result, status, err)
	}
	first, second := <-commands, <-commands
	if attempts.Load() != 2 || first.key == "" || first.key != second.key ||
		first.body != second.body {
		t.Fatalf("disconnect replay commands = %#v, %#v", first, second)
	}
}

func acceptanceAccessConfig() config {
	return config{
		clientID:       acceptanceClientClaudeCode,
		accessID:       "work",
		providerOrigin: "https://api.openai.com/v1",
		providerModel:  "acceptance-model",
		secretRef:      "secret://provider/acceptance",
	}
}

func writeAccessApplySuccess(t *testing.T, writer http.ResponseWriter, status int) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	if _, err := writer.Write([]byte(
		`{"outcome":"committed","revision":1,` +
			`"applicationState":"active","planHash":"` +
			strings.Repeat("ab", 32) + `"}`,
	)); err != nil {
		t.Errorf("write Access apply success: %v", err)
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
		_, _ = writer.Write([]byte(
			`{"items":[{"id":"Exchange-1",` +
				`"occurredAt":"2026-08-03T01:02:03Z",` +
				`"accessId":"Access-1","status":"failed"}],` +
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
	validItem := `{"id":"Exchange-1",` +
		`"occurredAt":"2026-08-03T01:02:03Z",` +
		`"accessId":"Access-1","status":"failed"}`
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
			name: "invalid Access ID",
			payload: `{"items":[` + strings.Replace(
				validItem,
				`"Access-1"`,
				`" Access-1"`,
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

func TestAssemblyAccessKeepsClientAndProviderIdentitySeparate(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		client  acceptanceClientID
		origin  string
		dialect access.Dialect
	}{
		{
			name:    "Claude",
			client:  acceptanceClientClaudeCode,
			origin:  "https://api.anthropic.com",
			dialect: access.DialectAnthropicMessages,
		},
		{
			name:    "Codex",
			client:  acceptanceClientCodexCLI,
			origin:  "https://api.openai.com",
			dialect: access.DialectOpenAIResponses,
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			assertAssemblyAccessClientEdge(
				t,
				test.client,
				test.origin,
				test.dialect,
			)
		})
	}
}

func assertAssemblyAccessClientEdge(
	t *testing.T,
	client acceptanceClientID,
	clientOrigin string,
	clientDialect access.Dialect,
) {
	t.Helper()

	config := config{
		clientID:       client,
		accessID:       "Acc-001",
		providerOrigin: "https://api.openai.com/v1",
		providerModel:  "fixed-provider-model",
		secretRef:      "secret://provider/acceptance",
	}
	input, err := assemblyAccess(config, 0)
	if err != nil {
		t.Fatal(err)
	}
	if input.AgentEndpoint.ClientOrigin != clientOrigin ||
		input.AgentEndpoint.ClientDialect != string(clientDialect) {
		t.Fatalf("client origin = %q", input.AgentEndpoint.ClientOrigin)
	}
	if len(input.ProviderTargets) != 1 ||
		input.ProviderTargets[0].Origin != config.providerOrigin ||
		input.ProviderTargets[0].Origin == input.AgentEndpoint.ClientOrigin {
		t.Fatalf(
			"provider targets = %+v",
			input.ProviderTargets,
		)
	}
	if len(input.AccountBindings) != 1 ||
		input.AccountBindings[0].SecretRef != config.secretRef {
		t.Fatalf("account bindings = %+v", input.AccountBindings)
	}
	if input.Access.ID != config.accessID ||
		input.AgentEndpoint.ID != "Acc-001-agent" ||
		input.Profiles[0].ID != "Acc-001-openai" ||
		input.AccountBindings[0].ID != "Acc-001-account" {
		t.Fatalf("derived identifiers = %+v", input)
	}
	command, err := accessapply.BuildCommand(config.accessID, input)
	if err != nil {
		t.Fatal(err)
	}
	if command.ExpectedRevision != 0 ||
		command.Aggregate.Binding.Revision != 1 {
		t.Fatalf("command = %+v", command)
	}
	payload, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), config.secretRef) ||
		strings.Contains(string(payload), `"secretValue"`) ||
		strings.Contains(string(payload), `"credential"`) {
		t.Fatal("Acceptance Access did not preserve the SecretRef-only boundary")
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

			fallback := desktopcontrol.ConnectionRuleInput{
				ID:       "default.ask",
				Decision: "ask",
				Match:    "any",
			}
			input, err := acceptanceConnectionRuleSet(
				config{clientID: test.id},
				desktopcontrol.ConnectionRuleSetResponse{
					Revision: 1,
					Rules:    []desktopcontrol.ConnectionRuleInput{},
					Default:  fallback,
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
			if input.Default != fallback {
				t.Fatalf("default changed: %+v", input.Default)
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
			Default: desktopcontrol.ConnectionRuleInput{
				ID:       "default.ask",
				Decision: "ask",
				Match:    "any",
			},
		},
	)
	if err == nil {
		t.Fatal("preauthorized connection rules were accepted")
	}
}

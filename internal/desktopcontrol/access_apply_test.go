package desktopcontrol

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/accessapply"
	"github.com/vibe-agi/vibermate/internal/activity"
)

func TestAccessApplyRejectsEveryNonEnabledStatusBeforeWrite(t *testing.T) {
	for _, status := range []string{"", "draft", "disabled", "Enabled"} {
		status := status
		t.Run(fmt.Sprintf("status_%q", status), func(t *testing.T) {
			writer := &applyWriter{}
			activities := &applyActivities{}
			handler := applyTestHandler(writer, activities)
			response := applyRequest(t, handler, map[string]any{
				"expectedRevision": 0,
				"access": map[string]any{
					"status": status,
				},
			})

			if response.Code != http.StatusUnprocessableEntity || writer.callCount() != 0 {
				t.Fatalf(
					"status=%q code=%d writes=%d body=%s",
					status,
					response.Code,
					writer.callCount(),
					response.Body.Bytes(),
				)
			}
			if events := activities.recorded(); len(events) != 0 {
				t.Fatalf("rejected apply recorded Activity: %+v", events)
			}
		})
	}
}

func TestAccessApplyUsesExactWriteReceiptWithoutResolvingCurrentPlan(t *testing.T) {
	receipt := access.PlanHash(sha256.Sum256([]byte("published candidate")))
	writer := &applyWriter{result: access.WriteResult{
		Outcome:  access.WriteOutcomeCommitted,
		Revision: 1,
		PlanHash: receipt,
	}}
	activities := &applyActivities{}
	handler := applyTestHandler(writer, activities)
	response := applyRequest(t, handler, validAccessApplyInput())

	if response.Code != http.StatusOK {
		t.Fatalf("apply code=%d body=%s", response.Code, response.Body.Bytes())
	}
	var result AccessApplyResponse
	decodeApplyResponse(t, response, &result)
	if result.Outcome != access.WriteOutcomeCommitted ||
		result.Revision != 1 ||
		result.ApplicationState != AccessApplicationStateActive ||
		result.PlanHash != receipt.String() {
		t.Fatalf("apply response = %+v", result)
	}
	events := activities.recorded()
	if len(events) != 1 ||
		events[0].Status != activity.StatusSucceeded ||
		events[0].ReasonCode != "" {
		t.Fatalf("apply Activity = %+v", events)
	}
}

func TestAccessApplyReportsCommittedProjectionFailureAsUnavailable(t *testing.T) {
	writer := &applyWriter{
		result: access.WriteResult{
			Outcome:  access.WriteOutcomeCommitted,
			Revision: 1,
			PlanHash: access.PlanHash(sha256.Sum256([]byte("must not escape"))),
		},
		err: fmt.Errorf("publish candidate: %w", access.ErrProjectionUnavailable),
	}
	activities := &applyActivities{}
	handler := applyTestHandler(writer, activities)
	response := applyRequest(t, handler, validAccessApplyInput())

	if response.Code != http.StatusOK {
		t.Fatalf("apply code=%d body=%s", response.Code, response.Body.Bytes())
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &wire); err != nil {
		t.Fatal(err)
	}
	if len(wire) != 3 || wire["planHash"] != nil {
		t.Fatalf("unavailable apply wire = %s", response.Body.Bytes())
	}
	var result AccessApplyResponse
	decodeApplyResponse(t, response, &result)
	if result.Outcome != access.WriteOutcomeCommitted ||
		result.Revision != 1 ||
		result.ApplicationState != AccessApplicationStateUnavailable ||
		result.PlanHash != "" {
		t.Fatalf("unavailable apply response = %+v", result)
	}
	events := activities.recorded()
	if len(events) != 1 ||
		events[0].Status != activity.StatusFailed ||
		events[0].ReasonCode != string(access.ReasonProjectionUnavailable) {
		t.Fatalf("unavailable apply Activity = %+v", events)
	}
}

func TestAccessApplyKeepsNoncommittedProjectionFailureAsProblem(t *testing.T) {
	writer := &applyWriter{
		result: access.WriteResult{Outcome: access.WriteOutcomeNotCommitted},
		err:    fmt.Errorf("pre-commit projection check: %w", access.ErrProjectionUnavailable),
	}
	activities := &applyActivities{}
	handler := applyTestHandler(writer, activities)
	response := applyRequest(t, handler, validAccessApplyInput())

	if response.Code == http.StatusOK {
		t.Fatalf("noncommitted failure was returned as success: %s", response.Body.Bytes())
	}
	if events := activities.recorded(); len(events) != 0 {
		t.Fatalf("noncommitted apply recorded Activity: %+v", events)
	}
}

type applyWriter struct {
	mu     sync.Mutex
	calls  int
	result access.WriteResult
	err    error
}

func (writer *applyWriter) WriteAccess(
	_ context.Context,
	_ access.WriteCommand,
) (access.WriteResult, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	writer.calls++
	return writer.result, writer.err
}

func (writer *applyWriter) callCount() int {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.calls
}

type applyPanicResolver struct{}

func (applyPanicResolver) ResolveAccess(
	access.AccessID,
) (access.AccessPlanSnapshot, error) {
	panic("Access apply resolved the mutable current projection")
}

type applyActivities struct {
	mu     sync.Mutex
	events []activity.Event
}

func (recorder *applyActivities) Record(
	_ context.Context,
	event activity.Event,
) (activity.Record, error) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.events = append(recorder.events, event)
	return activity.Record{}, nil
}

func (*applyActivities) GetExchange(
	context.Context,
	string,
) (activity.Record, error) {
	return activity.Record{}, activity.ErrExchangeNotFound
}

func (*applyActivities) List(
	context.Context,
	activity.PageRequest,
) (activity.Page, error) {
	return activity.Page{}, nil
}

func (*applyActivities) ListExchanges(
	context.Context,
	activity.PageRequest,
) (activity.Page, error) {
	return activity.Page{}, nil
}

func (*applyActivities) Shutdown(context.Context) error { return nil }

func (recorder *applyActivities) recorded() []activity.Event {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]activity.Event(nil), recorder.events...)
}

func applyTestHandler(
	writer access.Writer,
	activities activity.Runtime,
) *Handler {
	return &Handler{
		accesses:   writer,
		resolver:   applyPanicResolver{},
		activities: activities,
		idempotent: newIdempotencyCache(),
	}
}

func applyRequest(
	t *testing.T,
	handler *Handler,
	input any,
) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(
		http.MethodPut,
		"http://127.0.0.1/api/v1/accesses/access-control/actions/apply",
		bytes.NewReader(body),
	)
	request.SetPathValue("accessId", "access-control")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("If-Match", "0")
	request.Header.Set("Idempotency-Key", "access-apply-test-0001")
	response := httptest.NewRecorder()
	handler.applyAccess(response, request)
	return response
}

func decodeApplyResponse(
	t *testing.T,
	response *httptest.ResponseRecorder,
	result *AccessApplyResponse,
) {
	t.Helper()
	decoder := json.NewDecoder(response.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(result); err != nil {
		t.Fatal(err)
	}
}

func validAccessApplyInput() accessapply.Input {
	return accessapply.Input{
		ExpectedRevision: 0,
		Access: accessapply.AccessInput{
			ID:                "access-control",
			Name:              "Control Access",
			Description:       "Executable control Access",
			Status:            string(access.AccessStatusEnabled),
			AgentEndpointID:   "access-control-endpoint",
			DefaultRouteSetID: "access-control-routes",
			ProfileIDs:        []string{"access-control-profile"},
			EgressPolicyID:    "access-control-egress",
		},
		AgentEndpoint: accessapply.AgentEndpointInput{
			ID:            "access-control-endpoint",
			ClientOrigin:  "https://agent.example.test:443",
			ClientDialect: "anthropic-messages",
		},
		Profiles: []accessapply.ProfileInput{{
			ID:                     "access-control-profile",
			Name:                   "OpenAI Chat",
			Description:            "Fixed profile",
			BackendDialect:         "openai-chat",
			TargetID:               "access-control-target",
			UpstreamWireProfileRef: access.UpstreamWireProfileFollowClientValue,
			DefaultModelPolicy: accessapply.ModelPolicyInput{
				Mode:       "fixed",
				FixedModel: "gpt-4.1-mini",
			},
			AccountBindingIDs:       []string{"access-control-account"},
			DefaultAccountBindingID: "access-control-account",
		}},
		ProviderTargets: []accessapply.ProviderTargetInput{{
			ID:           "access-control-target",
			ProfileID:    "access-control-profile",
			Origin:       "https://api.openai.com:443/v1",
			Protocol:     "openai-chat",
			Capabilities: []string{"messages", "streaming", "tool_calls"},
		}},
		AccountBindings: []accessapply.AccountBindingInput{{
			ID:            "access-control-account",
			ProfileID:     "access-control-profile",
			Label:         "Primary",
			SecretRef:     "secret://provider/access-control",
			AuthDriverRef: "static_header",
			Enabled:       true,
		}},
		RouteSets: []accessapply.RouteSetInput{{
			ID:                  "access-control-routes",
			CandidateProfileIDs: []string{"access-control-profile"},
		}},
		EgressPolicy: accessapply.EgressPolicyInput{
			ID:   "access-control-egress",
			Mode: "direct",
		},
		PluginPlan: accessapply.PluginPlanInput{
			Mode:       "pass_through",
			BindingIDs: []string{},
		},
	}
}

package desktopcontrol_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/accessapply"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/workspaceroute"
)

func TestWorkspaceRouteProjectionGroupsRunsAndSwitchesWithCAS(t *testing.T) {
	t.Parallel()

	fixture := newAuditFixture(t)
	aggregate := workspaceRouteTestAggregate(t)
	write, err := fixture.runtime.AccessWriter().WriteAccess(
		context.Background(),
		access.WriteCommand{ExpectedRevision: 0, Aggregate: aggregate},
	)
	if err != nil || write.Outcome != access.WriteOutcomeCommitted {
		t.Fatalf("write Access result=%+v err=%v", write, err)
	}
	workspaceDirectory := t.TempDir()
	scope, err := fixture.runtime.WorkspaceIdentity().ResolveLocal(
		context.Background(),
		workspaceDirectory,
	)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := fixture.runtime.SnapshotResolver().ResolveAccess(
		aggregate.Binding.ID,
	)
	if err != nil {
		t.Fatal(err)
	}
	resolution, err := fixture.runtime.WorkspaceRoutes().Resolve(
		context.Background(),
		snapshot,
		scope,
	)
	if err != nil {
		t.Fatal(err)
	}
	resolution.Release()
	runGrant, err := fixture.runtime.CaptureRuns().Create(
		context.Background(),
		capturerun.CreateCommand{
			CWD:             workspaceDirectory,
			ExecutableLabel: "true",
			Lifetime:        time.Minute,
			CatalogRevision: 1,
			Workspace:       scope,
			LocalUserLabel:  "alice",
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	listed := doRequest(
		t,
		fixture.router,
		fixture.authority,
		http.MethodGet,
		"/api/v1/workspace-route-bindings",
		fixture.readToken,
		nil,
	)
	if listed.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listed.Code, listed.Body)
	}
	var page desktopcontrol.WorkspaceRouteBindingPage
	decodeResponse(t, listed, &page)
	if len(page.Items) != 1 ||
		page.Items[0].ID != resolution.BindingID.String() ||
		page.Items[0].ActiveRunCount != 1 ||
		len(page.Items[0].ActiveRuns) != 1 ||
		len(page.Items[0].ApprovedProfiles) != 2 ||
		page.Items[0].ApprovedProfiles[0].Kind != access.EndpointProfileManaged ||
		page.Items[0].ApprovedProfiles[1].Kind !=
			access.EndpointProfileOriginalPassthrough ||
		page.Items[0].ApprovedProfiles[1].AuthPresentation !=
			workspaceroute.AuthClient ||
		page.Items[0].ActiveRuns[0].LocalUserLabel != "alice" ||
		page.Items[0].WorkspaceLabel != scope.WorkspaceLabel() {
		t.Fatalf("workspace route page = %+v", page)
	}

	body, err := json.Marshal(desktopcontrol.WorkspaceRouteBindingUpdate{
		ProfileID: access.OriginalPassthroughProfileID().String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	restartRequired := workspaceRouteMutation(
		t,
		fixture,
		resolution.BindingID.String(),
		1,
		"workspace-route-update-0001",
		body,
	)
	if restartRequired.Code != http.StatusConflict ||
		!strings.Contains(
			restartRequired.Body.String(),
			string(desktopcontrol.ReasonCaptureRunRestartRequired),
		) {
		t.Fatalf(
			"cross-login update status=%d body=%s",
			restartRequired.Code,
			restartRequired.Body,
		)
	}
	if err := fixture.runtime.CaptureRuns().Finish(
		context.Background(),
		runGrant.Run.ID,
		runGrant.ControlCapability,
	); err != nil {
		t.Fatal(err)
	}
	updated := workspaceRouteMutation(
		t,
		fixture,
		resolution.BindingID.String(),
		1,
		"workspace-route-update-0002",
		body,
	)
	if updated.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", updated.Code, updated.Body)
	}
	var view desktopcontrol.WorkspaceRouteBindingResponse
	decodeResponse(t, updated, &view)
	if view.Revision != 2 ||
		view.ProfileID != access.OriginalPassthroughProfileID().String() {
		t.Fatalf("updated view = %+v", view)
	}
	stale := workspaceRouteMutation(
		t,
		fixture,
		resolution.BindingID.String(),
		1,
		"workspace-route-update-0003",
		body,
	)
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale update status=%d body=%s", stale.Code, stale.Body)
	}
}

func workspaceRouteTestAggregate(t *testing.T) access.Aggregate {
	t.Helper()
	const id = "access-workspace-ui"
	command, err := accessapply.BuildCommand(id, accessapply.Input{
		ExpectedRevision: 0,
		Access: accessapply.AccessInput{
			ID: id, Name: "Workspace UI", Description: "Workspace route fixture",
			Status: string(access.AccessStatusEnabled), AgentEndpointID: id + "-endpoint",
			DefaultRouteSetID: id + "-routes", ProfileIDs: []string{id + "-profile"},
			EgressPolicyID: id + "-egress",
		},
		AgentEndpoint: accessapply.AgentEndpointInput{
			ID: id + "-endpoint", ClientOrigin: "https://workspace-ui.example.test:443",
			ClientDialect: "anthropic-messages",
		},
		Profiles: []accessapply.ProfileInput{{
			ID: id + "-profile", Name: "Work account", Description: "Primary route",
			BackendDialect: "openai-chat", TargetID: id + "-target",
			UpstreamWireProfileRef:  access.UpstreamWireProfileFollowClientValue,
			DefaultModelPolicy:      accessapply.ModelPolicyInput{Mode: "fixed", FixedModel: "gpt-4.1-mini"},
			AccountBindingIDs:       []string{id + "-account"},
			DefaultAccountBindingID: id + "-account",
		}},
		ProviderTargets: []accessapply.ProviderTargetInput{{
			ID: id + "-target", ProfileID: id + "-profile",
			Origin: "https://api.openai.com:443/v1", Protocol: "openai-chat",
			Capabilities: []string{"messages", "streaming", "tool_calls"},
		}},
		AccountBindings: []accessapply.AccountBindingInput{{
			ID: id + "-account", ProfileID: id + "-profile", Label: "001",
			SecretRef: "secret://provider/" + id, AuthDriverRef: "static_header", Enabled: true,
		}},
		RouteSets: []accessapply.RouteSetInput{{
			ID: id + "-routes", CandidateProfileIDs: []string{id + "-profile"},
		}},
		EgressPolicy: accessapply.EgressPolicyInput{ID: id + "-egress", Mode: "direct"},
		PluginPlan:   accessapply.PluginPlanInput{Mode: "pass_through", BindingIDs: []string{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return command.Aggregate
}

func workspaceRouteMutation(
	t *testing.T,
	fixture *auditFixture,
	bindingID string,
	revision uint64,
	key string,
	body []byte,
) *httptest.ResponseRecorder {
	t.Helper()
	request := newRequest(
		http.MethodPatch,
		fixture.authority,
		"/api/v1/workspace-route-bindings/"+bindingID,
		fixture.writeToken,
		body,
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("If-Match", strconv.FormatUint(revision, 10))
	request.Header.Set("Idempotency-Key", key)
	recorder := httptest.NewRecorder()
	fixture.router.ServeHTTP(recorder, request)
	return recorder
}

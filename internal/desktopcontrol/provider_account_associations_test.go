package desktopcontrol_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

func TestAccountControlSeparatesCredentialsFromExplicitProfileLinks(t *testing.T) {
	t.Parallel()
	runtime := startRuntime(t)
	defer shutdownRuntime(t, runtime)
	application, err := desktopcontrol.New(desktopcontrol.Options{
		Readiness: readyState(true), Status: runtime,
		Environments: runtime.Environments(), Assignments: runtime.CaptureAssignments(),
		Activities: runtime.Activities(), Contents: runtime.ExchangeContents(), Connections: runtime.ConnectionEvents(),
		Egress: runtime.EgressAttempts(), Approvals: runtime.ToolApprovals(),
		Endpoints: runtime.UpstreamEndpoints(), Accounts: runtime.ProviderAccounts(), Offline: runtime,
		ManualCaptures: runtime.ManualCaptures(), Clock: desktopcontrol.SystemClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	created := environmentRequest(t, application, http.MethodPost, "/api/v1/provider-accounts", 0, "independent-account-create",
		[]byte(`{"id":"account.shared","displayName":"Shared","upstreamEndpointId":"target.claude.official","unlinked":true,"kind":"anthropic_api_key","secret":"synthetic-private-credential"}`))
	if created.Code != http.StatusCreated {
		t.Fatalf("create=%d %s", created.Code, created.Body)
	}
	var account desktopcontrol.ProviderAccountResponse
	if err := json.Unmarshal(created.Body.Bytes(), &account); err != nil {
		t.Fatal(err)
	}
	if len(account.LinkedEndpointIDs) != 0 || account.AssociationRevision != 1 {
		t.Fatalf("create granted implicit authority: %+v", account)
	}
	for _, profile := range []struct{ id, protocols string }{
		{"profile.second", `["anthropic_messages"]`},
		{"profile.wrong-driver", `["openai_responses"]`},
	} {
		response := environmentRequest(t, application, http.MethodPost, "/api/v1/upstream-endpoints", 0, "create-"+profile.id,
			[]byte(fmt.Sprintf(`{"id":%q,"displayName":"Profile","origin":"https://api.anthropic.com","backendProtocols":%s}`, profile.id, profile.protocols)))
		if response.Code != http.StatusCreated {
			t.Fatalf("profile=%d %s", response.Code, response.Body)
		}
	}
	for index, endpointID := range []string{upstreamendpoint.AnthropicOfficialID.String(), "profile.second"} {
		path := "/api/v1/provider-accounts/account.shared/associations/" + endpointID
		response := environmentRequest(t, application, http.MethodPut, path, uint64(index+1), "link-"+endpointID, []byte(`{"linked":true}`))
		if response.Code != http.StatusOK {
			t.Fatalf("link=%d %s", response.Code, response.Body)
		}
		if err := json.Unmarshal(response.Body.Bytes(), &account); err != nil {
			t.Fatal(err)
		}
		if account.Revision != 1 || account.CredentialEpoch != 1 || account.AssociationRevision != uint64(index+2) || len(account.LinkedEndpointIDs) != index+1 {
			t.Fatalf("link altered credentials: %+v", account)
		}
		if strings.Contains(response.Body.String(), "synthetic-private-credential") {
			t.Fatal("link response exposed secret")
		}
		replay := environmentRequest(t, application, http.MethodPut, path, uint64(index+1), "link-"+endpointID, []byte(`{"linked":true}`))
		if replay.Code != http.StatusOK || replay.Body.String() != response.Body.String() {
			t.Fatalf("idempotent replay=%d %s", replay.Code, replay.Body)
		}
	}
	for index, invalid := range []struct {
		endpoint string
		revision uint64
		status   int
	}{
		{"profile.second", 1, http.StatusConflict},
		{"target.openai.official", 3, http.StatusUnprocessableEntity},
		{"profile.wrong-driver", 3, http.StatusUnprocessableEntity},
	} {
		response := environmentRequest(t, application, http.MethodPut, "/api/v1/provider-accounts/account.shared/associations/"+invalid.endpoint, invalid.revision, fmt.Sprintf("invalid-link-attempt-%d", index), []byte(`{"linked":true}`))
		if response.Code != invalid.status {
			t.Fatalf("invalid link=%d want=%d %s", response.Code, invalid.status, response.Body)
		}
	}
	for index, endpointID := range []string{upstreamendpoint.AnthropicOfficialID.String(), "profile.second"} {
		response := environmentRequest(t, application, http.MethodPut, "/api/v1/provider-accounts/account.shared/associations/"+endpointID, uint64(index+3), "unlink-"+endpointID, []byte(`{"linked":false}`))
		if response.Code != http.StatusOK {
			t.Fatalf("unlink=%d %s", response.Code, response.Body)
		}
		if err := json.Unmarshal(response.Body.Bytes(), &account); err != nil {
			t.Fatal(err)
		}
		if len(account.LinkedEndpointIDs) != 1-index || account.CredentialEpoch != 1 {
			t.Fatalf("unlink changed another link/credential: %+v", account)
		}
	}
	view, err := runtime.ProviderAccounts().Get(context.Background(), "account.shared")
	if err != nil || view.Health.CredentialEpoch != 1 || len(view.Account.Associations.IDs()) != 0 {
		t.Fatalf("unlinked account missing=%+v %v", view, err)
	}
}

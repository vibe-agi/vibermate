package desktopcontrol

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/accessapply"
)

func TestAccessListReturnsStableMultipleDurableSummaries(t *testing.T) {
	catalog := accessViewCatalog{aggregates: []access.Aggregate{
		accessViewAggregate(t, "z-access", "Zulu", access.AccessStatusDisabled),
		accessViewAggregate(t, "a-access", "Alpha", access.AccessStatusEnabled),
	}}
	handler := &Handler{accessCatalog: catalog}
	response := httptest.NewRecorder()
	handler.listAccesses(
		response,
		httptest.NewRequest(http.MethodGet, "/api/v1/accesses", nil),
	)

	if response.Code != http.StatusOK {
		t.Fatalf("list code=%d body=%s", response.Code, response.Body.Bytes())
	}
	var wire struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	decodeClosedResponse(t, response, &wire)
	if len(wire.Items) != 2 ||
		string(wire.Items[0]["accessId"]) != `"a-access"` ||
		string(wire.Items[1]["accessId"]) != `"z-access"` {
		t.Fatalf("Access list is not stable: %s", response.Body.Bytes())
	}
	for _, item := range wire.Items {
		if len(item) != 7 {
			t.Fatalf("Access summary contract is open: %s", response.Body.Bytes())
		}
		for _, field := range []string{
			"accessId",
			"name",
			"description",
			"status",
			"revision",
			"clientOrigin",
			"clientDialect",
		} {
			if item[field] == nil {
				t.Fatalf("Access summary omitted %q: %s", field, response.Body.Bytes())
			}
		}
	}
}

func TestAccessListKeepsAnExplicitEmptyArray(t *testing.T) {
	handler := &Handler{accessCatalog: accessViewCatalog{}}
	response := httptest.NewRecorder()
	handler.listAccesses(
		response,
		httptest.NewRequest(http.MethodGet, "/api/v1/accesses", nil),
	)

	if response.Code != http.StatusOK ||
		strings.TrimSpace(response.Body.String()) != `{"items":[]}` {
		t.Fatalf("empty Access list code=%d body=%s", response.Code, response.Body.Bytes())
	}
}

func TestAccessDetailIsCompleteButNeverDisclosesSecretReference(t *testing.T) {
	aggregate := accessViewAggregate(
		t,
		"access-detail",
		"Detail",
		access.AccessStatusEnabled,
	)
	handler := &Handler{accessCatalog: accessViewCatalog{
		aggregates: []access.Aggregate{aggregate},
	}}
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/accesses/access-detail",
		nil,
	)
	request.SetPathValue("accessId", "access-detail")
	response := httptest.NewRecorder()
	handler.getAccess(response, request)

	if response.Code != http.StatusOK ||
		response.Header().Get("ETag") != `"revision-1"` {
		t.Fatalf(
			"detail code=%d ETag=%q body=%s",
			response.Code,
			response.Header().Get("ETag"),
			response.Body.Bytes(),
		)
	}
	body := response.Body.Bytes()
	if strings.Contains(string(body), "secret://") ||
		strings.Contains(string(body), "secretRef") ||
		strings.Contains(string(body), "provider/access-detail") {
		t.Fatalf("Access detail disclosed a SecretRef: %s", body)
	}
	var detail AccessDetailResponse
	decodeClosedResponse(t, response, &detail)
	if detail.Revision != 1 ||
		detail.Access.ID != "access-detail" ||
		detail.Access.Name != "Detail" ||
		detail.AgentEndpoint.ClientOrigin != "https://access-detail.example.test:443" ||
		len(detail.Profiles) != 2 ||
		len(detail.ProviderTargets) != 2 ||
		len(detail.AccountBindings) != 1 ||
		len(detail.RouteSets) != 1 ||
		detail.AccountBindings[0].SecretHandling != AccessSecretHandlingPreserveExisting ||
		detail.RouteSets[0].Fallback != access.FallbackDisabled {
		t.Fatalf("Access detail = %+v", detail)
	}
	if detail.Profiles[1].Kind != access.EndpointProfileOriginalPassthrough ||
		detail.Profiles[1].CredentialSource !=
			access.CredentialSourceClientPassthrough ||
		detail.Profiles[1].ProcessingMode != access.ProfileProcessingObserveOnly {
		t.Fatalf("original profile detail = %+v", detail.Profiles[1])
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(body, &object); err != nil {
		t.Fatal(err)
	}
	if len(object) != 9 {
		t.Fatalf("Access detail top-level contract is open: %s", body)
	}
	var accounts []map[string]json.RawMessage
	if err := json.Unmarshal(object["accountBindings"], &accounts); err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 || len(accounts[0]) != 6 ||
		accounts[0]["secretHandling"] == nil {
		t.Fatalf("credential projection contract = %s", object["accountBindings"])
	}
}

func TestAccessDetailClassifiesInvalidMissingAndUnavailable(t *testing.T) {
	tests := []struct {
		name       string
		accessID   string
		catalog    accessViewCatalog
		wantCode   int
		wantReason ReasonCode
	}{
		{
			name:       "invalid identifier",
			accessID:   " access ",
			wantCode:   http.StatusUnprocessableEntity,
			wantReason: ReasonInvalidRequest,
		},
		{
			name:       "not found",
			accessID:   "missing-access",
			wantCode:   http.StatusNotFound,
			wantReason: ReasonAccessNotConfigured,
		},
		{
			name:       "catalog unavailable",
			accessID:   "existing-access",
			catalog:    accessViewCatalog{err: errors.New("storage unavailable")},
			wantCode:   http.StatusServiceUnavailable,
			wantReason: ReasonRuntimeUnavailable,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := &Handler{accessCatalog: test.catalog}
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.SetPathValue("accessId", test.accessID)
			response := httptest.NewRecorder()
			handler.getAccess(response, request)

			var problem struct {
				Code ReasonCode `json:"code"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
				t.Fatal(err)
			}
			if response.Code != test.wantCode || problem.Code != test.wantReason {
				t.Fatalf(
					"code=%d reason=%q body=%s",
					response.Code,
					problem.Code,
					response.Body.Bytes(),
				)
			}
		})
	}
}

type accessViewCatalog struct {
	aggregates []access.Aggregate
	err        error
}

func (catalog accessViewCatalog) ListAccesses(
	context.Context,
) ([]access.Aggregate, error) {
	if catalog.err != nil {
		return nil, catalog.err
	}
	aggregates := make([]access.Aggregate, len(catalog.aggregates))
	for index, aggregate := range catalog.aggregates {
		aggregates[index] = aggregate.Clone()
	}
	return aggregates, nil
}

func (catalog accessViewCatalog) ReadAccess(
	_ context.Context,
	accessID access.AccessID,
) (access.Aggregate, bool, error) {
	if catalog.err != nil {
		return access.Aggregate{}, false, catalog.err
	}
	for _, aggregate := range catalog.aggregates {
		if aggregate.Binding.ID == accessID {
			return aggregate.Clone(), true, nil
		}
	}
	return access.Aggregate{}, false, nil
}

func accessViewAggregate(
	t *testing.T,
	id string,
	name string,
	status access.AccessStatus,
) access.Aggregate {
	t.Helper()
	input := validAccessApplyInput()
	input.Access.ID = id
	input.Access.Name = name
	input.Access.Description = name + " description"
	input.Access.Status = string(status)
	input.Access.AgentEndpointID = id + "-endpoint"
	input.Access.DefaultRouteSetID = id + "-routes"
	input.Access.ProfileIDs = []string{id + "-profile"}
	input.Access.EgressPolicyID = id + "-egress"
	input.AgentEndpoint.ID = id + "-endpoint"
	input.AgentEndpoint.ClientOrigin = "https://" + id + ".example.test:443"
	input.Profiles[0].ID = id + "-profile"
	input.Profiles[0].TargetID = id + "-target"
	input.Profiles[0].AccountBindingIDs = []string{id + "-account"}
	input.Profiles[0].DefaultAccountBindingID = id + "-account"
	input.ProviderTargets[0].ID = id + "-target"
	input.ProviderTargets[0].ProfileID = id + "-profile"
	input.AccountBindings[0].ID = id + "-account"
	input.AccountBindings[0].ProfileID = id + "-profile"
	input.AccountBindings[0].SecretRef = "secret://provider/" + id
	input.RouteSets[0].ID = id + "-routes"
	input.RouteSets[0].CandidateProfileIDs = []string{id + "-profile"}
	input.EgressPolicy.ID = id + "-egress"
	command, err := accessapply.BuildCommand(id, input)
	if err != nil {
		t.Fatal(err)
	}
	return command.Aggregate
}

func decodeClosedResponse(
	t *testing.T,
	response *httptest.ResponseRecorder,
	output any,
) {
	t.Helper()
	decoder := json.NewDecoder(response.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		t.Fatal(err)
	}
}

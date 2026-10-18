package providertransport

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/cyberaccess"
)

func TestAccountCyberProgramsUseFrozenCredentialAndIsolatedCache(t *testing.T) {
	gate := newStartedGate(t)
	audit := &runtimeAuditRecorder{}
	transport := &runtimeTransportStub{audit: audit}
	resetResponse := func() {
		transport.response = &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"models":[{"slug":"m","available_access_programs":{"cyber":["standard","daybreak_blue"]}}]}`))}
	}
	resetResponse()
	client := newRuntimeFetchClient(t, gate, transport, audit)
	now := time.Now()
	client.clock = func() time.Time { return now }
	plan := testRequestPlanWithOrigin(t, "https://chatgpt.com")
	request := newTestRequestWithPlan(t, gate, "blue-catalog", targetFromProviderOrigin(plan.providerOrigin), http.Header{"Authorization": {"Bearer original-client"}, "Cookie": {"private-client-cookie"}}, []byte(`{"model":"m","input":"never send this prompt to discovery"}`), plan)
	request.relativePath = "backend-api/codex/responses"
	request.accountRef.CyberAccessProgram = cyberaccess.Blue
	catalog, err := client.AccountCyberPrograms(context.Background(), request)
	if err != nil || len(catalog["m"]) != 2 {
		t.Fatalf("catalog=%v err=%v", catalog, err)
	}
	outbound := transport.lastRequest()
	if outbound.Method != http.MethodGet || outbound.URL.Path != "/backend-api/codex/models" || outbound.URL.Query().Get("client_version") != "0.163.0" || outbound.Header.Get("Authorization") != "Bearer catalog-test-token" || outbound.Header.Get("Cookie") != "" || outbound.ContentLength != 0 {
		t.Fatalf("discovery did not use frozen account GET: %s %s", outbound.Method, outbound.URL)
	}
	catalog["m"][1] = "changed-by-caller"
	got, err := client.AccountCyberPrograms(context.Background(), request)
	if err != nil || got["m"][1] != "daybreak_blue" || transport.callCount() != 1 {
		t.Fatalf("cache isolation failed: %v %v count=%d", got, err, transport.callCount())
	}
	request.accountRef.CredentialEpoch++
	resetResponse()
	if _, err := client.AccountCyberPrograms(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if transport.callCount() != 2 {
		t.Fatal("credential rotation reused old capability evidence")
	}
	now = now.Add(time.Minute)
	resetResponse()
	if _, err := client.AccountCyberPrograms(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if transport.callCount() != 3 {
		t.Fatal("expired capability evidence was reused")
	}
}

func TestAccountCyberProgramsRejectUnrelatedOrDisabledScope(t *testing.T) {
	gate := newStartedGate(t)
	audit := &runtimeAuditRecorder{}
	transport := &runtimeTransportStub{audit: audit}
	client := newRuntimeFetchClient(t, gate, transport, audit)
	for _, origin := range []string{"https://provider.example", "https://chatgpt.com"} {
		plan := testRequestPlanWithOrigin(t, origin)
		request := newTestRequestWithPlan(t, gate, "disabled-blue-"+strings.TrimPrefix(origin, "https://"), targetFromProviderOrigin(plan.providerOrigin), nil, []byte(`{"model":"m"}`), plan)
		request.relativePath = "backend-api/codex/responses"
		if origin != "https://chatgpt.com" {
			request.accountRef.CyberAccessProgram = cyberaccess.Blue
		}
		if _, err := client.AccountCyberPrograms(context.Background(), request); err == nil {
			t.Fatal("accepted unrelated origin or disabled preference")
		}
	}
	if transport.callCount() != 0 {
		t.Fatal("invalid scope reached network")
	}
}

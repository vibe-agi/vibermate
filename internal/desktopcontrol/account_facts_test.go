package desktopcontrol_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
)

func TestAccountFactsRequireOwnerCapabilityAndRejectUnknownReads(t *testing.T) {
	runtime := startRuntime(t)
	defer shutdownRuntime(t, runtime)
	application, err := desktopcontrol.New(desktopcontrol.Options{
		Readiness: readyState(true), Status: runtime, Environments: runtime.Environments(), Assignments: runtime.CaptureAssignments(),
		Activities: runtime.Activities(), Contents: runtime.ExchangeContents(), Connections: runtime.ConnectionEvents(), Egress: runtime.EgressAttempts(),
		Approvals: runtime.ToolApprovals(), Endpoints: runtime.UpstreamEndpoints(), Accounts: runtime.ProviderAccounts(), AccountReads: runtime.AccountReads(),
		Offline: runtime, ManualCaptures: runtime.ManualCaptures(), Clock: desktopcontrol.SystemClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/provider-accounts/missing/account-facts"
	if application.RequiredScope(httptest.NewRequest("GET", path, nil)) != desktopcontrol.ScopeWrite {
		t.Fatal("network-backed private account inspection accepted a read-only management capability")
	}
	response := environmentRequest(t, application, "GET", path, 0, "", nil)
	if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), "provider_account") {
		t.Fatalf("account facts handler did not reject a missing account: %d", response.Code)
	}
	for _, query := range []string{"?kind=reset", "?kind=quota&account_id=other", "?kind=quota&kind=history"} {
		response := environmentRequest(t, application, "GET", path+query, 0, "", nil)
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("unbounded account operation admitted: %s status=%d", query, response.Code)
		}
	}
}

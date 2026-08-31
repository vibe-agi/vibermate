package desktopcontrol

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/accountselector"
)

func TestAccountSelectorTestProblemExplainsTheFailure(t *testing.T) {
	t.Parallel()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/code-library/account-selectors:test", strings.NewReader(`{
  "policy":{"javaScript":"selection.accountId = ;"},
  "accounts":[],
  "request":{},
  "runtime":{}
}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	(&Handler{}).testAccountSelector(response, request)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", response.Code)
	}
	var problem struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if !strings.Contains(problem.Detail, "compile JavaScript") {
		t.Fatalf("detail = %q, want compile diagnosis", problem.Detail)
	}
}

func TestAccountSelectorSampleChoosesFromCredentialFreeAccountMetadata(t *testing.T) {
	t.Parallel()
	result, err := runAccountSelectorSample(context.Background(), AccountSelectorTestInput{
		Policy: accountselector.Policy{JavaScript: `
if (runtime.user.name !== "jack" || request.requestedModel !== "model.pro") {
  throw new Error("unexpected sample");
}
selection.accountId = accounts.find(account => account.displayName === "Pro").id;
`},
		Accounts: []accountselector.Account{
			{ID: "account.basic", DisplayName: "Basic"},
			{ID: "account.pro", DisplayName: "Pro"},
		},
		Request: AccountSelectorTestRequest{
			Method: "POST", Path: "/v1/responses",
			Headers: http.Header{},
			Body:    "{}", ClientProtocol: "openai_responses", RequestedModel: "model.pro",
		},
		Runtime: AccountSelectorTestRuntime{
			UserName: "jack", HomeDirectory: "/Users/jack",
			WorkspaceRoot: "/Users/jack/Code/project", WorkspaceLabel: "project",
			OperatingSystem: "darwin", OperatingSystemVersion: "15.0",
			Architecture: "arm64", TimeZone: "Asia/Singapore",
			TurnStartedAt: time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC),
		},
	})
	if err != nil {
		t.Fatalf("runAccountSelectorSample() error = %v", err)
	}
	if result.AccountID != "account.pro" {
		t.Fatalf("AccountID = %q, want account.pro", result.AccountID)
	}
}

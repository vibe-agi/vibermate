package desktopcontrol

import (
	"bytes"
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
if (runtime.user.name !== "local-os-user" || runtime.login.username !== "alice" || request.requestedModel !== "model.pro") {
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
			UserName: "local-os-user", LoginUsername: "alice", HomeDirectory: "/Users/local-os-user",
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

func TestLoginUserAccountSelectorExampleChoosesTheDefaultSample(t *testing.T) {
	t.Parallel()
	javaScript := `const accountByLogin = {
  "alice": "account.team-a",
  "bob": "account.team-b",
};
if (!runtime.login.username) {
  throw new Error("ViberMate login is required");
}
const accountId = accountByLogin[runtime.login.username];
if (!accounts.some(function (account) { return account.id === accountId; })) {
  throw new Error("No Account is configured for this ViberMate login");
}
selection.accountId = accountId;`
	body, err := json.Marshal(AccountSelectorTestInput{
		Policy: accountselector.Policy{JavaScript: javaScript},
		Accounts: []accountselector.Account{
			{ID: "account.team-a", DisplayName: "account.team-a"},
			{ID: "account.team-b", DisplayName: "account.team-b"},
		},
		Request: AccountSelectorTestRequest{
			Method: http.MethodPost, Path: "/v1/messages",
			Headers: make(http.Header), Body: `{"model":"claude-sonnet-4-5"}`,
			ClientProtocol: "anthropic_messages", RequestedModel: "claude-sonnet-4-5",
		},
		Runtime: AccountSelectorTestRuntime{
			UserName: "local-os-user", LoginUsername: "alice", HomeDirectory: "/Users/local-os-user",
			OperatingSystem: "darwin", OperatingSystemVersion: "15.0",
			Architecture: "arm64", TimeZone: "Asia/Singapore",
			WorkspaceRoot: "/workspace/work", WorkspaceLabel: "work",
			TurnStartedAt: time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/account-selectors/actions/test",
		bytes.NewReader(body),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	(&Handler{}).testAccountSelector(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.Bytes())
	}
	var result AccountSelectorTestResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.AccountID != "account.team-a" {
		t.Fatalf("AccountID = %q, want account.team-a", result.AccountID)
	}
}

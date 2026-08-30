package desktopcontrol

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/accountselector"
)

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
			Headers: http.Header{"Content-Type": {"application/json"}},
			Body:    "{}", ClientProtocol: "openai_responses", RequestedModel: "model.pro",
		},
		Runtime: AccountSelectorTestRuntime{
			UserName: "jack", HomeDirectory: "/Users/jack",
			WorkspaceRoot: "/Users/jack/Code/project", WorkspaceLabel: "project",
			OperatingSystem: "darwin", OperatingSystemVersion: "15.0",
			Architecture: "arm64", TimeZone: "Asia/Singapore",
			TurnStartedAt: time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC), TurnIndex: 3,
		},
	})
	if err != nil {
		t.Fatalf("runAccountSelectorSample() error = %v", err)
	}
	if result.AccountID != "account.pro" {
		t.Fatalf("AccountID = %q, want account.pro", result.AccountID)
	}
}

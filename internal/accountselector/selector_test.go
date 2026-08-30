package accountselector

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestPublishedSelectorChoosesOneFrozenAccountFromReadOnlyTurn(t *testing.T) {
	program, err := Compile(Policy{JavaScript: `
if (runtime.user.name !== "jack" || runtime.workspace.label !== "vibermate") {
  throw new Error("unexpected runtime");
}
if (request.method !== "POST" || request.path !== "/v1/messages") {
  throw new Error("unexpected request");
}
let readOnly = false;
try { request.body = "forged"; } catch (_) { readOnly = true; }
if (!readOnly) throw new Error("request is mutable");
selection.accountId = request.body.includes("premium") ? accounts[1].id : accounts[0].id;
`}, DefaultLimits())
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	turn, err := program.NewTurn(TurnOptions{
		Runtime: RuntimeMetadata{
			LocalUserName: "jack", WorkspaceLabel: "vibermate",
			TurnStartedAt: time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC),
		},
		Accounts: []Account{
			{ID: "account.basic", DisplayName: "Basic"},
			{ID: "account.premium", DisplayName: "Premium"},
		},
	})
	if err != nil {
		t.Fatalf("NewTurn() error = %v", err)
	}
	request := Request{
		Method: "POST", Path: "/v1/messages",
		Headers: http.Header{"Content-Type": {"application/json"}},
		Body:    []byte(`{"tier":"premium"}`),
	}
	selection, err := turn.Select(context.Background(), request)
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if selection.AccountID != "account.premium" {
		t.Fatalf("AccountID = %q, want account.premium", selection.AccountID)
	}

	// An internal retry of the same logical Turn reuses the decision rather
	// than executing user JavaScript again.
	again, err := turn.Select(context.Background(), request)
	if err != nil || again != selection {
		t.Fatalf("second Select() = %#v, %v, want %#v", again, err, selection)
	}
	request.Body = []byte(`{"tier":"basic"}`)
	if _, err := turn.Select(context.Background(), request); !errors.Is(err, ErrExecutionFailed) {
		t.Fatalf("different second Select() error = %v, want ErrExecutionFailed", err)
	}
}

func TestPublishedSelectorFailsClosedOutsideFrozenAccounts(t *testing.T) {
	program, err := Compile(Policy{
		JavaScript: `selection.accountId = "account.foreign";`,
	}, DefaultLimits())
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	turn, err := program.NewTurn(TurnOptions{
		Accounts: []Account{{ID: "account.allowed", DisplayName: "Allowed"}},
	})
	if err != nil {
		t.Fatalf("NewTurn() error = %v", err)
	}
	_, err = turn.Select(context.Background(), Request{
		Method: "POST", Path: "/v1/responses", Body: []byte(`{}`),
	})
	if !errors.Is(err, ErrInvalidSelection) {
		t.Fatalf("Select() error = %v, want ErrInvalidSelection", err)
	}
}

func TestPublishedSelectorFailsClosedWhenAccountIDAccessorThrows(t *testing.T) {
	program, err := Compile(Policy{JavaScript: `
Object.defineProperty(selection, "accountId", {
  get() { throw new Error("do not escape JavaScript"); }
});
`}, DefaultLimits())
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	turn, err := program.NewTurn(TurnOptions{
		Accounts: []Account{{ID: "account.allowed", DisplayName: "Allowed"}},
	})
	if err != nil {
		t.Fatalf("NewTurn() error = %v", err)
	}
	_, err = turn.Select(context.Background(), Request{
		Method: "POST", Path: "/v1/responses", Body: []byte(`{}`),
	})
	if !errors.Is(err, ErrExecutionFailed) {
		t.Fatalf("Select() error = %v, want ErrExecutionFailed", err)
	}
}

package desktopcontrol

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/vibe-agi/vibermate/internal/accountselector"
)

type AccountSelectorTestInput struct {
	Policy   accountselector.Policy     `json:"policy"`
	Accounts []accountselector.Account  `json:"accounts"`
	Request  AccountSelectorTestRequest `json:"request"`
	Runtime  AccountSelectorTestRuntime `json:"runtime"`
}

type AccountSelectorTestRequest struct {
	Method         string      `json:"method"`
	Path           string      `json:"path"`
	Headers        http.Header `json:"headers"`
	Body           string      `json:"body"`
	ClientProtocol string      `json:"clientProtocol"`
	RequestedModel string      `json:"requestedModel"`
}

type AccountSelectorTestRuntime struct {
	UserName               string    `json:"userName"`
	HomeDirectory          string    `json:"homeDirectory"`
	OperatingSystem        string    `json:"operatingSystem"`
	OperatingSystemVersion string    `json:"operatingSystemVersion"`
	Architecture           string    `json:"architecture"`
	TimeZone               string    `json:"timeZone"`
	WorkspaceRoot          string    `json:"workspaceRoot"`
	WorkspaceLabel         string    `json:"workspaceLabel"`
	TurnStartedAt          time.Time `json:"turnStartedAt"`
}

type AccountSelectorTestResult struct {
	AccountID string `json:"accountId"`
}

func (handler *Handler) testAccountSelector(writer http.ResponseWriter, request *http.Request) {
	body, err := readJSONBody(request)
	if err != nil {
		writeAccountSelectorTestProblem(writer, err)
		return
	}
	if request.URL.RawQuery != "" {
		writeAccountSelectorTestProblem(writer, errors.New("query parameters are not allowed"))
		return
	}
	var input AccountSelectorTestInput
	if err := decodeStrictJSON(body, &input); err != nil {
		writeAccountSelectorTestProblem(writer, err)
		return
	}
	result, err := runAccountSelectorSample(request.Context(), input)
	if err != nil {
		writeAccountSelectorTestProblem(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func writeAccountSelectorTestProblem(writer http.ResponseWriter, err error) {
	writeCached(writer, problemResponse(problemSpec{
		status: http.StatusUnprocessableEntity,
		reason: ReasonAccountSelectorTestFailed,
		detail: err.Error(),
	}))
}

func runAccountSelectorSample(
	ctx context.Context,
	input AccountSelectorTestInput,
) (AccountSelectorTestResult, error) {
	program, err := accountselector.Compile(input.Policy, accountselector.DefaultLimits())
	if err != nil {
		return AccountSelectorTestResult{}, err
	}
	turn, err := program.NewTurn(accountselector.TurnOptions{
		Accounts: input.Accounts,
		Runtime: accountselector.RuntimeMetadata{
			LocalUserName: input.Runtime.UserName, HomeDirectory: input.Runtime.HomeDirectory,
			OperatingSystem:        input.Runtime.OperatingSystem,
			OperatingSystemVersion: input.Runtime.OperatingSystemVersion,
			Architecture:           input.Runtime.Architecture, TimeZone: input.Runtime.TimeZone,
			WorkspaceRoot: input.Runtime.WorkspaceRoot, WorkspaceLabel: input.Runtime.WorkspaceLabel,
			TurnStartedAt: input.Runtime.TurnStartedAt,
		},
	})
	if err != nil {
		return AccountSelectorTestResult{}, err
	}
	selection, err := turn.Select(ctx, accountselector.Request{
		Method: input.Request.Method, Path: input.Request.Path,
		Headers: input.Request.Headers, Body: []byte(input.Request.Body),
		ClientProtocol: input.Request.ClientProtocol, RequestedModel: input.Request.RequestedModel,
	})
	if err != nil {
		return AccountSelectorTestResult{}, err
	}
	return AccountSelectorTestResult{AccountID: selection.AccountID}, nil
}

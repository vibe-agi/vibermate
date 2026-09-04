package servercontrol_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"github.com/vibe-agi/vibermate/internal/runtimeusage"
	"github.com/vibe-agi/vibermate/internal/runtimeuser"
	"github.com/vibe-agi/vibermate/internal/servercontrol"
)

func TestRuntimeUsageHTTPRequiresAndPreservesTheCivilWindow(t *testing.T) {
	t.Parallel()
	usage := &recordingRuntimeUsage{}
	handler := newRuntimeUsersHandler(t, usage)
	target := servercontrol.RuntimeUserUsagePath + "?" + url.Values{
		"from":     {"2026-07-27"},
		"until":    {"2026-08-26"},
		"timeZone": {"Asia/Singapore"},
	}.Encode()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("GET usage status = %d, body = %s", response.Code, response.Body.String())
	}
	if usage.calls != 1 || usage.period != (runtimeusage.Period{
		From: "2026-07-27", Until: "2026-08-26", TimeZone: "Asia/Singapore",
	}) {
		t.Fatalf("usage query = %#v across %d calls", usage.period, usage.calls)
	}
	body := response.Body.Bytes()
	if !bytes.Contains(body, []byte(`"agentApiCalls":1`)) ||
		bytes.Contains(body, []byte(`"turns"`)) {
		t.Fatalf("usage response uses an inaccurate call count contract: %s", body)
	}
	var report runtimeusage.Report
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		t.Fatalf("decode usage response: %v", err)
	}
	if report.Schema != runtimeusage.ReportSchema || report.Period != usage.period {
		t.Fatalf("usage response = %#v", report)
	}
}

func TestRuntimeUsageHTTPRejectsAmbiguousOrInvalidWindows(t *testing.T) {
	t.Parallel()
	tests := []string{
		servercontrol.RuntimeUserUsagePath,
		servercontrol.RuntimeUserUsagePath +
			"?from=2026-08-01&until=2026-08-02&timeZone=UTC&extra=true",
		servercontrol.RuntimeUserUsagePath +
			"?from=2026-08-01&from=2026-08-02&until=2026-08-03&timeZone=UTC",
		servercontrol.RuntimeUserUsagePath +
			"?from=2026-08-01&until=2026-08-01&timeZone=UTC",
		servercontrol.RuntimeUserUsagePath +
			"?from=2026-01-01&until=2027-01-03&timeZone=UTC",
		servercontrol.RuntimeUserUsagePath +
			"?from=2026-08-01&until=2026-08-02&timeZone=Not%2FAZone",
	}
	for _, target := range tests {
		usage := &recordingRuntimeUsage{}
		handler := newRuntimeUsersHandler(t, usage)
		response := httptest.NewRecorder()
		handler.ServeHTTP(
			response,
			httptest.NewRequest(http.MethodGet, target, nil),
		)
		if response.Code != http.StatusUnprocessableEntity || usage.calls != 0 {
			t.Fatalf(
				"GET %q status = %d, usage calls = %d, body = %s",
				target,
				response.Code,
				usage.calls,
				response.Body.String(),
			)
		}
	}
}

type recordingRuntimeUsage struct {
	calls  int
	period runtimeusage.Period
}

func (usage *recordingRuntimeUsage) Report(
	_ context.Context,
	query runtimeusage.Query,
) (runtimeusage.Report, error) {
	usage.calls++
	usage.period = query.Period()
	return runtimeusage.Report{
		Schema: runtimeusage.ReportSchema, Period: usage.period,
		GeneratedAt: time.Date(2026, 8, 26, 1, 2, 3, 0, time.UTC),
		Days: []runtimeusage.DayUsage{{
			Date: "2026-07-27", AgentAPICalls: 1,
		}},
		Users: []runtimeusage.UserUsage{},
	}, nil
}

func newRuntimeUsersHandler(
	t *testing.T,
	usage servercontrol.RuntimeUsageReader,
) *servercontrol.RuntimeUsersHandler {
	t.Helper()
	store, err := runtimepersistence.Open(context.Background(), runtimepersistence.Options{
		DatabasePath:           filepath.Join(t.TempDir(), "runtime.sqlite"),
		BusyTimeout:            runtimepersistence.DefaultBusyTimeout,
		CommitReconcileTimeout: runtimepersistence.DefaultCommitReconcileTimeout,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown() error = %v", err)
		}
	})
	clock := serverUsageClock{now: time.Date(2026, 8, 26, 1, 2, 3, 0, time.UTC)}
	users, err := runtimeuser.New(runtimeuser.Options{
		Repository: store.RuntimeUserRepository(), Clock: clock,
		Random:          bytes.NewReader(bytes.Repeat([]byte{0x44}, 512)),
		SessionLifetime: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := servercontrol.NewRuntimeUsers(servercontrol.RuntimeUsersOptions{
		Users: users, Usage: usage, Sessions: noopRuntimeUserWebSessions{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

type noopRuntimeUserWebSessions struct{}

func (noopRuntimeUserWebSessions) IsOwner(runtimeuser.UserID) bool { return false }
func (noopRuntimeUserWebSessions) EnsureOwner(runtimeuser.UserID) (bool, error) {
	return true, nil
}
func (noopRuntimeUserWebSessions) RevokeUserSessions(runtimeuser.UserID) {}

type serverUsageClock struct{ now time.Time }

func (clock serverUsageClock) Now() time.Time { return clock.now }

package servercontrol_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
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

func TestRuntimeUserHTTPEnablesTheSameDisabledAccount(t *testing.T) {
	handler := newRuntimeUsersHandler(t, &recordingRuntimeUsage{})
	created := webRequest(t, handler, http.MethodPost, servercontrol.RuntimeUsersPath, map[string]any{
		"schema": servercontrol.RuntimeUserCreateSchema, "username": "member", "password": "test-member-password",
	}, "")
	if created.Code != http.StatusCreated {
		t.Fatalf("create = %d", created.Code)
	}
	var user servercontrol.RuntimeUserAdminView
	if err := json.Unmarshal(created.Body.Bytes(), &user); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"disabled", "active"} {
		response := webRequest(t, handler, http.MethodPatch, servercontrol.RuntimeUsersPath+"/"+user.ID, map[string]any{
			"schema": servercontrol.RuntimeUserUpdateSchema, "state": state,
		}, "")
		if response.Code != http.StatusOK {
			t.Fatalf("%s = %d", state, response.Code)
		}
		var updated servercontrol.RuntimeUserAdminView
		if err := json.Unmarshal(response.Body.Bytes(), &updated); err != nil {
			t.Fatal(err)
		}
		if updated.ID != user.ID || updated.Username != user.Username || updated.State != state {
			t.Fatalf("account changed: %+v", updated)
		}
	}
}

func TestRuntimeUserPolicyHTTPStoresEnvironmentAccessAndSoftWarnings(t *testing.T) {
	handler := newRuntimeUsersHandler(t, &recordingRuntimeUsage{})
	created := webRequest(t, handler, http.MethodPost, servercontrol.RuntimeUsersPath, map[string]any{
		"schema": servercontrol.RuntimeUserCreateSchema, "username": "member", "password": "test-member-password",
	}, "")
	var user servercontrol.RuntimeUserAdminView
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &user) != nil {
		t.Fatalf("create = %d %s", created.Code, created.Body.String())
	}
	updated := webRequest(t, handler, http.MethodPatch, servercontrol.RuntimeUsersPath+"/"+user.ID+"/policy", map[string]any{
		"schema":                   servercontrol.RuntimeUserPolicySchema,
		"allowedEnvironmentIds":    []string{"system_transparent", "team"},
		"dailyAgentApiCallWarning": 100,
		"dailyTokenWarning":        1_000_000,
	}, "")
	if updated.Code != http.StatusOK || json.Unmarshal(updated.Body.Bytes(), &user) != nil ||
		len(user.AllowedEnvironmentIDs) != 2 || user.AllowedEnvironmentIDs[0] != "system_transparent" ||
		user.DailyAgentAPICallWarning != 100 || user.DailyTokenWarning != 1_000_000 {
		t.Fatalf("policy update = %d %+v body=%s", updated.Code, user, updated.Body.String())
	}
	invalid := webRequest(t, handler, http.MethodPatch, servercontrol.RuntimeUsersPath+"/"+user.ID+"/policy", map[string]any{
		"schema":                   servercontrol.RuntimeUserPolicySchema,
		"allowedEnvironmentIds":    []string{"team", "team"},
		"dailyAgentApiCallWarning": 0,
		"dailyTokenWarning":        0,
	}, "")
	if invalid.Code != http.StatusUnprocessableEntity {
		t.Fatalf("duplicate Environment policy admitted: %d", invalid.Code)
	}
}

type recordingRuntimeUsage struct {
	calls  int
	period runtimeusage.Period
	query  runtimeusage.AggregationQuery
	err    error
}

func TestUsageHTTPPagedFiltersAndConflictStayExplicit(t *testing.T) {
	usage := &recordingRuntimeUsage{}
	handler := newRuntimeUsersHandler(t, usage)
	base := servercontrol.RuntimeUserUsagePath + "?from=2026-09-01&until=2026-09-29&timeZone=UTC"
	path := base + "&groupBy=model&limit=50&filter.project=&filter.caller=user.alice&snapshot=" + strings.Repeat("a", 64)
	response := webRequest(t, handler, http.MethodGet, path, nil, "")
	if response.Code != http.StatusOK || usage.query.Dimension != "model" || usage.query.Limit != 50 || len(usage.query.Filters) != 2 || usage.query.Filters[1].ID != "" {
		t.Fatalf("query lost scope: %+v %d", usage.query, response.Code)
	}
	for _, suffix := range []string{"&userId=override", "&filter.userId=override", "&filter.session=without-client", "&groupBy=model&limit=51", "&snapshot=bad", "&groupBy=model&groupBy=caller", "&filter.caller=a&filter.caller=b", "&knownSnapshot=" + strings.Repeat("a", 64)} {
		before := usage.calls
		response := webRequest(t, handler, http.MethodGet, base+suffix, nil, "")
		if response.Code != http.StatusUnprocessableEntity || usage.calls != before {
			t.Fatalf("invalid query reached report: %s status=%d", suffix, response.Code)
		}
	}
	usage.err = runtimeusage.ErrSnapshotChanged
	response = webRequest(t, handler, http.MethodGet, path, nil, "")
	if response.Code != http.StatusConflict || !bytes.Contains(response.Body.Bytes(), []byte("usage_snapshot_changed")) {
		t.Fatalf("snapshot mismatch hidden: %d %s", response.Code, response.Body.String())
	}
}

func TestUsageCollectionRequiresExplicitBoundedOwnerInput(t *testing.T) {
	for _, trial := range []struct {
		body   map[string]any
		status int
	}{
		{map[string]any{"enabled": true, "retentionDays": 90, "revision": 1}, http.StatusOK},
		{map[string]any{"retentionDays": 90, "revision": 1}, http.StatusUnprocessableEntity},
		{map[string]any{"enabled": true, "retentionDays": 0, "revision": 1}, http.StatusUnprocessableEntity},
		{map[string]any{"enabled": true, "retentionDays": 366, "revision": 1}, http.StatusUnprocessableEntity},
		{map[string]any{"enabled": true, "retentionDays": 90, "revision": 0}, http.StatusUnprocessableEntity},
		{map[string]any{"enabled": true, "retentionDays": 90, "revision": 1, "userId": "injected"}, http.StatusUnprocessableEntity},
	} {
		usage := &recordingRuntimeUsage{}
		handler := newRuntimeUsersHandler(t, usage)
		response := webRequest(t, handler, http.MethodPatch, servercontrol.RuntimeUserUsagePath+"/collection", trial.body, "")
		if response.Code != trial.status {
			t.Fatalf("%v -> %d %s", trial.body, response.Code, response.Body.String())
		}
		if trial.status != http.StatusOK && usage.calls != 0 {
			t.Fatal("invalid policy reached mutation")
		}
	}
}

func (usage *recordingRuntimeUsage) SetCollectionPolicy(_ context.Context, policy runtimeusage.CollectionPolicy) (runtimeusage.CollectionPolicy, error) {
	usage.calls++
	policy.Revision++
	return policy, nil
}

func (usage *recordingRuntimeUsage) Report(
	_ context.Context,
	query runtimeusage.AggregationQuery,
) (runtimeusage.Report, error) {
	usage.calls++
	usage.period = query.Period.Period()
	usage.query = query
	if usage.err != nil {
		return runtimeusage.Report{}, usage.err
	}
	return runtimeusage.Report{
		Schema: runtimeusage.ReportSchema, Period: usage.period,
		GeneratedAt: time.Date(2026, 8, 26, 1, 2, 3, 0, time.UTC),
		Days: []runtimeusage.DayUsage{{
			Date: "2026-07-27", AgentAPICalls: 1,
		}},
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

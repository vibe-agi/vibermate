package servercontrol_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/runtimeusage"
	"github.com/vibe-agi/vibermate/internal/runtimeuser"
	"github.com/vibe-agi/vibermate/internal/serveradmin"
	"github.com/vibe-agi/vibermate/internal/servercontrol"
)

type usageInspectionReader struct {
	readErr, checkErr error
	reads, checks     int
	userID            runtimeuser.UserID
}

func (r *usageInspectionReader) Report(_ context.Context, query runtimeusage.AggregationQuery) (runtimeusage.Report, error) {
	r.reads++
	return runtimeusage.Report{
		Schema: runtimeusage.ReportSchema, Period: query.Period.Period(),
		GeneratedAt: time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC),
		Days:        []runtimeusage.DayUsage{{Date: "2026-09-01", AgentAPICalls: 7}},
		Groups:      []runtimeusage.GroupUsage{{ID: "private-usage-group", Label: "Private usage"}},
	}, r.readErr
}

func (r *usageInspectionReader) ReportForUser(ctx context.Context, query runtimeusage.AggregationQuery, userID runtimeuser.UserID) (runtimeusage.Report, error) {
	r.userID = userID
	return r.Report(ctx, query)
}

func (r *usageInspectionReader) CheckReport(context.Context, runtimeusage.Report) error {
	r.checks++
	return r.checkErr
}

func (*usageInspectionReader) SetCollectionPolicy(_ context.Context, policy runtimeusage.CollectionPolicy) (runtimeusage.CollectionPolicy, error) {
	return policy, nil
}

func TestUsageHTTPInspectionChangesRemainConflictsAtEveryPublicationBoundary(t *testing.T) {
	reader := &usageInspectionReader{}
	admin := newRuntimeUsersHandler(t, reader)
	self, token, userID := newUsageInspectionSelfHandler(t, reader)
	query := "?from=2026-09-01&until=2026-09-05&timeZone=UTC"
	for _, route := range []struct {
		name, path, token, readUnavailable string
		handler                            http.Handler
	}{
		{"admin", servercontrol.RuntimeUserUsagePath, "", "runtime_user_usage_unavailable", admin},
		{"self", servercontrol.WebSelfUsagePath, token, "runtime_usage_unavailable", self},
	} {
		for _, trial := range []struct {
			name              string
			readErr, checkErr error
			status            int
		}{
			{"read snapshot changed", runtimeusage.ErrSnapshotChanged, nil, http.StatusConflict},
			{"read inspection changed", fmt.Errorf("inspection read: %w", recording.ErrInspectionChanged), nil, http.StatusConflict},
			{"final snapshot changed", nil, fmt.Errorf("final report check: %w", runtimeusage.ErrSnapshotChanged), http.StatusConflict},
			{"final inspection changed", nil, errors.Join(recording.ErrInspectionChanged, errors.New("private inspection detail")), http.StatusConflict},
			{"read inspection unavailable", recording.ErrInspectionUnavailable, nil, http.StatusServiceUnavailable},
			{"final inspection unavailable", nil, recording.ErrInspectionUnavailable, http.StatusServiceUnavailable},
			{"read failure", errors.New("private report failure"), nil, http.StatusServiceUnavailable},
			{"final failure", nil, errors.New("private check failure"), http.StatusServiceUnavailable},
		} {
			t.Run(route.name+"/"+trial.name, func(t *testing.T) {
				reader.readErr, reader.checkErr = trial.readErr, trial.checkErr
				beforeReads, beforeChecks := reader.reads, reader.checks
				response := webRequest(t, route.handler, http.MethodGet, route.path+query, nil, route.token)
				wantCode := "usage_snapshot_changed"
				if trial.status == http.StatusServiceUnavailable {
					wantCode = "runtime_usage_unavailable"
					if trial.readErr != nil {
						wantCode = route.readUnavailable
					}
				}
				var problem struct {
					Code string `json:"code"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil || response.Code != trial.status || problem.Code != wantCode {
					t.Fatalf("usage error: want %d %s, got %d %s", trial.status, wantCode, response.Code, response.Body)
				}
				wantChecks := beforeChecks
				if trial.readErr == nil {
					wantChecks++
				}
				if reader.reads != beforeReads+1 || reader.checks != wantChecks {
					t.Fatalf("wrong report boundary: reads=%d checks=%d", reader.reads-beforeReads, reader.checks-beforeChecks)
				}
				if route.name == "self" && reader.userID != userID {
					t.Fatal("inspection error lost the authenticated user scope")
				}
				for _, forbidden := range []string{"agentApiCalls", "private-usage-group", "private report failure", "private check failure", "private inspection detail"} {
					if strings.Contains(response.Body.String(), forbidden) {
						t.Fatalf("failed usage inspection exposed %q: %s", forbidden, response.Body)
					}
				}
			})
		}
	}
	before := reader.reads
	unauthorized := webRequest(t, self, http.MethodGet, servercontrol.WebSelfUsagePath+query, nil, "")
	if unauthorized.Code != http.StatusUnauthorized || reader.reads != before {
		t.Fatalf("inspection failure replaced the authentication guard: %d %s", unauthorized.Code, unauthorized.Body)
	}
	invalid := webRequest(t, self, http.MethodGet, servercontrol.WebSelfUsagePath+query+"&userId=override", nil, token)
	if invalid.Code != http.StatusUnprocessableEntity || reader.reads != before {
		t.Fatalf("inspection failure replaced query validation: %d %s", invalid.Code, invalid.Body)
	}
}

func newUsageInspectionSelfHandler(t *testing.T, usage servercontrol.WebSelfUsageProjector) (*servercontrol.WebSelfHandler, string, runtimeuser.UserID) {
	t.Helper()
	sessions, users, authority, adminDirectory := newWebSessionsHandler(t)
	member, err := users.Create(t.Context(), runtimeuser.CreateCommand{Username: "member", Password: []byte("member password 123")})
	if err != nil {
		t.Fatal(err)
	}
	key, err := serveradmin.ReadRecoveryKey(adminDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if err = authority.ClaimOwner(key, member.ID); err != nil {
		t.Fatal(err)
	}
	login := webRequest(t, sessions, http.MethodPost, servercontrol.WebSessionPath, map[string]any{
		"schema": servercontrol.WebLoginSchema, "username": "member", "password": "member password 123",
	}, "")
	session := decodeWebSession(t, login)
	handler, err := servercontrol.NewWebSelf(servercontrol.WebSelfOptions{Sessions: authority, Usage: usage})
	if err != nil {
		t.Fatal(err)
	}
	return handler, session.ReadToken, member.ID
}

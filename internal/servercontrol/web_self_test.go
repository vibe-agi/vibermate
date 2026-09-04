package servercontrol_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/runtimeusage"
	"github.com/vibe-agi/vibermate/internal/runtimeuser"
	"github.com/vibe-agi/vibermate/internal/serveradmin"
	"github.com/vibe-agi/vibermate/internal/servercontrol"
)

type scopedUsageRecorder struct {
	userID runtimeuser.UserID
	calls  int
}

func (recorder *scopedUsageRecorder) ReportForUser(
	_ context.Context,
	_ runtimeusage.Query,
	userID runtimeuser.UserID,
) (runtimeusage.Report, error) {
	recorder.userID = userID
	recorder.calls++
	return runtimeusage.Report{
		Schema:      runtimeusage.ReportSchema,
		GeneratedAt: time.Date(2026, 9, 5, 1, 2, 3, 0, time.UTC),
		Days:        []runtimeusage.DayUsage{},
		Users: []runtimeusage.UserUsage{{
			UserID: userID, Username: "alice", State: runtimeuser.StateActive,
			Days: []runtimeusage.DayUsage{}, Models: []runtimeusage.ModelUsage{},
			Contexts:      []runtimeusage.ContextUsage{},
			AgentSessions: []runtimeusage.AgentSessionUsage{},
		}},
	}, nil
}

func TestWebSelfUsageIsBoundToTheAuthenticatedMember(t *testing.T) {
	t.Parallel()

	webSessions, users, authority, adminDirectory := newWebSessionsHandler(t)
	owner, err := users.Create(context.Background(), runtimeuser.CreateCommand{
		Username: "owner", Password: []byte("owner password 123"),
	})
	if err != nil {
		t.Fatal(err)
	}
	key, err := serveradmin.ReadRecoveryKey(adminDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if err := authority.ClaimOwner(key, owner.ID); err != nil {
		t.Fatal(err)
	}
	member, err := users.Create(context.Background(), runtimeuser.CreateCommand{
		Username: "alice", Password: []byte("alice password 123"),
	})
	if err != nil {
		t.Fatal(err)
	}
	login := webRequest(t, webSessions, http.MethodPost, servercontrol.WebSessionPath, map[string]any{
		"schema": servercontrol.WebLoginSchema, "username": "alice", "password": "alice password 123",
	}, "")
	session := decodeWebSession(t, login)

	usage := &scopedUsageRecorder{}
	handler, err := servercontrol.NewWebSelf(servercontrol.WebSelfOptions{
		Sessions: authority, Usage: usage,
	})
	if err != nil {
		t.Fatal(err)
	}
	path := servercontrol.WebSelfUsagePath + "?from=2025-09-05&until=2026-09-05&timeZone=UTC"
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Set("Authorization", "Bearer "+session.ReadToken)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("self usage = %d %s", response.Code, response.Body.String())
	}
	if usage.calls != 1 || usage.userID != member.ID {
		t.Fatalf("self usage scope = %q across %d calls", usage.userID, usage.calls)
	}
	if bytes.Contains(response.Body.Bytes(), []byte("owner")) ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"username":"alice"`)) {
		t.Fatalf("self usage body = %s", response.Body.String())
	}

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, path, nil))
	if unauthorized.Code != http.StatusUnauthorized || usage.calls != 1 {
		t.Fatalf("unauthorized self usage = %d, calls = %d", unauthorized.Code, usage.calls)
	}
}

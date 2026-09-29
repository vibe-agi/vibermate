package servercontrol_test

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/runtimeuser"
	"github.com/vibe-agi/vibermate/internal/serveradmin"
	"github.com/vibe-agi/vibermate/internal/servercontrol"
)

func TestTransientUserLookupFailureDoesNotEndWebSession(t *testing.T) {
	_, users, _, _ := newWebSessionsHandler(t)
	ctx := context.Background()
	owner, err := users.Create(ctx, runtimeuser.CreateCommand{
		Username: "review-owner", Password: []byte("review-password"),
	})
	if err != nil {
		t.Fatal(err)
	}
	var lookupFailure error
	authority, err := serveradmin.Open(serveradmin.Options{
		DataDirectory: t.TempDir(), Clock: serverUsageClock{now: time.Now()},
		Random: rand.Reader, SessionLifetime: time.Hour,
		LookupUser: func(ctx context.Context, id runtimeuser.UserID) (runtimeuser.User, error) {
			if lookupFailure != nil {
				return runtimeuser.User{}, lookupFailure
			}
			return users.User(ctx, id)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.EnsureOwner(owner.ID); err != nil {
		t.Fatal(err)
	}
	handler, err := servercontrol.NewWebSessions(servercontrol.WebSessionsOptions{
		InstanceID: "review-instance", Users: users, Sessions: authority,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, failure := range []error{context.Canceled, context.DeadlineExceeded, errors.New("temporary database read failure")} {
		t.Run(failure.Error(), func(t *testing.T) {
			session := decodeWebSession(t, webRequest(t, handler, http.MethodPost,
				servercontrol.WebSessionPath, map[string]any{
					"schema": servercontrol.WebLoginSchema, "username": owner.Username,
					"password": "review-password",
				}, ""))
			lookupFailure = failure
			unavailable := webRequest(t, handler, http.MethodGet,
				servercontrol.WebCurrentSessionPath, nil, session.ReadToken)
			lookupFailure = nil
			if unavailable.Code != http.StatusServiceUnavailable {
				t.Errorf("temporary lookup failure = HTTP %d, want 503", unavailable.Code)
			}
			recovered := webRequest(t, handler, http.MethodGet,
				servercontrol.WebCurrentSessionPath, nil, session.ReadToken)
			if recovered.Code != http.StatusOK {
				t.Errorf("same session after recovery = HTTP %d, want 200", recovered.Code)
			}
			logout := webRequest(t, handler, http.MethodDelete,
				servercontrol.WebCurrentSessionPath, nil, session.WriteToken)
			if logout.Code != http.StatusNoContent {
				t.Errorf("paired write token after recovery = HTTP %d, want 204", logout.Code)
			}
		})
	}
}

type pausedCredentialCheck struct {
	*runtimeuser.Manager
	verified chan struct{}
	resume   chan struct{}
}

func (users *pausedCredentialCheck) VerifyCredentials(ctx context.Context, name string, password []byte) (runtimeuser.User, error) {
	user, err := users.Manager.VerifyCredentials(ctx, name, password)
	if err == nil {
		close(users.verified)
		<-users.resume
	}
	return user, err
}

func TestRevocationFencesAnInFlightWebLogin(t *testing.T) {
	for _, action := range []string{"disable-member", "reset-owner-password"} {
		t.Run(action, func(t *testing.T) {
			_, users, authority, _ := newWebSessionsHandler(t)
			owner, err := users.Create(context.Background(), runtimeuser.CreateCommand{Username: "review-owner", Password: []byte("review-old-password")})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = authority.EnsureOwner(owner.ID); err != nil {
				t.Fatal(err)
			}
			target := owner
			if action == "disable-member" {
				target, err = users.Create(context.Background(), runtimeuser.CreateCommand{Username: "review-member", Password: []byte("review-old-password")})
				if err != nil {
					t.Fatal(err)
				}
			}
			paused := &pausedCredentialCheck{Manager: users, verified: make(chan struct{}), resume: make(chan struct{})}
			handler, err := servercontrol.NewWebSessions(servercontrol.WebSessionsOptions{InstanceID: "review-instance", Users: paused, Sessions: authority})
			if err != nil {
				t.Fatal(err)
			}
			completed := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				completed <- webRequest(t, handler, http.MethodPost, servercontrol.WebSessionPath, map[string]any{"schema": servercontrol.WebLoginSchema, "username": target.Username, "password": "review-old-password"}, "")
			}()
			<-paused.verified
			if action == "disable-member" {
				_, err = users.Disable(context.Background(), target.ID)
			} else {
				_, err = users.ReplacePassword(context.Background(), target.ID, []byte("review-new-password"))
			}
			if err != nil {
				close(paused.resume)
				t.Fatal(err)
			}
			authority.RevokeUserSessions(target.ID)
			close(paused.resume)
			response := <-completed
			if response.Code == http.StatusCreated {
				session := decodeWebSession(t, response)
				principal, authErr := authority.Authenticate(context.Background(), session.ReadToken, serveradmin.ScopeRead)
				t.Fatalf("%s completed before mint; old credentials still issued HTTP %d, auth error=%v, role=%s", action, response.Code, authErr, principal.Role)
			}
		})
	}
}

func TestAbandonedWebSessionsDoNotBlockAnotherLogin(t *testing.T) {
	_, users, authority, _ := newWebSessionsHandler(t)
	owner, err := users.Create(context.Background(), runtimeuser.CreateCommand{Username: "review-owner", Password: []byte("review-password")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.EnsureOwner(owner.ID); err != nil {
		t.Fatal(err)
	}
	member, err := users.Create(context.Background(), runtimeuser.CreateCommand{Username: "review-member", Password: []byte("review-password")})
	if err != nil {
		t.Fatal(err)
	}
	memberSession, err := authority.LoginUser(context.Background(), member)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for count < 129 {
		if _, err := authority.LoginUser(context.Background(), owner); err != nil {
			t.Fatalf("abandoned sessions blocked login %d: %v", count+1, err)
		}
		count++
	}
	if _, err := authority.Authenticate(context.Background(), memberSession.ReadToken.Value(), serveradmin.ScopeRead); err != nil {
		t.Fatal("another person's session was evicted")
	}
}

func TestWebSessionRevalidatesDurableCredentialRevision(t *testing.T) {
	_, users, authority, _ := newWebSessionsHandler(t)
	ctx := context.Background()
	owner, err := users.Create(ctx, runtimeuser.CreateCommand{Username: "review-owner", Password: []byte("review-password")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.EnsureOwner(owner.ID); err != nil {
		t.Fatal(err)
	}
	session, err := authority.LoginUser(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := users.ReplacePassword(ctx, owner.ID, []byte("new-review-password")); err != nil {
		t.Fatal(err)
	}
	// No in-memory revoke is needed, including a reset between lookup and mint.
	if _, err := authority.Authenticate(ctx, session.ReadToken.Value(), serveradmin.ScopeRead); err == nil {
		t.Fatal("old credential revision remained authorized")
	}
	if _, err := authority.Authenticate(ctx, session.WriteToken.Value(), serveradmin.ScopeWrite); err == nil {
		t.Fatal("paired write authority remained authorized")
	}
}

package capturecontrol_test

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
)

func TestLauncherAuthorityRotatesWithoutAuthenticationGapAndRevokes(t *testing.T) {
	t.Parallel()

	clock := &authorityClock{
		now: time.Date(2026, 7, 29, 8, 0, 0, 0, time.UTC),
	}
	first := authorityCapability(0x11)
	second := authorityCapability(0x22)
	authority, err := capturecontrol.NewLauncherAuthority(
		capturecontrol.LauncherGrant{
			Token:     first,
			ExpiresAt: clock.now.Add(time.Minute),
		},
		clock,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !authorizeLauncher(authority, first) {
		t.Fatal("initial launcher capability was rejected")
	}
	rotation, err := authority.Prepare(capturecontrol.LauncherGrant{
		Token:     second,
		ExpiresAt: clock.now.Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !authorizeLauncher(authority, first) ||
		!authorizeLauncher(authority, second) {
		t.Fatal("prepared rotation introduced an authentication gap")
	}
	if err := rotation.Commit(); err != nil {
		t.Fatal(err)
	}
	if authorizeLauncher(authority, first) ||
		!authorizeLauncher(authority, second) {
		t.Fatal("committed rotation retained the previous capability")
	}
	authority.Revoke()
	if authorizeLauncher(authority, second) {
		t.Fatal("revoked launcher authority accepted a capability")
	}
}

func TestLauncherAuthorityAbortRetainsOnlyPreviousCapability(t *testing.T) {
	t.Parallel()

	clock := &authorityClock{now: time.Now().UTC()}
	first := authorityCapability(0x31)
	second := authorityCapability(0x32)
	authority, err := capturecontrol.NewLauncherAuthority(
		capturecontrol.LauncherGrant{
			Token:     first,
			ExpiresAt: clock.now.Add(time.Minute),
		},
		clock,
	)
	if err != nil {
		t.Fatal(err)
	}
	rotation, err := authority.Prepare(capturecontrol.LauncherGrant{
		Token:     second,
		ExpiresAt: clock.now.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := rotation.Abort(); err != nil {
		t.Fatal(err)
	}
	if !authorizeLauncher(authority, first) ||
		authorizeLauncher(authority, second) {
		t.Fatal("aborted rotation changed the active capability")
	}
}

func authorizeLauncher(
	authority *capturecontrol.LauncherAuthority,
	token string,
) bool {
	request, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1/", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	return authority.Authorize(request)
}

func authorityCapability(fill byte) string {
	return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{fill}, 32))
}

type authorityClock struct {
	now time.Time
}

func (clock *authorityClock) Now() time.Time {
	return clock.now
}

package desktopbootstrap_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/desktopbootstrap"
)

func TestBootstrapExchangesNonceExactlyOnceWithoutReflectingIt(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 29, 8, 0, 0, 0, time.UTC)
	nonce := bootstrapCapability(0x11)
	session := desktopbootstrap.Session{
		Schema:     desktopbootstrap.SessionSchema,
		BaseURL:    "http://127.0.0.1:43127",
		ReadToken:  bootstrapCapability(0x22),
		WriteToken: bootstrapCapability(0x33),
		InstanceID: "instance",
		ExpiresAt:  now.Add(time.Hour),
	}
	authority, err := desktopbootstrap.New(desktopbootstrap.Grant{
		Nonce:     nonce,
		ExpiresAt: now.Add(time.Minute),
		Session:   session,
	}, bootstrapClock{now: now})
	if err != nil {
		t.Fatal(err)
	}
	first := bootstrapRequest(authority, nonce)
	if first.Code != http.StatusCreated ||
		bytes.Contains(first.Body.Bytes(), []byte(nonce)) {
		t.Fatalf("first exchange status=%d body=%s", first.Code, first.Body.Bytes())
	}
	var received desktopbootstrap.Session
	if err := json.NewDecoder(first.Body).Decode(&received); err != nil {
		t.Fatal(err)
	}
	if received != session {
		t.Fatalf("session = %+v, want %+v", received, session)
	}
	second := bootstrapRequest(authority, nonce)
	if second.Code != http.StatusUnauthorized ||
		bytes.Contains(second.Body.Bytes(), []byte(nonce)) {
		t.Fatalf("replay status=%d body=%s", second.Code, second.Body.Bytes())
	}
}

func TestDescriptorClonePreservesExplicitEmptyWireCollections(t *testing.T) {
	t.Parallel()

	descriptor := desktopbootstrap.Descriptor{
		Schema:         desktopbootstrap.DescriptorSchema,
		InstanceID:     "instance",
		ProcessID:      41,
		BaseURL:        "http://127.0.0.1:43127",
		APIVersions:    []string{"v1"},
		EventVersions:  []string{},
		BootstrapNonce: bootstrapCapability(0x44),
	}
	cloned := descriptor.Clone()
	descriptor.APIVersions[0] = "mutated"
	payload, err := json.Marshal(cloned)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(payload, []byte(`"apiVersions":["v1"]`)) ||
		!bytes.Contains(payload, []byte(`"eventVersions":[]`)) {
		t.Fatalf("descriptor wire payload = %s", payload)
	}
}

func bootstrapRequest(
	handler http.Handler,
	nonce string,
) *httptest.ResponseRecorder {
	request := httptest.NewRequest(
		http.MethodPost,
		"http://127.0.0.1:43127/api/v1/auth/sessions",
		nil,
	)
	request.Header.Set("Authorization", "Bootstrap "+nonce)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func bootstrapCapability(fill byte) string {
	return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{fill}, 32))
}

type bootstrapClock struct {
	now time.Time
}

func (clock bootstrapClock) Now() time.Time {
	return clock.now
}
